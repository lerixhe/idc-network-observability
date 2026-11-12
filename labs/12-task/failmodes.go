package main

import (
	"fmt"
	"sort"
	"time"
)

// DispatchMode 是控制面的分发方式。
type DispatchMode string

const (
	ModePull DispatchMode = "pull"
	ModePush DispatchMode = "push"
)

// ParseModes 解析 -modes：pull / push / both。
func ParseModes(s string) (pull, push bool, err error) {
	switch s {
	case "pull":
		return true, false, nil
	case "push":
		return false, true, nil
	case "both", "":
		return true, true, nil
	}
	return false, false, fmt.Errorf("未知分发模式 %q，可选：pull / push / both", s)
}

// ── 失败模式一：重复下发 ──────────────────────────────────────────

// DetectDuplicates 从应用记录里找「同一个任务、同一个 revision 落在两个探针上」。
//
// 这是幂等键压不住的那一类重复：键是 (probe_id, batch_seq, metric_id)，按探针去重，
// 两个探针各报一份，管道看到的是两个独立点位，统计口径当场翻倍。
// 控制面必须自己报出来，数据面没有办法。
func DetectDuplicates(apps []Application, simEnd, reportInterval time.Duration) []Event {
	type key struct {
		task string
		rev  int
	}
	groups := make(map[key]map[string]time.Duration)
	order := make([]key, 0, len(apps))
	for _, a := range apps {
		if a.Role != RoleAssign {
			continue
		}
		k := key{a.TaskID, a.Revision}
		if groups[k] == nil {
			groups[k] = make(map[string]time.Duration, 2)
			order = append(order, k)
		}
		if prev, ok := groups[k][a.ProbeID]; !ok || a.AppliedAt < prev {
			groups[k][a.ProbeID] = a.AppliedAt
		}
	}

	events := make([]Event, 0, len(order))
	for _, k := range order {
		byProbe := groups[k]
		if len(byProbe) < 2 {
			continue
		}
		probes := make([]string, 0, len(byProbe))
		lastApply := time.Duration(0)
		for p, at := range byProbe {
			probes = append(probes, p)
			if at > lastApply {
				lastApply = at
			}
		}
		sort.Strings(probes)
		overlap := simEnd - lastApply
		if overlap < 0 {
			overlap = 0
		}
		points := 0
		if reportInterval > 0 {
			points = int(overlap / reportInterval)
		}
		events = append(events, newEvent(lastApply, "duplicate-dispatch",
			fmt.Sprintf("%s/rev%d", k.task, k.rev),
			"同一个任务被两个探针同时接活：数据翻倍，幂等键按探针去重压不住这一份",
			map[string]any{
				"task_id":           k.task,
				"revision":          k.rev,
				"probes":            probes,
				"probe_count":       len(probes),
				"overlap_s":         secs(overlap),
				"duplicated_points": points,
			}))
	}
	sort.SliceStable(events, func(i, j int) bool { return events[i].AtMs < events[j].AtMs })
	return events
}

// ── 失败模式二：全量重推 ────────────────────────────────────────

// RepushNumbers 是全量重推的一笔账。
type RepushNumbers struct {
	Tasks         int
	Changes       int
	WindowSec     float64
	SteadyQPS     float64
	PeakQPS       float64
	Drain         time.Duration
	Amplification float64
}

// RepushNumbersFor 算一次全量重推对控制面的冲击。
//
// 稳态 QPS = 变更数 / 窗口。一条全局变更（换版本、改 schema）把全部任务重发一遍，
// 需求瞬间变成 tasks 条，限流之后峰值就是 ctlRate，排空时间 = tasks / ctlRate。
// 放大倍数 = 峰值 / 稳态，这个比值才是要看的数：它说明后续增量变更被挤到队尾多久。
func RepushNumbersFor(tasks, changes int, window time.Duration, ctlRate float64) RepushNumbers {
	n := RepushNumbers{Tasks: tasks, Changes: changes}
	if window <= 0 || ctlRate <= 0 {
		return n
	}
	n.WindowSec = window.Seconds()
	n.SteadyQPS = float64(changes) / n.WindowSec
	n.PeakQPS = ctlRate
	if float64(tasks) < ctlRate {
		n.PeakQPS = float64(tasks)
	}
	n.Drain = time.Duration(float64(tasks) / ctlRate * float64(time.Second))
	if n.SteadyQPS > 0 {
		n.Amplification = n.PeakQPS / n.SteadyQPS
	}
	return n
}

// 十万探针的规模，大纲里那笔账按这个算。
const (
	scaleProbes  = 100000
	scaleWindow  = time.Hour
	scaleTasks   = scaleProbes
	scaleChanges = scaleTasks + scaleTasks/4
)

// FullRepush 是重推的判定结果。
type FullRepush struct {
	Sample RepushNumbers
	Scale  RepushNumbers
	Events []Event
}

// DetectFullRepush 拿本次样本与十万探针两个尺度对比，放大倍数超过 10 倍就报信号。
func DetectFullRepush(cfg Config, tasks int, changes []Change) FullRepush {
	steady := float64(len(changes)) / cfg.Duration.Seconds()
	peak := cfg.CtlRate
	if float64(tasks) < peak {
		peak = float64(tasks)
	}
	amp := 0.0
	if steady > 0 {
		amp = peak / steady
	}
	out := FullRepush{
		Sample: RepushNumbersFor(tasks, len(changes), cfg.Duration, cfg.CtlRate),
		Scale:  RepushNumbersFor(scaleTasks, scaleChanges, scaleWindow, cfg.CtlRate),
	}
	if amp < 10 {
		return out
	}
	out.Events = append(out.Events, newEvent(cfg.Duration/3, "full-repush", "control-plane",
		"一条全局变更把全部任务重发一遍：控制面峰值是稳态的几十倍，后续变更排到队尾",
		map[string]any{
			"tasks":               tasks,
			"changes":             len(changes),
			"steady_qps":          round2(steady),
			"peak_qps":            round2(peak),
			"drain_s":             secs(out.Sample.Drain),
			"amplification":       round2(amp),
			"scale_tasks":         scaleTasks,
			"scale_steady_qps":    round2(out.Scale.SteadyQPS),
			"scale_peak_qps":      round2(out.Scale.PeakQPS),
			"scale_drain_s":       secs(out.Scale.Drain),
			"scale_amplification": round2(out.Scale.Amplification),
		}))
	return out
}

// ── 失败模式三：坏点替换震荡 ────────────────────────────────────

// FlapTaskResult 是一条震荡任务的轨迹。
type FlapTaskResult struct {
	TaskID      string
	Moves       int
	Faults      int
	Trail       []string
	GapSegments int
	GapDuration time.Duration
	FaultAt     time.Duration
	Stopped     string
}

// FlapResult 是抑制开/关两种策略下的对比。
type FlapResult struct {
	Suppress    bool
	PerTask     []FlapTaskResult
	Moves       int
	Faults      int
	GapSegments int
	GapDuration time.Duration
	Events      []Event
}

// SimulateFlap 跑坏点替换：目标设备整段不应答，探针每轮上报都失败，
// 控制器就换一台探针重新下任务——这个循环自己没有出口。
//
// 抑制策略：连续换 FlapReplaceLimit 次之后不再换，转成一条 task-fault
// （「目标设备坏了，不是探针点位的问题」），并把任务钉在当前点上等人工。
// 每换一次丢一段数据，所以少换几次就是少丢几段。
func SimulateFlap(cfg Config, sc *Scenario, suppress bool) FlapResult {
	idx := sc.OwnerIndex()
	res := FlapResult{Suppress: suppress}
	startAt := cfg.Duration / 4

	for _, taskID := range sc.Truth.FlapTasks {
		var task Task
		found := false
		for _, t := range sc.Tasks {
			if t.TaskID == taskID {
				task = t
				found = true
				break
			}
		}
		if !found {
			continue
		}
		tr := FlapTaskResult{TaskID: taskID, Trail: []string{task.ProbeID}}
		cur := task.ProbeID
		used := map[string]bool{cur: true}
		t := startAt
		for {
			attemptEnd := t + cfg.ReportInterval
			if attemptEnd > cfg.Duration {
				tr.Stopped = "仿真结束时仍在震荡"
				break
			}
			if suppress && tr.Moves >= cfg.FlapReplaceLimit {
				// 连续换点够数了：不再换，报故障。多换一次只是多丢一段数据。
				tr.Faults++
				tr.FaultAt = attemptEnd
				res.Events = append(res.Events, newEvent(attemptEnd, "task-fault",
					taskID,
					fmt.Sprintf("连续换点 %d 次仍失败，判定目标设备故障，停止换点", tr.Moves),
					map[string]any{
						"task_id":       taskID,
						"moves":         tr.Moves,
						"trail":         tr.Trail,
						"replace_limit": cfg.FlapReplaceLimit,
						"gap_segments":  tr.GapSegments,
						"gap_s":         secs(tr.GapDuration),
					}))
				break
			}
			next, ok := nextFlapProbe(sc.Probes, idx, cur, attemptEnd, used)
			if !ok {
				tr.Faults++
				tr.FaultAt = attemptEnd
				break
			}
			// 换点不是瞬间的：新点要等下一个拉取周期才接活，这一段是断档。
			moveAt := attemptEnd + cfg.Poll/2
			tr.Moves++
			tr.GapSegments++
			tr.GapDuration += cfg.ReportInterval + cfg.Poll/2
			tr.Trail = append(tr.Trail, next)
			used[next] = true
			res.Events = append(res.Events, newEvent(moveAt, "task-reassign",
				taskID,
				fmt.Sprintf("%s → %s：本轮仍失败，换点代价是一段数据断档", cur, next),
				map[string]any{
					"task_id": taskID,
					"from":    cur,
					"to":      next,
					"gap_s":   secs(cfg.ReportInterval + cfg.Poll/2),
				}))
			cur = next
			t = moveAt
		}
		res.PerTask = append(res.PerTask, tr)
		res.Moves += tr.Moves
		res.Faults += tr.Faults
		res.GapSegments += tr.GapSegments
		res.GapDuration += tr.GapDuration
	}
	return res
}

// nextFlapProbe 挑下一台接活的探针：先挑没用过的，用完了再回头。
// 真控制器就是这么转圈的，震荡的成因正是「候选集里没有能用的点」。
func nextFlapProbe(probes []Probe, idx map[string]int, from string, at time.Duration, used map[string]bool) (string, bool) {
	start, ok := idx[from]
	if !ok || len(probes) < 2 {
		return "", false
	}
	for pass := 0; pass < 2; pass++ {
		for off := 1; off <= len(probes); off++ {
			cand := probes[(start+off)%len(probes)]
			if cand.ProbeID == from || !cand.online(at) {
				continue
			}
			if pass == 0 && used[cand.ProbeID] {
				continue
			}
			return cand.ProbeID, true
		}
	}
	return "", false
}

// ── 失败模式四：过期任务不回收 ──────────────────────────────────

// StaleVariant 是一种回收策略的账。
type StaleVariant struct {
	Reclaim          bool
	StaleAssignments int
	OrphanTasks      []string
	GapDuration      time.Duration
	RecoveredAt      time.Duration // <0 表示到仿真结束都没恢复
	Silence          time.Duration
}

// StaleResult 是一台退役探针名下的任务在两种策略下的对比。
type StaleResult struct {
	ProbeID          string
	Tasks            []string
	DecommissionedAt time.Duration
	TTL              time.Duration
	On               StaleVariant
	Off              StaleVariant
	Events           []Event
}

// SimulateStale 处理「探针下线了，任务还挂在它名下」。
//
// 判据只有一条：控制面最后一次收到它的心跳距今多久。超过 TTL 就算过期。
// 不回收的后果不是多占一行记录，是这条任务永远不出数，而且没人知道该去找哪台探针。
func SimulateStale(cfg Config, sc *Scenario, reclaim bool) *StaleResult {
	var target *Probe
	for i := range sc.Probes {
		p := &sc.Probes[i]
		if p.DecommissionedAt < 0 || p.DecommissionedAt > cfg.Duration {
			continue
		}
		if len(sc.TasksOf(p.ProbeID)) == 0 {
			continue
		}
		target = p
		break
	}
	if target == nil {
		return nil
	}

	names := make([]string, 0, 2)
	for _, t := range sc.TasksOf(target.ProbeID) {
		names = append(names, t.TaskID)
	}
	res := &StaleResult{
		ProbeID:          target.ProbeID,
		Tasks:            names,
		DecommissionedAt: target.DecommissionedAt,
		TTL:              cfg.StaleTTL,
	}
	reassign := cfg.Poll / 2

	res.Off = StaleVariant{
		Reclaim:          false,
		StaleAssignments: len(names),
		OrphanTasks:      names,
		Silence:          cfg.Duration - target.DecommissionedAt,
		GapDuration:      cfg.Duration - target.DecommissionedAt,
		RecoveredAt:      -1,
	}
	reclaimAt := target.DecommissionedAt + cfg.StaleTTL
	res.On = StaleVariant{Reclaim: true, RecoveredAt: reclaimAt + reassign}
	if res.On.RecoveredAt > cfg.Duration {
		res.On.RecoveredAt = -1
		res.On.GapDuration = cfg.Duration - target.DecommissionedAt
	} else {
		res.On.GapDuration = res.On.RecoveredAt - target.DecommissionedAt
	}

	if reclaim {
		return res
	}
	// 不回收：静默超过 TTL 的那一刻，检测器把这条陈旧关联报出来。
	for _, id := range names {
		res.Events = append(res.Events, newEvent(reclaimAt, "stale-assignment",
			fmt.Sprintf("%s/%s", target.ProbeID, id),
			"探针静默超过 TTL，任务还挂在它名下：这条指标不再出数，也没人负责改派",
			map[string]any{
				"probe_id":        target.ProbeID,
				"task_id":         id,
				"ttl_s":           secs(cfg.StaleTTL),
				"silence_s":       secs(cfg.Duration - target.DecommissionedAt),
				"gap_s":           secs(cfg.Duration - target.DecommissionedAt),
				"reclaimed":       false,
				"would_recover_s": secs(reassign + cfg.StaleTTL),
			}))
	}
	return res
}

// ── 汇总 ────────────────────────────────────────────────────────

// Signals 是四个失败模式的判定结果与信号。
type Signals struct {
	Duplicates []Event
	Repush     *FullRepush
	FlapOn     *FlapResult
	FlapOff    *FlapResult
	Stale      *StaleResult
	Events     []Event
}

// AnalyzeFailures 按配置跑四个模式，拼出 events.jsonl 的全部内容。
func AnalyzeFailures(cfg Config, sc *Scenario, apps []Application) *Signals {
	sig := &Signals{}
	if cfg.Has(FailDuplicate) {
		sig.Duplicates = DetectDuplicates(apps, cfg.Duration, cfg.ReportInterval)
		sig.Events = append(sig.Events, sig.Duplicates...)
	}
	if cfg.Has(FailFullRepush) {
		r := DetectFullRepush(cfg, len(sc.Tasks), sc.Changes)
		sig.Repush = &r
		sig.Events = append(sig.Events, r.Events...)
	}
	if cfg.Has(FailFlap) {
		on := SimulateFlap(cfg, sc, true)
		off := SimulateFlap(cfg, sc, false)
		sig.FlapOn = &on
		sig.FlapOff = &off
		sig.Events = append(sig.Events, on.Events...)
	}
	if cfg.Has(FailStale) {
		sig.Stale = SimulateStale(cfg, sc, false)
		if sig.Stale != nil {
			sig.Events = append(sig.Events, sig.Stale.Events...)
		}
	}
	sort.SliceStable(sig.Events, func(i, j int) bool {
		if sig.Events[i].AtMs != sig.Events[j].AtMs {
			return sig.Events[i].AtMs < sig.Events[j].AtMs
		}
		return sig.Events[i].Kind < sig.Events[j].Kind
	})
	for i := range sig.Events {
		sig.Events[i].Seq = i + 1
	}
	return sig
}

func round2(v float64) float64 {
	return float64(int64(v*100+0.5)) / 100
}
