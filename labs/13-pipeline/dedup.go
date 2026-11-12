package main

import "fmt"

// 批次与点位去重。
//
// 幂等键是 (probe_id, batch_seq, metric_id)，三段全部来自契约。键到点位粒度而不是批次粒度：
// 整批粒度只能全收或全丢，收一半再重放就翻倍。带上 metric_id，「部分写入之后重放」才能补全。
//
// 一个批次号再来一次，有四种可能，判别顺序不能颠倒：
//
//	整批重放   每个点的值都与上次相同，一个点都不进聚合
//	部分补全   上次只写进了前几个点（进程被杀、磁盘满、连接断），这次是超集且交集值相同，只收新点
//	冲突       同一个 (probe_id, batch_seq) 两次的值不一样，整批拒收并标记
//	新批次     没见过这个 seq（可能小于已见最大值，那是探针重启后批次号从头数）
//
// 过了上面这道闸的点还要过第二道：同一条确认被后续批次重复上报时，
// 幂等键不同、确认键相同，只收第一次。
//
// 冲突这一格不许「以后到的为准」。同 seq 同 metric 两个不同的值，谁对谁错没有证据，
// 合并只会造出一个从没发生过的数。

// seqReorderAllowance 是批次号允许的回退幅度。局部乱序会让同一个探针的批次号前后颠倒几档，
// 那不是回退；探针重启是从 118 掉回 1，差两个数量级。这条阈值就是分界线。
const seqReorderAllowance = 16

// PointKey 是幂等键 (probe_id, batch_seq, metric_id)，压整批重放与部分补全。
type PointKey struct {
	ProbeID  string
	BatchSeq uint64
	MetricID string
}

// AckKey 是「同一条确认」的身份：(probe_id, metric_id, acked_batch_seq)。
//
// 它与幂等键分工不同：幂等键到批次，这条到确认本身。探针会把上层还没确认的点
// 在后续批次里再报一遍，信封的 batch_seq 变了，幂等键压不住——12-task 的落盘样本里
// 每一条确认都会在相邻几批里重复出现，正是这种情况。
type AckKey struct {
	ProbeID       string
	MetricID      string
	AckedBatchSeq uint64
}

// BatchKey 是批次身份，比幂等键少一段。
type BatchKey struct {
	ProbeID  string
	BatchSeq uint64
}

// 批次去向。
const (
	outcomeNew      = "新批次"
	outcomeReplay   = "整批重放"
	outcomeComplete = "部分补全"
	outcomeConflict = "冲突"
)

// BatchNote 是一个批次的处理结论，落 out/dedup.jsonl 供对账。
type BatchNote struct {
	Line       int    `json:"line"`
	ProbeID    string `json:"probe_id"`
	BatchSeq   uint64 `json:"batch_seq"`
	Outcome    string `json:"outcome"`
	Accepted   int    `json:"accepted"`
	Duplicated int    `json:"duplicated"`
	Repeated   int    `json:"repeated"`
	Conflicted int    `json:"conflicted"`
	Regressed  bool   `json:"regressed"`
	Detail     string `json:"detail,omitempty"`
}

// DedupStats 是去重阶段的账。
type DedupStats struct {
	BatchesIn        int
	PointsIn         int
	BatchesAccepted  int
	PointsAccepted   int
	PointsDuplicated int
	PointsRepeated   int // 跨批次重复上报的同一条确认，被确认键压掉
	PointsConflicted int
	ReplayBatches    int
	CompleteBatches  int
	ConflictBatches  int
	RegressedBatches int
	RestartConflicts int // 批次号回退导致的冲突：探针重启后从头数，撞上老号
	Notes            []BatchNote
}

// DedupResult 是去重后的数据流，顺序仍是到达顺序，只留下能进聚合的点。
type DedupResult struct {
	Stats   DedupStats
	Records []Record
}

// batchState 记住某个 (probe_id, batch_seq) 上次写进了哪些 metric、值是多少。
type batchState struct {
	values map[string]float64
}

// Deduper 是有状态的去重器。一个进程一个实例，状态要留到进程退出——
// 状态丢了，重放就会翻倍。
type Deduper struct {
	seen    map[PointKey]struct{}
	acks    map[AckKey]struct{}
	batches map[BatchKey]*batchState
	maxSeq  map[string]uint64
}

func NewDeduper() *Deduper {
	return &Deduper{
		seen:    make(map[PointKey]struct{}, 4096),
		acks:    make(map[AckKey]struct{}, 4096),
		batches: make(map[BatchKey]*batchState, 512),
		maxSeq:  make(map[string]uint64, 32),
	}
}

// Accept 处理一个批次，返回只含新增点位的 Record（可能为空）。
func (d *Deduper) Accept(rec Record) (Record, BatchNote) {
	note := BatchNote{Line: rec.Line, ProbeID: rec.ProbeID, BatchSeq: rec.BatchSeq}
	key := BatchKey{ProbeID: rec.ProbeID, BatchSeq: rec.BatchSeq}
	prev, seenBatch := d.batches[key]

	// 批次号回退：seq 比这个探针已接受的最大值小一大截。探针重启后批次号从 1 重数，
	// 数据本身可能是好的，但那是个「这批数据的序号已经不可信」的信号，得留下。
	note.Regressed = rec.BatchSeq+seqReorderAllowance < d.maxSeq[rec.ProbeID]

	if seenBatch {
		matched, missing, conflict := compareWithPrev(prev, rec)
		switch {
		case conflict != "":
			note.Outcome = outcomeConflict
			note.Conflicted = len(rec.Points)
			note.Detail = conflict
			return Record{}, note
		case missing == 0:
			note.Outcome = outcomeReplay
			note.Duplicated = matched
			return Record{}, note
		case matched == len(prev.values):
			note.Outcome = outcomeComplete
			note.Duplicated = matched
			fresh := d.takeNew(prev, rec.Points)
			kept, dupKey, dupAck := d.admit(rec, fresh)
			note.Accepted = len(kept)
			note.Duplicated += dupKey
			note.Repeated = dupAck
			d.learn(key, prev, rec.Points)
			d.bumpSeq(rec.ProbeID, rec.BatchSeq)
			return Record{Line: rec.Line, ProbeID: rec.ProbeID, BatchSeq: rec.BatchSeq,
				ProducedAt: rec.ProducedAt, Points: kept}, note
		default:
			note.Outcome = outcomeConflict
			note.Conflicted = len(rec.Points)
			note.Detail = fmt.Sprintf("同批次点位集合变了：上次 %d 个 metric，这次对上 %d 个又多了 %d 个",
				len(prev.values), matched, missing)
			return Record{}, note
		}
	}

	kept, dupKey, dupAck := d.admit(rec, rec.Points)
	note.Outcome = outcomeNew
	note.Accepted = len(kept)
	note.Duplicated = dupKey
	note.Repeated = dupAck
	switch {
	case dupKey > 0:
		// 不同 seq 却撞了幂等键，只有一种来源：样本或代码把 seq 写串了。
		note.Detail = fmt.Sprintf("%d 个点位的幂等键已存在", dupKey)
	case dupAck > 0:
		note.Detail = fmt.Sprintf("%d 个点是跨批次重复上报的确认", dupAck)
	}
	state := &batchState{values: make(map[string]float64, len(rec.Points))}
	d.learn(key, state, rec.Points)
	d.bumpSeq(rec.ProbeID, rec.BatchSeq)
	return Record{Line: rec.Line, ProbeID: rec.ProbeID, BatchSeq: rec.BatchSeq,
		ProducedAt: rec.ProducedAt, Points: kept}, note
}

// compareWithPrev 对比同一批次号的两次内容，返回对上的点数、新出现的点数、冲突原因。
func compareWithPrev(prev *batchState, rec Record) (matched, missing int, conflict string) {
	for _, p := range rec.Points {
		if v, ok := prev.values[p.MetricID]; ok {
			if v != p.Value {
				return matched, missing, fmt.Sprintf("metric %s 两次值不同：%v vs %v", p.MetricID, v, p.Value)
			}
			matched++
			continue
		}
		missing++
	}
	return matched, missing, ""
}

// admit 逐个点位过两道闸。
//
// 第一道按幂等键 (probe_id, batch_seq, metric_id) 压整批重放；第二道按确认键
// (probe_id, metric_id, acked_batch_seq) 压跨批次的周期重复上报。两道都记数，
// 谁挡下的要分得清——修法不一样：前者是重发，后者是探针重报。
func (d *Deduper) admit(rec Record, points []Datum) (kept []Datum, dupKey, dupAck int) {
	kept = make([]Datum, 0, len(points))
	for _, p := range points {
		k := PointKey{ProbeID: p.ProbeID, BatchSeq: rec.BatchSeq, MetricID: p.MetricID}
		if _, ok := d.seen[k]; ok {
			dupKey++
			continue
		}
		d.seen[k] = struct{}{}
		ak := AckKey{ProbeID: p.ProbeID, MetricID: p.MetricID, AckedBatchSeq: p.AckedBatchSeq}
		if _, ok := d.acks[ak]; ok {
			dupAck++
			continue
		}
		d.acks[ak] = struct{}{}
		kept = append(kept, p)
	}
	return kept, dupKey, dupAck
}

// takeNew 在补全场景下只收上次没写进去的 metric。
func (d *Deduper) takeNew(prev *batchState, points []Datum) []Datum {
	out := make([]Datum, 0, len(points))
	for _, p := range points {
		if _, ok := prev.values[p.MetricID]; ok {
			continue
		}
		out = append(out, p)
	}
	return out
}

func (d *Deduper) learn(key BatchKey, state *batchState, points []Datum) {
	for _, p := range points {
		state.values[p.MetricID] = p.Value
	}
	d.batches[key] = state
}

func (d *Deduper) bumpSeq(probeID string, seq uint64) {
	if seq > d.maxSeq[probeID] {
		d.maxSeq[probeID] = seq
	}
}

// dedupAll 按到达顺序跑一遍去重。
func dedupAll(recs []Record) DedupResult {
	d := NewDeduper()
	res := DedupResult{Records: make([]Record, 0, len(recs))}
	res.Stats.BatchesIn = len(recs)
	for _, rec := range recs {
		res.Stats.PointsIn += len(rec.Points)
		kept, note := d.Accept(rec)
		res.Stats.PointsAccepted += note.Accepted
		res.Stats.PointsDuplicated += note.Duplicated
		res.Stats.PointsRepeated += note.Repeated
		res.Stats.PointsConflicted += note.Conflicted
		switch note.Outcome {
		case outcomeNew:
			res.Stats.BatchesAccepted++
		case outcomeReplay:
			res.Stats.ReplayBatches++
		case outcomeComplete:
			res.Stats.CompleteBatches++
			res.Stats.BatchesAccepted++
		case outcomeConflict:
			res.Stats.ConflictBatches++
		}
		if note.Regressed {
			res.Stats.RegressedBatches++
			if note.Outcome == outcomeConflict {
				res.Stats.RestartConflicts++
			}
		}
		res.Stats.Notes = append(res.Stats.Notes, note)
		if len(kept.Points) > 0 {
			res.Records = append(res.Records, kept)
		}
	}
	return res
}
