package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
)

// 数据契约（与 labs/12-task 共用，字段名一字不改）：
//
//	上报批次：probe_id, batch_seq(uint64), produced_at(RFC3339), points[]
//	点位：     probe_id, metric_id, acked_batch_seq, acked_ts
//	幂等键：   (probe_id, batch_seq, metric_id)
//	最大补报延迟：15 分钟（可配）
//
// 两个 batch_seq 不是一回事，12-task 那边踩过一次：
//   - 信封上的 batch_seq 是本批序号，进幂等键，压整批重放；
//   - 点位上的 acked_batch_seq 是「这条确认第一次被上报时所在批次的序号」，
//     后续批次重复上报同一条确认时它不变，所以它配上 (probe_id, metric_id) 能压周期重复。
//
// 契约没写、但代码里有的三样：点位里可选的 value（12-task 的上报没有值，缺省按 1 计）、
// 批次的 points 数组、以及「到达顺序」——由样本行序隐含表达，字段里不造 received_at。
//
// 幂等键不带时间戳，有一个直接后果：**一个批次里同一个 metric_id 只能有一个点**。
// 想把一分钟的 4 个采样塞进一个批次，键就撞了，第二批数据会被当成重复吃掉。

// skewTolerance 是允许点位的 acked_ts 超前 produced_at 的上限。
// 探针与设备的时钟本来就有偏差，几分钟是常态；超前十几分钟就不像偏差，像时钟没同步。
const skewTolerance = 5 * time.Minute

// Batch 是一份上报批次，字段名与契约一致，只用来落 JSON。
type Batch struct {
	ProbeID    string  `json:"probe_id"`
	BatchSeq   uint64  `json:"batch_seq"`
	ProducedAt string  `json:"produced_at"`
	Points     []Point `json:"points"`
}

// Point 是一个点位。字段顺序按 12-task 的落盘格式排，两边对着看不用来回数。
//
// Value 是可选的：12-task 上报的是「任务确认」，点位里没有值。缺它按 1 计，
// 一次确认就是聚合里的一个计数——这样 12-task 的 out/batches.jsonl 能直接喂进来。
type Point struct {
	ProbeID       string   `json:"probe_id"`
	MetricID      string   `json:"metric_id"`
	AckedBatchSeq uint64   `json:"acked_batch_seq"`
	AckedTS       string   `json:"acked_ts"`
	Value         *float64 `json:"value,omitempty"`
}

// Datum 是校验通过的点位，时间已解析成 time.Time。
type Datum struct {
	ProbeID       string
	AckedBatchSeq uint64
	AckedTS       time.Time
	MetricID      string
	Value         float64
}

// Record 是校验通过的一个批次。Line 是样本文件里的行号，从 1 起。
type Record struct {
	Line       int
	ProbeID    string
	BatchSeq   uint64
	ProducedAt time.Time
	Points     []Datum
}

// 校验拒绝的原因。每一种都对应样本里埋的一类脏数据，
// 全部落 out/rejected.jsonl，绝不静默丢。
const (
	reasonBadJSON        = "JSON 解析失败"
	reasonEmptyProbe     = "probe_id 为空"
	reasonSeqZero        = "batch_seq 为 0，契约里批次号从 1 起"
	reasonBadProduced    = "produced_at 不是 RFC3339"
	reasonNoPoints       = "批次里没有点位"
	reasonPointProbe     = "点位 probe_id 与批次不一致"
	reasonEmptyMetric    = "metric_id 为空"
	reasonBadAcked       = "acked_ts 不是 RFC3339"
	reasonDupMetric      = "同批次 metric_id 重复，幂等键会撞"
	reasonFutureAcked    = "acked_ts 超前 produced_at 超过 5 分钟"
	reasonNegativeAcked  = "acked_ts 早于 produced_at 超过 1 天"
	reasonDuplicateProbe = "点位里混进了别的探针，批次边界不一致"
)

// Reject 是一条被拒的批次。Detail 放原始错误，Reason 放人话。
type Reject struct {
	Line     int    `json:"line"`
	ProbeID  string `json:"probe_id"`
	BatchSeq uint64 `json:"batch_seq"`
	Points   int    `json:"points"`
	Reason   string `json:"reason"`
	Detail   string `json:"detail,omitempty"`
}

// LoadStats 是样本加载的计数，用于核对「没有一段数据凭空消失」。
type LoadStats struct {
	Lines         int
	ParsedBatches int
	ParsedPoints  int // 只要能 unmarshal 成功就计入，后面被拒的也算
	Records       int
	Rejected      int
	RejectedPoint int
}

// loadBatches 按文件行序读样本。行序就是到达顺序（arrival order），
// 这是本实验唯一的「到达时间」定义：不在契约里加 received_at，样本里也不凭空造一个。
func loadBatches(path string) ([]Record, []Reject, LoadStats, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, LoadStats{}, err
	}
	defer f.Close()

	var (
		recs    []Record
		rejects []Reject
		stats   LoadStats
	)
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	line := 0
	for sc.Scan() {
		line++
		raw := strings.TrimSpace(sc.Text())
		if raw == "" {
			continue
		}
		stats.Lines++

		var b Batch
		if err := json.Unmarshal([]byte(raw), &b); err != nil {
			stats.Rejected++
			rejects = append(rejects, Reject{
				Line:   line,
				Points: countPointsLoose(raw),
				Reason: reasonBadJSON,
				Detail: err.Error(),
			})
			continue
		}
		stats.ParsedBatches++
		stats.ParsedPoints += len(b.Points)

		rec, reject := validate(line, b)
		if reject != nil {
			stats.Rejected++
			stats.RejectedPoint += reject.Points
			rejects = append(rejects, *reject)
			continue
		}
		recs = append(recs, rec)
		stats.Records++
	}
	if err := sc.Err(); err != nil {
		return nil, nil, stats, fmt.Errorf("读 %s: %w", path, err)
	}
	return recs, rejects, stats, nil
}

// countPointsLoose 在 JSON 都解析不动的一行里数出点位数，好让「点数守恒」这条断言成立。
// 数不准就报 0，宁可少算也不编。
func countPointsLoose(raw string) int {
	return strings.Count(raw, `"acked_ts"`)
}

// validate 做批次级校验。返回 (Record, nil) 或 (zero, Reject)。
func validate(line int, b Batch) (Record, *Reject) {
	reject := func(reason, detail string) (Record, *Reject) {
		return Record{}, &Reject{
			Line:     line,
			ProbeID:  b.ProbeID,
			BatchSeq: b.BatchSeq,
			Points:   len(b.Points),
			Reason:   reason,
			Detail:   detail,
		}
	}

	if strings.TrimSpace(b.ProbeID) == "" {
		return reject(reasonEmptyProbe, "")
	}
	if b.BatchSeq == 0 {
		return reject(reasonSeqZero, "")
	}
	produced, err := time.Parse(time.RFC3339, b.ProducedAt)
	if err != nil {
		return reject(reasonBadProduced, b.ProducedAt)
	}
	if len(b.Points) == 0 {
		return reject(reasonNoPoints, "")
	}

	pts := make([]Datum, 0, len(b.Points))
	seenMetric := make(map[string]struct{}, len(b.Points))
	for i, p := range b.Points {
		if p.ProbeID != b.ProbeID {
			return reject(reasonPointProbe, fmt.Sprintf("第 %d 个点位 probe_id=%q", i+1, p.ProbeID))
		}
		if strings.TrimSpace(p.MetricID) == "" {
			return reject(reasonEmptyMetric, fmt.Sprintf("第 %d 个点位", i+1))
		}
		if _, dup := seenMetric[p.MetricID]; dup {
			return reject(reasonDupMetric, p.MetricID)
		}
		seenMetric[p.MetricID] = struct{}{}
		acked, err := time.Parse(time.RFC3339, p.AckedTS)
		if err != nil {
			return reject(reasonBadAcked, p.AckedTS)
		}
		if acked.Sub(produced) > skewTolerance {
			return reject(reasonFutureAcked, fmt.Sprintf("acked_ts %s，produced_at %s",
				acked.UTC().Format(time.RFC3339), produced.UTC().Format(time.RFC3339)))
		}
		if produced.Sub(acked) > 24*time.Hour {
			return reject(reasonNegativeAcked, fmt.Sprintf("acked_ts %s，produced_at %s",
				acked.UTC().Format(time.RFC3339), produced.UTC().Format(time.RFC3339)))
		}
		value := 1.0 // 12-task 的点位没有 value：一次确认算一个点
		if p.Value != nil {
			value = *p.Value
		}
		pts = append(pts, Datum{
			ProbeID:       p.ProbeID,
			AckedBatchSeq: p.AckedBatchSeq,
			AckedTS:       acked.UTC(),
			MetricID:      p.MetricID,
			Value:         value,
		})
	}
	return Record{
		Line:       line,
		ProbeID:    b.ProbeID,
		BatchSeq:   b.BatchSeq,
		ProducedAt: produced.UTC(),
		Points:     pts,
	}, nil
}

// floorWindow 把事件时间对齐到窗口起点。按 Unix 秒向下取整，与时区无关；
// 窗口必须是整秒，负数时间也不出错。
func floorWindow(ts time.Time, window time.Duration) time.Time {
	sec := int64(window / time.Second)
	if sec <= 0 {
		sec = 1
	}
	u := ts.Unix()
	if u < 0 && u%sec != 0 {
		u -= sec
	}
	return time.Unix(u/sec*sec, 0).UTC()
}

// toBatches 把内部表示转回契约结构，用于落 JSON 与编码对比。
func toBatches(recs []Record) []Batch {
	out := make([]Batch, 0, len(recs))
	for _, r := range recs {
		pts := make([]Point, 0, len(r.Points))
		for _, d := range r.Points {
			v := d.Value
			pts = append(pts, Point{
				ProbeID:       d.ProbeID,
				MetricID:      d.MetricID,
				AckedBatchSeq: d.AckedBatchSeq,
				AckedTS:       d.AckedTS.UTC().Format(time.RFC3339),
				Value:         &v,
			})
		}
		out = append(out, Batch{
			ProbeID:    r.ProbeID,
			BatchSeq:   r.BatchSeq,
			ProducedAt: r.ProducedAt.UTC().Format(time.RFC3339),
			Points:     pts,
		})
	}
	return out
}

// writeJSONLines 把任意结构按行落盘，每行一个 JSON。
func writeJSONLines[T any](path string, rows []T) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	enc.SetEscapeHTML(false)
	for _, r := range rows {
		if err := enc.Encode(r); err != nil {
			return err
		}
	}
	return f.Sync()
}

// writeJSONFile 落一个缩进过的 JSON，给人看也用。
func writeJSONFile(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(path, data, 0o644)
}
