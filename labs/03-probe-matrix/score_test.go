package main

import (
	"math"
	"testing"
)

// gridMatrix 造一个 n 个节点两两互探的矩阵。归因逻辑要靠行/列失败率判定，
// 用真实拓扑反而不好控制比例，这里把小矩阵搭好，比例一目了然。
func gridMatrix(names ...string) *Matrix {
	m := &Matrix{Nodes: names}
	for _, a := range names {
		for _, b := range names {
			if a == b {
				continue
			}
			m.OrderedPairs++
			m.Pairs = append(m.Pairs, ProbePair{
				Src: a, Dst: b, Weight: 1, Hops: 2,
				Path: []string{linkKey(a, b)},
			})
		}
	}
	return m
}

func okResult(src, dst string, sent int) ProbeResult {
	return ProbeResult{Src: src, Dst: dst, Sent: sent, Recv: sent, P50us: 100, P99us: 120, Jitter: 8, Baseus: 100, TS: "t0"}
}

func lostResult(src, dst string, sent int) ProbeResult {
	return ProbeResult{Src: src, Dst: dst, Sent: sent, Recv: 0, Baseus: 100, TS: "t0"}
}

// verdictOf 从归因结果里取某一对的结论。
func verdictOf(a Attribution, src, dst string) string {
	for _, s := range a.Scores {
		if s.Src == src && s.Dst == dst {
			return s.Verdict
		}
	}
	return ""
}

func TestLossScoreTiers(t *testing.T) {
	// 60 个包：1 个包是 1.67%，2 个是 3.33%。
	cases := []struct {
		name string
		loss float64
		sent int
		want float64
	}{
		{"一个包都没丢", 0, 60, 100},
		{"丢一个包，落在噪声里", 1.0 / 60, 60, 85},
		{"丢两个包，刚过 2% 门限", 2.0 / 60, 60, 40},
		{"2% 以内还是「有点丢」", 0.019, 60, 70},
		{"刚到 2% 是一对失败的起点", 0.021, 60, 40},
		{"12% 丢包", 0.12, 60, 0},
		{"没发包没有分数", 0, 0, dimMissing},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := lossScore(c.loss, c.sent); got != c.want {
				t.Fatalf("loss=%v sent=%d → %v，期望 %v", c.loss, c.sent, got, c.want)
			}
		})
	}
}

func TestLatencyAndJitterScore(t *testing.T) {
	if got := latencyScore(120, 100); got != 100 {
		t.Fatalf("比值 1.2 应该满分，得到 %v", got)
	}
	if got := latencyScore(300, 100); got != 0 {
		t.Fatalf("比值 3.0 应该零分，得到 %v", got)
	}
	mid := latencyScore(217, 100) // 比值 2.17
	if mid <= 0 || mid >= 100 {
		t.Fatalf("中间比值应该落在开区间，得到 %v", mid)
	}
	if got := latencyScore(120, 0); got != dimMissing {
		t.Fatalf("没基线应该返回 dimMissing，得到 %v", got)
	}

	if got := jitterScore(8, 100); got != 100 {
		t.Fatalf("抖动占比 8%% 应该满分，得到 %v", got)
	}
	if got := jitterScore(80, 100); got != 0 {
		t.Fatalf("抖动占比 80%% 应该零分，得到 %v", got)
	}
	if got := jitterScore(0, 0); got != dimMissing {
		t.Fatalf("没 p50 应该返回 dimMissing，得到 %v", got)
	}

	if got := coverageScore(60, 60); got != 100 {
		t.Fatalf("样本齐应该满分，得到 %v", got)
	}
	if got := coverageScore(30, 60); math.Abs(got-50) > 1e-9 {
		t.Fatalf("半份样本应该 50 分，得到 %v", got)
	}
	if got := coverageScore(0, 60); got != 0 {
		t.Fatalf("没有样本应该零分，得到 %v", got)
	}
}

func TestWeightedScoreRenormalizes(t *testing.T) {
	// 缺数据的维度不参与，权重按剩下的归一。不能把「不知道」算成 0 分。
	withMissing := weightedScore([]Dim{
		{Name: "loss", Score: 100, Weight: 0.4},
		{Name: "latency", Score: dimMissing, Weight: 0.3},
		{Name: "jitter", Score: 100, Weight: 0.15},
		{Name: "coverage", Score: 100, Weight: 0.15},
	})
	if math.Abs(withMissing-100) > 1e-9 {
		t.Fatalf("其余维度满分时总分应该还是 100，得到 %v", withMissing)
	}
	if got := weightedScore([]Dim{{Name: "x", Score: dimMissing, Weight: 1}}); got != dimMissing {
		t.Fatalf("全缺数据应该返回 dimMissing，得到 %v", got)
	}

	// 健康分级的三条线。
	if healthLabel(95) != "healthy" || healthLabel(80) != "degraded" || healthLabel(50) != "faulty" || healthLabel(dimMissing) != "no_data" {
		t.Fatal("健康分级门槛不对")
	}
}

func TestScorePairNoData(t *testing.T) {
	p := ProbePair{Src: "a", Dst: "b", Hops: 2}
	s := ScorePair(p, ProbeResult{}, false, 60)
	if s.Score != dimMissing {
		t.Fatalf("没有样本时分数应该是 dimMissing，得到 %v", s.Score)
	}
	if s.Health != "no_data" {
		t.Fatalf("没有样本时 health = %q", s.Health)
	}

	full := ScorePair(p, okResult("a", "b", 60), true, 60)
	if full.Health != "healthy" || full.Score < HealthOK {
		t.Fatalf("干净的一对应该 healthy，得到 %v / %v", full.Score, full.Health)
	}
}

// TestAttributionFourClasses 四个异常类各造一次，确认判据对得上。
func TestAttributionFourClasses(t *testing.T) {
	names := []string{"n1", "n2", "n3", "n4"}

	newResults := func(skip func(src, dst string) bool, override func(src, dst string) ProbeResult) map[string]ProbeResult {
		out := map[string]ProbeResult{}
		for _, a := range names {
			for _, b := range names {
				if a == b || skip(a, b) {
					continue
				}
				if r := override(a, b); r.Sent > 0 {
					out[pairKey(a, b)] = r
				} else {
					out[pairKey(a, b)] = okResult(a, b, 60)
				}
			}
		}
		return out
	}

	t.Run("探针侧异常：行红列绿", func(t *testing.T) {
		m := gridMatrix(names...)
		// n1 的 agent 挂了：它一条都不上报，但别人探它正常。
		res := newResults(func(src, dst string) bool { return src == "n1" }, func(string, string) ProbeResult { return ProbeResult{} })
		a := Classify(m, res, 60)
		if len(a.ProbeSide) != 1 || a.ProbeSide[0] != "n1" {
			t.Fatalf("应该只判 n1 为探针侧异常，得到 %v", a.ProbeSide)
		}
		if v := verdictOf(a, "n1", "n2"); v != VerdictProbe {
			t.Fatalf("n1 发出的探测定性为 %q，期望 %q", v, VerdictProbe)
		}
		if v := verdictOf(a, "n2", "n1"); v != VerdictOK {
			t.Fatalf("别人探 n1 应该正常，得到 %q", v)
		}
	})

	t.Run("主机不可达：行红列也红", func(t *testing.T) {
		m := gridMatrix(names...)
		res := newResults(func(src, dst string) bool { return src == "n1" }, func(src, dst string) ProbeResult {
			if dst == "n1" {
				return lostResult(src, dst, 60)
			}
			return ProbeResult{}
		})
		a := Classify(m, res, 60)
		if len(a.HostDown) != 1 || a.HostDown[0] != "n1" {
			t.Fatalf("应该只判 n1 整机不可达，得到 %v", a.HostDown)
		}
		if len(a.ProbeSide) != 0 {
			t.Fatalf("整机不可达不该被说成探针侧异常：%v", a.ProbeSide)
		}
	})

	t.Run("对端侧异常：列红行绿", func(t *testing.T) {
		m := gridMatrix(names...)
		res := newResults(func(string, string) bool { return false }, func(src, dst string) ProbeResult {
			if dst == "n1" {
				return lostResult(src, dst, 60)
			}
			return ProbeResult{}
		})
		a := Classify(m, res, 60)
		if len(a.TargetSide) != 1 || a.TargetSide[0] != "n1" {
			t.Fatalf("应该只判 n1 为对端侧异常，得到 %v", a.TargetSide)
		}
	})

	t.Run("网络侧异常：单对失败，行列都不红", func(t *testing.T) {
		m := gridMatrix(names...)
		res := newResults(func(string, string) bool { return false }, func(src, dst string) ProbeResult {
			if src == "n1" && dst == "n2" {
				return lostResult(src, dst, 60)
			}
			return ProbeResult{}
		})
		a := Classify(m, res, 60)
		if v := verdictOf(a, "n1", "n2"); v != VerdictNetwork {
			t.Fatalf("单对丢包的定性是 %q，期望 %q", v, VerdictNetwork)
		}
		if a.SharedLink != linkKey("n1", "n2") {
			t.Fatalf("共同链路段 = %q，期望 %q", a.SharedLink, linkKey("n1", "n2"))
		}
		if len(a.ProbeSide)+len(a.HostDown)+len(a.TargetSide) != 0 {
			t.Fatal("一对丢包不该牵连任何节点")
		}
	})
}

// TestProbeOutageVsRealFault 是任务要求的那条对照：整批探针掉线与真故障
// 放在同一个矩阵上，必须给出两种完全不同的结论。
func TestProbeOutageVsRealFault(t *testing.T) {
	names := []string{"s1", "s2", "s3", "s4"}
	m := gridMatrix(names...)

	build := func(override func(src, dst string) *ProbeResult) map[string]ProbeResult {
		out := map[string]ProbeResult{}
		for _, a := range names {
			for _, b := range names {
				if a == b {
					continue
				}
				if r := override(a, b); r != nil {
					out[pairKey(a, b)] = *r
				}
			}
		}
		return out
	}
	ok := func(src, dst string) *ProbeResult { r := okResult(src, dst, 60); return &r }
	lost := func(src, dst string) *ProbeResult { r := lostResult(src, dst, 60); return &r }

	// 情景一：s1、s2 两台探针整批掉线（agent 挂了，主机还在）。矩阵里这两行是空的。
	outage := build(func(src, dst string) *ProbeResult {
		if src == "s1" || src == "s2" {
			return nil
		}
		return ok(src, dst)
	})
	a1 := Classify(m, outage, 60)
	if len(a1.ProbeSide) != 2 {
		t.Fatalf("两台探针掉线应该判出 2 个探针侧异常，得到 %v", a1.ProbeSide)
	}
	for _, s := range a1.Scores {
		if s.Verdict == VerdictNetwork {
			t.Fatalf("探针掉线不该产生网络侧告警：%s", pairKey(s.Src, s.Dst))
		}
	}

	// 情景二：s1→s2 这条路径真丢包，其余全部正常。
	fault := build(func(src, dst string) *ProbeResult {
		if src == "s1" && dst == "s2" {
			return lost(src, dst)
		}
		return ok(src, dst)
	})
	a2 := Classify(m, fault, 60)
	if v := verdictOf(a2, "s1", "s2"); v != VerdictNetwork {
		t.Fatalf("真故障被定性为 %q，期望 %q", v, VerdictNetwork)
	}
	if len(a2.ProbeSide) != 0 {
		t.Fatalf("真故障不该被说成探针侧异常：%v", a2.ProbeSide)
	}

	// 两条断言的合体：同一个矩阵，同一种「探测失败」，结论必须分开。
	if verdictOf(a1, "s1", "s2") == verdictOf(a2, "s1", "s2") {
		t.Fatal("探针掉线与链路丢包给出了同一个结论，实验的核心结论没实现")
	}
}
