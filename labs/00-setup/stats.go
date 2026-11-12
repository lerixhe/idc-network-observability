package main

import (
	"fmt"
	"sort"
	"time"
)

// Percentile 取分位数（最近秩法）。p 取 0~100。
//
// 用一个函数、一份口径算所有分位数，是第二篇「指标」的伏笔：
// 同一个 p99 在不同实现下能差出几毫秒，口径不写清，两边就能吵起来。
func Percentile(samples []int64, p float64) int64 {
	if len(samples) == 0 {
		return 0
	}
	if p <= 0 {
		p = 0
	}
	if p >= 100 {
		p = 100
	}
	sorted := make([]int64, len(samples))
	copy(sorted, samples)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })

	// 最近秩：rank = ceil(p/100 * n)，最小为 1
	exact := float64(len(sorted)) * p / 100
	rank := int(exact)
	if float64(rank) < exact {
		rank++
	}
	if rank < 1 {
		rank = 1
	}
	return sorted[rank-1]
}

// LossRate 丢包率 = 失败次数 / 总次数，返回 0~1。
func LossRate(total, ok int) float64 {
	if total == 0 {
		return 0
	}
	if ok > total {
		ok = total
	}
	return float64(total-ok) / float64(total)
}

// stat 是单个目标的样本统计。
type stat struct {
	total int
	ok    int
	rtts  []int64
}

// collect 按目标把样本压实。
func collect(targets []Target, samples []Sample) map[string]*stat {
	m := make(map[string]*stat, len(targets))
	for _, t := range targets {
		m[t.Name] = &stat{}
	}
	for _, s := range samples {
		st, found := m[s.Target]
		if !found {
			continue
		}
		st.total++
		if s.OK {
			st.ok++
			st.rtts = append(st.rtts, s.RTTus)
		}
	}
	return m
}

// printReport 打印一行一目标的小结。平均值不出现——理由同 Percentile 注释。
func printReport(targets []Target, samples []Sample) {
	fmt.Printf("%-16s %-6s %-6s %-10s %-10s %s\n", "目标", "总数", "成功", "丢包率", "p50", "p99")
	stats := collect(targets, samples)
	for _, t := range targets {
		st := stats[t.Name]
		fmt.Printf("%-16s %-6d %-6d %-10s %-10s %s\n",
			t.Name,
			st.total,
			st.ok,
			fmt.Sprintf("%.2f%%", LossRate(st.total, st.ok)*100),
			time.Duration(Percentile(st.rtts, 50))*time.Microsecond,
			time.Duration(Percentile(st.rtts, 99))*time.Microsecond,
		)
	}
}
