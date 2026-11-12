package main

import (
	"sort"
	"strings"
)

// Eval 是「判定结果」与「注入真相」的对账。
//
// 判定层看不到真相，这个对账只在实验里做得到。生产上你永远不知道
// 漏了多少——所以评测里的漏报数只是这套参数在这套剧本下的表现，不是保证。
type Eval struct {
	TrueWindows      int     // 注入的真实失联窗口数
	Detected         int     // 被判定出来的窗口数
	Missed           int     // 漏掉的窗口数
	DelaysMs         []int64 // 每个检出窗口的发现延迟
	FalseDownDevices int     // 没有任何真实故障、却被判成下线过的设备数
	DegradedTicks    int     // 劣化窗口内被判 down 的轮次（设备 × 轮）
	DegradedTotal    int     // 劣化窗口总轮次
	ProbeWindows     int     // 注入的探测侧故障窗口数
	ProbeReported    int     // 判定层发出的 probe-fault 事件数
	ProbeDetectMs    []int64 // 探测侧故障的识别延迟
	BlockedTicks     int     // 因探测侧故障被作废的观测轮次（设备 × 轮）
}

// DelayP 取发现延迟的分位数，口径同 00-setup：最近秩法。
func (e Eval) DelayP(p float64) int64 { return percentileOf(e.DelaysMs, p) }

// ProbeDetectP 取探测侧故障识别延迟的分位数。
func (e Eval) ProbeDetectP(p float64) int64 { return percentileOf(e.ProbeDetectMs, p) }

func percentileOf(samples []int64, p float64) int64 {
	if len(samples) == 0 {
		return 0
	}
	sorted := append([]int64(nil), samples...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	if p <= 0 {
		return sorted[0]
	}
	if p >= 100 {
		return sorted[len(sorted)-1]
	}
	exact := float64(len(sorted)) * p / 100
	rank := int(exact)
	if float64(rank) < exact {
		rank++
	}
	if rank < 1 {
		rank = 1
	}
	return sorted[rank-1]
}

// Evaluate 逐台设备、逐个窗口对账。
func Evaluate(sc *Scenario, m *Monitor) Eval {
	e := Eval{}
	ticks := m.cfg.Ticks()
	grace := m.cfg.FailThreshold + m.cfg.RecoverThreshold + 1
	tracks := m.Tracks()

	for i := range tracks {
		windows := sc.UnreachableWindows(i)
		changes := tracks[i].Changes

		for _, w := range windows {
			e.TrueWindows++
			// 只认窗口起点之后、宽限期内出现的 down 跃迁。
			// 宽限期之外才算漏报——探测侧故障会让判定整体后移，那是允许的代价。
			hit := -1
			for _, c := range changes {
				if c.State == StateDown && c.Tick >= w.start {
					hit = c.Tick
					break
				}
			}
			if hit >= 0 && hit <= w.end+grace {
				e.Detected++
				e.DelaysMs = append(e.DelaysMs, m.cfg.AtMs(hit-w.start))
			} else {
				e.Missed++
			}
		}

		intervals := downIntervals(changes, ticks)
		if len(windows) == 0 && len(intervals) > 0 {
			e.FalseDownDevices++
		}

		for _, w := range sc.DegradedWindows(i) {
			e.DegradedTotal += w.end - w.start
			for _, iv := range intervals {
				e.DegradedTicks += overlap(iv, [2]int{w.start, w.end})
			}
		}

		for _, f := range sc.perDevice[i] {
			if f.kind == FaultProbeOutage {
				e.BlockedTicks += f.end - f.start
			}
		}
	}

	e.ProbeWindows = len(sc.ProbeOutageWindows())
	for _, ev := range m.Events() {
		if ev.Kind == KindProbeFault {
			e.ProbeReported++
		}
	}
	// 识别延迟按机房前缀匹配：事件的主体是同源分组键 room-x/probe-x。
	for _, f := range sc.ProbeOutageWindows() {
		prefix := f.Room + "/"
		want := m.cfg.AtMs(f.StartTick)
		for _, ev := range m.Events() {
			if ev.Kind != KindProbeFault || !strings.HasPrefix(ev.Subject, prefix) {
				continue
			}
			if ev.AtMs >= want {
				e.ProbeDetectMs = append(e.ProbeDetectMs, ev.AtMs-want)
			}
			break
		}
	}
	return e
}

// downIntervals 把状态跃迁压成 [start, end) 区间。
func downIntervals(changes []StateChange, ticks int) [][2]int {
	var out [][2]int
	start := -1
	for _, c := range changes {
		switch {
		case c.State == StateDown && start < 0:
			start = c.Tick
		case c.State != StateDown && start >= 0:
			out = append(out, [2]int{start, c.Tick})
			start = -1
		}
	}
	if start >= 0 {
		out = append(out, [2]int{start, ticks})
	}
	return out
}

// overlap 返回两个区间的交集长度。
func overlap(a, b [2]int) int {
	lo, hi := a[0], a[1]
	if b[0] > lo {
		lo = b[0]
	}
	if b[1] < hi {
		hi = b[1]
	}
	if hi > lo {
		return hi - lo
	}
	return 0
}
