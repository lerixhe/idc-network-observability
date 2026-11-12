package main

import (
	"fmt"
	"sort"
	"strings"
)

// SuppressPolicy 是抑制口径。四个都是参数，不是常量。
//
//	LagMs          父告警必须比子告警早出现多久——同一秒冒出来的两条，
//	               是并发或后果，不是原因。
//	DegradeWindowMs 血缘缺失时，允许往回找多久以内的告警当疑似父。
//	ResolveGraceMs  父消失后，子告警在这个窗口内也恢复，就不补发。
//	ExplainRequired 是否要求父的类型能解释子的类型。
type SuppressPolicy struct {
	LagMs           int64
	DegradeWindowMs int64
	ResolveGraceMs  int64
	ExplainRequired bool
}

var defaultSuppressPolicy = SuppressPolicy{
	LagMs:           5_000,
	DegradeWindowMs: 60_000,
	ResolveGraceMs:  120_000,
	ExplainRequired: true,
}

// Outcome 是一条告警的处置结果。JSONL 每行一条。
type Outcome struct {
	ID        string `json:"id"`
	TS        string `json:"ts"`
	ResolveTS string `json:"resolve_ts,omitempty"`
	Obj       string `json:"obj"`
	Node      string `json:"node,omitempty"`
	// Path 是对象树里从根到这条告警对象的路径，直接回答「它为什么该被压」。
	Path     string `json:"path,omitempty"`
	Class    string `json:"class"`
	Severity string `json:"severity"`
	Truth    string `json:"truth"`

	Suppressed bool `json:"suppressed"`
	// SuppressedBy 是被谁压的。这一列是审计的全部依据，缺失即等于永久吞掉。
	SuppressedBy    string `json:"suppressed_by,omitempty"`
	SuppressedByObj string `json:"suppressed_by_obj,omitempty"`
	// SuppressChain 是压制链：直接父 → 祖父 → …，最多 6 层。
	SuppressChain []string `json:"suppress_chain,omitempty"`

	// Orphan 表示这条告警在拓扑里没有血缘（对象未知或节点无父），
	// 只能靠时间窗关联。Degraded 表示它确实被时间窗关联压掉了。
	Orphan        bool   `json:"orphan"`
	OrphanReason  string `json:"orphan_reason,omitempty"`
	Degraded      bool   `json:"degraded"`
	ResumedAt     string `json:"resumed_at,omitempty"`
	RestoredGrace bool   `json:"restored_within_grace,omitempty"`

	Note string `json:"note,omitempty"`
}

// SuppressResult 是一次回放的汇总。
type SuppressResult struct {
	Outcomes []Outcome

	Alerts int // 原始告警条数
	// Emitted 是最终需要独立投递的条数：没被压的 + 压了但父消失后必须补发的。
	Emitted int
	// Suppressed 是被压过的条数，NetConverged 是其中父消失后自己已经恢复、
	// 不需要补发的（真正省掉的投递）。
	Suppressed   int
	NetConverged int
	Resumed      int

	// Collateral 是误伤：被压掉但真值不是 child 的条数（真根因、或另一件独立故障）。
	Collateral         int
	DegradedSuppressed int
	DegradedCollateral int
	Orphans            int

	TopSuppressor    string
	TopSuppressorCnt int
}

// Ratio 是收敛比 N/M。M 为 0 时返回 0，由调用方说明。
func (r SuppressResult) Ratio() float64 {
	if r.Emitted == 0 {
		return 0
	}
	return float64(r.Alerts) / float64(r.Emitted)
}

// SuppressRatio 是抑制率 (N-M)/N。
func (r SuppressResult) SuppressRatio() float64 {
	if r.Alerts == 0 {
		return 0
	}
	return float64(r.Alerts-r.Emitted) / float64(r.Alerts)
}

// engine 是一次回放的上下文。所有判定都是纯函数式地读 tree 与 policy，没有隐藏状态。
type engine struct {
	tree     *ObjectTree
	policy   SuppressPolicy
	alerts   []Alert
	moments  []int64
	byID     map[string]*Alert
	nodeOfID map[string]string
}

func (a *Alert) activeAt(t int64) bool {
	return a.tsMs <= t && (a.resolveMs == 0 || a.resolveMs > t)
}

// Suppress 是抑制入口：按事件时间回放，逐条判定压不压、压了多久、要不要补发。
//
// 回放的粒度是「事件时刻」——每来一条告警或一条恢复，活跃集就变一次。
// 逐条时间点重算、不一次性求值，才能回答那个真要命的问题：
// 父告警消失之后，被压住的子告警有没有回来。
func Suppress(tree *ObjectTree, alerts []Alert, policy SuppressPolicy) SuppressResult {
	e := &engine{
		tree:     tree,
		policy:   policy,
		alerts:   alerts,
		byID:     make(map[string]*Alert, len(alerts)),
		nodeOfID: make(map[string]string, len(alerts)),
	}
	// 重复 ID 会让审计链断在一半，宁可拒收。
	for i := range alerts {
		a := &alerts[i]
		if _, dup := e.byID[a.ID]; dup {
			// 调用方已经 load 过一遍，这里只做保护性跳过。
			continue
		}
		e.byID[a.ID] = a
		node, _ := tree.Resolve(a.Obj)
		e.nodeOfID[a.ID] = node
	}

	momentSet := make(map[int64]bool, len(alerts)*2)
	for _, a := range alerts {
		momentSet[a.tsMs] = true
		if a.resolveMs != 0 {
			momentSet[a.resolveMs] = true
		}
	}
	e.moments = make([]int64, 0, len(momentSet))
	for m := range momentSet {
		e.moments = append(e.moments, m)
	}
	sort.Slice(e.moments, func(i, j int) bool { return e.moments[i] < e.moments[j] })

	res := SuppressResult{Alerts: len(alerts)}
	suppressorHits := map[string]int{}

	for i := range alerts {
		a := &alerts[i]
		o := Outcome{
			ID: a.ID, TS: a.TS, ResolveTS: a.ResolveTS,
			Obj: a.Obj, Node: e.nodeOfID[a.ID],
			Class: a.Class, Severity: a.Severity, Truth: a.Truth,
		}
		if o.Node != "" {
			o.Path = tree.Path(o.Node)
		}
		o.Orphan, o.OrphanReason = e.orphanOf(a, o.Node)

		parent, degraded := e.parentAt(a, a.tsMs)
		if parent == nil {
			o.Note = e.noParentNote(o)
			res.Emitted++
			if o.Orphan {
				res.Orphans++
			}
			res.Outcomes = append(res.Outcomes, o)
			continue
		}

		o.Suppressed = true
		o.Degraded = degraded
		o.SuppressedBy = parent.ID
		o.SuppressedByObj = parent.Obj
		o.SuppressChain = e.chainOf(parent, a.tsMs, 6)
		if pid, ok := tree.Resolve(parent.Obj); ok {
			o.Note = "父告警 " + parent.ID + " 活跃于 " + tree.Path(pid)
		} else {
			o.Note = "父告警 " + parent.ID + " 活跃（对象 " + parent.Obj + " 不在拓扑里）"
		}
		res.Suppressed++
		suppressorHits[parent.ID]++
		if o.Orphan {
			res.Orphans++
		}
		if o.Truth != TruthChild {
			res.Collateral++
			if degraded {
				res.DegradedCollateral++
			}
		}
		if degraded {
			res.DegradedSuppressed++
		}

		release, kind := e.releaseAt(a)
		switch kind {
		case releaseNever:
			// 父一直压到样本结束，子告警没有补发机会。
			res.NetConverged++
			o.Note += "；父一直活跃到样本结束，不补发"
		case releaseBeforeChild:
			// 父还在的时候子自己就恢复了，是同一件事的尾巴，不补发。
			res.NetConverged++
			o.Note += "；父仍在时自己恢复，不补发"
		default:
			if a.resolveMs != 0 && a.resolveMs <= release+policy.ResolveGraceMs {
				res.NetConverged++
				o.RestoredGrace = true
				o.Note += fmt.Sprintf("；%s 父消失后 %ds 内同步恢复，不补发",
					fmtTS(release), (a.resolveMs-release)/1000)
			} else {
				// 父没了，子还在——这条必须补发，否则就是永久吞噬。
				res.Resumed++
				o.ResumedAt = fmtTS(release)
				o.Note += "；" + fmtTS(release) + " 父告警消失后补发"
			}
		}
		res.Outcomes = append(res.Outcomes, o)
	}

	res.Emitted = res.Alerts - res.NetConverged
	for id, n := range suppressorHits {
		if n > res.TopSuppressorCnt || (n == res.TopSuppressorCnt && id < res.TopSuppressor) {
			res.TopSuppressor, res.TopSuppressorCnt = id, n
		}
	}
	return res
}

// orphanOf 判断血缘缺失与原因。缺失不等于被压——它只是少了那条最可靠的依据。
func (e *engine) orphanOf(a *Alert, node string) (bool, string) {
	if node == "" {
		return true, DegradeObjectUnknown
	}
	if !e.tree.HasParent(node) {
		return true, DegradeNoAncestor
	}
	return false, ""
}

// parentAt 找 t 时刻压住 a 的那条告警。返回的 degraded 表示这次压制靠的是时间窗而不是拓扑。
//
// 两条路：
//
//	有血缘  父必须是严格祖先、早于子 LagMs、类型能解释子。
//	无血缘  对象在树里查不到、或节点没有父。只能用时间窗往回找：
//	        更早、在窗口内、类型能解释、严重度不低于自己。这条路的误伤是结构性的。
func (e *engine) parentAt(a *Alert, t int64) (*Alert, bool) {
	node := e.nodeOfID[a.ID]
	orphan := node == "" || !e.tree.HasParent(node)
	if !orphan {
		return e.bestAncestorAt(a, node, t), false
	}
	return e.bestWindowPeerAt(a, t), true
}

// bestAncestorAt 在活跃的祖先里挑最合适的一条。
//
// 挑「最早的那条」而不是「最严重的那条」：最早的那条是根因，
// 后面冒出来的是它的后果，拿后果当压制依据，报告里就会出现
// 「A 被 B 压掉，而 B 自己也被别的压掉」这种解释不通的链。
func (e *engine) bestAncestorAt(a *Alert, node string, t int64) *Alert {
	var best *Alert
	for i := range e.alerts {
		p := &e.alerts[i]
		if p.ID == a.ID || !p.activeAt(t) {
			continue
		}
		if p.tsMs+e.policy.LagMs > a.tsMs {
			// 父必须比子早站稳。同秒、或比子更晚的一律不压。
			continue
		}
		pnode := e.nodeOfID[p.ID]
		if pnode == "" || !e.tree.IsAncestor(pnode, node) {
			continue
		}
		if e.policy.ExplainRequired && !explains(p.Class, a.Class) {
			continue
		}
		if best == nil || p.tsMs < best.tsMs || (p.tsMs == best.tsMs && p.order < best.order) {
			best = p
		}
	}
	return best
}

// bestWindowPeerAt 是无血缘时的退化路径：时间窗关联。
//
// 三个约束都是硬的：更早、窗口内、类型能解释。少了类型这一条，
// 它就变成「一分钟内谁红得早谁当爹」——那是误伤工厂。
//
// 这条路的输出永远带 degraded 标记，因为它是猜的。
func (e *engine) bestWindowPeerAt(a *Alert, t int64) *Alert {
	var best *Alert
	for i := range e.alerts {
		p := &e.alerts[i]
		if p.ID == a.ID || !p.activeAt(t) {
			continue
		}
		if p.tsMs+e.policy.LagMs > a.tsMs {
			continue
		}
		if a.tsMs-p.tsMs > e.policy.DegradeWindowMs {
			continue
		}
		// 疑似父自己的对象也必须已知。两个都查不到对象的告警互相压制，
		// 等于拿未知压未知，压完谁也说不清为什么。
		if e.nodeOfID[p.ID] == "" {
			continue
		}
		if e.policy.ExplainRequired && !explains(p.Class, a.Class) {
			continue
		}
		if sevRank(p.Severity) < sevRank(a.Severity) {
			continue
		}
		if best == nil || p.tsMs < best.tsMs || (p.tsMs == best.tsMs && p.order < best.order) {
			best = p
		}
	}
	return best
}

// releaseKind 说清楚「为什么没有补发时刻」。两种原因看起来一样，代价完全不同。
type releaseKind int

const (
	// releaseNever：父一直活跃到样本结束，子告警根本没等到补发的机会。
	releaseNever releaseKind = iota
	// releaseBeforeChild：父还在的时候子自己就恢复了，本来就不用补。
	releaseBeforeChild
	// releaseFound：找到释放时刻，后面按宽限期判补不补。
	releaseFound
)

// releaseAt 找第一个「父已经全部消失、而子还活着」的时刻。
//
// 这个时刻就是补发时刻。找不到时的原因分两种，报告里必须分开写：
// 「父压到底」是设计缺陷，「子先恢复」是正常收敛，混成一句话会被当成同一个问题。
func (e *engine) releaseAt(a *Alert) (int64, releaseKind) {
	for _, t := range e.moments {
		if t <= a.tsMs {
			continue
		}
		if !a.activeAt(t) {
			// 子自己先恢复了，之后没有补发的必要。
			return 0, releaseBeforeChild
		}
		if p, _ := e.parentAt(a, t); p == nil {
			return t, releaseFound
		}
	}
	return 0, releaseNever
}

// chainOf 往上追压制链。限深是必须的：脏数据里环很常见。
func (e *engine) chainOf(parent *Alert, t int64, depth int) []string {
	var chain []string
	cur := parent
	for i := 0; i < depth; i++ {
		chain = append(chain, cur.ID)
		next, degraded := e.parentAt(cur, t)
		if next == nil || degraded {
			break
		}
		cur = next
	}
	return chain
}

func (e *engine) noParentNote(o Outcome) string {
	if o.Orphan {
		switch o.OrphanReason {
		case DegradeObjectUnknown:
			return "对象不在拓扑里，时间窗内也没找到可解释的上游告警，正常发出"
		case DegradeNoAncestor:
			return "节点在拓扑里但没有父（孤岛），时间窗内也没找到可解释的上游告警，正常发出"
		}
	}
	return "没有活跃的祖先告警，正常发出"
}

func sevRank(s string) int {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "critical":
		return 3
	case "major":
		return 2
	case "warning":
		return 1
	default:
		return 0
	}
}

// sortedOutcomes 按抑制优先级排序：先压掉的、再没压的，各自保持时间序。
// 报告里先看被压的那批，人才会去点父告警。
func sortedOutcomes(list []Outcome) []Outcome {
	out := append([]Outcome(nil), list...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Suppressed != out[j].Suppressed {
			return out[i].Suppressed
		}
		return out[i].TS < out[j].TS
	})
	return out
}
