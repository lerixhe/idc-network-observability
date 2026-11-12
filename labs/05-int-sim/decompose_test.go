package main

import (
	"math"
	"testing"
)

// threeHopTrace 造一条 3 跳的路径：首末跳有数据，中间一跳可选有没有。
//
// 时间线是按真值排的：中间那跳排队 1 ms，驻留 1.004 ms；
// 链路每段 30 m（150 ns）；首跳的 link_length_m 记 0，因为「服务器→leaf」
// 那一段不在端到端窗口里——台账里记它，差额就会多出一截（样本里也是 0）。
func threeHopTrace(middleBlind bool) Trace {
	h1 := goodHop(1, "sw-a", 0, 3_000, 0)
	h1.LinkLengthM = 0
	h2 := goodHop(2, "sw-b", 3_150, 1_007_150, 12_500_000) // 12.5 MB @100G = 1 ms 排队
	if middleBlind {
		h2 = blindHop(2, "sw-b")
		h2.LinkLengthM = 30
	}
	h3 := goodHop(3, "sw-c", 1_007_300, 1_010_300, 0)
	return Trace{ID: "t3", Hops: []HopMeta{h1, h2, h3}}
}

// TestQueueDelayNs 把算式钉住：queue_ns = depth × 8 ÷ bps × 10^9。
func TestQueueDelayNs(t *testing.T) {
	cases := []struct {
		depth, bps, want int64
	}{
		// 39.6 MB @ 100G = 316.8 Mbit ÷ 100 Gbit/s = 3.168 ms
		{39_600_000, 100_000_000_000, 3_168_000},
		// 4144 B @ 25G = 33 152 bit ÷ 25 Gbit/s = 1326.08 ns
		{4144, 25_000_000_000, 1326},
		{0, 100_000_000_000, 0},
		{1000, 0, 0},
	}
	for _, c := range cases {
		if got := queueDelayNs(c.depth, c.bps); got != c.want {
			t.Errorf("queueDelayNs(%d B, %d bps) = %d, 期望 %d", c.depth, c.bps, got, c.want)
		}
	}
}

// TestResidentIsClockInvariant 是本实验的核心断言：
// 每跳驻留只依赖同一台设备的时间戳差，把整条路径的时钟偏移挪走，驻留一个都不变。
func TestResidentIsClockInvariant(t *testing.T) {
	base := threeHopTrace(false)
	shifted := threeHopTrace(false)
	offsets := map[int]int64{0: 700_000, 1: -3_400_000, 2: 12_000_000}
	for i := range shifted.Hops {
		if !shifted.Hops[i].IntEnabled {
			continue
		}
		shifted.Hops[i].IngressTsNs += offsets[i]
		shifted.Hops[i].EgressTsNs += offsets[i]
	}

	d1, err := Decompose(base, defaultClockAccuracy)
	if err != nil {
		t.Fatal(err)
	}
	d2, err := Decompose(shifted, defaultClockAccuracy)
	if err != nil {
		t.Fatal(err)
	}
	if d1.SumResidentNs != d2.SumResidentNs {
		t.Errorf("时钟偏移改变了驻留合计：%d vs %d", d1.SumResidentNs, d2.SumResidentNs)
	}
	for i := range d1.Hops {
		if d1.Hops[i].ResidentNs != d2.Hops[i].ResidentNs {
			t.Errorf("hop %d 驻留被时钟偏移影响：%d vs %d",
				d1.Hops[i].Index, d1.Hops[i].ResidentNs, d2.Hops[i].ResidentNs)
		}
	}
	// 端到端反过来必须变，而且变的量就是首末跳偏移差。
	want := d1.E2EMeasuredNs + (offsets[2] - offsets[0])
	if d2.E2EMeasuredNs != want {
		t.Errorf("端到端应当移动 %d ns，实际 %d → %d（期望 %d）",
			offsets[2]-offsets[0], d1.E2EMeasuredNs, d2.E2EMeasuredNs, want)
	}
}

// TestFullCoverageTrace 全跳有数据时：能归因到底，盲区为 0（在时钟误差内）。
func TestFullCoverageTrace(t *testing.T) {
	d, err := Decompose(threeHopTrace(false), defaultClockAccuracy)
	if err != nil {
		t.Fatal(err)
	}
	if d.Instrumented != 3 || d.HopCoverage != 1 {
		t.Errorf("应当 3/3 跳有数据，实际 %d/%d", d.Instrumented, d.HopCount)
	}
	if len(d.Segments) != 0 {
		t.Errorf("全跳有数据时不该有盲区段，实际 %d 段", len(d.Segments))
	}
	if d.AttributedShare < 0.9 {
		t.Errorf("全跳有数据时归因比例应当接近 1，实际 %.3f", d.AttributedShare)
	}
	if d.TopQueueDevice != "sw-b" {
		t.Errorf("排队最多的应当是 sw-b，实际 %s", d.TopQueueDevice)
	}
	// 中间那跳 1 ms 排队，驻留 1.004 ms，处理只有 4 µs。
	mid := d.Hops[1]
	if mid.QueueNs != 1_000_000 {
		t.Errorf("sw-b 排队应当是 1000000 ns，实际 %d", mid.QueueNs)
	}
	if mid.Attribution != AttrQueue {
		t.Errorf("sw-b 应当判排队为主，实际 %s", mid.Attribution)
	}
	if d.Hops[0].Attribution != AttrForward {
		t.Errorf("sw-a 没有排队，应当判处理为主，实际 %s", d.Hops[0].Attribution)
	}
}

// TestPartialCoverageTrace 中间一跳没数据：差额全进盲区段。
func TestPartialCoverageTrace(t *testing.T) {
	d, err := Decompose(threeHopTrace(true), defaultClockAccuracy)
	if err != nil {
		t.Fatal(err)
	}
	if d.Instrumented != 2 || math.Abs(d.HopCoverage-2.0/3.0) > 1e-9 {
		t.Errorf("应当 2/3 跳有数据，实际 %d/%d", d.Instrumented, d.HopCount)
	}
	if len(d.Segments) != 1 {
		t.Fatalf("应当有 1 个盲区段，实际 %d", len(d.Segments))
	}
	seg := d.Segments[0]
	if seg.FromHop != 1 || seg.ToHop != 3 || len(seg.BlindDevice) != 1 {
		t.Errorf("盲区段应当是 hop1 → hop3 中间夹 sw-b，实际 %+v", seg)
	}
	if !seg.Allocatable {
		t.Error("只有一个盲区段时应当可分到差额")
	}
	if seg.ShareNs != d.BlindResidentNs {
		t.Errorf("段差额应当等于盲区驻留：%d vs %d", seg.ShareNs, d.BlindResidentNs)
	}
	// 盲区里那一跳的真驻留是 1.004 ms（1 ms 排队 + 4 µs 处理），估计区间得罩住它。
	const blindResidentTruth = 1_004_000
	if d.BlindLoNs > blindResidentTruth || d.BlindHiNs < blindResidentTruth {
		t.Errorf("盲区区间 [%d, %d] 没有覆盖真值 %d ns", d.BlindLoNs, d.BlindHiNs, blindResidentTruth)
	}
	if d.AttributedShare > 0.1 {
		t.Errorf("盲区吃掉 1 ms 时归因比例不该高，实际 %.3f", d.AttributedShare)
	}
}

// TestMultipleBlindSegmentsNotAllocatable 有多个盲区段时，差额不可按段切分。
func TestMultipleBlindSegmentsNotAllocatable(t *testing.T) {
	r1 := goodHop(1, "sw-a", 0, 3_000, 0)
	r1.LinkLengthM = 0
	b2 := blindHop(2, "old-a")
	b2.LinkLengthM = 30
	r3 := goodHop(3, "sw-c", 20_200, 23_200, 0)
	b4 := blindHop(4, "old-b")
	r5 := goodHop(5, "sw-e", 40_400, 43_400, 0)
	tr := Trace{ID: "t4", Hops: []HopMeta{r1, b2, r3, b4, r5}}
	d, err := Decompose(tr, defaultClockAccuracy)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Segments) != 2 {
		t.Fatalf("应当有 2 个盲区段，实际 %d", len(d.Segments))
	}
	for _, seg := range d.Segments {
		if seg.Allocatable {
			t.Error("多个盲区段时不该声称差额可分")
		}
		if seg.ShareNs != 0 {
			t.Errorf("不可分时不该给段差额，实际 %d", seg.ShareNs)
		}
	}
}

// TestQueueCappedByResident 队列估到超过驻留时按驻留封顶，并把矛盾说出来。
// 真实原因是队列深度是采样时刻的瞬时值，与本包的排队时间不是一个口径。
func TestQueueCappedByResident(t *testing.T) {
	h1 := goodHop(1, "sw-a", 0, 3_000, 0)
	h1.LinkLengthM = 0
	h1.QueueDepthBytes = 50_000 // @100G = 4 µs，但驻留只有 3 µs
	tr := Trace{ID: "t5", Hops: []HopMeta{h1, goodHop(2, "sw-b", 10_000, 13_000, 0)}}
	d, err := Decompose(tr, defaultClockAccuracy)
	if err != nil {
		t.Fatal(err)
	}
	h := d.Hops[0]
	if h.QueueNs != h.ResidentNs {
		t.Errorf("排队应当被驻留封顶：%d vs %d", h.QueueNs, h.ResidentNs)
	}
	if h.ForwardNs != 0 {
		t.Errorf("封顶后处理时延应当是 0，实际 %d", h.ForwardNs)
	}
	if h.Note == "" {
		t.Error("封顶时必须留下说明，不能悄悄取个小值")
	}
}

// TestNaiveCrossHopCountsNegatives 跨跳相减的反例：负时延。
func TestNaiveCrossHopCountsNegatives(t *testing.T) {
	tr := threeHopTrace(false)
	tr.Hops[2].IngressTsNs -= 900_000 // 末跳时钟落后 0.9 ms
	tr.Hops[2].EgressTsNs -= 900_000
	d, err := Decompose(tr, defaultClockAccuracy)
	if err != nil {
		t.Fatal(err)
	}
	if d.Naive.Count != 2 {
		t.Errorf("跨跳相减应当有 2 段，实际 %d", d.Naive.Count)
	}
	if d.Naive.Negatives == 0 {
		t.Errorf("应当至少出现一段负时延，实际 0；min=%d max=%d", d.Naive.MinNs, d.Naive.MaxNs)
	}
	if d.Naive.MinNs >= d.Naive.MaxNs {
		t.Errorf("最小最大没算出来：%d / %d", d.Naive.MinNs, d.Naive.MaxNs)
	}
}

// TestSamplesDecompose 用仓库样本跑一遍完整断言。
func TestSamplesDecompose(t *testing.T) {
	traces, err := loadTraces("samples/traces.json")
	if err != nil {
		t.Fatal(err)
	}
	byID := make(map[string]Decomposition, len(traces))
	for _, tr := range traces {
		d, err := Decompose(tr, defaultClockAccuracy)
		if err != nil {
			t.Fatalf("%s: %v", tr.ID, err)
		}
		byID[tr.ID] = d
	}

	full := byID["t-full"]
	if full.Instrumented != 6 || len(full.Segments) != 0 {
		t.Errorf("t-full 应当是 6/6 跳、无盲区，实际 %d/%d、%d 段",
			full.Instrumented, full.HopCount, len(full.Segments))
	}
	if full.TopQueueDevice != "spine-01" {
		t.Errorf("t-full 的排队应当归到 spine-01，实际 %s", full.TopQueueDevice)
	}
	if full.Naive.Negatives != 3 {
		t.Errorf("t-full 跨跳相减应当有 3 段负数，实际 %d", full.Naive.Negatives)
	}
	// 全跳有数据时，盲区驻留应当落在时钟误差之内——也就是「没有盲区」。
	if abs64(full.BlindResidentNs) > full.ClockErrNs {
		t.Errorf("全跳有数据却算出 %d ns 盲区，超过时钟误差 %d ns",
			full.BlindResidentNs, full.ClockErrNs)
	}
	if full.AttributedShare < 0.95 {
		t.Errorf("t-full 归因比例应当 > 95%%，实际 %.1f%%", full.AttributedShare*100)
	}

	partial := byID["t-partial"]
	if partial.Instrumented != 4 || len(partial.Segments) != 1 {
		t.Errorf("t-partial 应当是 4/6 跳、1 个盲区段，实际 %d/%d、%d 段",
			partial.Instrumented, partial.HopCount, len(partial.Segments))
	}
	seg := partial.Segments[0]
	if seg.FromDevice != "spine-01" || seg.ToDevice != "spine-11" {
		t.Errorf("盲区段应当是 spine-01 → spine-11，实际 %s → %s", seg.FromDevice, seg.ToDevice)
	}
	if len(seg.BlindDevice) != 2 {
		t.Errorf("盲区段应当跨 2 跳，实际 %d", len(seg.BlindDevice))
	}
	if partial.AttributedShare > 0.2 {
		t.Errorf("t-partial 归因比例不该超过 20%%，实际 %.1f%%", partial.AttributedShare*100)
	}
}

// TestReconcileWithTruth 拿真值对账：驻留必须精确，排队是估的，端到端被时钟污染。
func TestReconcileWithTruth(t *testing.T) {
	traces, err := loadTraces("samples/traces.json")
	if err != nil {
		t.Fatal(err)
	}
	truth, err := loadTruth("samples/truth.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, tr := range traces {
		tt, ok := truth[tr.ID]
		if !ok {
			t.Fatalf("%s 没有真值", tr.ID)
		}
		d, err := Decompose(tr, defaultClockAccuracy)
		if err != nil {
			t.Fatal(err)
		}
		byIndex := make(map[int]HopTruth, len(tt.Hops))
		for _, h := range tt.Hops {
			byIndex[h.Index] = h
		}
		for _, h := range d.Hops {
			th := byIndex[h.Index]
			if !h.HasData {
				continue
			}
			if h.ResidentNs != th.ResidentNs {
				t.Errorf("%s hop %d 驻留不精确：%d vs 真值 %d",
					tr.ID, h.Index, h.ResidentNs, th.ResidentNs)
			}
			// 排队是估的，允许 5% 以内的偏差——这个偏差来自采样时刻。
			if th.QueueNs > 0 {
				off := math.Abs(float64(h.QueueNs-th.QueueNs)) / float64(th.QueueNs)
				if off > 0.05 {
					t.Errorf("%s hop %d 排队估计偏差 %.1f%% 超过 5%%：%d vs %d",
						tr.ID, h.Index, off*100, h.QueueNs, th.QueueNs)
				}
			}
		}
		// 端到端误差必须正好等于首末跳的时钟偏移差。
		delta := tt.ClockOffsetsNs[tr.Hops[len(tr.Hops)-1].Device] - tt.ClockOffsetsNs[tr.Hops[0].Device]
		if got := d.E2EMeasuredNs - tt.E2ETrueNs; got != delta {
			t.Errorf("%s 端到端误差 %d ≠ 时钟偏移差 %d", tr.ID, got, delta)
		}
		if got := d.BlindResidentNs - blindTruth(tt); got != delta {
			t.Errorf("%s 盲区误差 %d ≠ 时钟偏移差 %d", tr.ID, got, delta)
		}
		// 盲区估计的区间必须罩住真值：这是「部分覆盖」敢不敢下结论的唯一依据。
		if d.BlindLoNs > blindTruth(tt) || d.BlindHiNs < blindTruth(tt) {
			t.Errorf("%s 盲区区间 [%d, %d] 没罩住真值 %d",
				tr.ID, d.BlindLoNs, d.BlindHiNs, blindTruth(tt))
		}
	}
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}
