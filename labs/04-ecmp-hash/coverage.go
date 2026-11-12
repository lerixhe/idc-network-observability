package main

import (
	"fmt"
	"math"
	"math/rand"
	"sort"
	"strings"
)

// Mode 是一种探测方式。三种方式的区别只有一个：源端口怎么选。
//
// 五元组的其余四项在探测里都是固定的（源 IP 是探针地址，目的 IP 是目标，
// 协议和目的端口由探测协议定），所以「换五元组」在实践中就等于「换源端口」。
type Mode string

const (
	// ModeFixed：每次探测用同一个源端口。绝大多数 ping / 手写脚本 / 固定配置的
	// 探针都是这一类，也是「ECMP 让探测说谎」的根源。
	ModeFixed Mode = "fixed"
	// ModeRandom：每次探测随机换源端口，等价于每次新建一条连接。
	ModeRandom Mode = "random"
	// ModeBudget：一次探测任务里按预算取 B 个等距源端口，扫一遍。
	ModeBudget Mode = "budget"
)

const (
	// fixedPort 是固定五元组用的源端口。取一个不会撞上常见客户端端口段的值。
	fixedPort uint16 = 40001
	// 可分配源端口范围。Linux 默认临时端口是 32768–60999，探测包想探多少
	// 探多少，不受内核临时端口池限制，所以这里用整个 1024–65535。
	portMin  = 1024
	portMax  = 65535
	portSpan = portMax - portMin + 1 // 64512
	// budgetStride 是预算模式的端口步长。4099 是质数，与 64512 互质，
	// 因此前 64512 个预算端口互不重复；同时它和 2 的幂不对齐，
	// 避免「端口号高低位固定、低位全 0」这种自欺欺人的均匀。
	budgetStride = 4099
)

// modeNames 是终端表格里的固定顺序。
var modeNames = []Mode{ModeFixed, ModeRandom, ModeBudget}

// ParseMode 校验命令行传入的探测方式。
func ParseMode(s string) (Mode, error) {
	for _, m := range modeNames {
		if string(m) == s {
			return m, nil
		}
	}
	return "", fmt.Errorf("不认识的探测方式 %q", s)
}

// budgetPort 返回第 k 个（从 0 数）预算端口的取值。
func budgetPort(k int) uint16 {
	return uint16(portMin + (k*budgetStride)%portSpan)
}

// probePort 返回第 k 个探测包（从 0 数）该用的源端口。
//
// 曲线、负载、故障可见性三张表都走这一个函数，保证它们算的是同一批包。
// 随机模式要求调用方按 k 递增顺序消费同一个 rng 流，否则三张表对不上。
func probePort(mode Mode, k int, rng *rand.Rand) uint16 {
	switch mode {
	case ModeRandom:
		return uint16(portMin + rng.Intn(portSpan))
	case ModeBudget:
		return budgetPort(k)
	default:
		return fixedPort
	}
}

// Point 是覆盖率曲线上的一个点：打了 Probes 个包之后，覆盖到几条 member。
type Point struct {
	Probes   int
	Covered  int
	Coverage float64
}

// Curve 返回累进覆盖率曲线，只取 checkpoints 里的那几个点。
//
// 累进是关键：曲线只升不降。降了说明代码在数「当前这一轮打到了几条」，
// 那个数没有意义——探测的目的就是把路径一条条点亮，点亮过的不会熄。
func Curve(set PathSet, h HashFunc, mode Mode, checkpoints []int, seed int64) ([]Point, error) {
	if len(checkpoints) == 0 {
		return nil, fmt.Errorf("checkpoints 不能为空")
	}
	for i := 1; i < len(checkpoints); i++ {
		if checkpoints[i] <= checkpoints[i-1] {
			return nil, fmt.Errorf("checkpoints 必须严格递增，第 %d 个是 %d", i+1, checkpoints[i])
		}
	}
	rng := rand.New(rand.NewSource(seed))
	seen := make(map[int]bool, len(set.Members))
	points := make([]Point, 0, len(checkpoints))
	next := 0
	for k := 0; k < checkpoints[len(checkpoints)-1]; k++ {
		bucket := h.Bucket(set.TupleWithPort(probePort(mode, k, rng)), set.HashSalt, len(set.Members))
		seen[bucket] = true
		if k+1 == checkpoints[next] {
			points = append(points, Point{
				Probes:   k + 1,
				Covered:  len(seen),
				Coverage: float64(len(seen)) / float64(len(set.Members)),
			})
			next++
			if next == len(checkpoints) {
				break
			}
		}
	}
	return points, nil
}

// Hit 是一条 member 在一轮探测里收到多少个包。
type Hit struct {
	Member string
	Index  int
	Probes int
	Share  float64
}

// Hits 统计 probes 个探测包分别落在哪条 member 上。
func Hits(set PathSet, h HashFunc, mode Mode, probes int, seed int64) []Hit {
	counts := make([]int, len(set.Members))
	rng := rand.New(rand.NewSource(seed))
	for k := 0; k < probes; k++ {
		bucket := h.Bucket(set.TupleWithPort(probePort(mode, k, rng)), set.HashSalt, len(set.Members))
		counts[bucket]++
	}
	hits := make([]Hit, len(set.Members))
	for i, m := range set.Members {
		hits[i] = Hit{
			Member: m.ID,
			Index:  i,
			Probes: counts[i],
			Share:  float64(counts[i]) / float64(probes),
		}
	}
	return hits
}

// BlindSpot 是一条 member 坏掉之后，探测数据能不能看出来。
//
// ObservedLoss 是探测侧观测到的丢包率。0 就是「完全看不见」：面板全绿、
// 告警不响，而这条 member 承担的流量全丢。
type BlindSpot struct {
	Broken       string
	BrokenIndex  int
	ObservedLoss float64
	Invisible    bool
}

// BlindSpots 依次假定每条 member 坏掉，算探测侧能看到多少丢包。
func BlindSpots(set PathSet, h HashFunc, mode Mode, probes int, seed int64) []BlindSpot {
	hits := Hits(set, h, mode, probes, seed)
	spots := make([]BlindSpot, 0, len(hits))
	for _, hit := range hits {
		spots = append(spots, BlindSpot{
			Broken:       hit.Member,
			BrokenIndex:  hit.Index,
			ObservedLoss: hit.Share,
			Invisible:    hit.Share == 0,
		})
	}
	return spots
}

// ExpectedProbes 返回覆盖全部 n 条 member 的期望探测次数（等概率落桶、有放回）：
//
//	E[T] = n · H_n = n · (1 + 1/2 + 1/3 + … + 1/n)
//
// n=4 时 = 4 × 2.08333 = 8.333；n=8 时 = 8 × 2.71786 = 21.743。
// 这是随机源端口的下界，也是「4 条 member 打 4 个包」这种做法的失败原因。
func ExpectedProbes(n int) float64 {
	if n <= 0 {
		return 0
	}
	h := 0.0
	for i := 1; i <= n; i++ {
		h += 1 / float64(i)
	}
	return float64(n) * h
}

// CoverStats 是「覆盖全部 member 需要多少次探测」的分布。
//
// 固定五元组永远不会覆盖满（Unfinished == Trials），这时 P50/P90/P99 打不出来。
type CoverStats struct {
	Trials     int
	Unfinished int
	P50        int
	P90        int
	P99        int
}

// maxProbesPerTrial 是单次试验的探测上限。超过它就算「这一轮没覆盖满」，
// 不能当成无限大——那个数会污染分位数。
const maxProbesPerTrial = 20000

// ProbesToCover 用 trials 次独立试验估计「覆盖全部 member」所需探测次数的分位数。
//
// budget 模式是确定性的：每次试验的端口序列完全一样，三个分位数会相等——
// 这不是 bug，是「预算固定就没有运气成分」的意思。
func ProbesToCover(set PathSet, h HashFunc, mode Mode, trials int, seed int64) CoverStats {
	need := make([]int, 0, trials)
	unfinished := 0
	for t := 0; t < trials; t++ {
		rng := rand.New(rand.NewSource(seed + int64(t)))
		seen := make(map[int]bool, len(set.Members))
		k := 0
		for ; k < maxProbesPerTrial; k++ {
			bucket := h.Bucket(set.TupleWithPort(probePort(mode, k, rng)), set.HashSalt, len(set.Members))
			seen[bucket] = true
			if len(seen) == len(set.Members) {
				k++
				break
			}
		}
		if len(seen) < len(set.Members) {
			unfinished++
			continue
		}
		need = append(need, k)
	}
	sort.Ints(need)
	stats := CoverStats{Trials: trials, Unfinished: unfinished}
	if len(need) == 0 {
		return stats
	}
	stats.P50 = percentile(need, 0.50)
	stats.P90 = percentile(need, 0.90)
	stats.P99 = percentile(need, 0.99)
	return stats
}

// percentile 取最近秩分位数。输入必须已排序。
func percentile(sorted []int, p float64) int {
	if len(sorted) == 0 {
		return 0
	}
	rank := int(math.Ceil(p * float64(len(sorted))))
	if rank < 1 {
		rank = 1
	}
	if rank > len(sorted) {
		rank = len(sorted)
	}
	return sorted[rank-1]
}

// BlindMembers 返回一轮探测里一个包都没收到的 member，用来给「盲区」列填值。
func BlindMembers(hits []Hit) []string {
	var blind []string
	for _, hit := range hits {
		if hit.Probes == 0 {
			blind = append(blind, hit.Member)
		}
	}
	return blind
}

// joinMembers 把 member 列表拼成一行，空列表返回 "-"。
func joinMembers(ids []string) string {
	if len(ids) == 0 {
		return "-"
	}
	return strings.Join(ids, ",")
}
