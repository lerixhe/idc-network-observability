package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"time"
)

// 固定窗口 + 水位线。
//
// 规则只有两条，但顺序很重要：
//
//  1. 水位线 = 已见最大事件时间 − 水位线宽度。事件时间取点位的 acked_ts，与到达顺序无关。
//  2. 一个窗口在「窗口右边界 ≤ 水位线」时关闭。窗口关了，后面到的点进不去。
//
// 于是迟到只有两种结局：赶在关窗之前进来，被正常计入；或者关窗之后才到，被标记出来，
// 不进聚合但也绝不静默消失。丢多少、偏多少，都能算出来。
//
// 水位线宽度不是拍出来的：控制面（12-task）承诺「断连后可补 15 分钟」，
// 水位线就不能窄于 15 分钟。窄了就是明知数据要迟到还提前关门。

// unboundedWatermark 让所有点都被接受，用来算真值。
const unboundedWatermark = time.Duration(1) << 62

// Agg 是一个窗口里一个维度的聚合。字段选取只花了两个字节：sum 与 count 可加可并，
// 所以 1 分钟能并成 5 分钟。换成 p99 就并不了——这是第 34 课的坑。
type Agg struct {
	WindowStart time.Time `json:"window_start"`
	ProbeID     string    `json:"probe_id"`
	MetricID    string    `json:"metric_id"`
	Sum         float64   `json:"sum"`
	Count       int       `json:"count"`
	Min         float64   `json:"min"`
	Max         float64   `json:"max"`
}

type aggKey struct {
	windowStart int64
	probeID     string
	metricID    string
}

// LateMark 是一个被挡在窗外的点。它是「标记」，不是「丢弃日志」：
// 数量、迟到多少、按哪种理由挡的，全部可对账。
type LateMark struct {
	WindowStart time.Time `json:"window_start"`
	ProbeID     string    `json:"probe_id"`
	MetricID    string    `json:"metric_id"`
	AckedTS     time.Time `json:"acked_ts"`
	LatenessMs  int64     `json:"lateness_ms"`
	WatermarkMs int64     `json:"watermark_ms"`
	Reason      string    `json:"reason"`
}

// 迟到被挡的两种理由，对应的修法完全不同。
const (
	lateWindowClosed = "窗口已关闭（水位线太窄）"
	lateOverBackfill = "超出最大补报延迟（控制面兜不住）"
)

// WindowResult 是一次「窗口 + 水位线」跑完的结论。
type WindowResult struct {
	Window      time.Duration
	Watermark   time.Duration
	MaxBackfill time.Duration

	Aggs []Agg
	Late []LateMark

	PointsIn      int
	PointsKept    int
	PointsLate    int
	EarlyRejected int // 迟到超过最大补报延迟的点数，PointsLate 的子集

	Windows        int
	MaxEvent       time.Time
	MaxLateness    time.Duration
	MeanLateness   time.Duration // 被接受点的平均迟到（到达时刻距事件时刻）
	MeanVisibleLag time.Duration // 点的理论可见延迟：窗口右边界 + 水位线 − 事件时间
	MaxVisibleLag  time.Duration
}

// runWindow 按到达顺序跑一遍。recs 必须是去重之后的数据流。
func runWindow(recs []Record, window, watermark, maxBackfill time.Duration) WindowResult {
	res := WindowResult{Window: window, Watermark: watermark, MaxBackfill: maxBackfill}
	agg := make(map[aggKey]*Agg, 512)

	var maxEvent time.Time
	var latenessSum time.Duration
	var visibleSum time.Duration

	for _, rec := range recs {
		for _, d := range rec.Points {
			res.PointsIn++
			if d.AckedTS.After(maxEvent) {
				maxEvent = d.AckedTS
			}
			wStart := floorWindow(d.AckedTS, window)
			wEnd := wStart.Add(window)
			lateness := maxEvent.Sub(d.AckedTS)
			watermarkPos := maxEvent.Add(-watermark)

			if !wEnd.After(watermarkPos) {
				reason := lateWindowClosed
				if maxBackfill > 0 && lateness > maxBackfill {
					reason = lateOverBackfill
					res.EarlyRejected++
				}
				res.Late = append(res.Late, LateMark{
					WindowStart: wStart,
					ProbeID:     d.ProbeID,
					MetricID:    d.MetricID,
					AckedTS:     d.AckedTS,
					LatenessMs:  lateness.Milliseconds(),
					WatermarkMs: watermark.Milliseconds(),
					Reason:      reason,
				})
				res.PointsLate++
				if lateness > res.MaxLateness {
					res.MaxLateness = lateness
				}
				continue
			}

			res.PointsKept++
			latenessSum += lateness
			visible := wEnd.Add(watermark).Sub(d.AckedTS)
			if visible > 0 {
				visibleSum += visible
				if visible > res.MaxVisibleLag {
					res.MaxVisibleLag = visible
				}
			}

			k := aggKey{windowStart: wStart.Unix(), probeID: d.ProbeID, metricID: d.MetricID}
			a, ok := agg[k]
			if !ok {
				a = &Agg{WindowStart: wStart, ProbeID: d.ProbeID, MetricID: d.MetricID, Min: d.Value, Max: d.Value}
				agg[k] = a
			}
			a.Sum += d.Value
			a.Count++
			if d.Value < a.Min {
				a.Min = d.Value
			}
			if d.Value > a.Max {
				a.Max = d.Value
			}
		}
	}

	res.MaxEvent = maxEvent
	if res.PointsKept > 0 {
		res.MeanLateness = latenessSum / time.Duration(res.PointsKept)
		res.MeanVisibleLag = visibleSum / time.Duration(res.PointsKept)
	}

	res.Aggs = make([]Agg, 0, len(agg))
	windows := make(map[int64]struct{}, len(agg))
	for _, a := range agg {
		res.Aggs = append(res.Aggs, *a)
		windows[a.WindowStart.Unix()] = struct{}{}
	}
	res.Windows = len(windows)
	sortAggs(res.Aggs)
	sort.Slice(res.Late, func(i, j int) bool {
		if !res.Late[i].WindowStart.Equal(res.Late[j].WindowStart) {
			return res.Late[i].WindowStart.Before(res.Late[j].WindowStart)
		}
		if res.Late[i].ProbeID != res.Late[j].ProbeID {
			return res.Late[i].ProbeID < res.Late[j].ProbeID
		}
		return res.Late[i].MetricID < res.Late[j].MetricID
	})
	return res
}

// truthWindow 是无界水位线：什么点都收，作为偏差比较的真值。
// 它不是「正确配置」，只是分母。
func truthWindow(recs []Record, window time.Duration) WindowResult {
	return runWindow(recs, window, unboundedWatermark, 0)
}

func sortAggs(aggs []Agg) {
	sort.Slice(aggs, func(i, j int) bool {
		if !aggs[i].WindowStart.Equal(aggs[j].WindowStart) {
			return aggs[i].WindowStart.Before(aggs[j].WindowStart)
		}
		if aggs[i].ProbeID != aggs[j].ProbeID {
			return aggs[i].ProbeID < aggs[j].ProbeID
		}
		return aggs[i].MetricID < aggs[j].MetricID
	})
}

// MetricDeviation 是一个指标在「丢掉迟到数据」之后的偏差账。
type MetricDeviation struct {
	MetricID       string
	TruthSum       float64
	KeptSum        float64
	LostSum        float64
	TotalPct       float64
	WorstWindow    time.Time
	WorstWindowPct float64
}

// deviation 逐指标比较真值与窗口结果。
func deviation(truth, got WindowResult) []MetricDeviation {
	truthByMetric := map[string]float64{}
	// 逐窗口的真值，用来找偏差最大的那个窗口。
	truthByWindow := map[string]map[int64]float64{}
	gotByWindow := map[string]map[int64]float64{}
	for _, a := range truth.Aggs {
		truthByMetric[a.MetricID] += a.Sum
		m, ok := truthByWindow[a.MetricID]
		if !ok {
			m = map[int64]float64{}
			truthByWindow[a.MetricID] = m
		}
		m[a.WindowStart.Unix()] += a.Sum
	}
	for _, a := range got.Aggs {
		m, ok := gotByWindow[a.MetricID]
		if !ok {
			m = map[int64]float64{}
			gotByWindow[a.MetricID] = m
		}
		m[a.WindowStart.Unix()] += a.Sum
	}

	out := make([]MetricDeviation, 0, len(truthByMetric))
	for metric, tsum := range truthByMetric {
		d := MetricDeviation{MetricID: metric, TruthSum: tsum}
		d.KeptSum = sumAggsByMetric(got.Aggs, metric)
		d.LostSum = tsum - d.KeptSum
		d.TotalPct = pct(d.KeptSum-tsum, tsum)
		for win, tw := range truthByWindow[metric] {
			gw := gotByWindow[metric][win]
			p := pct(gw-tw, tw)
			if absFloat(p) > absFloat(d.WorstWindowPct) {
				d.WorstWindowPct = p
				d.WorstWindow = time.Unix(win, 0).UTC()
			}
		}
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].MetricID < out[j].MetricID })
	return out
}

func sumAggsByMetric(aggs []Agg, metric string) float64 {
	s := 0.0
	for _, a := range aggs {
		if a.MetricID == metric {
			s += a.Sum
		}
	}
	return s
}

// WatermarkRow 是「同一份样本、不同水位线宽度」的一行。窄与宽的代价在这一行里对着看。
type WatermarkRow struct {
	Watermark     time.Duration
	PointsKept    int
	PointsLate    int
	OverBackfill  int
	LateBytes     int64
	MaxDeviation  float64 // 各指标偏差绝对值的最大者
	WorstMetric   string
	VisibleLag    time.Duration // 窗口宽度 + 水位线宽度，结果最早什么时候能看见
	MeanLateness  time.Duration
	UnderBackfill bool // 水位线窄于控制面承诺的补报上限
	Devs          []MetricDeviation
}

// compareWatermarks 用同一份去重后的数据流跑多个水位线宽度。
func compareWatermarks(recs []Record, window time.Duration, widths []time.Duration,
	maxBackfill time.Duration, bytesPerPoint float64) []WatermarkRow {
	truth := truthWindow(recs, window)
	rows := make([]WatermarkRow, 0, len(widths))
	for _, wm := range widths {
		res := runWindow(recs, window, wm, maxBackfill)
		devs := deviation(truth, res)
		row := WatermarkRow{
			Watermark:     wm,
			PointsKept:    res.PointsKept,
			PointsLate:    res.PointsLate,
			OverBackfill:  res.EarlyRejected,
			LateBytes:     int64(float64(res.PointsLate) * bytesPerPoint),
			VisibleLag:    window + wm,
			MeanLateness:  res.MeanLateness,
			UnderBackfill: maxBackfill > 0 && wm < maxBackfill,
			Devs:          devs,
		}
		for _, d := range devs {
			if absFloat(d.TotalPct) > absFloat(row.MaxDeviation) {
				row.MaxDeviation = d.TotalPct
				row.WorstMetric = d.MetricID
			}
		}
		rows = append(rows, row)
	}
	return rows
}

// aggsDigest 是聚合结果的指纹。回放对比、落盘校验都用它：
// 相等的 digest 意味着每个窗口每个维度的 sum/count/min/max 一模一样。
func aggsDigest(aggs []Agg) string {
	sorted := make([]Agg, len(aggs))
	copy(sorted, aggs)
	sortAggs(sorted)
	h := sha256.New()
	for _, a := range sorted {
		fmt.Fprintf(h, "%d|%s|%s|%.6f|%d|%.6f|%.6f\n",
			a.WindowStart.Unix(), a.ProbeID, a.MetricID, a.Sum, a.Count, a.Min, a.Max)
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// pct 返回 (got-truth)/truth×100：丢数据时是负数，读作「少算了多少」。
// 分母为 0 时给 0，不编一个相对偏差出来。
func pct(diff, truth float64) float64 {
	if truth == 0 {
		return 0
	}
	return diff / truth * 100
}

func absFloat(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}

// rollup5m 把 1 分钟聚合并成 5 分钟。sum 与 count 可加、min/max 可取极值，所以这一步是无损的。
// 存的是 p99 就不能这么并——分位数不可加，这是留的坑之一。
func rollup5m(aggs []Agg) []Agg {
	const target = 5 * time.Minute
	type k struct {
		windowStart int64
		probeID     string
		metricID    string
	}
	m := make(map[k]*Agg, len(aggs)/4+1)
	for _, a := range aggs {
		ws := floorWindow(a.WindowStart, target)
		key := k{windowStart: ws.Unix(), probeID: a.ProbeID, metricID: a.MetricID}
		cur, ok := m[key]
		if !ok {
			cp := a
			cp.WindowStart = ws
			m[key] = &cp
			continue
		}
		cur.Sum += a.Sum
		cur.Count += a.Count
		if a.Min < cur.Min {
			cur.Min = a.Min
		}
		if a.Max > cur.Max {
			cur.Max = a.Max
		}
	}
	out := make([]Agg, 0, len(m))
	for _, a := range m {
		out = append(out, *a)
	}
	sortAggs(out)
	return out
}
