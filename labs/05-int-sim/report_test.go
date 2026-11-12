package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFmtBytes(t *testing.T) {
	cases := []struct {
		b    int64
		want string
	}{
		{0, "0 B"},
		{512, "512 B"},
		{2048, "2.0 KiB"},
		{39_600_000, "37.8 MiB"},
		{67_108_864, "64.0 MiB"},
	}
	for _, c := range cases {
		if got := fmtBytes(c.b); got != c.want {
			t.Errorf("fmtBytes(%d) = %q, 期望 %q", c.b, got, c.want)
		}
	}
}

func TestFmtBps(t *testing.T) {
	if got := fmtBps(25_000_000_000); got != "25G" {
		t.Errorf("fmtBps(25G) = %q", got)
	}
	if got := fmtBps(100_000_000_000); got != "100G" {
		t.Errorf("fmtBps(100G) = %q", got)
	}
}

func TestFmtUsAndSigned(t *testing.T) {
	if got := fmtUs(3_202_100); got != "3202.1" {
		t.Errorf("fmtUs = %q", got)
	}
	if got := fmtSigned(299_360); got != "+299.4" {
		t.Errorf("fmtSigned(正) = %q", got)
	}
	if got := fmtSigned(-1_968_455); got != "-1968.5" {
		t.Errorf("fmtSigned(负) = %q", got)
	}
}

// TestDispWidth 中文按两列算，否则所有带中文的行都会错位。
func TestDispWidth(t *testing.T) {
	cases := []struct {
		s    string
		want int
	}{
		{"abc", 3},
		{"端到端", 6},
		{"盲区驻留", 8},
		{"a端b", 4},
	}
	for _, c := range cases {
		if got := dispWidth(c.s); got != c.want {
			t.Errorf("dispWidth(%q) = %d, 期望 %d", c.s, got, c.want)
		}
	}
}

func TestPadLabel(t *testing.T) {
	if got := padLabel("端到端", 8); got != "端到端  " {
		t.Errorf("padLabel 补空格不对: %q", got)
	}
	if got := padLabel("时钟误差上界", 8); got != "时钟误差上界" {
		t.Errorf("超宽时不该截断: %q", got)
	}
}

func TestShortIf(t *testing.T) {
	if got := shortIf("Ethernet1/49"); got != "Et1/49" {
		t.Errorf("shortIf = %q", got)
	}
	if got := shortIf(""); got != "-" {
		t.Errorf("空接口名应当打 \"-\"，实际 %q", got)
	}
	if got := shortIf("eth0"); got != "Etheth0" && got != "Eteth0" {
		// eth0 不以 Ethernet 开头，前缀不该被吃掉
		if !strings.HasSuffix(got, "eth0") {
			t.Errorf("shortIf(eth0) = %q", got)
		}
	}
}

func TestPrintHopTable(t *testing.T) {
	d, err := Decompose(threeHopTrace(false), defaultClockAccuracy)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	printHopTable(&buf, d)
	out := buf.String()
	for _, want := range []string{"hop", "device", "speed", "queue", "forward", "resident", "sw-a", "sw-b", "100G"} {
		if !strings.Contains(out, want) {
			t.Errorf("逐跳表里缺 %q:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "排队为主") {
		t.Errorf("sw-b 排队 1 ms 应当判排队为主:\n%s", out)
	}
}

// TestPrintHopTableBlind 中间跳没数据时，那一行只打「盲区」，不留任何数字。
func TestPrintHopTableBlind(t *testing.T) {
	d, err := Decompose(threeHopTrace(true), defaultClockAccuracy)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	printHopTable(&buf, d)
	out := buf.String()
	if !strings.Contains(out, "盲区（无 INT 数据）") {
		t.Errorf("没数据的跳应当标盲区:\n%s", out)
	}
	if strings.Contains(out, "排队为主") {
		t.Errorf("盲区跳不该被判成排队为主:\n%s", out)
	}
}

func TestPrintHopTableShowsCapNote(t *testing.T) {
	h1 := goodHop(1, "sw-a", 0, 3_000, 0)
	h1.LinkLengthM = 0
	h1.QueueDepthBytes = 50_000
	d, err := Decompose(Trace{ID: "t5", Hops: []HopMeta{h1, goodHop(2, "sw-b", 10_000, 13_000, 0)}},
		defaultClockAccuracy)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	printHopTable(&buf, d)
	if !strings.Contains(buf.String(), "按驻留封顶") {
		t.Errorf("封顶说明没打出来:\n%s", buf.String())
	}
}

func TestPrintBreakdown(t *testing.T) {
	tr := threeHopTrace(true)
	d, err := Decompose(tr, defaultClockAccuracy)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	printBreakdown(&buf, d, tr)
	out := buf.String()
	for _, want := range []string{
		"端到端", "时钟误差上界", "已归因", "链路传播", "差额", "盲区驻留",
		"台账长度 60 m × 5 ns/m",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("分解里缺 %q:\n%s", want, out)
		}
	}
}

func TestPrintSegmentsFullCoverage(t *testing.T) {
	d, err := Decompose(threeHopTrace(false), defaultClockAccuracy)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	printSegments(&buf, d)
	if !strings.Contains(buf.String(), "无盲区") {
		t.Errorf("全跳有数据时应当说无盲区:\n%s", buf.String())
	}
}

// TestPrintSegmentsPartial 部分覆盖时的关键输出：段、差额、分不清是哪一跳。
func TestPrintSegmentsPartial(t *testing.T) {
	tr := Trace{ID: "t4", Hops: []HopMeta{
		goodHop(1, "sw-a", 0, 3_000, 0),
		blindHop(2, "dci-border-01"),
		blindHop(3, "dci-border-02"),
		goodHop(4, "sw-d", 900_000, 903_000, 0),
	}}
	d, err := Decompose(tr, defaultClockAccuracy)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	printSegments(&buf, d)
	out := buf.String()
	for _, want := range []string{"段 1", "dci-border-01", "dci-border-02", "跨 2 跳无数据", "分不清是哪一跳"} {
		if !strings.Contains(out, want) {
			t.Errorf("盲区段输出缺 %q:\n%s", want, out)
		}
	}
}

func TestPrintNaive(t *testing.T) {
	tr := threeHopTrace(false)
	tr.Hops[2].IngressTsNs -= 900_000
	tr.Hops[2].EgressTsNs -= 900_000
	d, err := Decompose(tr, defaultClockAccuracy)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	printNaive(&buf, d)
	out := buf.String()
	if !strings.Contains(out, "负数") || !strings.Contains(out, "时钟不在一个域") {
		t.Errorf("反例段落没把负时延的成因说清:\n%s", out)
	}
}

func TestPrintReconcile(t *testing.T) {
	traces, err := loadTraces("samples/traces.json")
	if err != nil {
		t.Fatal(err)
	}
	truth, err := loadTruth("samples/truth.json")
	if err != nil {
		t.Fatal(err)
	}
	d, err := Decompose(traces[1], defaultClockAccuracy)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	printReconcile(&buf, d, truth["t-partial"])
	out := buf.String()
	for _, want := range []string{"对账", "合计", "盲区", "驻留误差为 0", "时钟偏移差"} {
		if !strings.Contains(out, want) {
			t.Errorf("对账输出缺 %q:\n%s", want, out)
		}
	}
}

func TestWriteOutputs(t *testing.T) {
	traces, err := loadTraces("samples/traces.json")
	if err != nil {
		t.Fatal(err)
	}
	var decomps []Decomposition
	hops := 0
	for _, tr := range traces {
		d, err := Decompose(tr, defaultClockAccuracy)
		if err != nil {
			t.Fatal(err)
		}
		decomps = append(decomps, d)
		hops += d.HopCount
	}
	dir := filepath.Join(t.TempDir(), "nested", "out")
	hopPath, summaryPath, err := writeOutputs(dir, decomps)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(hopPath, "hop-delays.jsonl") || !strings.HasSuffix(summaryPath, "decomposition.json") {
		t.Errorf("产物文件名不对: %s / %s", hopPath, summaryPath)
	}

	f, err := os.Open(hopPath)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	n := 0
	for sc.Scan() {
		var row hopRow
		if err := json.Unmarshal(sc.Bytes(), &row); err != nil {
			t.Fatalf("第 %d 行不是合法 JSON: %v", n+1, err)
		}
		if row.TraceID == "" || row.Hop.Index == 0 {
			t.Fatalf("第 %d 行缺字段: %+v", n+1, row)
		}
		n++
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	if n != hops {
		t.Errorf("逐跳明细应当 %d 行，实际 %d", hops, n)
	}

	data, err := os.ReadFile(summaryPath)
	if err != nil {
		t.Fatal(err)
	}
	var summaries []Summary
	if err := json.Unmarshal(data, &summaries); err != nil {
		t.Fatal(err)
	}
	if len(summaries) != len(traces) {
		t.Fatalf("汇总应当 %d 条，实际 %d", len(traces), len(summaries))
	}
	partial := summaries[1]
	if partial.BlindHops != 2 || partial.BlindSegments != 1 || partial.Instrumented != 4 {
		t.Errorf("t-partial 汇总不对: %+v", partial)
	}
}

// TestRunEndToEnd 跑完整流程：两条 trace、带对账、落产物。
func TestRunEndToEnd(t *testing.T) {
	dir := t.TempDir()
	var buf bytes.Buffer
	if err := run("samples/traces.json", "samples/truth.json", "", dir, &buf); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{"t-full", "t-partial", "盲区段", "对账"} {
		if !strings.Contains(out, want) {
			t.Errorf("输出里缺 %q", want)
		}
	}
	for _, name := range []string{"hop-delays.jsonl", "decomposition.json"} {
		if info, err := os.Stat(filepath.Join(dir, name)); err != nil || info.Size() == 0 {
			t.Errorf("%s 没有正常落盘: %v", name, err)
		}
	}
}

// TestRunWithoutTruth 不给真值时不该出现对账段——生产环境没有真值。
func TestRunWithoutTruth(t *testing.T) {
	var buf bytes.Buffer
	if err := run("samples/traces.json", "", "", "", &buf); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), "对账") {
		t.Error("没给真值却打出了对账，等于在生产里假装有答案")
	}
}

func TestRunSingleTrace(t *testing.T) {
	var buf bytes.Buffer
	if err := run("samples/traces.json", "", "t-partial", "", &buf); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "t-partial") || strings.Contains(buf.String(), "t-full") {
		t.Error("按 id 过滤没生效")
	}
}

func TestRunRejectsUnknownTrace(t *testing.T) {
	var buf bytes.Buffer
	if err := run("samples/traces.json", "", "t-missing", "", &buf); err == nil {
		t.Error("不存在的 trace id 应当报错")
	}
}
