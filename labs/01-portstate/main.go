// 实验 01：端口状态与 down 根因判定。
//
// 喂进一份录好的 ifTable 快照和一段 Trap 序列，回答两个问题：
// 这个端口为什么 down，以及这条 down 该记在谁头上（变更 / 硬件 / 配置 / 故障）。
//
// 判定树覆盖六种成因：本端 admin shutdown、对端 admin shutdown、光模块不在位
// 或未识别、光功率异常、协商失败、单向链路。另外顺手识别设备重启造成的假抖动。
package main

import (
	"flag"
	"fmt"
	"os"
	"time"
)

func main() {
	var (
		portsPath  = flag.String("ports", "samples/portstate.json", "ifTable 快照（JSON 数组）")
		trapsPath  = flag.String("traps", "samples/traps.jsonl", "Trap 序列（JSONL）")
		outDir     = flag.String("out", "out", "判定结果输出目录，留空则不落盘")
		flapWindow = flag.Duration("flap-window", time.Duration(defaultFlapPolicy.WindowMs)*time.Millisecond, "抖动判定窗口 M")
		flapMin    = flag.Int("flap-min", defaultFlapPolicy.MinFlips, "窗口内状态翻转次数阈值 N")
		summary    = flag.Bool("summary", false, "额外打印归属汇总与观测辅助信息")
	)
	flag.Parse()

	ports, err := loadPorts(*portsPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "读取端口快照失败:", err)
		os.Exit(1)
	}
	traps, err := loadTraps(*trapsPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "读取 Trap 序列失败:", err)
		os.Exit(1)
	}

	policy := FlapPolicy{WindowMs: flapWindow.Milliseconds(), MinFlips: *flapMin}
	analysis, err := Decide(ports, traps, policy)
	if err != nil {
		fmt.Fprintln(os.Stderr, "判定失败:", err)
		os.Exit(1)
	}

	fmt.Printf("%d 个端口，%d 条 Trap，抖动口径：%s 内 %d 次翻转\n\n",
		len(ports), len(traps), flapWindow.String(), *flapMin)
	printTable(os.Stdout, analysis.Verdicts)
	if *summary {
		printSummary(os.Stdout, analysis)
	}

	if *outDir == "" {
		return
	}
	path, err := writeVerdicts(*outDir, analysis.Verdicts)
	if err != nil {
		fmt.Fprintln(os.Stderr, "写判定结果失败:", err)
		os.Exit(1)
	}
	fmt.Printf("\n判定结果已写入 %s\n", path)
}
