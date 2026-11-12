package main

import (
	"math"
	"path/filepath"
	"testing"
)

func sflowDatagram(t *testing.T, ts string, seq int, samples ...SFlowSample) SFlowDatagram {
	t.Helper()
	ms, err := parseTS("test", ts)
	if err != nil {
		t.Fatal(err)
	}
	return SFlowDatagram{TS: ts, Agent: "10.0.0.11", Seq: seq, Samples: samples, tsMs: ms}
}

func sampleOf(seq, rate int, h Header) SFlowSample {
	return SFlowSample{Seq: seq, IfIndex: 48, SamplingRate: rate, Header: h}
}

func TestAggregateSFlowScalesBySamplingRate(t *testing.T) {
	h := Header{SrcIP: "10.0.0.1", DstIP: "10.0.0.2", Proto: "tcp", SrcPort: 51000, DstPort: 445, Bytes: 1514}
	dgs := []SFlowDatagram{
		sflowDatagram(t, "2026-05-12T10:00:00Z", 1, sampleOf(1, 1000, h), sampleOf(2, 2000, h)),
	}
	st := AggregateSFlow(dgs, 5)
	if len(st.Entries) != 1 {
		t.Fatalf("应当只有 1 条流，实际 %d", len(st.Entries))
	}
	e := st.Entries[0]
	// 1514×1000 + 1514×2000
	if want := int64(1514 * 3000); e.Bytes != want {
		t.Errorf("估算字节 = %d，期望 %d", e.Bytes, want)
	}
	if e.Packets != 3000 {
		t.Errorf("估算包数 = %d，期望 3000", e.Packets)
	}
	if e.Samples != 2 || math.Abs(e.RelErr-1/math.Sqrt2) > 1e-9 {
		t.Errorf("样本数/误差 = %d/%v", e.Samples, e.RelErr)
	}
	if e.Share != 1 {
		t.Errorf("只有一条流时 share 应当是 1，实际 %v", e.Share)
	}
}

func TestAggregateSFlowDropsUnscalableSamples(t *testing.T) {
	good := Header{SrcIP: "10.0.0.1", DstIP: "10.0.0.2", Proto: "tcp", Bytes: 1514}
	zeroRate := good
	zeroBytes := good
	zeroBytes.SrcIP = "10.0.0.3"
	zeroBytes.Bytes = 0
	dgs := []SFlowDatagram{
		sflowDatagram(t, "2026-05-12T10:00:00Z", 1,
			sampleOf(1, 1000, good),
			sampleOf(2, 0, zeroRate),
			sampleOf(3, 1000, zeroBytes),
		),
	}
	st := AggregateSFlow(dgs, 5)
	if st.Units != 1 || st.DroppedUnits != 2 {
		t.Fatalf("可信/不可信 = %d/%d，期望 1/2", st.Units, st.DroppedUnits)
	}
	if st.DropReasons[reasonRateMissing] != 1 || st.DropReasons[reasonBytesMissing] != 1 {
		t.Errorf("丢弃原因统计 = %+v", st.DropReasons)
	}
	if got := st.Coverage; math.Abs(got-1.0/3) > 1e-9 {
		t.Errorf("覆盖率 = %v，期望 1/3", got)
	}
	if len(st.Entries) != 1 {
		t.Errorf("只该留下 1 条流，实际 %d", len(st.Entries))
	}
}

func TestAggregateNetFlowSkipsUntrustedSegments(t *testing.T) {
	exports := []NetFlowExport{
		exportOf(t, "2026-05-12T10:00:00Z", "exp-a", 1,
			tmplSet(256, fiveFields...),
			dataSet(256, []any{"10.0.0.1", "10.0.0.2", "tcp", 8421, 7})),
		exportOf(t, "2026-05-12T10:01:00Z", "exp-a", 2,
			dataSet(400, []any{"10.0.0.1", "10.0.0.2", "tcp", 999999, 999})),
	}
	res := DecodeExports(exports, defaultTemplateTTL)
	st := AggregateNetFlow(res, 5)
	if st.Bytes != 8421 || st.Packets != 7 {
		t.Fatalf("合计 = %d 字节 / %d 包，模板缺失那段不该被算进去", st.Bytes, st.Packets)
	}
	if st.Units != 1 || st.DroppedUnits != 1 || st.UntrustedSegments != 1 {
		t.Errorf("单元账 = %d/%d，不可信段 %d", st.Units, st.DroppedUnits, st.UntrustedSegments)
	}
	if st.Coverage != 0.5 {
		t.Errorf("覆盖率 = %v，期望 0.5", st.Coverage)
	}
	if st.DropReasons[reasonTemplateMissing] != 1 {
		t.Errorf("丢弃原因 = %+v", st.DropReasons)
	}
}

func TestTopNTruncatesButTotalsStayComplete(t *testing.T) {
	exports := []NetFlowExport{exportOf(t, "2026-05-12T10:00:00Z", "exp-a", 1,
		tmplSet(256, fiveFields...),
		dataSet(256,
			[]any{"10.0.0.1", "10.0.0.9", "tcp", 500, 5},
			[]any{"10.0.0.2", "10.0.0.9", "tcp", 400, 4},
			[]any{"10.0.0.3", "10.0.0.9", "tcp", 300, 3},
		))}
	st := AggregateNetFlow(DecodeExports(exports, defaultTemplateTTL), 2)
	if len(st.Entries) != 2 {
		t.Fatalf("TopN=2 时应当只留 2 条，实际 %d", len(st.Entries))
	}
	if st.Bytes != 1200 {
		t.Errorf("合计字节 = %d，截断不能影响合计", st.Bytes)
	}
	if st.Entries[0].Bytes != 500 || st.Entries[1].Bytes != 400 {
		t.Errorf("排序不对: %+v", st.Entries)
	}
}

func TestTopNTieBreakIsStable(t *testing.T) {
	// 字节数相同时按 key 排序。平局不排序，TopN 的尾巴每次跑都会跳。
	entries := []TopEntry{
		{Key: makeKey("10.0.0.9", "10.0.0.1", "tcp", 0, 0), Bytes: 100},
		{Key: makeKey("10.0.0.1", "10.0.0.1", "tcp", 0, 0), Bytes: 100},
	}
	sortEntries(entries)
	if entries[0].Key.SrcIP != "10.0.0.1" {
		t.Fatalf("平局时应当按 key 排，实际第一条是 %s", entries[0].Key.SrcIP)
	}
}

func TestMergeStatsDoublesTheSameFlow(t *testing.T) {
	sflow := SourceStat{
		Name: "sflow",
		Entries: []TopEntry{
			{Key: makeKey("10.0.0.1", "10.0.0.2", "tcp", 0, 0), Bytes: 1000},
		},
		Bytes: 1000,
	}
	netflow := SourceStat{
		Name: "netflow",
		Entries: []TopEntry{
			{Key: makeKey("10.0.0.1", "10.0.0.2", "tcp", 0, 0), Bytes: 4000},
		},
		Bytes: 4000,
	}
	merged := MergeStats(sflow, netflow, 5)
	if merged.Bytes != 5000 {
		t.Fatalf("合并字节 = %d，期望 5000", merged.Bytes)
	}
	if len(merged.Entries) != 1 || merged.Entries[0].Bytes != 5000 {
		t.Fatalf("同一个五元组在合并表里成了多行: %+v", merged.Entries)
	}
}

func TestAggregateSampleFiles(t *testing.T) {
	dgs, err := loadSFlow(filepath.Join("samples", "sflow.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	exports, err := loadNetFlow(filepath.Join("samples", "netflow.jsonl"))
	if err != nil {
		t.Fatal(err)
	}

	sflow := AggregateSFlow(dgs, 5)
	if sflow.Units != 16 || sflow.DroppedUnits != 2 {
		t.Fatalf("sFlow 可信/不可信 = %d/%d，期望 16/2", sflow.Units, sflow.DroppedUnits)
	}
	if sflow.Entries[0].Samples != 8 {
		t.Errorf("大象流样本数 = %d，期望 8", sflow.Entries[0].Samples)
	}
	if want := int64(8 * 1514 * 1000); sflow.Entries[0].Bytes != want {
		t.Errorf("大象流估算字节 = %d，期望 %d", sflow.Entries[0].Bytes, want)
	}

	res := DecodeExports(exports, defaultTemplateTTL)
	netflow := AggregateNetFlow(res, 5)
	if netflow.Units != 12 || netflow.DroppedUnits != 5 {
		t.Fatalf("NetFlow 可信/不可信 = %d/%d，期望 12/5", netflow.Units, netflow.DroppedUnits)
	}
	if netflow.Entries[0].Bytes != 238_000_000 {
		t.Errorf("Top1 字节 = %d，期望 238000000", netflow.Entries[0].Bytes)
	}
	// 模板重定义之后，10.20.2.30 那三条可信记录必须按新字段顺序解出来：
	// 56200000 + 58800000 + 62100000。
	for _, e := range netflow.Entries {
		if e.Key.SrcIP == "10.20.2.30" {
			if e.Bytes != 177_100_000 {
				t.Errorf("重定义后累计字节 = %d，期望 177100000", e.Bytes)
			}
			return
		}
	}
	t.Fatal("TopN 里没有 10.20.2.30")
}

func TestCoverageAndRelErr(t *testing.T) {
	if got := coverage(0, 0); got != 1 {
		t.Errorf("没有数据时覆盖率 = %v，期望 1", got)
	}
	if got := coverage(3, 1); got != 0.75 {
		t.Errorf("覆盖率 = %v，期望 0.75", got)
	}
	if got := relErr(4); math.Abs(got-0.5) > 1e-9 {
		t.Errorf("4 个样本的相对误差 = %v，期望 0.5", got)
	}
	if got := relErr(0); got != 0 {
		t.Errorf("没有样本时 relErr = %v，期望 0（不能是 NaN）", got)
	}
}
