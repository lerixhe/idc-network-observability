package main

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestPrintCurveTable(t *testing.T) {
	var buf bytes.Buffer
	if err := printCurveTable(&buf, fourMembers(), mustHash(t, "crc32c-mod"), []int{1, 10, 100, 1000}, 1); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{"mode", "probes", "covered", "coverage", "fixed", "random", "budget", "1/4"} {
		if !strings.Contains(out, want) {
			t.Errorf("表格里缺 %q:\n%s", want, out)
		}
	}
	// 固定模式这一行必须出现 4 次 25.0%：1/10/100/1000 四个点都不动。
	if n := strings.Count(out, "25.0%"); n < 4 {
		t.Errorf("固定模式应当有 4 个 25.0%% 的点，实际 %d 个:\n%s", n, out)
	}
}

func TestPrintBudgetTable(t *testing.T) {
	var buf bytes.Buffer
	printBudgetTable(&buf, fourMembers(), mustHash(t, "crc32c-mod"), []int{1, 8}, 1)
	out := buf.String()
	if !strings.Contains(out, "budget") || !strings.Contains(out, "blind") {
		t.Errorf("表头缺列:\n%s", out)
	}
	// 预算 1 的 blind 列应当是三条 member，预算 8 应该覆盖满。
	lines := strings.Split(strings.TrimSpace(out), "\n")
	// 1 行标题 + 1 行表头 + 2 行数据。
	if len(lines) != 4 {
		t.Fatalf("应当有标题 + 表头 + 2 行数据，实际 %d 行:\n%s", len(lines), out)
	}
	if n := strings.Count(lines[2], "m"); n != 3 {
		t.Errorf("预算 1 的盲区应当是 3 条 member，实际 %d:\n%s", n, lines[2])
	}
	if !strings.HasSuffix(lines[3], "-") {
		t.Errorf("预算 8 应当覆盖满，盲区列打 \"-\":\n%s", lines[3])
	}
}

// TestPrintBlindTableFixedBlindCount 表尾那行统计是这张表的结论：
// 4 条 member 里 3 条坏了看不见。
func TestPrintBlindTableFixedBlindCount(t *testing.T) {
	var buf bytes.Buffer
	printBlindTable(&buf, fourMembers(), mustHash(t, "crc32c-mod"), 100, 1)
	out := buf.String()
	if !strings.Contains(out, "fixed 列里 3/4 条是 0.0%") {
		t.Errorf("表尾统计不对:\n%s", out)
	}
	if !strings.Contains(out, "没换五元组") {
		t.Errorf("表尾应当点出「问题在没换五元组」:\n%s", out)
	}
}

// TestPrintNarrativePicksBlindMember 演示段的每一句都要能被前面的计算对上。
func TestPrintNarrativePicksBlindMember(t *testing.T) {
	var buf bytes.Buffer
	set := fourMembers()
	h := mustHash(t, "crc32c-mod")
	idx, err := brokenIndex(set, h, "auto")
	if err != nil {
		t.Fatal(err)
	}
	printNarrative(&buf, set, h, idx, 100, 1)
	out := buf.String()
	if !strings.Contains(out, "覆盖率 1/4 = 25.0%") {
		t.Errorf("演示段里的覆盖率不对:\n%s", out)
	}
	if !strings.Contains(out, "探测侧观测丢包率 0.0%") {
		t.Errorf("演示段里的观测丢包率不对:\n%s", out)
	}
	if !strings.Contains(out, "全丢") {
		t.Errorf("演示段应当点出真实代价:\n%s", out)
	}
}

func TestBrokenIndex(t *testing.T) {
	h := mustHash(t, "crc32c-mod")
	set := fourMembers()
	if _, err := brokenIndex(set, h, "m9"); err == nil {
		t.Error("不存在的 member id 应当报错")
	}
	idx, err := brokenIndex(set, h, "m2")
	if err != nil {
		t.Fatal(err)
	}
	if idx != 2 {
		t.Errorf("brokenIndex(m2) = %d", idx)
	}
	auto, err := brokenIndex(set, h, "auto")
	if err != nil {
		t.Fatal(err)
	}
	// auto 挑的必须是固定五元组打不到的那条，否则演示段自相矛盾。
	hits := Hits(set, h, ModeFixed, 1, 0)
	if hits[auto].Probes != 0 {
		t.Errorf("auto 挑中了 %s，但它收到了固定五元组的包", set.Members[auto].ID)
	}
}

func TestWriteCoverageCSV(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "out")
	rows := []coverageRow{
		{Set: "s", Mode: "fixed", Probes: 1, Covered: 1, Total: 4, Coverage: 0.25},
		{Set: "s", Mode: "random", Probes: 1000, Covered: 4, Total: 4, Coverage: 1},
	}
	path, err := writeCoverageCSV(dir, rows)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(path, "coverage.csv") {
		t.Errorf("输出文件名应当是 coverage.csv，实际 %s", path)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	records, err := csv.NewReader(f).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 3 {
		t.Fatalf("应当 1 行表头 + 2 行数据，实际 %d 行", len(records))
	}
	if records[0][5] != "coverage" {
		t.Errorf("最后一列应当是 coverage，实际 %q", records[0][5])
	}
	if records[2][2] != "1000" || records[2][5] != "1.0000" {
		t.Errorf("第二行数据不对: %v", records[2])
	}
	// 覆盖率必须能反算回去，不能被格式化截断成 0。
	if v, err := strconv.ParseFloat(records[1][5], 64); err != nil || v != 0.25 {
		t.Errorf("覆盖率列不可反算: %q", records[1][5])
	}
}

func TestWriteBlindSpots(t *testing.T) {
	dir := t.TempDir()
	rows := []blindRow{
		{Set: "s", Mode: "fixed", Probes: 100, Broken: "m0", ObservedLoss: 0, Invisible: true},
		{Set: "s", Mode: "fixed", Probes: 100, Broken: "m2", ObservedLoss: 1, Invisible: false},
	}
	path, err := writeBlindSpots(dir, rows)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 {
		t.Fatalf("应当写 2 行，实际 %d", len(lines))
	}
	var back blindRow
	if err := json.Unmarshal([]byte(lines[0]), &back); err != nil {
		t.Fatal(err)
	}
	if back.Broken != "m0" || !back.Invisible || back.ObservedLoss != 0 {
		t.Errorf("回读不一致: %+v", back)
	}
}

// TestRunEndToEnd 跑一遍完整流程，确认没有一步是空的。
func TestRunEndToEnd(t *testing.T) {
	outDir := t.TempDir()
	if err := run("samples/paths.json", "crc32c-mod", "auto", 50, 50, 1, outDir, true); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"coverage.csv", "blindspots.jsonl"} {
		info, err := os.Stat(filepath.Join(outDir, name))
		if err != nil {
			t.Fatalf("%s 没有落盘: %v", name, err)
		}
		if info.Size() == 0 {
			t.Errorf("%s 是空文件", name)
		}
	}
}

// TestRunRejectsBadFlags 参数错了要立刻报错，不要跑出一份看起来正常的假结果。
func TestRunRejectsBadFlags(t *testing.T) {
	if err := run("samples/paths.json", "murmur3", "auto", 50, 10, 1, "", false); err == nil {
		t.Error("未知哈希函数应当报错")
	}
	if err := run("samples/paths.json", "crc32c-mod", "auto", 0, 10, 1, "", false); err == nil {
		t.Error("probes=0 应当报错")
	}
	if err := run("samples/paths.json", "crc32c-mod", "auto", 10, 0, 1, "", false); err == nil {
		t.Error("trials=0 应当报错")
	}
	if err := run("samples/paths.json", "crc32c-mod", "m9", 10, 10, 1, "", false); err == nil {
		t.Error("不存在的 member id 应当报错")
	}
}
