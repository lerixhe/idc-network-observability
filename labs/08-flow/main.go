// 实验 08：流数据解析与采样误差。
//
// 两件事，对应第 20、21 课：
//
//  1. 解析录好的样本：sFlow 的报文头样本（每 N 个包抽一个包头），
//     与 NetFlow/IPFIX 的流记录（模板 + 数据）。两者都出 TopN 与字节/包统计，
//     但口径不同：前者是「样本 × 采样率」的估算，后者是 exporter 的聚合计数。
//     模板缺失或过期时，这一段标成不可信——不猜字段、不拿垃圾数据凑数。
//  2. 采样误差模拟：给一份真值（一条大象 + 一窝老鼠），按 1:100 / 1:1000 / 1:10000
//     抽样，输出 TopN 失真、可见流条数，以及「最小可观测流速率」。
//
// 全程离线：样本是录好的 JSON，不发包、不连设备、零第三方依赖。
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"
)

func main() {
	var (
		sflowPath = flag.String("sflow", "samples/sflow.jsonl", "sFlow 报文头样本（JSONL，一行一个 datagram）")
		nfPath    = flag.String("netflow", "samples/netflow.jsonl", "NetFlow/IPFIX 样本（JSONL，一行一个 export）")
		truthPath = flag.String("truth", "samples/flows-truth.json", "采样误差模拟的真值")
		topN      = flag.Int("top", 5, "TopN 条数")
		ttl       = flag.Duration("template-ttl", defaultTemplateTTL, "模板寿命，超过它就当过期")
		ratesText = flag.String("rates", "100,1000,10000", "采样率列表，逗号分隔，1:N 里的 N")
		window    = flag.Float64("window", 0, "覆盖真值里的窗口长度（秒），0 表示用真值文件里的值")
		seed      = flag.Int64("seed", 42, "抽样随机种子，换个数值结论不变")
		mode      = flag.String("mode", "all", "all / flows / sampling")
		merge     = flag.Bool("merge", false, "额外打印两个源直接相加的 TopN（演示双计错误）")
		outDir    = flag.String("out", "out", "产物目录，留空不落盘")
	)
	flag.Parse()

	if err := run(*sflowPath, *nfPath, *truthPath, *topN, *ttl, *ratesText,
		*window, *seed, *mode, *merge, *outDir, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "失败:", err)
		os.Exit(1)
	}
}

func run(sflowPath, nfPath, truthPath string, topN int, ttl time.Duration,
	ratesText string, window float64, seed int64, mode string, merge bool,
	outDir string, w io.Writer) error {
	if topN <= 0 {
		return fmt.Errorf("-top 必须大于 0，实际 %d", topN)
	}
	if ttl <= 0 {
		return fmt.Errorf("-template-ttl 必须大于 0，实际 %s", ttl)
	}
	switch mode {
	case "all", "flows", "sampling":
	default:
		return fmt.Errorf("-mode 只能是 all / flows / sampling，实际 %q", mode)
	}

	var stats []SourceStat
	if mode == "all" || mode == "flows" {
		var err error
		stats, err = runFlows(w, sflowPath, nfPath, topN, ttl, merge)
		if err != nil {
			return err
		}
	}

	var sim Simulation
	if mode == "all" || mode == "sampling" {
		rates, err := parseRates(ratesText)
		if err != nil {
			return err
		}
		truth, err := loadTruth(truthPath)
		if err != nil {
			return err
		}
		if window > 0 {
			truth.WindowSeconds = window
		}
		sim, err = Simulate(truth, rates, topN, seed)
		if err != nil {
			return err
		}
		if mode == "all" {
			fmt.Fprint(w, "\n=== 二、采样误差：同一份真值，三个采样率 ===\n")
		} else {
			fmt.Fprintf(w, "%d 条真实流，窗口 %.0fs，包长 %.0f 字节\n",
				sim.Flows, sim.WindowSeconds, sim.MeanPacketBytes)
		}
		for _, r := range sim.Rates {
			printSampling(w, r)
		}
		printVisibility(w, sim)
	}

	if outDir == "" {
		return nil
	}
	fmt.Fprint(w, "\n产物\n")
	if len(stats) > 0 {
		topnPath, err := writeTopN(outDir, stats)
		if err != nil {
			return err
		}
		fmt.Fprintf(w, "  %s\n", topnPath)
	}
	if len(sim.Rates) > 0 {
		samplingPath, err := writeSampling(outDir, sim)
		if err != nil {
			return err
		}
		fmt.Fprintf(w, "  %s\n", samplingPath)
	}
	return nil
}

// runFlows 跑第一部分：解析两个源，出段账与 TopN。
func runFlows(w io.Writer, sflowPath, nfPath string, topN int, ttl time.Duration, merge bool) ([]SourceStat, error) {
	dgs, err := loadSFlow(sflowPath)
	if err != nil {
		return nil, err
	}
	exports, err := loadNetFlow(nfPath)
	if err != nil {
		return nil, err
	}

	if merge {
		fmt.Fprint(w, "=== 一、解析录好的流样本（两个源合并演示） ===\n")
	} else {
		fmt.Fprint(w, "=== 一、解析录好的流样本 ===\n")
	}

	sflowStat := AggregateSFlow(dgs, topN)
	printSFlowSummary(w, dgs, sflowStat)
	printSourceTable(w, sflowStat)

	res := DecodeExports(exports, ttl)
	nfStat := AggregateNetFlow(res, topN)
	fmt.Fprintln(w)
	printSegments(w, res, ttl)
	fmt.Fprintf(w, "  可信记录 %d / 不可信 %d，覆盖率 %s\n",
		nfStat.Units, nfStat.DroppedUnits, pct(nfStat.Coverage))
	for _, reason := range sortedReasons(nfStat.DropReasons) {
		fmt.Fprintf(w, "  丢弃 %d 条记录：%s（%s）\n",
			nfStat.DropReasons[reason], reason, reasonZH[reason])
	}
	fmt.Fprintf(w, "  合计 %s / %d 包（exporter 聚合值，可直接相加）\n",
		humanBytes(nfStat.Bytes), nfStat.Packets)
	printSourceTable(w, nfStat)

	stats := []SourceStat{sflowStat, nfStat}
	if merge {
		merged := MergeStats(sflowStat, nfStat, topN)
		fmt.Fprintf(w, "\n%s\n", merged.Label)
		printSourceTable(w, merged)
		fmt.Fprintf(w, "  两个源各自合计：sFlow %s / NetFlow %s，相加得 %s\n",
			humanBytes(sflowStat.Bytes), humanBytes(nfStat.Bytes), humanBytes(merged.Bytes))
		fmt.Fprint(w, "  合并不是合并口径：同一条流既被抽到过样本、又被聚合过记录，"+
			"相加就是把同一批包算了两遍；两条链路的流量也可能落在同一个五元组上\n")
		stats = append(stats, merged)
	}
	return stats, nil
}

// parseRates 解 -rates 参数。必须是正整数且严格递增：
// 重复或倒序的采样率放在一张表里，读的人会以为是笔误。
func parseRates(text string) ([]int, error) {
	fields := strings.Split(text, ",")
	rates := make([]int, 0, len(fields))
	prev := 0
	for _, f := range fields {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		n, err := strconv.Atoi(f)
		if err != nil {
			return nil, fmt.Errorf("-rates 里的 %q 不是整数", f)
		}
		if n <= 0 {
			return nil, fmt.Errorf("-rates 里的 %d 不合法，1:N 的 N 必须大于 0", n)
		}
		if n <= prev {
			return nil, fmt.Errorf("-rates 必须严格递增，%d 出现在 %d 之后", n, prev)
		}
		prev = n
		rates = append(rates, n)
	}
	if len(rates) == 0 {
		return nil, fmt.Errorf("-rates 是空的")
	}
	return rates, nil
}
