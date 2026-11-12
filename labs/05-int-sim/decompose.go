package main

import "fmt"

// Attribution 是一跳时延的归属。
type Attribution string

const (
	// AttrQueue：排队占了这一跳驻留时间的一半以上。
	AttrQueue Attribution = "queue"
	// AttrForward：处理与出队为主，这一跳是健康的。
	AttrForward Attribution = "forward"
	// AttrBlind：这一跳没有 INT 数据，什么都不知道。
	AttrBlind Attribution = "blind"
)

var attributionZH = map[Attribution]string{
	AttrQueue:   "排队为主",
	AttrForward: "处理为主",
	AttrBlind:   "盲区",
}

// HopDelay 是一跳的时延分解。全部时间单位是纳秒，打印时换成微秒。
//
// 三个量口径完全不同，混起来用就错了：
//
//	ResidentNs = 出口时间戳 − 入口时间戳       同一设备时钟，精确，与时钟偏移无关
//	QueueNs    = 队列深度 × 8 ÷ 出口速率       估算，误差来自采样时刻与本包不重合
//	ForwardNs  = 驻留 − 排队                    处理、查表、交换、自身序列化
type HopDelay struct {
	Index           int         `json:"index"`
	Device          string      `json:"device"`
	Role            string      `json:"role"`
	IngressPort     string      `json:"ingress_port"`
	EgressPort      string      `json:"egress_port"`
	ClockDomain     string      `json:"clock_domain"`
	HasData         bool        `json:"has_data"`
	ResidentNs      int64       `json:"resident_ns"`
	QueueNs         int64       `json:"queue_ns"`
	ForwardNs       int64       `json:"forward_ns"`
	QueueDepthBytes int64       `json:"queue_depth_bytes"`
	QueueLimitBytes int64       `json:"queue_limit_bytes"`
	EgressBps       int64       `json:"egress_bps"`
	LinkPropNs      int64       `json:"link_prop_ns"`
	Attribution     Attribution `json:"attribution"`
	Note            string      `json:"note,omitempty"`
}

// BlindSegment 是相邻两个「有数据」的跳之间的那段。
//
// 段内的差额是「中间那几跳的驻留 + 链路传播 + 时钟偏移差」混在一起，
// 只有整段的总量，没有逐跳的数。
type BlindSegment struct {
	FromHop     int      `json:"from_hop"`
	ToHop       int      `json:"to_hop"`
	FromDevice  string   `json:"from_device"`
	ToDevice    string   `json:"to_device"`
	BlindHops   []int    `json:"blind_hops"`
	BlindDevice []string `json:"blind_devices"`
	// ShareNs 是分到这一段的差额，只有全路径只有一个盲区段时才成立。
	ShareNs int64 `json:"share_ns"`
	// Allocatable 为 false 表示有多个盲区段，差额不可按段切分。
	Allocatable bool `json:"allocatable"`
}

// NaiveView 是「跳与跳之间直接相减」的结果，用来演示不做时钟对齐会怎样。
type NaiveView struct {
	Count     int   `json:"count"`
	Negatives int   `json:"negatives"`
	MinNs     int64 `json:"min_ns"`
	MaxNs     int64 `json:"max_ns"`
}

// Decomposition 是一条 trace 的完整分解结果。
type Decomposition struct {
	TraceID      string  `json:"trace_id"`
	Desc         string  `json:"desc"`
	HopCount     int     `json:"hop_count"`
	Instrumented int     `json:"instrumented_hops"`
	HopCoverage  float64 `json:"hop_coverage"`

	// E2EMeasuredNs = 末跳出口 − 首跳入口。跨了时钟域，含偏移差。
	E2EMeasuredNs int64 `json:"e2e_measured_ns"`
	// ClockErrNs 是首末跳时钟精度之和，端到端测量值的误差上界。
	ClockErrNs int64 `json:"clock_err_ns"`
	// SumResidentNs 是可归因的部分：有数据跳的驻留之和，精确。
	SumResidentNs int64 `json:"sum_resident_ns"`
	// PropNs 是链路传播估值：台账长度 × 5 ns/m。
	PropNs int64 `json:"prop_ns"`
	// GapNs = 端到端 − 驻留合计。它等于「盲区驻留 + 全部链路传播 + 时钟偏移差」。
	GapNs int64 `json:"gap_ns"`
	// BlindResidentNs = GapNs − PropNs，即盲区驻留的估计值。
	BlindResidentNs int64 `json:"blind_resident_ns"`
	BlindLoNs       int64 `json:"blind_lo_ns"`
	BlindHiNs       int64 `json:"blind_hi_ns"`
	// AttributedShare = 驻留合计 ÷ 端到端。时延口径的覆盖率。
	AttributedShare float64 `json:"attributed_share"`
	// TopQueueDevice 是排队最多的一跳；没有任何跳有数据时为空。
	TopQueueDevice string `json:"top_queue_device,omitempty"`
	TopQueueNs     int64  `json:"top_queue_ns"`

	Hops     []HopDelay     `json:"hops"`
	Segments []BlindSegment `json:"segments"`
	Naive    NaiveView      `json:"naive_cross_hop"`
}

// us 把纳秒换成微秒，打印与断言都用这个。
func us(ns int64) float64 { return float64(ns) / 1000 }

// queueDelayNs 按「队列深度 ÷ 出口速率」估排队时延：
//
//	queue_ns = depth_bytes × 8 ÷ egress_bps × 10^9
//
// 例：100 Gbps 端口上 39 600 000 字节在队列里
//
//	= 316 800 000 bit ÷ 100 000 000 000 bit/s = 3.168 ms = 3 168 000 ns
//
// 这个估值有三处系统性偏差：队列深度是采样时刻的瞬时值，不含本包自己的
// 序列化时间，共享缓存里其它队列的丢弃也会改变真实等待。
func queueDelayNs(depthBytes, egressBps int64) int64 {
	if egressBps <= 0 {
		return 0
	}
	return depthBytes * 8 * 1_000_000_000 / egressBps
}

// Decompose 是一条 trace 的分解入口。
func Decompose(t Trace, acc ClockAccuracyNs) (Decomposition, error) {
	if err := t.validate(acc); err != nil {
		return Decomposition{}, err
	}

	hops := make([]HopDelay, len(t.Hops))
	var sumResident, prop int64
	instrumented := 0
	topIdx := -1
	for i, h := range t.Hops {
		link := int64(h.LinkLengthM) * nsPerMeter
		prop += link
		d := HopDelay{
			Index:           h.Index,
			Device:          h.Device,
			Role:            h.Role,
			IngressPort:     h.IngressPort,
			EgressPort:      h.EgressPort,
			ClockDomain:     h.ClockDomain,
			HasData:         h.IntEnabled,
			QueueDepthBytes: h.QueueDepthBytes,
			QueueLimitBytes: h.QueueLimitBytes,
			EgressBps:       h.EgressBps,
			LinkPropNs:      link,
			Attribution:     AttrBlind,
		}
		if h.IntEnabled {
			instrumented++
			d.ResidentNs = h.EgressTsNs - h.IngressTsNs
			d.QueueNs = queueDelayNs(h.QueueDepthBytes, h.EgressBps)
			if d.QueueNs > d.ResidentNs {
				// 估值超过驻留，唯一可能的解释是两个量的口径不一致：
				// 队列深度是采样时刻的瞬时值，驻留是本包实际待的时间。
				// 按驻留封顶，并把这件事说出来——悄悄取个小值会把矛盾藏掉。
				d.Note = fmt.Sprintf("排队估值 %.1f µs 超过驻留 %.1f µs，按驻留封顶（队列深度是采样时刻的瞬时值）",
					us(d.QueueNs), us(d.ResidentNs))
				d.QueueNs = d.ResidentNs
			}
			d.ForwardNs = d.ResidentNs - d.QueueNs
			d.Attribution = AttrForward
			if d.QueueNs*2 >= d.ResidentNs {
				d.Attribution = AttrQueue
			}
			sumResident += d.ResidentNs
			if topIdx < 0 || d.QueueNs > hops[topIdx].QueueNs {
				topIdx = i
			}
		}
		hops[i] = d
	}

	first, last := t.Hops[0], t.Hops[len(t.Hops)-1]
	e2e := last.EgressTsNs - first.IngressTsNs
	firstAcc, err := acc.Accuracy(first.ClockDomain)
	if err != nil {
		return Decomposition{}, err
	}
	lastAcc, err := acc.Accuracy(last.ClockDomain)
	if err != nil {
		return Decomposition{}, err
	}
	clockErr := firstAcc + lastAcc

	gap := e2e - sumResident
	blindResident := gap - prop
	segments := blindSegments(t, hops)
	allocatable := len(segments) == 1
	if allocatable {
		segments[0].ShareNs = blindResident
		segments[0].Allocatable = true
	}

	d := Decomposition{
		TraceID:         t.ID,
		Desc:            t.Desc,
		HopCount:        len(t.Hops),
		Instrumented:    instrumented,
		HopCoverage:     float64(instrumented) / float64(len(t.Hops)),
		E2EMeasuredNs:   e2e,
		ClockErrNs:      clockErr,
		SumResidentNs:   sumResident,
		PropNs:          prop,
		GapNs:           gap,
		BlindResidentNs: blindResident,
		BlindLoNs:       blindResident - clockErr,
		BlindHiNs:       blindResident + clockErr,
		AttributedShare: float64(sumResident) / float64(e2e),
		Hops:            hops,
		Segments:        segments,
		Naive:           naiveCrossHop(t),
	}
	if topIdx >= 0 {
		d.TopQueueDevice = hops[topIdx].Device
		d.TopQueueNs = hops[topIdx].QueueNs
	}
	return d, nil
}

// blindSegments 找出「上一个有数据的跳 → 下一个有数据的跳」之间的段。
func blindSegments(t Trace, hops []HopDelay) []BlindSegment {
	var segs []BlindSegment
	prev := -1
	for i, h := range hops {
		if !h.HasData {
			continue
		}
		if prev >= 0 && i-prev > 1 {
			seg := BlindSegment{
				FromHop:    hops[prev].Index,
				ToHop:      h.Index,
				FromDevice: hops[prev].Device,
				ToDevice:   h.Device,
			}
			for j := prev + 1; j < i; j++ {
				seg.BlindHops = append(seg.BlindHops, hops[j].Index)
				seg.BlindDevice = append(seg.BlindDevice, hops[j].Device)
			}
			segs = append(segs, seg)
		}
		prev = i
	}
	return segs
}

// naiveCrossHop 算「跳与跳之间直接相减」，也就是很多工具默认做的事。
//
// 结果里那些负数不是物理：两台设备的时钟不在一个域，偏移差被算进了传播时延。
func naiveCrossHop(t Trace) NaiveView {
	v := NaiveView{}
	for i := 0; i+1 < len(t.Hops); i++ {
		a, b := t.Hops[i], t.Hops[i+1]
		if !a.IntEnabled || !b.IntEnabled {
			continue
		}
		raw := b.IngressTsNs - a.EgressTsNs
		if raw < 0 {
			v.Negatives++
		}
		if v.Count == 0 || raw < v.MinNs {
			v.MinNs = raw
		}
		if v.Count == 0 || raw > v.MaxNs {
			v.MaxNs = raw
		}
		v.Count++
	}
	return v
}

// Summary 是把 Decomposition 压成一行汇总用的形状：不含逐跳明细。
type Summary struct {
	TraceID         string  `json:"trace_id"`
	HopCount        int     `json:"hop_count"`
	Instrumented    int     `json:"instrumented_hops"`
	HopCoverage     float64 `json:"hop_coverage"`
	E2EMeasuredNs   int64   `json:"e2e_measured_ns"`
	ClockErrNs      int64   `json:"clock_err_ns"`
	SumResidentNs   int64   `json:"sum_resident_ns"`
	PropNs          int64   `json:"prop_ns"`
	GapNs           int64   `json:"gap_ns"`
	BlindResidentNs int64   `json:"blind_resident_ns"`
	BlindLoNs       int64   `json:"blind_lo_ns"`
	BlindHiNs       int64   `json:"blind_hi_ns"`
	AttributedShare float64 `json:"attributed_share"`
	TopQueueDevice  string  `json:"top_queue_device,omitempty"`
	TopQueueNs      int64   `json:"top_queue_ns"`
	BlindHops       int     `json:"blind_hops"`
	BlindSegments   int     `json:"blind_segments"`
}

// Summary 压掉逐跳明细，留汇总字段。
func (d Decomposition) Summary() Summary {
	s := Summary{
		TraceID:         d.TraceID,
		HopCount:        d.HopCount,
		Instrumented:    d.Instrumented,
		HopCoverage:     d.HopCoverage,
		E2EMeasuredNs:   d.E2EMeasuredNs,
		ClockErrNs:      d.ClockErrNs,
		SumResidentNs:   d.SumResidentNs,
		PropNs:          d.PropNs,
		GapNs:           d.GapNs,
		BlindResidentNs: d.BlindResidentNs,
		BlindLoNs:       d.BlindLoNs,
		BlindHiNs:       d.BlindHiNs,
		AttributedShare: d.AttributedShare,
		TopQueueDevice:  d.TopQueueDevice,
		TopQueueNs:      d.TopQueueNs,
		BlindSegments:   len(d.Segments),
	}
	for _, h := range d.Hops {
		if !h.HasData {
			s.BlindHops++
		}
	}
	return s
}
