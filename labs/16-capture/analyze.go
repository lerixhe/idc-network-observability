package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
)

// ---------- 样本结构 ----------

// Sender 发送端计数器。
type Sender struct {
	Port       string `json:"port"`
	TxPackets  int64  `json:"tx_packets"`
	TxBytes    int64  `json:"tx_bytes"`
	TxDiscards int64  `json:"tx_discards"`
	TxErrors   int64  `json:"tx_errors"`
}

// Hop 中间设备一跳的入出计数。
type Hop struct {
	Device     string `json:"device"`
	InPackets  int64  `json:"in_packets"`
	OutPackets int64  `json:"out_packets"`
	InBytes    int64  `json:"in_bytes"`
	OutBytes   int64  `json:"out_bytes"`
}

// Ethtool 主机侧 NIC 计数与 offload 开关。
type Ethtool struct {
	RingRxEntries  int64 `json:"ring_rx_entries"`
	RxQueueDropped int64 `json:"rx_queue_dropped"`
	RxNoBuffer     int64 `json:"rx_no_buffer_count"`
	TSO            bool  `json:"tso"`
	GSO            bool  `json:"gso"`
	GRO            bool  `json:"gro"`
	LRO            bool  `json:"lro"`
}

// ICMP 接收侧 ICMP 计数，rate_limited 是内核限速丢掉的回包数。
type ICMP struct {
	Sent        int64 `json:"sent"`
	RateLimited int64 `json:"rate_limited"`
}

// Receiver 接收端计数器。
type Receiver struct {
	Port       string  `json:"port"`
	RxPackets  int64   `json:"rx_packets"`
	RxBytes    int64   `json:"rx_bytes"`
	RxDiscards int64   `json:"rx_discards"`
	RxErrors   int64   `json:"rx_errors"`
	Ethtool    Ethtool `json:"ethtool"`
	ICMP       ICMP    `json:"icmp"`
}

// Flow 一条端到端对照记录。
type Flow struct {
	ID       string   `json:"id"`
	Sender   Sender   `json:"sender"`
	Path     []Hop    `json:"path"`
	Receiver Receiver `json:"receiver"`
}

// Sample 一份抓包窗口的输入。
type Sample struct {
	Case          string `json:"case"`
	WindowSeconds int    `json:"window_seconds"`
	Flows         []Flow `json:"flows"`
}

func LoadSample(r io.Reader) (Sample, error) {
	var s Sample
	dec := json.NewDecoder(r)
	if err := dec.Decode(&s); err != nil {
		return Sample{}, fmt.Errorf("解析样本失败: %w", err)
	}
	if len(s.Flows) == 0 {
		return Sample{}, fmt.Errorf("样本里没有 flows")
	}
	return s, nil
}

func LoadSampleFile(path string) (Sample, error) {
	f, err := os.Open(path)
	if err != nil {
		return Sample{}, err
	}
	defer f.Close()
	return LoadSample(f)
}

// ---------- 归因 ----------

// 段名，用来定位丢包落点。
const (
	SegSender       = "sender"       // 本机协议栈没发出去
	SegLink         = "link"         // 网线/中间设备之间
	SegDevice       = "device"       // 中间设备内部丢
	SegReceiver     = "receiver"     // 对端 NIC 计数上的丢
	SegHost         = "host"         // 对端主机内核侧丢（ring buffer 等）
	SegUnattributed = "unattributed" // 对不上账的残差
	SegNone         = "none"         // 没丢
)

var segOrder = []string{SegSender, SegLink, SegDevice, SegReceiver, SegHost, SegUnattributed}

// 陷阱代号。
const (
	TrapRingBuffer = "ring-buffer"     // ring buffer 溢出
	TrapOffload    = "offload-segment" // offload 假分段
	TrapICMP       = "icmp-rate-limit" // ICMP 限速假丢包
)

// Trap 命中的陷阱。
type Trap struct {
	Code   string
	Detail string
}

// Segment 一段丢包。
type Segment struct {
	Name    string
	Packets int64
}

// HopDrop 单个中间设备的入出差额。
type HopDrop struct {
	Device string
	Lost   int64
}

// Result 单条 flow 的结论。
type Result struct {
	FlowID      string
	TxPackets   int64
	RxPackets   int64
	LostPackets int64
	LostBytes   int64
	PktLossPct  float64
	ByteLossPct float64
	Segments    []Segment
	HopDrops    []HopDrop
	Traps       []Trap
	Verdict     string
	Summary     string
}

// offload 判定门槛：包丢率比字节丢率多出这么多百分点，就当成假分段。
const offloadDeltaPct = 0.5

func Analyze(s Sample) []Result {
	out := make([]Result, 0, len(s.Flows))
	for _, f := range s.Flows {
		out = append(out, analyzeFlow(f))
	}
	return out
}

func analyzeFlow(f Flow) Result {
	r := Result{FlowID: f.ID, TxPackets: f.Sender.TxPackets, RxPackets: f.Receiver.RxPackets}

	// 发送端：没发出去的部分。
	senderLost := clampNeg(f.Sender.TxDiscards) + clampNeg(f.Sender.TxErrors)
	txOnWire := f.Sender.TxPackets - senderLost

	// 链路：发送端出线到第一跳入线之间的缺口。
	var firstIn int64
	if len(f.Path) > 0 {
		firstIn = f.Path[0].InPackets
	} else {
		firstIn = txOnWire
	}
	linkLost := clampNeg(txOnWire - firstIn)

	// 中间设备：逐跳入出差额。
	var deviceLost int64
	lastOut := firstIn
	for _, h := range f.Path {
		lost := clampNeg(h.InPackets - h.OutPackets)
		r.HopDrops = append(r.HopDrops, HopDrop{Device: h.Device, Lost: lost})
		deviceLost += lost
		lastOut = h.OutPackets
	}
	if len(f.Path) == 0 {
		lastOut = txOnWire
	}

	// 接收端：到达 NIC 但没交给协议栈的部分，再拆成 NIC 计数与主机内核。
	gap := clampNeg(lastOut - f.Receiver.RxPackets)
	rcvLost := clampNeg(f.Receiver.RxDiscards) + clampNeg(f.Receiver.RxErrors)
	hostLost := clampNeg(f.Receiver.Ethtool.RxQueueDropped) + clampNeg(f.Receiver.Ethtool.RxNoBuffer)
	unattributed := gap - rcvLost - hostLost
	if unattributed < 0 {
		unattributed = 0
	}

	r.LostPackets = senderLost + linkLost + deviceLost + rcvLost + hostLost + unattributed
	r.LostBytes = clampNeg(f.Sender.TxBytes - f.Receiver.RxBytes)
	if f.Sender.TxPackets > 0 {
		r.PktLossPct = pct(clampNeg(f.Sender.TxPackets-f.Receiver.RxPackets), f.Sender.TxPackets)
	}
	if f.Sender.TxBytes > 0 {
		r.ByteLossPct = pct(r.LostBytes, f.Sender.TxBytes)
	}

	r.Segments = sortSegments([]Segment{
		{SegSender, senderLost},
		{SegLink, linkLost},
		{SegDevice, deviceLost},
		{SegReceiver, rcvLost},
		{SegHost, hostLost},
		{SegUnattributed, unattributed},
	})

	r.Traps = detectTraps(f, r)

	// 字节口径才是真相：offload 假分段时包数差额不成立，按字节判落点。
	pktGain := f.Receiver.RxPackets > f.Sender.TxPackets
	offloadSuspect := pktGain || hasTrap(r.Traps, TrapOffload)
	switch {
	case offloadSuspect && r.ByteLossPct < offloadDeltaPct:
		r.Verdict = SegNone
		r.Summary = "链路无损：包数差额来自 offload 假分段，字节口径对得上"
	case r.LostPackets == 0 && r.LostBytes == 0:
		r.Verdict = SegNone
		r.Summary = "三端包数与字节完全对齐，无丢包"
	default:
		r.Verdict = dominant(r.Segments)
		r.Summary = verdictSummary(r.Verdict, r)
	}
	return r
}

func detectTraps(f Flow, r Result) []Trap {
	var traps []Trap

	hostLost := segValue(r.Segments, SegHost)
	if hostLost > 0 {
		detail := fmt.Sprintf("ethtool 显示队列丢弃 %d 包（ring_rx_entries=%d），NIC 计数层看不到，是主机侧配置问题",
			hostLost, f.Receiver.Ethtool.RingRxEntries)
		if f.Receiver.RxDiscards == 0 && f.Receiver.RxErrors == 0 {
			detail += "；rx_discards/rx_errors 为 0，只看网卡计数会误判成链路丢"
		}
		traps = append(traps, Trap{TrapRingBuffer, detail})
	}

	et := f.Receiver.Ethtool
	if et.TSO || et.GSO || et.GRO || et.LRO {
		if r.PktLossPct-r.ByteLossPct >= offloadDeltaPct || f.Receiver.RxPackets > f.Sender.TxPackets {
			traps = append(traps, Trap{TrapOffload, fmt.Sprintf(
				"offload 开启（tso=%t gso=%t gro=%t lro=%t），包丢率 %.2f%% 与字节丢率 %.2f%% 背离，包数不能当丢包证据",
				et.TSO, et.GSO, et.GRO, et.LRO, r.PktLossPct, r.ByteLossPct)})
		}
	}

	if f.Receiver.ICMP.RateLimited > 0 {
		traps = append(traps, Trap{TrapICMP, fmt.Sprintf(
			"内核限速丢弃 ICMP 回包 %d 个，探测端看到的丢包是假信号，数据面按字节口径 %d 包无丢",
			f.Receiver.ICMP.RateLimited, r.LostBytes)})
	}
	return traps
}

func verdictSummary(seg string, r Result) string {
	switch seg {
	case SegSender:
		return "丢包在发送端：本机 tx_discards/tx_errors，查 qdisc 与队列映射"
	case SegLink:
		return "丢包在发送端到第一跳之间：查光模块、线缆、MTU 与速率协商"
	case SegDevice:
		d := ""
		for _, h := range r.HopDrops {
			if h.Lost > 0 {
				if d != "" {
					d += "、"
				}
				d += fmt.Sprintf("%s %d 包", h.Device, h.Lost)
			}
		}
		return "丢包在中间设备内部：" + d + "，查缓存水位与拥塞"
	case SegReceiver:
		return "丢包在接收端 NIC：rx_discards/rx_errors，查物理层与中断"
	case SegHost:
		return "丢包在接收端主机内核：ring buffer 溢出，加 rx ring 或减突发"
	case SegUnattributed:
		return "残差对不上账：样本计数窗口不一致或漏采了一段"
	default:
		return "链路无损"
	}
}

// ---------- 输出 ----------

func Report(w io.Writer, s Sample, results []Result) {
	fmt.Fprintf(w, "case: %s    窗口: %ds    流数: %d\n\n", s.Case, s.WindowSeconds, len(results))
	for _, r := range results {
		fmt.Fprintf(w, "=== %s ===\n", r.FlowID)
		fmt.Fprintf(w, "  包: tx %d -> rx %d   丢 %d (%.3f%%)\n", r.TxPackets, r.RxPackets, r.LostPackets, r.PktLossPct)
		fmt.Fprintf(w, "  字节: 丢 %d (%.3f%%)\n", r.LostBytes, r.ByteLossPct)
		fmt.Fprintf(w, "  %-13s %10s\n", "段", "丢失包")
		for _, seg := range r.Segments {
			fmt.Fprintf(w, "  %-13s %10d\n", seg.Name, seg.Packets)
		}
		if len(r.HopDrops) > 0 {
			fmt.Fprintf(w, "  %-13s %10s\n", "逐跳", "入出差额")
			for _, h := range r.HopDrops {
				fmt.Fprintf(w, "  %-13s %10d\n", h.Device, h.Lost)
			}
		}
		fmt.Fprintf(w, "  结论: 落点=%s\n", r.Verdict)
		fmt.Fprintf(w, "  说明: %s\n", r.Summary)
		if len(r.Traps) == 0 {
			fmt.Fprintf(w, "  陷阱: 无\n")
		} else {
			fmt.Fprintf(w, "  陷阱:\n")
			for _, t := range r.Traps {
				fmt.Fprintf(w, "    - %s: %s\n", t.Code, t.Detail)
			}
		}
		fmt.Fprintln(w)
	}
}

func dominant(segs []Segment) string {
	best := SegNone
	var max int64
	for _, s := range segs {
		if s.Packets > max {
			max = s.Packets
			best = s.Name
		}
	}
	return best
}

func segValue(segs []Segment, name string) int64 {
	for _, s := range segs {
		if s.Name == name {
			return s.Packets
		}
	}
	return 0
}

func hasTrap(traps []Trap, code string) bool {
	for _, t := range traps {
		if t.Code == code {
			return true
		}
	}
	return false
}

func clampNeg(v int64) int64 {
	if v < 0 {
		return 0
	}
	return v
}

func pct(part, total int64) float64 {
	if total == 0 {
		return 0
	}
	return float64(part) / float64(total) * 100
}

// sortSegments 按固定顺序输出，测试用得上。
func sortSegments(segs []Segment) []Segment {
	order := map[string]int{}
	for i, n := range segOrder {
		order[n] = i
	}
	out := append([]Segment(nil), segs...)
	sort.SliceStable(out, func(i, j int) bool { return order[out[i].Name] < order[out[j].Name] })
	return out
}
