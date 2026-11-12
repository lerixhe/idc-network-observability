package main

import (
	"fmt"
	"strings"
	"testing"
)

// ---- 构造小数据集 ----

func dev(name, serial, ip, mac, lifecycle string) Device {
	return Device{
		Hostname: name, Serial: serial, MgmtIP: ip, MgmtMAC: mac,
		Lifecycle: lifecycle, Model: "N9K-C93180YC", Site: "idc-a", Rack: "R-" + name,
	}
}

func port(device, name string, ifIndex int, mgmtIP string, neighbors ...LldpNeighbor) LldpPort {
	return LldpPort{Device: device, Port: name, IfIndex: ifIndex, MgmtIP: mgmtIP, Neighbors: neighbors}
}

func nb(device, name string) LldpNeighbor {
	return LldpNeighbor{Device: device, Port: name}
}

func lldpOf(ports ...LldpPort) *LldpSnapshot {
	return &LldpSnapshot{CollectedAt: "2026-05-12T09:12:00Z", Ports: ports}
}

func arpOf(entries ...ArpEntry) *ArpSnapshot {
	return &ArpSnapshot{CollectedAt: "2026-05-12T09:13:00Z", Gateway: "gw", Entries: entries}
}

func reconcile(t *testing.T, l *Ledger, lldp *LldpSnapshot, arp *ArpSnapshot) Report {
	t.Helper()
	if err := l.Validate(); err != nil {
		t.Fatalf("测试数据本身不合法: %v", err)
	}
	r, err := Reconcile(l, lldp, arp)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func findDiff(diffs []Diff, target string) (Diff, bool) {
	for _, d := range diffs {
		if d.Target == target {
			return d, true
		}
	}
	return Diff{}, false
}

// ---- 样本上的三类差异 ----

func loadSamples(t *testing.T) (*Ledger, *LldpSnapshot, *ArpSnapshot) {
	t.Helper()
	ledger, err := loadLedger("samples/cmdb.json")
	if err != nil {
		t.Fatal(err)
	}
	lldp, err := loadLLDP("samples/lldp.json")
	if err != nil {
		t.Fatal(err)
	}
	arp, err := loadARP("samples/arp.json")
	if err != nil {
		t.Fatal(err)
	}
	return ledger, lldp, arp
}

func TestSampleZombies(t *testing.T) {
	ledger, lldp, arp := loadSamples(t)
	r := reconcile(t, ledger, lldp, arp)

	if len(r.Zombies) != 3 {
		t.Fatalf("僵尸应当 3 条，实际 %d：%+v", len(r.Zombies), r.Zombies)
	}
	cases := []struct {
		target string
		conf   string
		action string
	}{
		{"agg-core-01", ConfHigh, ActRetire},
		{"tor-3f-07", ConfHigh, ActRetire},
		{"tor-a3f-06", ConfMedium, ActRetireAfterUnlink},
	}
	for _, c := range cases {
		d, ok := findDiff(r.Zombies, c.target)
		if !ok {
			t.Errorf("僵尸清单里没有 %s", c.target)
			continue
		}
		if d.Confidence != c.conf {
			t.Errorf("%s 置信度应当是 %s，实际 %s", c.target, c.conf, d.Confidence)
		}
		if d.Action != c.action {
			t.Errorf("%s 的纠正建议应当是 %s，实际 %s", c.target, c.action, d.Action)
		}
	}

	// agg-core-01 的 ARP 里 IP 还在，但 MAC 换了一个人：按 IP 判存活会漏掉它。
	d, _ := findDiff(r.Zombies, "agg-core-01")
	if !hasEvidence(d, "00:1b:21:aa:99:51") {
		t.Errorf("agg-core-01 的判据里应当点出 ARP 的 MAC 与台账不符: %v", d.Evidence)
	}
}

func TestSampleGhosts(t *testing.T) {
	ledger, lldp, arp := loadSamples(t)
	r := reconcile(t, ledger, lldp, arp)

	if len(r.Ghosts) != 2 {
		t.Fatalf("幽灵应当 2 条，实际 %d：%+v", len(r.Ghosts), r.Ghosts)
	}
	hi, ok := findDiff(r.Ghosts, "tor-a3f-04")
	if !ok || hi.Confidence != ConfHigh || hi.Action != ActRegister {
		t.Errorf("tor-a3f-04 应当是 high/register，实际 %+v", hi)
	}
	if hi.Fix["mgmt_ip"] != "10.20.3.14" {
		t.Errorf("tor-a3f-04 的补丁应当带上 LLDP 里的管理地址，实际 %v", hi.Fix)
	}
	mid, ok := findDiff(r.Ghosts, "tor-a3f-05")
	if !ok || mid.Confidence != ConfMedium || mid.Action != ActRegisterVerify {
		t.Errorf("tor-a3f-05 只在一个来源里出现，应当 medium/register_verify，实际 %+v", mid)
	}
}

func TestSampleMismatches(t *testing.T) {
	ledger, lldp, arp := loadSamples(t)
	r := reconcile(t, ledger, lldp, arp)

	if len(r.Mismatches) != 4 {
		t.Fatalf("错挂应当 4 条，实际 %d：%+v", len(r.Mismatches), r.Mismatches)
	}
	cases := []struct {
		target string
		conf   string
		action string
		fix    map[string]string
	}{
		{"spine-01#Ethernet1/5", ConfHigh, ActFixPeer,
			map[string]string{"peer_device": "tor-a3f-01", "peer_port": "Ethernet1/1"}},
		{"spine-02#Ethernet1/7", ConfHigh, ActFixPeer,
			map[string]string{"peer_device": "tor-a3f-03", "peer_port": "Ethernet1/49"}},
		{"tor-a3f-02#Ethernet1/2", ConfMedium, ActFixPeer,
			map[string]string{"peer_device": "spine-02", "peer_port": "Ethernet1/6"}},
		{"tor-a3f-03#Ethernet1/1", ConfHigh, ActFixLocalPort,
			map[string]string{"port": "Ethernet1/49"}},
	}
	for _, c := range cases {
		d, ok := findDiff(r.Mismatches, c.target)
		if !ok {
			t.Errorf("错挂清单里没有 %s", c.target)
			continue
		}
		if d.Confidence != c.conf {
			t.Errorf("%s 置信度应当是 %s，实际 %s", c.target, c.conf, d.Confidence)
		}
		if d.Action != c.action {
			t.Errorf("%s 的动作应当是 %s，实际 %s", c.target, c.action, d.Action)
		}
		for k, v := range c.fix {
			if d.Fix[k] != v {
				t.Errorf("%s 的修正建议 %s 应当是 %q，实际 %q", c.target, k, v, d.Fix[k])
			}
		}
	}
}

// 台账两端一致、LLDP 双向也一致的线，一条都不许报。
func TestSampleCleanLinksAreNotReported(t *testing.T) {
	ledger, lldp, arp := loadSamples(t)
	r := reconcile(t, ledger, lldp, arp)

	clean := []string{
		"spine-01#Ethernet1/6", "tor-a3f-02#Ethernet1/1",
		"spine-01#Ethernet1/49", "border-01#Ethernet1/1",
		"spine-02#Ethernet1/5", "tor-a3f-01#Ethernet1/2",
		"tor-a3f-01#Ethernet1/1",
	}
	for _, target := range clean {
		if d, ok := findDiff(r.Mismatches, target); ok {
			t.Errorf("%s 是对得上的线，不该报错挂: %+v", target, d)
		}
	}

	// 下电设备的下联口：LLDP 里彻底消失，但这不能当成错挂，
	// 否则一台僵尸会连带刷出十几条假差异。
	for _, target := range []string{"spine-02#Ethernet1/8", "tor-a3f-06#Ethernet1/1"} {
		if d, ok := findDiff(r.Mismatches, target); ok {
			t.Errorf("%s 只是现网没有观测，不该判错挂: %+v", target, d)
		}
	}
}

func TestSampleIdentity(t *testing.T) {
	ledger, lldp, arp := loadSamples(t)
	r := reconcile(t, ledger, lldp, arp)

	if len(r.Renames) != 1 {
		t.Fatalf("改名应当 1 条，实际 %d：%+v", len(r.Renames), r.Renames)
	}
	d := r.Renames[0]
	if d.Target != "tor-3f-02" || d.Confidence != ConfHigh || d.Action != ActAlias {
		t.Errorf("改名条目不对: %+v", d)
	}
	if d.Fix["alias_of"] != "tor-a3f-02" {
		t.Errorf("alias_of 应当指向 tor-a3f-02，实际 %v", d.Fix)
	}
	if got := r.Primary["tor-3f-02"]; got != "tor-a3f-02" {
		t.Errorf("归并应当把旧名映射到现名，实际 %q", got)
	}
	// 旧名被归并之后不许再出现在僵尸清单里。
	if _, ok := findDiff(r.Zombies, "tor-3f-02"); ok {
		t.Error("旧名是同一台机器改名留下的，不该算僵尸")
	}

	if len(r.Reindexes) != 2 {
		t.Fatalf("端口重排应当 2 条，实际 %d：%+v", len(r.Reindexes), r.Reindexes)
	}
	for _, want := range []struct{ target, ifIndex string }{
		{"tor-a3f-02#Ethernet1/1", "51"},
		{"tor-a3f-02#Ethernet1/2", "52"},
	} {
		d, ok := findDiff(r.Reindexes, want.target)
		if !ok {
			t.Errorf("端口重排清单里没有 %s", want.target)
			continue
		}
		if d.Fix["if_index"] != want.ifIndex {
			t.Errorf("%s 的 if_index 应当是 %s，实际 %v", want.target, want.ifIndex, d.Fix)
		}
	}
}

func TestSamplePatches(t *testing.T) {
	ledger, lldp, arp := loadSamples(t)
	r := reconcile(t, ledger, lldp, arp)

	if len(r.Patches) != 18 {
		t.Fatalf("补丁应当 18 条，实际 %d", len(r.Patches))
	}
	counts := map[string]int{}
	for i, p := range r.Patches {
		counts[p.Op]++
		if want := fmt.Sprintf("P%02d", i+1); p.ID != want {
			t.Errorf("第 %d 条补丁 ID 应当是 %s，实际 %s", i+1, want, p.ID)
		}
		if p.DiffID == "" {
			t.Errorf("补丁 %s 没有回指差异单", p.ID)
		}
		if p.Confidence == "" {
			t.Errorf("补丁 %s 没有置信度", p.ID)
		}
	}
	want := map[string]int{
		OpDeviceLifecycle: 4, // 3 台僵尸 + 旧名
		OpDeviceAlias:     1,
		OpDeviceCreate:    2,
		OpLinkDelete:      2,
		OpLinkCreate:      2,
		OpLinkPeer:        3,
		OpLinkLocalPort:   1,
		OpLinkIfIndex:     3, // 重排 2 条 + 改端口名之后的那一条
	}
	for op, n := range want {
		if counts[op] != n {
			t.Errorf("补丁 %s 应当 %d 条，实际 %d", op, n, counts[op])
		}
	}
}

func hasEvidence(d Diff, sub string) bool {
	for _, e := range d.Evidence {
		if strings.Contains(e, sub) {
			return true
		}
	}
	return false
}

// ---- 判据的边界 ----

// 管理面还活着、只是 LLDP 没覆盖到的设备，不能判僵尸。
func TestZombieSkippedWhenMgmtStillAlive(t *testing.T) {
	ledger := &Ledger{Devices: []Device{
		dev("sw-alive", "S1", "10.0.0.1", "aa:aa:aa:aa:aa:01", LifecycleActive),
		dev("sw-quiet", "S2", "10.0.0.2", "aa:aa:aa:aa:aa:02", LifecycleActive),
	}}
	lldp := lldpOf(port("sw-alive", "Ethernet1/1", 1, "10.0.0.1"))
	arp := arpOf(ArpEntry{IP: "10.0.0.2", MAC: "aa:aa:aa:aa:aa:02"})

	r := reconcile(t, ledger, lldp, arp)
	if len(r.Zombies) != 0 {
		t.Errorf("ARP 里 IP 与 MAC 都对得上，说明设备活着，不该判僵尸: %+v", r.Zombies)
	}
	if r.Stats.LldpBlind != 1 {
		t.Errorf("应当记 1 台 LLDP 盲区，实际 %d", r.Stats.LldpBlind)
	}
}

// 同一台设备 MAC 对不上，说明那个管理地址已经换人了，仍然判僵尸。
func TestZombieWhenMgmtIPReused(t *testing.T) {
	ledger := &Ledger{Devices: []Device{
		dev("sw-gone", "S1", "10.0.0.9", "aa:aa:aa:aa:aa:09", LifecycleActive),
	}}
	lldp := lldpOf(port("sw-other", "Ethernet1/1", 1, "10.0.0.1"))
	arp := arpOf(ArpEntry{IP: "10.0.0.9", MAC: "bb:bb:bb:bb:bb:09"})

	r := reconcile(t, ledger, lldp, arp)
	if len(r.Zombies) != 1 {
		t.Fatalf("MAC 对不上就是另一台机器在用这个 IP，应当判僵尸: %+v", r.Zombies)
	}
	if r.Zombies[0].Confidence != ConfHigh {
		t.Errorf("管理地址被复用算强证据，置信度应当 high，实际 %s", r.Zombies[0].Confidence)
	}
}

func TestZombieSkippedWhenStaging(t *testing.T) {
	ledger := &Ledger{Devices: []Device{
		dev("sw-new", "S1", "10.0.0.9", "aa:aa:aa:aa:aa:09", LifecycleStaging),
	}}
	r := reconcile(t, ledger, lldpOf(port("sw-other", "Ethernet1/1", 1, "10.0.0.1")), arpOf())
	if len(r.Zombies) != 0 {
		t.Error("staging 是还没上线的机器，现网看不见很正常")
	}
}

func TestZombieRetireAfterUnlink(t *testing.T) {
	ledger := &Ledger{
		Devices: []Device{
			dev("sw-a", "S1", "10.0.0.1", "aa:aa:aa:aa:aa:01", LifecycleActive),
			dev("sw-gone", "S2", "10.0.0.2", "aa:aa:aa:aa:aa:02", LifecycleActive),
		},
		Links: []PortDecl{
			{Device: "sw-a", Port: "Ethernet1/1", IfIndex: 1, PeerDevice: "sw-gone", PeerPort: "Ethernet1/1"},
		},
	}
	lldp := lldpOf(port("sw-a", "Ethernet1/1", 1, "10.0.0.1"))

	r := reconcile(t, ledger, lldp, arpOf())
	d, ok := findDiff(r.Zombies, "sw-gone")
	if !ok {
		t.Fatalf("应当判一台僵尸: %+v", r.Zombies)
	}
	if d.Confidence != ConfMedium || d.Action != ActRetireAfterUnlink {
		t.Errorf("还有端口声明指向它，应当 medium/retire_after_unlink，实际 %+v", d)
	}
	if !hasEvidence(d, "sw-a#Ethernet1/1") {
		t.Errorf("判据里应当点出是谁还在引用它: %v", d.Evidence)
	}
}

// 台账两端互相矛盾、现网又没有任何观测，只能给 low，交给人工。
func TestMismatchLowWhenOnlyLedgerContradicts(t *testing.T) {
	ledger := &Ledger{
		Devices: []Device{
			dev("sw-a", "S1", "10.0.0.1", "aa:aa:aa:aa:aa:01", LifecycleActive),
			dev("sw-b", "S2", "10.0.0.2", "aa:aa:aa:aa:aa:02", LifecycleActive),
		},
		Links: []PortDecl{
			{Device: "sw-a", Port: "Ethernet1/1", IfIndex: 1, PeerDevice: "sw-b", PeerPort: "Ethernet1/3"},
			{Device: "sw-b", Port: "Ethernet1/3", IfIndex: 3, PeerDevice: "sw-a", PeerPort: "Ethernet1/5"},
		},
	}
	lldp := lldpOf(port("sw-a", "Ethernet1/1", 1, ""), port("sw-b", "Ethernet1/3", 3, ""))

	r := reconcile(t, ledger, lldp, arpOf())
	if len(r.Mismatches) != 1 {
		t.Fatalf("只有一端矛盾、另一端对不上号，应当只报 1 条，实际 %d：%+v",
			len(r.Mismatches), r.Mismatches)
	}
	d := r.Mismatches[0]
	if d.Target != "sw-a#Ethernet1/1" || d.Confidence != ConfLow {
		t.Errorf("现网无观测的内部矛盾只能给 low: %+v", d)
	}
}

// 组内一个名字都没在现网出现：分不清改名还是下架，整组跳过僵尸判定。
func TestIdentityAmbiguousWhenNobodyInLLDP(t *testing.T) {
	ledger := &Ledger{Devices: []Device{
		dev("sw-old", "SAME", "10.0.0.9", "aa:aa:aa:aa:aa:09", LifecycleActive),
		dev("sw-new", "SAME", "10.0.0.8", "aa:aa:aa:aa:aa:08", LifecycleActive),
	}}
	r := reconcile(t, ledger, lldpOf(port("sw-other", "Ethernet1/1", 1, "")), arpOf())

	if len(r.Renames) != 1 {
		t.Fatalf("应当报 1 条身份问题，实际 %d", len(r.Renames))
	}
	if r.Renames[0].Action != ActManualMerge || r.Renames[0].Confidence != ConfLow {
		t.Errorf("证据不足时只能给 manual_merge/low: %+v", r.Renames[0])
	}
	if len(r.Zombies) != 0 {
		t.Errorf("整组跳过僵尸判定，不该重复报同一台机器: %+v", r.Zombies)
	}
}

// 组内两个名字都在现网出现：更像两条记录抄了同一个 serial，同样交人工。
func TestIdentityAmbiguousWhenTwoNamesInLLDP(t *testing.T) {
	ledger := &Ledger{Devices: []Device{
		dev("sw-a", "SAME", "10.0.0.1", "aa:aa:aa:aa:aa:01", LifecycleActive),
		dev("sw-b", "SAME", "10.0.0.2", "aa:aa:aa:aa:aa:02", LifecycleActive),
	}}
	lldp := lldpOf(
		port("sw-a", "Ethernet1/1", 1, "10.0.0.1"),
		port("sw-b", "Ethernet1/1", 1, "10.0.0.2"),
	)
	r := reconcile(t, ledger, lldp, arpOf())

	if len(r.Renames) != 1 || r.Renames[0].Action != ActManualMerge {
		t.Fatalf("两个名字都在现网，应当报 manual_merge: %+v", r.Renames)
	}
	if len(r.Zombies) != 0 {
		t.Errorf("两台都在现网，不该有僵尸: %+v", r.Zombies)
	}
}

// serial 为空的记录不参与合并：宁可不合，不能猜。
func TestIdentityIgnoresEmptySerial(t *testing.T) {
	ledger := &Ledger{Devices: []Device{
		dev("sw-a", "", "10.0.0.1", "aa:aa:aa:aa:aa:01", LifecycleActive),
		dev("sw-b", "", "10.0.0.2", "aa:aa:aa:aa:aa:02", LifecycleActive),
	}}
	r := reconcile(t, ledger, lldpOf(port("sw-other", "Ethernet1/1", 1, "")), arpOf())

	if len(r.Renames) != 0 {
		t.Errorf("serial 为空证明不了是同一台，不该合并: %+v", r.Renames)
	}
	if len(r.Zombies) != 2 {
		t.Errorf("两台都该各自判僵尸，实际 %d", len(r.Zombies))
	}
}

// if_index 在台账里填 0 表示没登记，不能报成重排。
func TestReindexIgnoresUnregisteredIfIndex(t *testing.T) {
	ledger := &Ledger{
		Devices: []Device{
			dev("sw-a", "S1", "10.0.0.1", "aa:aa:aa:aa:aa:01", LifecycleActive),
			dev("sw-b", "S2", "10.0.0.2", "aa:aa:aa:aa:aa:02", LifecycleActive),
		},
		Links: []PortDecl{
			{Device: "sw-a", Port: "Ethernet1/1", IfIndex: 0, PeerDevice: "sw-b", PeerPort: "Ethernet1/1"},
			{Device: "sw-b", Port: "Ethernet1/1", IfIndex: 1, PeerDevice: "sw-a", PeerPort: "Ethernet1/1"},
		},
	}
	lldp := lldpOf(
		port("sw-a", "Ethernet1/1", 7, "10.0.0.1", nb("sw-b", "Ethernet1/1")),
		port("sw-b", "Ethernet1/1", 1, "10.0.0.2", nb("sw-a", "Ethernet1/1")),
	)
	r := reconcile(t, ledger, lldp, arpOf())

	if len(r.Reindexes) != 0 {
		t.Errorf("台账没登记 if_index 就不是重排: %+v", r.Reindexes)
	}
	if len(r.Mismatches) != 0 {
		t.Errorf("两端台账一致、LLDP 双向一致，不该报错挂: %+v", r.Mismatches)
	}
}

// 声明指向台账里不存在的设备：跳过对账，但要计数，不能悄悄当没这条线。
func TestDanglingDeclarationCounted(t *testing.T) {
	ledger := &Ledger{
		Devices: []Device{dev("sw-a", "S1", "10.0.0.1", "aa:aa:aa:aa:aa:01", LifecycleActive)},
		Links: []PortDecl{
			{Device: "sw-a", Port: "Ethernet1/1", IfIndex: 1, PeerDevice: "sw-void", PeerPort: "Ethernet1/1"},
		},
	}
	r := reconcile(t, ledger, lldpOf(port("sw-a", "Ethernet1/1", 1, "10.0.0.1")), arpOf())
	if r.Stats.Dangling != 1 {
		t.Errorf("应当记 1 条悬空声明，实际 %d", r.Stats.Dangling)
	}
	if len(r.Mismatches) != 0 {
		t.Errorf("悬空声明不参与错挂判定: %+v", r.Mismatches)
	}
}
