package main

import (
	"testing"
	"time"
)

func sampleStream(t *testing.T) []Record {
	t.Helper()
	recs, _, _ := mustLoadSample(t)
	return dedupAll(recs).Records
}

// 迟到超窗口的点必须被标记，不许静默丢弃。
func TestLatePointsAreMarkedNotDropped(t *testing.T) {
	stream := sampleStream(t)
	narrow := runWindow(stream, time.Minute, time.Second, 15*time.Minute)

	if narrow.PointsLate == 0 {
		t.Fatal("1 秒水位线居然一个点都没丢？样本里的迟到批次白埋了")
	}
	if len(narrow.Late) != narrow.PointsLate {
		t.Fatalf("%d 个迟到点只有 %d 条标记，有数据被静默丢了", narrow.PointsLate, len(narrow.Late))
	}
	if narrow.PointsIn != narrow.PointsKept+narrow.PointsLate {
		t.Fatalf("点数不守恒：进 %d ≠ 收 %d + 迟 %d",
			narrow.PointsIn, narrow.PointsKept, narrow.PointsLate)
	}
	for _, l := range narrow.Late {
		if l.Reason == "" {
			t.Fatalf("迟到标记没写理由：%+v", l)
		}
		if l.LatenessMs < 0 {
			t.Fatalf("迟到量算成负数：%+v", l)
		}
		if l.LatenessMs == 0 {
			t.Fatalf("被挡下的点迟到量不该是 0：%+v", l)
		}
	}
}

// 水位线越宽，丢得越少——这条单调性是水位线实现对不对的第一道检查。
func TestWiderWatermarkNeverDropsMore(t *testing.T) {
	stream := sampleStream(t)
	widths := []time.Duration{0, time.Second, time.Minute, 5 * time.Minute, 15 * time.Minute, 30 * time.Minute}
	prevLate := -1
	for _, wm := range widths {
		res := runWindow(stream, time.Minute, wm, 15*time.Minute)
		if prevLate >= 0 && res.PointsLate > prevLate {
			t.Fatalf("水位线 %s 丢了 %d 点，比更窄的一档 %d 点还多", wm, res.PointsLate, prevLate)
		}
		prevLate = res.PointsLate
	}
}

// 超出控制面补报承诺的点，理由要不一样：那不是水位线配窄了，是承诺本身兜不住。
func TestLateBeyondBackfillIsLabelled(t *testing.T) {
	stream := sampleStream(t)
	res := runWindow(stream, time.Minute, 15*time.Minute, 15*time.Minute)
	if res.PointsLate == 0 {
		t.Fatal("15 分钟水位线下仍应有 20 分钟才到的那一批被挡下")
	}
	if res.EarlyRejected != res.PointsLate {
		t.Fatalf("这些点全部超补报延迟，实际标了 %d / %d", res.EarlyRejected, res.PointsLate)
	}
	for _, l := range res.Late {
		if l.Reason != lateOverBackfill {
			t.Fatalf("理由应是超出补报延迟，实际 %q", l.Reason)
		}
		if time.Duration(l.LatenessMs)*time.Millisecond <= 15*time.Minute {
			t.Fatalf("迟到 %dms 没超过 15 分钟却按超补报处理", l.LatenessMs)
		}
	}
}

// 宽水位线把所有点都收下，聚合就等于真值：偏差 0，这是「不丢数据」的参照组。
func TestWideWatermarkMatchesTruth(t *testing.T) {
	stream := sampleStream(t)
	truth := truthWindow(stream, time.Minute)
	wide := runWindow(stream, time.Minute, 30*time.Minute, 15*time.Minute)

	if wide.PointsLate != 0 {
		t.Fatalf("30 分钟水位线不该丢点，实际丢 %d", wide.PointsLate)
	}
	devs := deviation(truth, wide)
	if len(devs) == 0 {
		t.Fatal("偏差表是空的")
	}
	for _, d := range devs {
		if d.TotalPct != 0 || d.LostSum != 0 {
			t.Fatalf("%s 一个点没丢却算出偏差 %.4f%%", d.MetricID, d.TotalPct)
		}
	}
}

// 窄水位线丢的是真数据：偏差必须是负的，而且要能指出丢了多少。
func TestNarrowWatermarkLosesValue(t *testing.T) {
	stream := sampleStream(t)
	truth := truthWindow(stream, time.Minute)
	narrow := runWindow(stream, time.Minute, time.Second, 15*time.Minute)

	devs := deviation(truth, narrow)
	lost := 0.0
	for _, d := range devs {
		if d.LostSum < 0 {
			t.Fatalf("%s 丢了负数？%v", d.MetricID, d.LostSum)
		}
		if d.TotalPct > 0 {
			t.Fatalf("%s 偏差为正 %v，说明聚合值被低估而不是被丢掉", d.MetricID, d.TotalPct)
		}
		lost += d.LostSum
	}
	if lost <= 0 {
		t.Fatal("窄水位线应该有损失，实际一点没丢")
	}
	if narrow.PointsKept >= truth.PointsKept {
		t.Fatalf("窄水位线收下的点 %d 不该多于真值 %d", narrow.PointsKept, truth.PointsKept)
	}
}

// 乱序但没迟到：同一窗口内的批次前后颠倒，一个点都不该丢。
func TestOutOfOrderWithinWindowKeptAll(t *testing.T) {
	base := dateTime(2025, 3, 11, 9, 0, 0, 0)
	mk := func(seq uint64, offset time.Duration, value float64) Record {
		ts := base.Add(offset)
		return Record{
			ProbeID: "p1", BatchSeq: seq, ProducedAt: ts,
			Points: []Datum{{ProbeID: "p1", AckedBatchSeq: 1, AckedTS: ts, MetricID: "m", Value: value}},
		}
	}
	// 到达顺序 30s → 0s → 45s → 15s，全在同一个 60 秒窗口里
	stream := []Record{
		mk(2, 30*time.Second, 3),
		mk(1, 0, 1),
		mk(4, 45*time.Second, 4),
		mk(3, 15*time.Second, 2),
	}
	res := runWindow(stream, time.Minute, 10*time.Second, time.Minute)
	if res.PointsLate != 0 || res.PointsKept != 4 {
		t.Fatalf("窗口内的乱序不该丢点：迟到 %d / 收 %d", res.PointsLate, res.PointsKept)
	}
	if len(res.Aggs) != 1 || res.Aggs[0].Sum != 10 || res.Aggs[0].Count != 4 {
		t.Fatalf("聚合不是 sum=10/count=4：%+v", res.Aggs)
	}
	if res.Aggs[0].Min != 1 || res.Aggs[0].Max != 4 {
		t.Fatalf("min/max 算错：%+v", res.Aggs[0])
	}
}

// 1 分钟并成 5 分钟必须无损：sum 与 count 可加，min/max 取极值。
func TestRollup5mIsLossless(t *testing.T) {
	stream := sampleStream(t)
	win := runWindow(stream, time.Minute, 15*time.Minute, 15*time.Minute)
	rolled := rollup5m(win.Aggs)

	if len(rolled) == 0 || len(rolled)*5 != len(win.Aggs) {
		t.Fatalf("1 分钟聚合 %d 个应并成 %d 个 5 分钟聚合，实际 %d",
			len(win.Aggs), len(win.Aggs)/5, len(rolled))
	}
	type key struct{ probe, metric string }
	sumIn := map[key]float64{}
	cntIn := map[key]int{}
	for _, a := range win.Aggs {
		k := key{a.ProbeID, a.MetricID}
		sumIn[k] += a.Sum
		cntIn[k] += a.Count
	}
	sumOut := map[key]float64{}
	cntOut := map[key]int{}
	for _, a := range rolled {
		k := key{a.ProbeID, a.MetricID}
		sumOut[k] += a.Sum
		cntOut[k] += a.Count
	}
	for k, v := range sumIn {
		if diff := sumOut[k] - v; diff > 1e-6 || diff < -1e-6 {
			t.Fatalf("%v 的 sum 合并后差了 %v", k, diff)
		}
		if cntOut[k] != cntIn[k] {
			t.Fatalf("%v 的 count 合并后 %d ≠ %d", k, cntOut[k], cntIn[k])
		}
	}
	for _, a := range rolled {
		if a.WindowStart.Unix()%300 != 0 {
			t.Fatalf("5 分钟窗口起点没对齐：%s", a.WindowStart)
		}
	}
}

func TestWindowFlushClosesOpenWindows(t *testing.T) {
	// 数据流结束时的尾巴窗口要刷出来，不能因为「还没等到水位线」就留在内存里。
	base := dateTime(2025, 3, 11, 9, 0, 0, 0)
	stream := []Record{{
		ProbeID: "p1", BatchSeq: 1, ProducedAt: base,
		Points: []Datum{{ProbeID: "p1", AckedBatchSeq: 1, AckedTS: base, MetricID: "m", Value: 5}},
	}}
	res := runWindow(stream, time.Minute, 15*time.Minute, 15*time.Minute)
	if len(res.Aggs) != 1 || res.Aggs[0].Sum != 5 || res.Aggs[0].Count != 1 {
		t.Fatalf("收盘时最后一个窗口没刷出来：%+v", res.Aggs)
	}
	if res.Windows != 1 {
		t.Fatalf("窗口数 %d ≠ 1", res.Windows)
	}
}

func TestCompareWatermarksCoversNarrowAndWide(t *testing.T) {
	stream := sampleStream(t)
	widths, err := parseCompare("", 15*time.Minute)
	if err != nil {
		t.Fatalf("默认对比档位: %v", err)
	}
	if len(widths) != 4 {
		t.Fatalf("默认应有 4 档，实际 %d", len(widths))
	}
	rows := compareWatermarks(stream, time.Minute, widths, 15*time.Minute, 8.2)
	if rows[0].PointsLate == 0 {
		t.Fatal("最窄的一档应该丢数据，否则「太窄」这件事没演示出来")
	}
	if rows[len(rows)-1].PointsLate != 0 {
		t.Fatal("最宽的一档不该丢数据")
	}
	if rows[0].VisibleLag >= rows[len(rows)-1].VisibleLag {
		t.Fatal("可见延迟应随水位线变宽而变长")
	}
	if !rows[0].UnderBackfill {
		t.Fatal("1 分钟水位线窄于 15 分钟补报承诺，应标记 UnderBackfill")
	}
	if rows[2].Watermark != 15*time.Minute || rows[2].UnderBackfill {
		t.Fatalf("15 分钟档应与补报承诺对齐：%+v", rows[2])
	}
}
