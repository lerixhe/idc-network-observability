package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// verdictZh 是结论的中文说法。output 里两个都打，机器读前者、人读后者。
var verdictZh = map[string]string{
	VerdictLocated:   "已定位",
	VerdictArpOnly:   "ARP 有、二层表没有",
	VerdictLeaseOnly: "租约在、现网未见",
	VerdictNotFound:  "查不到",
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func trapsText(traps []string) string {
	if len(traps) == 0 {
		return "-"
	}
	return strings.Join(traps, ",")
}

// printTable 打印批量联查结果。前面几列全用 ASCII，
// 证据与下一步各占一行挂在下面——挤进表格里，中文一多全表错位。
func printTable(w io.Writer, results []Result) {
	fmt.Fprintf(w, "%-14s %-11s %-18s %-30s %-11s %s\n",
		"ip", "verdict", "mac", "所在", "机柜/U 位", "陷阱")
	for _, r := range results {
		loc := "-"
		if r.Switch != "" {
			loc = r.Switch + " " + r.Port
		}
		fmt.Fprintf(w, "%-14s %-11s %-18s %-30s %-11s %s\n",
			r.IP, r.Verdict, dash(r.MAC), loc, r.RackU(), trapsText(r.Traps))
		for i, ev := range r.Evidence {
			if i == 0 {
				fmt.Fprintf(w, "   ↳ %s\n", ev)
			} else {
				fmt.Fprintf(w, "     %s\n", ev)
			}
		}
		if r.NextStep != "" {
			fmt.Fprintf(w, "   → %s\n", r.NextStep)
		}
		fmt.Fprintln(w)
	}
}

// printDetail 是单查的输出：竖排，一条证据一行，
// 运维拿着这个能一路追到机柜前面。
func printDetail(w io.Writer, r Result, now time.Time, aging time.Duration) {
	fmt.Fprintf(w, "查询 %s @ %s（ARP 老化门限 %s）\n\n",
		r.IP, now.Format(time.RFC3339), durText(aging))
	fmt.Fprintf(w, "结论  %s · %s\n", r.Verdict, verdictZh[r.Verdict])
	if r.MAC != "" {
		fmt.Fprintf(w, "MAC   %s   vlan %d\n", r.MAC, r.VLAN)
	}
	if r.Switch != "" {
		fmt.Fprintf(w, "端口  %s %s   %s\n", r.Switch, r.Port, dash(r.PortDesc))
	}
	fmt.Fprintf(w, "位置  %s\n", r.RackU())
	fmt.Fprintf(w, "陷阱  %s\n", trapsText(r.Traps))
	for _, c := range r.Candidates {
		fmt.Fprintf(w, "      成员口 %s %s → %s  %s\n",
			c.Device, c.Port, rackText(c.Rack, c.U), dash(c.Desc))
	}
	for _, s := range r.Stale {
		fmt.Fprintf(w, "      stale  %s %s（vlan %d, age %ds）\n",
			s.Device, s.Port, s.VLAN, s.AgeS)
	}

	fmt.Fprint(w, "\n证据\n")
	for i, ev := range r.Evidence {
		fmt.Fprintf(w, "  %d. %s\n", i+1, ev)
	}
	if r.NextStep != "" {
		fmt.Fprintf(w, "\n下一步 %s\n", r.NextStep)
	}
	fmt.Fprintln(w)
}

// printSummary 是 demo 段：结论分布、三个陷阱各命中几条、三表规模、租约日志里没生效的行。
// 陷阱那三行是这个实验的重点——三条全是零，说明数据没喂对。
func printSummary(w io.Writer, ds *Dataset, results []Result) {
	counts := map[string]int{}
	trapSites := map[string][]string{}
	for _, r := range results {
		counts[r.Verdict]++
		for _, t := range r.Traps {
			trapSites[t] = append(trapSites[t], r.IP)
		}
	}

	fmt.Fprint(w, "\n结论分布：")
	for i, v := range []string{VerdictLocated, VerdictArpOnly, VerdictLeaseOnly, VerdictNotFound} {
		if i > 0 {
			fmt.Fprint(w, " / ")
		}
		fmt.Fprintf(w, "%s %d", v, counts[v])
	}
	fmt.Fprintln(w)

	fmt.Fprintf(w, "三表规模：ARP %d 条（%d 台网关）/ FDB %d 条（%d 台二层）/ 租约 %d 行 / 台账 %d 台设备 %d 条端口\n",
		len(ds.ARP), ds.gatewayCount(), len(ds.FDB), ds.switchCount(), len(ds.Leases),
		len(ds.Inv.Devices), len(ds.Inv.Ports))

	for _, t := range []string{TrapMacDrift, TrapLagLogical, TrapLeaseOnly} {
		sites := trapSites[t]
		if len(sites) == 0 {
			fmt.Fprintf(w, "陷阱 %s：0 条\n", t)
			continue
		}
		fmt.Fprintf(w, "陷阱 %s：%d 条（%s）\n", t, len(sites), strings.Join(sites, " · "))
	}

	if bad := ds.inactiveLeases(); len(bad) > 0 {
		fmt.Fprintf(w, "租约日志里最终没生效的 IP：%d 个 —— %s\n", len(bad), strings.Join(bad, " · "))
	}
}

// writeResults 把结论落成 JSONL：一行一个 IP，机器读这份。
func writeResults(dir string, results []Result) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, "endpoint.jsonl")
	f, err := os.Create(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	enc := json.NewEncoder(f)
	for _, r := range results {
		if err := enc.Encode(r); err != nil {
			return "", err
		}
	}
	return path, nil
}
