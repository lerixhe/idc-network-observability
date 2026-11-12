package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// shortIf 把接口名压短。Ethernet1/49 在表格里太占地方，Et1/49 一样能认。
func shortIf(name string) string {
	if name == "" {
		return "-"
	}
	return "Et" + strings.TrimPrefix(name, "Ethernet")
}

// fmtBytes 把字节数换成 KiB/MiB。队列深度是 2 的幂量级，不用十进制前缀。
func fmtBytes(b int64) string {
	switch {
	case b >= 100<<20:
		return fmt.Sprintf("%.0f MiB", float64(b)/(1<<20))
	case b >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(b)/(1<<20))
	case b >= 1<<10:
		return fmt.Sprintf("%.1f KiB", float64(b)/(1<<10))
	default:
		return fmt.Sprintf("%d B", b)
	}
}

// dispWidth 按终端列宽算字符串宽度：CJK 记两列，其余记一列。
// 中文标签不打平宽度，后面那列数值就永远对不齐。
func dispWidth(s string) int {
	w := 0
	for _, r := range s {
		if r > 0x2E80 {
			w += 2
			continue
		}
		w++
	}
	return w
}

// padLabel 把标签补空格到指定列宽。
func padLabel(s string, width int) string {
	if n := width - dispWidth(s); n > 0 {
		return s + strings.Repeat(" ", n)
	}
	return s
}

// fmtBps 把出口速率换成人读的档位。
func fmtBps(bps int64) string {
	if bps%1_000_000_000 == 0 {
		return fmt.Sprintf("%dG", bps/1_000_000_000)
	}
	return fmt.Sprintf("%.1fG", float64(bps)/1e9)
}

// fmtUs 打微秒，一位小数。全部时间列统一用 µs，避免 µs 与 ms 混着看串。
func fmtUs(ns int64) string { return fmt.Sprintf("%.1f", us(ns)) }

// fmtSigned 打带符号的误差，"+299.4" 比 "299.4" 少一次心算。
func fmtSigned(ns int64) string {
	if ns >= 0 {
		return "+" + fmtUs(ns)
	}
	return fmtUs(ns)
}

// printTraceHeader 打印一条 trace 的身份与覆盖情况。
func printTraceHeader(w io.Writer, d Decomposition, t Trace) {
	fmt.Fprintf(w, "== %s · %s\n", d.TraceID, d.Desc)
	fmt.Fprintf(w, "   %s %s → %s · %d 跳 · INT 覆盖 %d/%d 跳（%.0f%%）\n\n",
		t.Flow.Proto, t.Flow.Src, t.Flow.Dst,
		d.HopCount, d.Instrumented, d.HopCount, d.HopCoverage*100)
}

// printHopTable 打印逐跳分解。时间列单位 µs，深度按 KiB/MiB。
//
// 速度列不能省：同样是 38 MiB 的队列，100G 口是 3.2 ms，25G 口是 12.9 ms。
// 只看字节数会把两条完全不慢的链路看上同一个刻度。
//
// 驻留是精确量，排队是估的，处理是减出来的——三列的可信度不一样，
// 表头那句话就是为了提醒这件事。
func printHopTable(w io.Writer, d Decomposition) {
	fmt.Fprintln(w, "逐跳分解（时间 µs；驻留 = 出口时间戳 − 入口时间戳，同一设备时钟，偏移抵消）")
	fmt.Fprintf(w, "%-4s %-14s %-14s %5s %9s %7s %9s %9s %10s  %s\n",
		"hop", "device", "in→out", "speed", "depth", "link", "queue", "forward", "resident", "attribution")
	for _, h := range d.Hops {
		ports := shortIf(h.IngressPort) + "→" + shortIf(h.EgressPort)
		if !h.HasData {
			fmt.Fprintf(w, "%-4d %-14s %-14s %5s %9s %7s %9s %9s %10s  %s\n",
				h.Index, h.Device, ports, fmtBps(h.EgressBps), "-", fmtUs(h.LinkPropNs), "-", "-", "-",
				attributionZH[AttrBlind]+"（无 INT 数据）")
			continue
		}
		share := 0.0
		if h.ResidentNs > 0 {
			share = float64(h.QueueNs) / float64(h.ResidentNs) * 100
		}
		attr := attributionZH[h.Attribution]
		if h.Attribution == AttrQueue {
			attr = fmt.Sprintf("%s %.0f%%", attr, share)
		}
		fmt.Fprintf(w, "%-4d %-14s %-14s %5s %9s %7s %9s %9s %10s  %s\n",
			h.Index, h.Device, ports, fmtBps(h.EgressBps), fmtBytes(h.QueueDepthBytes), fmtUs(h.LinkPropNs),
			fmtUs(h.QueueNs), fmtUs(h.ForwardNs), fmtUs(h.ResidentNs), attr)
		if h.Note != "" {
			fmt.Fprintf(w, "     %s\n", h.Note)
		}
	}
	fmt.Fprintln(w)
}

// printBreakdown 打印时延分解的算式与结果。每一行都能自己算一遍。
func printBreakdown(w io.Writer, d Decomposition, t Trace) {
	fmt.Fprintln(w, "时延分解（单位 µs）")
	row := func(label, value, note string) {
		fmt.Fprintf(w, "  %s %10s   %s\n", padLabel(label, 14), value, note)
	}
	row("端到端", fmtUs(d.E2EMeasuredNs), "末跳出口 − 首跳入口，含时钟偏移差")
	row("时钟误差上界", fmtUs(d.ClockErrNs),
		fmt.Sprintf("%s + %s 两个时钟域的精度", t.Hops[0].ClockDomain, t.Hops[len(t.Hops)-1].ClockDomain))
	row("已归因", fmtUs(d.SumResidentNs),
		fmt.Sprintf("驻留合计，占端到端 %.1f%%，与时钟无关", d.AttributedShare*100))
	row("链路传播", fmtUs(d.PropNs), fmt.Sprintf("台账长度 %d m × 5 ns/m", totalLength(t)))
	row("差额", fmtUs(d.GapNs), "端到端 − 已归因")
	row("盲区驻留", fmtUs(d.BlindResidentNs),
		fmt.Sprintf("差额 − 传播，区间 [%s, %s]", fmtUs(d.BlindLoNs), fmtUs(d.BlindHiNs)))
	fmt.Fprintln(w)
}

// totalLength 汇总全路径的链路长度，给「链路传播」那行凑公式。
func totalLength(t Trace) int {
	sum := 0
	for _, m := range t.linksOf() {
		sum += m
	}
	return sum
}

// printSegments 打印盲区段。这是「部分覆盖」时唯一能说的话。
func printSegments(w io.Writer, d Decomposition) {
	fmt.Fprintln(w, "盲区段（段 = 上一个有数据的跳 → 下一个有数据的跳）")
	if len(d.Segments) == 0 {
		fmt.Fprintf(w, "  无盲区：%d/%d 跳都有 INT 数据，逐跳可归因\n\n", d.Instrumented, d.HopCount)
		return
	}
	for i, seg := range d.Segments {
		fmt.Fprintf(w, "  段 %d  %s → %s（hop %d → hop %d）\n",
			i+1, seg.FromDevice, seg.ToDevice, seg.FromHop, seg.ToHop)
		fmt.Fprintf(w, "        跨 %d 跳无数据：%s\n", len(seg.BlindDevice), strings.Join(seg.BlindDevice, "、"))
		if seg.Allocatable {
			fmt.Fprintf(w, "        吸收 %s µs，区间 [%s, %s]\n",
				fmtUs(seg.ShareNs), fmtUs(d.BlindLoNs), fmtUs(d.BlindHiNs))
		} else {
			fmt.Fprintln(w, "        多个盲区段，差额不能按段切分，只能给总量")
		}
		if len(seg.BlindDevice) == 1 {
			fmt.Fprintf(w, "        这段里只有 %s 一跳，差额全归它\n", seg.BlindDevice[0])
		} else {
			fmt.Fprintf(w, "        %d 跳都在盲区里，分不清是哪一跳排队\n", len(seg.BlindDevice))
		}
	}
	fmt.Fprintln(w)
}

// printNaive 打印反例：不做时钟对齐的跨跳相减。
func printNaive(w io.Writer, d Decomposition) {
	n := d.Naive
	if n.Count == 0 {
		return
	}
	fmt.Fprintln(w, "反例：跨跳相减（不做时钟对齐）")
	fmt.Fprintf(w, "  %d 段里 %d 段算出来是负数，最小 %s µs，最大 %s µs\n",
		n.Count, n.Negatives, fmtUs(n.MinNs), fmtUs(n.MaxNs))
	fmt.Fprintln(w, "  真值的链路传播是 0.15–30 µs。负时延不是物理，是两台设备的时钟不在一个域。")
	fmt.Fprintln(w, "  这个格子在真实工具里长得完全正常，只是数字没意义——所以 INT 只信「同跳内的差值」。")
	fmt.Fprintln(w)
}

// printReconcile 拿真值对账。生产环境没有这一节，台架上有。
func printReconcile(w io.Writer, d Decomposition, truth TraceTruth) {
	fmt.Fprintln(w, "对账（真值，生产环境拿不到）")
	fmt.Fprintf(w, "%-4s %-14s %11s %11s %8s %11s %11s %8s\n",
		"hop", "device", "resident", "true", "err", "queue", "true", "err")
	byIndex := make(map[int]HopTruth, len(truth.Hops))
	for _, h := range truth.Hops {
		byIndex[h.Index] = h
	}
	var resEst, resTrue, qEst, qTrue int64
	for _, h := range d.Hops {
		th := byIndex[h.Index]
		if !h.HasData {
			fmt.Fprintf(w, "%-4d %-14s %11s %11s %8s %11s %11s %8s\n",
				h.Index, h.Device, "-", fmtUs(th.ResidentNs), "-", "-", fmtUs(th.QueueNs), "盲区")
			continue
		}
		resEst += h.ResidentNs
		resTrue += th.ResidentNs
		qEst += h.QueueNs
		qTrue += th.QueueNs
		fmt.Fprintf(w, "%-4d %-14s %11s %11s %8s %11s %11s %8s\n",
			h.Index, h.Device, fmtUs(h.ResidentNs), fmtUs(th.ResidentNs),
			fmtSigned(h.ResidentNs-th.ResidentNs),
			fmtUs(h.QueueNs), fmtUs(th.QueueNs), fmtSigned(h.QueueNs-th.QueueNs))
	}
	fmt.Fprintf(w, "%-19s %11s %11s %8s %11s %11s %8s\n", "合计", fmtUs(resEst), fmtUs(resTrue),
		fmtSigned(resEst-resTrue), fmtUs(qEst), fmtUs(qTrue), fmtSigned(qEst-qTrue))
	fmt.Fprintf(w, "\n  驻留误差为 0：它是同一台设备两个时间戳的差，时钟偏移自动抵消。\n")
	fmt.Fprintf(w, "  排队误差来自队列深度的采样时刻：估值 = 深度 × 8 ÷ 速率，真值是本包实际等待。\n")
	fmt.Fprintf(w, "  端到端误差 %s µs、盲区误差 %s µs，两者都等于首末跳的时钟偏移差。\n\n",
		fmtSigned(d.E2EMeasuredNs-truth.E2ETrueNs), fmtSigned(d.BlindResidentNs-blindTruth(truth)))
}

// blindTruth 求盲区跳的真值驻留之和。
func blindTruth(truth TraceTruth) int64 {
	var sum int64
	for _, h := range truth.Hops {
		if !h.IntEnabled {
			sum += h.ResidentNs
		}
	}
	return sum
}

// hopRow 是落 JSONL 的一行：一条 trace 的一跳。
type hopRow struct {
	TraceID string   `json:"trace_id"`
	Hop     HopDelay `json:"hop"`
}

// writeOutputs 落两份产物：逐跳 JSONL 与汇总 JSON。
func writeOutputs(dir string, decomps []Decomposition) (hopPath, summaryPath string, err error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", "", err
	}

	hopPath = filepath.Join(dir, "hop-delays.jsonl")
	f, err := os.Create(hopPath)
	if err != nil {
		return "", "", err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	for _, d := range decomps {
		for _, h := range d.Hops {
			if err := enc.Encode(hopRow{TraceID: d.TraceID, Hop: h}); err != nil {
				return "", "", err
			}
		}
	}

	summaryPath = filepath.Join(dir, "decomposition.json")
	summaries := make([]Summary, 0, len(decomps))
	for _, d := range decomps {
		summaries = append(summaries, d.Summary())
	}
	data, err := json.MarshalIndent(summaries, "", "  ")
	if err != nil {
		return "", "", err
	}
	if err := os.WriteFile(summaryPath, append(data, '\n'), 0o644); err != nil {
		return "", "", err
	}
	return hopPath, summaryPath, nil
}
