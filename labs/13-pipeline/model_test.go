package main

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

// 契约锁：12-task 的 ReportBatch / Point 用的就是这些 JSON 标签，改名等于断契约。
// 这个测试挑字段名与顺序，不挑类型——类型改了编译器会拦，名字改了只有人能拦。
func TestContractFieldNames(t *testing.T) {
	jsonNames := func(v any) []string {
		tp := reflect.TypeOf(v)
		out := make([]string, 0, tp.NumField())
		for i := 0; i < tp.NumField(); i++ {
			tag := tp.Field(i).Tag.Get("json")
			name := strings.Split(tag, ",")[0]
			out = append(out, name)
		}
		return out
	}

	wantBatch := []string{"probe_id", "batch_seq", "produced_at", "points"}
	if got := jsonNames(Batch{}); !reflect.DeepEqual(got, wantBatch) {
		t.Fatalf("批次字段 = %v，契约要求 %v", got, wantBatch)
	}
	wantPoint := []string{"probe_id", "metric_id", "acked_batch_seq", "acked_ts", "value"}
	if got := jsonNames(Point{}); !reflect.DeepEqual(got, wantPoint) {
		t.Fatalf("点位字段 = %v，契约要求 %v", got, wantPoint)
	}
	if !strings.Contains(reflect.TypeOf(Point{}).Field(4).Tag.Get("json"), "omitempty") {
		t.Fatal("value 必须可缺省：12-task 的点位没有这一项")
	}
}

const samplePath = "samples/batches.jsonl"

// mustLoadSample 读真实样本。样本是实验的一部分，读不动就直接失败，
// 不做「读不到就跳过」——那样 CI 里这条断言等于不存在。
func mustLoadSample(t *testing.T) ([]Record, []Reject, LoadStats) {
	t.Helper()
	recs, rejects, stats, err := loadBatches(samplePath)
	if err != nil {
		t.Fatalf("读样本 %s: %v", samplePath, err)
	}
	return recs, rejects, stats
}

func TestLoadSampleIsDirtyButMostlyValid(t *testing.T) {
	recs, rejects, stats := mustLoadSample(t)
	if len(recs) < 300 {
		t.Fatalf("样本应有几百条批次，实际 %d", len(recs))
	}
	if len(rejects) == 0 {
		t.Fatal("样本里必须埋了该被拒的批次，实际一条都没有")
	}
	if stats.ParsedPoints-stats.RejectedPoint != countPoints(recs) {
		t.Fatalf("通过校验的点位数对不上：统计 %d，实读 %d",
			stats.ParsedPoints-stats.RejectedPoint, countPoints(recs))
	}
	// 每一类脏数据都要在样本里出现过，否则这个实验白做。
	wantReasons := []string{
		reasonBadJSON, reasonSeqZero, reasonNoPoints,
		reasonPointProbe, reasonBadProduced, reasonDupMetric, reasonFutureAcked,
	}
	seen := map[string]bool{}
	for _, r := range rejects {
		seen[r.Reason] = true
	}
	for _, reason := range wantReasons {
		if !seen[reason] {
			t.Errorf("样本里缺这一类脏数据：%s", reason)
		}
	}
}

func TestValidateRejects(t *testing.T) {
	const produced = "2025-03-11T09:00:00Z"
	v := 1.0
	goodPoint := func() Point {
		return Point{ProbeID: "p1", AckedBatchSeq: 3, AckedTS: produced, MetricID: "rtt_p50_us", Value: &v}
	}
	goodBatch := func() Batch {
		return Batch{ProbeID: "p1", BatchSeq: 1, ProducedAt: produced, Points: []Point{goodPoint()}}
	}

	cases := []struct {
		name   string
		mutate func(*Batch)
		want   string
	}{
		{"空探针", func(b *Batch) { b.ProbeID = "  " }, reasonEmptyProbe},
		{"批次号为 0", func(b *Batch) { b.BatchSeq = 0 }, reasonSeqZero},
		{"时间格式错", func(b *Batch) { b.ProducedAt = "2025-03-11 09:00:00" }, reasonBadProduced},
		{"空批次", func(b *Batch) { b.Points = nil }, reasonNoPoints},
		{"点位探针不一致", func(b *Batch) { b.Points[0].ProbeID = "p2" }, reasonPointProbe},
		{"空指标", func(b *Batch) { b.Points[0].MetricID = "" }, reasonEmptyMetric},
		{"acked 时间格式错", func(b *Batch) { b.Points[0].AckedTS = "09:00:00" }, reasonBadAcked},
		{"acked 超前", func(b *Batch) {
			b.Points[0].AckedTS = "2025-03-11T09:12:00Z"
		}, reasonFutureAcked},
		{"acked 落后一天以上", func(b *Batch) {
			b.Points[0].AckedTS = "2025-03-09T09:00:00Z"
		}, reasonNegativeAcked},
		{"同批次指标重复", func(b *Batch) {
			dup := b.Points[0]
			nine := 9.0
			dup.Value = &nine
			b.Points = append(b.Points, dup)
		}, reasonDupMetric},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := goodBatch()
			tc.mutate(&b)
			_, reject := validate(1, b)
			if reject == nil {
				t.Fatalf("这一批该被拒（%s），实际通过了", tc.want)
			}
			if reject.Reason != tc.want {
				t.Fatalf("拒收理由 = %q，期望 %q", reject.Reason, tc.want)
			}
		})
	}

	if _, reject := validate(1, goodBatch()); reject != nil {
		t.Fatalf("干净批次不该被拒，实际 %s", reject.Reason)
	}
}

func TestValidateKeepsSubSecondAckedTS(t *testing.T) {
	twelve := 12.0
	b := Batch{
		ProbeID: "p1", BatchSeq: 7, ProducedAt: "2025-03-11T09:00:00Z",
		Points: []Point{{
			ProbeID: "p1", AckedBatchSeq: 1,
			AckedTS: "2025-03-11T08:59:58Z", MetricID: "rtt_p50_us", Value: &twelve,
		}},
	}
	rec, reject := validate(1, b)
	if reject != nil {
		t.Fatalf("不该被拒：%s", reject.Reason)
	}
	if !rec.ProducedAt.Equal(time.Date(2025, 3, 11, 9, 0, 0, 0, time.UTC)) {
		t.Fatalf("produced_at 解析成了 %s", rec.ProducedAt)
	}
	if !rec.Points[0].AckedTS.Equal(time.Date(2025, 3, 11, 8, 59, 58, 0, time.UTC)) {
		t.Fatalf("acked_ts 解析成了 %s", rec.Points[0].AckedTS)
	}
}

// 12-task 的点位没有 value：缺它按 1 计，一次确认算一个点。
func TestValidateDefaultsMissingValueToOne(t *testing.T) {
	b := Batch{
		ProbeID: "p1", BatchSeq: 3, ProducedAt: "2025-03-11T09:00:00Z",
		Points: []Point{{
			ProbeID: "p1", MetricID: "if_util.a1.eth1-1",
			AckedBatchSeq: 3, AckedTS: "2025-03-11T09:00:00Z",
		}},
	}
	rec, reject := validate(1, b)
	if reject != nil {
		t.Fatalf("缺 value 的点位是合法的（12-task 就这样），实际被拒：%s", reject.Reason)
	}
	if rec.Points[0].Value != 1 {
		t.Fatalf("缺 value 应按 1 计，实际 %v", rec.Points[0].Value)
	}
}

func TestFloorWindow(t *testing.T) {
	base := time.Date(2025, 3, 11, 9, 0, 0, 0, time.UTC)
	cases := []struct {
		offset time.Duration
		window time.Duration
		want   time.Duration
	}{
		{0, time.Minute, 0},
		{59 * time.Second, time.Minute, 0},
		{60 * time.Second, time.Minute, 60 * time.Second},
		{90*time.Second + 250*time.Millisecond, time.Minute, 60 * time.Second},
		{7 * time.Minute, 5 * time.Minute, 5 * time.Minute},
	}
	for _, tc := range cases {
		got := floorWindow(base.Add(tc.offset), tc.window)
		if want := base.Add(tc.want); !got.Equal(want) {
			t.Errorf("%s 对齐到 %s 应是 %s，实际 %s", tc.offset, tc.window, want, got)
		}
	}
}

func TestCountPointsLooseOnBrokenLine(t *testing.T) {
	// 坏行只能用 "acked_ts" 的出现次数粗数，够用来对账清单，不参与点数守恒。
	raw := `{"probe_id":"p","batch_seq":1,"points":[{"acked_ts":"x"},{"acked_ts":"y"}`
	if got := countPointsLoose(raw); got != 2 {
		t.Fatalf("粗数点位数 = %d，期望 2", got)
	}
	if got := countPointsLoose(`{"probe_id":"p"`); got != 0 {
		t.Fatalf("没有点位的行应数出 0，实际 %d", got)
	}
}

func TestToBatchesRoundTripsThroughJSON(t *testing.T) {
	recs, _, _ := mustLoadSample(t)
	want := recs[:20]
	batches := toBatches(want)
	if len(batches) != len(want) {
		t.Fatalf("批次数 %d ≠ %d", len(batches), len(want))
	}
	if !strings.Contains(batches[0].ProducedAt, "T") {
		t.Fatalf("produced_at 应是 RFC3339，实际 %q", batches[0].ProducedAt)
	}
	back, err := decodeJSONRecords(mustMarshal(t, batches))
	if err != nil {
		t.Fatalf("JSON 往返: %v", err)
	}
	if err := compareRecords(want, back); err != nil {
		t.Fatalf("JSON 往返不一致: %v", err)
	}
}
