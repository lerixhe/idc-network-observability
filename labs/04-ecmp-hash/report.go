package main

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
)

// printSetHeader 打印一组等价路径的身份。哈希盐用十六进制，和设备的配置对齐。
func printSetHeader(w io.Writer, set PathSet, h HashFunc) {
	fmt.Fprintf(w, "== %s · 路由 %s · %d 条 member · 哈希盐 0x%08x · %s\n",
		set.Name, set.Route, len(set.Members), set.HashSalt, h.Name)
	fmt.Fprintf(w, "   固定四元组：%s → %s/%d（proto %d），可变字段只有源端口\n",
		set.Base.SrcIP, set.Base.DstIP, set.Base.DstPort, set.Base.Proto)
	for i, m := range set.Members {
		fmt.Fprintf(w, "   [%d] %-3s %s\n", i, m.ID, m.Desc)
	}
	fmt.Fprintln(w)
}

// printCurveTable 打印覆盖率曲线在若干个探测次数上的取值。
//
// 列全用 ASCII，中文只放表后的说明行——终端里对齐不会被宽字符带偏。
func printCurveTable(w io.Writer, set PathSet, h HashFunc, checkpoints []int, seed int64) error {
	fmt.Fprintln(w, "覆盖率（累计探测次数 → 覆盖到的 member）")
	fmt.Fprintf(w, "%-8s %8s %10s %9s\n", "mode", "probes", "covered", "coverage")
	for _, mode := range modeNames {
		points, err := Curve(set, h, mode, checkpoints, seed)
		if err != nil {
			return err
		}
		for _, p := range points {
			fmt.Fprintf(w, "%-8s %8d %10s %8.1f%%\n",
				mode, p.Probes,
				fmt.Sprintf("%d/%d", p.Covered, len(set.Members)),
				p.Coverage*100)
		}
	}
	fmt.Fprintln(w)
	return nil
}

// printBudgetTable 打印「预算多少端口 → 覆盖到几条 member」。
//
// 这张表是能直接拿去定预算的：要求 100% 覆盖，就照最后一行那个数配。
func printBudgetTable(w io.Writer, set PathSet, h HashFunc, budgets []int, seed int64) {
	fmt.Fprintln(w, "预算 → 覆盖（budget 是这一轮允许打几个包）")
	fmt.Fprintf(w, "%-8s %10s %9s %s\n", "budget", "covered", "coverage", "blind")
	for _, b := range budgets {
		hits := Hits(set, h, ModeBudget, b, seed)
		covered := len(set.Members) - len(BlindMembers(hits))
		fmt.Fprintf(w, "%-8d %10s %8.1f%% %s\n",
			b,
			fmt.Sprintf("%d/%d", covered, len(set.Members)),
			float64(covered)/float64(len(set.Members))*100,
			joinMembers(BlindMembers(hits)))
	}
	fmt.Fprintln(w)
}

// printBlindTable 是「一条 member 坏了，探测能不能看出来」。
//
// 一列一个探测方式，最后一列是结论。fixed 列里那些 0.0% 的行就是这个实验
// 要说的事：坏了，而且完全看不见。
func printBlindTable(w io.Writer, set PathSet, h HashFunc, probes int, seed int64) {
	// 先把每个探测方式的可见性算完，再按 member 打行。
	spots := make(map[Mode][]BlindSpot, len(modeNames))
	for _, mode := range modeNames {
		spots[mode] = BlindSpots(set, h, mode, probes, seed)
	}
	fmt.Fprintln(w, "故障可见性（假定某一条 member 坏掉，探测侧观测到的丢包率）")
	fmt.Fprintf(w, "%-6s", "broken")
	for _, mode := range modeNames {
		fmt.Fprintf(w, " %9s", mode)
	}
	fmt.Fprintln(w)
	fixedBlind := 0
	for i, m := range set.Members {
		fmt.Fprintf(w, "%-6s", m.ID)
		for _, mode := range modeNames {
			fmt.Fprintf(w, " %8.1f%%", spots[mode][i].ObservedLoss*100)
		}
		fmt.Fprintln(w)
		if spots[ModeFixed][i].Invisible {
			fixedBlind++
		}
	}
	fmt.Fprintf(w, "每列 %d 个探测包。fixed 列里 %d/%d 条是 0.0%%：它们坏了，固定五元组永远发现不了\n",
		probes, fixedBlind, len(set.Members))
	fmt.Fprintf(w, "random、budget 两列不是 0，说明问题不在「测不了」，在「没换五元组」\n\n")
}

// printLoadTable 打印各 member 的负载占比。覆盖率和这个数是两件事。
func printLoadTable(w io.Writer, set PathSet, shares []float64) {
	fmt.Fprintln(w, "member 负载（65536 个随机源端口的落桶分布）")
	fmt.Fprintf(w, "%-6s %8s\n", "member", "share")
	for i, s := range shares {
		fmt.Fprintf(w, "%-6s %7.1f%%\n", set.Members[i].ID, s*100)
	}
	ideal := 100.0 / float64(len(set.Members))
	fmt.Fprintf(w, "理想均分是 %.1f%%；偏离它就是某条 member 在替别人扛流量\n\n", ideal)
}

// printCoverStats 打印「覆盖满需要多少次探测」的分布与理论值。
func printCoverStats(w io.Writer, set PathSet, stats map[Mode]CoverStats) {
	n := len(set.Members)
	fmt.Fprintf(w, "覆盖满需要多少次探测（%d 次试验；理论期望 n·H_n = %d × %.4f = %.1f）\n",
		stats[ModeRandom].Trials, n, harmonic(n), ExpectedProbes(n))
	fmt.Fprintf(w, "%-8s %8s %8s %8s %12s\n", "mode", "p50", "p90", "p99", "unfinished")
	for _, mode := range modeNames {
		s := stats[mode]
		if s.Unfinished == s.Trials {
			fmt.Fprintf(w, "%-8s %8s %8s %8s %12s\n", mode, "-", "-", "-",
				fmt.Sprintf("%d/%d", s.Unfinished, s.Trials))
			continue
		}
		fmt.Fprintf(w, "%-8s %8d %8d %8d %12s\n", mode, s.P50, s.P90, s.P99,
			fmt.Sprintf("%d/%d", s.Unfinished, s.Trials))
	}
	fmt.Fprintln(w)
}

// harmonic 返回 H_n，给上面那行公式凑数用。
func harmonic(n int) float64 {
	h := 0.0
	for i := 1; i <= n; i++ {
		h += 1 / float64(i)
	}
	return h
}

// printNarrative 打印那段演示：一条 member 坏了，覆盖率 25%，面板全绿。
//
// 里面的每个数字都从真实计算里取，没有一个写死——换种子、换哈希函数、
// 换 member 数，这段话跟着变。
func printNarrative(w io.Writer, set PathSet, h HashFunc, brokenIdx, probes int, seed int64) {
	broken := set.Members[brokenIdx]
	fixedHits := Hits(set, h, ModeFixed, probes, seed)
	covered := len(set.Members) - len(BlindMembers(fixedHits))
	coverage := float64(covered) / float64(len(set.Members))
	loss := BlindSpots(set, h, ModeFixed, probes, seed)[brokenIdx].ObservedLoss
	landOn := fixedMemberID(set, h)
	trafficShare := LoadShare(h, set, 65536, seed)[brokenIdx]

	fmt.Fprintln(w, "== 一条 member 坏了，覆盖率不够，你什么都没看见")
	fmt.Fprintf(w, "固定五元组：源端口 %d，%s 落桶永远落在 %s\n", fixedPort, h.Name, landOn)
	fmt.Fprintf(w, "覆盖率 %d/%d = %.1f%%，一条 member 几乎吃掉了全部探测包\n",
		covered, len(set.Members), coverage*100)
	fmt.Fprintf(w, "坏掉的是 %s：%s\n", broken.ID, broken.Desc)
	fmt.Fprintf(w, "探测侧观测丢包率 %.1f%%（%d 个包一个都没落到它上面）\n", loss*100, probes)
	fmt.Fprintf(w, "真实代价：这条路由 %.1f%% 的流量走 %s，全丢\n", trafficShare*100, broken.ID)
	fmt.Fprintln(w, "设备侧有 link down 事件，路由还在、BGP 邻居还在；告警只按「探测丢包率」触发，它不会响。")
	fmt.Fprintln(w, "这就是第 12 课那句：你以为在测一条链路，其实在测一条哈希路径。")
	fmt.Fprintln(w)
}

// fixedMemberID 返回固定五元组落到的 member ID，给演示文案用。
func fixedMemberID(set PathSet, h HashFunc) string {
	idx := h.Bucket(set.TupleWithPort(fixedPort), set.HashSalt, len(set.Members))
	return set.Members[idx].ID
}

// coverageRow 是一行覆盖率曲线数据，进 CSV。
type coverageRow struct {
	Set      string  `json:"path_set"`
	Mode     string  `json:"mode"`
	Probes   int     `json:"probes"`
	Covered  int     `json:"covered"`
	Total    int     `json:"total"`
	Coverage float64 `json:"coverage"`
}

// blindRow 是故障可见性表的一行。
type blindRow struct {
	Set          string  `json:"path_set"`
	Mode         string  `json:"mode"`
	Probes       int     `json:"probes"`
	Broken       string  `json:"broken"`
	ObservedLoss float64 `json:"observed_loss"`
	Invisible    bool    `json:"invisible"`
}

// writeCoverageCSV 把覆盖率曲线落成 CSV：画图就吃这一份。
func writeCoverageCSV(dir string, rows []coverageRow) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, "coverage.csv")
	f, err := os.Create(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	cw := csv.NewWriter(f)
	if err := cw.Write([]string{"path_set", "mode", "probes", "covered", "total", "coverage"}); err != nil {
		return "", err
	}
	for _, r := range rows {
		if err := cw.Write([]string{
			r.Set, r.Mode, strconv.Itoa(r.Probes), strconv.Itoa(r.Covered),
			strconv.Itoa(r.Total), strconv.FormatFloat(r.Coverage, 'f', 4, 64),
		}); err != nil {
			return "", err
		}
	}
	cw.Flush()
	if err := cw.Error(); err != nil {
		return "", err
	}
	return path, nil
}

// writeBlindSpots 把故障可见性落成 JSONL：一行一个（路径组，探测方式，坏掉的 member）。
func writeBlindSpots(dir string, rows []blindRow) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, "blindspots.jsonl")
	f, err := os.Create(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	for _, r := range rows {
		if err := enc.Encode(r); err != nil {
			return "", err
		}
	}
	return path, nil
}
