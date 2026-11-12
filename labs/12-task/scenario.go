package main

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"sort"
	"strings"
	"time"
)

// Truth 是注入的真值。判定层看不到它，跑完拿它对账。
type Truth struct {
	Changes              int      `json:"changes"`
	Revises              int      `json:"revises"`
	Moves                int      `json:"moves"`
	DuplicateDispatches  int      `json:"duplicate_dispatches"`
	DuplicateTasks       []string `json:"duplicate_tasks"`
	FlapTasks            []string `json:"flap_tasks"`
	StaleProbes          []string `json:"stale_probes"`
	StaleTasks           []string `json:"stale_tasks"`
	DecommissionedProbes int      `json:"decommissioned_probes"`
	OfflineProbes        int      `json:"offline_probes"`
	// TailChanges 是被刻意塞进离线窗口的变更数：随机时刻撞不进窗口，尾延迟就没素材。
	TailChanges int `json:"tail_changes"`
}

// Scenario 是一次仿真的输入：探针、任务、变更、真值。
type Scenario struct {
	Probes  []Probe
	Tasks   []Task
	Changes []Change
	Truth   Truth
}

// OwnerIndex 是 probe_id → 下标。
func (s *Scenario) OwnerIndex() map[string]int {
	m := make(map[string]int, len(s.Probes))
	for i, p := range s.Probes {
		m[p.ProbeID] = i
	}
	return m
}

// TasksOf 返回挂在某台探针名下的任务（按样本里的初始归属）。
func (s *Scenario) TasksOf(probeID string) []Task {
	var out []Task
	for _, t := range s.Tasks {
		if t.ProbeID == probeID {
			out = append(out, t)
		}
	}
	return out
}

// ── 样本文件 ────────────────────────────────────────────────────

type windowJSON struct {
	FromS float64 `json:"from_s"`
	ToS   float64 `json:"to_s"`
}

type probeJSON struct {
	ProbeID           string       `json:"probe_id"`
	Site              string       `json:"site"`
	Capacity          int          `json:"capacity"`
	Version           string       `json:"version"`
	Offline           []windowJSON `json:"offline"`
	DecommissionedAtS *float64     `json:"decommissioned_at_s"`
}

type rosterFile struct {
	Sites  []string    `json:"sites"`
	Probes []probeJSON `json:"probes"`
}

type taskFile struct {
	Tasks []Task `json:"tasks"`
}

func secondsToDuration(s float64) time.Duration {
	return time.Duration(s * float64(time.Second))
}

// LoadRoster 读探针清单。文件缺失直接报错，样本就是这个实验的输入，不能悄悄用内置值。
func LoadRoster(path string) ([]Probe, []string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, fmt.Errorf("读探针清单 %s 失败：%w", path, err)
	}
	var f rosterFile
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, nil, fmt.Errorf("解析 %s 失败：%w", path, err)
	}
	if len(f.Probes) == 0 {
		return nil, nil, fmt.Errorf("%s 里没有 probes", path)
	}
	out := make([]Probe, 0, len(f.Probes))
	seen := make(map[string]bool, len(f.Probes))
	for _, pj := range f.Probes {
		if pj.ProbeID == "" {
			return nil, nil, fmt.Errorf("%s：有探针缺 probe_id", path)
		}
		if seen[pj.ProbeID] {
			return nil, nil, fmt.Errorf("%s：probe_id %s 重复登记", path, pj.ProbeID)
		}
		seen[pj.ProbeID] = true
		p := Probe{
			ProbeID:          pj.ProbeID,
			Site:             pj.Site,
			Capacity:         pj.Capacity,
			Version:          pj.Version,
			DecommissionedAt: -1,
		}
		if p.Capacity <= 0 {
			return nil, nil, fmt.Errorf("%s %s：capacity 必须大于 0", path, pj.ProbeID)
		}
		for _, w := range pj.Offline {
			if w.ToS <= w.FromS {
				return nil, nil, fmt.Errorf("%s %s：离线窗口 from_s %v ≥ to_s %v", path, pj.ProbeID, w.FromS, w.ToS)
			}
			p.Offline = append(p.Offline, Window{
				From: secondsToDuration(w.FromS),
				To:   secondsToDuration(w.ToS),
			})
		}
		if pj.DecommissionedAtS != nil {
			p.DecommissionedAt = secondsToDuration(*pj.DecommissionedAtS)
		}
		out = append(out, p)
	}
	return out, f.Sites, nil
}

// LoadTasks 读任务集。
func LoadTasks(path string) ([]Task, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读任务集 %s 失败：%w", path, err)
	}
	var f taskFile
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("解析 %s 失败：%w", path, err)
	}
	if len(f.Tasks) == 0 {
		return nil, fmt.Errorf("%s 里没有 tasks", path)
	}
	seen := make(map[string]bool, len(f.Tasks))
	seenMetric := make(map[string]string, len(f.Tasks))
	for i := range f.Tasks {
		t := &f.Tasks[i]
		switch {
		case t.TaskID == "":
			return nil, fmt.Errorf("%s：第 %d 条任务缺 task_id", path, i)
		case t.MetricID == "":
			return nil, fmt.Errorf("%s：任务 %s 缺 metric_id，幂等键要靠它", path, t.TaskID)
		case t.Target == "":
			return nil, fmt.Errorf("%s：任务 %s 缺 target", path, t.TaskID)
		case t.ProbeID == "":
			return nil, fmt.Errorf("%s：任务 %s 缺 probe_id", path, t.TaskID)
		}
		if seen[t.TaskID] {
			return nil, fmt.Errorf("%s：task_id %s 重复", path, t.TaskID)
		}
		// metric_id 是时序的唯一标识，必须带上目标：两条任务撞同一个 metric_id，
		// 同一台探针上 (probe_id, batch_seq, metric_id) 就会碰撞，管道一压就丢一条。
		if prev, ok := seenMetric[t.MetricID]; ok {
			return nil, fmt.Errorf("%s：任务 %s 与 %s 共用 metric_id %s，幂等键会碰撞",
				path, t.TaskID, prev, t.MetricID)
		}
		seenMetric[t.MetricID] = t.TaskID
		seen[t.TaskID] = true
		t.Revision = 1
	}
	return f.Tasks, nil
}

// ── 场景构造 ────────────────────────────────────────────────────

// expandProbes 把样本清单补到目标规模。补出来的探针没有离线窗口、不退役，
// 只有样本里那几台带着坑。
func expandProbes(n int, roster []Probe, sites []string) ([]Probe, error) {
	if n < len(roster) {
		return nil, fmt.Errorf("探针数 %d 小于样本清单 %d 台：样本里的坑（离线、退役）大多在后半段，截断会丢注入点", n, len(roster))
	}
	out := make([]Probe, 0, n)
	out = append(out, roster...)
	if len(sites) == 0 {
		sites = []string{"room-a"}
	}
	version := roster[0].Version
	for i := len(roster); i < n; i++ {
		out = append(out, Probe{
			ProbeID:          fmt.Sprintf("probe-%s-%05d", siteCode(sites[i%len(sites)]), i),
			Site:             sites[i%len(sites)],
			Capacity:         8,
			Version:          version,
			DecommissionedAt: -1,
		})
	}
	return out, nil
}

// baseMetric 取指标名，丢掉目标后缀：样本里的 metric_id 形如 if_util.leaf-a1f-01.eth1-1。
func baseMetric(metricID string) string {
	if i := strings.Index(metricID, "."); i > 0 {
		return metricID[:i]
	}
	return metricID
}

// siteCode 把 room-a 取成 a，只用来拼探针名。
func siteCode(site string) string {
	if i := len(site) - 1; i >= 0 {
		return string(site[i])
	}
	return "x"
}

// expandTasks 给补出来的探针配上任务。十万探针那一档要是只有 14 条任务，
// 变更数、控制面峰值这些数就全是摆设：一台探针平均一条任务才是 IDC 里的常态。
func expandTasks(probes []Probe, rosterLen int, tasks []Task) []Task {
	out := append([]Task(nil), tasks...)
	if len(probes) <= rosterLen {
		return out
	}
	metrics := make([]string, 0, len(tasks))
	for _, t := range tasks {
		metrics = append(metrics, baseMetric(t.MetricID))
	}
	for i := rosterLen; i < len(probes); i++ {
		p := probes[i]
		out = append(out, Task{
			TaskID:   fmt.Sprintf("t-auto-%05d", i),
			MetricID: fmt.Sprintf("%s.leaf-auto-%05d.eth1-1", metrics[i%len(metrics)], i),
			Target:   fmt.Sprintf("leaf-auto-%05d/Ethernet1/1", i),
			ProbeID:  p.ProbeID,
			Revision: 1,
		})
	}
	return out
}

// BuildScenario 组装探针、任务、变更与真值。同一个 seed 出来的变更序列完全一样。
func BuildScenario(cfg Config, roster []Probe, sites []string, tasks []Task) (*Scenario, error) {
	probes, err := expandProbes(cfg.Probes, roster, sites)
	if err != nil {
		return nil, err
	}
	idx := make(map[string]int, len(probes))
	for i, p := range probes {
		idx[p.ProbeID] = i
	}
	for _, t := range tasks {
		if _, ok := idx[t.ProbeID]; !ok {
			return nil, fmt.Errorf("任务 %s 挂在 %s 上，但探针清单里没有这台", t.TaskID, t.ProbeID)
		}
	}

	tasks = expandTasks(probes, len(roster), tasks)
	sc := &Scenario{Probes: probes, Tasks: tasks}
	rng := rand.New(rand.NewSource(cfg.Seed))

	// 变更只有两类：内容变更（同一个点，revision+1）与换点（任务搬到另一台探针）。
	// 换点每 4 条任务来一次，够跑出延迟分布，又不至于把控制面压满。
	var moves, revises []Change
	high := cfg.Duration - cfg.ReportInterval
	if high <= cfg.ReportInterval {
		high = cfg.Duration
	}
	for i, t := range tasks {
		at := cfg.ReportInterval + time.Duration(rng.Int63n(int64(high-cfg.ReportInterval)))
		revises = append(revises, Change{
			TaskID: t.TaskID, MetricID: t.MetricID, Revision: t.Revision + 1,
			Kind: KindRevise, At: at, FromProbe: t.ProbeID, ToProbe: t.ProbeID,
		})
		if i%4 != 0 {
			continue
		}
		to, ok := nextProbeInSite(probes, idx, t.ProbeID, 1)
		if !ok {
			continue
		}
		mAt := cfg.ReportInterval + time.Duration(rng.Int63n(int64(high-cfg.ReportInterval)))
		if mAt == at {
			mAt += cfg.Poll
		}
		moves = append(moves, Change{
			TaskID: t.TaskID, MetricID: t.MetricID, Revision: t.Revision + 2,
			Kind: KindMove, At: mAt, FromProbe: t.ProbeID, ToProbe: to,
		})
	}
	// 刻意把离线探针名下的那条变更塞进它的离线窗口。尾延迟、迟到补报、
	// 补报超限丢弃都需要素材，靠随机时刻撞窗口是撞不出来的。
	for _, p := range probes {
		if len(p.Offline) == 0 {
			continue
		}
		var owner *Task
		for i := range tasks {
			if tasks[i].ProbeID == p.ProbeID {
				owner = &tasks[i]
				break
			}
		}
		if owner == nil {
			continue
		}
		w := p.Offline[0]
		at := w.From + (w.To-w.From)/4
		for i := range revises {
			if revises[i].TaskID == owner.TaskID {
				revises[i].At = at
				sc.Truth.TailChanges++
			}
		}
	}

	// 换点排在内容变更之后：Revise 先到、Move 后到，撤销才有东西可撤。
	sort.SliceStable(revises, func(i, j int) bool { return revises[i].At < revises[j].At })
	sort.SliceStable(moves, func(i, j int) bool { return moves[i].At < moves[j].At })

	// 重复下发注入：每 2 次换点丢一次撤销消息，条件是旧点此刻在线——
	// 旧点不在线就压不出「两个探针同时接活」这个现象，注入了也白注入，不计数。
	dupInjected := 0
	var dupTasks []string
	if cfg.Has(FailDuplicate) {
		for k := range moves {
			if k%2 != 0 {
				continue
			}
			m := &moves[k]
			old := probes[idx[m.FromProbe]]
			if !old.online(m.At) {
				continue
			}
			m.DupInject = true
			dupInjected++
			dupTasks = append(dupTasks, m.TaskID)
		}
	}

	changes := append(append([]Change(nil), revises...), moves...)
	sort.SliceStable(changes, func(i, j int) bool {
		if changes[i].At != changes[j].At {
			return changes[i].At < changes[j].At
		}
		return changes[i].TaskID < changes[j].TaskID
	})
	for i := range changes {
		changes[i].Seq = i + 1
	}
	sc.Changes = changes

	// 坏点：前两条挂在在役探针上的任务，目标设备整段不应答。
	for _, t := range tasks {
		if len(sc.Truth.FlapTasks) >= 2 {
			break
		}
		if probes[idx[t.ProbeID]].DecommissionedAt >= 0 {
			continue
		}
		sc.Truth.FlapTasks = append(sc.Truth.FlapTasks, t.TaskID)
	}
	// 过期任务：退役探针名下的任务。
	for _, p := range probes {
		// 仿真结束之后才退役的探针不进真值：它在本窗口里一直在线，过期任务无从注入。
		if p.DecommissionedAt < 0 || p.DecommissionedAt > cfg.Duration {
			continue
		}
		sc.Truth.DecommissionedProbes++
		sc.Truth.StaleProbes = append(sc.Truth.StaleProbes, p.ProbeID)
		for _, t := range tasks {
			if t.ProbeID == p.ProbeID {
				sc.Truth.StaleTasks = append(sc.Truth.StaleTasks, t.TaskID)
			}
		}
	}
	for _, p := range probes {
		if len(p.Offline) > 0 {
			sc.Truth.OfflineProbes++
		}
	}

	sc.Truth.Changes = len(changes)
	sc.Truth.Revises = len(revises)
	sc.Truth.Moves = len(moves)
	sc.Truth.DuplicateDispatches = dupInjected
	sc.Truth.DuplicateTasks = dupTasks
	return sc, nil
}

// nextProbeInSite 从 from 往后找第 k 台同机房的探针；同机房没有就跨机房兜一圈。
func nextProbeInSite(probes []Probe, idx map[string]int, from string, k int) (string, bool) {
	start, ok := idx[from]
	if !ok || len(probes) < 2 {
		return "", false
	}
	site := probes[start].Site
	tries := 0
	for off := 1; off <= len(probes) && tries < k; off++ {
		cand := probes[(start+off)%len(probes)]
		if cand.ProbeID == from || cand.Site != site {
			continue
		}
		tries++
		if tries == k {
			return cand.ProbeID, true
		}
	}
	for off := 1; off <= len(probes); off++ {
		cand := probes[(start+off)%len(probes)]
		if cand.ProbeID != from {
			return cand.ProbeID, true
		}
	}
	return "", false
}
