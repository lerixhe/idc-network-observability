package main

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

func locateAll(ds *Dataset, ips []string) []Result {
	results := make([]Result, 0, len(ips))
	for _, ip := range ips {
		results = append(results, Locate(ds, ip, Options{}))
	}
	return results
}

// 批量表格里三个陷阱的标记必须都能看见——看不见就等于没做。
func TestTableShowsTrapMarkers(t *testing.T) {
	ds := loadSamples(t)
	results := locateAll(ds, []string{"10.32.8.44", "10.20.4.51", "10.32.7.18", "10.32.7.31"})

	var buf bytes.Buffer
	printTable(&buf, results)
	out := buf.String()

	for _, want := range []string{
		TrapMacDrift,
		TrapLagLogical,
		TrapLeaseOnly,
		"租约在、现网未见", // 陷阱 c 的人话说法，不能只有机器标记
		"B-01/U07",
		"A-03/U20",
		"A-03/U12",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("表格里看不到 %q\n%s", want, out)
		}
	}
	if !strings.Contains(out, "↳") || !strings.Contains(out, "→") {
		t.Errorf("每条都要挂证据与下一步\n%s", out)
	}
}

// 单查输出是竖排的，一行一条证据，带序号。
func TestDetailOutput(t *testing.T) {
	ds := loadSamples(t)
	r := Locate(ds, "10.32.8.44", Options{})

	var buf bytes.Buffer
	printDetail(&buf, r, ds.TS, defaultArpAging)
	out := buf.String()

	for _, want := range []string{
		"查询 10.32.8.44 @ 2026-05-12T10:12:00Z",
		"结论  located · 已定位",
		"MAC   00:50:56:aa:00:44   vlan 30",
		"端口  leaf-b1f-02 Ethernet1/12",
		"位置  B-01/U07",
		"陷阱  " + TrapMacDrift,
		"stale  leaf-b1f-01 Ethernet1/5（vlan 30, age 780s）",
		"证据",
		"  1. ",
		"下一步 ",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("单查输出里看不到 %q\n%s", want, out)
		}
	}
}

// demo 汇总里三个陷阱各要有计数，全是零就说明数据没喂对。
func TestSummaryCountsTraps(t *testing.T) {
	ds := loadSamples(t)
	results := locateAll(ds, []string{
		"10.32.7.31", "10.32.7.18", "10.32.8.44", "10.20.4.51",
		"10.20.4.60", "10.33.1.12", "10.32.7.99",
	})

	var buf bytes.Buffer
	printSummary(&buf, ds, results)
	out := buf.String()

	for _, want := range []string{
		"结论分布：located 4 / arp_only 1 / lease_only 1 / not_found 1",
		"三表规模：ARP 10 条（2 台网关）/ FDB 17 条（4 台二层）/ 租约 10 行 / 台账 6 台设备 18 条端口",
		"陷阱 mac_drift：1 条（10.32.8.44）",
		"陷阱 lag_logical_only：1 条（10.20.4.51）",
		"陷阱 lease_only：1 条（10.32.7.18）",
		"租约日志里最终没生效的 IP：2 个",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("汇总里看不到 %q\n%s", want, out)
		}
	}
}

func TestWriteResultsJSONL(t *testing.T) {
	ds := loadSamples(t)
	results := locateAll(ds, []string{"10.32.8.44"})

	dir := t.TempDir()
	path, err := writeResults(dir, results)
	if err != nil {
		t.Fatalf("落盘失败: %v", err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"ip":"10.32.8.44"`, `"verdict":"located"`, `"traps":["mac_drift"]`, `"age_s":780`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("JSONL 里看不到 %q\n%s", want, b)
		}
	}
}

func TestLoadQueriesSkipsComments(t *testing.T) {
	ips, err := loadQueries("samples/queries.txt")
	if err != nil {
		t.Fatalf("读清单失败: %v", err)
	}
	if len(ips) != 7 {
		t.Fatalf("IP 数 = %d，想要 7：%v", len(ips), ips)
	}
	if ips[0] != "10.32.7.31" || ips[6] != "10.32.7.99" {
		t.Errorf("顺序不对：%v", ips)
	}
}

// 老化门限打印成人读的形式，0 值要走默认值而不是「0s 门限」。
func TestPrintDetailDefaultAging(t *testing.T) {
	ds := loadSamples(t)
	r := Locate(ds, "10.32.7.31", Options{})

	var buf bytes.Buffer
	printDetail(&buf, r, ds.TS, defaultArpAging)
	if !strings.Contains(buf.String(), "ARP 老化门限 5m") {
		t.Errorf("门限没打出来：%s", buf.String())
	}
}
