package main

import (
	"encoding/json"
	"fmt"
	"os"
)

// 这个文件是留给你的另一半。常量、类型、loadErrCounters 不要改。
//
// 题目：同一份错包计数，四个方向各给证据——线缆、模块、拥塞、主机。
// 分界靠两个比例：符号错 ÷ 校验失败帧、每百万帧的坏帧数。
// 判据在 ../README.md，参考实现在 ../../07-counters/attr.go。

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

// Attribute 是你要实现的另一半。
//
// 输入：一个端口在一个窗口内的错包与丢弃计数。
// 输出：主归属 + verdict + Reasons（支撑它的算术）+ NextStep + 四个派生比例。
//
// 规则顺序是刻意的，不能调：
//  1. 先排除「交换机全干净、主机在丢」这一格。证据只在主机侧计数器里，
//     交换机侧看起来一点问题都没有
//  2. 物理层优先于拥塞。坏包会拖垮上层重传，把拥塞指标一起带坏
//  3. 物理层内部按 symbolFrameRatio 分线缆与模块：符号错成片是模块或光路衰减，
//     帧校验零星失败而编码层干净是线缆与连接器
//  4. 帧错率过 frameErrorReplacePPM 就别观察了，直接换件
//
// 分母别弄错：错误率按「百万帧」算，分母是 in_ucast_pkts；
// 利用率按 in_octets 与 window_s 算。两个分母不通用。
func Attribute(c ErrCounters) Attribution {
	return Attribution{
		Counters: c,
		Verdict:  "未实现：四类归因见 README",
		NextStep: "实现 Attribute，然后跑 make check",
	}
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
