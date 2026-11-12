package main

import (
	"os"
	"testing"
)

func seriesOf(name string, points []Point, faults []Span) Series {
	return Series{Name: name, Unit: "Mbps", Points: points, Faults: faults}
}

func seq(start, step int64, vals ...float64) []Point {
	out := make([]Point, len(vals))
	for i, v := range vals {
		out[i] = Point{T: start + int64(i)*step, V: v}
	}
	return out
}

func TestThresholdHitsFoldRuns(t *testing.T) {
	s := seriesOf("x", seq(0, 30, 10, 90, 92, 88, 20, 91), nil)
	got := thresholdHits(s, defaultRulePolicy)
	want := []Span{{From: 30, To: 90}, {From: 150, To: 150}}
	if len(got) != len(want) {
		t.Fatalf("命中段 %v，想要 %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("第 %d 段 %v，想要 %v", i, got[i], want[i])
		}
	}
}

// 持续时长规则的命中时刻是「段首 + 时长」，不是段首。
// 取段首等于自己骗自己：那一刻根本还不知道它是持续的还是尖刺。
func TestSustainedHitStartsAfterDuration(t *testing.T) {
	// 30 秒超线，一直撑到 300 秒。
	s := seriesOf("x", seq(0, 30, 10, 10, 90, 91, 92, 93, 94, 95, 96, 97, 98, 97), nil)
	got := sustainedHits(s, defaultRulePolicy)
	if len(got) != 1 {
		t.Fatalf("命中段 %v", got)
	}
	if got[0].From != 60+180 {
		t.Fatalf("命中时刻 %d，想要段首 60 + 时长 180 = 240", got[0].From)
	}

	// 只超线一个点，撑不满时长，不该命中。
	spike := seriesOf("y", seq(0, 30, 10, 94, 10), nil)
	if hits := sustainedHits(spike, defaultRulePolicy); len(hits) != 0 {
		t.Fatalf("单点尖刺被判成持续故障：%v", hits)
	}
}

// 跨采集中断算斜率得出的数既不是故障也不是正常，必须跳过。
func TestRateSkipsSamplingGap(t *testing.T) {
	// 正常时刻：240 秒后断到 540 秒，值从 3 跳到 95。
	points := []Point{{0, 3}, {30, 3}, {240, 3}, {540, 95}, {570, 95}}
	s := seriesOf("x", points, nil)

	if hits := rateHits(s, defaultRulePolicy); len(hits) != 0 {
		t.Fatalf("跨断点算出了斜率：%v", hits)
	}

	// 把断点上限抬到 400 秒，并降门槛，同一个跳变就算出了斜率。
	// 这个斜率是真的跨了 300 秒算出来的，与中间到底发生什么无关。
	loose := defaultRulePolicy
	loose.MaxGapS = 400
	loose.RateLimit = 0.3
	hits := rateHits(s, loose)
	if len(hits) != 1 || hits[0].From != 540 {
		t.Fatalf("抬宽断点上限后命中 %v，想要一段在 540", hits)
	}
}

func TestRateDetectsStep(t *testing.T) {
	s := seriesOf("x", seq(0, 30, 60, 60, 95, 96), nil)
	hits := rateHits(s, defaultRulePolicy)
	if len(hits) != 1 || hits[0].From != 60 {
		t.Fatalf("阶跃命中 %v，想要一段在 60", hits)
	}
}

// 一个故障窗里没有命中段就是漏报；命中段的起点落在窗外就是误报。
func TestScoreAgainstFaultWindows(t *testing.T) {
	s := seriesOf("x", seq(0, 30, 10, 94, 10, 10, 95, 95), []Span{{From: 30, To: 30}, {From: 120, To: 150}})
	sc := scoreOf(s, RuleThreshold, thresholdHits(s, defaultRulePolicy), defaultRulePolicy)

	if sc.Hits != 2 || sc.OnTime != 2 || sc.FalseAlarm != 0 || sc.Missed != 0 {
		t.Fatalf("评分 %+v，想要 2 段全落在窗内", sc)
	}
	if sc.FirstDelayS != 0 {
		t.Fatalf("首报延迟 %d，想要 0", sc.FirstDelayS)
	}

	// 只留第二个窗作为真故障，第一段命中就成了误报。
	s2 := seriesOf("x", s.Points, []Span{{From: 120, To: 150}})
	sc2 := scoreOf(s2, RuleThreshold, thresholdHits(s2, defaultRulePolicy), defaultRulePolicy)
	if sc2.FalseAlarm != 1 || sc2.OnTime != 1 {
		t.Fatalf("评分 %+v，想要 1 误报 1 及时", sc2)
	}
}

func TestEvaluateOnSamples(t *testing.T) {
	series, err := loadSeries("samples/metrics.jsonl")
	if err != nil {
		t.Fatalf("读样本: %v", err)
	}
	rep := Evaluate(series, defaultRulePolicy)

	if rep.Series != 5 {
		t.Fatalf("序列 %d 条，样本里是 5 条", rep.Series)
	}
	if rep.Interval != 30 {
		t.Fatalf("采样间隔 %d，样本里是 30 秒", rep.Interval)
	}

	sum := map[string]RuleSummary{}
	for _, s := range rep.Summary {
		sum[s.Rule] = s
	}

	// 漂移那条：三条规则全漏。这是静态阈值的死穴，也是第 39 课的入口。
	pkt := findScore(t, rep, "tor-a3f-01:Ethernet1/48:pkt_loss_pct", RuleThreshold)
	if pkt.Missed != 1 || pkt.Hits != 0 {
		t.Fatalf("漂移序列上阈值规则命中 %d 段、漏 %d 段，想要 0 和 1", pkt.Hits, pkt.Missed)
	}

	// 尖刺那条：阈值规则必误报，持续时长规则不上当。
	cpu := findScore(t, rep, "spine-01:cpu_util_pct", RuleThreshold)
	if cpu.FalseAlarm != 1 {
		t.Fatalf("尖刺序列上阈值规则误报 %d 段，想要 1", cpu.FalseAlarm)
	}
	cpuSustained := findScore(t, rep, "spine-01:cpu_util_pct", RuleSustained)
	if cpuSustained.Hits != 0 {
		t.Fatalf("尖刺序列上持续时长规则命中 %d 段，想要 0", cpuSustained.Hits)
	}

	// 断点那条：变化率规则因为采集中断完全没看到故障。
	errSeries := findScore(t, rep, "tor-a3f-04:Ethernet1/1:rx_error_pps", RuleRate)
	if errSeries.Missed != 1 {
		t.Fatalf("断点序列上变化率规则漏 %d 段，想要 1", errSeries.Missed)
	}

	if sum[RuleSustained].Missed <= sum[RuleThreshold].Missed {
		t.Fatalf("持续时长规则的漏报 %d 条不该少于阈值的 %d 条",
			sum[RuleSustained].Missed, sum[RuleThreshold].Missed)
	}
}

func findScore(t *testing.T, rep RuleReport, series, rule string) RuleScore {
	t.Helper()
	for _, sc := range rep.Scores {
		if sc.Series == series && sc.Rule == rule {
			return sc
		}
	}
	t.Fatalf("没有 %s × %s 的评分", series, rule)
	return RuleScore{}
}

func TestLoadSeriesRejectsBadPoints(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/m.jsonl"
	write := func(body string) {
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatalf("写样本失败: %v", err)
		}
	}

	write(`{"series":"a","points":[[0,1],[30,2]]}` + "\n")
	if _, err := loadSeries(path); err != nil {
		t.Fatalf("正常样本被拒: %v", err)
	}

	write(`{"series":"a","points":[[30,1],[30,2]]}` + "\n")
	if _, err := loadSeries(path); err == nil {
		t.Fatal("时间没递增应当拒收")
	}

	write(`{"series":"a","points":[[0,1]]}` + "\n")
	if _, err := loadSeries(path); err == nil {
		t.Fatal("只有一个点应当拒收（连斜率都算不出来）")
	}

	write(`{"series":"a","points":[[0,1,2],[30,2,3]]}` + "\n")
	if _, err := loadSeries(path); err == nil {
		t.Fatal("三列采样点应当拒收")
	}

	write(`{"series":"a","points":[[0,1],[30,2]],"faults":[[60,30]]}` + "\n")
	if _, err := loadSeries(path); err == nil {
		t.Fatal("终点早于起点的故障窗应当拒收")
	}
}
