// 实验 04：五元组哈希与路径覆盖。
//
// 反直觉结论：你以为在测一条链路，其实在测一条哈希路径。
// 同一批五元组在按五元组哈希的设备上永远落在同一条 ECMP 分支，另一条坏了
// 毫无察觉。这个实验把覆盖率算出来、把盲区标出来：
//
//  1. 固定五元组：覆盖率永远停在 1/N，剩下的分支一次都探不到
//  2. 随机源端口：覆盖率按优惠券收集器的节奏上升，期望 n·H_n 次覆盖满
//  3. 按预算分配端口段：预算多少就覆盖多少，可以直接拿去定配额定
//
// 全程虚拟：不发一个真实数据包，不碰外网。
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
)

// curveCheckpoints 是终端覆盖率表打的那几个点。
// 1/10/100/1000 对应「一条命令」「一轮脚本」「一轮探测」「一个采样周期」四个量级。
var curveCheckpoints = []int{1, 10, 100, 1000}

// curveGrid 是落 CSV 的完整曲线点，等比取密一点，画出来才平滑。
var curveGrid = []int{
	1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 12, 14, 16, 20, 24, 28, 32, 40, 48, 56,
	64, 80, 100, 128, 160, 200, 256, 320, 400, 512, 640, 800, 1000,
}

// budgetCandidates 是终端预算表试的那几档预算。
var budgetCandidates = []int{1, 2, 4, 8, 16}

func main() {
	var (
		pathsPath = flag.String("paths", "samples/paths.json", "等价路径样本（JSON 数组）")
		hashName  = flag.String("hash", "crc32c-mod", "哈希函数："+strings.Join(hashNames(), " / "))
		broken    = flag.String("broken", "auto", "假定坏掉的 member id；auto = 取固定五元组打不到的那条")
		probes    = flag.Int("probes", 100, "故障可见性表每列的探测包数")
		trials    = flag.Int("trials", 2000, "「覆盖满需要多少次探测」的试验次数")
		seed      = flag.Int64("seed", 20260512, "随机种子；同种子同结果")
		outDir    = flag.String("out", "out", "产物目录，留空不落盘")
		demo      = flag.Bool("demo", false, "追加 member 负载、期望探测次数与一段演示")
	)
	flag.Parse()

	if err := run(*pathsPath, *hashName, *broken, *probes, *trials, *seed, *outDir, *demo); err != nil {
		fmt.Fprintln(os.Stderr, "失败:", err)
		os.Exit(1)
	}
}

func run(pathsPath, hashName, broken string, probes, trials int, seed int64, outDir string, demo bool) error {
	if probes < 1 {
		return fmt.Errorf("probes 必须 ≥ 1")
	}
	if trials < 1 {
		return fmt.Errorf("trials 必须 ≥ 1")
	}
	sets, err := loadPathSets(pathsPath)
	if err != nil {
		return err
	}
	h, err := HashByName(hashName)
	if err != nil {
		return err
	}

	fmt.Printf("%d 组等价路径，哈希函数 %s，种子 %d（同种子同结果）\n\n", len(sets), h.Name, seed)

	var coverageRows []coverageRow
	var blindRows []blindRow
	for _, set := range sets {
		printSetHeader(os.Stdout, set, h)
		if err := printCurveTable(os.Stdout, set, h, curveCheckpoints, seed); err != nil {
			return err
		}
		printBudgetTable(os.Stdout, set, h, budgetCandidates, seed)
		printBlindTable(os.Stdout, set, h, probes, seed)

		if demo {
			printLoadTable(os.Stdout, set, LoadShare(h, set, 65536, seed))
			stats := make(map[Mode]CoverStats, len(modeNames))
			for _, mode := range modeNames {
				stats[mode] = ProbesToCover(set, h, mode, trials, seed)
			}
			printCoverStats(os.Stdout, set, stats)
		}

		idx, err := brokenIndex(set, h, broken)
		if err != nil {
			return err
		}
		if demo {
			printNarrative(os.Stdout, set, h, idx, probes, seed)
		}

		for _, mode := range modeNames {
			points, err := Curve(set, h, mode, curveGrid, seed)
			if err != nil {
				return err
			}
			for _, p := range points {
				coverageRows = append(coverageRows, coverageRow{
					Set: set.Name, Mode: string(mode), Probes: p.Probes,
					Covered: p.Covered, Total: len(set.Members), Coverage: p.Coverage,
				})
			}
			for _, spot := range BlindSpots(set, h, mode, probes, seed) {
				blindRows = append(blindRows, blindRow{
					Set: set.Name, Mode: string(mode), Probes: probes,
					Broken: spot.Broken, ObservedLoss: spot.ObservedLoss, Invisible: spot.Invisible,
				})
			}
		}
	}

	if outDir == "" {
		return nil
	}
	csvPath, err := writeCoverageCSV(outDir, coverageRows)
	if err != nil {
		return err
	}
	blindPath, err := writeBlindSpots(outDir, blindRows)
	if err != nil {
		return err
	}
	fmt.Printf("产物\n  %s  %d 行覆盖率曲线\n  %s  %d 行故障可见性\n",
		csvPath, len(coverageRows), blindPath, len(blindRows))
	return nil
}

// brokenIndex 把 -broken 解析成成员下标。
//
// auto 取的是「固定五元组永远打不到」的第一条 member：这不是特例，
// 4 条 member 里固定五元组只能盯住 1 条，剩下 3 条都是盲区，概率 75%。
func brokenIndex(set PathSet, h HashFunc, broken string) (int, error) {
	if broken == "auto" {
		for _, spot := range BlindSpots(set, h, ModeFixed, 1, 0) {
			if spot.Invisible {
				return spot.BrokenIndex, nil
			}
		}
		return 0, nil
	}
	for i, m := range set.Members {
		if m.ID == broken {
			return i, nil
		}
	}
	return 0, fmt.Errorf("%s: 没有 id 为 %q 的 member", set.Name, broken)
}
