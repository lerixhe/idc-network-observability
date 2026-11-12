package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// fuseSample 跑一遍真实样本，几乎所有拓扑断言都建立在它上面。
func fuseSample(t *testing.T) (*Directory, Topology) {
	t.Helper()
	dir := mustDirectory(t)
	lldp, err := loadLLDP(filepath.Join("samples", "lldp.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	stp, err := loadSTP(filepath.Join("samples", "stp.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	routes, err := loadRoutes(filepath.Join("samples", "route.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	macs, err := loadMACs(filepath.Join("samples", "mac.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	return dir, Fuse(dir, lldp, stp, routes, macs)
}

func findLink(links []Link, a, b string) *Link {
	for i := range links {
		if links[i].A.String() == a && links[i].B.String() == b {
			return &links[i]
		}
		if links[i].A.String() == b && links[i].B.String() == a {
			return &links[i]
		}
	}
	return nil
}

func noticeClasses(notices []Notice) map[string]int {
	m := map[string]int{}
	for _, n := range notices {
		m[n.Class]++
	}
	return m
}

func TestFuseSampleLinkCount(t *testing.T) {
	_, tp := fuseSample(t)

	if tp.LLDPRows != 17 || tp.LLDPUsable != 16 {
		t.Errorf("LLDP 17 行里应当有 16 行可用（1 行端口翻不出来），实际 %d/%d",
			tp.LLDPRows, tp.LLDPUsable)
	}
	if len(tp.Links) != 10 {
		t.Fatalf("样本应当融合出 10 条链路，实际 %d", len(tp.Links))
	}

	byKind := map[string]int{}
	for _, l := range tp.Links {
		byKind[l.Kind]++
	}
	if byKind[KindPhysical] != 8 || byKind[KindLag] != 1 || byKind[KindInferred] != 1 {
		t.Errorf("链路类型分布 = %v；期望物理 8 / 聚合 1 / 反推 1", byKind)
	}

	// 每条链路的 ID 必须唯一，否则落库时后半条会覆盖前半条。
	seen := map[string]bool{}
	for _, l := range tp.Links {
		if seen[l.ID] {
			t.Errorf("链路 ID 重复：%s", l.ID)
		}
		seen[l.ID] = true
	}
}

func TestFuseSampleNoticeClasses(t *testing.T) {
	_, tp := fuseSample(t)
	got := noticeClasses(tp.Notices)

	want := map[string]int{
		NoticePeerMismatch:    1, // spine-01 与 tor-a3f-02 对 5 口的说法不一致
		NoticeOneSided:        3,
		NoticeSTPBlocked:      1,
		NoticeMACConflict:     1,
		NoticeMACInferred:     1,
		NoticeDeviceUnknown:   1, // acc-a3f-09 幽灵设备
		NoticePortUnknown:     1, // Eth1/11x 翻不出来
		NoticeRouteMismatch:   1,
		NoticeRouteNoPhysical: 0,
	}
	for class, n := range want {
		if got[class] != n {
			t.Errorf("%s 提示 %d 条，期望 %d", class, got[class], n)
		}
	}
	if len(tp.Notices) != 10 {
		t.Errorf("提示合计 %d 条，期望 10", len(tp.Notices))
	}
}

// 这是整个实验最核心的一条断言：冲突不投票，按置信度取高。
func TestFusePrefersLLDPOverMACNoVoting(t *testing.T) {
	dir := newDirectory(t, CMDB{
		Devices: []Device{
			{Name: "sw-1", MgmtMAC: "00:00:00:00:00:01",
				Ports: []CMDBPort{{Name: "Ethernet1/1", Aliases: []string{"Eth1/1"}, IfIndex: 1}}},
			{Name: "sw-2", Ports: []CMDBPort{{Name: "Ethernet1/1", Aliases: []string{"Eth1/1"}, IfIndex: 1}}},
			{Name: "sw-3", MgmtMAC: "00:00:00:00:00:03",
				Ports: []CMDBPort{{Name: "Ethernet1/1", Aliases: []string{"Eth1/1"}, IfIndex: 1}}},
		},
	})

	lldp := []LLDPEntry{
		{LocalDevice: "sw-1", LocalPort: "Eth1/1", RemoteDevice: "sw-2", RemotePortDesc: "Eth1/1"},
		{LocalDevice: "sw-2", LocalPort: "Eth1/1", RemoteDevice: "sw-1", RemotePortDesc: "Eth1/1"},
	}
	// MAC 表说 sw-1 的 1 口后面是 sw-3，跟 LLDP 打架。
	macs := []MACEntry{{Device: "sw-1", Port: "Eth1/1", MAC: "00:00:00:00:00:03"}}

	tp := Fuse(dir, lldp, nil, nil, macs)

	link := findLink(tp.Links, "sw-1:Ethernet1/1", "sw-2:Ethernet1/1")
	if link == nil {
		t.Fatalf("LLDP 报的链路不见了：%v", tp.Links)
	}
	if link.Confidence != ConfHigh || link.Question {
		t.Errorf("LLDP 链路应当 high 且不打问号，实际 conf=%s question=%v", link.Confidence, link.Question)
	}
	if !hasString(link.Sources, SourceLLDP) || hasString(link.Sources, SourceMAC) {
		t.Errorf("结论来源应当只有 lldp，实际 %v", link.Sources)
	}

	// 不投票：不能因为 MAC 说了另一台设备就多出一条 sw-1 ↔ sw-3 的链路。
	if extra := findLink(tp.Links, "sw-1:Ethernet1/1", "sw-3:?"); extra != nil {
		t.Errorf("MAC 反推不该在与 LLDP 冲突时另立一条链路：%s", extra.ID)
	}
	if noticeClasses(tp.Notices)[NoticeMACConflict] != 1 {
		t.Errorf("冲突必须进提示清单，实际提示 %v", tp.Notices)
	}
}

func TestFuseLagAlignsLogicalAndMember(t *testing.T) {
	_, tp := fuseSample(t)
	link := findLink(tp.Links, "spine-01:Port-Channel10", "tor-a3f-01:Eth-Trunk1")
	if link == nil {
		t.Fatalf("聚合链路没有对齐：%v", linkIDs(tp.Links))
	}
	if link.Kind != KindLag {
		t.Errorf("这条链路的 kind = %s，期望 lag", link.Kind)
	}
	// 一端报逻辑口（Eth-Trunk1）、一端报成员口（Ethernet1/10、1/11），
	// 折叠之后必须落到同一条链路上，两个成员口都要留底。
	if len(link.Members) != 2 {
		t.Errorf("成员对应当有 2 对，实际 %d：%v", len(link.Members), link.Members)
	}
	members := map[string]bool{}
	for _, m := range link.Members {
		members[m.A.Port] = true
	}
	if !members["Ethernet1/10"] || !members["Ethernet1/11"] {
		t.Errorf("成员口应当是 Ethernet1/10 与 1/11，实际 %v", members)
	}
	// 两端都报回对方，折叠后双向成立。
	if !link.Bidirectional {
		t.Error("折叠后两端应当互相报回，判双向")
	}
}

func TestFuseSTPBlockedIsRedundantNotWrong(t *testing.T) {
	_, tp := fuseSample(t)
	link := findLink(tp.Links, "spine-01:Ethernet1/9", "spine-02:Ethernet1/9")
	if link == nil {
		t.Fatalf("STP 校验不该删掉链路：%v", linkIDs(tp.Links))
	}
	if !link.Redundant {
		t.Error("spine-02 侧是 alternate/blocking，应当标 redundant")
	}
	if link.Confidence != ConfHigh || link.Question {
		t.Error("冗余链路仍然是高置信度链路，不该降级")
	}
	if !hasString(link.VerifiedBy, SourceSTP) {
		t.Errorf("STP 应当记进 verified_by，实际 %v", link.VerifiedBy)
	}
	// blocked 是 info，不是 warn：它是设计，不是故障。
	for _, n := range tp.Notices {
		if n.Class == NoticeSTPBlocked && n.Severity != SevInfo {
			t.Errorf("stp_blocked 的严重级是 %s，应当是 info", n.Severity)
		}
	}
}

func TestFusePeerMismatch(t *testing.T) {
	_, tp := fuseSample(t)

	// spine-01 说 5 口连 tor-a3f-02 的 5 口，tor-a3f-02 的 5 口说连 spine-02。
	// 两条链路都留着（各有一侧证据），但冲突必须点名说清楚。
	one := findLink(tp.Links, "spine-01:Ethernet1/5", "tor-a3f-02:Ethernet1/5")
	two := findLink(tp.Links, "spine-02:Ethernet1/5", "tor-a3f-02:Ethernet1/5")
	if one == nil || two == nil {
		t.Fatalf("两端说法不一致时两条链路都该保留：%v", linkIDs(tp.Links))
	}

	var detail string
	for _, n := range tp.Notices {
		if n.Class == NoticePeerMismatch {
			detail = n.Detail
		}
	}
	if detail == "" {
		t.Fatal("错挂没有进提示清单")
	}
	if !strings.Contains(detail, "spine-01:Ethernet1/5") || !strings.Contains(detail, "spine-02:Ethernet1/5") {
		t.Errorf("错挂提示要把两端各自的说法都写出来，实际：%s", detail)
	}
}

func TestFuseAliasRescuesTwoNamesOnePort(t *testing.T) {
	_, tp := fuseSample(t)
	// spine-01 报 Eth1/6、tor-a3f-02 报 Gi1/0/6，两个名字都归到 Ethernet1/6，
	// 折叠后才判得出双向。
	link := findLink(tp.Links, "spine-01:Ethernet1/6", "tor-a3f-02:Ethernet1/6")
	if link == nil {
		t.Fatalf("别名没归一化出这条链路：%v", linkIDs(tp.Links))
	}
	if !link.Bidirectional {
		t.Error("两侧都报了对方，应当判双向")
	}
}

func TestFuseMACInferredHasQuestion(t *testing.T) {
	_, tp := fuseSample(t)
	var link *Link
	for i := range tp.Links {
		if tp.Links[i].Kind == KindInferred {
			link = &tp.Links[i]
		}
	}
	if link == nil {
		t.Fatal("tor-a3f-04 关了 LLDP，MAC 反推该出一条链路")
	}
	if !link.Question || link.Confidence != ConfLow {
		t.Errorf("MAC 反推必须是低置信度 + 问号，实际 conf=%s question=%v",
			link.Confidence, link.Question)
	}
	if link.B.Port != "" {
		t.Errorf("反推不出对端端口，B 端端口该是空的，实际 %q", link.B.Port)
	}
	if !strings.Contains(link.ID, "?") {
		t.Errorf("问号要出现在链路 ID 里，实际 %s", link.ID)
	}
}

func TestFuseUnknownDeviceKeepsLink(t *testing.T) {
	_, tp := fuseSample(t)
	link := findLink(tp.Links, "acc-a3f-09:Ethernet1/1", "spine-01:Ethernet1/2")
	if link == nil {
		t.Fatalf("幽灵设备连过来的链路不该丢：%v", linkIDs(tp.Links))
	}
	if !link.PeerUnknown {
		t.Error("acc-a3f-09 不在台账里，应当标 peer_unknown")
	}
	// 幽灵设备墙上的端口名没法和台账对齐，只能照抄 LLDP 报的原文。
	if link.A.Port != "Ethernet1/1" {
		t.Errorf("幽灵设备侧端口名应当保留原文，实际 %q", link.A.Port)
	}
}

func TestFuseRouteMismatchAndNoPhysical(t *testing.T) {
	_, tp := fuseSample(t)
	got := noticeClasses(tp.Notices)
	// tor-a3f-02 的 OSPF 邻居指向 spine-01，LLDP 说那口连的是 tor-a3f-01。
	if got[NoticeRouteMismatch] != 1 {
		t.Errorf("三层邻接与二层链路打架应当提示 1 条，实际 %d", got[NoticeRouteMismatch])
	}
	// Loopback0 上的邻接是逻辑接口，不该被当成物理链路缺口。
	if got[NoticeRouteNoPhysical] != 0 {
		t.Errorf("Loopback 邻接不该报 route_no_physical，实际 %d 条", got[NoticeRouteNoPhysical])
	}
}

func TestFusePortNameUnknownDropsRow(t *testing.T) {
	dir := newDirectory(t, CMDB{Devices: []Device{
		{Name: "sw-1", Ports: []CMDBPort{{Name: "Ethernet1/1", IfIndex: 1}}},
		{Name: "sw-2", Ports: []CMDBPort{{Name: "Ethernet1/1", IfIndex: 1}}},
	}})
	lldp := []LLDPEntry{
		{LocalDevice: "sw-1", LocalPort: "Ethernet1/1", RemoteDevice: "sw-2", RemotePortID: "Eth9/99"},
	}
	tp := Fuse(dir, lldp, nil, nil, nil)
	if len(tp.Links) != 0 {
		t.Errorf("对端端口翻不出来时不该凭空造链路，实际 %v", linkIDs(tp.Links))
	}
	got := noticeClasses(tp.Notices)
	if got[NoticePortUnknown] != 1 {
		t.Errorf("翻不出来的端口要进提示清单，实际 %v", tp.Notices)
	}
}

func TestFusePortIDFallsBackToIfIndex(t *testing.T) {
	dir := newDirectory(t, CMDB{Devices: []Device{
		{Name: "sw-1", Ports: []CMDBPort{{Name: "Ethernet1/48", IfIndex: 48}}},
		{Name: "sw-2", Ports: []CMDBPort{{Name: "Ethernet1/48", IfIndex: 48}}},
	}})
	// 部分设备只报 lldpRemPortId 不报 lldpRemPortDesc，值是纯编号。
	lldp := []LLDPEntry{
		{LocalDevice: "sw-1", LocalPort: "Ethernet1/48", RemoteDevice: "sw-2", RemotePortID: "48"},
	}
	tp := Fuse(dir, lldp, nil, nil, nil)
	if len(tp.Links) != 1 {
		t.Fatalf("编号要靠 ifIndex 翻成端口名，实际 %v", linkIDs(tp.Links))
	}
	if tp.Links[0].B.Port != "Ethernet1/48" {
		t.Errorf("B 端端口 = %q，期望 Ethernet1/48", tp.Links[0].B.Port)
	}
}

func TestLinkKeyOrderIndependent(t *testing.T) {
	a := linkEnd{Device: "sw-1", Port: "Ethernet1/1"}
	b := linkEnd{Device: "sw-2", Port: "Ethernet1/1"}
	if makeLinkKey(a, b) != makeLinkKey(b, a) {
		t.Error("链路是端对，与方向无关：两个方向的 key 必须相同")
	}
	if k := makeLinkKey(a, b); k.a != a || k.b != b {
		t.Errorf("key 的两端要按字典序排好，实际 %v", k)
	}
}

func linkIDs(links []Link) []string {
	out := make([]string, 0, len(links))
	for _, l := range links {
		out = append(out, l.ID)
	}
	return out
}
