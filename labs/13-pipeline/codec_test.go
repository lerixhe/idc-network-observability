package main

import (
	"bytes"
	"encoding/json"
	"math"
	"testing"
)

func mustMarshal(t *testing.T, v any) []byte {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return data
}

func TestBinaryRoundTrip(t *testing.T) {
	recs, _, _ := mustLoadSample(t)
	encoded, meta := encodeBinary(recs)
	back, err := decodeBinary(encoded)
	if err != nil {
		t.Fatalf("解码: %v", err)
	}
	if err := compareRecords(recs, back); err != nil {
		t.Fatalf("往返不一致: %v", err)
	}
	if meta.StringTableEntries == 0 {
		t.Fatal("字符串表应该是空的？probe_id 与 metric_id 都进表了")
	}
	if meta.StringTableBytes <= 0 || meta.BatchBytes <= 0 {
		t.Fatalf("分段字节数没统计出来：%+v", meta)
	}
	if meta.StringTableBytes+meta.BatchBytes != len(encoded) {
		t.Fatalf("分段之和 %d ≠ 总长 %d", meta.StringTableBytes+meta.BatchBytes, len(encoded))
	}
}

func TestBinaryEncodeIsDeterministic(t *testing.T) {
	recs, _, _ := mustLoadSample(t)
	a, _ := encodeBinary(recs)
	b, _ := encodeBinary(recs)
	if !bytes.Equal(a, b) {
		t.Fatal("同一份数据两次编码结果不同，回放对账就没法做了")
	}
}

func TestBinaryIsMuchSmallerThanJSON(t *testing.T) {
	recs, _, _ := mustLoadSample(t)
	res, err := compareCodecs(recs)
	if err != nil {
		t.Fatalf("编码对比: %v", err)
	}
	if res.BinaryBytes >= res.JSONBytes {
		t.Fatalf("PB1 %d 字节不小于 JSON %d 字节", res.BinaryBytes, res.JSONBytes)
	}
	// 实测在 10 倍以上；门限定在 5 倍，留足不同数据形状的余地。
	if res.Ratio() < 5 {
		t.Fatalf("JSON/PB1 = %.2f，比预期小", res.Ratio())
	}
	if res.JSONKeyBytes <= 0 || res.JSONKeyBytes >= res.JSONBytes {
		t.Fatalf("JSON 键名开销统计不合理：%d / %d", res.JSONKeyBytes, res.JSONBytes)
	}
	if res.StringTableBytes <= 0 {
		t.Fatal("字符串表字节数没统计出来")
	}
}

func TestBinaryPrecisionIsDocumented(t *testing.T) {
	// 时间存毫秒、值存千分之一。超出精度的部分会被吃掉，这是格式的一部分，不是 bug。
	produced := dateTime(2025, 3, 11, 9, 0, 0, 0)
	recs := []Record{{
		ProbeID: "p1", BatchSeq: 1, ProducedAt: produced,
		Points: []Datum{
			{ProbeID: "p1", AckedBatchSeq: 1, AckedTS: produced.Add(-1500 * 1e6), MetricID: "a", Value: 1.23456},
			{ProbeID: "p1", AckedBatchSeq: 1, AckedTS: produced, MetricID: "b", Value: -7.5},
		},
	}}
	encoded, _ := encodeBinary(recs)
	back, err := decodeBinary(encoded)
	if err != nil {
		t.Fatalf("解码: %v", err)
	}
	got := back[0].Points[0]
	if math.Abs(got.Value-1.235) > 1e-9 {
		t.Fatalf("值 1.23456 应四舍五入到 1.235，实际 %v", got.Value)
	}
	if !got.AckedTS.Equal(produced.Add(-1500 * 1e6)) {
		t.Fatalf("毫秒级时间戳往返丢了精度：%s", got.AckedTS)
	}
	if back[0].Points[1].Value != -7.5 {
		t.Fatalf("负值往返错了：%v", back[0].Points[1].Value)
	}
}

func TestDecodeBinaryRejectsGarbage(t *testing.T) {
	if _, err := decodeBinary([]byte("PB")); err == nil {
		t.Fatal("短数据应报错")
	}
	if _, err := decodeBinary([]byte("XX\x01abc")); err == nil {
		t.Fatal("魔数不对应报错")
	}
	recs, _, _ := mustLoadSample(t)
	encoded, _ := encodeBinary(recs[:3])
	if _, err := decodeBinary(encoded[:len(encoded)-3]); err == nil {
		t.Fatal("截断的数据应报错")
	}
}

func TestAggsDigestIgnoresOrder(t *testing.T) {
	recs, _, _ := mustLoadSample(t)
	d := dedupAll(recs)
	win := runWindow(d.Records, dateDur(60), dateDur(900), dateDur(900))
	shuffled := make([]Agg, len(win.Aggs))
	copy(shuffled, win.Aggs)
	for i, j := 0, len(shuffled)-1; i < j; i, j = i+1, j-1 {
		shuffled[i], shuffled[j] = shuffled[j], shuffled[i]
	}
	a := aggsDigest(win.Aggs)
	b := aggsDigest(shuffled)
	if a != b {
		t.Fatal("同样一组聚合换个顺序 digest 就变了，回放对账会误报")
	}
	shuffled[0].Sum += 1
	if aggsDigest(shuffled) == a {
		t.Fatal("改了一个值 digest 居然没变")
	}
}
