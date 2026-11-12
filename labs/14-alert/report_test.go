package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPrintOutcomesMarksRootAndSuppressed(t *testing.T) {
	list := []Outcome{
		{ID: "a1", TS: "2026-05-12T10:00:00Z", Obj: "device:d1", Node: "device:d1",
			Class: ClassDeviceReset, Severity: "critical"},
		{ID: "a2", TS: "2026-05-12T10:00:10Z", Obj: "port:d1:p1", Node: "port:d1:p1",
			Class: ClassPortDown, Severity: "critical", Suppressed: true, SuppressedBy: "a1"},
		{ID: "a3", TS: "2026-05-12T10:00:20Z", Obj: "acc-x:Eth1/2",
			Class: ClassPortDown, Severity: "critical", Suppressed: true, SuppressedBy: "a1", Degraded: true},
	}

	var buf bytes.Buffer
	printOutcomes(&buf, list)
	out := buf.String()

	for _, want := range []string{"#", "ts", "object", "class", "sev", "result"} {
		if !strings.Contains(out, want) {
			t.Errorf("表头缺列 %q:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "ROOT") {
		t.Error("没标出自己报出去的告警")
	}
	if !strings.Contains(out, "<- a1") {
		t.Error("没标出被谁压住")
	}
	if !strings.Contains(out, "(窗)") {
		t.Error("没标出靠时间窗压的那条")
	}
	if !strings.Contains(out, "acc-x:Eth1/2(?)") {
		t.Error("对象不在拓扑里时没打问号")
	}
}

func TestPrintSuppressSummaryHasCheckableNumbers(t *testing.T) {
	res := SuppressResult{
		Alerts: 87, Emitted: 5, Suppressed: 85, NetConverged: 82, Resumed: 3,
		Collateral: 1, TopSuppressor: "a-0001", TopSuppressorCnt: 85,
	}
	var buf bytes.Buffer
	printSuppressSummary(&buf, "收敛账：", res, defaultSuppressPolicy)
	out := buf.String()

	for _, want := range []string{"87", "5", "17.40:1", "94.3%", "85", "a-0001"} {
		if !strings.Contains(out, want) {
			t.Errorf("汇总里找不到 %q:\n%s", want, out)
		}
	}
}

// 误伤必须单独列一段。它不列出来，就永远没人看。
func TestPrintCollateralNamesTheVictims(t *testing.T) {
	res := SuppressResult{
		Alerts: 3, Emitted: 2, Suppressed: 1, NetConverged: 1, Collateral: 1,
		Outcomes: []Outcome{
			{ID: "a1", Suppressed: false},
			{ID: "a2", Obj: "port:d1:p7", Truth: TruthIndependent,
				Suppressed: true, SuppressedBy: "a1"},
			{ID: "a3", Suppressed: false},
		},
	}
	var buf bytes.Buffer
	printCollateral(&buf, res)
	out := buf.String()
	if !strings.Contains(out, "a2") || !strings.Contains(out, "independent") {
		t.Fatalf("误伤清单没点名：\n%s", out)
	}

	clean := SuppressResult{
		Alerts: 1, Emitted: 1,
		Outcomes: []Outcome{{ID: "a1"}},
	}
	buf.Reset()
	printCollateral(&buf, clean)
	if !strings.Contains(buf.String(), "空") {
		t.Fatalf("没有误伤时应当明说：\n%s", buf.String())
	}
}

func TestPrintRulesShowsThreeRules(t *testing.T) {
	rep := Evaluate([]Series{
		seriesOf("x:rx_bps", seq(0, 30, 60, 60, 95, 96, 97), []Span{{From: 60, To: 120}}),
	}, defaultRulePolicy)

	var buf bytes.Buffer
	printRules(&buf, rep)
	out := buf.String()
	for _, want := range []string{RuleThreshold, RuleSustained, RuleRate, "30s", "采样"} {
		if !strings.Contains(out, want) {
			t.Errorf("规则表里找不到 %q:\n%s", want, out)
		}
	}
}

func TestWriteJSONL(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "out")
	rows := []any{
		Outcome{ID: "a1", Obj: "device:d1", Suppressed: true, SuppressedBy: "a0"},
	}
	path, err := writeJSONL(dir, "x.jsonl", rows)
	if err != nil {
		t.Fatalf("落盘失败: %v", err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读回失败: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) != 1 {
		t.Fatalf("行数 %d，想要 1", len(lines))
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &got); err != nil {
		t.Fatalf("不是合法 JSON: %v", err)
	}
	if got["suppressed_by"] != "a0" {
		t.Fatalf("可以追溯的字段丢了：%v", got)
	}
}

func TestTruncate(t *testing.T) {
	cases := []struct {
		in   string
		n    int
		want string
	}{
		{"abc", 5, "abc"},
		{"abcdef", 4, "abc…"},
		{"abcdef", 1, "a"},
	}
	for _, c := range cases {
		if got := truncate(c.in, c.n); got != c.want {
			t.Errorf("truncate(%q, %d) = %q，想要 %q", c.in, c.n, got, c.want)
		}
	}
}

func TestFmtTSRoundTrip(t *testing.T) {
	ms, err := parseTS("2026-05-12T10:00:00Z")
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if got := fmtTS(ms); got != "2026-05-12T10:00:00Z" {
		t.Fatalf("回写 %q", got)
	}
}
