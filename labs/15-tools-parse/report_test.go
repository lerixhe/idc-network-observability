package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 缺失打「-」，设备报的 0 打「0」。这张表能不能用全看这一条。
func TestCellDashVsZero(t *testing.T) {
	p := PortCounters{Counters: map[Field]Counter{
		FieldInErrors: {Value: 0, Unit: UnitCount, Raw: "0"},
	}}
	if got := cell(p, FieldInErrors); got != "0" {
		t.Errorf("设备报的 0 打成了 %q", got)
	}
	if got := cell(p, FieldOutBytes); got != "-" {
		t.Errorf("缺失的字段打成了 %q", got)
	}
}

// 主表要能看出漂移：4.2 段的出口字节是「-」，未知列点名。
func TestPrintCountersShowsGapAndUnknown(t *testing.T) {
	ds := loadSamples(t)
	var buf bytes.Buffer
	printCounters(&buf, ds)
	out := buf.String()

	for _, want := range []string{
		"vendor-c-counters@v2",
		"FecUncorrected",
		"sw-c1f-01",
		"1288412390",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("主表里没有 %q", want)
		}
	}
	if strings.Count(out, "\n") != 13 {
		t.Errorf("主表行数 = %d，想要表头 1 + 端口 12", strings.Count(out, "\n"))
	}
}

// 漂移报告要把「出口字节从 bytes 变成不可得」说出来。
func TestPrintDriftNamesOutBytes(t *testing.T) {
	var buf bytes.Buffer
	printDrift(&buf, loadSamples(t))
	out := buf.String()

	if !strings.Contains(out, "out_bytes：bytes（fw 3.9） → 不可得（fw 4.2）") {
		t.Errorf("漂移报告没说清出口字节的变化：\n%s", out)
	}
}

// 丢弃清单计数必须非零，否则等于没做未知列处理。
func TestPrintDroppedCounts(t *testing.T) {
	var buf bytes.Buffer
	printDropped(&buf, loadSamples(t))
	out := buf.String()

	if !strings.Contains(out, "模板没声明的列（3 个") {
		t.Errorf("丢弃清单计数不对：\n%s", out)
	}
	if !strings.Contains(out, "CRC=0") || !strings.Contains(out, "InMcastPkts=388.1K") {
		t.Errorf("丢弃清单没带上原值：\n%s", out)
	}
}

// 落盘的是统一结构：字段名、单位、模板版本都在。
func TestWriteJSONL(t *testing.T) {
	ds := loadSamples(t)
	dir := t.TempDir()

	path, err := writeJSONL(dir, ds)
	if err != nil {
		t.Fatalf("落盘失败: %v", err)
	}
	if path != filepath.Join(dir, "parsed.jsonl") {
		t.Errorf("路径 = %s", path)
	}

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) != 13 {
		t.Fatalf("JSONL 行数 = %d，想要 12 条端口 + 1 条 mtr", len(lines))
	}

	var first map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &first); err != nil {
		t.Fatalf("第一行不是合法 JSON: %v", err)
	}
	if first["kind"] != "counters" {
		t.Errorf("kind = %v", first["kind"])
	}
	if first["template"] != "vendor-a-counters@v1" {
		t.Errorf("template = %v", first["template"])
	}
	counters, ok := first["counters"].(map[string]any)
	if !ok {
		t.Fatalf("counters 字段类型不对：%T", first["counters"])
	}
	inBytes, ok := counters["in_bytes"].(map[string]any)
	if !ok {
		t.Fatalf("缺 in_bytes：%v", counters)
	}
	if inBytes["unit"] != "bytes" {
		t.Errorf("in_bytes 单位 = %v", inBytes["unit"])
	}
	if inBytes["value"].(float64) != 1288412390 {
		t.Errorf("in_bytes 值 = %v", inBytes["value"])
	}

	var last map[string]any
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &last); err != nil {
		t.Fatalf("最后一行不是合法 JSON: %v", err)
	}
	if last["kind"] != "mtr" || last["verdict"] != "target_loss" {
		t.Errorf("mtr 行 = %v / %v", last["kind"], last["verdict"])
	}
}
