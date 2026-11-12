package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
)

// printTable 打印一行一端口的判定结果。前面几列全用 ASCII，
// 中文只放最后一列（判据），这样终端里对齐不会被宽字符带偏。
func printTable(w io.Writer, verdicts []Verdict) {
	fmt.Fprintf(w, "%-12s %-14s %-6s %-16s %-17s %-11s %-5s %s\n",
		"device", "port", "admin", "oper", "cause", "owner", "flips", "evidence ｜ 下一步")
	for _, v := range verdicts {
		owner := v.Owner
		if owner == "" {
			owner = "-"
		}
		flap := fmt.Sprintf("%d", v.FlapCount)
		if v.Flapping {
			flap += "*"
		}
		// 判据与下一步动作挤在同一列：两段都是自由文本，开新列反而全表错位。
		tail := v.Evidence
		if v.NextStep != "" {
			tail += "｜下一步：" + v.NextStep
		}
		fmt.Fprintf(w, "%-12s %-14s %-6s %-16s %-17s %-11s %-5s %s\n",
			v.Device, v.IfName, v.Admin, v.Oper, v.Cause, owner, flap, tail)
	}
}

// printSummary 打印归属汇总、抖动清单与设备重启。这部分不进 JSONL，
// 是给值班人一眼扫的。
func printSummary(w io.Writer, a Analysis) {
	owners := map[string]int{}
	causes := map[string]int{}
	var flapping []Verdict
	for _, v := range a.Verdicts {
		owners[v.Owner]++
		causes[v.Cause]++
		if v.Flapping {
			flapping = append(flapping, v)
		}
	}

	// 归属为空串的就是「无异常」那批端口，它们不该记在任何人的账上。
	fmt.Fprintf(w, "\n归属合计：变更 %d / 硬件 %d / 配置 %d / 故障 %d / 无异常 %d\n",
		owners[OwnerChange], owners[OwnerHardware], owners[OwnerConfig],
		owners[OwnerFault], owners[""])

	names := make([]string, 0, len(causes))
	for c := range causes {
		names = append(names, c)
	}
	sort.Strings(names)
	fmt.Fprint(w, "成因分布：")
	for i, c := range names {
		if i > 0 {
			fmt.Fprint(w, " / ")
		}
		fmt.Fprintf(w, "%s %d", c, causes[c])
	}
	fmt.Fprintln(w)

	for _, v := range flapping {
		fmt.Fprintf(w, "抖动：%s %s 窗口内 %d 次翻转（≥ 阈值），判 flapping\n",
			v.Device, v.IfName, v.FlapCount)
	}
	for _, r := range a.Reboots {
		fmt.Fprintf(w, "设备重启：%s @%s（sysUpTime %.1fs），该设备冷启动期 %d 条 Trap 已剔除\n",
			r.Device, r.TS, float64(r.UptimeMs)/1000, r.Dropped)
	}
	if a.DroppedTraps > 0 {
		fmt.Fprintf(w, "共剔除 %d 条冷启动期 Trap（仍在 sysUpTime %.0fs 以内）\n",
			a.DroppedTraps, float64(agentColdStartMs)/1000)
	}
}

// writeVerdicts 把判定结果落成 JSONL：一行一个端口，机器读这份。
func writeVerdicts(dir string, verdicts []Verdict) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, "verdicts.jsonl")
	f, err := os.Create(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if err := writeJSONL(f, verdicts); err != nil {
		return "", err
	}
	return path, nil
}

func writeJSONL(w io.Writer, verdicts []Verdict) error {
	enc := json.NewEncoder(w)
	for _, v := range verdicts {
		if err := enc.Encode(v); err != nil {
			return err
		}
	}
	return nil
}
