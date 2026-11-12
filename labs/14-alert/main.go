// 实验 14：告警收敛与父子抑制。
//
// 一次 Spine 板卡重启会炸出几百条告警。这一课的全部工作是把它们收敛成
// 三件事：哪条是根因、哪些是它的症状、哪些看着像其实是另一件事。
//
//	抑制     父压制子、不反向；父消失后子必须能回来
//	退化     拓扑里查不到父时，靠时间窗关联，代价是误伤
//	规则     阈值 / 持续时长 / 变化率三类规则在同一批数据上的命中对比
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"time"
)

func main() {
	var (
		mode = flag.String("mode", "all", "跑哪几段：all / suppress / degrade / rules")

		treePath      = flag.String("tree", "samples/tree.json", "对象树（拓扑/父子关系）")
		alertsPath    = flag.String("alerts", "samples/alerts.jsonl", "主剧本：一次 Spine 板卡重启的告警流")
		sparsePath    = flag.String("sparse", "samples/alerts-sparse.jsonl", "血缘缺失剧本：对象不在拓扑里")
		overreachPath = flag.String("overreach", "samples/alerts-overreach.jsonl", "反例剧本：抑制太狠，把真父告警也压了")
		metricsPath   = flag.String("metrics", "samples/metrics.jsonl", "时间序列（带真故障窗），给三类规则对比用")

		lag         = flag.Duration("suppress-lag", time.Duration(defaultSuppressPolicy.LagMs)*time.Millisecond, "父告警必须比子告警早出现多久")
		degradeWin  = flag.Duration("degrade-window", time.Duration(defaultSuppressPolicy.DegradeWindowMs)*time.Millisecond, "血缘缺失时，往回找疑似父的时间窗")
		resolveGrac = flag.Duration("resolve-grace", time.Duration(defaultSuppressPolicy.ResolveGraceMs)*time.Millisecond, "父消失后子告警也恢复的宽限，窗内不补发")
		explain     = flag.Bool("explain-required", defaultSuppressPolicy.ExplainRequired, "父告警的类型必须能解释子告警的类型")

		threshold = flag.Float64("threshold", defaultRulePolicy.Threshold, "阈值规则的过线值")
		sustain   = flag.Duration("sustain", time.Duration(defaultRulePolicy.SustainForS)*time.Second, "持续时长规则的时长下限")
		rateLimit = flag.Float64("rate", defaultRulePolicy.RateLimit, "变化率规则的斜率上限（单位/秒）")
		maxGap    = flag.Duration("max-gap", time.Duration(defaultRulePolicy.MaxGapS)*time.Second, "相邻采样点的最大合法间隔，超了说明采集中断")

		outDir = flag.String("out", "out", "结果输出目录，留空则不落盘")
	)
	flag.Parse()

	switch *mode {
	case "all", "suppress", "degrade", "rules":
	default:
		fmt.Fprintf(os.Stderr, "不认识的 -mode %q（all / suppress / degrade / rules）\n", *mode)
		os.Exit(2)
	}

	policy := SuppressPolicy{
		LagMs:           lag.Milliseconds(),
		DegradeWindowMs: degradeWin.Milliseconds(),
		ResolveGraceMs:  resolveGrac.Milliseconds(),
		ExplainRequired: *explain,
	}
	rulePolicy := RulePolicy{
		Threshold:   *threshold,
		SustainForS: int64(sustain.Seconds()),
		RateLimit:   *rateLimit,
		GraceS:      defaultRulePolicy.GraceS,
		MaxGapS:     int64(maxGap.Seconds()),
	}

	tree, err := loadTree(*treePath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "读取对象树失败:", err)
		os.Exit(1)
	}

	if *mode == "all" || *mode == "suppress" {
		if _, err := runSuppress(tree, *alertsPath, policy, *outDir,
			"=== 一、父子抑制：一次 Spine 板卡重启的重放 ===",
			"一次板卡重启下有 87 条告警冒出来，树上一看只有一条是根因。", "suppressed.jsonl"); err != nil {
			fmt.Fprintln(os.Stderr, "父子抑制失败:", err)
			os.Exit(1)
		}
	}

	if *mode == "all" || *mode == "degrade" {
		if _, err := runSuppress(tree, *sparsePath, policy, *outDir,
			"=== 二、血缘缺失：拓扑里查不到父，只能靠时间窗 ===",
			"同一个站点里两台设备都在红，代码没有血缘可用，只能按时间先后猜谁是谁的父。",
			"sparse.jsonl"); err != nil {
			fmt.Fprintln(os.Stderr, "退化路径失败:", err)
			os.Exit(1)
		}
	}

	if *mode == "all" || *mode == "degrade" {
		if _, err := runOverreach(tree, *overreachPath, policy, *outDir); err != nil {
			fmt.Fprintln(os.Stderr, "反例剧本失败:", err)
			os.Exit(1)
		}
	}

	if *mode == "all" || *mode == "rules" {
		if err := runRules(*metricsPath, rulePolicy, *outDir); err != nil {
			fmt.Fprintln(os.Stderr, "规则对比失败:", err)
			os.Exit(1)
		}
	}
}

// runSuppress 跑一份告警流，打印决策表与收敛账，落盘 JSONL。
func runSuppress(tree *ObjectTree, path string, policy SuppressPolicy, outDir, title, lede, outName string) (SuppressResult, error) {
	alerts, err := loadAlerts(path)
	if err != nil {
		return SuppressResult{}, err
	}
	res := Suppress(tree, alerts, policy)

	fmt.Printf("\n%s\n%s\n\n", title, lede)
	printOutcomes(os.Stdout, sortedOutcomes(res.Outcomes))
	printSuppressSummary(os.Stdout, "收敛账：", res, policy)
	printPellet(os.Stdout, res)
	printCollateral(os.Stdout, res)
	printOrphans(os.Stdout, res)

	if outDir == "" {
		return res, nil
	}
	p, err := writeJSONL(outDir, outName, outcomesAsAny(res.Outcomes))
	if err != nil {
		return res, err
	}
	fmt.Printf("\n抑制结果已写入 %s\n", p)
	return res, nil
}

// runOverreach 跑反例剧本：同一份数据，三套口径。
//
// 中间那档只拆掉时间闸，右边那档连类型的闸一起拆。右边把真根因也压掉了，
// 收敛比反而最漂亮。这一节存在的意义就是把那个漂亮数字的代价摆出来：
// 真正拦住它的是「父的类型必须能解释子」——时间闸在这一档上没起作用。
func runOverreach(tree *ObjectTree, path string, policy SuppressPolicy, outDir string) (SuppressResult, error) {
	alerts, err := loadAlerts(path)
	if err != nil {
		return SuppressResult{}, err
	}

	fmt.Printf("\n=== 三、反例：抑制太狠，把真父告警也压了 ===\n")
	fmt.Printf("剧本：site:room3 的出口拥塞比 spine-01 的板卡重启早 17 秒冒出来，拓扑上它是祖先。\n")
	fmt.Printf("三条口径跑同一份告警，差别只在拆掉哪道闸：\n\n")

	type run struct {
		name string
		res  SuppressResult
		gate string
	}
	runs := []run{
		{"严格（默认）", Suppress(tree, alerts, policy), "时间闸 + 类型闸都开"},
		{"只关时间闸", Suppress(tree, alerts, SuppressPolicy{
			LagMs: 0, DegradeWindowMs: policy.DegradeWindowMs,
			ResolveGraceMs: policy.ResolveGraceMs, ExplainRequired: true,
		}), "lag=0，仍查类型"},
		{"全贪心", Suppress(tree, alerts, SuppressPolicy{
			LagMs: 0, DegradeWindowMs: policy.DegradeWindowMs * 2,
			ResolveGraceMs: policy.ResolveGraceMs, ExplainRequired: false,
		}), "lag=0，祖先即压"},
	}

	fmt.Printf("%-16s %-8s %-6s %-6s %-28s %s\n", "口径", "投递", "收敛比", "误伤", "口径说明", "被压掉的真根因")
	for _, r := range runs {
		fmt.Printf("%-16s %-8d %-6.2f %-6d %-28s %s\n",
			r.name, r.res.Emitted, r.res.Ratio(), r.res.Collateral, r.gate, orDash(collateralIDs(r.res)))
	}

	strict, loose := runs[0].res, runs[2].res
	fmt.Printf("\n全贪心多压掉 %d 条，收敛比从 %.2f 涨到 %.2f，代价是丢了 %d 条本该自己报的告警。\n",
		strict.Emitted-loose.Emitted, strict.Ratio(), loose.Ratio(), loose.Collateral)
	fmt.Printf("只关时间闸那一档和严格口径一样。真正拦住它的是「父的类型必须能解释子」，时间闸在这一档上没起作用。\n")

	fmt.Printf("\n严格口径下的处置明细：\n")
	printOutcomes(os.Stdout, sortedOutcomes(strict.Outcomes))
	printCollateral(os.Stdout, strict)

	diff := diffOutcomes(strict.Outcomes, loose.Outcomes)
	if len(diff) > 0 {
		fmt.Printf("\n全贪心压掉、严格放行的 %d 条：\n", len(diff))
		for _, d := range diff {
			fmt.Printf("  %-8s %-30s truth=%-11s 贪心口径里 ← %s\n",
				d.ID, truncate(d.Obj, 30), d.Truth, d.SuppressedBy)
		}
	}

	if outDir != "" {
		if _, err := writeJSONL(outDir, "overreach-greedy.jsonl", outcomesAsAny(loose.Outcomes)); err != nil {
			return strict, err
		}
	}
	return strict, nil
}

// diffOutcomes 找「b 压了、a 没压」的那批。
func diffOutcomes(a, b []Outcome) []Outcome {
	relaxed := make(map[string]bool, len(a))
	for _, o := range a {
		if !o.Suppressed {
			relaxed[o.ID] = true
		}
	}
	var out []Outcome
	for _, o := range b {
		if o.Suppressed && relaxed[o.ID] {
			out = append(out, o)
		}
	}
	return out
}

// runRules 跑三类规则对比。
func runRules(path string, policy RulePolicy, outDir string) error {
	series, err := loadSeries(path)
	if err != nil {
		return err
	}
	rep := Evaluate(series, policy)

	fmt.Printf("\n=== 四、三类规则：阈值 / 持续时长 / 变化率 ===\n")
	fmt.Printf("阈值 %.0f；持续时长 %ds；变化率 %.2f/秒；宽限 %ds；跨断点（>%ds）不算斜率\n\n",
		policy.Threshold, policy.SustainForS, policy.RateLimit, policy.GraceS, policy.MaxGapS)
	printRules(os.Stdout, rep)

	if outDir == "" {
		return nil
	}
	p, err := writeJSONL(outDir, "rule-scores.jsonl", scoresAsAny(rep.Scores))
	if err != nil {
		return err
	}
	fmt.Printf("\n规则评分已写入 %s\n", p)
	return nil
}

// collateralIDs 列出误伤的那几条 ID，按出现顺序。
func collateralIDs(res SuppressResult) []string {
	var out []string
	for _, o := range res.Outcomes {
		if o.Suppressed && o.Truth != TruthChild {
			out = append(out, o.ID+"/"+o.Truth)
		}
	}
	return out
}

func orDash(items []string) string {
	if len(items) == 0 {
		return "-"
	}
	return strings.Join(items, "、")
}
