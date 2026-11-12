// 实验 05：INT 逐跳数据还原。
//
// 端到端 RTT 涨 2 ms，是哪一跳涨的？INT 让每个节点往包头写入口/出口时间戳、
// 队列深度、端口，于是这个问题有答案——前提是每一跳都吐数据。
//
// 这个实验做三件事：
//
//  1. 时延分解：每跳驻留 = 出口时间戳 − 入口时间戳（同设备时钟，偏移抵消）
//  2. 排队归因：排队 ≈ 队列深度 × 8 ÷ 出口速率，剩下的算处理
//  3. 部分覆盖：只有一部分跳有数据时，输出能归因的部分，把差额圈进「盲区段」
//
// 全程离线：样本是录好的，不发包、不连设备。
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"
)

func main() {
	var (
		tracesPath = flag.String("traces", "samples/traces.json", "INT 元数据样本（JSON）")
		truthPath  = flag.String("truth", "samples/truth.json", "对账真值；留空则不打印对账")
		traceID    = flag.String("trace", "", "只跑指定 trace id；留空跑全部")
		outDir     = flag.String("out", "out", "产物目录，留空不落盘")
	)
	flag.Parse()

	if err := run(*tracesPath, *truthPath, *traceID, *outDir, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "失败:", err)
		os.Exit(1)
	}
}

func run(tracesPath, truthPath, traceID, outDir string, w io.Writer) error {
	traces, err := loadTraces(tracesPath)
	if err != nil {
		return err
	}
	if err := validateAll(traces, defaultClockAccuracy); err != nil {
		return err
	}
	if traceID != "" {
		kept := make([]Trace, 0, 1)
		for _, t := range traces {
			if t.ID == traceID {
				kept = append(kept, t)
			}
		}
		if len(kept) == 0 {
			return fmt.Errorf("样本里没有 id 为 %q 的 trace", traceID)
		}
		traces = kept
	}

	var truth map[string]TraceTruth
	if truthPath != "" {
		truth, err = loadTruth(truthPath)
		if err != nil {
			return err
		}
	}

	fmt.Printf("%d 条 trace，时钟精度口径：%s，光纤 %d ns/m\n\n",
		len(traces), clockPolicyText(defaultClockAccuracy), nsPerMeter)

	decomps := make([]Decomposition, 0, len(traces))
	for _, t := range traces {
		d, err := Decompose(t, defaultClockAccuracy)
		if err != nil {
			return err
		}
		decomps = append(decomps, d)

		printTraceHeader(w, d, t)
		printHopTable(w, d)
		printBreakdown(w, d, t)
		printSegments(w, d)
		printNaive(w, d)
		if truth != nil {
			tr, ok := truth[t.ID]
			if !ok {
				return fmt.Errorf("%s: 真值文件里没有这条 trace", t.ID)
			}
			printReconcile(w, d, tr)
		}
	}

	if outDir == "" {
		return nil
	}
	hopPath, summaryPath, err := writeOutputs(outDir, decomps)
	if err != nil {
		return err
	}
	hops := 0
	for _, d := range decomps {
		hops += d.HopCount
	}
	fmt.Printf("产物\n  %s  %d 行逐跳明细\n  %s  %d 条汇总\n", hopPath, hops, summaryPath, len(decomps))
	return nil
}

// clockPolicyText 把时钟精度口径打成一行，让输出自带口径说明。
func clockPolicyText(acc ClockAccuracyNs) string {
	names := make([]string, 0, len(acc))
	for name := range acc {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool { return acc[names[i]] < acc[names[j]] })
	parts := make([]string, 0, len(names))
	for _, name := range names {
		parts = append(parts, fmt.Sprintf("%s ±%s", name, time.Duration(acc[name])))
	}
	return strings.Join(parts, ", ")
}
