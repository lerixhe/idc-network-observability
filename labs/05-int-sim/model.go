package main

import (
	"encoding/json"
	"fmt"
	"os"
)

// HopMeta 是一跳的 INT 元数据。
//
// 时间戳是「设备本地时钟」的纳秒值，设备之间不对齐——这一点是本实验的
// 全部难点。跨设备相减得到的不是时延，是时延加两台设备的时钟偏移。
type HopMeta struct {
	Index       int    `json:"index"`
	Device      string `json:"device"`
	Role        string `json:"role"`
	IngressPort string `json:"ingress_port"`
	EgressPort  string `json:"egress_port"`
	// IntEnabled 为 false 表示这一跳不吐 INT：时间戳与队列深度都没有。
	// 老设备不支持、INT 没开、或者这一跳的采样被丢了，在数据里长得一样。
	IntEnabled bool `json:"int_enabled"`
	// ClockDomain 是时钟域，决定这一跳时间戳的精度上界。
	ClockDomain string `json:"clock_domain"`
	// LinkLengthM 是进这一跳的链路长度，单位米。来自台账，不是 INT 字段。
	LinkLengthM int   `json:"link_length_m"`
	EgressBps   int64 `json:"egress_bps"`
	// 下面四个字段在 int_enabled=false 时全为 0，不是「时间戳为 0 的那一刻」。
	IngressTsNs     int64 `json:"ingress_ts_ns"`
	EgressTsNs      int64 `json:"egress_ts_ns"`
	QueueDepthBytes int64 `json:"queue_depth_bytes"`
	QueueLimitBytes int64 `json:"queue_limit_bytes"`
}

// Flow 是这条报文是谁发给谁的，用来把一份 trace 和业务对上号。
type Flow struct {
	Src   string `json:"src"`
	Dst   string `json:"dst"`
	Proto string `json:"proto"`
}

// Trace 是一份报文穿过的全部跳。
type Trace struct {
	ID   string    `json:"id"`
	Desc string    `json:"desc"`
	Flow Flow      `json:"flow"`
	Hops []HopMeta `json:"hops"`
}

// TraceSet 是样本文件的外层结构。
type TraceSet struct {
	Note   string  `json:"note"`
	Traces []Trace `json:"traces"`
}

// ClockAccuracyNs 是各时钟域的精度上界：这个数不是常量，是策略。
//
// PTP 硬件时间戳 ±1 µs，NTP 在内网能到 ±1 ms（差的时候 10 ms 以上），
// free-run 就是没同步，±10 ms 只是给个能报数的话术。换硬件、换时间源，
// 这张表就得跟着改，所以做成参数而不是写死在算式里。
type ClockAccuracyNs map[string]int64

// defaultClockAccuracy 是样本用的口径。
var defaultClockAccuracy = ClockAccuracyNs{
	"ptp":      1_000,
	"ntp":      1_000_000,
	"free-run": 10_000_000,
}

// nsPerMeter 是光纤里的传播时延：约 2×10^8 m/s，也就是 5 ns/m（5 µs/km）。
//
// 这个数只用来估链路传播，估错了会算进「盲区驻留」里——第 5 节那条坑。
const nsPerMeter = 5

// Accuracy 返回某个时钟域的精度上界。不认识的域直接报错：
// 静默按 0 处理，等于宣称这条路径的时间戳精确到纳秒，结论会硬得离谱。
func (c ClockAccuracyNs) Accuracy(domain string) (int64, error) {
	ns, ok := c[domain]
	if !ok {
		return 0, fmt.Errorf("不认识的时钟域 %q", domain)
	}
	if ns < 0 {
		return 0, fmt.Errorf("时钟域 %q 的精度不能是负数", domain)
	}
	return ns, nil
}

// loadTraces 读 INT 元数据样本。
func loadTraces(path string) ([]Trace, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var set TraceSet
	if err := json.Unmarshal(data, &set); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if len(set.Traces) == 0 {
		return nil, fmt.Errorf("%s: 样本里没有任何 trace", path)
	}
	return set.Traces, nil
}

// loadTruth 读对账用的真值。真值只在仿真台架上有，生产环境拿不到。
func loadTruth(path string) (map[string]TraceTruth, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var set TruthSet
	if err := json.Unmarshal(data, &set); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	m := make(map[string]TraceTruth, len(set.Traces))
	for _, t := range set.Traces {
		m[t.ID] = t
	}
	return m, nil
}

// TruthSet 是真值文件的外层结构。
type TruthSet struct {
	Note   string       `json:"note"`
	Traces []TraceTruth `json:"traces"`
}

// TraceTruth 是一条 trace 的真值。
type TraceTruth struct {
	ID             string           `json:"id"`
	E2ETrueNs      int64            `json:"e2e_true_ns"`
	ClockOffsetsNs map[string]int64 `json:"clock_offsets_ns"`
	Hops           []HopTruth       `json:"hops"`
}

// HopTruth 是一跳的真值。驻留是精确量，排队与处理是仿真注入的。
type HopTruth struct {
	Index      int    `json:"index"`
	Device     string `json:"device"`
	LinkPropNs int64  `json:"link_prop_ns"`
	QueueNs    int64  `json:"queue_ns"`
	ForwardNs  int64  `json:"forward_ns"`
	ResidentNs int64  `json:"resident_ns"`
	IntEnabled bool   `json:"int_enabled"`
}

// validate 挡住会让实验跑出「看起来正常但错」的结论的样本。
//
// 每一条对应一类真实的坑：时间戳缺失但标记为已启用、跳号、首末跳没数据。
func (t Trace) validate(acc ClockAccuracyNs) error {
	if t.ID == "" {
		return fmt.Errorf("trace 没有 id")
	}
	if len(t.Hops) < 2 {
		return fmt.Errorf("%s: 只有 %d 跳，端到端分解至少要两跳", t.ID, len(t.Hops))
	}
	for i := range t.Hops {
		h := t.Hops[i]
		if h.Index != i+1 {
			return fmt.Errorf("%s: 第 %d 个 hop 的 index 是 %d，应当连续从 1 开始", t.ID, i+1, h.Index)
		}
		if h.Device == "" {
			return fmt.Errorf("%s: hop %d 没有设备名", t.ID, h.Index)
		}
		if h.EgressBps <= 0 {
			return fmt.Errorf("%s hop %d (%s): egress_bps 必须大于 0", t.ID, h.Index, h.Device)
		}
		if _, err := acc.Accuracy(h.ClockDomain); err != nil {
			return fmt.Errorf("%s hop %d (%s): %w", t.ID, h.Index, h.Device, err)
		}
		if h.IntEnabled {
			if h.IngressTsNs == 0 || h.EgressTsNs == 0 {
				return fmt.Errorf("%s hop %d (%s): int_enabled=true 却没有时间戳", t.ID, h.Index, h.Device)
			}
			if h.EgressTsNs <= h.IngressTsNs {
				return fmt.Errorf("%s hop %d (%s): 出口时间戳不晚于入口，元数据不可用",
					t.ID, h.Index, h.Device)
			}
			if h.QueueDepthBytes < 0 {
				return fmt.Errorf("%s hop %d (%s): 队列深度是负数", t.ID, h.Index, h.Device)
			}
			if h.QueueLimitBytes > 0 && h.QueueDepthBytes > h.QueueLimitBytes {
				return fmt.Errorf("%s hop %d (%s): 队列深度 %d 超过容量 %d，数据不可信",
					t.ID, h.Index, h.Device, h.QueueDepthBytes, h.QueueLimitBytes)
			}
			continue
		}
		// 没启用 INT 的跳不许夹带数据：带了就说明样本是拼出来的，不是录的。
		if h.IngressTsNs != 0 || h.EgressTsNs != 0 || h.QueueDepthBytes != 0 {
			return fmt.Errorf("%s hop %d (%s): int_enabled=false 却带着时间戳或队列深度",
				t.ID, h.Index, h.Device)
		}
	}
	first, last := t.Hops[0], t.Hops[len(t.Hops)-1]
	if !first.IntEnabled || !last.IntEnabled {
		return fmt.Errorf("%s: 首跳或末跳没有 INT 数据，端到端时延无法从 INT 得到", t.ID)
	}
	return nil
}

// validateAll 校验全部样本，顺带挡住重复的 trace id。
func validateAll(traces []Trace, acc ClockAccuracyNs) error {
	seen := make(map[string]bool, len(traces))
	for _, t := range traces {
		if seen[t.ID] {
			return fmt.Errorf("trace id %q 重复", t.ID)
		}
		seen[t.ID] = true
		if err := t.validate(acc); err != nil {
			return err
		}
	}
	return nil
}

// linksOf 返回跳与跳之间的全部链路长度（米），按跳顺序。
func (t Trace) linksOf() []int {
	links := make([]int, 0, len(t.Hops))
	for _, h := range t.Hops {
		links = append(links, h.LinkLengthM)
	}
	return links
}
