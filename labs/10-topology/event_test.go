package main

import (
	"path/filepath"
	"testing"
)

func dedupeSample(t *testing.T, dir *Directory, paths ...string) EventSet {
	t.Helper()
	streams := make([]EventStream, 0, len(paths))
	for _, p := range paths {
		s, err := loadEventStream(filepath.Join("samples", p))
		if err != nil {
			t.Fatal(err)
		}
		streams = append(streams, s)
	}
	set, err := Dedupe(dir, streams, defaultEventPolicy)
	if err != nil {
		t.Fatal(err)
	}
	return set
}

func findEvent(events []Event, device, port, family string) *Event {
	for i := range events {
		e := &events[i]
		if e.Device == device && e.Port == port && e.Family == family {
			return e
		}
	}
	return nil
}

func TestFingerprintFields(t *testing.T) {
	if got := fingerprintOf("tor-a3f-01", "Ethernet1/48", FamilyLinkDown); got != "link_down|tor-a3f-01|Ethernet1/48" {
		t.Errorf("端口级指纹 = %q", got)
	}
	if got := fingerprintOf("tor-a3f-03", "", FamilyLinkDown); got != "link_down|tor-a3f-03|*" {
		t.Errorf("设备级指纹 = %q", got)
	}
	// 指纹必须与时间、来源无关：同一件事换个源、晚 10 秒到，指纹还是同一个。
	a := fingerprintOf("sw-1", "eth1/1", FamilyLinkDown)
	b := fingerprintOf("sw-1", "eth1/1", FamilyLinkDown)
	if a != b {
		t.Error("同设备同端口同族的指纹必须稳定")
	}
	if fingerprintOf("sw-1", "eth1/1", FamilyLinkDown) == fingerprintOf("sw-1", "eth1/1", FamilyAdminDown) {
		t.Error("不同族的指纹必须不同，否则故障和变更会被并成一条")
	}
}

// 四源指向同一件事，输出一条带来源计数的事件。
func TestDedupCollapsesFourSources(t *testing.T) {
	dir := mustDirectory(t)
	set := dedupeSample(t, dir, "events.jsonl")

	e := findEvent(set.Events, "tor-a3f-01", "Ethernet1/48", FamilyLinkDown)
	if e == nil {
		t.Fatalf("没找到 tor-a3f-01 Ethernet1/48 的 link_down：%d 条事件", len(set.Events))
	}
	// 同一端口在 10:20:10 又 down 了一次，那条超窗口，另起一条，所以这里取最早的一条。
	if !startsWith(e.TS, "2026-05-12T10:14:03") {
		t.Errorf("锚点时间应当是窗口内最早的那条，实际 %s", e.TS)
	}
	if e.RawCount != 4 {
		t.Errorf("四个源应当合成一条（raw_count=4），实际 %d", e.RawCount)
	}
	if len(e.Sources) != 4 {
		t.Errorf("来源应当有 trap/syslog/counter/probe 四个，实际 %v", e.Sources)
	}
	// 四个源的原生 kind 都不一样，归到同一族才並得起来。
	if len(e.Kinds) != 4 {
		t.Errorf("原始 kind 应当保留四条，实际 %v", e.Kinds)
	}
	if e.Fingerprint != "link_down|tor-a3f-01|Ethernet1/48" {
		t.Errorf("指纹 = %q", e.Fingerprint)
	}
	// 四种写法（TOR-A3F-01 / 带域名主机名 / 管理 IP / 小写）都要归一化到同一个设备名。
	if e.Device != "tor-a3f-01" {
		t.Errorf("设备名没归一化：%q", e.Device)
	}
	// 端口四种写法（Ethernet1/48 / Eth1/48 / ifIndex 48 / 小写）都要归到规范名。
	if e.Port != "Ethernet1/48" {
		t.Errorf("端口名没归一化：%q", e.Port)
	}
}

// 这台设备 48 个端口在同一秒全 down：192 条原始事件 → 1 条，带计数。
func TestDeviceBurstCollapses48Ports(t *testing.T) {
	dir := mustDirectory(t)
	set := dedupeSample(t, dir, "events-burst.jsonl")

	if set.Raw != 192 {
		t.Fatalf("压力样本 192 条，实际读到 %d", set.Raw)
	}
	// 每个端口 4 个源先合成 1 条端口级事件。
	if set.PortEvents != 48 {
		t.Errorf("端口级事件应当是 48 条，实际 %d", set.PortEvents)
	}
	if len(set.Events) != 1 {
		t.Fatalf("48 口同秒全 down 应当折叠成 1 条，实际 %d 条", len(set.Events))
	}

	e := set.Events[0]
	if e.Scope != ScopeDevice {
		t.Errorf("scope = %s，期望 device", e.Scope)
	}
	if e.PortCount != 48 {
		t.Errorf("端口计数 = %d，期望 48", e.PortCount)
	}
	if e.RawCount != 192 {
		t.Errorf("原始条数 = %d，期望 192", e.RawCount)
	}
	if e.Port != "" {
		t.Errorf("设备级事件不该带端口名，实际 %q", e.Port)
	}
	if e.Family != FamilyLinkDown {
		t.Errorf("族 = %s", e.Family)
	}
	if len(e.Sources) != 4 {
		t.Errorf("四个来源都该记上，实际 %v", e.Sources)
	}
	if e.Fingerprint != "link_down|tor-a3f-03|*" {
		t.Errorf("设备级指纹 = %q", e.Fingerprint)
	}
}

// 端口数不到下限就不折叠：3 个口 down 是三个独立的端口故障。
func TestBurstNeedsEnoughPorts(t *testing.T) {
	dir := mustDirectory(t)
	policy := defaultEventPolicy
	policy.BurstMinPorts = 64 // 比 48 大

	stream, err := loadEventStream(filepath.Join("samples", "events-burst.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	set, err := Dedupe(dir, []EventStream{stream}, policy)
	if err != nil {
		t.Fatal(err)
	}
	if len(set.Events) != 48 {
		t.Fatalf("下限抬到 64 后不该折叠，应当是 48 条端口事件，实际 %d", len(set.Events))
	}
	for _, e := range set.Events {
		if e.Scope != ScopePort {
			t.Errorf("不该出现设备级事件：%+v", e)
		}
	}
}

// 窗口外的同一件事不合并，这是抖动，不是重复。
func TestDedupWindowDoesNotMergeFarEvents(t *testing.T) {
	dir := mustDirectory(t)
	set := dedupeSample(t, dir, "events.jsonl")

	var hits []Event
	for _, e := range set.Events {
		if e.Device == "tor-a3f-01" && e.Port == "Ethernet1/48" && e.Family == FamilyLinkDown {
			hits = append(hits, e)
		}
	}
	if len(hits) != 2 {
		t.Fatalf("10:14:03 与 10:20:10 相隔 6 分钟，应当是两条事件，实际 %d 条", len(hits))
	}
	if hits[0].RawCount != 4 || hits[1].RawCount != 1 {
		t.Errorf("两条事件的原始条数应当分别是 4 和 1，实际 %d 和 %d",
			hits[0].RawCount, hits[1].RawCount)
	}
}

// 同端口不同族不合并：一个是故障，一个是变更。
func TestKindConflictNotMerged(t *testing.T) {
	dir := mustDirectory(t)
	set := dedupeSample(t, dir, "events.jsonl")

	down := findEvent(set.Events, "tor-a3f-01", "Ethernet1/1", FamilyLinkDown)
	admin := findEvent(set.Events, "tor-a3f-01", "Ethernet1/1", FamilyAdminDown)
	if down == nil || admin == nil {
		t.Fatalf("link_down 与 admin_down 必须各留一条：%v", eventKeys(set.Events))
	}
	if down.RawCount != 1 || admin.RawCount != 1 {
		t.Errorf("两条事件各自只该有一个源，实际 %d 和 %d", down.RawCount, admin.RawCount)
	}
	if noticeClasses(set.Notices)[NoticeKindConflict] < 1 {
		t.Errorf("族冲突必须进提示清单，实际 %v", set.Notices)
	}
}

// 序号不同、消息原文不同，不构成新事件。
func TestDedupIgnoresSeqAndMessage(t *testing.T) {
	dir := mustDirectory(t)
	set := dedupeSample(t, dir, "events.jsonl")

	e := findEvent(set.Events, "tor-a3f-02", "Ethernet1/5", FamilyLinkDown)
	if e == nil {
		t.Fatal("没找到 tor-a3f-02 Ethernet1/5 的 link_down")
	}
	// trap 1 条 + syslog 2 条（seq 42011/42012，重复投递）→ 一条，raw_count=3。
	if e.RawCount != 3 {
		t.Errorf("重复投递应当並进同一条，raw_count 期望 3，实际 %d", e.RawCount)
	}
	if len(e.Sources) != 2 {
		t.Errorf("来源只有 trap 与 syslog，实际 %v", e.Sources)
	}
}

// 归一化不了的事件保留原文，并且必须报出来。
func TestUnresolvedPortAndDeviceKept(t *testing.T) {
	dir := mustDirectory(t)
	set := dedupeSample(t, dir, "events.jsonl")

	if e := findEvent(set.Events, "tor-a3f-02", "Gi1/0/99", FamilyLinkDown); e == nil {
		t.Error("端口翻不出来时事件要保留原文，不能悄悄丢掉")
	}
	if e := findEvent(set.Events, "tor-a3f-07", "Ethernet1/3", FamilyLinkDown); e == nil {
		t.Error("台账里没有的设备，事件也要保留")
	}
	got := noticeClasses(set.Notices)
	if got[NoticePortUnknown] != 1 {
		t.Errorf("端口翻不出来应当提示 1 条，实际 %d", got[NoticePortUnknown])
	}
	if got[NoticeDeviceUnknown] != 1 {
		t.Errorf("设备不在台账应当提示 1 条，实际 %d", got[NoticeDeviceUnknown])
	}
	if set.Unresolved != 2 {
		t.Errorf("归一化失败条数 = %d，期望 2", set.Unresolved)
	}
}

func TestOutOfOrderCountedPerStream(t *testing.T) {
	dir := mustDirectory(t)
	set := dedupeSample(t, dir, "events.jsonl", "events-burst.jsonl")

	// 乱序按流算：主样本 6 条，压力样本按时间顺序录制 0 条。
	// 两份文件拼起来数倒置，数出来的会是文件排列顺序，不是采集侧的真实乱序。
	if set.OutOfOrder != 6 {
		t.Errorf("乱序 = %d，期望 6", set.OutOfOrder)
	}
	if noticeClasses(set.Notices)[NoticeOutOfOrder] != 1 {
		t.Errorf("乱序要提示一次，实际 %v", set.Notices)
	}
	// 乱序不影响去重结果：迟到的那条仍要並进同一件事。
	e := findEvent(set.Events, "tor-a3f-01", "Ethernet1/7", FamilyLinkUp)
	if e == nil || e.RawCount != 2 {
		t.Errorf("10:13:58 与 10:13:59 两条迟到事件应当合成一条，实际 %v", e)
	}
}

func TestDedupAnchorDoesNotSlide(t *testing.T) {
	dir := mustDirectory(t)
	// 每 4 分钟一条，窗口 5 分钟：锚点固定的情况下，第 3 条（距锚点 8 分钟）
	// 另起一条；锚点滑动的实现会把三条连成一条。
	raw := []RawEvent{
		{TS: "2026-05-12T10:00:00Z", Source: SourceTrap, Device: "tor-a3f-01", Port: "Eth1/1", Kind: "link_down"},
		{TS: "2026-05-12T10:04:00Z", Source: SourceTrap, Device: "tor-a3f-01", Port: "Eth1/1", Kind: "link_down"},
		{TS: "2026-05-12T10:08:00Z", Source: SourceTrap, Device: "tor-a3f-01", Port: "Eth1/1", Kind: "link_down"},
	}
	set, err := Dedupe(dir, []EventStream{{Raw: raw}}, defaultEventPolicy)
	if err != nil {
		t.Fatal(err)
	}
	if len(set.Events) != 2 {
		t.Fatalf("锚点固定应当切成 2 条（0–4 分钟一条、8 分钟一条），实际 %d 条", len(set.Events))
	}
	if set.Events[0].RawCount != 2 || set.Events[1].RawCount != 1 {
		t.Errorf("两条的原始条数应当是 2 与 1，实际 %d 与 %d",
			set.Events[0].RawCount, set.Events[1].RawCount)
	}
}

func TestDedupeSampleTotals(t *testing.T) {
	dir := mustDirectory(t)
	set := dedupeSample(t, dir, "events.jsonl", "events-burst.jsonl")

	if set.Raw != 211 {
		t.Errorf("原始条数 = %d，期望 19 + 192 = 211", set.Raw)
	}
	// 11 条端口级 + 1 条设备级。
	if len(set.Events) != 12 {
		t.Errorf("输出 %d 条，期望 12（端口 11 + 设备级 1）", len(set.Events))
	}
	var deviceScoped int
	for _, e := range set.Events {
		if e.Scope == ScopeDevice {
			deviceScoped++
		}
	}
	if deviceScoped != 1 {
		t.Errorf("设备级事件应当只有 1 条，实际 %d", deviceScoped)
	}
	// 所有原始条数都要落到事件上，不许多也不许少——少一条就说明有事件被静默吞掉。
	sum := 0
	for _, e := range set.Events {
		sum += e.RawCount
	}
	if sum != 211 {
		t.Errorf("落到事件上的原始条数合计 = %d，期望 211", sum)
	}
}

func eventKeys(events []Event) []string {
	out := make([]string, 0, len(events))
	for _, e := range events {
		out = append(out, e.Fingerprint)
	}
	return out
}

func startsWith(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}
