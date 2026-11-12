package main

import (
	"math"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestMinObservablePPS(t *testing.T) {
	cases := []struct {
		k, n   int
		window float64
		want   float64
	}{
		{1, 1000, 60, 1000 / 60.0},
		{5, 1000, 60, 5 * 1000 / 60.0},
		{1, 100, 60, 100 / 60.0},
		{1, 10000, 60, 10000 / 60.0},
		{1, 1000, 300, 1000 / 300.0},
	}
	for _, c := range cases {
		got := MinObservablePPS(c.k, c.n, c.window)
		if math.Abs(got-c.want) > 1e-9 {
			t.Errorf("MinObservablePPS(%d,%d,%v) = %v, 期望 %v", c.k, c.n, c.window, got, c.want)
		}
	}
	if got := MinObservablePPS(1, 1000, 0); got != 0 {
		t.Errorf("窗口为 0 时应当返回 0，实际 %v", got)
	}
	if got := MinObservablePPS(0, 1000, 60); got != 0 {
		t.Errorf("k 为 0 时应当返回 0，实际 %v", got)
	}
}

func TestLoadTruthSampleFile(t *testing.T) {
	truth, err := loadTruth(filepath.Join("samples", "flows-truth.json"))
	if err != nil {
		t.Fatal(err)
	}
	flows, err := truth.synthesize()
	if err != nil {
		t.Fatal(err)
	}
	if len(flows) != 184 {
		t.Fatalf("真值展开后应当有 184 条流，实际 %d", len(flows))
	}
	if flows[4].Name != "mice-batch-001" || flows[4].SrcIP != "10.40.0.1" {
		t.Errorf("第一条老鼠 = %+v", flows[4])
	}
	// 最大的一条流包速率 25000，180 条老鼠各 12，总量对得上。
	var pps float64
	for _, f := range flows {
		pps += f.PPS
	}
	if math.Abs(pps-27820) > 1e-9 {
		t.Errorf("总包速率 = %v，期望 27820", pps)
	}
}

func TestSynthesizeRejectsDuplicateKey(t *testing.T) {
	truth := TruthSet{
		WindowSeconds: 60, MeanPacketBytes: 1000,
		Flows: []TruthFlow{
			{Name: "a", SrcIP: "10.0.0.1", DstIP: "10.0.0.2", Proto: "tcp", PPS: 10},
			{Name: "b", SrcIP: "10.0.0.1", DstIP: "10.0.0.2", Proto: "tcp", PPS: 20},
		},
	}
	if _, err := truth.synthesize(); err == nil || !strings.Contains(err.Error(), "五元组相同") {
		t.Fatalf("五元组重复应当报错，实际 %v", err)
	}
}

func TestFanoutRejectsCrossingSubnet(t *testing.T) {
	if _, err := fanoutIPs("10.40.0.250", 10); err == nil {
		t.Fatal("跨 /24 的 fanout 应当报错")
	}
	ips, err := fanoutIPs("10.40.0.0", 3)
	if err != nil {
		t.Fatal(err)
	}
	if ips[0] != "10.40.0.1" || ips[2] != "10.40.0.3" {
		t.Errorf("展开结果 = %v", ips)
	}
}

func TestTruthValidation(t *testing.T) {
	base := TruthSet{WindowSeconds: 60, MeanPacketBytes: 1000,
		Flows: []TruthFlow{{Name: "a", SrcIP: "10.0.0.1", DstIP: "10.0.0.2", PPS: 1}}}
	if err := base.validate(); err != nil {
		t.Fatalf("合法真值被拒: %v", err)
	}

	cases := []struct {
		name   string
		mutate func(*TruthSet)
		want   string
	}{
		{"窗口为 0", func(s *TruthSet) { s.WindowSeconds = 0 }, "window_seconds"},
		{"包长为 0", func(s *TruthSet) { s.MeanPacketBytes = 0 }, "mean_packet_bytes"},
		{"没有流", func(s *TruthSet) { s.Flows = nil }, "flows"},
		{"缺名字", func(s *TruthSet) { s.Flows[0].Name = "" }, "name"},
		{"负的 pps", func(s *TruthSet) { s.Flows[0].PPS = -1 }, "负数"},
		{"fanout 为 0 条", func(s *TruthSet) { s.Fanout = &Fanout{Count: 0} }, "fanout.count"},
		{"fanout 没有前缀", func(s *TruthSet) {
			s.Fanout = &Fanout{Count: 2, SrcIPBase: "10.40.0.0"}
		}, "fanout.prefix"},
	}
	for _, c := range cases {
		s := base
		s.Flows = append([]TruthFlow{}, base.Flows...)
		c.mutate(&s)
		err := s.validate()
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: 错误应当含「%s」，实际 %v", c.name, c.want, err)
		}
	}
}

// 采样模拟跑一次要几十万次掷骰子，几个用例共用一份结果。
var (
	simOnce sync.Once
	simRes  Simulation
	simErr  error
)

func sampleSimulation(t *testing.T) Simulation {
	t.Helper()
	simOnce.Do(func() {
		truth, err := loadTruth(filepath.Join("samples", "flows-truth.json"))
		if err != nil {
			simErr = err
			return
		}
		simRes, simErr = Simulate(truth, []int{100, 1000, 10000}, 5, 42)
	})
	if simErr != nil {
		t.Fatal(simErr)
	}
	return simRes
}

// 这一条是实验的核心断言：同一份真值，三个采样率，TopN 从「基本可信」
// 一路烂到「榜上一半是噪声」。
func TestSimulateTopNDegradesWithSamplingRate(t *testing.T) {
	sim := sampleSimulation(t)
	if len(sim.Rates) != 3 {
		t.Fatalf("应当跑 3 个采样率，实际 %d", len(sim.Rates))
	}
	lo, mid, hi := sim.Rates[0], sim.Rates[1], sim.Rates[2]

	// 大象流在任何采样率下都排第一：它是唯一一条包速率远超可见性阈值的流。
	for _, r := range sim.Rates {
		if r.ElephantName != "elephant-backup" {
			t.Fatalf("1:%d 的大象应当是 elephant-backup，实际 %q", r.N, r.ElephantName)
		}
		if r.Flows[0].EstRank != 1 || r.Flows[0].Name != "elephant-backup" {
			t.Fatalf("1:%d 的估算第一名应当是 elephant-backup，实际 %q", r.N, r.Flows[0].Name)
		}
		if r.ElephantErr > 0.15 {
			t.Errorf("1:%d 下大象估算误差 %.2f%% 偏大", r.N, r.ElephantErr*100)
		}
	}

	// 可见流数随采样率下降：1:100 几乎全见，1:10000 只剩零头。
	if !(lo.Visible > mid.Visible && mid.Visible > hi.Visible) {
		t.Fatalf("可见流数应当随采样率单调下降，实际 %d / %d / %d", lo.Visible, mid.Visible, hi.Visible)
	}
	zeroLo, zeroHi := lo.Total-lo.Visible, hi.Total-hi.Visible
	if zeroLo > 20 || zeroHi < 150 {
		t.Errorf("一个样本都没抽到的流数：1:100 %d 条，1:10000 %d 条", zeroLo, zeroHi)
	}

	// TopN 的成色：与真值 Top5 的交集在 1:10000 时缩水。
	if hi.TopNOverlap >= lo.TopNOverlap {
		t.Errorf("1:10000 的 TopN 交集 %d 应当少于 1:100 的 %d", hi.TopNOverlap, lo.TopNOverlap)
	}

	// 榜单上出现真值排在十名之外的流，就是采样噪声冒充大象。
	noise := func(r RateResult) int {
		n := 0
		for _, f := range r.Flows {
			if f.EstRank <= r.TopN && f.TrueRank > 10 {
				n++
			}
		}
		return n
	}
	if noise(hi) < 2 {
		t.Errorf("1:10000 的 Top5 里应当至少 2 条噪声，实际 %d", noise(hi))
	}
	if noise(hi) <= noise(lo) {
		t.Errorf("噪声数应当随采样率上升：1:100 %d 条，1:10000 %d 条", noise(lo), noise(hi))
	}

	// 排名具体变了哪几条：1:100 与 1:10000 的 Top5 名单不同。
	if topNames(lo) == topNames(hi) {
		t.Errorf("1:100 与 1:10000 的 Top5 应当不同，都是 %s", topNames(lo))
	}
}

// topNames 把 TopN 名单拼成字符串，方便断言「名单变了」。
func topNames(r RateResult) string {
	names := make([]string, 0, r.TopN)
	for _, f := range r.Flows {
		if f.EstRank <= r.TopN {
			names = append(names, f.Name)
		}
	}
	return strings.Join(names, ",")
}

// 可见性阈值：包速率低于 r_min = N/T 的流，TopN 上根本不应该出现。
func TestSimulateInvisibleBelowThreshold(t *testing.T) {
	truth, err := loadTruth(filepath.Join("samples", "flows-truth.json"))
	if err != nil {
		t.Fatal(err)
	}
	// 一条 1 包/秒的小流，在 1:100 下 60 秒能抽到 60 个样本，1:10000 下只有 0.006 个。
	truth.Flows = []TruthFlow{
		{Name: "elephant", SrcIP: "10.20.0.10", DstIP: "10.30.9.9", Proto: "tcp", SrcPort: 1, DstPort: 2, PPS: 5000},
		{Name: "tiny", SrcIP: "10.20.0.11", DstIP: "10.30.9.9", Proto: "tcp", SrcPort: 1, DstPort: 2, PPS: 1},
	}
	truth.Fanout = nil

	sim, err := Simulate(truth, []int{100, 10000}, 2, 7)
	if err != nil {
		t.Fatal(err)
	}
	lo, hi := sim.Rates[0], sim.Rates[1]
	if lo.Visible != 2 {
		t.Fatalf("1:100 下两条流都该可见，实际可见 %d 条", lo.Visible)
	}
	if hi.Visible != 1 {
		t.Fatalf("1:10000 下 1 包/秒的流不该有样本，实际可见 %d 条", hi.Visible)
	}
	// 1:10000 的阈值是 166.7 包/秒，5000 包/秒的大象远在阈值之上。
	if hi.MinObservablePPS <= 100 || hi.MinObservablePPS >= 200 {
		t.Errorf("1:10000 的最小可观测速率 = %v 包/秒，期望约 166.7", hi.MinObservablePPS)
	}
}

func TestSimulateIsDeterministic(t *testing.T) {
	truth := TruthSet{
		WindowSeconds: 60, MeanPacketBytes: 1000,
		Flows: []TruthFlow{
			{Name: "a", SrcIP: "10.0.0.1", DstIP: "10.0.0.9", Proto: "tcp", SrcPort: 1, DstPort: 2, PPS: 900},
			{Name: "b", SrcIP: "10.0.0.2", DstIP: "10.0.0.9", Proto: "tcp", SrcPort: 1, DstPort: 2, PPS: 40},
		},
	}
	first, err := Simulate(truth, []int{1000}, 2, 42)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Simulate(truth, []int{1000}, 2, 42)
	if err != nil {
		t.Fatal(err)
	}
	if first.Rates[0].SampledPackets != second.Rates[0].SampledPackets {
		t.Fatalf("同一个 seed 抽出来的样本数应当一致：%d vs %d",
			first.Rates[0].SampledPackets, second.Rates[0].SampledPackets)
	}
}

func TestSimulateRejectsBadInput(t *testing.T) {
	truth := TruthSet{WindowSeconds: 60, MeanPacketBytes: 1000,
		Flows: []TruthFlow{{Name: "a", SrcIP: "10.0.0.1", DstIP: "10.0.0.2", PPS: 1}}}
	if _, err := Simulate(truth, []int{100}, 0, 1); err == nil {
		t.Error("topN 为 0 应当报错")
	}
	if _, err := Simulate(truth, nil, 5, 1); err == nil {
		t.Error("采样率列表为空应当报错")
	}
	if _, err := Simulate(truth, []int{0}, 5, 1); err == nil {
		t.Error("采样率 1:0 应当报错")
	}
}

func TestParseRates(t *testing.T) {
	rates, err := parseRates("100, 1000,10000")
	if err != nil {
		t.Fatal(err)
	}
	if len(rates) != 3 || rates[0] != 100 || rates[2] != 10000 {
		t.Fatalf("解析结果 = %v", rates)
	}
	for _, bad := range []string{"", "abc", "0", "1000,100", "-5"} {
		if _, err := parseRates(bad); err == nil {
			t.Errorf("parseRates(%q) 应当报错", bad)
		}
	}
}
