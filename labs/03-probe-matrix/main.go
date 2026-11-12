// 实验 03：探测矩阵与健康评分。
//
// 两件事：
//  1. 把一份拓扑翻成探测矩阵——谁探谁、跨过哪些链路、覆盖率多少、一年花多少钱
//  2. 读一轮探测结果，给每对打分，再把「探针侧异常」和「网络侧异常」分开
//
// 全流程纯本地计算，一个包都不发，不碰外网。
package main

import (
	"flag"
	"fmt"
	"os"
)

func main() {
	var (
		topoPath = flag.String("topology", "samples/topology.json", "拓扑样本（JSON）")
		incPath  = flag.String("incidents", "samples/incidents.json", "故障注入剧本（JSON）")
		outDir   = flag.String("out", "out", "输出目录，留空则不落盘")
		interval = flag.Int("interval", 0, "覆盖探测间隔（秒），0 表示用剧本里的 interval_s")
		limit    = flag.Int("limit", 10, "每一节里最多打印多少行")
		demo     = flag.Bool("demo", false, "额外打印归因汇总、节点判定与真值对账")
	)
	flag.Parse()

	topo, err := LoadTopology(*topoPath)
	if err != nil {
		fatal("读取拓扑失败：", err)
	}
	sc, err := LoadScenario(*incPath)
	if err != nil {
		fatal("读取剧本失败：", err)
	}
	if *interval > 0 {
		sc.IntervalS = *interval
	}

	matrix, err := GenerateMatrix(topo, sc.IntervalS)
	if err != nil {
		fatal("生成探测矩阵失败：", err)
	}

	results, truth, err := Simulate(topo, matrix, sc)
	if err != nil {
		fatal("仿真探测结果失败：", err)
	}

	expected := sc.WindowS / sc.IntervalS
	attribution := Classify(matrix, indexResults(results), expected)

	printMatrixReport(os.Stdout, topo, matrix)
	fmt.Fprintf(os.Stdout, "\n结果窗口：%s 起 %ds，间隔 %ds，每对 %d 个包\n",
		sc.WindowStart, sc.WindowS, sc.IntervalS, expected)
	printScoreTable(os.Stdout, attribution, *limit)

	if *demo {
		printAttribution(os.Stdout, attribution)
		printReconcile(os.Stdout, Evaluate(matrix, attribution, truth))
	}

	if *outDir == "" {
		return
	}
	paths, err := persist(*outDir, matrix, results, attribution)
	if err != nil {
		fatal("写输出失败：", err)
	}
	fmt.Fprintln(os.Stdout, "\n已写入：")
	for _, p := range paths {
		fmt.Fprintf(os.Stdout, "  %s\n", p)
	}
}

// indexResults 把结果列表按探测对建索引。同一个键出现两回就是数据源有问题，
// 但这里已经是从仿真出来的切片，重复只可能是代码 bug。
func indexResults(results []ProbeResult) map[string]ProbeResult {
	out := make(map[string]ProbeResult, len(results))
	for _, r := range results {
		out[pairKey(r.Src, r.Dst)] = r
	}
	return out
}

// persist 落三份文件：矩阵、探测结果、评分。
func persist(dir string, m *Matrix, results []ProbeResult, a Attribution) ([]string, error) {
	var paths []string
	matrixPath, err := writeMatrix(dir, m)
	if err != nil {
		return nil, err
	}
	paths = append(paths, matrixPath)

	resultsPath, err := writeResults(dir, results)
	if err != nil {
		return nil, err
	}
	paths = append(paths, resultsPath)

	scoresPath, err := writeScores(dir, a.Scores)
	if err != nil {
		return nil, err
	}
	paths = append(paths, scoresPath)
	return paths, nil
}

func fatal(msg string, err error) {
	fmt.Fprintln(os.Stderr, msg, err)
	os.Exit(1)
}
