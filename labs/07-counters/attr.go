package main

import (
	"encoding/json"
	"fmt"
	"os"
)

// 归因结论的四个归属。线缆和模块都在物理层，区分靠「符号错 / 校验失败帧」的比例：
// 编码层成片出错是模块（或光路衰减），帧校验零星失败而编码层干净是线缆与连接器。
const (
	OwnerCable      = "cable"
	OwnerModule     = "module"
	OwnerCongestion = "congestion"
	OwnerHost       = "host"
	OwnerClean      = "clean"
)

// symbolFrameRatio 是模块与线缆的分界：一个校验失败帧伴随 8 个以上符号错，判模块。
//
// 一个坏符号能让一个帧校验失败，所以符号错通常多于帧错；数量级接近或反过来，
// 说明坏的不是编码，是帧在线上被打断——那才是线缆、连接器、端面污损的形状。
const symbolFrameRatio = 8

// 帧错率的两条线。百万帧里坏 1 个以下当噪声，坏 100 个以上别观察了，直接换件。
// 1G 口按 480 字节的均值跑 300 秒是 4680 万个帧，1 ppm 就是 47 个坏帧。
const (
	visiblePPM           = 1.0
	frameErrorReplacePPM = 100.0
)

// ErrCounters 是一个端口在一个观测窗口内的错包与丢弃计数。
//
// 计数器分四组，对应四个归属方向：校验失败与符号错看物理层，
// 队列丢与缓存丢看拥塞，主机侧计数器看端主机。交换机上全干净而业务侧丢包，
// 唯一的证据就在主机那头。
type ErrCounters struct {
	Device      string  `json:"device"`
	IfName      string  `json:"if_name"`
	IfAlias     string  `json:"if_alias"`
	SpeedBps    uint64  `json:"speed_bps"`
	WindowS     float64 `json:"window_s"`
	InOctets    uint64  `json:"in_octets"`     // 窗口内入向字节增量，算利用率
	InUcastPkts uint64  `json:"in_ucast_pkts"` // 窗口内入向单播帧增量，当错误率的分母

	FcsErrors    uint64 `json:"fcs_errors"`    // CRC / FCS / 对齐错合并成一类：帧校验失败
	SymbolErrors uint64 `json:"symbol_errors"` // 编码层（PMA）符号错

	InBufferDiscards uint64 `json:"in_buffer_discards"` // 入向缓存不足丢（超订）
	OutQueueDiscards uint64 `json:"out_queue_discards"` // 出向队列尾丢（拥塞）
	OutDiscards      uint64 `json:"out_discards"`       // 出向丢弃总数，含队列丢

	HostRxDrops     uint64 `json:"host_rx_drops"`    // 主机侧接收丢弃：socket 缓冲满 / ring 满
	HostRetransmits uint64 `json:"host_retransmits"` // 主机侧重传段数

	Note string `json:"note"` // 样本注入的真实情况，归因逻辑看不到
}

// Key 与 Series.Key 同形，报告里两边能对上。
func (c ErrCounters) Key() string { return c.Device + "/" + c.IfName }

// Attribution 是一个端口的归因结论与支撑它的算术。
type Attribution struct {
	Counters ErrCounters `json:"counters"`
	Owner    string      `json:"owner"`
	Verdict  string      `json:"verdict"`
	Reasons  []string    `json:"reasons"`
	Also     []string    `json:"also,omitempty"`
	NextStep string      `json:"next_step"`

	FramePPM     float64 `json:"frame_ppm"`      // 每百万帧的校验失败数
	SymbolPerErr float64 `json:"symbol_per_err"` // 每个校验失败帧摊到的符号错
	DropPPM      float64 `json:"drop_ppm"`       // 每百万帧的丢弃数
	UtilPct      float64 `json:"util_pct"`       // 入向利用率
}

// Key 与 ErrCounters.Key 一致。
func (a Attribution) Key() string { return a.Counters.Key() }

// Attribute 按证据组合给端口定一个主归属。
//
// 规则顺序是刻意的：先排除「交换机干净、主机在丢」这一格，
// 再按物理层 → 拥塞的顺序排。物理错与拥塞同时存在时，先修物理层，
// 因为坏包会拖垮上层重传，把拥塞指标一起带坏。
func Attribute(c ErrCounters) Attribution {
	a := Attribution{Counters: c}
	if c.WindowS <= 0 {
		a.Owner = OwnerClean
		a.Verdict = "无法归因"
		a.Reasons = []string{"窗口长度缺失，所有速率都没有分母"}
		return a
	}
	if c.InUcastPkts > 0 {
		a.FramePPM = float64(c.FcsErrors) / float64(c.InUcastPkts) * 1e6
		a.DropPPM = float64(c.OutQueueDiscards+c.InBufferDiscards) / float64(c.InUcastPkts) * 1e6
	}
	if c.SpeedBps > 0 {
		a.UtilPct = float64(c.InOctets) * 8 / (float64(c.SpeedBps) * c.WindowS) * 100
	}
	if c.FcsErrors > 0 {
		a.SymbolPerErr = float64(c.SymbolErrors) / float64(c.FcsErrors)
	}

	queueVisible := c.OutQueueDiscards+c.InBufferDiscards > 0 && a.DropPPM >= visiblePPM
	hostVisible := c.HostRxDrops+c.HostRetransmits > 0
	physicalVisible := c.FcsErrors > 0 || c.SymbolErrors > 0

	switch {
	case !physicalVisible && !queueVisible && hostVisible:
		a.Owner = OwnerHost
		a.Verdict = "主机"
		a.Reasons = []string{fmt.Sprintf(
			"交换机侧校验失败 0、符号错 0、队列丢 0，主机报了 %d 个接收丢弃 / %d 次重传",
			c.HostRxDrops, c.HostRetransmits)}
		a.NextStep = "查主机 socket 缓冲与软中断分布（netstat -s 的 receive buffer errors），交换机不是瓶颈"

	case c.SymbolErrors > 0 && float64(c.SymbolErrors) >= symbolFrameRatio*float64(mathMaxU64(c.FcsErrors, 1)):
		a.Owner = OwnerModule
		a.Verdict = "模块"
		a.Reasons = []string{fmt.Sprintf(
			"符号错 %d，校验失败帧 %d，每个坏帧摊到 %.0f 个符号错（阈值 %d）——编码层在成片出错",
			c.SymbolErrors, c.FcsErrors, a.SymbolPerErr, symbolFrameRatio)}
		a.NextStep = "换模块，先换收端；两端对换能一次定死是哪一头"

	case c.FcsErrors > 0:
		a.Owner = OwnerCable
		a.Verdict = "线缆"
		a.Reasons = []string{fmt.Sprintf(
			"校验失败帧 %d（%.1f ppm），符号错 %d（每坏帧 %.2f 个，低于阈值 %d）——编码层干净，坏在帧上",
			c.FcsErrors, a.FramePPM, c.SymbolErrors, a.SymbolPerErr, symbolFrameRatio)}
		a.NextStep = "换纤、清端面；光纤看收发功率，铜缆看长度与串扰"

	case queueVisible && c.OutQueueDiscards > 0:
		a.Owner = OwnerCongestion
		a.Verdict = "拥塞"
		a.Reasons = []string{fmt.Sprintf(
			"出向队列尾丢 %d（%.1f ppm），校验失败 0、符号错 0——队列满了，不是线坏了",
			c.OutQueueDiscards, a.DropPPM)}
		a.NextStep = "看这条链路的利用率与微突发（第 40 课），调队列长度或改 ECMP 权重"

	case queueVisible && c.InBufferDiscards > 0:
		a.Owner = OwnerCongestion
		a.Verdict = "拥塞"
		a.Reasons = []string{fmt.Sprintf(
			"入向缓存不足丢 %d（%.1f ppm），校验失败 0——入口缓存被超订打穿",
			c.InBufferDiscards, a.DropPPM)}
		a.NextStep = "算下行口到上行口的超订比，入口限速或换上行"

	default:
		a.Owner = OwnerClean
		a.Verdict = "干净"
		a.Reasons = []string{fmt.Sprintf(
			"校验失败 0、符号错 0、队列与缓存丢弃 %.1f ppm（出向丢弃总数 %d）；低于可见门限 %.0f ppm，当噪声",
			a.DropPPM, c.OutDiscards, visiblePPM)}
		a.NextStep = "没有可修的，去查对端或上层"
	}

	// 次要发现：主结论之外还有别的东西在丢，写清楚，别让读者以为修完主因就完事。
	if a.Owner != OwnerCongestion && queueVisible {
		a.Also = append(a.Also, fmt.Sprintf("同时有丢弃 %d 个（%.1f ppm），物理层修好之后拥塞问题还在", c.OutQueueDiscards+c.InBufferDiscards, a.DropPPM))
	}
	if (a.Owner == OwnerCable || a.Owner == OwnerModule) && hostVisible {
		a.Also = append(a.Also, fmt.Sprintf("主机侧也报了 %d 个接收丢弃，坏包会一路传染到主机", c.HostRxDrops))
	}
	if a.UtilPct >= 80 {
		a.Also = append(a.Also, fmt.Sprintf("入向利用率 %.1f%%，先把容量与流量形状看一遍再动手", a.UtilPct))
	}
	if a.FramePPM > 0 && a.FramePPM < visiblePPM {
		a.Also = append(a.Also, fmt.Sprintf("帧错率 %.2f ppm，低于 %.0f ppm，可以观察不必立刻换件", a.FramePPM, visiblePPM))
	}
	if (a.Owner == OwnerCable || a.Owner == OwnerModule) && a.FramePPM >= frameErrorReplacePPM {
		a.Also = append(a.Also, fmt.Sprintf("帧错率 %.1f ppm 超过 %.0f ppm 的换件线，别只记工单", a.FramePPM, frameErrorReplacePPM))
	}
	return a
}

// mathMaxU64 取两个无符号数的较大者，下限至少 1，避免除零。
func mathMaxU64(a, b uint64) uint64 {
	if a > b {
		return a
	}
	return b
}

// errFile 是 samples/errors.json 的外层结构。
type errFile struct {
	Ports []ErrCounters `json:"ports"`
}

func loadErrCounters(path string) ([]ErrCounters, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var f errFile
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("解析 %s 失败：%w", path, err)
	}
	if len(f.Ports) == 0 {
		return nil, fmt.Errorf("%s 里没有 ports", path)
	}
	for i, c := range f.Ports {
		if c.Device == "" || c.IfName == "" {
			return nil, fmt.Errorf("%s 第 %d 个端口缺少 device 或 if_name", path, i)
		}
	}
	return f.Ports, nil
}
