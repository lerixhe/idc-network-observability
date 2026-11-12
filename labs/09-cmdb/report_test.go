package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPrintDiffsShape(t *testing.T) {
	diffs := []Diff{{
		ID: "Z1", Kind: DiffZombie, Target: "tor-3f-07", Confidence: ConfHigh,
		Evidence: []string{"LLDP 快照里没有它", "ARP 表里没有 10.20.3.17"},
		Action:   ActRetire, ActionZH: actionZH[ActRetire],
	}}
	var buf bytes.Buffer
	printDiffs(&buf, "僵尸设备", diffs)
	out := buf.String()

	for _, want := range []string{"僵尸设备 · 1 条", "Z1", "tor-3f-07", ConfHigh, "LLDP 快照里没有它", "建议：标记 lifecycle=retired"} {
		if !strings.Contains(out, want) {
			t.Errorf("输出里没有 %q:\n%s", want, out)
		}
	}
	// 第二条判据要缩进到最后一列下面，不能挤回 id 那一列。
	var cont string
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "ARP 表里没有") {
			cont = line
		}
	}
	if cont == "" {
		t.Fatalf("找不到第二条判据那一行:\n%s", out)
	}
	if !strings.HasPrefix(cont, "                            ") {
		t.Errorf("续行没有缩进对齐: %q", cont)
	}
}

func TestPrintDiffsEmpty(t *testing.T) {
	var buf bytes.Buffer
	printDiffs(&buf, "幽灵设备", nil)
	if !strings.Contains(buf.String(), "幽灵设备 · 0 条") || !strings.Contains(buf.String(), "无") {
		t.Errorf("空段落要打出来并写明无差异:\n%s", buf.String())
	}
}

func TestPrintPatchesShape(t *testing.T) {
	patches := []Patch{{
		ID: "P01", DiffID: "Z1", Op: OpDeviceLifecycle, Target: "agg-core-01",
		Set: map[string]string{"lifecycle": LifecycleRetired}, Confidence: ConfHigh,
	}}
	var buf bytes.Buffer
	printPatches(&buf, patches, true)
	out := buf.String()
	for _, want := range []string{"建议写入的补丁 · 1 条", "dry-run，未落库", "P01", OpDeviceLifecycle, "agg-core-01", "lifecycle=retired"} {
		if !strings.Contains(out, want) {
			t.Errorf("输出里没有 %q:\n%s", want, out)
		}
	}
}

func TestWriteJSONLRoundTrip(t *testing.T) {
	dir := t.TempDir()
	items := []Diff{
		{ID: "M1", Kind: DiffMismatch, Target: "spine-01#Ethernet1/5", Confidence: ConfHigh,
			Evidence: []string{"台账登记 A", "LLDP 观测 B"}, Action: ActFixPeer,
			Fix: map[string]string{"peer_port": "Ethernet1/1"}},
		{ID: "M2", Kind: DiffMismatch, Target: "spine-02#Ethernet1/7", Confidence: ConfMedium,
			Evidence: []string{"台账登记 C"}, Action: ActFixPeer},
	}
	path, err := writeJSONL(dir, "diffs.jsonl", items)
	if err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) != len(items) {
		t.Fatalf("JSONL 应当一行一条，期望 %d 行，实际 %d", len(items), len(lines))
	}
	for i, line := range lines {
		var got Diff
		if err := json.Unmarshal([]byte(line), &got); err != nil {
			t.Fatalf("第 %d 行解析失败: %v", i+1, err)
		}
		if got.ID != items[i].ID || got.Target != items[i].Target {
			t.Errorf("第 %d 行与预期不符: %+v", i+1, got)
		}
	}
}

func TestWriteJSONLCreatesDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "out")
	if _, err := writeJSONL(dir, "cmdb-patch.jsonl", []Patch{}); err != nil {
		t.Fatalf("目录不存在时应当自己建: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "cmdb-patch.jsonl")); err != nil {
		t.Error("文件没落盘")
	}
}
