package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
)

// 三类规则。同一批数据上，它们的名字回答的是三个不同的问题：
//
//	threshold 现在是不是已经出事
//	sustained 出事了多久
//	rate      变化有多快
//
// 三条都报同一件事的时候，值班人还能忍。三条各报各的时候，
// 就该知道规则本身在打架，不是数据有问题。
const (
	RuleThreshold = "threshold"
	RuleSustained = "sustained"
	RuleRate      = "rate"
)

// Point 是一个采样点。JSON 里写成 [秒, 值]，两列数组比 {"t":..,"v":..} 省一半字节。
type Point struct {
	T int64
	V float64
}

func (p *Point) UnmarshalJSON(b []byte) error {
	var raw []float64
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	if len(raw) != 2 {
		return fmt.Errorf("采样点必须是 [秒, 值] 两列，收到 %d 列", len(raw))
	}
	p.T = int64(raw[0])
	p.V = raw[1]
	return nil
}

// Span 是区间。故障真值和高命中段都用它，单位是相对起点的秒。
type Span struct {
	From int64 `json:"from"`
	To   int64 `json:"to"`
}

func (s *Span) UnmarshalJSON(b []byte) error {
	var raw []float64
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	if len(raw) != 2 {
		return fmt.Errorf("区间必须是 [起, 止] 两列，收到 %d 列", len(raw))
	}
	s.From = int64(raw[0])
	s.To = int64(raw[1])
	if s.To < s.From {
		return fmt.Errorf("区间 [%d, %d] 终点早于起点", s.From, s.To)
	}
	return nil
}

// Series 是一条时间序列，自带故障真值。
//
// Faults 是实验给的真值，只用于算误报漏报，规则本身看不到它。
// 生产上没有这一列，所以这里算出来的误报率是「事后才知道」的上限。
type Series struct {
	Name   string  `json:"series"`
	Unit   string  `json:"unit"`
	Start  string  `json:"start,omitempty"`
	Points []Point `json:"points"`
	Faults []Span  `json:"faults"`
}

// RulePolicy 是三条规则的参数。四个都是策略，换一档设备就该重调。
type RulePolicy struct {
	// Threshold 是阈值。和 SustainFor 一起用：先过阈值，再看持续多久。
	Threshold float64
	// SustainForS 是持续时长下限（秒）。持续时长规则的判据是它，
	// 阈值规则用的也是同一个门限值，所以两条规则看的是同一个水位。
	SustainForS int64
	// RateLimit 是变化率上限（单位/秒）。单位跟着序列走。
	RateLimit float64
	// GraceS 是故障结束后的宽限：命中落在故障窗尾部 GraceS 内仍算及时。
	GraceS int64
	// MaxGapS 是相邻采样点的最大合法间隔。超过它说明采集中断，
	// 跨断点算斜率得出的数既不是故障也不是正常，必须跳过。
	MaxGapS int64
}

var defaultRulePolicy = RulePolicy{
	Threshold:   85,
	SustainForS: 180,
	RateLimit:   0.5,
	GraceS:      120,
	MaxGapS:     90,
}

// RuleScore 是一条序列 × 一条规则的评分。
type RuleScore struct {
	Series string `json:"series"`
	Rule   string `json:"rule"`
	Hits   int    `json:"hits"`
	// OnTime 是起点落在故障窗（含宽限）内的命中段数，FalseAlarm 是落在窗外的。
	OnTime     int `json:"on_time"`
	FalseAlarm int `json:"false_alarm"`
	// Missed 是一个命中段都没落进去的故障窗数。
	Missed int `json:"missed"`
	// FirstDelayS 是第一个故障窗的检出延迟，-1 表示没检出。
	FirstDelayS int64  `json:"first_delay_s"`
	Spans       []Span `json:"hit_spans,omitempty"`
}

// RuleSummary 是全部序列按规则汇总。
type RuleSummary struct {
	Rule       string
	Hits       int
	OnTime     int
	FalseAlarm int
	Missed     int
}

// RuleReport 是一次规则对比的结果。
type RuleReport struct {
	Scores   []RuleScore
	Summary  []RuleSummary
	Series   int
	Faults   int
	Points   int
	Interval int64
}

// loadSeries 读一份多序列样本（JSONL）。
func loadSeries(path string) ([]Series, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var out []Series
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	line := 0
	for sc.Scan() {
		line++
		raw := strings.TrimSpace(sc.Text())
		if raw == "" || strings.HasPrefix(raw, "//") {
			continue
		}
		var s Series
		if err := json.Unmarshal([]byte(raw), &s); err != nil {
			return nil, fmt.Errorf("%s 第 %d 行: %w", path, line, err)
		}
		if s.Name == "" {
			return nil, fmt.Errorf("%s 第 %d 行缺 series 名", path, line)
		}
		if len(s.Points) < 2 {
			return nil, fmt.Errorf("%s 的 %s 只有 %d 个采样点，连斜率都算不出来", path, s.Name, len(s.Points))
		}
		for i := 1; i < len(s.Points); i++ {
			if s.Points[i].T <= s.Points[i-1].T {
				return nil, fmt.Errorf("%s 的 %s 第 %d 个采样点时间没有递增（%d ≤ %d）",
					path, s.Name, i+1, s.Points[i].T, s.Points[i-1].T)
			}
		}
		out = append(out, s)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// Evaluate 跑三条规则并打分。
func Evaluate(series []Series, policy RulePolicy) RuleReport {
	report := RuleReport{Series: len(series)}
	for _, s := range series {
		report.Points += len(s.Points)
		report.Faults += len(s.Faults)
	}
	report.Interval = typicalInterval(series)

	for _, s := range series {
		hits := map[string][]Span{
			RuleThreshold: thresholdHits(s, policy),
			RuleSustained: sustainedHits(s, policy),
			RuleRate:      rateHits(s, policy),
		}
		for _, rule := range []string{RuleThreshold, RuleSustained, RuleRate} {
			report.Scores = append(report.Scores, scoreOf(s, rule, hits[rule], policy))
		}
	}

	sums := map[string]*RuleSummary{}
	for _, rule := range []string{RuleThreshold, RuleSustained, RuleRate} {
		sums[rule] = &RuleSummary{Rule: rule}
	}
	for _, sc := range report.Scores {
		s := sums[sc.Rule]
		s.Hits += sc.Hits
		s.OnTime += sc.OnTime
		s.FalseAlarm += sc.FalseAlarm
		s.Missed += sc.Missed
	}
	for _, rule := range []string{RuleThreshold, RuleSustained, RuleRate} {
		report.Summary = append(report.Summary, *sums[rule])
	}
	return report
}

// thresholdHits 是阈值规则：过线就算，不问过了多久。
func thresholdHits(s Series, policy RulePolicy) []Span {
	return foldRuns(s.Points, func(_ int, v float64) bool { return v >= policy.Threshold })
}

// sustainedHits 是持续时长规则：过线并且连续撑住 SustainForS。
//
// 命中时刻取「段首 + SustainForS」，不是段首。取段首等于自己骗自己：
// 报告上写着 30 秒检出，实际那一刻根本还不知道它是持续的还是尖刺。
func sustainedHits(s Series, policy RulePolicy) []Span {
	runs := foldRuns(s.Points, func(_ int, v float64) bool { return v >= policy.Threshold })
	out := make([]Span, 0, len(runs))
	for _, r := range runs {
		if r.To-r.From < policy.SustainForS {
			continue
		}
		out = append(out, Span{From: r.From + policy.SustainForS, To: r.To})
	}
	return out
}

// rateHits 是变化率规则：两点之间的斜率过线。
//
// 跨采集中断的斜率不算。样本里 tor-a3f-04 的序列中间断了 5 个点，
// 断点两侧从 60 跳到 95，按 120 秒算斜率只有 0.29，看着「没变」；
// 而真实变化发生在断点里，谁也不知道多久。这种数只能标出来，不能当判据。
func rateHits(s Series, policy RulePolicy) []Span {
	hits := make([]bool, len(s.Points))
	for i := 1; i < len(s.Points); i++ {
		dt := s.Points[i].T - s.Points[i-1].T
		if dt <= 0 || dt > policy.MaxGapS {
			continue
		}
		slope := (s.Points[i].V - s.Points[i-1].V) / float64(dt)
		hits[i] = slope >= policy.RateLimit
	}
	out := make([]Span, 0, 4)
	for i := 0; i < len(hits); {
		if !hits[i] {
			i++
			continue
		}
		j := i
		for j+1 < len(hits) && hits[j+1] {
			j++
		}
		out = append(out, Span{From: s.Points[i].T, To: s.Points[j].T})
		i = j + 1
	}
	return out
}

// foldRuns 把逐点判定折成连续段。段的范围是「首个为真的点到末个为真的点」。
func foldRuns(points []Point, pred func(i int, v float64) bool) []Span {
	var out []Span
	for i := 0; i < len(points); {
		if !pred(i, points[i].V) {
			i++
			continue
		}
		j := i
		for j+1 < len(points) && pred(j+1, points[j+1].V) {
			j++
		}
		out = append(out, Span{From: points[i].T, To: points[j].T})
		i = j + 1
	}
	return out
}

// scoreOf 对着真故障窗给命中段打分。
//
// 一个命中段只算一次：起点落在某个故障窗（含宽限尾巴）内算及时，否则算误报。
// 一个故障窗只要有一个命中段落进去就不算漏报。
func scoreOf(s Series, rule string, hits []Span, policy RulePolicy) RuleScore {
	sc := RuleScore{Series: s.Name, Rule: rule, Hits: len(hits), Spans: hits, FirstDelayS: -1}
	if len(s.Faults) > 0 {
		f0 := s.Faults[0]
		for _, h := range hits {
			if h.From >= f0.From && h.From <= f0.To+policy.GraceS {
				sc.FirstDelayS = h.From - f0.From
				break
			}
		}
	}
	for _, h := range hits {
		if inFault(s.Faults, h.From, policy.GraceS) {
			sc.OnTime++
		} else {
			sc.FalseAlarm++
		}
	}
	for _, f := range s.Faults {
		covered := false
		for _, h := range hits {
			if h.From >= f.From && h.From <= f.To+policy.GraceS {
				covered = true
				break
			}
		}
		if !covered {
			sc.Missed++
		}
	}
	return sc
}

func inFault(faults []Span, t int64, grace int64) bool {
	for _, f := range faults {
		if t >= f.From && t <= f.To+grace {
			return true
		}
	}
	return false
}

// typicalInterval 取相邻点间隔的中位数，报告里用来交代采样周期。
// 微突发这一课的全部麻烦都从这一个数字来。
func typicalInterval(series []Series) int64 {
	var gaps []int64
	for _, s := range series {
		for i := 1; i < len(s.Points); i++ {
			if d := s.Points[i].T - s.Points[i-1].T; d > 0 {
				gaps = append(gaps, d)
			}
		}
	}
	if len(gaps) == 0 {
		return 0
	}
	sort.Slice(gaps, func(i, j int) bool { return gaps[i] < gaps[j] })
	return gaps[len(gaps)/2]
}
