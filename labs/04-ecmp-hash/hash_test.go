package main

import (
	"math"
	"testing"
)

// TestFnv1aGolden 钉住 FNV-1a 的取值。哈希必须可复现：同一份样本、
// 同一个盐，任何机器、任何时候跑出来都必须落同一个桶。
func TestFnv1aGolden(t *testing.T) {
	b := sampleTuple().encode()
	if got, want := fnv1a(b, 0), uint32(0x6726b588); got != want {
		t.Errorf("fnv1a(salt=0) = %#x, 期望 %#x", got, want)
	}
	if got, want := fnv1a(b, 0x12345678), uint32(0x56fd9cb0); got != want {
		t.Errorf("fnv1a(salt=0x12345678) = %#x, 期望 %#x", got, want)
	}
}

// TestCRC32CCheckValue 用 CRC-32C 的标准校验值（"123456789" → 0xE3069283）
// 确认这条多项式没写错。写错了哈希照样能跑，只是和硬件对不上。
func TestCRC32CCheckValue(t *testing.T) {
	if got, want := crc32c([]byte("123456789"), 0), uint32(0xE3069283); got != want {
		t.Errorf("CRC-32C 校验值 = %#x, 期望 %#x", got, want)
	}
	if got, want := crc32c(sampleTuple().encode(), 0x12345678), uint32(0x6c9aac0e); got != want {
		t.Errorf("crc32c(tuple, salt=0x12345678) = %#x, 期望 %#x", got, want)
	}
}

// TestBucketChangesWithSalt 说明「换一台设备就得重测」不是玄学：
// 同一批五元组，盐一换，落桶就变。
func TestBucketChangesWithSalt(t *testing.T) {
	h, err := HashByName("crc32c-mod")
	if err != nil {
		t.Fatal(err)
	}
	base := sampleTuple()
	diff := 0
	for port := 1024; port < 1024+256; port++ {
		a := h.Bucket(withPort(base, uint16(port)), 0x12345678, 4)
		b := h.Bucket(withPort(base, uint16(port)), 0xdeadbeef, 4)
		if a != b {
			diff++
		}
	}
	if diff < 64 {
		t.Errorf("256 个端口里只有 %d 个换了桶，盐几乎没起作用", diff)
	}
}

func withPort(t Tuple, port uint16) Tuple {
	t.SrcPort = port
	return t
}

// TestHashFuncsAreDeterministic 同一个输入调两次必须一样。
func TestHashFuncsAreDeterministic(t *testing.T) {
	set := PathSet{Base: sampleTuple(), HashSalt: 0x12345678}
	for _, h := range hashFuncs {
		first := h.Bucket(set.TupleWithPort(33445), set.HashSalt, 4)
		for i := 0; i < 10; i++ {
			if got := h.Bucket(set.TupleWithPort(33445), set.HashSalt, 4); got != first {
				t.Fatalf("%s 不稳定：第一次 %d，第 %d 次 %d", h.Name, first, i+2, got)
			}
		}
	}
}

// TestPow2BucketFillsTable：成员表填充到 2 的幂时，n=6 里 m0、m1 各占两格。
// 覆盖率照样 100%，负载却是 2:1——覆盖率和负载均衡是两件事。
func TestPow2BucketFillsTable(t *testing.T) {
	const n = 6
	counts := make([]int, n)
	for h := uint32(0); h < 4096; h++ {
		counts[pow2Bucket(h, n)]++
	}
	if counts[0] != 1024 || counts[1] != 1024 {
		t.Errorf("填充后 m0、m1 各应得 1024 次，实际 %d / %d", counts[0], counts[1])
	}
	for i := 2; i < n; i++ {
		if counts[i] != 512 {
			t.Errorf("m%d 应得 512 次，实际 %d", i, counts[i])
		}
	}
	// 2 的幂成员数不受影响：填充不改变任何东西。
	even := make([]int, 4)
	for h := uint32(0); h < 4096; h++ {
		even[pow2Bucket(h, 4)]++
	}
	for i, c := range even {
		if c != 1024 {
			t.Errorf("n=4 时 m%d 应得 1024 次，实际 %d", i, c)
		}
	}
}

// TestPow2BucketInRange 掩码取值必须落在 [0, members)。
func TestPow2BucketInRange(t *testing.T) {
	for _, n := range []int{2, 3, 5, 6, 7, 9, 12, 17} {
		for h := uint32(0); h < 1000; h++ {
			if idx := pow2Bucket(h*2654435761, n); idx < 0 || idx >= n {
				t.Fatalf("n=%d 时落桶下标 %d 越界", n, idx)
			}
		}
	}
}

func TestHashByNameRejectsUnknown(t *testing.T) {
	if _, err := HashByName("murmur3"); err == nil {
		t.Error("不认识的哈希函数应当报错，不能静默退回默认值")
	}
	if _, err := HashByName("crc32c-mod"); err != nil {
		t.Errorf("已知哈希函数不该报错: %v", err)
	}
}

// TestLoadShareSumsToOne 负载占比必须归一。四条 member 覆盖率 100% 的时候，
// 负载仍然可能歪到 40/20/20/20。
func TestLoadShareSumsToOne(t *testing.T) {
	set := PathSet{
		Base:     sampleTuple(),
		HashSalt: 0x12345678,
		Members:  []Member{{ID: "m0"}, {ID: "m1"}, {ID: "m2"}, {ID: "m3"}},
	}
	h, err := HashByName("crc32c-mod")
	if err != nil {
		t.Fatal(err)
	}
	shares := LoadShare(h, set, 20000, 7)
	sum := 0.0
	for _, s := range shares {
		sum += s
	}
	if math.Abs(sum-1) > 1e-9 {
		t.Errorf("负载占比之和 = %v，应当为 1", sum)
	}
	for i, s := range shares {
		// 20000 个样本下，均匀分布的偏差应在 ±2% 以内。
		if math.Abs(s-0.25) > 0.02 {
			t.Errorf("m%d 占比 %.3f 偏离 0.25 过多", i, s)
		}
	}
}
