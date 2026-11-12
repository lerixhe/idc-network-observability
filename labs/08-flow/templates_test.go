package main

import (
	"strings"
	"testing"
	"time"
)

// 下面三个是构造样本的小工具。用例只写自己关心的那一段，
// 其余字段用默认值，免得每个用例都堆一坨结构体字面量。

func tmplSet(id int, fields ...string) ExportSet {
	return ExportSet{Template: &TemplateSpec{ID: id, Fields: fields}}
}

func dataSet(id int, records ...[]any) ExportSet {
	return ExportSet{Data: &DataSet{TemplateID: id, Records: records}}
}

func exportOf(t *testing.T, ts, exporter string, seq int, sets ...ExportSet) NetFlowExport {
	t.Helper()
	ms, err := parseTS("test", ts)
	if err != nil {
		t.Fatal(err)
	}
	return NetFlowExport{TS: ts, Exporter: exporter, Seq: seq, Sets: sets, tsMs: ms}
}

var (
	fiveFields  = []string{fieldSrcIP, fieldDstIP, fieldProto, fieldOctets, fieldPackets}
	swappedFour = []string{fieldSrcIP, fieldDstIP, fieldPackets, fieldOctets}
)

func TestDecodeTrustedSegment(t *testing.T) {
	exports := []NetFlowExport{exportOf(t, "2026-05-12T10:00:00Z", "exp-a", 1,
		tmplSet(256, fiveFields...),
		dataSet(256, []any{"10.0.0.1", "10.0.0.2", "tcp", 8421, 7}),
	)}
	res := DecodeExports(exports, defaultTemplateTTL)
	if len(res.Records) != 1 {
		t.Fatalf("应当解出 1 条记录，实际 %d", len(res.Records))
	}
	rec := res.Records[0]
	if rec.Octets != 8421 || rec.Packets != 7 {
		t.Errorf("octets/packets = %d/%d，期望 8421/7", rec.Octets, rec.Packets)
	}
	if rec.Key.String() != "10.0.0.1 → 10.0.0.2/tcp" {
		t.Errorf("五元组 = %q", rec.Key.String())
	}
	if len(res.Segments) != 1 || !res.Segments[0].Trusted || res.Segments[0].Records != 1 {
		t.Errorf("段账不对: %+v", res.Segments)
	}
	if res.TemplateDefs != 1 {
		t.Errorf("模板定义次数 = %d，期望 1", res.TemplateDefs)
	}
}

func TestTemplateMissingMarksUntrusted(t *testing.T) {
	exports := []NetFlowExport{exportOf(t, "2026-05-12T10:00:00Z", "exp-a", 1,
		dataSet(400, []any{"10.0.0.1", "10.0.0.2", "tcp", 40000, 3}),
	)}
	res := DecodeExports(exports, defaultTemplateTTL)
	if len(res.Records) != 0 {
		t.Fatalf("模板缺失时不能产出记录，实际 %d 条", len(res.Records))
	}
	seg := res.Segments[0]
	if seg.Trusted || seg.Reason != reasonTemplateMissing {
		t.Fatalf("段应当是 template_missing，实际 trusted=%v reason=%q", seg.Trusted, seg.Reason)
	}
	if seg.Dropped != 1 || seg.Records != 0 {
		t.Errorf("被丢的条数 = %d，参与统计的 = %d", seg.Dropped, seg.Records)
	}
	if res.DroppedSegments != 1 {
		t.Errorf("不可信段数 = %d，期望 1", res.DroppedSegments)
	}
	if !strings.Contains(seg.Note, "不猜") {
		t.Errorf("说明里应当写清「不猜字段」，实际 %q", seg.Note)
	}
	if got := len(reasonZH); got < 6 {
		t.Errorf("状态中文名只有 %d 条", got)
	}
}

func TestTemplateExpiryBoundary(t *testing.T) {
	// 模板在 10:00:00 定义。正好一个寿命之后仍然可信，多一秒就作废。
	cases := []struct {
		name    string
		dataTS  string
		trusted bool
	}{
		{"差一秒到期", "2026-05-12T10:29:59Z", true},
		{"正好到期", "2026-05-12T10:30:00Z", true},
		{"过了一秒", "2026-05-12T10:30:01Z", false},
	}
	for _, c := range cases {
		exports := []NetFlowExport{
			exportOf(t, "2026-05-12T10:00:00Z", "exp-a", 1, tmplSet(300, fiveFields...)),
			exportOf(t, c.dataTS, "exp-a", 2,
				dataSet(300, []any{"10.0.0.1", "10.0.0.2", "tcp", 100, 1})),
		}
		res := DecodeExports(exports, 30*time.Minute)
		seg := res.Segments[0]
		if seg.Trusted != c.trusted {
			t.Errorf("%s: trusted = %v，期望 %v（reason=%q）", c.name, seg.Trusted, c.trusted, seg.Reason)
		}
		if !c.trusted && seg.Reason != reasonTemplateExpired {
			t.Errorf("%s: reason = %q，期望 %q", c.name, seg.Reason, reasonTemplateExpired)
		}
	}
}

func TestTemplateExpiryIsSticky(t *testing.T) {
	// 模板过期之后，后续的数据段全部按「模板缺失」处理，直到重发模板。
	exports := []NetFlowExport{
		exportOf(t, "2026-05-12T10:00:00Z", "exp-a", 1, tmplSet(300, fiveFields...)),
		exportOf(t, "2026-05-12T11:00:00Z", "exp-a", 2,
			dataSet(300, []any{"10.0.0.1", "10.0.0.2", "tcp", 100, 1})),
		exportOf(t, "2026-05-12T11:01:00Z", "exp-a", 3,
			dataSet(300, []any{"10.0.0.1", "10.0.0.2", "tcp", 200, 2})),
	}
	res := DecodeExports(exports, 30*time.Minute)
	if len(res.Segments) != 2 {
		t.Fatalf("应当有 2 段，实际 %d", len(res.Segments))
	}
	if res.Segments[0].Reason != reasonTemplateExpired {
		t.Errorf("第一段原因 = %q", res.Segments[0].Reason)
	}
	if res.Segments[1].Reason != reasonTemplateMissing {
		t.Errorf("过期后第二段原因 = %q，期望 template_missing", res.Segments[1].Reason)
	}
}

func TestTemplateRedefinitionOrderMatters(t *testing.T) {
	// 同一个模板 ID 重新定义，字段顺序变了。字节数与包数长度一样、名字一样，
	// 沿用旧模板读出来不是报错，而是一组看起来正常的错数。
	oldSpec := TemplateSpec{ID: 300, Fields: []string{fieldSrcIP, fieldDstIP, fieldOctets, fieldPackets}}
	newSpec := TemplateSpec{ID: 300, Fields: []string{fieldSrcIP, fieldDstIP, fieldPackets, fieldOctets}}
	values := []any{"10.0.0.1", "10.0.0.2", 7, 8421}

	exports := []NetFlowExport{
		exportOf(t, "2026-05-12T10:00:00Z", "exp-a", 1, tmplSet(300, oldSpec.Fields...)),
		exportOf(t, "2026-05-12T10:01:00Z", "exp-a", 2, tmplSet(300, newSpec.Fields...), dataSet(300, values)),
	}
	res := DecodeExports(exports, defaultTemplateTTL)

	if len(res.Records) != 1 {
		t.Fatalf("应当解出 1 条记录，实际 %d", len(res.Records))
	}
	if res.Records[0].Octets != 8421 || res.Records[0].Packets != 7 {
		t.Errorf("按新模板解码后 octets/packets = %d/%d，期望 8421/7",
			res.Records[0].Octets, res.Records[0].Packets)
	}

	// 反过来验一遍：同一条记录按旧模板读，字节数会变成包数。
	stale, err := recordFromValues(oldSpec, values)
	if err != nil {
		t.Fatal(err)
	}
	if stale.Octets != 7 || stale.Packets != 8421 {
		t.Fatalf("旧模板读出来应当是 octets=7 packets=8421，实际 %d/%d",
			stale.Octets, stale.Packets)
	}

	redefined := 0
	for _, e := range res.Events {
		if e.Kind == eventTemplateRedefined {
			redefined++
			if !strings.Contains(e.Detail, "第 2 次定义") || !strings.Contains(e.Detail, "不同") {
				t.Errorf("事件详情 = %q", e.Detail)
			}
		}
	}
	if redefined != 1 {
		t.Fatalf("重定义事件应当有 1 条，实际 %d", redefined)
	}
}

func TestTemplateRefreshIsNotRedefinition(t *testing.T) {
	// exporter 定时重发同一份模板，字段没变。这只是刷新，不算重定义。
	exports := []NetFlowExport{
		exportOf(t, "2026-05-12T10:00:00Z", "exp-a", 1, tmplSet(300, fiveFields...)),
		exportOf(t, "2026-05-12T10:02:00Z", "exp-a", 2, tmplSet(300, fiveFields...)),
	}
	res := DecodeExports(exports, defaultTemplateTTL)
	if len(res.Events) != 0 {
		t.Fatalf("刷新不该产事件，实际 %+v", res.Events)
	}
	if res.TemplateDefs != 2 {
		t.Errorf("模板定义次数 = %d，期望 2", res.TemplateDefs)
	}
}

func TestTemplateCacheIsPerExporter(t *testing.T) {
	// 两个 exporter 都用模板 ID 300，字段布局不同。只按 ID 缓存会串味。
	a := []string{fieldSrcIP, fieldDstIP, fieldOctets}
	b := []string{fieldSrcIP, fieldDstIP, fieldPackets}
	exports := []NetFlowExport{
		exportOf(t, "2026-05-12T10:00:00Z", "exp-a", 1, tmplSet(300, a...),
			dataSet(300, []any{"10.0.0.1", "10.0.0.2", 8421})),
		exportOf(t, "2026-05-12T10:00:01Z", "exp-b", 1, tmplSet(300, b...),
			dataSet(300, []any{"10.0.1.1", "10.0.1.2", 7})),
	}
	res := DecodeExports(exports, defaultTemplateTTL)
	if len(res.Records) != 2 {
		t.Fatalf("应当解出 2 条记录，实际 %d", len(res.Records))
	}
	if res.Records[0].Octets != 8421 {
		t.Errorf("exp-a 的记录 octets = %d，期望 8421", res.Records[0].Octets)
	}
	if res.Records[1].Packets != 7 {
		t.Errorf("exp-b 的记录 packets = %d，期望 7", res.Records[1].Packets)
	}
	for _, e := range res.Events {
		if e.Kind == eventTemplateRedefined {
			t.Errorf("不同 exporter 的同号模板不该算重定义: %+v", e)
		}
	}
}

func TestLengthMismatchDropsWholeSegment(t *testing.T) {
	exports := []NetFlowExport{exportOf(t, "2026-05-12T10:00:00Z", "exp-a", 1,
		tmplSet(300, fiveFields...),
		dataSet(300,
			[]any{"10.0.0.1", "10.0.0.2", "tcp", 100, 1},
			[]any{"10.0.0.3", "10.0.0.4", "tcp", 200}, // 少一个字段
		),
	)}
	res := DecodeExports(exports, defaultTemplateTTL)
	if len(res.Records) != 0 {
		t.Fatalf("长度对不上时不能产出半条记录，实际 %d 条", len(res.Records))
	}
	seg := res.Segments[0]
	if seg.Reason != reasonLengthMismatch || seg.Dropped != 2 {
		t.Fatalf("段 = %+v", seg)
	}
	if !strings.Contains(seg.Note, "第 2 条记录") {
		t.Errorf("说明要指出是哪条记录对不上，实际 %q", seg.Note)
	}
}

func TestUnknownFieldDropsSegment(t *testing.T) {
	exports := []NetFlowExport{exportOf(t, "2026-05-12T10:00:00Z", "exp-a", 1,
		tmplSet(300, fieldSrcIP, "bgp_next_hop", fieldOctets),
		dataSet(300, []any{"10.0.0.1", "10.0.0.254", 100}),
	)}
	res := DecodeExports(exports, defaultTemplateTTL)
	seg := res.Segments[0]
	if seg.Reason != reasonUnknownField {
		t.Fatalf("reason = %q，期望 %q", seg.Reason, reasonUnknownField)
	}
	if len(res.Records) != 0 {
		t.Fatalf("不认识的字段不能瞎填，实际解出 %d 条", len(res.Records))
	}
}

func TestFieldTypeMismatchDropsSegment(t *testing.T) {
	exports := []NetFlowExport{exportOf(t, "2026-05-12T10:00:00Z", "exp-a", 1,
		tmplSet(300, fiveFields...),
		dataSet(300, []any{"10.0.0.1", "10.0.0.2", "tcp", "不是数字", 1}),
	)}
	res := DecodeExports(exports, defaultTemplateTTL)
	seg := res.Segments[0]
	if seg.Reason != reasonFieldType || seg.Trusted {
		t.Fatalf("段 = %+v", seg)
	}
}

func TestRecordFromValuesRejectsNegativeCounters(t *testing.T) {
	spec := TemplateSpec{ID: 1, Fields: fiveFields}
	if _, err := recordFromValues(spec, []any{"a", "b", "tcp", -1, 1}); err == nil {
		t.Fatal("负的字节数应当报错")
	}
	if _, err := recordFromValues(spec, []any{"a", "b", "tcp", 1}); err == nil {
		t.Fatal("值个数与模板不符应当报错")
	}
}

func TestTrustedAndDroppedCounts(t *testing.T) {
	segs := []Segment{
		{Trusted: true, Records: 3, Reason: reasonOK},
		{Trusted: false, Dropped: 2, Reason: reasonTemplateExpired},
		{Trusted: false, Dropped: 1, Reason: reasonTemplateMissing},
	}
	if got := trustedRecordCount(segs); got != 3 {
		t.Errorf("可信记录 = %d，期望 3", got)
	}
	if got := droppedRecordCount(segs); got != 3 {
		t.Errorf("被丢记录 = %d，期望 3", got)
	}
	if got := coverage(3, 3); got != 0.5 {
		t.Errorf("覆盖率 = %v，期望 0.5", got)
	}
}
