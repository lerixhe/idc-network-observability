package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFlagTextMarksBothKindsOfDivergence(t *testing.T) {
	cases := []struct {
		name string
		p    Interval
		want string
	}{
		{"正常", Interval{}, "-"},
		{"回绕让 v2 与 v3 分岔", Interval{Wraps: 1}, "◄v2≠v3"},
		{"重启让 v2 与 v3 分岔", Interval{Reset: true}, "◄v2≠v3"},
		{"丢样让 v1 与 v2 分岔", Interval{Gap: 3}, "◄v1≠v2"},
		{"丢样与回绕叠在同一格", Interval{Gap: 3, Wraps: 1}, "◄v1≠v2,v2≠v3"},
	}
	for _, c := range cases {
		if got := flagText(c.p); got != c.want {
			t.Errorf("%s：flag = %q，期望 %q", c.name, got, c.want)
		}
	}
}

// TestReportShowsThreeColumns 检查报告真的把三列摆出来了，分岔点有标记。
func TestReportShowsThreeColumns(t *testing.T) {
	var buf bytes.Buffer
	if err := run(&buf, options{
		samplesPath: "samples/counters.json",
		errPath:     "samples/errors.json",
		truthPath:   "samples/truth.json",
		outDir:      "",
	}); err != nil {
		t.Fatalf("跑不通：%v", err)
	}
	out := buf.String()

	for _, want := range []string{
		"v1 naive", "v2 norm", "v3 wrap", // 三列表头
		"◄v2≠v3",                                         // 回绕点的分岔标记
		"◄v1≠v2",                                         // 丢样点的分岔标记
		"回绕 1 圈",                                         // 回绕段的理由
		"采集周期红线",                                         // 告警小节
		"与真值对账",                                          // 对账小节
		"错包归因",                                           // 归因小节
		"cable", "module", "congestion", "host", "clean", // 四类归属 + 干净
	} {
		if !strings.Contains(out, want) {
			t.Errorf("报告里没有 %q", want)
		}
	}
	// 10G 口用 10s 采集周期那条红线必须在告警里明说，且带公式里的两个数。
	if !strings.Contains(out, "3.44s") || !strings.Contains(out, "1.72s") {
		t.Errorf("告警里没有写清回绕周期 3.44s 与红线 1.72s")
	}
}

func TestQuietKeepsConclusions(t *testing.T) {
	var buf bytes.Buffer
	if err := run(&buf, options{
		samplesPath: "samples/counters.json",
		errPath:     "samples/errors.json",
		truthPath:   "",
		outDir:      "",
		quiet:       true,
	}); err != nil {
		t.Fatalf("跑不通：%v", err)
	}
	out := buf.String()
	if strings.Contains(out, "from->to") {
		t.Error("quiet 模式不该打逐区间明细")
	}
	if !strings.Contains(out, "小结：回绕 1") {
		t.Error("quiet 模式必须保留每个端口的结论行")
	}
}

func TestReconcileShippedSamples(t *testing.T) {
	series, err := loadSeries("samples/counters.json")
	if err != nil {
		t.Fatal(err)
	}
	truth, err := loadTruth("samples/truth.json")
	if err != nil {
		t.Fatal(err)
	}
	results := make([]SeriesRates, 0, len(series))
	for _, s := range series {
		r, err := ComputeRates(s, 0)
		if err != nil {
			t.Fatal(err)
		}
		results = append(results, r)
	}
	var buf bytes.Buffer
	if n := printReconcile(&buf, results, truth); n != 0 {
		t.Fatalf("对账有 %d 处不一致：\n%s", n, buf.String())
	}
}

func TestPrintWrapTableMatchesLessonNumbers(t *testing.T) {
	var buf bytes.Buffer
	printWrapTable(&buf, wrapTableSpeeds, 10*time.Second)
	out := buf.String()

	// 这一课必须记住的两个数：1G 34.36 秒，10G 3.44 秒。
	if !strings.Contains(out, "34.36s") {
		t.Error("回绕周期表里没有 1G 口的 34.36s")
	}
	if !strings.Contains(out, "3.44s") {
		t.Error("回绕周期表里没有 10G 口的 3.44s")
	}
	// 10 秒采集周期对 1G 口安全、对 10G 口危险，界线必须显式判出来。
	var sawOK, sawDanger bool
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "1.00G") && strings.Contains(line, "ok:") {
			sawOK = true
		}
		if strings.HasPrefix(strings.TrimSpace(line), "10.00G") && strings.Contains(line, "DANGER") {
			sawDanger = true
		}
	}
	if !sawOK || !sawDanger {
		t.Errorf("采集周期判定不对：1G 应当 ok，10G 应当 DANGER\n%s", out)
	}
}

func TestWriteOutputs(t *testing.T) {
	dir := t.TempDir()
	series, err := loadSeries("samples/counters.json")
	if err != nil {
		t.Fatal(err)
	}
	results := make([]SeriesRates, 0, len(series))
	for _, s := range series {
		r, err := ComputeRates(s, 0)
		if err != nil {
			t.Fatal(err)
		}
		results = append(results, r)
	}
	ports, err := loadErrCounters("samples/errors.json")
	if err != nil {
		t.Fatal(err)
	}
	attrs := make([]Attribution, 0, len(ports))
	for _, c := range ports {
		attrs = append(attrs, Attribute(c))
	}

	ratesPath, err := writeRates(dir, results)
	if err != nil {
		t.Fatal(err)
	}
	attrPath, err := writeAttribution(dir, attrs)
	if err != nil {
		t.Fatal(err)
	}

	f, err := os.Open(ratesPath)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	lines := 0
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var row rateRow
		if err := json.Unmarshal(sc.Bytes(), &row); err != nil {
			t.Fatalf("rates.jsonl 第 %d 行解析失败：%v", lines+1, err)
		}
		if row.Device == "" || row.Counter == "" {
			t.Fatalf("rates.jsonl 第 %d 行缺端口身份：%s", lines+1, sc.Text())
		}
		lines++
	}
	if lines != 29 {
		t.Errorf("rates.jsonl 有 %d 行，8 条序列共 29 个区间", lines)
	}

	raw, err := os.ReadFile(attrPath)
	if err != nil {
		t.Fatal(err)
	}
	var got []Attribution
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("attribution.json 解析失败：%v", err)
	}
	if len(got) != len(attrs) {
		t.Errorf("attribution.json 有 %d 条，期望 %d 条", len(got), len(attrs))
	}
	if got[0].Owner == "" || got[0].NextStep == "" {
		t.Errorf("归因产物里缺结论或下一步：%+v", got[0])
	}
}

// TestUntrustedIntervalsAreWrittenAsUntrusted 盯住落盘口径：
// 不可信的区间必须带 trusted=false，机器读那一份才不会把它当数用。
func TestUntrustedIntervalsAreWrittenAsUntrusted(t *testing.T) {
	dir := t.TempDir()
	r := mustRates(t, mustSeries(t, "tor-a3f-02/Ethernet1/2"))
	path, err := writeRates(dir, []SeriesRates{r})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, filepath.Base(path)))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var row rateRow
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			t.Fatal(err)
		}
		if row.Trusted {
			t.Errorf("圈数不可判的区间被写成 trusted=true：%s", line)
		}
		if !row.Ambiguous {
			t.Errorf("圈数不可判的区间没带 ambiguous 标记：%s", line)
		}
	}
}

func TestRunRejectsUnmatchedPortFilter(t *testing.T) {
	var buf bytes.Buffer
	err := run(&buf, options{
		samplesPath: "samples/counters.json",
		errPath:     "samples/errors.json",
		portFilter:  "Ethernet9/99",
	})
	if err == nil || !strings.Contains(err.Error(), "没有匹配") {
		t.Fatalf("-port 匹配不到端口时应当报错，实际 %v", err)
	}
}
