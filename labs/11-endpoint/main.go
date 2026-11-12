// 实验 11：终端定位联查。
//
// 喂进三套快照——两台网关的 ARP 表、四台二层交换机的 FDB、一份 DHCP 租约日志——
// 再加一份最小的端口台账，回答运维问得最多的那句话：
// 「10.32.7.18 现在在哪个机柜的哪个口？」
//
// 三张表接力：ARP 给 IP → MAC，FDB 给 MAC → 交换机端口，台账给端口 → 机柜 / U 位。
// 每一环都有自己的老化周期，接力断在哪一环，决定了答案该是「已定位」还是「查不到」。
// 样本里埋了三个陷阱：MAC 漂移、聚合口只报逻辑口、ARP 老化但租约还在。
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
		arpPath   = flag.String("arp", "samples/arp.jsonl", "两台网关的 ARP 表快照（JSONL）")
		fdbPath   = flag.String("fdb", "samples/fdb.jsonl", "二层交换机 FDB 快照（JSONL）")
		leasePath = flag.String("leases", "samples/dhcp-leases.jsonl", "DHCP 租约日志（JSONL，事件流）")
		portPath  = flag.String("ports", "samples/ports.json", "最小台账：端口 → 机柜 / U 位（JSON）")
		queryPath = flag.String("queries", "samples/queries.txt", "批量查询的 IP 清单，一行一个")
		query     = flag.String("query", "", "单查一个 IP，给了就只查这一个")
		aging     = flag.Duration("aging", defaultArpAging, "ARP 老化门限，超过这个时长不算数")
		demo      = flag.Bool("demo", false, "额外打印结论分布、三个陷阱命中与三表规模")
		outDir    = flag.String("out", "out", "结果落盘目录，留空则不落")
	)
	flag.Parse()

	ds, err := LoadDataset(*arpPath, *fdbPath, *leasePath, *portPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "加载样本失败:", err)
		os.Exit(1)
	}

	ips := []string{}
	if *query != "" {
		ips = append(ips, *query)
	} else {
		ips, err = loadQueries(*queryPath)
		if err != nil {
			fmt.Fprintln(os.Stderr, "读取查询清单失败:", err)
			os.Exit(1)
		}
	}

	opt := Options{ArpAging: *aging}
	results := make([]Result, 0, len(ips))
	for _, ip := range ips {
		results = append(results, Locate(ds, ip, opt))
	}

	if *query != "" {
		printDetail(os.Stdout, results[0], ds.TS, opt.ArpAging)
	} else {
		fmt.Printf("%d 个 IP，ARP %d 条 / FDB %d 条 / 租约 %d 行 / 台账端口 %d 条，快照时刻 %s\n\n",
			len(ips), len(ds.ARP), len(ds.FDB), len(ds.Leases), len(ds.Inv.Ports),
			ds.TS.Format(time.RFC3339))
		printTable(os.Stdout, results)
		if *demo {
			printSummary(os.Stdout, ds, results)
		}
	}

	if *outDir == "" {
		return
	}
	path, err := writeResults(*outDir, results)
	if err != nil {
		fmt.Fprintln(os.Stderr, "写结果失败:", err)
		os.Exit(1)
	}
	fmt.Printf("\n结论已写入 %s\n", path)
}

// loadQueries 读批量查询清单。行内注释只是给人看的，代码只看第一个字段。
func loadQueries(path string) ([]string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var ips []string
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if f := strings.Fields(line); len(f) > 0 {
			ips = append(ips, f[0])
		}
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("%s 里没有 IP", path)
	}
	return ips, nil
}
