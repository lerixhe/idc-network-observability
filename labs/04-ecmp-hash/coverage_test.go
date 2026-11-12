package main

import (
	"math"
	"math/rand"
	"testing"
)

// newRng 给测试造一个固定种子的随机源。
func newRng(seed int64) *rand.Rand {
	return rand.New(rand.NewSource(seed))
}

func fourMembers() PathSet {
	return PathSet{
		Name:     "test",
		HashSalt: 0x12345678,
		Base:     sampleTuple(),
		Members:  []Member{{ID: "m0"}, {ID: "m1"}, {ID: "m2"}, {ID: "m3"}},
	}
}

func mustHash(t *testing.T, name string) HashFunc {
	t.Helper()
	h, err := HashByName(name)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// TestFixedModeCoverageStuck 是整个实验的核心结论：
// 固定五元组打一万个包，覆盖率也不动一下。
func TestFixedModeCoverageStuck(t *testing.T) {
	h := mustHash(t, "crc32c-mod")
	points, err := Curve(fourMembers(), h, ModeFixed, []int{1, 10, 100, 1000, 10000}, 1)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range points {
		if p.Covered != 1 {
			t.Fatalf("固定五元组在 %d 次探测后覆盖了 %d 条 member，应当永远是 1", p.Probes, p.Covered)
		}
	}
}

// TestCurveMonotone 曲线只能升。降了说明在数「这一轮打到了几条」，那个数没有意义。
func TestCurveMonotone(t *testing.T) {
	h := mustHash(t, "crc32c-mod")
	for _, mode := range modeNames {
		points, err := Curve(fourMembers(), h, mode, curveGrid, 3)
		if err != nil {
			t.Fatal(err)
		}
		prev := 0
		for _, p := range points {
			if p.Covered < prev {
				t.Fatalf("%s 在 %d 次探测时覆盖率回落：%d → %d", mode, p.Probes, prev, p.Covered)
			}
			prev = p.Covered
		}
	}
}

// TestCurveRejectsBadCheckpoints 递增校验。乱序的检查点会静默少算几个点。
func TestCurveRejectsBadCheckpoints(t *testing.T) {
	h := mustHash(t, "crc32c-mod")
	if _, err := Curve(fourMembers(), h, ModeFixed, nil, 1); err == nil {
		t.Error("空 checkpoints 应当报错")
	}
	if _, err := Curve(fourMembers(), h, ModeFixed, []int{10, 10}, 1); err == nil {
		t.Error("重复的 checkpoints 应当报错")
	}
	if _, err := Curve(fourMembers(), h, ModeFixed, []int{10, 5}, 1); err == nil {
		t.Error("乱序的 checkpoints 应当报错")
	}
}

// TestRandomModeReachesFull 随机源端口最终必须覆盖满：2000 个包足够。
func TestRandomModeReachesFull(t *testing.T) {
	h := mustHash(t, "crc32c-mod")
	points, err := Curve(fourMembers(), h, ModeRandom, []int{2000}, 5)
	if err != nil {
		t.Fatal(err)
	}
	if points[0].Covered != 4 {
		t.Errorf("2000 次随机探测仍未覆盖满：%d/4", points[0].Covered)
	}
}

// TestBudgetPortsUnique 预算端口前 64512 个互不重复。
// 步长与端口区间必须互质，否则预算加到某个数就原地打转。
func TestBudgetPortsUnique(t *testing.T) {
	seen := make(map[uint16]bool, portSpan)
	for k := 0; k < portSpan; k++ {
		p := budgetPort(k)
		if p < portMin || p > portMax {
			t.Fatalf("第 %d 个预算端口 %d 越界", k, p)
		}
		if seen[p] {
			t.Fatalf("第 %d 个预算端口 %d 重复", k, p)
		}
		seen[p] = true
	}
}

// TestRandomPortsInRange 随机端口必须落在可分配区间内。
func TestRandomPortsInRange(t *testing.T) {
	for k := 0; k < 1000; k++ {
		p := probePort(ModeRandom, k, newRng(11))
		if p < portMin || p > portMax {
			t.Fatalf("随机端口 %d 越界", p)
		}
	}
}

func TestProbePortFixed(t *testing.T) {
	for k := 0; k < 5; k++ {
		if got := probePort(ModeFixed, k, newRng(1)); got != fixedPort {
			t.Errorf("固定模式的第 %d 个包用了端口 %d，应当是 %d", k+1, got, fixedPort)
		}
	}
}

// TestExpectedProbes 对优惠券收集器的期望值：E[T] = n·H_n。
func TestExpectedProbes(t *testing.T) {
	cases := []struct {
		n    int
		want float64
	}{
		{1, 1.0},
		{2, 3.0},
		{4, 8.3333333},
		{6, 14.7},
		{8, 21.7428571},
	}
	for _, c := range cases {
		if got := ExpectedProbes(c.n); math.Abs(got-c.want) > 1e-6 {
			t.Errorf("ExpectedProbes(%d) = %.7f, 期望 %.7f", c.n, got, c.want)
		}
	}
}

// TestProbesToCoverFixedNeverCompletes 固定五元组永远覆盖不满，
// 这一项必须如实报「永不」，不能填一个天文数字冒充分位数。
func TestProbesToCoverFixedNeverCompletes(t *testing.T) {
	h := mustHash(t, "crc32c-mod")
	s := ProbesToCover(fourMembers(), h, ModeFixed, 20, 1)
	if s.Unfinished != s.Trials {
		t.Errorf("固定模式应当全部未覆盖满，实际 %d/%d", s.Unfinished, s.Trials)
	}
	if s.P50 != 0 {
		t.Errorf("未覆盖满时不该给出分位数，实际 p50=%d", s.P50)
	}
}

// TestProbesToCoverRandomNearTheory 实测中位数应当和 n·H_n 同量级。
// 卡的区间很宽：这里验的是「按优惠券收集器在走」，不是精确值。
func TestProbesToCoverRandomNearTheory(t *testing.T) {
	h := mustHash(t, "crc32c-mod")
	s := ProbesToCover(fourMembers(), h, ModeRandom, 500, 2)
	if s.Unfinished != 0 {
		t.Fatalf("随机模式应当每次都能覆盖满，实际 %d 次没满", s.Unfinished)
	}
	if s.P50 < 4 || s.P50 > 20 {
		t.Errorf("p50 = %d 超出 [4, 20]，不像优惠券收集器的节奏（理论期望 8.3）", s.P50)
	}
	if !(s.P50 <= s.P90 && s.P90 <= s.P99) {
		t.Errorf("分位数没有单调：%d / %d / %d", s.P50, s.P90, s.P99)
	}
}

// TestBlindSpotsFixedModeExactlyOneVisible 4 条 member 里，
// 固定五元组只能看见 1 条，另外 3 条坏了完全看不见。
func TestBlindSpotsFixedModeExactlyOneVisible(t *testing.T) {
	h := mustHash(t, "crc32c-mod")
	spots := BlindSpots(fourMembers(), h, ModeFixed, 100, 1)
	visible := 0
	for _, s := range spots {
		if !s.Invisible {
			visible++
			if s.ObservedLoss != 1 {
				t.Errorf("%s 是唯一被覆盖的 member，观测丢包率应当是 100%%，实际 %.2f", s.Broken, s.ObservedLoss)
			}
		}
	}
	if visible != 1 {
		t.Errorf("固定五元组只该看见 1 条 member，实际 %d", visible)
	}
}

// TestHitsSumToProbes 每个包只能落到一条 member，命中数之和等于包数。
func TestHitsSumToProbes(t *testing.T) {
	h := mustHash(t, "crc32c-mod")
	for _, mode := range modeNames {
		hits := Hits(fourMembers(), h, mode, 137, 9)
		sum := 0
		for _, hit := range hits {
			sum += hit.Probes
		}
		if sum != 137 {
			t.Errorf("%s 命中数之和 = %d，应当是 137", mode, sum)
		}
	}
}

// TestModeParsing 探测方式的名字进 CSV，拼错必须报错。
func TestModeParsing(t *testing.T) {
	if m, err := ParseMode("random"); err != nil || m != ModeRandom {
		t.Errorf("ParseMode(random) = %v, %v", m, err)
	}
	if _, err := ParseMode("Random"); err == nil {
		t.Error("大小写不同应当报错，不要偷偷兜底")
	}
}

// TestJoinMembers 空列表打 "-"，不然表格里那一列会错位。
func TestJoinMembers(t *testing.T) {
	if got := joinMembers(nil); got != "-" {
		t.Errorf("空列表应当打 \"-\"，实际 %q", got)
	}
	if got := joinMembers([]string{"m0", "m2"}); got != "m0,m2" {
		t.Errorf("joinMembers = %q", got)
	}
}
