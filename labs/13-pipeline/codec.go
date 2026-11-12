package main

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"time"
)

// 紧凑二进制格式 PB1。设计目标只有一个：在同一份数据上量出 JSON 到底贵在哪。
//
// 布局（全部小端 / varint，字段顺序固定）：
//
//	"PB" 0x01                     魔数与版本，3 字节
//	uvarint 字符串表长度 n + n 项  每项 uvarint 长度 + 字节；probe_id 与 metric_id 去重后入表
//	uvarint 批次数 m + m 个批次
//	  每批：uvarint probe_id 下标 | uvarint batch_seq | varint produced_at 毫秒
//	        uvarint 点位数 | 点位
//	  每点：uvarint probe_id 下标 | uvarint acked_batch_seq
//	        varint (acked_ts - produced_at) 毫秒 | uvarint metric_id 下标 | varint 值
//
// 三处省钱的地方：字符串表干掉了每个点重复的探针名与指标名；varint 让整数只占需要的字节；
// 点位时间戳存相对批次的毫秒差，不存绝对时间——一个批次里的点都挨着，差值只有几位数。
//
// 精度是硬的：时间戳存毫秒，值乘 1000 取整。改精度要升版本号，否则老数据读出来是错的。
const (
	pb1Version    = 1
	pb1ValueScale = 1000
)

var pb1Magic = []byte{'P', 'B', pb1Version}

// 错误定义成哨兵值，测试与上层按值判，不按字符串匹配。
var (
	errPB1Short   = errors.New("PB1 数据太短")
	errPB1Magic   = errors.New("PB1 魔数或版本不对")
	errPB1Corrupt = errors.New("PB1 数据被截断或损坏")
)

// binaryMeta 记录二进制格式里各段占多少字节，用来回答「省下来的钱从哪来」。
type binaryMeta struct {
	StringTableEntries int
	StringTableBytes   int
	BatchBytes         int
}

// strTable 是编码期的字符串表，按首次出现顺序编号，输出稳定可复现。
type strTable struct {
	idx  map[string]int
	list []string
}

func newStrTable() *strTable {
	return &strTable{idx: make(map[string]int, 16)}
}

func (t *strTable) id(s string) int {
	if i, ok := t.idx[s]; ok {
		return i
	}
	i := len(t.list)
	t.idx[s] = i
	t.list = append(t.list, s)
	return i
}

// encodeBinary 把批次编成 PB1 字节流。
func encodeBinary(recs []Record) ([]byte, binaryMeta) {
	tbl := newStrTable()
	for _, r := range recs {
		tbl.id(r.ProbeID)
		for _, d := range r.Points {
			tbl.id(d.ProbeID)
			tbl.id(d.MetricID)
		}
	}

	var buf bytes.Buffer
	buf.Write(pb1Magic)
	putUvarint(&buf, uint64(len(tbl.list)))
	for _, s := range tbl.list {
		putUvarint(&buf, uint64(len(s)))
		buf.WriteString(s)
	}
	tableBytes := buf.Len()

	putUvarint(&buf, uint64(len(recs)))
	for _, r := range recs {
		putUvarint(&buf, uint64(tbl.id(r.ProbeID)))
		putUvarint(&buf, r.BatchSeq)
		putVarint(&buf, r.ProducedAt.UnixMilli())
		putUvarint(&buf, uint64(len(r.Points)))
		for _, d := range r.Points {
			putUvarint(&buf, uint64(tbl.id(d.ProbeID)))
			putUvarint(&buf, d.AckedBatchSeq)
			putVarint(&buf, d.AckedTS.Sub(r.ProducedAt).Milliseconds())
			putUvarint(&buf, uint64(tbl.id(d.MetricID)))
			putVarint(&buf, int64(math.Round(d.Value*pb1ValueScale)))
		}
	}

	out := buf.Bytes()
	meta := binaryMeta{
		StringTableEntries: len(tbl.list),
		StringTableBytes:   tableBytes,
		BatchBytes:         len(out) - tableBytes,
	}
	return out, meta
}

// decodeBinary 解 PB1。Line 无线上表示，解出来一律置 0。
func decodeBinary(data []byte) ([]Record, error) {
	if len(data) < len(pb1Magic) {
		return nil, errPB1Short
	}
	if !bytes.Equal(data[:len(pb1Magic)], pb1Magic) {
		return nil, errPB1Magic
	}
	pos := len(pb1Magic)

	nStr, err := readUvarint(data, &pos)
	if err != nil {
		return nil, err
	}
	table := make([]string, nStr)
	for i := range table {
		l, err := readUvarint(data, &pos)
		if err != nil {
			return nil, err
		}
		if pos+int(l) > len(data) {
			return nil, errPB1Corrupt
		}
		table[i] = string(data[pos : pos+int(l)])
		pos += int(l)
	}

	nBatch, err := readUvarint(data, &pos)
	if err != nil {
		return nil, err
	}
	recs := make([]Record, 0, nBatch)
	for b := uint64(0); b < nBatch; b++ {
		probeIdx, err := readUvarint(data, &pos)
		if err != nil {
			return nil, err
		}
		seq, err := readUvarint(data, &pos)
		if err != nil {
			return nil, err
		}
		producedMs, err := readVarint(data, &pos)
		if err != nil {
			return nil, err
		}
		nPoint, err := readUvarint(data, &pos)
		if err != nil {
			return nil, err
		}
		if probeIdx >= uint64(len(table)) {
			return nil, errPB1Corrupt
		}
		produced := time.UnixMilli(producedMs).UTC()
		rec := Record{
			ProbeID:    table[probeIdx],
			BatchSeq:   seq,
			ProducedAt: produced,
			Points:     make([]Datum, 0, nPoint),
		}
		for p := uint64(0); p < nPoint; p++ {
			pIdx, err := readUvarint(data, &pos)
			if err != nil {
				return nil, err
			}
			ackedSeq, err := readUvarint(data, &pos)
			if err != nil {
				return nil, err
			}
			deltaMs, err := readVarint(data, &pos)
			if err != nil {
				return nil, err
			}
			mIdx, err := readUvarint(data, &pos)
			if err != nil {
				return nil, err
			}
			v, err := readVarint(data, &pos)
			if err != nil {
				return nil, err
			}
			if pIdx >= uint64(len(table)) || mIdx >= uint64(len(table)) {
				return nil, errPB1Corrupt
			}
			rec.Points = append(rec.Points, Datum{
				ProbeID:       table[pIdx],
				AckedBatchSeq: ackedSeq,
				AckedTS:       produced.Add(time.Duration(deltaMs) * time.Millisecond),
				MetricID:      table[mIdx],
				Value:         float64(v) / pb1ValueScale,
			})
		}
		recs = append(recs, rec)
	}
	return recs, nil
}

func putUvarint(buf *bytes.Buffer, v uint64) {
	var tmp [binary.MaxVarintLen64]byte
	n := binary.PutUvarint(tmp[:], v)
	buf.Write(tmp[:n])
}

func putVarint(buf *bytes.Buffer, v int64) {
	var tmp [binary.MaxVarintLen64]byte
	n := binary.PutVarint(tmp[:], v)
	buf.Write(tmp[:n])
}

func readUvarint(data []byte, pos *int) (uint64, error) {
	v, n := binary.Uvarint(data[*pos:])
	if n <= 0 {
		return 0, errPB1Corrupt
	}
	*pos += n
	return v, nil
}

func readVarint(data []byte, pos *int) (int64, error) {
	v, n := binary.Varint(data[*pos:])
	if n <= 0 {
		return 0, errPB1Corrupt
	}
	*pos += n
	return v, nil
}

func countPoints(recs []Record) int {
	n := 0
	for _, r := range recs {
		n += len(r.Points)
	}
	return n
}

// --- 编码开销对比 ---

// CodecResult 是同一份数据两种编码的账。耗时取 7 次的中位数，机器不同绝对值不同，比值稳定。
type CodecResult struct {
	Batches int
	Points  int

	JSONBytes   int
	BinaryBytes int

	JSONKeyBytes     int // JSON 里键名与标点占多少
	JSONRepeatBytes  int // probe_id / metric_id 字符串重复了多少
	StringTableBytes int
	StringTableItems int

	JSONEncode   time.Duration
	JSONDecode   time.Duration
	BinaryEncode time.Duration
	BinaryDecode time.Duration
}

// 点位与批次在 JSON 里固定要写的键名与标点（值留空）。每点这笔是白送的。
const (
	jsonPointSkeleton = `{"probe_id":"","metric_id":"","acked_batch_seq":,"acked_ts":"","value":}`
	jsonBatchSkeleton = `{"probe_id":"","batch_seq":,"produced_at":"","points":[]}`
)

func (r CodecResult) JSONPerPoint() float64 {
	return div(float64(r.JSONBytes), float64(r.Points))
}

func (r CodecResult) BinaryPerPoint() float64 {
	return div(float64(r.BinaryBytes), float64(r.Points))
}

// Ratio 是 JSON 字节数 ÷ 二进制字节数。1.0 表示二进制一点没省。
func (r CodecResult) Ratio() float64 {
	return div(float64(r.JSONBytes), float64(r.BinaryBytes))
}

func (r CodecResult) JSONKeyPct() float64 {
	return div(float64(r.JSONKeyBytes)*100, float64(r.JSONBytes))
}

func (r CodecResult) JSONRepeatPct() float64 {
	return div(float64(r.JSONRepeatBytes)*100, float64(r.JSONBytes))
}

// compareCodecs 在同一份数据上跑两种编码，顺带把二进制解码结果与原始数据对一遍。
func compareCodecs(recs []Record) (CodecResult, error) {
	res := CodecResult{Batches: len(recs), Points: countPoints(recs)}
	if len(recs) == 0 {
		return res, errors.New("样本里没有可用批次")
	}

	batches := toBatches(recs)
	var (
		jsonBytes   []byte
		binaryBytes []byte
		meta        binaryMeta
		err         error
	)
	res.JSONEncode = medianDuration(7, func() {
		jsonBytes, err = json.Marshal(batches)
	})
	if err != nil {
		return res, fmt.Errorf("JSON 编码: %w", err)
	}
	res.BinaryEncode = medianDuration(7, func() {
		binaryBytes, meta = encodeBinary(recs)
	})
	res.JSONBytes = len(jsonBytes)
	res.BinaryBytes = len(binaryBytes)
	res.StringTableBytes = meta.StringTableBytes
	res.StringTableItems = meta.StringTableEntries

	// 二进制往返：解出来的必须与原文一致，否则「省了字节」是把数据省没了。
	back, err := decodeBinary(binaryBytes)
	if err != nil {
		return res, fmt.Errorf("PB1 解码: %w", err)
	}
	if err := compareRecords(recs, back); err != nil {
		return res, fmt.Errorf("PB1 往返不一致: %w", err)
	}

	res.JSONDecode = medianDuration(7, func() {
		_, err = decodeJSONRecords(jsonBytes)
	})
	if err != nil {
		return res, fmt.Errorf("JSON 解码: %w", err)
	}
	res.BinaryDecode = medianDuration(7, func() {
		_, err = decodeBinary(binaryBytes)
	})
	if err != nil {
		return res, fmt.Errorf("PB1 解码: %w", err)
	}

	res.JSONKeyBytes = len(recs)*len(jsonBatchSkeleton) + res.Points*len(jsonPointSkeleton)
	res.JSONRepeatBytes = repeatStringBytes(recs)
	return res, nil
}

// repeatStringBytes 数出重复的字符串值：每个点位都会再写一遍 probe_id 与 metric_id，
// 去重后剩下的部分就是字符串表省掉的那笔。
func repeatStringBytes(recs []Record) int {
	total := 0
	probes := map[string]struct{}{}
	metrics := map[string]struct{}{}
	for _, r := range recs {
		probes[r.ProbeID] = struct{}{}
		for _, d := range r.Points {
			total += len(d.ProbeID) + len(d.MetricID)
			probes[d.ProbeID] = struct{}{}
			metrics[d.MetricID] = struct{}{}
		}
	}
	unique := 0
	for s := range probes {
		unique += len(s)
	}
	for s := range metrics {
		unique += len(s)
	}
	return total - unique
}

// decodeJSONRecords 是 JSON 侧的完整解码：unmarshal 加上时间解析。
// 时间解析要算进去，因为二进制那边的时间戳本来就是个整数，不需要解析。
func decodeJSONRecords(data []byte) ([]Record, error) {
	var batches []Batch
	if err := json.Unmarshal(data, &batches); err != nil {
		return nil, err
	}
	out := make([]Record, 0, len(batches))
	for _, b := range batches {
		produced, err := time.Parse(time.RFC3339, b.ProducedAt)
		if err != nil {
			return nil, err
		}
		rec := Record{ProbeID: b.ProbeID, BatchSeq: b.BatchSeq, ProducedAt: produced.UTC()}
		for _, p := range b.Points {
			acked, err := time.Parse(time.RFC3339, p.AckedTS)
			if err != nil {
				return nil, err
			}
			value := 1.0
			if p.Value != nil {
				value = *p.Value
			}
			rec.Points = append(rec.Points, Datum{
				ProbeID:       p.ProbeID,
				AckedBatchSeq: p.AckedBatchSeq,
				AckedTS:       acked.UTC(),
				MetricID:      p.MetricID,
				Value:         value,
			})
		}
		out = append(out, rec)
	}
	return out, nil
}

// compareRecords 比两批 Record 是否等价，值按 PB1 的精度（0.001）容差。
func compareRecords(want, got []Record) error {
	if len(want) != len(got) {
		return fmt.Errorf("批次数 %d ≠ %d", len(want), len(got))
	}
	for i := range want {
		w, g := want[i], got[i]
		if w.ProbeID != g.ProbeID || w.BatchSeq != g.BatchSeq {
			return fmt.Errorf("第 %d 批身份不一致：%s/%d ≠ %s/%d", i, w.ProbeID, w.BatchSeq, g.ProbeID, g.BatchSeq)
		}
		if !w.ProducedAt.Equal(g.ProducedAt) {
			return fmt.Errorf("第 %d 批 produced_at 不一致：%s ≠ %s", i, w.ProducedAt, g.ProducedAt)
		}
		if len(w.Points) != len(g.Points) {
			return fmt.Errorf("第 %d 批点位数 %d ≠ %d", i, len(w.Points), len(g.Points))
		}
		for j := range w.Points {
			wp, gp := w.Points[j], g.Points[j]
			if wp.ProbeID != gp.ProbeID || wp.MetricID != gp.MetricID || wp.AckedBatchSeq != gp.AckedBatchSeq {
				return fmt.Errorf("第 %d 批第 %d 点身份不一致", i, j)
			}
			if !wp.AckedTS.Equal(gp.AckedTS) {
				return fmt.Errorf("第 %d 批第 %d 点 acked_ts 不一致：%s ≠ %s", i, j, wp.AckedTS, gp.AckedTS)
			}
			if math.Abs(wp.Value-gp.Value) > 1.0/(2*pb1ValueScale) {
				return fmt.Errorf("第 %d 批第 %d 点值不一致：%v ≠ %v", i, j, wp.Value, gp.Value)
			}
		}
	}
	return nil
}

// medianDuration 跑 iter 次取中位数。单次测量在这个量级上噪声太大。
func medianDuration(iter int, f func()) time.Duration {
	ds := make([]time.Duration, 0, iter)
	for i := 0; i < iter; i++ {
		start := time.Now()
		f()
		ds = append(ds, time.Since(start))
	}
	sort.Slice(ds, func(i, j int) bool { return ds[i] < ds[j] })
	return ds[len(ds)/2]
}

func div(a, b float64) float64 {
	if b == 0 {
		return 0
	}
	return a / b
}
