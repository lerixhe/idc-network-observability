package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func sampleVerdicts() []Verdict {
	return []Verdict{
		{
			Device: "sw-a", IfName: "Ethernet1/1", IfIndex: 1, LinkID: "L1",
			Admin: "down", Oper: "down",
			Cause: CauseSelfAdminDown, CauseZH: causeZH[CauseSelfAdminDown],
			Owner: OwnerChange, Evidence: "ifAdminStatus=2（本端手工 shutdown）",
			NextStep:  causeNext[CauseSelfAdminDown],
			FlapCount: 0, Flapping: false,
		},
		{
			Device: "sw-b", IfName: "Ethernet1/9", IfIndex: 9,
			Admin: "up", Oper: "up",
			Cause: CauseLinkOK, CauseZH: causeZH[CauseLinkOK],
			Owner: "", Evidence: "两端 ifOperStatus 均 up",
			FlapCount: 6, Flapping: true,
		},
	}
}

func TestWriteJSONL(t *testing.T) {
	var buf bytes.Buffer
	if err := writeJSONL(&buf, sampleVerdicts()); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("应当写 2 行，实际 %d", len(lines))
	}
	var back Verdict
	if err := json.Unmarshal([]byte(lines[1]), &back); err != nil {
		t.Fatal(err)
	}
	if back.Cause != CauseLinkOK || back.Owner != "" || !back.Flapping || back.FlapCount != 6 {
		t.Errorf("回读不一致: %+v", back)
	}
	var first Verdict
	if err := json.Unmarshal([]byte(lines[0]), &first); err != nil {
		t.Fatal(err)
	}
	if first.NextStep == "" {
		t.Error("down 的端口必须带下一步动作，否则这条判定又推回给人了")
	}
}

func TestWriteVerdictsCreatesDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "out")
	path, err := writeVerdicts(dir, sampleVerdicts())
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(strings.Split(strings.TrimSpace(string(data)), "\n")); n != 2 {
		t.Errorf("文件里应当是 2 行，实际 %d", n)
	}
	if !strings.HasSuffix(path, "verdicts.jsonl") {
		t.Errorf("输出文件名应当是 verdicts.jsonl，实际 %s", path)
	}
}

func TestPrintTable(t *testing.T) {
	var buf bytes.Buffer
	printTable(&buf, sampleVerdicts())
	out := buf.String()

	for _, want := range []string{"device", "port", "cause", "owner", "flips", "evidence"} {
		if !strings.Contains(out, want) {
			t.Errorf("表头缺 %q:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "self_admin_down") {
		t.Errorf("表里应当出现成因标签:\n%s", out)
	}
	// 无异常的端口归属列打 "-"，不能打空成一列错位。
	if !strings.Contains(out, " - ") {
		t.Errorf("无归属的端口应当打 \"-\":\n%s", out)
	}
	if !strings.Contains(out, "6*") {
		t.Errorf("抖动端口应当在翻转次数后打星号:\n%s", out)
	}
	if !strings.Contains(out, "下一步：") {
		t.Errorf("表格里应当带下一步动作:\n%s", out)
	}
}

func TestPrintSummary(t *testing.T) {
	var buf bytes.Buffer
	printSummary(&buf, Analysis{
		Verdicts: sampleVerdicts(),
		Reboots: []Reboot{
			{Device: "sw-b", TS: "2026-05-12T10:03:05Z", UptimeMs: 12_000, Dropped: 6},
		},
		DroppedTraps: 6,
	})
	out := buf.String()
	for _, want := range []string{"归属合计", "变更 1", "无异常 1", "抖动：sw-b Ethernet1/9", "设备重启：sw-b", "共剔除 6 条"} {
		if !strings.Contains(out, want) {
			t.Errorf("汇总里缺 %q:\n%s", want, out)
		}
	}
}
