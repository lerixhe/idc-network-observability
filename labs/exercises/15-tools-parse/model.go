// 实验 15：设备命令输出解析。
//
// 平台里总有一条「SSH 执行 show / display 命令 + 解析文本」的通路：
// 队列深度、buffer 使用、MAC 表、FDB、BGP 邻居状态这些数 SNMP 采不到，只能跑命令。
// 这条路最脆的一环是解析——固件一升级，列变多、单位改、列头改名，正则式写法当天就崩。
//
// 这里的做法：统一结构 + 按厂商与固件版本选模板 + 模板声明列的单位。
// 解析按列头的字符区间切片，不写死列位置，也不按空白切分（值为空时按空白切会整体错位）。
package main

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Unit 是计数器值的物理单位。三个值之间不存在默认换算关系：
// bytes 与 packets 之间要换算必须有平均帧长，个数（errors）根本不是流量。
// 所以单位是每条计数的属性，任何跨单位比较都要显式换算并标注来源。
type Unit string

const (
	UnitUnset   Unit = ""
	UnitBytes   Unit = "bytes"
	UnitPackets Unit = "packets"
	UnitCount   Unit = "count"
)

// Field 是对齐后的语义字段名。解析的产出不是「哪一列」，是「哪个语义」。
// 换厂商、换固件只改模板里的列名，字段名不动，下游查询不用跟着改。
type Field string

const (
	FieldPort        Field = "port"
	FieldSpeed       Field = "speed"
	FieldInBytes     Field = "in_bytes"
	FieldInPackets   Field = "in_packets"
	FieldInErrors    Field = "in_errors"
	FieldInDiscards  Field = "in_discards"
	FieldInPause     Field = "in_pause"
	FieldOutBytes    Field = "out_bytes"
	FieldOutPackets  Field = "out_packets"
	FieldOutErrors   Field = "out_errors"
	FieldOutDiscards Field = "out_discards"
	FieldQueueDrops  Field = "queue_drops"
)

// counterFields 是全部计数类字段，顺序就是报告里的列顺序。
// 增删字段只改这一处，报告与 JSON 输出跟着变。
var counterFields = []Field{
	FieldInBytes, FieldInPackets,
	FieldOutBytes, FieldOutPackets,
	FieldInErrors, FieldInDiscards,
	FieldOutErrors, FieldQueueDrops, FieldInPause, FieldOutDiscards,
}

// Counter 是一条计数。Value 已经按 SI 后缀展开到基本单位
// （1.29G → 1290000000），Raw 保留设备原样给你的字符串。
// 设备上看到 1.29G，库里查出来 1290000000，两者能对上账全靠 Raw。
type Counter struct {
	Value float64 `json:"value"`
	Unit  Unit    `json:"unit"`
	Raw   string  `json:"raw"`
}

// UnknownColumn 是模板里没有声明的列。它进丢弃清单，不进计数结构，
// 但值要留着——否则下次有人加模板时，得先把设备再登一遍。
type UnknownColumn struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// PortCounters 是一个端口在某个时刻的统一视图。
// 三份不同厂商的样本解析出来都是这个结构，端口名、单位、未知列都在里面。
type PortCounters struct {
	Device   string            `json:"device"`
	Vendor   string            `json:"vendor"`
	Firmware string            `json:"firmware"`
	Command  string            `json:"command"`
	Template string            `json:"template"` // 模板 ID@版本，例如 vendor-c-counters@v2
	Port     string            `json:"port"`
	Speed    string            `json:"speed,omitempty"`
	Counters map[Field]Counter `json:"counters"`
	Unknown  []UnknownColumn   `json:"unknown,omitempty"`
}

// Get 取一条计数。取不到就是不可得，绝不返回 0——0 是一个真实的值
// （比如 InErrors 为 0 表示没出错），拿 0 顶替「没这一列」会把缺失算成健康。
func (p PortCounters) Get(f Field) (Counter, bool) {
	if p.Counters == nil {
		return Counter{}, false
	}
	c, ok := p.Counters[f]
	return c, ok
}

// Value 取数值，缺失返回 0 与 false。调用方必须看第二个返回值。
func (p PortCounters) Value(f Field) (float64, bool) {
	c, ok := p.Get(f)
	return c.Value, ok
}

// AvgFrameBytes 用同一条记录的入向字节数与包数算出平均帧长。
// 这不是假设值，是设备自己给的两个数相除。固件只给包数时，
// 换算成字节的唯一依据就是它。
func (p PortCounters) AvgFrameBytes() (float64, bool) {
	in, ok1 := p.Value(FieldInBytes)
	pk, ok2 := p.Value(FieldInPackets)
	if !ok1 || !ok2 || pk <= 0 {
		return 0, false
	}
	return in / pk, true
}

// DerivedOutBytes 用平均帧长把出口包数折成字节，标成派生值。
// 出口包数与入口包数的平均帧长不一样（小包走一个方向很常见），
// 所以这个数只能用来「比量级」，不能进报表。
func (p PortCounters) DerivedOutBytes() (float64, bool) {
	frame, ok := p.AvgFrameBytes()
	if !ok {
		return 0, false
	}
	pk, ok := p.Value(FieldOutPackets)
	if !ok {
		return 0, false
	}
	return frame * pk, true
}

// Capture 是一次采集（一份样本里的一段输出）。漂移样本一个文件里有两段：
// 固件 3.9 与 4.2，同一个 @device，模板版本不同。
type Capture struct {
	Device   string
	Vendor   string
	Firmware string
	Command  string
	Template string
	Source   string // 样本文件名，报错时能指回哪一份
	Ports    []PortCounters
	Unknown  []string // 本段里所有未声明的列名（去重，按出现顺序）
	Warnings []string
}

// Dataset 是整个样本目录解析后的结果。
type Dataset struct {
	Captures []Capture
	MTR      []MTRRun
}

// UnitChange 是同一台设备跨固件版本的字段单位变化。
// 这是本实验的靶心：升级后列变多不会让解析崩，单位变了才会让数字静默变错。
type UnitChange struct {
	Device string `json:"device"`
	Field  Field  `json:"field"`
	From   string `json:"from"` // 形如 bytes（fw 3.9）
	To     string `json:"to"`   // 形如 不可得（fw 4.2）
}

// unitText 把单位渲染成给人看的形式。缺失单独写，不写空串——
// 空串在表格里跟「不关心这一列」长得一样。
func unitText(u Unit, present bool) string {
	if !present {
		return "不可得"
	}
	if u == UnitUnset {
		return "未声明"
	}
	return string(u)
}

// fieldUnits 汇总一段采集里每个字段的单位。同一段里同一个字段只有一个单位；
// 出现两个说明模板声明错了，这里取第一个并在报告里由模板自检兜住。
func (c Capture) fieldUnits() map[Field]Unit {
	units := make(map[Field]Unit, len(counterFields))
	for _, p := range c.Ports {
		for f, v := range p.Counters {
			if _, ok := units[f]; !ok {
				units[f] = v.Unit
			}
		}
	}
	return units
}

// UnitDrift 按设备分组、按样本里出现的顺序比较相邻两段采集的单位。
// 只比同一台设备的相邻版本——跨设备比单位没有意义。
func UnitDrift(caps []Capture) []UnitChange {
	var order []string
	byDevice := map[string][]Capture{}
	for _, c := range caps {
		if _, ok := byDevice[c.Device]; !ok {
			order = append(order, c.Device)
		}
		byDevice[c.Device] = append(byDevice[c.Device], c)
	}

	var out []UnitChange
	for _, dev := range order {
		seq := byDevice[dev]
		for i := 1; i < len(seq); i++ {
			before, after := seq[i-1], seq[i]
			ub, ua := before.fieldUnits(), after.fieldUnits()
			for _, f := range counterFields {
				vb, okb := ub[f]
				va, oka := ua[f]
				if okb == oka && vb == va {
					continue
				}
				out = append(out, UnitChange{
					Device: dev,
					Field:  f,
					From:   fmt.Sprintf("%s（fw %s）", unitText(vb, okb), before.Firmware),
					To:     fmt.Sprintf("%s（fw %s）", unitText(va, oka), after.Firmware),
				})
			}
		}
	}
	return out
}

// 设备在「这列没数」时给的占位符。认成 0 是最常见的解析事故：
// 一份固件改过的样本会解析出一堆 0，看板上一片绿。
var placeholders = map[string]bool{
	"": true, "-": true, "--": true, "---": true, "?": true, "??": true, "n/a": true, "na": true,
}

// parseCounter 解析一格值：去千位逗号、展开 SI 后缀、识别占位符。
// 返回 ok=false 表示这格是占位符（不是错误）；err 才是真的解析不了。
func parseCounter(raw string, unit Unit) (Counter, bool, error) {
	s := strings.TrimSpace(raw)
	if placeholders[strings.ToLower(s)] {
		return Counter{}, false, nil
	}

	num := strings.ReplaceAll(s, ",", "")
	scale := 1.0
	if n := len(num); n > 0 {
		switch num[n-1] {
		case 'k', 'K':
			scale, num = 1e3, num[:n-1]
		case 'm', 'M':
			scale, num = 1e6, num[:n-1]
		case 'g', 'G':
			scale, num = 1e9, num[:n-1]
		case 't', 'T':
			scale, num = 1e12, num[:n-1]
		}
	}

	v, err := strconv.ParseFloat(strings.TrimSpace(num), 64)
	if err != nil {
		return Counter{}, false, fmt.Errorf("%q 既不是数字也不是占位符", s)
	}
	return Counter{Value: v * scale, Unit: unit, Raw: s}, true, nil
}

// formatCount 打数字：整数不带小数点，小数留两位。
// 计数器本来是整数，出现小数只可能是 SI 后缀展开的结果。
func formatCount(v float64) string {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return "-"
	}
	if v == math.Trunc(v) && math.Abs(v) < 1e15 {
		return strconv.FormatFloat(v, 'f', -1, 64)
	}
	return strconv.FormatFloat(v, 'f', 2, 64)
}

// cell 是报告里一格：有数就打数，没数打「-」。
// 「-」与 0 必须长得不一样，这是这张表能用的前提。
func cell(p PortCounters, f Field) string {
	if v, ok := p.Value(f); ok {
		return formatCount(v)
	}
	return "-"
}
