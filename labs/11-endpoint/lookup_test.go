package main

import (
	"strings"
	"testing"
	"time"
)

func hasTrap(r Result, trap string) bool {
	for _, t := range r.Traps {
		if t == trap {
			return true
		}
	}
	return false
}

func evidenceHas(r Result, sub string) bool {
	for _, e := range r.Evidence {
		if strings.Contains(e, sub) {
			return true
		}
	}
	return false
}

// 三张表接力走通的那条路。结论要落到具体机柜 U 位，且不该命中任何陷阱。
func TestLocateNormalHit(t *testing.T) {
	r := Locate(loadSamples(t), "10.32.7.31", Options{})

	if r.Verdict != VerdictLocated {
		t.Fatalf("verdict = %s，想要 %s", r.Verdict, VerdictLocated)
	}
	if r.MAC != "3c:52:82:11:07:31" {
		t.Errorf("MAC = %s", r.MAC)
	}
	if r.Switch != "leaf-a3f-01" || r.Port != "Ethernet1/7" {
		t.Errorf("位置 = %s %s，想要 leaf-a3f-01 Ethernet1/7", r.Switch, r.Port)
	}
	if r.RackU() != "A-03/U12" {
		t.Errorf("机柜 U 位 = %s，想要 A-03/U12", r.RackU())
	}
	if len(r.Traps) != 0 {
		t.Errorf("这条不该命中陷阱，实际 %v", r.Traps)
	}
	if r.NextStep == "" {
		t.Error("定位成功也要给下一步")
	}
}

// 陷阱 a：同一个 MAC 在两个端口。取 age 最小的，旧的标 stale。
func TestLocateMacDrift(t *testing.T) {
	r := Locate(loadSamples(t), "10.32.8.44", Options{})

	if r.Verdict != VerdictLocated {
		t.Fatalf("verdict = %s，想要 %s", r.Verdict, VerdictLocated)
	}
	if r.Switch != "leaf-b1f-02" || r.Port != "Ethernet1/12" {
		t.Errorf("取最新表项 = %s %s，想要 leaf-b1f-02 Ethernet1/12", r.Switch, r.Port)
	}
	if r.RackU() != "B-01/U07" {
		t.Errorf("机柜 U 位 = %s，想要 B-01/U07", r.RackU())
	}
	if !hasTrap(r, TrapMacDrift) {
		t.Fatalf("没标 %s：%v", TrapMacDrift, r.Traps)
	}
	if len(r.Stale) != 1 {
		t.Fatalf("stale 条数 = %d，想要 1", len(r.Stale))
	}
	if s := r.Stale[0]; s.Device != "leaf-b1f-01" || s.Port != "Ethernet1/5" || s.AgeS != 780 {
		t.Errorf("stale = %s %s age %ds，想要 leaf-b1f-01 Ethernet1/5 age 780s", s.Device, s.Port, s.AgeS)
	}
	if !evidenceHas(r, "按 age 取最新的") {
		t.Errorf("证据里要说清为什么取新的：%v", r.Evidence)
	}
	if !strings.Contains(r.NextStep, "迁移余温") {
		t.Errorf("下一步要提醒旧口是余温，实际 %q", r.NextStep)
	}
}

// 陷阱 b：FDB 只报聚合逻辑口，得靠台账的 members 回到成员口。
func TestLocateLagLogicalOnly(t *testing.T) {
	r := Locate(loadSamples(t), "10.20.4.51", Options{})

	if r.Verdict != VerdictLocated {
		t.Fatalf("verdict = %s，想要 %s", r.Verdict, VerdictLocated)
	}
	if r.Port != "Port-Channel10" {
		t.Errorf("FDB 命中的端口 = %s，想要 Port-Channel10", r.Port)
	}
	if !hasTrap(r, TrapLagLogical) {
		t.Fatalf("没标 %s：%v", TrapLagLogical, r.Traps)
	}
	if len(r.Candidates) != 2 {
		t.Fatalf("成员口候选 = %d 个，想要 2：%v", len(r.Candidates), r.Candidates)
	}
	if r.Candidates[0].Port != "Ethernet1/1" || r.Candidates[1].Port != "Ethernet1/2" {
		t.Errorf("成员口 = %v，想要 Ethernet1/1、Ethernet1/2", r.Candidates)
	}
	if r.RackU() != "A-03/U20" {
		t.Errorf("机柜 U 位 = %s，想要 A-03/U20（两成员口一致）", r.RackU())
	}
	if !evidenceHas(r, "回到成员口") {
		t.Errorf("证据里要有回成员口这一步：%v", r.Evidence)
	}
	if !strings.Contains(r.NextStep, "成员口") {
		t.Errorf("下一步要按成员口报修，实际 %q", r.NextStep)
	}
}

// 陷阱 c：ARP 已老化、二层表也没有，但 DHCP 租约还在。
// 这时候答「查不到」是错的，必须说清「租约在、现网未见」。
func TestLocateLeaseOnly(t *testing.T) {
	r := Locate(loadSamples(t), "10.32.7.18", Options{})

	if r.Verdict != VerdictLeaseOnly {
		t.Fatalf("verdict = %s，想要 %s", r.Verdict, VerdictLeaseOnly)
	}
	if !hasTrap(r, TrapLeaseOnly) {
		t.Fatalf("没标 %s：%v", TrapLeaseOnly, r.Traps)
	}
	if !evidenceHas(r, "租约在、现网未见") {
		t.Fatalf("证据里必须有「租约在、现网未见」：%v", r.Evidence)
	}
	if !evidenceHas(r, "已过老化门限") {
		t.Errorf("证据里要写清 ARP 为什么不可用：%v", r.Evidence)
	}
	if !strings.Contains(r.NextStep, "pi-sensor-07") {
		t.Errorf("下一步要带租约里的主机名，实际 %q", r.NextStep)
	}
	if r.Rack != "" {
		t.Errorf("现网没见着，不该给机柜：%s", r.Rack)
	}
}

// ARP 新鲜但二层表里没有：断在 MAC 表这一环，跟「租约在」是两回事。
func TestLocateArpOnly(t *testing.T) {
	r := Locate(loadSamples(t), "10.20.4.60", Options{})

	if r.Verdict != VerdictArpOnly {
		t.Fatalf("verdict = %s，想要 %s", r.Verdict, VerdictArpOnly)
	}
	if len(r.Traps) != 0 {
		t.Errorf("这条不该命中陷阱，实际 %v", r.Traps)
	}
	if !evidenceHas(r, "二层表没有") {
		t.Errorf("证据要说清断在 FDB：%v", r.Evidence)
	}
	if !strings.Contains(r.NextStep, "采集清单") {
		t.Errorf("下一步指向采集覆盖，实际 %q", r.NextStep)
	}
}

func TestLocateNotFound(t *testing.T) {
	r := Locate(loadSamples(t), "10.32.7.99", Options{})

	if r.Verdict != VerdictNotFound {
		t.Fatalf("verdict = %s，想要 %s", r.Verdict, VerdictNotFound)
	}
	if len(r.Traps) != 0 {
		t.Errorf("真查不到不该命中陷阱，实际 %v", r.Traps)
	}
	if !evidenceHas(r, "ARP 表里没有这个 IP") || !evidenceHas(r, "没有这个 IP 的任何事件") {
		t.Errorf("证据要分别说明三张表都空：%v", r.Evidence)
	}
}

// 三表都走通，但台账里没有这个端口 —— 定位的最后一公里断了。
func TestLocateInventoryGap(t *testing.T) {
	r := Locate(loadSamples(t), "10.33.1.12", Options{})

	if r.Verdict != VerdictLocated {
		t.Fatalf("verdict = %s，想要 %s", r.Verdict, VerdictLocated)
	}
	if r.Switch != "leaf-b1f-01" || r.Port != "Ethernet1/40" {
		t.Errorf("位置 = %s %s", r.Switch, r.Port)
	}
	if r.Rack != "" {
		t.Errorf("台账没这个端口，不该编一个机柜：%s", r.RackU())
	}
	if !evidenceHas(r, "没有 leaf-b1f-01 Ethernet1/40 这条记录") {
		t.Errorf("证据要点明台账缺口：%v", r.Evidence)
	}
	if !strings.Contains(r.NextStep, "补台账") {
		t.Errorf("下一步先补台账，实际 %q", r.NextStep)
	}
}

// 老化门限是策略：调到 1 小时，同一个 IP 的 ARP 表项就从「不可用」变成「可用」，
// 结论也跟着从 lease_only 翻成 arp_only。采得慢了就是查不到，说的就是这个旋钮。
func TestAgingThresholdChangesVerdict(t *testing.T) {
	ds := loadSamples(t)

	strict := Locate(ds, "10.32.7.18", Options{ArpAging: 5 * time.Minute})
	if strict.Verdict != VerdictLeaseOnly {
		t.Fatalf("门限 5m 时 verdict = %s，想要 %s", strict.Verdict, VerdictLeaseOnly)
	}

	loose := Locate(ds, "10.32.7.18", Options{ArpAging: time.Hour})
	if loose.Verdict != VerdictArpOnly {
		t.Fatalf("门限 1h 时 verdict = %s，想要 %s", loose.Verdict, VerdictArpOnly)
	}
	if hasTrap(loose, TrapLeaseOnly) {
		t.Error("ARP 可用时不该再报 lease_only")
	}
}

// 同 MAC 跨 VLAN 的表项不算漂移，先按 VLAN 过滤；一条都没有才放宽并提示。
func TestFdbByMACVlanFilter(t *testing.T) {
	ds := miniDataset()
	ds.FDB = append(ds.FDB, FdbEntry{
		TS: ds.TS, Switch: "sw2", VLAN: 20, MAC: "00:11:22:33:44:55", Port: "Ethernet9/9", AgeS: 1,
	})

	hits, exact := ds.fdbByMAC("00:11:22:33:44:55", 10)
	if len(hits) != 1 || !exact {
		t.Fatalf("vlan 10 应只命中 1 条且 exact，实际 %d 条 exact=%v", len(hits), exact)
	}
	if hits[0].VLAN != 10 {
		t.Errorf("命中 VLAN = %d，想要 10", hits[0].VLAN)
	}

	all, exact := ds.fdbByMAC("00:11:22:33:44:55", 99)
	if len(all) != 2 || exact {
		t.Fatalf("vlan 99 没有表项，应放宽返回 2 条且 exact=false，实际 %d 条 exact=%v", len(all), exact)
	}
	// 放宽后按 age 升序：sw2 的 1s 排前面。
	if all[0].Device != "sw2" {
		t.Errorf("排序按 age 升序，首条 = %s，想要 sw2", all[0].Device)
	}
}

// 广播域对不上时照旧回答，但证据里必须说出来。
func TestLocateVLANMismatch(t *testing.T) {
	ds := miniDataset()
	ds.FDB = []FdbEntry{{
		TS: ds.TS, Switch: "sw1", VLAN: 20, MAC: "00:11:22:33:44:55", Port: "Ethernet1/1", AgeS: 5,
	}}

	r := Locate(ds, "10.0.0.5", Options{})
	if r.Verdict != VerdictLocated {
		t.Fatalf("verdict = %s，想要 %s", r.Verdict, VerdictLocated)
	}
	if !evidenceHas(r, "广播域对不上") {
		t.Errorf("VLAN 不一致要提示：%v", r.Evidence)
	}
}

// 成员口报的机柜不一致：回到成员口仍然定不到唯一位置，不许挑一个当答案。
func TestLagMemberConflict(t *testing.T) {
	r := Locate(miniDataset(), "10.0.0.5", Options{})

	if r.Verdict != VerdictLocated {
		t.Fatalf("verdict = %s，想要 %s", r.Verdict, VerdictLocated)
	}
	if !hasTrap(r, TrapLagLogical) {
		t.Fatalf("没标 %s：%v", TrapLagLogical, r.Traps)
	}
	if r.Rack != "" {
		t.Errorf("成员口不一致时不该给机柜，实际 %s", r.RackU())
	}
	if len(r.Candidates) != 2 {
		t.Fatalf("候选 = %d 个，想要 2", len(r.Candidates))
	}
	if r.Candidates[0].Rack != "A-01" || r.Candidates[1].Rack != "A-02" {
		t.Errorf("候选位置 = %v", r.Candidates)
	}
	if !evidenceHas(r, "不一致") {
		t.Errorf("证据要说清冲突：%v", r.Evidence)
	}
}

// ARP 老化但二层表还在：这是静默主机，不是「租约在、现网未见」。
func TestArpStaleButFdbHit(t *testing.T) {
	ds := miniDataset()
	ds.ARP[0].State = "stale"
	ds.ARP[0].LastSeen = ds.TS.Add(-30 * time.Minute)
	ds.Leases = []LeaseRecord{{
		TS: ds.TS, Server: "dhcp", Event: "ack", IP: "10.0.0.5",
		MAC: "00:11:22:33:44:55", Hostname: "silent-host", VLAN: 10,
		Start: ds.TS.Add(-2 * time.Hour), Expire: ds.TS.Add(2 * time.Hour),
	}}

	r := Locate(ds, "10.0.0.5", Options{})
	if r.Verdict != VerdictLocated {
		t.Fatalf("verdict = %s，想要 %s", r.Verdict, VerdictLocated)
	}
	if hasTrap(r, TrapLeaseOnly) {
		t.Error("二层表还在，不该报 lease_only")
	}
	if !evidenceHas(r, "已过老化门限") {
		t.Errorf("ARP 老化要留痕：%v", r.Evidence)
	}
}

// ARP state=incomplete、也没有租约：三张表都空，但原因要说清是「网关在问没人答」。
func TestLocateArpIncomplete(t *testing.T) {
	ds := miniDataset()
	ds.ARP[0].State = "incomplete"
	ds.ARP[0].MAC = ""
	ds.FDB = nil

	r := Locate(ds, "10.0.0.5", Options{})
	if r.Verdict != VerdictNotFound {
		t.Fatalf("verdict = %s，想要 %s", r.Verdict, VerdictNotFound)
	}
	if !evidenceHas(r, "incomplete") {
		t.Errorf("证据要点明 ARP 是 incomplete：%v", r.Evidence)
	}
}

// 批量查询清单里七条 IP 的结论分布，一条一条钉住。
func TestLocateAllSampleQueries(t *testing.T) {
	ds := loadSamples(t)
	want := map[string]string{
		"10.32.7.31": VerdictLocated,
		"10.32.7.18": VerdictLeaseOnly,
		"10.32.8.44": VerdictLocated,
		"10.20.4.51": VerdictLocated,
		"10.20.4.60": VerdictArpOnly,
		"10.33.1.12": VerdictLocated,
		"10.32.7.99": VerdictNotFound,
	}
	for ip, v := range want {
		if got := Locate(ds, ip, Options{}).Verdict; got != v {
			t.Errorf("%s verdict = %s，想要 %s", ip, got, v)
		}
	}
}
