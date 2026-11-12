package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// printMatrixReport 打印拓扑摘要、覆盖率与成本。
func printMatrixReport(w io.Writer, t *Topology, m *Matrix) {
	roles := map[string]int{}
	for _, n := range t.Nodes {
		roles[n.Role]++
	}
	fmt.Fprintf(w, "拓扑：%s，%d 个节点（border %d / spine %d / tor %d / server %d），%d 条链路\n",
		t.Site, len(t.Nodes), roles[roleBorder], roles[roleSpine], roles[roleTOR], roles[roleServer], len(t.Links()))
	fmt.Fprintf(w, "探测矩阵：%d 对（全量有序对 %d，省掉 %.1f%%）\n\n",
		len(m.Pairs), m.OrderedPairs, 100*(1-m.PairRate()))

	fmt.Fprintln(w, "覆盖率")
	fmt.Fprintf(w, "  节点对覆盖  %d / %d = %.1f%%\n", len(m.Pairs), m.OrderedPairs, m.PairRate()*100)
	fmt.Fprintf(w, "  链路覆盖    %d / %d = %.1f%%\n", m.LinkCovered, m.LinkTotal, m.LinkRate()*100)
	fmt.Fprintf(w, "  节点覆盖    %d / %d = %.1f%%\n", m.NodeCovered, m.NodeTotal, m.NodeRate()*100)
	if len(m.UncoveredLinks) == 0 {
		fmt.Fprintln(w, "  未覆盖链路  无")
	} else {
		fmt.Fprintf(w, "  未覆盖链路  %d 条：%s\n", len(m.UncoveredLinks), strings.Join(m.UncoveredLinks, "、"))
	}

	c := m.Cost
	fmt.Fprintf(w, "\n成本（每对每 %ds 一个包，包长 %d 字节；每 %ds 聚合一条记录）\n", c.IntervalS, c.PacketBytes, aggWindowS)
	fmt.Fprintf(w, "  包速率      %.0f pps = %d 对 ÷ %ds\n", c.Pps, c.Pairs, c.IntervalS)
	fmt.Fprintf(w, "  带宽        %.1f kbit/s，占拓扑总带宽 %.0f Gbps 的 %.2f ppm\n",
		c.KbitPerS, c.TotalBWGbps, c.BWSharePPM)
	fmt.Fprintf(w, "  聚合存储    %.0f 条/天 × %d 字节 = %.1f MB/天，30 天 %.2f GB\n",
		c.RecordsPerDay, aggRecordBytes, c.AggBytesPerDay/1e6, c.AggBytesPerDay*30/1e9)
	fmt.Fprintf(w, "  逐包原始    %.0f 包/天 × %d 字节 = %.1f GB/天，30 天 %.0f GB\n",
		c.Pps*86400, rawRecordBytes, c.RawBytesPerDay/1e9, c.RawBytesPerDay*30/1e9)
	fmt.Fprintf(w, "  全 mesh 对照 %d 对 → %.0f pps，是当前 %.1f 倍\n",
		m.OrderedPairs, float64(m.OrderedPairs)/float64(c.IntervalS), float64(m.OrderedPairs)/float64(c.Pairs))
}

// pairGroup 是同一结论聚在一起的一批对。一个节点关机影响 60 对，
// 它们的判据是同一句话，不该占 60 行。
type pairGroup struct {
	node string
	rows []PairScore
}

// printScoreTable 打印逐对结果。按结论分组，不按矩阵顺序平铺：
// 749 行平铺里 700 行「正常」，四类异常会被埋算；而 156 条主机不可达
// 逐条打出来，每一行的判据都是同一句话。分组之后，一屏能看完。
func printScoreTable(w io.Writer, a Attribution, limit int) {
	hostDown := make(map[string]bool, len(a.HostDown))
	for _, n := range a.HostDown {
		hostDown[n] = true
	}

	groups := make(map[string]*pairGroup)
	var network, degraded, healthy []PairScore

	addTo := func(verdict, node string, s PairScore) {
		k := verdict + "|" + node
		g, ok := groups[k]
		if !ok {
			g = &pairGroup{node: node}
			groups[k] = g
		}
		g.rows = append(g.rows, s)
	}

	for _, s := range a.Scores {
		switch {
		case hostDown[s.Src]:
			addTo(VerdictHostDown, s.Src, s)
		case hostDown[s.Dst]:
			addTo(VerdictHostDown, s.Dst, s)
		case s.Verdict == VerdictProbe:
			addTo(VerdictProbe, s.Src, s)
		case s.Verdict == VerdictTarget:
			addTo(VerdictTarget, s.Dst, s)
		case s.Verdict == VerdictNetwork:
			network = append(network, s)
		case s.Score >= 0 && s.Score < HealthOK:
			degraded = append(degraded, s)
		default:
			healthy = append(healthy, s)
		}
	}

	fmt.Fprintf(w, "\n探针结果：%d 对，异常 %d 对（network_side %d / host_down %d / probe_side %d / target_side %d）\n",
		len(a.Scores), len(a.Scores)-len(healthy)-len(degraded),
		len(network), countGroup(groups, VerdictHostDown), countGroup(groups, VerdictProbe), countGroup(groups, VerdictTarget))

	for _, v := range []string{VerdictHostDown, VerdictProbe, VerdictTarget} {
		keys := make([]string, 0, len(groups))
		for k := range groups {
			if strings.HasPrefix(k, v+"|") {
				keys = append(keys, k)
			}
		}
		sort.Strings(keys)
		for _, k := range keys {
			g := groups[k]
			fmt.Fprintf(w, "\n[%s] %s：%d 对\n    %s\n", v, g.node, len(g.rows), g.rows[0].Evidence)
		}
	}

	if len(network) > 0 {
		tail := ""
		if a.SharedLink != "" {
			tail = "，共同链路段 " + a.SharedLink
		}
		fmt.Fprintf(w, "\n[%s] %d 对%s\n", VerdictNetwork, len(network), tail)
		printScoreHeader(w)
		for i, s := range network {
			if i >= limit {
				fmt.Fprintf(w, "    …… 其余 %d 对跳过同一链路段，判据与上面一致\n", len(network)-i)
				break
			}
			printScoreRow(w, s)
		}
	}

	if len(degraded) > 0 {
		fmt.Fprintf(w, "\n[质量劣化] 没到失败线但分数低于 %d：%d 对\n", HealthOK, len(degraded))
		printScoreHeader(w)
		for i, s := range degraded {
			if i >= limit {
				fmt.Fprintf(w, "    …… 其余 %d 对未打印\n", len(degraded)-i)
				break
			}
			printScoreRow(w, s)
		}
	}

	fmt.Fprintf(w, "\n[正常] %d 对，列前 %d 条\n", len(healthy), limit)
	printScoreHeader(w)
	for i, s := range healthy {
		if i >= limit {
			fmt.Fprintf(w, "    …… 其余 %d 对未打印（-limit 控制）\n", len(healthy)-i)
			break
		}
		printScoreRow(w, s)
	}
}

// countGroup 数某一类结论影响的对数。
func countGroup(groups map[string]*pairGroup, verdict string) int {
	n := 0
	for k, g := range groups {
		if strings.HasPrefix(k, verdict+"|") {
			n += len(g.rows)
		}
	}
	return n
}

func printScoreHeader(w io.Writer) {
	fmt.Fprintf(w, "%-32s %-7s %-6s %-6s %-7s %-6s %-6s %-11s %s\n",
		"pair", "loss", "p99us", "base", "jitter", "cover", "score", "health", "判据")
}

func printScoreRow(w io.Writer, s PairScore) {
	score := "-"
	if s.Score >= 0 {
		score = fmt.Sprintf("%.1f", s.Score)
	}
	loss, p99, base, jit := "-", "-", "-", "-"
	if s.Sent > 0 {
		loss = fmt.Sprintf("%.2f%%", s.Loss*100)
	}
	if s.P99us > 0 {
		p99 = fmt.Sprintf("%d", s.P99us)
		base = fmt.Sprintf("%d", s.Baseus)
		jit = fmt.Sprintf("%d", s.Jitterus)
	}
	extra := s.Evidence
	if extra == "" {
		extra = "-"
	}
	if s.Verdict == VerdictNetwork {
		extra = fmt.Sprintf("%d 跳", s.Hops)
	}
	fmt.Fprintf(w, "%-32s %-7s %-6s %-6s %-7s %-6.0f %-6s %-11s %s\n",
		pairKey(s.Src, s.Dst), loss, p99, base, jit, s.Coverage, score, s.Health, extra)
}

// printAttribution 打印判定汇总：谁被判成了什么，共同链路在哪。
func printAttribution(w io.Writer, a Attribution) {
	counts := map[string]int{}
	for _, s := range a.Scores {
		counts[s.Verdict]++
	}
	fmt.Fprintln(w, "\n归因合计")
	for _, v := range []string{VerdictOK, VerdictNetwork, VerdictProbe, VerdictTarget, VerdictHostDown} {
		fmt.Fprintf(w, "  %-13s %-11s %d 对\n", v, verdictZH[v], counts[v])
	}

	fmt.Fprintln(w, "\n节点判定")
	printNodes := func(label string, names []string) {
		for _, n := range names {
			fmt.Fprintf(w, "  %-13s %-30s 行失败 %.1f%% / 列失败 %.1f%%\n",
				label, n, a.RowFail[n]*100, a.ColFail[n]*100)
		}
	}
	printNodes(VerdictHostDown, a.HostDown)
	printNodes(VerdictProbe, a.ProbeSide)
	printNodes(VerdictTarget, a.TargetSide)
	if len(a.HostDown)+len(a.ProbeSide)+len(a.TargetSide) == 0 {
		fmt.Fprintln(w, "  没有任何节点被判成异常")
	}

	if a.SharedLink != "" {
		share := 0.0
		if a.NetworkN > 0 {
			share = 100 * float64(a.SharedPairs) / float64(a.NetworkN)
		}
		fmt.Fprintf(w, "\n共同链路段：%s，出现在 %d / %d 条网络侧异常对的路径上（%.0f%%）\n",
			a.SharedLink, a.SharedPairs, a.NetworkN, share)
		fmt.Fprintln(w, "  这只是最大公约数，不是真相：抽样矩阵里两条不同的链路可能承载同一批对")
	}

	excl := "无"
	if a.Excluded > 0 {
		excl = fmt.Sprintf("%d 对（探针侧 + 主机侧）", a.Excluded)
	}
	fmt.Fprintf(w, "\n网络健康分 %.1f（%s），已剔除 %s\n",
		a.NetworkScore, healthLabel(a.NetworkScore), excl)
}

// printReconcile 打印与真值的对账。这一段是实验独有的，生产上没有。
func printReconcile(w io.Writer, rec Reconcile) {
	fmt.Fprintln(w, "\n与真相对照")
	fmt.Fprintf(w, "  主机不可达    命中 %d，误判 %d%s\n",
		len(rec.HostDownHit), len(rec.HostDownFalse), listSuffix(rec.HostDownFalse))
	fmt.Fprintf(w, "  探针侧异常    命中 %d，误判 %d%s\n",
		len(rec.ProbeHit), len(rec.ProbeFalse), listSuffix(rec.ProbeFalse))
	fmt.Fprintf(w, "  对端侧异常    命中 %d，误判 %d%s\n",
		len(rec.TargetHit), len(rec.TargetFalse), listSuffix(rec.TargetFalse))
	fmt.Fprintf(w, "  网络侧异常    真实受影响 %d 对，判成 network_side 且确实跨链路的 %d 对，漏判 %d，假阳性 %d\n",
		rec.LinkTruth(), rec.LinkHit, rec.LinkMissed, rec.LinkFalse)
}

// listSuffix 把误判清单拼成一句尾巴，空清单就不打。
func listSuffix(names []string) string {
	if len(names) == 0 {
		return ""
	}
	return "：" + strings.Join(names, "、")
}

// writeMatrix 把矩阵、覆盖率与成本落成 JSON，机器读这份。
func writeMatrix(dir string, m *Matrix) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, "matrix.json")
	f, err := os.Create(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(m); err != nil {
		return "", err
	}
	return path, nil
}

// writeResults 把探测结果矩阵落成 JSONL，一行一对。它是输入的回放件：
// 同一份矩阵喂给 LoadResults 能原样读回来。
func writeResults(dir string, results []ProbeResult) (string, error) {
	path := filepath.Join(dir, "results.jsonl")
	return path, writeJSONL(path, func(enc *json.Encoder) error {
		for _, r := range results {
			if err := enc.Encode(r); err != nil {
				return err
			}
		}
		return nil
	})
}

// writeScores 把评分与归因落成 JSONL。工单从这里开，表格只给人看。
func writeScores(dir string, scores []PairScore) (string, error) {
	path := filepath.Join(dir, "scores.jsonl")
	return path, writeJSONL(path, func(enc *json.Encoder) error {
		for _, s := range scores {
			if err := enc.Encode(s); err != nil {
				return err
			}
		}
		return nil
	})
}

// writeJSONL 开文件、建目录、把编码活交给调用方。一行一条是硬要求：
// 追加写不会撞上重写冲突，grep 和 awk 也只能处理这种格式。
func writeJSONL(path string, fill func(*json.Encoder) error) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return fill(json.NewEncoder(f))
}
