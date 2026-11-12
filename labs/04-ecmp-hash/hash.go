package main

import (
	"fmt"
	"hash/crc32"
	"math/rand"
	"strings"
)

// HashFunc 是一次「五元组 → 成员下标」的计算。
//
// Name 进终端表格和 CSV，改名等于换口径，别改。
type HashFunc struct {
	Name string
	// Bucket 把五元组落到 [0, members) 里的一个下标。
	Bucket func(t Tuple, salt uint32, members int) int
}

// cstTable 是 Castagnoli 多项式（CRC-32C）。多数交换 ASIC 做哈希用的就是它，
// 硬件里那条 crc32c 指令算的就是这条。
var cstTable = crc32.MakeTable(crc32.Castagnoli)

// fnv1a 手写的 FNV-1a 32：先 XOR 盐当初始值，再逐字节混淆一次。
//
// 手写是因为它要能在 README 里一行一行对着算：一次 XOR、一次乘。
func fnv1a(b []byte, salt uint32) uint32 {
	h := uint32(2166136261) ^ salt
	for _, c := range b {
		h ^= uint32(c)
		h *= 16777619
	}
	return h
}

// crc32c 算 CRC-32C，盐当初始值——等价于设备上配一个不同的 hash seed。
func crc32c(b []byte, salt uint32) uint32 {
	return crc32.Update(salt, cstTable, b)
}

// hashFuncs 是这一课用到的三个哈希口径。前两个是全位取模，第三个模拟
// 「成员表填充到 2 的幂、用低位掩码索引」的 ASIC。
var hashFuncs = []HashFunc{
	{Name: "fnv1a-mod", Bucket: func(t Tuple, salt uint32, n int) int {
		return int(fnv1a(t.encode(), salt) % uint32(n))
	}},
	{Name: "crc32c-mod", Bucket: func(t Tuple, salt uint32, n int) int {
		return int(crc32c(t.encode(), salt) % uint32(n))
	}},
	{Name: "crc32c-pow2", Bucket: func(t Tuple, salt uint32, n int) int {
		return pow2Bucket(crc32c(t.encode(), salt), n)
	}},
}

// pow2Bucket 模拟「成员表填充到 2 的幂，再用低位掩码取下标」的落桶方式。
//
// 成员数不是 2 的幂时，多出来的槽位重复填前面的成员：n=6 填成 8 格，
// m0、m1 各占两格，负载是别人的两倍。覆盖率照样 100%，但流量从第一步就不均。
// 覆盖率和负载均衡是两件事，这张表把它分开。
func pow2Bucket(h uint32, members int) int {
	slots := 1
	for slots < members {
		slots <<= 1
	}
	idx := int(h & uint32(slots-1))
	if idx >= members {
		idx -= members
	}
	return idx
}

// hashNames 返回全部哈希函数名，供命令行提示与 README 对齐。
func hashNames() []string {
	names := make([]string, 0, len(hashFuncs))
	for _, h := range hashFuncs {
		names = append(names, h.Name)
	}
	return names
}

// HashByName 按名字取哈希函数。名字打错就报错，不静默退回默认值——
// 静默退回会让「换哈希重跑一遍」这件事失去意义。
func HashByName(name string) (HashFunc, error) {
	for _, h := range hashFuncs {
		if h.Name == name {
			return h, nil
		}
	}
	return HashFunc{}, fmt.Errorf("不认识的哈希函数 %q，可选：%s", name, strings.Join(hashNames(), " / "))
}

// LoadShare 统计 tuples 个随机源端口落到各 member 的比例。
//
// 这个数和覆盖率无关：四条 member 全被覆盖到（覆盖率 100%），负载仍可能
// 是 25/25/25/25 之外的任何东西。
func LoadShare(h HashFunc, set PathSet, tuples int, seed int64) []float64 {
	counts := make([]int, len(set.Members))
	rng := rand.New(rand.NewSource(seed))
	for i := 0; i < tuples; i++ {
		port := uint16(portMin + rng.Intn(portSpan))
		counts[h.Bucket(set.TupleWithPort(port), set.HashSalt, len(set.Members))]++
	}
	shares := make([]float64, len(counts))
	for i, c := range counts {
		shares[i] = float64(c) / float64(tuples)
	}
	return shares
}
