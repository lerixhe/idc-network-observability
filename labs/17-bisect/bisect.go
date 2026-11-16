package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
)

// 五层链路的故障段定位。
//
// 一条链路按顺序切成若干段，每段都有测量样本。故障只会落在其中一段，
// 「切分次数」是探测次数，「总耗时」是这些探测的成本之和。三种策略的差别不在结论，
// 而在拿到结论要付多少探测代价。
//
// 顺序扫：从入口往对端逐段点名，命中即停。结论最稳，代价与故障位置成正比。
// 对半二分：探「前缀是否健康」，一次探测覆盖一半链路，把范围砍半。
// 成本加权：同样探前缀，但每次挑切点不取中点，取「按剩余段数加权后期望代价最小」的点。
// 核心是前缀探测比单段探测贵得多，切点该往前压，而不是机械砍半。

// Baseline 是健康时的每段基线。
type Baseline struct {
	LatencyMs float64 `json:"latencyMs"`
	LossPct   float64 `json:"lossPct"`
}

// Sample 是一段的测量样本。Covered 为 false 表示这段没有探针，测不出东西。
type Sample struct {
	LatencyMs   float64 `json:"latencyMs"`
	LossPct     float64 `json:"lossPct"`
	Covered     bool    `json:"covered"`
	ProbeCostMs int     `json:"probeCostMs"`
}

// Case 是一个已注入故障的场景。
type Case struct {
	Name       string   `json:"name"`
	FaultIndex int      `json:"faultIndex"`
	Samples    []Sample `json:"samples"`
}

// Fixture 是 samples/case.json 的结构。
type Fixture struct {
	Baseline           Baseline `json:"baseline"`
	PerProbeOverheadMs int      `json:"perProbeOverheadMs"`
	Segments           []string `json:"segments"`
	Cases              []Case   `json:"cases"`
}

// 判定阈值：时延超基线 2 倍，或丢包超 1%，都算异常。
const (
	latencyFactor = 2.0
	lossCeiling   = 1.0
)

type legState int

const (
	legHealthy legState = iota
	legAnomalous
	legUncovered
)

// prober 记账：每次探测的段成本 + 固定启动开销都落进 spentMs。
type prober struct {
	baseline   Baseline
	samples    []Sample
	overheadMs int
	probes     int
	spentMs    int
}

func (p *prober) classify(i int) legState {
	s := p.samples[i]
	if !s.Covered {
		return legUncovered
	}
	if s.LatencyMs > p.baseline.LatencyMs*latencyFactor || s.LossPct > lossCeiling {
		return legAnomalous
	}
	return legHealthy
}

// probeLeg 单段探测，成本就是这一段的探针成本。
func (p *prober) probeLeg(i int) legState {
	p.probes++
	p.spentMs += p.samples[i].ProbeCostMs + p.overheadMs
	return p.classify(i)
}

// probePrefix 探「入口到第 i 段」整条前缀，成本是沿途各段之和。
// 覆盖缺口返回 legUncovered，异常优先于缺口返回。
func (p *prober) probePrefix(i int) legState {
	cost := 0
	for k := 0; k <= i; k++ {
		cost += p.samples[k].ProbeCostMs
	}
	p.probes++
	p.spentMs += cost + p.overheadMs

	state := legHealthy
	for k := 0; k <= i; k++ {
		switch p.classify(k) {
		case legAnomalous:
			return legAnomalous
		case legUncovered:
			state = legUncovered
		}
	}
	return state
}

// Result 是单次定位的产出。
type Result struct {
	Strategy   string
	FoundIndex int
	Found      bool
	NeedProbe  bool
	Holes      []string
	Probes     int
	TotalMs    int
}

func (r Result) verdict(segments []string) string {
	switch {
	case r.Found:
		return segments[r.FoundIndex] + " 异常"
	case r.NeedProbe:
		return "需补探针（" + strings.Join(r.Holes, "、") + " 无覆盖）"
	default:
		return "未发现故障段"
	}
}

// holes 列出所有没有覆盖的段。
func holes(segments []string, samples []Sample) []string {
	var out []string
	for i, s := range samples {
		if !s.Covered {
			out = append(out, segments[i])
		}
	}
	return out
}

// locateSequential 顺序扫：逐段点名，第一个异常段就是答案，先撞缺口就报需补探针。
func locateSequential(segments []string, samples []Sample, base Baseline, overhead int) Result {
	p := &prober{baseline: base, samples: samples, overheadMs: overhead}
	res := Result{Strategy: "顺序扫"}
	for i := range samples {
		switch p.probeLeg(i) {
		case legAnomalous:
			res.Found, res.FoundIndex = true, i
			res.Probes, res.TotalMs = p.probes, p.spentMs
			return res
		case legUncovered:
			res.NeedProbe, res.Holes = true, holes(segments, samples)
			res.Probes, res.TotalMs = p.probes, p.spentMs
			return res
		}
	}
	res.Probes, res.TotalMs = p.probes, p.spentMs
	return res
}

// confirm 收尾复核：二分与成本加权都只定位到「前缀从哪段开始异常」，
// 还得单段探一次，才能区分「这段异常」和「这段没覆盖、判断不了」。
func confirm(segments []string, samples []Sample, p *prober, idx int) Result {
	res := Result{Probes: p.probes, TotalMs: p.spentMs}
	switch p.probeLeg(idx) {
	case legAnomalous:
		res.Found, res.FoundIndex = true, idx
	case legUncovered:
		res.NeedProbe, res.Holes = true, holes(segments, samples)
	default:
		if h := holes(segments, samples); len(h) > 0 {
			res.NeedProbe, res.Holes = true, h
		}
	}
	res.Probes, res.TotalMs = p.probes, p.spentMs
	return res
}

// locateBisect 对半二分：探前缀中点是异常就往左收，健康就往右收。
func locateBisect(segments []string, samples []Sample, base Baseline, overhead int) Result {
	p := &prober{baseline: base, samples: samples, overheadMs: overhead}
	if len(samples) == 0 {
		return Result{Strategy: "对半二分"}
	}
	lo, hi := 0, len(samples)-1
	for lo < hi {
		mid := (lo + hi) / 2
		if p.probePrefix(mid) == legAnomalous {
			hi = mid
		} else {
			lo = mid + 1
		}
	}
	out := confirm(segments, samples, p, lo)
	out.Strategy = "对半二分"
	return out
}

// locateCostWeighted 成本加权：切点取「当前前缀代价 + 按剩余段数加权的期望代价」最小的位置。
func locateCostWeighted(segments []string, samples []Sample, base Baseline, overhead int) Result {
	p := &prober{baseline: base, samples: samples, overheadMs: overhead}
	n := len(samples)
	if n == 0 {
		return Result{Strategy: "成本加权"}
	}
	// 前缀探测只付一次启动开销，单段探测每段都要付。
	prefixCost := make([]int, n)
	sum := overhead
	for i := 0; i < n; i++ {
		sum += samples[i].ProbeCostMs
		prefixCost[i] = sum
	}
	legCost := make([]float64, n)
	for i := 0; i < n; i++ {
		legCost[i] = float64(samples[i].ProbeCostMs + overhead)
	}
	best := planCosts(prefixCost, legCost)

	lo, hi := 0, n-1
	for lo < hi {
		k := best(lo, hi)
		if p.probePrefix(k) == legAnomalous {
			hi = k
		} else {
			lo = k + 1
		}
	}
	out := confirm(segments, samples, p, lo)
	out.Strategy = "成本加权"
	return out
}

// planCosts 用区间 DP 算每个区间的最优切点，返回「给定区间查切点」的函数。
func planCosts(prefixCost []int, legCost []float64) func(lo, hi int) int {
	n := len(prefixCost)
	memo := make(map[[2]int]float64)
	choice := make(map[[2]int]int)

	var est func(lo, hi int) float64
	est = func(lo, hi int) float64 {
		if lo >= hi {
			return 0
		}
		key := [2]int{lo, hi}
		if v, ok := memo[key]; ok {
			return v
		}
		total := float64(hi - lo + 1)
		bestCost := -1.0
		bestK := lo
		for k := lo; k < hi; k++ {
			left := float64(k-lo+1) / total
			right := float64(hi-k) / total
			c := float64(prefixCost[k]) + left*est(lo, k) + right*est(k+1, hi)
			if bestCost < 0 || c < bestCost {
				bestCost, bestK = c, k
			}
		}
		memo[key], choice[key] = bestCost, bestK
		return bestCost
	}
	est(0, n-1)
	return func(lo, hi int) int {
		if lo >= hi {
			return lo
		}
		if k, ok := choice[[2]int{lo, hi}]; ok {
			return k
		}
		return (lo + hi) / 2
	}
}

// locate 跑三种策略。
func locate(segments []string, samples []Sample, base Baseline, overhead int) []Result {
	var out []Result
	out = append(out, locateSequential(segments, samples, base, overhead))
	out = append(out, locateBisect(segments, samples, base, overhead))
	out = append(out, locateCostWeighted(segments, samples, base, overhead))
	return out
}

// LoadFixture 读场景文件。
func LoadFixture(path string) (Fixture, error) {
	var f Fixture
	raw, err := os.ReadFile(path)
	if err != nil {
		return f, fmt.Errorf("读场景文件 %s: %w", path, err)
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		return f, fmt.Errorf("解析场景文件 %s: %w", path, err)
	}
	if len(f.Segments) == 0 {
		return f, fmt.Errorf("场景文件 %s 没有段名", path)
	}
	for _, c := range f.Cases {
		if len(c.Samples) != len(f.Segments) {
			return f, fmt.Errorf("场景 %s 的样本数 %d ≠ 段数 %d", c.Name, len(c.Samples), len(f.Segments))
		}
	}
	return f, nil
}

// printCase 打一个场景的三行对比。
func printCase(w *strings.Builder, segments []string, c Case, results []Result) {
	fmt.Fprintf(w, "场景：%s（注入故障段：%s）\n", c.Name, truth(segments, c.FaultIndex))
	fmt.Fprintf(w, "  %-8s %-8s %-10s %s\n", "策略", "探测次数", "总耗时", "结论")
	for _, r := range results {
		fmt.Fprintf(w, "  %-8s %-8d %-10s %s\n", r.Strategy, r.Probes, fmt.Sprintf("%dms", r.TotalMs), r.verdict(segments))
	}
	w.WriteString("\n")
}

func truth(segments []string, idx int) string {
	if idx < 0 || idx >= len(segments) {
		return "无"
	}
	return segments[idx]
}

func main() {
	path := flag.String("file", "samples/case.json", "场景文件路径")
	flag.Parse()

	f, err := LoadFixture(*path)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "五层链路故障段定位：%d 段，基线时延 %.1fms、丢包 %.2f%%\n\n",
		len(f.Segments), f.Baseline.LatencyMs, f.Baseline.LossPct)
	for _, c := range f.Cases {
		printCase(&b, f.Segments, c, locate(f.Segments, c.Samples, f.Baseline, f.PerProbeOverheadMs))
	}
	fmt.Print(b.String())
}
