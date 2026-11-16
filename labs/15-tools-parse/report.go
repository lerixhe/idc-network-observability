package main

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
)

// printHeader 打第一行：样本规模与用到的模板版本。
// 模板版本要出现在报告里，否则解析出鬼数据时第一件事就是登设备看固件。
func printHeader(w io.Writer, ds *Dataset) {
	rows := 0
	for _, c := range ds.Captures {
		rows += len(c.Ports)
	}
	var refs []string
	seen := map[string]bool{}
	for _, c := range ds.Captures {
		if seen[c.Template] {
			continue
		}
		seen[c.Template] = true
		refs = append(refs, c.Template)
	}

	fmt.Fprintf(w, "设备样本 %d 份 / 采集段 %d 段 / 端口行 %d 条 / mtr %d 次\n",
		countSources(ds.Captures), len(ds.Captures), rows, len(ds.MTR))
	fmt.Fprintf(w, "模板 %s\n\n", strings.Join(refs, " · "))
}

// printWarnings 打模板回落这类「数据能出来但可疑」的提示。
// 它们不是错误，不能中断解析；但也不能只写日志——报告里看不见的告警等于没有。
func printWarnings(w io.Writer, ds *Dataset) {
	var lines []string
	for _, c := range ds.Captures {
		for _, warn := range c.Warnings {
			lines = append(lines, fmt.Sprintf("  %s（%s fw %s）：%s", c.Device, c.Source, c.Firmware, warn))
		}
	}
	if len(lines) == 0 {
		return
	}
	fmt.Fprintf(w, "\n模板告警 %d 条\n", len(lines))
	for _, l := range lines {
		fmt.Fprintln(w, l)
	}
	fmt.Fprintln(w)
}

func countSources(caps []Capture) int {
	seen := map[string]bool{}
	for _, c := range caps {
		seen[c.Source] = true
	}
	return len(seen)
}

// printCounters 打统一结构主表。
//
// 一格里「-」表示这条记录里没有这个字段，0 表示设备报的就是 0。
// 这两者长得不一样，是这张表唯一的价值：固件删掉一列，你会看到一片「-」，
// 而不是一片看不出异常的 0。
func printCounters(w io.Writer, ds *Dataset) {
	if len(ds.Captures) == 0 {
		return
	}
	tw := tabwriter.NewWriter(w, 0, 8, 2, ' ', 0)
	// 列头全用 ASCII：中文列头在 tabwriter 里按 rune 算宽度，全表会错位。
	fmt.Fprintln(tw, "device\tfw\ttemplate\tport\tin_bytes\tin_packets\tout_bytes\tout_packets\tin_errors\tout_errors\tspeed\tunknown")
	for _, c := range ds.Captures {
		for _, p := range c.Ports {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
				p.Device, p.Firmware, p.Template, p.Port,
				cell(p, FieldInBytes), cell(p, FieldInPackets),
				cell(p, FieldOutBytes), cell(p, FieldOutPackets),
				cell(p, FieldInErrors), cell(p, FieldOutErrors),
				orDash(p.Speed), unknownNames(p.Unknown))
		}
	}
	tw.Flush()
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func unknownNames(us []UnknownColumn) string {
	if len(us) == 0 {
		return "-"
	}
	names := make([]string, 0, len(us))
	for _, u := range us {
		names = append(names, u.Name)
	}
	return strings.Join(names, ",")
}

// printMTR 打每跳丢包率与判定。判定列是给人看的，说明列是给工单看的。
func printMTR(w io.Writer, ds *Dataset) {
	for _, run := range ds.MTR {
		fmt.Fprintf(w, "\nmtr  %s → %s（%s）\n", run.Probe, run.Target, run.Summary())
		tw := tabwriter.NewWriter(w, 0, 8, 2, ' ', 0)
		fmt.Fprintln(tw, "hop\thost\tloss%\tsnt\tlast\tavg\tbest\tworst\tverdict\tnote")
		for _, h := range run.Hops {
			fmt.Fprintf(tw, "%d\t%s\t%.1f\t%d\t%.1f\t%.1f\t%.1f\t%.1f\t%s\t%s\n",
				h.Index, h.Host, h.LossPct, h.Snt, h.Last, h.Avg, h.Best, h.Wrst,
				h.Verdict, h.Note)
		}
		tw.Flush()
		fmt.Fprintf(w, "结论 %s · %s\n", run.Verdict, run.Note)
	}
}

// printDrift 打同设备跨固件的单位变化。这是「不崩」之外的第二个要求：
// 不崩只是没 panic，单位变了才是真的会把数算错。
func printDrift(w io.Writer, ds *Dataset) {
	changes := UnitDrift(ds.Captures)
	fmt.Fprintf(w, "\n单位 / 列漂移（同设备相邻两次采集对比，共 %d 处）\n", len(changes))
	if len(changes) == 0 {
		fmt.Fprintln(w, "  没有跨版本对比对象：样本里每台设备只有一次采集")
		return
	}
	for _, c := range changes {
		fmt.Fprintf(w, "  %s %s：%s → %s\n", c.Device, c.Field, c.From, c.To)
	}
	fmt.Fprintln(w, "  out_bytes 消失意味着按出口字节求和的下游查询会得到空值，不是 0；换字段或加换算，二者都要写进变更单")
}

// printDropped 打丢弃清单。计数非零说明模板落后于固件，这一项要能进监控。
func printDropped(w io.Writer, ds *Dataset) {
	total := 0
	var lines []string
	for _, c := range ds.Captures {
		if len(c.Unknown) == 0 {
			continue
		}
		total += len(c.Unknown)
		vals := make([]string, 0, len(c.Unknown))
		for _, name := range c.Unknown {
			v, ok := firstUnknownValue(c, name)
			if !ok {
				vals = append(vals, name)
				continue
			}
			vals = append(vals, fmt.Sprintf("%s=%s", name, v))
		}
		lines = append(lines, fmt.Sprintf("  %-12s %-22s %s", c.Device, c.Template, strings.Join(vals, " ")))
	}

	fmt.Fprintf(w, "\n模板没声明的列（%d 个，值留在丢弃清单里）\n", total)
	if total == 0 {
		fmt.Fprintln(w, "  没有：三份样本的列头都被模板覆盖了")
		return
	}
	for _, l := range lines {
		fmt.Fprintln(w, l)
	}
	fmt.Fprintln(w, "  这个数不为 0 就是模板升级工单，不是可以忽略的噪音")
}

func firstUnknownValue(c Capture, name string) (string, bool) {
	for _, p := range c.Ports {
		for _, u := range p.Unknown {
			if u.Name == name {
				return u.Value, true
			}
		}
	}
	return "", false
}

// printDerived 打派生换算。单位不一致时唯一合法的换算依据是同一行的
// 字节数与包数之比——这是设备给的两个数相除，不是假设一个帧长。
func printDerived(w io.Writer, ds *Dataset) {
	fmt.Fprintln(w, "\n出口字节的派生值（用同一行入向的均值换算，只用于比量级）")
	printed := 0
	for _, c := range ds.Captures {
		for _, p := range c.Ports {
			if _, ok := p.Value(FieldOutBytes); ok {
				continue
			}
			frame, ok := p.AvgFrameBytes()
			if !ok {
				continue
			}
			bytes, ok := p.DerivedOutBytes()
			if !ok {
				continue
			}
			fmt.Fprintf(w, "  %s %s fw %s：入向 %.2f B/包（%s / %s）→ out_packets %s 折合 %s B（派生）\n",
				p.Device, p.Port, p.Firmware, frame,
				cell(p, FieldInBytes), cell(p, FieldInPackets),
				cell(p, FieldOutPackets), formatCount(math.Round(bytes)))
			printed++
		}
	}
	if printed == 0 {
		fmt.Fprintln(w, "  没有需要换算的记录：每条都有设备直接给的出口字节数")
	}
	fmt.Fprintln(w, "  派生值别进报表：出口的平均帧长与入口不一样，小包单向发送很常见")
}

// jsonCounterLine 是一行 JSONL。结构体字段名就是下游的表结构，
// 所以字段名用 in_bytes 这种语义名，不用设备上的列头名。
type jsonCounterLine struct {
	Kind     string            `json:"kind"`
	Device   string            `json:"device"`
	Vendor   string            `json:"vendor"`
	Firmware string            `json:"firmware"`
	Command  string            `json:"command"`
	Template string            `json:"template"`
	Port     string            `json:"port"`
	Speed    string            `json:"speed,omitempty"`
	Counters map[Field]Counter `json:"counters"`
	Unknown  []UnknownColumn   `json:"unknown,omitempty"`
	AvgFrame *float64          `json:"avg_frame_bytes,omitempty"`
	Warnings []string          `json:"warnings,omitempty"`
}

type jsonMTRLine struct {
	Kind    string `json:"kind"`
	Probe   string `json:"probe"`
	Target  string `json:"target"`
	Verdict string `json:"verdict"`
	Note    string `json:"note"`
	Hops    []Hop  `json:"hops"`
}

// writeJSONL 把解析结果落成 JSONL：一行一条端口、一行一次 mtr。
// 落盘的是统一结构，不是原始文本——原始文本另存对象存储，两者用 template 版本关联。
func writeJSONL(dir string, ds *Dataset) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, "parsed.jsonl")
	f, err := os.Create(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	enc := json.NewEncoder(f)
	for _, c := range ds.Captures {
		for _, p := range c.Ports {
			line := jsonCounterLine{
				Kind:     "counters",
				Device:   p.Device,
				Vendor:   p.Vendor,
				Firmware: p.Firmware,
				Command:  p.Command,
				Template: p.Template,
				Port:     p.Port,
				Speed:    p.Speed,
				Counters: p.Counters,
				Unknown:  p.Unknown,
				Warnings: c.Warnings,
			}
			if frame, ok := p.AvgFrameBytes(); ok {
				line.AvgFrame = &frame
			}
			if err := enc.Encode(line); err != nil {
				return "", err
			}
		}
	}
	for _, run := range ds.MTR {
		line := jsonMTRLine{
			Kind:    "mtr",
			Probe:   run.Probe,
			Target:  run.Target,
			Verdict: run.Verdict,
			Note:    run.Note,
			Hops:    run.Hops,
		}
		if err := enc.Encode(line); err != nil {
			return "", err
		}
	}
	return path, nil
}
