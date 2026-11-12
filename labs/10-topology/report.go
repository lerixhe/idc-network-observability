package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// printLinks 打印融合后的链路表。前面几列全 ASCII，中文只出现在下面的说明段，
// 这样终端里对齐不会被宽字符带偏。
func printLinks(w io.Writer, links []Link) {
	fmt.Fprintf(w, "%-4s %-26s %-26s %-9s %-5s %-24s %s\n",
		"#", "a", "b", "kind", "conf", "flags", "sources")
	for i, l := range links {
		fmt.Fprintf(w, "%-4d %-26s %-26s %-9s %-5s %-24s %s\n",
			i+1, l.A.String(), l.B.String(), l.Kind, l.Confidence, linkFlags(l), strings.Join(l.Sources, ","))
	}
}

// linkFlags 把几个布尔压成一列短标记。问号排最前，它是唯一需要人先看的。
func linkFlags(l Link) string {
	var flags []string
	if l.Question {
		flags = append(flags, "?")
	}
	// 「单侧」只对 LLDP 链路有意义：MAC 反推本来就只有一端，谈不上少一半证据。
	if !l.Bidirectional && hasString(l.Sources, SourceLLDP) {
		flags = append(flags, "one-sided")
	}
	if l.Redundant {
		flags = append(flags, "redundant")
	}
	if l.PeerUnknown {
		flags = append(flags, "unknown-peer")
	}
	if len(flags) == 0 {
		return "-"
	}
	return strings.Join(flags, ",")
}

// printTopologySummary 打印拓扑的账：多少条断言进来，多少条链路出去，几个问号。
func printTopologySummary(w io.Writer, t Topology, dir *Directory) {
	byKind := map[string]int{}
	var question, redundant, oneSided int
	for _, l := range t.Links {
		byKind[l.Kind]++
		if l.Question {
			question++
		}
		if l.Redundant {
			redundant++
		}
		if !l.Bidirectional && hasString(l.Sources, SourceLLDP) {
			oneSided++
		}
	}
	fmt.Fprintf(w, "\n链路合计 %d 条：物理 %d / 聚合 %d / 反推 %d；其中问号 %d；冗余 %d；单侧断言 %d\n",
		len(t.Links), byKind[KindPhysical], byKind[KindLag], byKind[KindInferred],
		question, redundant, oneSided)
	fmt.Fprintf(w, "来源账：LLDP %d 行（可用 %d）/ MAC %d 行（用上 %d）——STP 与路由邻居只做校验，不出链路\n",
		t.LLDPRows, t.LLDPUsable, t.MACRows, t.MACUsed)

	for _, l := range t.Links {
		if l.Kind != KindLag {
			continue
		}
		fmt.Fprintf(w, "聚合口对齐：%s\n", l.ID)
		fmt.Fprintf(w, "  LLDP 实际报的对：")
		for i, m := range l.Members {
			if i > 0 {
				fmt.Fprint(w, "、")
			}
			fmt.Fprintf(w, "%s ↔ %s", m.A.String(), m.B.String())
		}
		fmt.Fprintln(w)
		for _, end := range []linkEnd{l.A, l.B} {
			if !dir.IsLag(end.Device, end.Port) {
				continue
			}
			fmt.Fprintf(w, "  台账成员 %s:%s = [%s]\n", end.Device, end.Port,
				strings.Join(dir.Members(end.Device, end.Port), ", "))
		}
	}
}

// printNotices 打印提示清单。冲突与校验信息都在这里，它不改上面任何一行结论。
func printNotices(w io.Writer, items []Notice) {
	notices := sortNotices(items)
	if len(notices) == 0 {
		fmt.Fprintln(w, "\n提示清单：空")
		return
	}
	counts := map[string]int{}
	for _, n := range notices {
		counts[n.Class]++
	}
	classes := make([]string, 0, len(counts))
	for c := range counts {
		classes = append(classes, c)
	}
	sort.Strings(classes)
	fmt.Fprintf(w, "\n提示清单 %d 条（", len(notices))
	for i, c := range classes {
		if i > 0 {
			fmt.Fprint(w, " / ")
		}
		fmt.Fprintf(w, "%s %d", c, counts[c])
	}
	fmt.Fprintln(w, "）：")
	for _, n := range notices {
		fmt.Fprintf(w, "  [%-8s] %-19s %s\n", n.Severity, n.Class, n.Detail)
	}
}

// printEvents 打印去重后的事件表。
func printEvents(w io.Writer, events []Event) {
	fmt.Fprintf(w, "%-4s %-21s %-14s %-13s %-11s %-4s %-30s %s\n",
		"#", "ts", "device", "port", "family", "raw", "fingerprint", "sources")
	for i, e := range events {
		port := e.Port
		if e.Scope == ScopeDevice {
			port = fmt.Sprintf("*%d ports", e.PortCount)
		}
		fmt.Fprintf(w, "%-4d %-21s %-14s %-13s %-11s %-4d %-30s %s\n",
			i+1, e.TS, e.Device, port, e.Family, e.RawCount, e.Fingerprint, strings.Join(e.Sources, ","))
	}
}

// printEventSummary 打印去重的账：原始多少条，压成多少条，压缩比多少。
func printEventSummary(w io.Writer, s EventSet) {
	var portEvents, deviceEvents, rawInEvents int
	for _, e := range s.Events {
		switch e.Scope {
		case ScopeDevice:
			deviceEvents++
		case ScopePort:
			portEvents++
		}
		rawInEvents += e.RawCount
	}
	fmt.Fprintf(w, "\n原始 %d 条 → 端口级 %d 条 → 输出 %d 条（端口 %d + 设备级 %d），归一化失败 %d 条，乱序 %d 条\n",
		s.Raw, s.PortEvents, len(s.Events), portEvents, deviceEvents, s.Unresolved, s.OutOfOrder)
	if s.Raw > 0 {
		fmt.Fprintf(w, "原始条数落到事件上的 %d 条，压缩比 %.1f:1\n",
			rawInEvents, float64(rawInEvents)/float64(maxInt(len(s.Events), 1)))
	}
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// writeJSONL 把一组记录落成 JSONL，一行一条，机器读这份。
func writeJSONL[T any](dir, name string, rows []T) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, name)
	f, err := os.Create(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	enc := json.NewEncoder(f)
	for _, r := range rows {
		if err := enc.Encode(r); err != nil {
			return "", err
		}
	}
	return path, nil
}
