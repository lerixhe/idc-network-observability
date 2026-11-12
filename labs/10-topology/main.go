// 实验 10：拓扑多源融合与事件去重。
//
// 一个实验，两段能力，共同点只有一个：都靠 CMDB 的别名表把「别人嘴里的名字」
// 翻译成规范名。名字翻译错了，拓扑连错设备、事件记错端口，而且两者都不会报错。
//
//	拓扑融合：LLDP / STP / 路由邻居 / MAC 反推四份快照 → 设备拓扑 + 冲突清单
//	事件去重：Trap / syslog / 计数器归零 / 探测失败四源 → 同一件事一条事件
package main

import (
	"flag"
	"fmt"
	"os"
	"time"
)

func main() {
	var (
		mode = flag.String("mode", "all", "跑哪一段：all / topology / events")

		cmdbPath   = flag.String("cmdb", "samples/cmdb.json", "台账快照（设备、别名、聚合口）")
		lldpPath   = flag.String("lldp", "samples/lldp.jsonl", "LLDP 邻居快照（JSONL，一行一条单端断言）")
		stpPath    = flag.String("stp", "samples/stp.jsonl", "STP 端口角色快照（JSONL）")
		routePath  = flag.String("route", "samples/route.jsonl", "路由邻居快照（JSONL）")
		macPath    = flag.String("mac", "samples/mac.jsonl", "MAC 表快照（JSONL）")
		eventsPath = flag.String("events", "samples/events.jsonl", "四源事件流（JSONL）")
		burstPath  = flag.String("events-burst", "samples/events-burst.jsonl", "48 口同秒全 down 的压力样本（JSONL）")

		dedupWindow = flag.Duration("dedup-window", time.Duration(defaultEventPolicy.DedupWindowMs)*time.Millisecond, "同指纹合并的时间窗")
		burstWindow = flag.Duration("burst-window", time.Duration(defaultEventPolicy.BurstWindowMs)*time.Millisecond, "设备级聚合窗口")
		burstMin    = flag.Int("burst-min-ports", defaultEventPolicy.BurstMinPorts, "折叠成设备级事件所需的端口数下限")
		outDir      = flag.String("out", "out", "结果输出目录，留空则不落盘")
	)
	flag.Parse()

	switch *mode {
	case "all", "topology", "events":
	default:
		fmt.Fprintf(os.Stderr, "不认识的 -mode %q（all / topology / events）\n", *mode)
		os.Exit(2)
	}

	dir, err := loadDirectory(*cmdbPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "读取台账失败:", err)
		os.Exit(1)
	}

	if *mode == "all" || *mode == "topology" {
		if err := runTopology(dir, *lldpPath, *stpPath, *routePath, *macPath, *outDir); err != nil {
			fmt.Fprintln(os.Stderr, "拓扑融合失败:", err)
			os.Exit(1)
		}
	}
	if *mode == "all" || *mode == "events" {
		policy := EventPolicy{
			DedupWindowMs: dedupWindow.Milliseconds(),
			BurstWindowMs: burstWindow.Milliseconds(),
			BurstMinPorts: *burstMin,
		}
		if err := runEvents(dir, []string{*eventsPath, *burstPath}, policy, *outDir); err != nil {
			fmt.Fprintln(os.Stderr, "事件去重失败:", err)
			os.Exit(1)
		}
	}
}

// loadDirectory 读台账并建归一化器。后面两段都靠它。
func loadDirectory(path string) (*Directory, error) {
	var c CMDB
	if err := loadJSON(path, &c); err != nil {
		return nil, err
	}
	return NewDirectory(c)
}

func runTopology(dir *Directory, lldpPath, stpPath, routePath, macPath, outDir string) error {
	lldp, err := loadLLDP(lldpPath)
	if err != nil {
		return err
	}
	stp, err := loadSTP(stpPath)
	if err != nil {
		return err
	}
	routes, err := loadRoutes(routePath)
	if err != nil {
		return err
	}
	macs, err := loadMACs(macPath)
	if err != nil {
		return err
	}

	t := Fuse(dir, lldp, stp, routes, macs)

	fmt.Println("=== 一、拓扑融合：四份快照 → 设备拓扑 + 冲突清单 ===")
	fmt.Printf("LLDP %d 行 / STP %d 行 / 路由邻居 %d 行 / MAC %d 行\n\n",
		len(lldp), len(stp), len(routes), len(macs))
	printLinks(os.Stdout, t.Links)
	printTopologySummary(os.Stdout, t, dir)
	printNotices(os.Stdout, t.Notices)

	if outDir == "" {
		return nil
	}
	if _, err := writeJSONL(outDir, "links.jsonl", t.Links); err != nil {
		return err
	}
	if _, err := writeJSONL(outDir, "topology-notices.jsonl", t.Notices); err != nil {
		return err
	}
	fmt.Printf("\n链路已写入 %s/links.jsonl，提示已写入 %s/topology-notices.jsonl\n", outDir, outDir)
	return nil
}

func runEvents(dir *Directory, paths []string, policy EventPolicy, outDir string) error {
	streams := make([]EventStream, 0, len(paths))
	for _, p := range paths {
		s, err := loadEventStream(p)
		if err != nil {
			return err
		}
		streams = append(streams, s)
	}

	set, err := Dedupe(dir, streams, policy)
	if err != nil {
		return err
	}

	fmt.Println("\n=== 二、事件去重：四源事件 → 同一件事一条事件 ===")
	fmt.Printf("合并窗口 %s；设备级聚合：%ds 内 ≥ %d 个端口同族触发\n",
		time.Duration(policy.DedupWindowMs)*time.Millisecond,
		policy.BurstWindowMs/1000, policy.BurstMinPorts)
	fmt.Printf("指纹字段：族 | 设备规范名 | 端口规范名（时间、来源、序号、原文都不进指纹）\n\n")
	printEvents(os.Stdout, set.Events)
	printEventSummary(os.Stdout, set)
	printNotices(os.Stdout, set.Notices)

	if outDir == "" {
		return nil
	}
	if _, err := writeJSONL(outDir, "events.jsonl", set.Events); err != nil {
		return err
	}
	if _, err := writeJSONL(outDir, "event-notices.jsonl", set.Notices); err != nil {
		return err
	}
	fmt.Printf("\n事件已写入 %s/events.jsonl，提示已写入 %s/event-notices.jsonl\n", outDir, outDir)
	return nil
}
