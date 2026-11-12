package main

import (
	"testing"
	"time"
)

// baseConfig 是一份能跑的默认策略，测试里只改需要的那几个字段。
func baseConfig(ticks int) Config {
	return Config{
		Devices:          4,
		Interval:         30 * time.Second,
		Duration:         time.Duration(ticks) * 30 * time.Second,
		FailThreshold:    3,
		RecoverThreshold: 2,
		CorrThreshold:    0.5,
		CorrMinPeers:     4,
		CorrTicks:        2,
		RebootSuspect:    6 * time.Minute,
		Seed:             1,
	}
}

// sameGroup 造 n 台同机房同探针的设备，它们共享一条探测路径。
func sameGroup(n int) []Device {
	out := make([]Device, n)
	for i := range out {
		out[i] = Device{ID: deviceID(i), Room: "room-a", Probe: "probe-a"}
	}
	return out
}

func deviceID(i int) string {
	return "leaf-" + string(rune('a'+i))
}

func eventKinds(events []Event) []EventKind {
	out := make([]EventKind, len(events))
	for i, e := range events {
		out[i] = e.Kind
	}
	return out
}

func countKind(events []Event, kind EventKind) int {
	n := 0
	for _, e := range events {
		if e.Kind == kind {
			n++
		}
	}
	return n
}

func firstEvent(events []Event, kind EventKind) (Event, bool) {
	for _, e := range events {
		if e.Kind == kind {
			return e, true
		}
	}
	return Event{}, false
}

func TestDebounceDownAfterN(t *testing.T) {
	cases := []struct {
		name      string
		failTicks int
		wantDown  int
		wantTick  int
	}{
		{"失败 2 次不够判失联", 2, 0, 0},
		{"失败 3 次判失联", 3, 1, 2},
		{"失败 5 次只发一条下线事件", 5, 1, 2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg := baseConfig(10)
			m := NewMonitor(cfg, sameGroup(1)[:1])
			m.Run(func(i, tick int) bool { return tick >= c.failTicks })

			if got := countKind(m.Events(), KindDeviceDown); got != c.wantDown {
				t.Fatalf("下线事件 %d 条，期望 %d：%v", got, c.wantDown, eventKinds(m.Events()))
			}
			if c.wantDown == 0 {
				return
			}
			ev, _ := firstEvent(m.Events(), KindDeviceDown)
			if wantMs := cfg.AtMs(c.wantTick); ev.AtMs != wantMs {
				t.Errorf("事件时间 %d，期望 %d（第 %d 轮）", ev.AtMs, wantMs, c.wantTick)
			}
			if ev.Evidence.ConsecutiveFails != cfg.FailThreshold {
				t.Errorf("证据里的连续失败 %d，期望 %d", ev.Evidence.ConsecutiveFails, cfg.FailThreshold)
			}
		})
	}
}

func TestDebounceResetsOnSingleSuccess(t *testing.T) {
	// 失败、失败、成功、失败、失败：连续失败从未到 3，不该判失联。
	pattern := []bool{false, false, true, false, false, true}
	m := NewMonitor(baseConfig(len(pattern)), sameGroup(1)[:1])
	m.Run(func(i, tick int) bool { return pattern[tick] })

	if n := len(m.Events()); n != 0 {
		t.Fatalf("中间成功过一次，不该产生事件，实际 %d 条: %v", n, eventKinds(m.Events()))
	}
	if state := m.Tracks()[0].State; state != StateUp {
		t.Errorf("状态 %s，期望 up", state)
	}
}

func TestRecoverAfterM(t *testing.T) {
	cfg := baseConfig(12)
	m := NewMonitor(cfg, sameGroup(1)[:1])
	// 前 6 轮失败（第 3 轮判下线），之后全部成功。
	m.Run(func(i, tick int) bool { return tick >= 6 })

	down, ok := firstEvent(m.Events(), KindDeviceDown)
	if !ok {
		t.Fatal("应当判出下线")
	}
	up, ok := firstEvent(m.Events(), KindDeviceUp)
	if !ok {
		t.Fatal("应当判出恢复")
	}
	// M=2：第 6 轮成功计 1 次，第 7 轮才够 2 次。
	if want := cfg.AtMs(7); up.AtMs != want {
		t.Errorf("恢复时间 %d，期望 %d", up.AtMs, want)
	}
	if up.Evidence.ConsecutiveOKs != cfg.RecoverThreshold {
		t.Errorf("恢复证据的成功次数 %d，期望 %d", up.Evidence.ConsecutiveOKs, cfg.RecoverThreshold)
	}
	if got, want := up.Evidence.DowntimeMs, cfg.AtMs(7)-down.AtMs; got != want {
		t.Errorf("离线时长 %d，期望 %d", got, want)
	}
}

func TestSuspectRebootFlag(t *testing.T) {
	cfg := baseConfig(20)
	m := NewMonitor(cfg, sameGroup(1)[:1])
	// 失联 4 分钟后恢复，落在 6 分钟的疑似重启阈值内。
	m.Run(func(i, tick int) bool { return tick < 4 || tick >= 12 })

	up, ok := firstEvent(m.Events(), KindDeviceUp)
	if !ok {
		t.Fatal("应当判出恢复")
	}
	if !up.Evidence.SuspectReboot {
		t.Errorf("离线 %s 应当标疑似重启", fmtDur(up.Evidence.DowntimeMs))
	}
}

func TestSinglePeerFailureStillDown(t *testing.T) {
	// 同组 4 台，只有第 0 台一直失败：组失败比例 25%，不足以判探针故障。
	cfg := baseConfig(6)
	m := NewMonitor(cfg, sameGroup(4))
	m.Run(func(i, tick int) bool { return i != 0 })

	if got := countKind(m.Events(), KindProbeFault); got != 0 {
		t.Fatalf("单台失败不该判探针故障，实际 %d 条", got)
	}
	down, ok := firstEvent(m.Events(), KindDeviceDown)
	if !ok {
		t.Fatal("应当判出设备下线")
	}
	if down.Subject != deviceID(0) {
		t.Errorf("下线主体 %s，期望 %s", down.Subject, deviceID(0))
	}
	if down.Evidence.PeerFailed != 1 || down.Evidence.PeerTotal != 4 {
		t.Errorf("证据里的同组失败 %d/%d，期望 1/4", down.Evidence.PeerFailed, down.Evidence.PeerTotal)
	}
}

func TestProbeOutageSupressesDeviceEvents(t *testing.T) {
	// 整组 4 台同时不通 6 轮：判探针故障，不发任何设备下线。
	cfg := baseConfig(14)
	m := NewMonitor(cfg, sameGroup(4))
	m.Run(func(i, tick int) bool { return tick < 6 || tick >= 12 })

	if got := countKind(m.Events(), KindDeviceDown); got != 0 {
		t.Fatalf("同源整组失败不该发设备下线事件，实际 %d 条", got)
	}
	if got := countKind(m.Events(), KindDeviceUp); got != 0 {
		t.Fatalf("同源整组失败不该发设备上线事件，实际 %d 条", got)
	}
	fault, ok := firstEvent(m.Events(), KindProbeFault)
	if !ok {
		t.Fatal("应当判出探测侧故障")
	}
	if fault.Subject != "room-a/probe-a" {
		t.Errorf("事件主体 %s，期望分组键 room-a/probe-a", fault.Subject)
	}
	if fault.Evidence.PeerFailed != 4 || fault.Evidence.PeerTotal != 4 {
		t.Errorf("证据里的同组失败 %d/%d，期望 4/4", fault.Evidence.PeerFailed, fault.Evidence.PeerTotal)
	}
	if got := countKind(m.Events(), KindProbeRecovered); got != 1 {
		t.Errorf("应当有一条 probe-recovered，实际 %d", got)
	}
}

func TestCorrTicksFiltersSingleRoundBlip(t *testing.T) {
	// 整组只丢一轮：不加确认轮数就是一条假故障，加了就什么都不发。
	cfg := baseConfig(10)
	m := NewMonitor(cfg, sameGroup(4))
	m.Run(func(i, tick int) bool { return tick != 3 })

	if n := len(m.Events()); n != 0 {
		t.Fatalf("单轮抖动不该发事件，实际 %d 条: %v", n, eventKinds(m.Events()))
	}
	for _, tr := range m.Tracks() {
		if tr.Fails != 0 {
			t.Errorf("%s 的连续失败应当被成功清零，实际 %d", tr.Device.ID, tr.Fails)
		}
	}
}

func TestCorrMinPeersSkipsSmallGroup(t *testing.T) {
	// 组里只有 2 台，低于 CorrMinPeers=4：相关性无从谈起，只能按设备判。
	cfg := baseConfig(6)
	m := NewMonitor(cfg, sameGroup(2))
	m.Run(func(i, tick int) bool { return false })

	if got := countKind(m.Events(), KindProbeFault); got != 0 {
		t.Errorf("小规模分组不该判探针故障，实际 %d 条", got)
	}
	if got := countKind(m.Events(), KindDeviceDown); got != 2 {
		t.Errorf("两台设备都该判下线，实际 %d 条", got)
	}
}

func TestNoUpEventForInitialUp(t *testing.T) {
	m := NewMonitor(baseConfig(8), sameGroup(2))
	m.Run(func(i, tick int) bool { return true })

	if n := len(m.Events()); n != 0 {
		t.Fatalf("刚接入的正常设备不该有事件，实际 %d 条", n)
	}
	for _, tr := range m.Tracks() {
		if tr.State != StateUp {
			t.Errorf("%s 的状态 %s，期望 up", tr.Device.ID, tr.State)
		}
	}
}

func TestSnapshotCountersAndFlapped(t *testing.T) {
	cfg := baseConfig(16)
	m := NewMonitor(cfg, sameGroup(2))
	// 第 0 台在 3–9 轮失联后恢复；第 1 台一直正常。
	m.Run(func(i, tick int) bool { return i != 0 || tick < 3 || tick >= 9 })

	snap := m.Snapshot(ScenarioAll)
	if snap.Counters.DeviceDown != 1 || snap.Counters.DeviceUp != 1 {
		t.Fatalf("计数器不对: %+v", snap.Counters)
	}
	// 快照里全员 up，但有一台期间下线过——这个数字只能从事件流算出来。
	if snap.Counters.UpNow != 2 || snap.Counters.DownNow != 0 {
		t.Errorf("终态计数 up=%d down=%d，期望 2/0", snap.Counters.UpNow, snap.Counters.DownNow)
	}
	if snap.Counters.FlappedDevices != 1 {
		t.Errorf("抖动设备数 %d，期望 1", snap.Counters.FlappedDevices)
	}
	if snap.Config.FailThreshold != cfg.FailThreshold || snap.Config.CorrTicks != cfg.CorrTicks {
		t.Errorf("快照没把策略参数带上: %+v", snap.Config)
	}
}

func TestStateAtReplay(t *testing.T) {
	tr := DeviceTrack{Changes: []StateChange{
		{Tick: 0, State: StateUnknown},
		{Tick: 3, State: StateDown},
		{Tick: 9, State: StateUp},
	}}
	cases := []struct {
		tick int
		want State
	}{
		{0, StateUnknown},
		{2, StateUnknown},
		{3, StateDown},
		{8, StateDown},
		{9, StateUp},
		{100, StateUp},
	}
	for _, c := range cases {
		if got := tr.StateAt(c.tick); got != c.want {
			t.Errorf("StateAt(%d) = %s，期望 %s", c.tick, got, c.want)
		}
	}
}

func TestConfigValidate(t *testing.T) {
	ok := baseConfig(10)
	if err := ok.Validate(); err != nil {
		t.Fatalf("默认配置应当合法: %v", err)
	}
	cases := []struct {
		name  string
		patch func(*Config)
	}{
		{"设备数为 0", func(c *Config) { c.Devices = 0 }},
		{"探测周期为 0", func(c *Config) { c.Interval = 0 }},
		{"仿真不到一轮", func(c *Config) { c.Duration = time.Second }},
		{"N 为 0", func(c *Config) { c.FailThreshold = 0 }},
		{"M 为 0", func(c *Config) { c.RecoverThreshold = 0 }},
		{"相关性阈值为 0", func(c *Config) { c.CorrThreshold = 0 }},
		{"相关性阈值大于 1", func(c *Config) { c.CorrThreshold = 1.5 }},
		{"同源组只有 1 台", func(c *Config) { c.CorrMinPeers = 1 }},
		{"确认轮数为 0", func(c *Config) { c.CorrTicks = 0 }},
		{"确认轮数不小于 N", func(c *Config) { c.CorrTicks = 3 }},
		{"疑似重启阈值非正", func(c *Config) { c.RebootSuspect = 0 }},
	}
	for _, c := range cases {
		cfg := ok
		c.patch(&cfg)
		if err := cfg.Validate(); err == nil {
			t.Errorf("%s：应当报错", c.name)
		}
	}
}
