// 实验 09：脏台账对账。
//
// 喂进三份快照——台账、LLDP、ARP——回答三个问题：台账里哪些设备现网已经没了
// （僵尸）、现网哪些设备台账里没有（幽灵）、哪些链路两端说法不一致（错挂）。
// 每一条差异带置信度和纠正建议，最后汇总成一份「建议写入」的补丁文件。
//
// 补丁不会自动落库。默认 dry-run，加 -apply -confirm 也只是写到另一份副本上；
// 台账文件本身在任何路径下都不被改写，写库这一步留给人工评审和变更窗口。
package main

import (
	"bytes"
	"crypto/sha256"
	"flag"
	"fmt"
	"io"
	"os"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run 是全部流程，返回进程退出码。拆出来是为了让测试能跑完整两条路径：
// dry-run 一个字节都不写，apply 只写副本。
func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("09-cmdb", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var (
		ledgerPath = fs.String("ledger", "samples/cmdb.json", "台账快照（JSON）")
		lldpPath   = fs.String("lldp", "samples/lldp.json", "LLDP 快照（JSON）")
		arpPath    = fs.String("arp", "samples/arp.json", "ARP 快照（JSON）")
		outDir     = fs.String("out", "out", "差异单与补丁的落盘目录，留空则不落盘")
		apply      = fs.Bool("apply", false, "把补丁应用到台账副本，必须同时带 -confirm")
		confirm    = fs.Bool("confirm", false, "人工确认：确认差异单之后再打开")
		writePath  = fs.String("write", "out/cmdb.applied.json", "副本写入路径，不允许等于 -ledger")
	)
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 1
	}

	ledger, err := loadLedger(*ledgerPath)
	if err != nil {
		fmt.Fprintln(stderr, "读取台账失败:", err)
		return 1
	}
	lldp, err := loadLLDP(*lldpPath)
	if err != nil {
		fmt.Fprintln(stderr, "读取 LLDP 快照失败:", err)
		return 1
	}
	arp, err := loadARP(*arpPath)
	if err != nil {
		fmt.Fprintln(stderr, "读取 ARP 快照失败:", err)
		return 1
	}

	// 先存一份原始字节，收尾时比对：台账一个字都不该变。
	before, err := os.ReadFile(*ledgerPath)
	if err != nil {
		fmt.Fprintln(stderr, "读取台账字节失败:", err)
		return 1
	}

	report, err := Reconcile(ledger, lldp, arp)
	if err != nil {
		fmt.Fprintln(stderr, "对账失败:", err)
		return 1
	}

	printSummary(stdout, report, lldp.CollectedAt, arp.CollectedAt, arp.Gateway)

	dryRun := !(*apply && *confirm)
	printPatches(stdout, report.Patches, dryRun)

	if *outDir != "" {
		diffsPath, err := writeJSONL(*outDir, "diffs.jsonl", report.All())
		if err != nil {
			fmt.Fprintln(stderr, "写差异单失败:", err)
			return 1
		}
		patchPath, err := writeJSONL(*outDir, "cmdb-patch.jsonl", report.Patches)
		if err != nil {
			fmt.Fprintln(stderr, "写补丁失败:", err)
			return 1
		}
		fmt.Fprintf(stdout, "\n产物：%s、%s\n", diffsPath, patchPath)
	}

	if *apply && !*confirm {
		fmt.Fprintln(stderr, "\n-apply 需要人工确认：先把差异单看一遍，再带 -confirm 跑一次。本轮什么都没写。")
		fmt.Fprintf(stdout, "台账 %s 未被改写（sha256 %s）\n", *ledgerPath, shortSHA(before))
		return 2
	}

	if *apply {
		if err := guardWritePath(*ledgerPath, *writePath); err != nil {
			fmt.Fprintln(stderr, "拒绝写库:", err)
			return 1
		}
		applied, err := Apply(ledger, report.Patches)
		if err != nil {
			fmt.Fprintln(stderr, "应用补丁失败:", err)
			return 1
		}
		if err := WriteLedger(*writePath, applied); err != nil {
			fmt.Fprintln(stderr, "写副本失败:", err)
			return 1
		}
		after, err := os.ReadFile(*ledgerPath)
		if err != nil {
			fmt.Fprintln(stderr, "复读台账失败:", err)
			return 1
		}
		if !bytes.Equal(before, after) {
			fmt.Fprintf(stderr, "台账 %s 被改动了，这不应当发生\n", *ledgerPath)
			return 3
		}
		fmt.Fprintf(stdout, "\n台账副本已写入 %s：%d 条设备记录、%d 条端口声明（原台账未动）\n",
			*writePath, len(applied.Devices), len(applied.Links))
	}

	fmt.Fprintf(stdout, "台账 %s 未被改写（sha256 %s）\n", *ledgerPath, shortSHA(before))
	return 0
}

func shortSHA(data []byte) string {
	sum := sha256.Sum256(data)
	return fmt.Sprintf("%x", sum[:6])
}
