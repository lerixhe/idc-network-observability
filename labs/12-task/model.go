package main

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

// baseTime 是虚拟时钟的零点。所有 produced_at / acked_ts 都由它加偏移算出：
// 同种子同参数跑两遍，产物逐字节一致，13-pipeline 的回放才能对上号。
var baseTime = time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC)

// ── 数据契约（与 labs/13-pipeline 共用，字段名一字不改）──────────────
//
//	上报批次：probe_id, batch_seq(uint64), produced_at(RFC3339), points[]
//	点位：   probe_id, acked_batch_seq, acked_ts
//	幂等键： (probe_id, batch_seq, metric_id)
//	最大补报延迟：15 分钟（-max-report-delay 可配）
//
// 两个 batch_seq 不是一回事，踩过一次坑：
//   - 信封上的 batch_seq 是本批序号，进幂等键，用来压整批重放；
//   - 点位上的 acked_batch_seq 是「这条确认第一次被上报时所在批次的序号」，
//     同一个值复制到后面每一批都不变，管道按它压掉周期重复上报。
type ReportBatch struct {
	ProbeID    string  `json:"probe_id"`
	BatchSeq   uint64  `json:"batch_seq"`
	ProducedAt string  `json:"produced_at"`
	Points     []Point `json:"points"`
}

// Point 是一条确认。metric_id 出现在幂等键里，所以点位必须带着它。
type Point struct {
	ProbeID       string `json:"probe_id"`
	MetricID      string `json:"metric_id"`
	AckedBatchSeq uint64 `json:"acked_batch_seq"`
	AckedTS       string `json:"acked_ts"`
}

// IdempotencyKey 是契约里的 (probe_id, batch_seq, metric_id)，batch_seq 取信封上的。
func (b ReportBatch) IdempotencyKey(p Point) string {
	return fmt.Sprintf("%s|%d|%s", b.ProbeID, b.BatchSeq, p.MetricID)
}

// stamp 把仿真偏移写成 RFC3339 时间戳。RFC3339Nano 在整秒上不会补零，
// 所以整秒输出仍然是标准的 2024-03-01T00:01:00Z，带毫秒时才是 .500000000 之内的一段。
func stamp(d time.Duration) string {
	return baseTime.Add(d).Format(time.RFC3339Nano)
}

// ── 控制面对象 ──────────────────────────────────────────────────

// Window 是一段离线区间，左闭右开，单位是相对仿真起点的偏移。
type Window struct {
	From time.Duration
	To   time.Duration
}

// Probe 是一台探针。DecommissionedAt < 0 表示全程在役。
type Probe struct {
	ProbeID          string
	Site             string
	Capacity         int
	Version          string
	Offline          []Window
	DecommissionedAt time.Duration
}

// online 判断某一刻探针是否连着控制面。
func (p Probe) online(t time.Duration) bool {
	if p.DecommissionedAt >= 0 && t >= p.DecommissionedAt {
		return false
	}
	for _, w := range p.Offline {
		if t >= w.From && t < w.To {
			return false
		}
	}
	return true
}

// NextOnlineAt 返回不早于 t 的第一个在线时刻。探针已退役则第二个返回值为 false。
func (p Probe) NextOnlineAt(t time.Duration) (time.Duration, bool) {
	cur := t
	for i := 0; i <= len(p.Offline)+1; i++ {
		if p.DecommissionedAt >= 0 && cur >= p.DecommissionedAt {
			return 0, false
		}
		next := cur
		inside := false
		for _, w := range p.Offline {
			if cur >= w.From && cur < w.To {
				inside = true
				if w.To > next {
					next = w.To
				}
			}
		}
		if !inside {
			return cur, true
		}
		cur = next
	}
	return 0, false
}

// LastSeen 是最后一次能联系上探针的时刻：在线状态取 now，否则取离线窗口起点。
// 误差不超过一个窗口边界，判定过期任务够用。
func (p Probe) LastSeen(now time.Duration) time.Duration {
	if p.online(now) {
		return now
	}
	best := time.Duration(-1)
	for _, w := range p.Offline {
		if w.From < now && w.From > best && (p.DecommissionedAt < 0 || w.From <= p.DecommissionedAt) {
			best = w.From
		}
	}
	if p.DecommissionedAt >= 0 && p.DecommissionedAt <= now && p.DecommissionedAt > best {
		best = p.DecommissionedAt
	}
	if best < 0 {
		return 0
	}
	return best
}

// onlineDuration 统计 [from, to) 内探针在线多久。
func (p Probe) onlineDuration(from, to time.Duration) time.Duration {
	if to <= from {
		return 0
	}
	total := to - from
	for _, w := range p.Offline {
		a, b := maxDur(from, w.From), minDur(to, w.To)
		if b > a {
			total -= b - a
		}
	}
	if p.DecommissionedAt >= 0 {
		if a := maxDur(from, p.DecommissionedAt); to > a {
			total -= to - a
		}
	}
	if total < 0 {
		return 0
	}
	return total
}

// Task 是一条采集任务。Revision 由变更推进，样本文件里不写。
type Task struct {
	TaskID   string `json:"task_id"`
	MetricID string `json:"metric_id"`
	Target   string `json:"target"`
	ProbeID  string `json:"probe_id"`
	Revision int    `json:"-"`
}

// ChangeKind 区分两类变更：内容变更与换点。
type ChangeKind string

const (
	KindRevise ChangeKind = "revise"
	KindMove   ChangeKind = "move"
)

// Change 是控制面的一次变更，Latency 统计的就是它到生效的间隔。
type Change struct {
	Seq       int
	TaskID    string
	MetricID  string
	Revision  int
	Kind      ChangeKind
	At        time.Duration
	FromProbe string
	ToProbe   string
	// DupInject 为真表示这次变更被同时下给了两个探针（撤销消息丢了）。
	DupInject bool
}

// AppRole 区分一次控制面消息是「派活」还是「收活」。
type AppRole string

const (
	RoleAssign AppRole = "assign"
	RoleRevoke AppRole = "revoke"
)

// Application 是一条任务落在某个探针上的结果。
// 判定层只看这个结构，注入的真相在 Scenario.Truth 里，最后两边对账。
type Application struct {
	TaskID    string
	MetricID  string
	Revision  int
	ProbeID   string
	Kind      ChangeKind
	Role      AppRole
	ChangeAt  time.Duration
	AppliedAt time.Duration
	Latency   time.Duration
	// Stale 表示生效延迟超过最大补报延迟：这批数据到了也进不了窗口。
	Stale bool
	// Dup 表示这条是重复下发造成的第二份应用。
	Dup bool
}

// Event 是失败模式吐出的可观测信号，走 events.jsonl，是告警与工单的来源。
type Event struct {
	Seq      int            `json:"seq"`
	AtMs     int64          `json:"at_ms"`
	Kind     string         `json:"kind"`
	Subject  string         `json:"subject"`
	Evidence map[string]any `json:"evidence,omitempty"`
	Note     string         `json:"note,omitempty"`
}

func newEvent(at time.Duration, kind, subject, note string, ev map[string]any) Event {
	return Event{AtMs: at.Milliseconds(), Kind: kind, Subject: subject, Evidence: ev, Note: note}
}

// ── 延迟统计 ────────────────────────────────────────────────────

// LatencyStats 是一组「变更到生效」延迟的分位。
type LatencyStats struct {
	N   int
	Min time.Duration
	P50 time.Duration
	P90 time.Duration
	P95 time.Duration
	P99 time.Duration
	Max time.Duration
}

// NewLatencyStats 会对入参排序，调用方不必自己排。
func NewLatencyStats(in []time.Duration) LatencyStats {
	if len(in) == 0 {
		return LatencyStats{}
	}
	s := make([]time.Duration, len(in))
	copy(s, in)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	return LatencyStats{
		N:   len(s),
		Min: s[0],
		P50: percentile(s, 50),
		P90: percentile(s, 90),
		P95: percentile(s, 95),
		P99: percentile(s, 99),
		Max: s[len(s)-1],
	}
}

// percentile 用最近秩法，p 取 0–100。
func percentile(sorted []time.Duration, p float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(math.Ceil(p/100*float64(len(sorted)))) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

// Bucket 是延迟直方图的一档。
type Bucket struct {
	Label string
	Count int
}

// latencyBounds 是直方图的分档边界。最后一档兜住 ≥5m，包括那些超过补报窗口的。
var latencyBounds = []time.Duration{
	time.Second, 5 * time.Second, 10 * time.Second, time.Minute, 5 * time.Minute,
}

func latencyLabel(d time.Duration) string {
	switch {
	case d < time.Second:
		return "<1s"
	case d < 5*time.Second:
		return "<5s"
	case d < 10*time.Second:
		return "<10s"
	case d < time.Minute:
		return "<1m"
	case d < 5*time.Minute:
		return "<5m"
	default:
		return "≥5m"
	}
}

// Histogram 按 latencyBounds 分档，返回 6 档，最后一档是 ≥5m。
func Histogram(in []time.Duration) []Bucket {
	labels := make([]string, 0, len(latencyBounds)+1)
	labels = append(labels, latencyLabel(0))
	for _, b := range latencyBounds {
		labels = append(labels, latencyLabel(b))
	}
	counts := make(map[string]int, len(labels))
	for _, d := range in {
		counts[latencyLabel(d)]++
	}
	out := make([]Bucket, 0, len(labels))
	for _, l := range labels {
		out = append(out, Bucket{Label: l, Count: counts[l]})
	}
	return out
}

// ── 配置 ────────────────────────────────────────────────────────

// FailMode 是四个要注入并检测的失败模式。
type FailMode string

const (
	FailDuplicate  FailMode = "duplicate"
	FailFullRepush FailMode = "full-repush"
	FailFlap       FailMode = "flap"
	FailStale      FailMode = "stale"
)

var allFailModes = []FailMode{FailDuplicate, FailFullRepush, FailFlap, FailStale}

// ParseFailModes 接受 all / none / 逗号分隔的模式名。
func ParseFailModes(s string) ([]FailMode, error) {
	s = strings.TrimSpace(strings.ToLower(s))
	switch s {
	case "", "none":
		return nil, nil
	case "all":
		return append([]FailMode(nil), allFailModes...), nil
	}
	seen := make(map[FailMode]bool, len(allFailModes))
	out := make([]FailMode, 0, len(allFailModes))
	for _, f := range strings.Split(s, ",") {
		m := FailMode(strings.TrimSpace(f))
		ok := false
		for _, cand := range allFailModes {
			if m == cand {
				ok = true
				break
			}
		}
		if !ok {
			return nil, fmt.Errorf("未知失败模式 %q，可选：duplicate / full-repush / flap / stale / all / none", f)
		}
		if !seen[m] {
			seen[m] = true
			out = append(out, m)
		}
	}
	return out, nil
}

// Config 是一次仿真的全部参数。
type Config struct {
	Probes           int
	Duration         time.Duration
	Pull             bool
	Push             bool
	Poll             time.Duration
	Heartbeat        time.Duration
	PushDelay        time.Duration
	ReconnectBackoff time.Duration
	ReportInterval   time.Duration
	MaxReportDelay   time.Duration
	FailModes        []FailMode
	FlapReplaceLimit int
	StaleTTL         time.Duration
	// CtlRate 是控制面每秒最多下发多少条，全量重推排空时间由它决定。
	CtlRate float64
	Seed    int64
}

func (c Config) Has(m FailMode) bool {
	for _, f := range c.FailModes {
		if f == m {
			return true
		}
	}
	return false
}

// Validate 挡住会让实验静默跑出错误结论的参数组合。
func (c Config) Validate() error {
	switch {
	case c.Probes <= 0:
		return errors.New("探针数必须大于 0")
	case c.Duration < 2*time.Minute:
		return fmt.Errorf("仿真时长 %s 太短，至少 2m：变更延迟的统计要落在窗口里", dur(c.Duration))
	case !c.Pull && !c.Push:
		return errors.New("至少要选一种分发模式：-modes pull / push / both")
	case c.Poll <= 0:
		return errors.New("拉取周期必须大于 0")
	case c.Heartbeat <= 0:
		return errors.New("长连接心跳周期必须大于 0")
	case c.ReportInterval <= 0:
		return errors.New("上报周期必须大于 0")
	case c.MaxReportDelay <= 0:
		return errors.New("最大补报延迟必须大于 0")
	case c.StaleTTL <= 0:
		return errors.New("探针静默多久算过期必须大于 0")
	case c.FlapReplaceLimit < 1:
		return errors.New("坏点替换抑制阈值至少为 1")
	case c.CtlRate <= 0:
		return errors.New("控制面限流速率必须大于 0")
	case c.PushDelay < 0:
		return errors.New("单程推送延迟不能为负")
	}
	return nil
}

// ── 小工具 ──────────────────────────────────────────────────────

func minDur(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}

func maxDur(a, b time.Duration) time.Duration {
	if a > b {
		return a
	}
	return b
}

// dur 把时长写成人能读的形态：25ms / 5s / 4.5s / 6m12s。
func dur(d time.Duration) string {
	neg := d < 0
	if neg {
		d = -d
	}
	var s string
	switch {
	case d < time.Millisecond:
		s = fmt.Sprintf("%dµs", d.Microseconds())
	case d < time.Second:
		s = fmt.Sprintf("%dms", d.Milliseconds())
	case d < time.Minute:
		if d%time.Second == 0 {
			s = fmt.Sprintf("%ds", int(d.Seconds()))
		} else {
			s = fmt.Sprintf("%.1fs", d.Seconds())
		}
	case d < time.Hour:
		s = fmt.Sprintf("%dm%ds", int(d.Minutes()), int(d.Seconds())%60)
	default:
		s = fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
	}
	if neg {
		return "-" + s
	}
	return s
}

// secs 把时长写成秒，进 signal 的 evidence 用，别把裸纳秒塞进 JSON。
func secs(d time.Duration) float64 {
	return math.Round(d.Seconds()*1000) / 1000
}
