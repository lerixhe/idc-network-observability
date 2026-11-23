package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"time"
)

// printCompare 打印每个端口的三列速率对比。
//
// 前面几列全是 ASCII（时间、差值、三份速率），中文只放最后一句判定，
// 终端里对齐不会被宽字符带偏。
func printCompare(w io.Writer, results []SeriesRates, detail bool) {
	for _, r := range results {
		s := r.Series
		fmt.Fprintf(w, "\n%s · %s\n", s.Key(), s.IfAlias)
		fmt.Fprintf(w, "  %s %d 位 · %s · 标称周期 %s · 满速回绕周期 %s",
			s.Counter, s.CounterBits, fmtBps(float64(s.SpeedBps)), shortDur(r.Nominal.Seconds()), wrapText(r))
		if r.WrapKnown {
			fmt.Fprintf(w, "（采集周期红线 %s）", shortDur(r.WrapPeriod.Seconds()*wrapWarnRatio))
		}
		fmt.Fprintln(w)

		if detail {
			fmt.Fprintf(w, "  %-16s %-6s %-14s %-10s %-10s %-10s %-10s %s\n",
				"from->to", "dt", "delta", "v1 naive", "v2 norm", "v3 wrap", "flag", "判定")
			for _, p := range r.Points {
				fmt.Fprintf(w, "  %-16s %-6s %-14d %-10s %-10s %-10s %-10s %s\n",
					spanText(p), shortDur(p.DtSeconds), p.RawDelta,
					fmtBps(p.Naive), fmtBps(p.Normalized), fmtBps(p.WrapInterp),
					flagText(p), p.Reason)
			}
		}
		peak := fmtBps(r.maxRate())
		if peak == "0" && r.Untrusted > 0 {
			peak = "不可用（全段不可信）"
		}
		fmt.Fprintf(w, "  小结：回绕 %d · 重启 %d · uptime 回绕 %d · 丢样 %d 段 · 乱序 %d 处 · 不可信 %d 段 · 峰值 %s\n",
			r.Wraps, r.Resets, r.UptimeWraps, r.Gaps, r.Reorders, r.Untrusted, peak)
	}
}

// wrapText 描述回绕周期的量级；64 位计数器那行不写具体数字，写了也没人信。
func wrapText(r SeriesRates) string {
	if !r.WrapKnown {
		return "不回绕（量程 2^64）"
	}
	return shortDur(r.WrapPeriod.Seconds())
}

// spanText 把区间两端写成「0.0->10.0s」。
func spanText(p Interval) string {
	return fmt.Sprintf("%.1f->%.1fs", float64(p.FromMs)/1000, float64(p.ToMs)/1000)
}

// flagText 标出分岔：v1≠v2 是间隔不等（丢样），v2≠v3 是回绕。
func flagText(p Interval) string {
	v1off := p.Gap > 0
	v2off := p.Wraps > 0 || p.Reset
	switch {
	case v1off && v2off:
		return "◄v1≠v2,v2≠v3"
	case v2off:
		return "◄v2≠v3"
	case v1off:
		return "◄v1≠v2"
	default:
		return "-"
	}
}

// printWarnings 打印告警。这里的每一条都意味着有一段速率不可信，
// 而不是「数据质量稍差」——宁可让面板缺一格，也不要一格错数。
func printWarnings(w io.Writer, results []SeriesRates) int {
	n := 0
	for _, r := range results {
		for _, msg := range r.Warnings {
			fmt.Fprintf(w, "⚠ %s\n", msg)
			n++
		}
	}
	if n == 0 {
		fmt.Fprintln(w, "⚠ 无：所有端口都没有出现采集周期逼近回绕周期的情况")
	}
	return n
}

// printReconcile 拿判定结果与样本注入的真值对账。
func printReconcile(w io.Writer, results []SeriesRates, truth map[string]Truth) int {
	mismatch := 0
	fmt.Fprintln(w, "\n与真值对账")
	for _, r := range results {
		t, ok := truth[r.Series.Key()]
		if !ok {
			fmt.Fprintf(w, "  %-34s 真值缺失，跳过\n", r.Series.Key())
			continue
		}
		bad := []string{}
		check := func(name string, got, want int) {
			if got != want {
				bad = append(bad, fmt.Sprintf("%s 检出 %d / 真值 %d", name, got, want))
			}
		}
		check("回绕", r.Wraps, t.Wraps)
		check("重启", r.Resets, t.Resets)
		check("uptime 回绕", r.UptimeWraps, t.UptimeWraps)
		check("丢样", r.Gaps, t.Gaps)
		check("乱序", r.Reorders, t.Reorders)
		if len(bad) == 0 {
			fmt.Fprintf(w, "  %-34s 对上：回绕 %d / 重启 %d / uptime 回绕 %d / 丢样 %d 段 / 乱序 %d 处\n",
				r.Series.Key(), r.Wraps, r.Resets, r.UptimeWraps, r.Gaps, r.Reorders)
			continue
		}
		mismatch += len(bad)
		fmt.Fprintf(w, "  %-34s 对不上：%s\n", r.Series.Key(), joinComma(bad))
	}
	return mismatch
}

func joinComma(items []string) string {
	out := ""
	for i, s := range items {
		if i > 0 {
			out += "；"
		}
		out += s
	}
	return out
}

// printAttribution 打印错包归因表。
func printAttribution(w io.Writer, attrs []Attribution) {
	fmt.Fprintln(w, "\n错包归因")
	fmt.Fprintf(w, "  %-34s %-6s %-8s %-8s %-8s %-8s %-6s %s\n",
		"port", "util", "fcs_ppm", "sym", "qdrop", "hdrop", "owner", "证据 ｜ 下一步")
	for _, a := range attrs {
		c := a.Counters
		fmt.Fprintf(w, "  %-34s %-6s %-8.1f %-8d %-8d %-8d %-6s %s｜下一步：%s\n",
			c.Key(), fmt.Sprintf("%.0f%%", a.UtilPct), a.FramePPM, c.SymbolErrors,
			c.OutQueueDiscards+c.InBufferDiscards, c.HostRxDrops, a.Owner,
			joinComma(a.Reasons), a.NextStep)
		if len(a.Also) > 0 {
			fmt.Fprintf(w, "  %-34s %-6s %-8s %-8s %-8s %-8s %-6s 还有：%s\n", "", "", "", "", "", "", "", joinComma(a.Also))
		}
	}
}

// printWrapTable 打印回绕周期公式表：速率 → 满速回绕周期 → 采集周期上限。
//
// 这张表是这一课唯一必须记住的算术：32 位字节计数器，1G 口 34.36 秒走完一圈，
// 10G 口 3.44 秒。采集周期必须小于一半，否则圈数不可判。
func printWrapTable(w io.Writer, speeds []uint64, poll time.Duration) {
	fmt.Fprintf(w, "32 位计数器回绕周期 = 2^32 / 每秒增量\n")
	fmt.Fprintf(w, "  字节计数器：每秒增量 = 线速 / 8；包计数器（64 字节帧含前导与帧间隙共 84 字节）：每秒增量 = 线速 / (84×8)\n\n")
	fmt.Fprintf(w, "  %-10s %-14s %-14s %-14s %-14s %s\n",
		"speed", "octets/period", "octets/max poll", "pkts/period", "pkts/max poll", "poll verdict")
	for _, sp := range speeds {
		octets := float64(sp) / 8
		pkts := float64(sp) / (84 * 8)
		op, _ := WrapPeriod(counter32Bits, octets)
		pp, _ := WrapPeriod(counter32Bits, pkts)
		verdict := "-"
		if poll > 0 {
			switch {
			case poll.Seconds() >= op.Seconds():
				verdict = fmt.Sprintf("DANGER: poll %s ≥ 回绕周期 %s，一个周期漏整圈", shortDur(poll.Seconds()), shortDur(op.Seconds()))
			case poll.Seconds() >= op.Seconds()*wrapWarnRatio:
				verdict = fmt.Sprintf("DANGER: poll %s ≥ 半周期 %s", shortDur(poll.Seconds()), shortDur(op.Seconds()*wrapWarnRatio))
			default:
				verdict = fmt.Sprintf("ok: poll %s < 半周期 %s", shortDur(poll.Seconds()), shortDur(op.Seconds()*wrapWarnRatio))
			}
		}
		fmt.Fprintf(w, "  %-10s %-14s %-14s %-14s %-14s %s\n",
			fmtBps(float64(sp)), shortDur(op.Seconds()), shortDur(op.Seconds()*wrapWarnRatio),
			shortDur(pp.Seconds()), shortDur(pp.Seconds()*wrapWarnRatio), verdict)
	}
}

// fmtBps 把 bps 压成 4–6 个字符，表格里够看，也别丢掉数量级。
func fmtBps(v float64) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	a := math.Abs(v)
	var s string
	switch {
	case a >= 1e12:
		s = fmt.Sprintf("%.2fT", a/1e12)
	case a >= 1e9:
		s = fmt.Sprintf("%.2fG", a/1e9)
	case a >= 1e6:
		s = fmt.Sprintf("%.2fM", a/1e6)
	case a >= 1e3:
		s = fmt.Sprintf("%.1fk", a/1e3)
	default:
		s = fmt.Sprintf("%.0f", a)
	}
	if neg {
		return "-" + s
	}
	return s
}

// writeRates 把每个区间的三份结论落成 JSONL，一行一个区间。
func writeRates(dir string, results []SeriesRates) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, "rates.jsonl")
	f, err := os.Create(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	w := bufio.NewWriter(f)
	enc := json.NewEncoder(w)
	for _, r := range results {
		for _, p := range r.Points {
			row := rateRow{
				Device: r.Series.Device, IfName: r.Series.IfName, Counter: r.Series.Counter,
				Interval: p,
			}
			if err := enc.Encode(row); err != nil {
				return "", err
			}
		}
	}
	if err := w.Flush(); err != nil {
		return "", err
	}
	return path, nil
}

// rateRow 是 rates.jsonl 的一行：区间结论前面补上端口身份。
type rateRow struct {
	Device  string `json:"device"`
	IfName  string `json:"if_name"`
	Counter string `json:"counter"`
	Interval
}

// writeAttribution 落归因结论。
func writeAttribution(dir string, attrs []Attribution) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, "attribution.json")
	raw, err := json.MarshalIndent(attrs, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(path, append(raw, '\n'), 0o644); err != nil {
		return "", err
	}
	return path, nil
}
