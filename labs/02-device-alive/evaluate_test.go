package main

import (
	"math/rand"
	"testing"
)

// runScenario 端到端跑一遍：装配剧本、推进判定、与真相对账。
func runScenario(t *testing.T, name string, n, ticks int, mutate func(*Config)) (*Scenario, *Monitor, Eval) {
	t.Helper()
	cfg := baseConfig(ticks)
	cfg.Devices = n
	if mutate != nil {
		mutate(&cfg)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("测试配置不合法: %v", err)
	}
	sc, err := BuildScenario(name, ScenarioConfig{
		Devices:  n,
		Ticks:    ticks,
		Interval: cfg.Interval,
		Seed:     cfg.Seed,
		Plan:     DefaultPlan(),
	})
	if err != nil {
		t.Fatalf("装配剧本失败: %v", err)
	}
	m := NewMonitor(cfg, sc.Devices)
	rng := rand.New(rand.NewSource(cfg.Seed))
	m.Run(func(i, tick int) bool { return sc.Probe(i, tick, rng) })
	return sc, m, Evaluate(sc, m)
}

func TestEvaluateHardDownFullyDetected(t *testing.T) {
	_, _, ev := runScenario(t, ScenarioHardDown, 40, 60, nil)

	if ev.TrueWindows != 2 {
		t.Fatalf("真实失联窗口 %d 个，期望 2（剧本注入 2 台）", ev.TrueWindows)
	}
	if ev.Detected != ev.TrueWindows || ev.Missed != 0 {
		t.Errorf("检出 %d / 漏报 %d，期望全检出", ev.Detected, ev.Missed)
	}
	if ev.FalseDownDevices != 0 {
		t.Errorf("误报设备 %d 台，期望 0", ev.FalseDownDevices)
	}
	// 发现延迟落在 [(N-1)×P, N×P] 里。
	interval := baseConfig(60).Interval
	lo := int64(baseConfig(60).FailThreshold-1) * interval.Milliseconds()
	hi := int64(baseConfig(60).FailThreshold) * interval.Milliseconds()
	for _, d := range ev.DelaysMs {
		if d < lo || d > hi {
			t.Errorf("发现延迟 %s 超出 [%s, %s]", fmtDur(d), fmtDur(lo), fmtDur(hi))
		}
	}
}

func TestEvaluateRebootIsNotMissed(t *testing.T) {
	_, m, ev := runScenario(t, ScenarioReboot, 40, 60, nil)

	if ev.TrueWindows != 2 || ev.Missed != 0 {
		t.Fatalf("重启剧本应当全部检出，实际窗口 %d 漏报 %d", ev.TrueWindows, ev.Missed)
	}
	// 重启窗口短，每条恢复事件都该被标成疑似重启。
	up := 0
	for _, e := range m.Events() {
		if e.Kind != KindDeviceUp {
			continue
		}
		up++
		if !e.Evidence.SuspectReboot {
			t.Errorf("%s 的恢复没标疑似重启，离线 %s", e.Subject, fmtDur(e.Evidence.DowntimeMs))
		}
	}
	if up != ev.Detected {
		t.Errorf("恢复事件 %d 条，检出的窗口 %d 个，两者应当相等", up, ev.Detected)
	}
}

func TestEvaluateCPUStallIsMisjudgedNotMissed(t *testing.T) {
	_, _, ev := runScenario(t, ScenarioCPUStall, 40, 60, nil)

	if ev.TrueWindows != 0 {
		t.Errorf("CPU 打满不是设备下线，真实窗口应当是 0，实际 %d", ev.TrueWindows)
	}
	if ev.DegradedTotal == 0 {
		t.Fatal("剧本应当注入劣化窗口")
	}
	if ev.DegradedTicks == 0 {
		t.Log("本 seed 下 CPU 打满没触发误判，N=3 时是概率事件")
	}
	if ev.FalseDownDevices == 0 {
		t.Errorf("N=3、丢包 60%% 时应当出现误判，实际 0 台——去抖参数的失效边界没被演示出来")
	}
}

func TestEvaluateProbeOutageIsAttributedToProbe(t *testing.T) {
	_, m, ev := runScenario(t, ScenarioProbeOutage, 40, 60, nil)

	if ev.ProbeWindows != 1 {
		t.Fatalf("探测侧故障窗口 %d 个，期望 1", ev.ProbeWindows)
	}
	if ev.ProbeReported != 1 {
		t.Errorf("判定层发出 %d 条 probe-fault，期望 1", ev.ProbeReported)
	}
	if ev.BlockedTicks == 0 {
		t.Error("探测侧故障期间应当有观测被判为不可信")
	}
	// 关键：整机房不可达不能被写成 20 台设备下线。
	if n := countKind(m.Events(), KindDeviceDown); n != 0 {
		t.Errorf("探测侧故障不该产生设备下线事件，实际 %d 条", n)
	}
	if ev.FalseDownDevices != 0 {
		t.Errorf("误报设备 %d 台，期望 0", ev.FalseDownDevices)
	}
	if ev.ProbeDetectP(50) == 0 {
		t.Error("识别延迟应当大于 0：确认要连续 CorrTicks 轮")
	}
}

func TestEvaluateAllScenarioKeepsKindsApart(t *testing.T) {
	_, m, ev := runScenario(t, ScenarioAll, 40, 60, nil)

	if ev.TrueWindows != 4 {
		t.Errorf("真实失联窗口 %d 个，期望 4（2 硬挂 + 2 重启）", ev.TrueWindows)
	}
	if ev.Missed != 0 {
		t.Errorf("漏报 %d 个窗口，期望 0", ev.Missed)
	}
	if ev.ProbeReported != 1 {
		t.Errorf("probe-fault 事件 %d 条，期望 1", ev.ProbeReported)
	}
	// 误报只应该来自 CPU 打满那 3 台，不该多出别的。
	if ev.FalseDownDevices > 3 {
		t.Errorf("误报设备 %d 台，超过 CPU 打满的 3 台", ev.FalseDownDevices)
	}
	subject := map[string]bool{}
	for _, e := range m.Events() {
		if e.Kind == KindProbeFault {
			subject[e.Subject] = true
		}
	}
	if !subject["room-c/probe-c"] {
		t.Errorf("探测侧故障主体应是 room-c/probe-c，实际 %v", subject)
	}
}

func TestDownIntervals(t *testing.T) {
	changes := []StateChange{
		{Tick: 0, State: StateUnknown},
		{Tick: 3, State: StateDown},
		{Tick: 9, State: StateUp},
		{Tick: 12, State: StateDown},
	}
	got := downIntervals(changes, 20)
	want := [][2]int{{3, 9}, {12, 20}}
	if len(got) != len(want) {
		t.Fatalf("区间 %v，期望 %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("第 %d 段 %v，期望 %v", i, got[i], want[i])
		}
	}
	if empty := downIntervals([]StateChange{{Tick: 0, State: StateUp}}, 20); len(empty) != 0 {
		t.Errorf("全程 up 不该有 down 区间，实际 %v", empty)
	}
}

func TestOverlap(t *testing.T) {
	cases := []struct {
		a, b [2]int
		want int
	}{
		{[2]int{0, 10}, [2]int{5, 12}, 5},
		{[2]int{0, 10}, [2]int{10, 20}, 0},
		{[2]int{5, 12}, [2]int{0, 10}, 5},
		{[2]int{0, 5}, [2]int{1, 2}, 1},
		{[2]int{0, 0}, [2]int{0, 0}, 0},
	}
	for _, c := range cases {
		if got := overlap(c.a, c.b); got != c.want {
			t.Errorf("overlap(%v, %v) = %d，期望 %d", c.a, c.b, got, c.want)
		}
	}
}

func TestPercentileOf(t *testing.T) {
	cases := []struct {
		samples []int64
		p       float64
		want    int64
	}{
		{nil, 50, 0},
		{[]int64{42}, 99, 42},
		{[]int64{10, 20, 30, 40, 50}, 0, 10},
		{[]int64{10, 20, 30, 40, 50}, 50, 30},
		{[]int64{10, 20, 30, 40, 50}, 100, 50},
		{[]int64{50, 10, 30}, 50, 30},
	}
	for _, c := range cases {
		if got := percentileOf(c.samples, c.p); got != c.want {
			t.Errorf("percentileOf(%v, %v) = %d，期望 %d", c.samples, c.p, got, c.want)
		}
	}
}
