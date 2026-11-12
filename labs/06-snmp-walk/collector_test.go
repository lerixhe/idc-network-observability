package main

import (
	"testing"
	"time"
)

// collectOnce 用样本真实跑一轮采集器（虚拟时钟，瞬时完成）。
// 集成测试一律走这条路，不直接读样本结构——采不到就说明采集链有问题。
func collectOnce(t *testing.T, path string, cycles int, mutate func(*Config)) *Collector {
	t.Helper()
	f, err := LoadFleet(path)
	if err != nil {
		t.Fatalf("加载 %s: %v", path, err)
	}
	start, err := sampleStart(f)
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{Cycles: cycles, Interval: 30 * time.Second, Seed: 1}
	if mutate != nil {
		mutate(&cfg)
	}
	c, err := NewCollector(f, NewSampleTransport(f, cfg.Seed), NewVirtualClock(start), cfg)
	if err != nil {
		t.Fatalf("建采集器: %v", err)
	}
	c.Run()
	return c
}

func sessionOf(t *testing.T, c *Collector, name string) *Session {
	t.Helper()
	for _, s := range c.Sessions {
		if s.Dev.Name == name {
			return s
		}
	}
	t.Fatalf("采集器里没有 %s", name)
	return nil
}

func roundsOf(c *Collector, dev string) []CycleResult {
	var out []CycleResult
	for _, r := range c.Results {
		if r.Device == dev {
			out = append(out, r)
		}
	}
	return out
}

// 一轮下来五台设备各自的命运：三台正常、一台慢、一台不可达。
func TestCollectFleetOnce(t *testing.T) {
	c := collectOnce(t, "samples/fleet.json", 1, nil)
	want := map[string]DevState{
		"spine-01":       StateOK,
		"tor-a3f-01":     StateOK,
		"tor-a3f-02":     StateOK,
		"leaf-legacy-01": StateSlow,
		"tor-b7f-01":     StateUnreachable,
	}
	for name, state := range want {
		s := sessionOf(t, c, name)
		if s.State != state {
			t.Fatalf("%s 的结论 = %s，期望 %s", name, s.State, state)
		}
	}
	// 采到的端口数：不可达那台一行都没有，其余按样本端口数。
	if got := len(c.LastPorts["tor-b7f-01"]); got != 0 {
		t.Fatalf("不可达设备不该有端口数据，实际 %d 行", got)
	}
	if got := len(c.LastPorts["spine-01"]); got != 24 {
		t.Fatalf("spine-01 应采到 24 行，实际 %d", got)
	}
}

// 一台设备的请求数就是 PDU 数：列数 × ⌈(端口数+1)/reps⌉。
func TestCollectPDUCount(t *testing.T) {
	c := collectOnce(t, "samples/fleet.json", 1, nil)
	cases := []struct {
		dev  string
		pdus int
		reps int
	}{
		{"spine-01", 12, 64},      // 24 行 / 64 条 = 1 页 × 12 列
		{"tor-a3f-01", 24, 16},    // 24 行 / 16 条 = 2 页 × 12 列
		{"tor-a3f-02", 48, 8},     // 24 行 / 8 条 = 4 页 × 12 列
		{"leaf-legacy-01", 24, 8}, // 12 行 / 8 条 = 2 页 × 12 列
	}
	for _, c2 := range cases {
		rounds := roundsOf(c, c2.dev)
		if len(rounds) != 1 {
			t.Fatalf("%s 只有一轮", c2.dev)
		}
		if rounds[0].Stats.PDUs != c2.pdus {
			t.Fatalf("%s 的 PDU = %d，期望 %d", c2.dev, rounds[0].Stats.PDUs, c2.pdus)
		}
		if rounds[0].Reps != c2.reps {
			t.Fatalf("%s 的 max-repetitions = %d，期望 %d", c2.dev, rounds[0].Reps, c2.reps)
		}
		if len(rounds[0].Missing) != 0 {
			t.Fatalf("%s 不该缺列，实际 %v", c2.dev, rounds[0].Missing)
		}
	}
}

// 老设备被限流打到排队：C 档 2 请求/秒，48 个请求最快也要 24 秒。
func TestRateLimitingQueuesOnSlowTier(t *testing.T) {
	c := collectOnce(t, "samples/fleet.json", 1, nil)
	r := roundsOf(c, "tor-a3f-02")[0]
	queue := time.Duration(r.Stats.QueueWait) * time.Millisecond
	// 48 个请求 / 2 请求每秒 = 24s；这期间设备在回包的那 10s 里令牌还在补，
	// 所以真正干等的是 13s 左右，但总耗时仍然是 23s 以上。
	if queue < 10*time.Second {
		t.Fatalf("tor-a3f-02 的排队时间 = %s，期望 ≥ 10s（2 请求/秒）", queue)
	}
	if r.Stats.Requests != 48 {
		t.Fatalf("请求数 = %d，期望 48（排队不该丢请求）", r.Stats.Requests)
	}
	if r.Elapsed < 23*time.Second {
		t.Fatalf("本轮耗时 = %s，受限流节流应 ≥ 23s", r.Elapsed)
	}
	if r.Elapsed > 30*time.Second {
		t.Fatalf("本轮耗时 = %s，不该超过周期（超过的话样本参数需要重调）", r.Elapsed)
	}
	// A 档同样的端口数，几乎不排队。
	a := roundsOf(c, "spine-01")[0]
	if a.Stats.QueueWait != 0 {
		t.Fatalf("A 档 12 个请求不该排队，实际排队 %dms", a.Stats.QueueWait)
	}
}

// 响应慢但不丢包：leaf-legacy-01 一轮 37 秒，超过 30 秒周期，必须被记下来。
func TestSlowDeviceExceedsInterval(t *testing.T) {
	c := collectOnce(t, "samples/fleet.json", 1, nil)
	r := roundsOf(c, "leaf-legacy-01")[0]
	if r.State != StateSlow {
		t.Fatalf("结论 = %s，期望 slow", r.State)
	}
	if r.Stats.Timeouts != 0 {
		t.Fatalf("这台设备只是慢，不该有超时，实际 %d 次", r.Stats.Timeouts)
	}
	if r.Elapsed <= 30*time.Second {
		t.Fatalf("一轮耗时 = %s，应超过 30 秒周期", r.Elapsed)
	}
	if r.Note == "" {
		t.Fatal("超过周期要在 note 里写明")
	}
}

// 不可达设备：连续超时后隔离，之后几轮直接跳过，别再去打。
func TestUnreachableDeviceIsQuarantined(t *testing.T) {
	c := collectOnce(t, "samples/fleet.json", 6, nil)
	rounds := roundsOf(c, "tor-b7f-01")
	if len(rounds) != 6 {
		t.Fatalf("轮数 = %d", len(rounds))
	}
	first := rounds[0]
	if first.State != StateUnreachable {
		t.Fatalf("第 1 轮结论 = %s，期望 unreachable", first.State)
	}
	if first.Rows != 0 {
		t.Fatalf("不可达设备不该拼出行，实际 %d", first.Rows)
	}
	// 连续 3 列拿不到就中止，所以只发了 3 列 × 2 次尝试 = 6 个请求。
	if first.Stats.Requests != 6 {
		t.Fatalf("第 1 轮请求数 = %d，期望 6（3 列 × 2 次尝试）", first.Stats.Requests)
	}
	if first.Stats.Timeouts != 6 {
		t.Fatalf("第 1 轮超时数 = %d，期望 6", first.Stats.Timeouts)
	}
	for _, r := range rounds[1:5] {
		if !r.Skipped {
			t.Fatalf("第 %d 轮应该被跳过，实际还是采了", r.Cycle)
		}
	}
	last := rounds[5]
	if last.Skipped {
		t.Fatal("第 6 轮应该去探一次（隔离期每 5 轮一次）")
	}
	if last.State != StateUnreachable {
		t.Fatalf("第 6 轮结论 = %s，期望仍然不可达", last.State)
	}
}

// 慢设备连续两轮判慢就降频，跳过 3 轮，第 6 轮回来。
func TestSlowDeviceIsDegraded(t *testing.T) {
	c := collectOnce(t, "samples/fleet.json", 6, nil)
	rounds := roundsOf(c, "leaf-legacy-01")
	if rounds[0].Skipped || rounds[1].Skipped {
		t.Fatal("前两轮应该照采（去抖之后才降频）")
	}
	for _, r := range rounds[2:5] {
		if !r.Skipped {
			t.Fatalf("第 %d 轮应该降频跳过", r.Cycle)
		}
	}
	if rounds[5].Skipped {
		t.Fatal("第 6 轮应该回来采一次")
	}
	if rounds[1].Note == "" {
		t.Fatal("降频的那一轮要写明原因")
	}
}

// 正常设备不该被降频，六轮都采。
func TestHealthyDevicesCollectedEveryCycle(t *testing.T) {
	c := collectOnce(t, "samples/fleet.json", 6, nil)
	for _, dev := range []string{"spine-01", "tor-a3f-01", "tor-a3f-02"} {
		for _, r := range roundsOf(c, dev) {
			if r.Skipped {
				t.Fatalf("%s 第 %d 轮不该跳过", dev, r.Cycle)
			}
			if r.Rows != len(c.LastPorts[dev]) {
				t.Fatalf("%s 第 %d 轮行数 = %d", dev, r.Cycle, r.Rows)
			}
		}
	}
}

// -reps 顶到 64：B/C 档设备回 tooBig，采集器砍半退到设备能力上限。
func TestRepsOverrideBacksOff(t *testing.T) {
	c := collectOnce(t, "samples/fleet.json", 1, func(cfg *Config) { cfg.RepsOverride = 64 })
	cases := []struct {
		dev      string
		tooBig   int
		finalRep int
	}{
		{"tor-a3f-01", 2, 16}, // 64→32→16
		{"tor-a3f-02", 3, 8},  // 64→32→16→8
		{"spine-01", 0, 64},   // A 档本来就是 64
	}
	for _, want := range cases {
		r := roundsOf(c, want.dev)[0]
		if r.Stats.TooBig != want.tooBig {
			t.Fatalf("%s 的 tooBig 次数 = %d，期望 %d", want.dev, r.Stats.TooBig, want.tooBig)
		}
		s := sessionOf(t, c, want.dev)
		if s.Reps != want.finalRep {
			t.Fatalf("%s 最终条数 = %d，期望 %d", want.dev, s.Reps, want.finalRep)
		}
		if r.Rows != len(s.Dev.Interfaces) {
			t.Fatalf("%s 背退之后应采全 %d 行，实际 %d", want.dev, len(s.Dev.Interfaces), r.Rows)
		}
	}
}

func TestClassify(t *testing.T) {
	cases := []struct {
		name  string
		res   CycleResult
		state DevState
	}{
		{"零行就是不可达", CycleResult{Rows: 0, Stats: WalkStats{Requests: 2, Timeouts: 2}}, StateUnreachable},
		{"有超时但有数据算慢", CycleResult{Rows: 5, Stats: WalkStats{Requests: 4, Timeouts: 1, Latency: 400}}, StateSlow},
		{"往返超 500ms 算慢", CycleResult{Rows: 5, Stats: WalkStats{Requests: 4, Latency: 2400}}, StateSlow},
		{"全正常", CycleResult{Rows: 24, Stats: WalkStats{Requests: 12, Latency: 540}}, StateOK},
	}
	for _, c := range cases {
		if got := classify(c.res); got != c.state {
			t.Fatalf("%s：classify = %s，期望 %s", c.name, got, c.state)
		}
	}
}

func TestConfigValidate(t *testing.T) {
	bad := []Config{
		{Cycles: 0, Interval: time.Second},
		{Cycles: 1, Interval: 0},
		{Cycles: 1, Interval: time.Second, RepsOverride: -1},
		{Cycles: 1, Interval: time.Second, TimeoutOverride: -time.Second},
	}
	for i, cfg := range bad {
		if err := cfg.Validate(); err == nil {
			t.Fatalf("第 %d 个非法配置应该报错: %+v", i, cfg)
		}
	}
	if err := (Config{Cycles: 1, Interval: time.Second}).Validate(); err != nil {
		t.Fatalf("合法配置不该报错: %v", err)
	}
}

// 档位认不出来就整场失败，不许静默按 A 档打。
func TestUnknownTierFailsFast(t *testing.T) {
	f := &Fleet{Devices: []Device{{
		Name: "x", SNMP: AgentProfile{Tier: "Z", MaxRepetitions: 8},
		Interfaces: []Interface{{IfIndex: 1, IfName: "Ethernet1/1"}},
	}}}
	_, err := NewCollector(f, NewSampleTransport(f, 1), NewVirtualClock(time.Now()),
		Config{Cycles: 1, Interval: time.Second})
	if err == nil {
		t.Fatal("未知档位应该报错")
	}
}
