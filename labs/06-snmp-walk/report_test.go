package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func reportFor(t *testing.T, cycles int) ReportInput {
	t.Helper()
	before := collectOnce(t, "samples/fleet.json", cycles, nil)
	after := collectOnce(t, "samples/fleet-rebooted.json", 1, nil)
	return ReportInput{
		Collector:   before,
		Rebooted:    after,
		SampleFile:  "samples/fleet.json",
		RebootFile:  "samples/fleet-rebooted.json",
		OutDir:      t.TempDir(),
		FocusDevice: "spine-01",
		WireKind:    "sample",
		ClockKind:   "虚拟",
		WallTime:    time.Millisecond,
	}
}

// 报表要能一个人从头读到尾：脚本、耗时、隔离、端口表、重排。
func TestPrintReportSections(t *testing.T) {
	var buf bytes.Buffer
	PrintReport(&buf, reportFor(t, 6))
	out := buf.String()

	musts := []string{
		"传输 sample",
		"包量与耗时",
		"按设备汇总",
		"限流与隔离",
		"排队不是丢包",
		"端口表（spine-01",
		"ifSpeed 读出 4294967295",
		"换板重启之后",
		"按 ifName 对接",
		"曲线不会断",
	}
	for _, m := range musts {
		if !strings.Contains(out, m) {
			t.Fatalf("报表里缺少 %q", m)
		}
	}
	// 关键数字必须出现在表里。
	for _, m := range []string{"不可达", "响应慢", "tor-b7f-01", "leaf-legacy-01", "隔离"} {
		if !strings.Contains(out, m) {
			t.Fatalf("报表里缺少 %q", m)
		}
	}
}

// 没给重排样本时，报表不许打一节空的重排。
func TestPrintReportWithoutRebootSample(t *testing.T) {
	in := reportFor(t, 2)
	in.Rebooted = nil
	var buf bytes.Buffer
	PrintReport(&buf, in)
	if strings.Contains(buf.String(), "换板重启之后") {
		t.Fatal("没有重排样本时不该打这一节")
	}
}

func TestWriteOutputs(t *testing.T) {
	in := reportFor(t, 3)
	paths, err := WriteOutputs(in)
	if err != nil {
		t.Fatalf("写产物失败: %v", err)
	}
	if len(paths) != 3 {
		t.Fatalf("产物 %d 份，期望 3（指标、账本、重排）", len(paths))
	}

	// 指标：每轮每端口一行，key 是 device/ifName。
	raw, err := os.ReadFile(filepath.Join(in.OutDir, "if_metrics.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	var first MetricLine
	if err := json.Unmarshal([]byte(lines[0]), &first); err != nil {
		t.Fatalf("指标行不是 JSON: %v", err)
	}
	if first.Key != first.Device+"/"+first.IfName {
		t.Fatalf("key = %q，应等于 device/ifName", first.Key)
	}
	if first.IfIndex == 0 || first.IfName == "" {
		t.Fatalf("指标行缺字段: %+v", first)
	}
	// 3 轮 × 每轮采到的端口（24+24+24），不可达那台一行都没有；
	// 慢设备第 2 轮判慢、第 3 轮被降频跳过，所以只算两轮。
	want := 3*(24+24+24) + 2*12
	if len(lines) != want {
		t.Fatalf("指标行数 = %d，期望 %d", len(lines), want)
	}

	// 账本：档位与每轮 PDU 都要在。
	var doc CollectDoc
	raw, err = os.ReadFile(filepath.Join(in.OutDir, "collect.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("账本不是 JSON: %v", err)
	}
	if doc.Sample != "samples/fleet.json" || doc.Wire != "sample" {
		t.Fatalf("账本没记来源: %+v", doc)
	}
	if len(doc.Devices) != 5 {
		t.Fatalf("账本里 %d 台设备，期望 5", len(doc.Devices))
	}
	for _, d := range doc.Devices {
		if d.Policy.MaxRepetitions == 0 || d.Policy.RequestsPerSec == 0 {
			t.Fatalf("%s 的档位参数没落盘", d.Name)
		}
		if len(d.Rounds) != 3 {
			t.Fatalf("%s 记了 %d 轮，期望 3", d.Name, len(d.Rounds))
		}
		if d.Name == "tor-a3f-02" && d.Rounds[0].QueueMs == 0 {
			t.Fatal("C 档设备应该有排队时间")
		}
	}

	// 重排：三把键的差异数字都要能复核。
	var reindex struct {
		Devices []ReindexDoc `json:"devices"`
	}
	raw, err = os.ReadFile(filepath.Join(in.OutDir, "reindex.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &reindex); err != nil {
		t.Fatalf("重排产物不是 JSON: %v", err)
	}
	if len(reindex.Devices) != 2 {
		t.Fatalf("重排产物里 %d 台设备，期望 2", len(reindex.Devices))
	}
	for _, d := range reindex.Devices {
		switch d.Device {
		case "tor-a3f-01":
			if d.ByIndex.WrongObjects != 16 || d.ByIndex.Dropped != 8 || d.ByIndex.Fresh != 8 {
				t.Fatalf("tor-a3f-01 按 ifIndex 的差异 = %+v", d.ByIndex)
			}
			if d.ByName.WrongObjects != 0 || d.ByAlias.WrongObjects != 0 {
				t.Fatalf("tor-a3f-01 按名字不该错对象: name=%+v alias=%+v", d.ByName, d.ByAlias)
			}
			if len(d.Reindexes) != 16 {
				t.Fatalf("重排点 = %d，期望 16", len(d.Reindexes))
			}
		case "leaf-legacy-01":
			if d.ByName.Dropped != 12 || d.ByAlias.Dropped != 0 {
				t.Fatalf("leaf-legacy-01 的 ifName 应该全断、alias 全接上: %+v / %+v", d.ByName, d.ByAlias)
			}
		}
	}
}

func TestWriteOutputsCreatesDir(t *testing.T) {
	in := reportFor(t, 1)
	in.OutDir = filepath.Join(in.OutDir, "nested", "out")
	if _, err := WriteOutputs(in); err != nil {
		t.Fatalf("产物目录不存在时应自动建: %v", err)
	}
	if _, err := os.Stat(filepath.Join(in.OutDir, "collect.json")); err != nil {
		t.Fatalf("账本没写出来: %v", err)
	}
}
