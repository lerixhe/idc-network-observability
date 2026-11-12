package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPrintLinks(t *testing.T) {
	links := []Link{
		{
			ID:   "spine-01:Port-Channel10 ↔ tor-a3f-01:Eth-Trunk1",
			A:    linkEnd{Device: "spine-01", Port: "Port-Channel10"},
			B:    linkEnd{Device: "tor-a3f-01", Port: "Eth-Trunk1"},
			Kind: KindLag, Confidence: ConfHigh, Bidirectional: true,
			Sources: []string{SourceLLDP},
		},
		{
			ID:   "tor-a3f-04:Ethernet1/1 ↔ spine-01:?",
			A:    linkEnd{Device: "tor-a3f-04", Port: "Ethernet1/1"},
			B:    linkEnd{Device: "spine-01"},
			Kind: KindInferred, Confidence: ConfLow, Question: true,
			Sources: []string{SourceMAC},
		},
	}

	var buf bytes.Buffer
	printLinks(&buf, links)
	out := buf.String()

	for _, want := range []string{"kind", "conf", "flags", "spine-01:Port-Channel10", "tor-a3f-01:Eth-Trunk1"} {
		if !strings.Contains(out, want) {
			t.Errorf("表格里缺 %q：\n%s", want, out)
		}
	}
	// 反推链路的对端端口未知，必须打问号；它也只有 MAC 一个来源，不该标 one-sided。
	if !strings.Contains(out, "lag       high  -") {
		t.Errorf("聚合链路不该带 flags：\n%s", out)
	}
	if !strings.Contains(out, "inferred  low   ?") {
		t.Errorf("反推链路要打问号：\n%s", out)
	}
}

func TestLinkFlagsOneSidedOnlyForLLDP(t *testing.T) {
	lldpLink := Link{Sources: []string{SourceLLDP}, Bidirectional: false}
	if got := linkFlags(lldpLink); got != "one-sided" {
		t.Errorf("LLDP 单侧断言应当标 one-sided，实际 %q", got)
	}
	macLink := Link{Sources: []string{SourceMAC}, Question: true}
	if got := linkFlags(macLink); got != "?" {
		t.Errorf("MAC 反推只该打问号，实际 %q", got)
	}
	full := Link{Sources: []string{SourceLLDP}, Bidirectional: true}
	if got := linkFlags(full); got != "-" {
		t.Errorf("双向确认的链路不该有 flags，实际 %q", got)
	}
}

func TestPrintEventsBurstRow(t *testing.T) {
	events := []Event{{
		TS: "2026-05-12T10:14:00Z", Device: "tor-a3f-03", Family: FamilyLinkDown,
		Scope: ScopeDevice, PortCount: 48, RawCount: 192,
		Sources:     []string{"counter", "probe", "syslog", "trap"},
		Fingerprint: "link_down|tor-a3f-03|*",
	}}
	var buf bytes.Buffer
	printEvents(&buf, events)
	out := buf.String()

	if !strings.Contains(out, "*48 ports") {
		t.Errorf("设备级事件要显示端口计数：\n%s", out)
	}
	if !strings.Contains(out, "192") {
		t.Errorf("设备级事件要显示原始条数：\n%s", out)
	}
	if !strings.Contains(out, "link_down|tor-a3f-03|*") {
		t.Errorf("指纹要打出来：\n%s", out)
	}
}

func TestPrintNoticesWarnFirst(t *testing.T) {
	notices := []Notice{
		{Class: NoticeSTPBlocked, Severity: SevInfo, Subject: "a", Detail: "info one"},
		{Class: NoticePeerMismatch, Severity: SevWarn, Subject: "b", Detail: "warn one"},
	}
	var buf bytes.Buffer
	printNotices(&buf, notices)
	out := buf.String()

	if strings.Index(out, "warn one") > strings.Index(out, "info one") {
		t.Errorf("warn 要排在 info 前面：\n%s", out)
	}
	if !strings.Contains(out, "peer_mismatch 1") {
		t.Errorf("要按分类统计条数：\n%s", out)
	}
}

func TestPrintTopologySummaryLagDetail(t *testing.T) {
	dir := mustDirectory(t)
	tp := Topology{
		Links: []Link{{
			ID:   "spine-01:Port-Channel10 ↔ tor-a3f-01:Eth-Trunk1",
			A:    linkEnd{Device: "spine-01", Port: "Port-Channel10"},
			B:    linkEnd{Device: "tor-a3f-01", Port: "Eth-Trunk1"},
			Kind: KindLag, Confidence: ConfHigh, Bidirectional: true,
			Members: []MemberPair{{
				A: linkEnd{Device: "spine-01", Port: "Ethernet1/10"},
				B: linkEnd{Device: "tor-a3f-01", Port: "Eth-Trunk1"},
			}},
		}},
		LLDPRows: 17, LLDPUsable: 16, MACRows: 4, MACUsed: 3,
	}

	var buf bytes.Buffer
	printTopologySummary(&buf, tp, dir)
	out := buf.String()

	if !strings.Contains(out, "聚合 1") {
		t.Errorf("要按类型分类计数：\n%s", out)
	}
	// 逻辑口对齐了，成员口还要列出台账里的归属。
	for _, want := range []string{"LLDP 实际报的对", "台账成员 spine-01:Port-Channel10 = [Ethernet1/10, Ethernet1/11]"} {
		if !strings.Contains(out, want) {
			t.Errorf("聚合口明细缺 %q：\n%s", want, out)
		}
	}
}

func TestWriteJSONL(t *testing.T) {
	dir := t.TempDir()
	rows := []Notice{
		{Class: NoticeSTPBlocked, Severity: SevInfo, Subject: "x", Detail: "第一条"},
		{Class: NoticePeerMismatch, Severity: SevWarn, Subject: "y", Detail: "第二条"},
	}
	path, err := writeJSONL(dir, "notices.jsonl", rows)
	if err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 {
		t.Fatalf("JSONL 应当两行，实际 %d 行", len(lines))
	}
	for i, line := range lines {
		var n Notice
		if err := json.Unmarshal([]byte(line), &n); err != nil {
			t.Fatalf("第 %d 行不是合法 JSON: %v", i+1, err)
		}
		if n.Detail != rows[i].Detail {
			t.Errorf("第 %d 行 detail = %q，期望 %q", i+1, n.Detail, rows[i].Detail)
		}
	}
}

func TestWriteJSONLCreatesDir(t *testing.T) {
	out := filepath.Join(t.TempDir(), "nested", "out")
	if _, err := writeJSONL(out, "links.jsonl", []Link{}); err != nil {
		t.Fatalf("目录不存在时应当自动建: %v", err)
	}
}
