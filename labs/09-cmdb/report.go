package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// printSummary 打对账结果的总览与四段差异单。
func printSummary(w io.Writer, r Report, lldpAt, arpAt, arpGateway string) {
	fmt.Fprintf(w, "台账 %d 条设备记录 / %d 条端口声明；LLDP %d 个端口；ARP %d 条\n",
		r.Stats.Devices, r.Stats.Links, r.Stats.LldpPorts, r.Stats.ArpEntries)
	fmt.Fprintf(w, "LLDP 采集于 %s，ARP 采集于 %s（网关 %s）\n", lldpAt, arpAt, arpGateway)

	printDiffs(w, "僵尸设备（台账在役，现网无痕迹）", r.Zombies)
	printDiffs(w, "幽灵设备（现网在，台账没有）", r.Ghosts)
	printDiffs(w, "错挂链路（台账与 LLDP 观测不符）", r.Mismatches)

	identities := make([]Diff, 0, len(r.Renames)+len(r.Reindexes))
	identities = append(identities, r.Renames...)
	identities = append(identities, r.Reindexes...)
	printDiffs(w, "身份问题（改名 / 端口重排）", identities)

	if r.Stats.MergedOld > 0 {
		fmt.Fprintf(w, "\n已归并的旧名 %d 条，僵尸判定按主名做：\n", r.Stats.MergedOld)
		for _, old := range sortedKeys(r.Primary) {
			fmt.Fprintf(w, "  %s -> %s\n", old, r.Primary[old])
		}
	}
	if r.Stats.LldpBlind > 0 {
		fmt.Fprintf(w, "\n%d 台设备 LLDP 里看不到但管理面还活着，未判僵尸（先查 LLDP 覆盖，别急着下架）\n",
			r.Stats.LldpBlind)
	}
	if r.Stats.Dangling > 0 {
		fmt.Fprintf(w, "\n%d 条端口声明指向台账里不存在的设备，已跳过对账\n", r.Stats.Dangling)
	}
}

// printDiffs 打一段差异。前面几列全 ASCII，中文只放最后一列。
func printDiffs(w io.Writer, title string, diffs []Diff) {
	fmt.Fprintf(w, "\n== %s · %d 条 ==\n", title, len(diffs))
	if len(diffs) == 0 {
		fmt.Fprintln(w, "无")
		return
	}
	fmt.Fprintf(w, "%-4s %-24s %-6s %s\n", "id", "target", "conf", "依据 / 建议")
	for _, d := range diffs {
		tail := ""
		if len(d.Evidence) > 0 {
			tail = d.Evidence[0]
		}
		fmt.Fprintf(w, "%-4s %-24s %-6s %s\n", d.ID, d.Target, d.Confidence, tail)
		pad := strings.Repeat(" ", 4+1+24+1+6+1)
		for _, e := range d.Evidence[1:] {
			fmt.Fprintf(w, "%s%s\n", pad, e)
		}
		if d.ActionZH != "" {
			fmt.Fprintf(w, "%s建议：%s（%s）\n", pad, d.ActionZH, d.Action)
		}
	}
}

// printPatches 打补丁清单。每条补丁一行，字段按名字排序，读起来稳定。
func printPatches(w io.Writer, patches []Patch, dryRun bool) {
	mode := "dry-run，未落库"
	if !dryRun {
		mode = "已应用到副本"
	}
	fmt.Fprintf(w, "\n== 建议写入的补丁 · %d 条（%s） ==\n", len(patches), mode)
	if len(patches) == 0 {
		fmt.Fprintln(w, "无")
		return
	}
	for _, p := range patches {
		var kv []string
		for _, k := range sortedKeys(p.Set) {
			kv = append(kv, k+"="+p.Set[k])
		}
		fields := strings.Join(kv, " ")
		if fields == "" {
			// link_delete 这类补丁没有字段要写，删一条声明就完了。
			fields = "（删掉这条声明）"
		}
		fmt.Fprintf(w, "%-4s %-18s %-26s %-7s %s\n",
			p.ID, p.Op, p.Target, p.Confidence, fields)
	}
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// writeJSONL 把一组对象落成 JSONL：一行一个，机器读这份。
func writeJSONL[T any](dir, name string, items []T) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, name)
	f, err := os.Create(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	enc := json.NewEncoder(f)
	for _, it := range items {
		if err := enc.Encode(it); err != nil {
			return "", err
		}
	}
	return path, nil
}
