package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// 表格一律「ASCII 列在前，中文尾巴在最后一列」。终端里中文是双宽字符，
// 混在中间对齐会整体错位。

func humanBytes(b int64) string {
	neg := ""
	if b < 0 {
		neg, b = "-", -b
	}
	switch {
	case b >= 1_000_000_000:
		return fmt.Sprintf("%s%.2f GB", neg, float64(b)/1e9)
	case b >= 1_000_000:
		return fmt.Sprintf("%s%.1f MB", neg, float64(b)/1e6)
	case b >= 1_000:
		return fmt.Sprintf("%s%.1f KB", neg, float64(b)/1e3)
	default:
		return fmt.Sprintf("%s%d B", neg, b)
	}
}

func humanBps(bps float64) string {
	switch {
	case bps >= 1e9:
		return fmt.Sprintf("%.1f Gbps", bps/1e9)
	case bps >= 1e6:
		return fmt.Sprintf("%.1f Mbps", bps/1e6)
	case bps >= 1e3:
		return fmt.Sprintf("%.1f kbps", bps/1e3)
	default:
		return fmt.Sprintf("%.0f bps", bps)
	}
}

func pct(v float64) string { return fmt.Sprintf("%.1f%%", v*100) }

// pct2 给误差与抽中率用。0.02% 打成 0.0% 会看着像「没有误差」，
// 而这个实验讲的正好是误差，一位小数不够用。
func pct2(v float64) string { return fmt.Sprintf("%.2f%%", v*100) }

// printSFlowSummary 打一段 sFlow 的样本账：几个 datagram、几个样本、丢在哪里。
func printSFlowSummary(w io.Writer, dgs []SFlowDatagram, st SourceStat) {
	samples := 0
	for _, d := range dgs {
		samples += len(d.Samples)
	}
	fmt.Fprintf(w, "sFlow：%d 个 datagram，%d 个报文头样本 · %s\n", len(dgs), samples, st.Label)
	rates := map[int]int{}
	for _, d := range dgs {
		for _, s := range d.Samples {
			rates[s.SamplingRate]++
		}
	}
	parts := make([]string, 0, len(rates))
	for _, r := range sortedIntKeys(rates) {
		parts = append(parts, fmt.Sprintf("%d×%d", r, rates[r]))
	}
	fmt.Fprintf(w, "  采样率分布：%s\n", strings.Join(parts, "，"))
	fmt.Fprintf(w, "  可信样本 %d / 不可信 %d，覆盖率 %s\n", st.Units, st.DroppedUnits, pct(st.Coverage))
	for _, reason := range sortedReasons(st.DropReasons) {
		fmt.Fprintf(w, "  丢弃 %d 条：%s（%s）\n", st.DropReasons[reason], reason, sflowDropZH[reason])
	}
	fmt.Fprintf(w, "  合计估算 %s / %d 包（样本字节 × 采样率，不是计数值）\n", humanBytes(st.Bytes), st.Packets)
}

// printSourceTable 打一个源的 TopN。第五列的名字随源变：sFlow 是抽到的样本数，
// NetFlow 是记录条数，两个数长得像但含义不同，表头不能都写 samples。
func printSourceTable(w io.Writer, st SourceStat) {
	unitHeader := "units"
	switch st.Name {
	case "sflow":
		unitHeader = "samples"
	case "netflow":
		unitHeader = "records"
	}
	fmt.Fprintf(w, "\n%-3s %-11s %-12s %-7s %-7s %-7s %s\n",
		"#", "bytes", "packets", "share", unitHeader, "±误差", "flow")
	for i, e := range st.Entries {
		errText := "-"
		if e.RelErr > 0 {
			errText = pct(e.RelErr)
		}
		fmt.Fprintf(w, "%-3d %-11s %-12d %-7s %-7d %-7s %s\n",
			i+1, humanBytes(e.Bytes), e.Packets, pct(e.Share), e.Samples, errText, e.Key.String())
	}
}

// printSegments 打 NetFlow/IPFIX 的段账。可信与否是逐段判的，
// 所以表里一行是一段，不是一条记录。
func printSegments(w io.Writer, res DecodeResult, ttl time.Duration) {
	fmt.Fprintf(w, "NetFlow/IPFIX：%d 个 export set 段，%d 次模板定义，模板寿命口径 %s\n",
		len(res.Segments), res.TemplateDefs, templateTTLText(ttl))
	fmt.Fprintf(w, "%-12s %-4s %-21s %-5s %-7s %-6s %s\n",
		"exporter", "seq", "ts", "tmpl", "ok/drop", "verdict", "依据")
	for _, s := range res.Segments {
		verdict := "可信"
		if !s.Trusted {
			verdict = "不可信"
		}
		note := s.Note
		if note == "" {
			note = "-"
		}
		fmt.Fprintf(w, "%-12s %-4d %-21s %-5d %-7s %-6s %s（%s）\n",
			s.Exporter, s.Seq, s.TS, s.TemplateID,
			fmt.Sprintf("%d/%d", s.Records, s.Dropped), verdict, note, reasonZH[s.Reason])
	}
	for _, e := range res.Events {
		fmt.Fprintf(w, "事件：%s %s %s\n", e.TS, e.Exporter, e.Detail)
	}
}

// printSampling 打某个采样率下的一次抽样与真值对账。
func printSampling(w io.Writer, r RateResult) {
	sampled := 0.0
	if r.TruePackets > 0 {
		sampled = float64(r.SampledPackets) / float64(r.TruePackets)
	}
	fmt.Fprintf(w, "\n采样率 1:%d · 窗口 %.0fs · 抽中 %d / %d 个包（%s）· 最小可观测 %.1f 包/秒（%s）\n",
		r.N, r.WindowSeconds, r.SampledPackets, r.TruePackets, pct2(sampled),
		r.MinObservablePPS, humanBps(r.MinObservableBps))
	fmt.Fprintf(w, "%-3s %-11s %-11s %-7s %-7s %-6s %-6s %s\n",
		"#", "est bytes", "true bytes", "±误差", "samples", "true#", "est#", "flow")
	for _, f := range r.Flows {
		if f.EstRank > r.TopN {
			continue
		}
		errText := "-"
		if f.Sampled > 0 {
			errText = pct(f.RelErr)
		}
		fmt.Fprintf(w, "%-3d %-11s %-11s %-7s %-7d %-6d %-6d %s %s\n",
			f.EstRank, humanBytes(f.EstBytes), humanBytes(f.TrueBytes), errText,
			f.Sampled, f.TrueRank, f.EstRank, f.Name, f.Key.String())
	}

	// TopN 失真：估算榜上有名字、真值榜上前 N 没有的，一条条列出来。
	var noise []FlowEstimate
	for _, f := range r.Flows {
		if f.EstRank <= r.TopN && f.TrueRank > r.TopN {
			noise = append(noise, f)
		}
	}
	if len(noise) > 0 {
		parts := make([]string, 0, len(noise))
		for _, f := range noise {
			parts = append(parts, fmt.Sprintf("est#%d %s（真值 #%d）", f.EstRank, f.Name, f.TrueRank))
		}
		fmt.Fprintf(w, "  TopN 失真：%s\n", strings.Join(parts, "，"))
	}
	fmt.Fprintf(w, "  可见流 %d/%d（%d 条一个样本都没抽到）· TopN 交集 %d/%d · 大象估算误差 %s\n",
		r.Visible, r.Total, r.Total-r.Visible, r.TopNOverlap, r.TopN,
		pct2(r.ElephantErr))
	fmt.Fprintf(w, "  大象 %s：真值 %s，估算 %s\n",
		r.ElephantName, humanBytes(r.ElephantTrue), humanBytes(r.ElephantEst))
}

// printVisibility 打可见性阈值表。这张表是这一课的落点：
// 换一个采样率，第一条流就要重新算一遍它到底还看得见吗。
func printVisibility(w io.Writer, sim Simulation) {
	fmt.Fprintf(w, "\n最小可观测流速率（r_min = k·N/T，窗口 T=%.0fs，包长 %.0f 字节）\n",
		sim.WindowSeconds, sim.MeanPacketBytes)
	fmt.Fprint(w, "pps = 包/秒，bw = 按包长折算的带宽\n")
	fmt.Fprintf(w, "%-8s %-14s %-13s %-14s %s\n",
		"rate", "k=1 pps", "k=1 bw", "k=5 pps", "k=5 bw")
	for _, r := range sim.Rates {
		p1 := MinObservablePPS(1, r.N, sim.WindowSeconds)
		p5 := MinObservablePPS(5, r.N, sim.WindowSeconds)
		fmt.Fprintf(w, "%-8s %-14.1f %-13s %-14.1f %s\n",
			fmt.Sprintf("1:%d", r.N), p1, humanBps(p1*sim.MeanPacketBytes*8),
			p5, humanBps(p5*sim.MeanPacketBytes*8))
	}
}

// 产物结构。TopN 与采样是两个文件：一个是解析结果，一个是模拟结果，
// 混在一个文件里，读的人分不清哪个数是采到的、哪个是算出来的。
type topnDoc struct {
	Note    string       `json:"note"`
	Sources []SourceStat `json:"sources"`
}

func writeTopN(outDir string, stats []SourceStat) (string, error) {
	doc := topnDoc{
		Note:    "sFlow 的字节数是样本长度 × 采样率（估算），NetFlow 的是 exporter 聚合计数；两个源的数字不可相加",
		Sources: stats,
	}
	path := filepath.Join(outDir, "topn.json")
	if err := writeJSON(path, doc); err != nil {
		return "", err
	}
	return path, nil
}

func writeSampling(outDir string, sim Simulation) (string, error) {
	path := filepath.Join(outDir, "sampling.json")
	if err := writeJSON(path, sim); err != nil {
		return "", err
	}
	return path, nil
}

func writeJSON(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(path, data, 0o644)
}

// sortedIntKeys 让 map[int]int 的输出顺序稳定。
func sortedIntKeys(m map[int]int) []int {
	out := make([]int, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Ints(out)
	return out
}

// sortedReasons 让 map 的输出顺序稳定，测试才能断言。产物与终端都得是同一份顺序。
func sortedReasons(m map[string]int64) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// templateTTLText 把模板寿命打成一行，让输出自报口径。
func templateTTLText(ttl time.Duration) string {
	if ttl%time.Minute == 0 {
		return fmt.Sprintf("%.0f 分钟", ttl.Minutes())
	}
	return ttl.String()
}
