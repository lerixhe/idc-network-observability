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

// printOutcomes 打印抑制决策表。
//
// 前面几列全是 ASCII，中文只在下面的说明段，终端对齐不会被宽字符带偏。
// result 一列就是全部答案：ROOT 是自己出去，`<- a-0002` 是被谁压住。
func printOutcomes(w io.Writer, list []Outcome) {
	fmt.Fprintf(w, "%-4s %-21s %-34s %-14s %-9s %s\n",
		"#", "ts", "object", "class", "sev", "result")
	for i, o := range list {
		obj := o.Node
		if obj == "" {
			obj = o.Obj + "(?)"
		}
		result := "ROOT"
		if o.Suppressed {
			result = "<- " + o.SuppressedBy
			if o.Degraded {
				result += " (窗)"
			}
		}
		fmt.Fprintf(w, "%-4d %-21s %-34s %-14s %-9s %s\n",
			i+1, o.TS, truncate(obj, 34), o.Class, o.Severity, result)
	}
}

// printSuppressSummary 打收敛账。这一节的所有数字都能自己对上：
// 原始 = 投递 + 净收敛，净收敛 = 压掉 - 补发。
func printSuppressSummary(w io.Writer, title string, res SuppressResult, policy SuppressPolicy) {
	fmt.Fprintf(w, "\n%s\n", title)
	fmt.Fprintf(w, "抑制口径：父须早于子 %s；退化窗口 %s；恢复宽限 %s；类型白名单 %s\n",
		dur(policy.LagMs), dur(policy.DegradeWindowMs), dur(policy.ResolveGraceMs),
		onOff(policy.ExplainRequired))
	fmt.Fprintf(w, "\n原始告警 %d 条 → 独立投递 %d 条：收敛比 %.2f:1，抑制率 %.1f%%\n",
		res.Alerts, res.Emitted, res.Ratio(), res.SuppressRatio()*100)
	fmt.Fprintf(w, "  压掉 %d 条 = 净收敛 %d（父消失后自行恢复，不补发）+ 补发 %d（父没了子还活着）\n",
		res.Suppressed, res.NetConverged, res.Resumed)
	fmt.Fprintf(w, "  血缘缺失 %d 条；其中走时间窗关联压掉 %d 条\n", res.Orphans, res.DegradedSuppressed)
	if res.TopSuppressor != "" {
		fmt.Fprintf(w, "  压得最多的那条：%s 压掉 %d 条——它是这次故障的根因，先点它\n",
			res.TopSuppressor, res.TopSuppressorCnt)
	}
	fmt.Fprintf(w, "  误伤 %d 条（被压的告警里真值不是 child 的条数）\n", res.Collateral)
}

// printPellet 逐条交代被压的原因。审计靠这一段，不靠数字。
//
// 终端只打前 12 条，剩下的全部写进 JSONL。一次板卡重启能压掉八十条，
// 全打在屏幕上没人看，那就等于没写。
func printPellet(w io.Writer, res SuppressResult) {
	suppressed := make([]Outcome, 0, res.Suppressed)
	for _, o := range res.Outcomes {
		if o.Suppressed {
			suppressed = append(suppressed, o)
		}
	}
	if len(suppressed) == 0 {
		return
	}

	// 按父告警分组，先把「谁压了谁」的账摆出来。
	type bucket struct {
		count, degraded, resumed int
		sample                   string
	}
	groups := map[string]*bucket{}
	var order []string
	for _, o := range suppressed {
		b, ok := groups[o.SuppressedBy]
		if !ok {
			b = &bucket{sample: o.Obj}
			groups[o.SuppressedBy] = b
			order = append(order, o.SuppressedBy)
		}
		b.count++
		if o.Degraded {
			b.degraded++
		}
		if o.ResumedAt != "" {
			b.resumed++
		}
	}
	sort.Strings(order)
	fmt.Fprintf(w, "\n被压的 %d 条，按父告警分组：\n", len(suppressed))
	for _, id := range order {
		b := groups[id]
		extra := ""
		if b.degraded > 0 {
			extra += fmt.Sprintf("，其中 %d 条靠时间窗", b.degraded)
		}
		if b.resumed > 0 {
			extra += fmt.Sprintf("，%d 条后来补发", b.resumed)
		}
		fmt.Fprintf(w, "  %-8s 压掉 %3d 条%s\n", id, b.count, extra)
	}

	show := 12
	if len(suppressed) < show {
		show = len(suppressed)
	}
	fmt.Fprintf(w, "\n前 %d 条明细（全量在 JSONL 里）：\n", show)
	for _, o := range suppressed[:show] {
		tag := "压"
		if o.Degraded {
			tag = "压(窗)"
		}
		fmt.Fprintf(w, "  [%s] %-8s %-32s ← %-8s %s\n",
			tag, o.ID, truncate(o.Obj, 32), o.SuppressedBy, o.Note)
	}
}

// printCollateral 单独把误伤拎出来。它总有，装看不见迟早出事。
func printCollateral(w io.Writer, res SuppressResult) {
	var bad []Outcome
	for _, o := range res.Outcomes {
		if o.Suppressed && o.Truth != TruthChild {
			bad = append(bad, o)
		}
	}
	if len(bad) == 0 {
		fmt.Fprintln(w, "\n误伤清单：空（样本里每条被压的告警真值都是 child）")
		return
	}
	fmt.Fprintf(w, "\n误伤清单 %d 条——这些本来就是该自己报出去的：\n", len(bad))
	for _, o := range bad {
		why := "真根因被压掉了"
		if o.Truth == TruthIndependent {
			why = "另一件独立故障被压掉了"
		}
		how := "拓扑"
		if o.Degraded {
			how = "时间窗"
		}
		fmt.Fprintf(w, "  [误伤] %-8s %-32s truth=%-11s 被 %s 靠%s压掉\n",
			o.ID, truncate(o.Obj, 32), o.Truth, o.SuppressedBy, how)
		fmt.Fprintf(w, "         %s。%s\n", why, o.Note)
	}
}

// printOrphans 交代血缘缺失的那批。它们没被压不代表没问题，
// 只代表这次没找到能解释它们的上游。
func printOrphans(w io.Writer, res SuppressResult) {
	type bucket struct{ unknown, noAnc, suppressed int }
	counts := map[string]*bucket{}
	var order []string
	for _, o := range res.Outcomes {
		if !o.Orphan {
			continue
		}
		if _, ok := counts[o.OrphanReason]; !ok {
			counts[o.OrphanReason] = &bucket{}
			order = append(order, o.OrphanReason)
		}
		b := counts[o.OrphanReason]
		if o.OrphanReason == DegradeObjectUnknown {
			b.unknown++
		} else {
			b.noAnc++
		}
		if o.Suppressed {
			b.suppressed++
		}
	}
	if len(order) == 0 {
		return
	}
	sort.Strings(order)
	fmt.Fprintf(w, "\n血缘缺失 %d 条，按原因：\n", res.Orphans)
	for _, r := range order {
		b := counts[r]
		fmt.Fprintf(w, "  %-15s %d 条（时间窗压掉 %d 条）\n", r, b.unknown+b.noAnc, b.suppressed)
	}
}

// printRules 打印三类规则的命中对比。
func printRules(w io.Writer, rep RuleReport) {
	fmt.Fprintf(w, "采样间隔 %ds（相邻点间隔的中位数）；序列 %d 条、采样点 %d 个、真故障窗 %d 段\n\n",
		rep.Interval, rep.Series, rep.Points, rep.Faults)
	fmt.Fprintf(w, "%-42s %-10s %-4s %-4s %-4s %-4s %s\n",
		"series", "rule", "hits", "on", "fa", "miss", "first")
	for _, sc := range rep.Scores {
		first := "-"
		if sc.FirstDelayS >= 0 {
			first = fmt.Sprintf("%ds", sc.FirstDelayS)
		}
		fmt.Fprintf(w, "%-42s %-10s %-4d %-4d %-4d %-4d %s\n",
			truncate(sc.Series, 42), sc.Rule, sc.Hits, sc.OnTime, sc.FalseAlarm, sc.Missed, first)
	}
	fmt.Fprintf(w, "\n按规则汇总（hits 命中段 / on 落在故障窗内 / fa 误报 / miss 漏掉的故障窗）：\n")
	fmt.Fprintf(w, "  %-10s %-6s %-6s %-6s %s\n", "rule", "hits", "on", "fa", "miss")
	for _, s := range rep.Summary {
		fmt.Fprintf(w, "  %-10s %-6d %-6d %-6d %d\n", s.Rule, s.Hits, s.OnTime, s.FalseAlarm, s.Missed)
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= 1 {
		return s[:n]
	}
	return s[:n-1] + "…"
}

func dur(ms int64) string {
	if ms == 0 {
		return "0"
	}
	if ms%1000 == 0 {
		return fmt.Sprintf("%ds", ms/1000)
	}
	return fmt.Sprintf("%.1fs", float64(ms)/1000)
}

func onOff(b bool) string {
	if b {
		return "开"
	}
	return "关"
}

// writeJSONL 落盘。一行一条，不缩进——这些文件是给下游读的，不是给人读的。
func writeJSONL(dir, name string, rows []any) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, name)
	f, err := os.Create(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	enc := json.NewEncoder(f)
	// 关掉 HTML 转义：路径里的 `>` 要原样留着，「room3 > spine-01」比
	// 「room3 \u003e spine-01」好用得多。
	enc.SetEscapeHTML(false)
	for _, r := range rows {
		if err := enc.Encode(r); err != nil {
			return "", err
		}
	}
	return path, nil
}

// outcomesAsAny 抹掉具体类型，喂给 writeJSONL。
func outcomesAsAny(list []Outcome) []any {
	out := make([]any, 0, len(list))
	for _, o := range list {
		out = append(out, o)
	}
	return out
}

func scoresAsAny(list []RuleScore) []any {
	out := make([]any, 0, len(list))
	for _, s := range list {
		out = append(out, s)
	}
	return out
}

// formatOutcomeLine 是给测试用的单行摘要，报告与测试看同一份口径。
func formatOutcomeLine(o Outcome) string {
	state := "root"
	if o.Suppressed {
		state = "suppressed<-" + o.SuppressedBy
		if o.Degraded {
			state += "(window)"
		}
	}
	if o.ResumedAt != "" {
		state += "@resume:" + o.ResumedAt
	}
	if o.RestoredGrace {
		state += "@grace"
	}
	return strings.Join([]string{o.ID, state}, " ")
}
