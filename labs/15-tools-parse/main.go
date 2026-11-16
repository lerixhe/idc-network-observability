// 实验 15：设备命令输出解析。
//
// 跑起来看的是两件事：三份不同厂商的计数器样本解成同一张表，
// 以及一份固件升级样本（列变多、出口单位从 bytes 改成 packets）解出来不崩、
// 单位变化被点名。mtr 那段给的是「中间跳丢包而终点正常」的判定。
package main

import (
	"flag"
	"fmt"
	"os"
)

func main() {
	var (
		sampleDir = flag.String("samples", "samples", "样本目录，目录里所有 .txt 都参与解析")
		demo      = flag.Bool("demo", false, "额外打印单位漂移、丢弃清单、派生换算")
		outDir    = flag.String("out", "out", "解析结果落盘目录（JSONL），留空则不落")
	)
	flag.Parse()

	ds, err := LoadDataset(*sampleDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "解析失败:", err)
		os.Exit(1)
	}

	printHeader(os.Stdout, ds)
	printWarnings(os.Stdout, ds)
	printCounters(os.Stdout, ds)
	printMTR(os.Stdout, ds)
	if *demo {
		printDrift(os.Stdout, ds)
		printDropped(os.Stdout, ds)
		printDerived(os.Stdout, ds)
	}

	if *outDir == "" {
		return
	}
	path, err := writeJSONL(*outDir, ds)
	if err != nil {
		fmt.Fprintln(os.Stderr, "写结果失败:", err)
		os.Exit(1)
	}
	fmt.Printf("\n解析结果已写入 %s\n", path)
}
