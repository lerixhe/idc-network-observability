package main

import (
	"fmt"
	"time"
)

// 这个文件是留给你的。类型、常量、maxRate、shortDur 都不要改——
// report.go 与 expected/rates.jsonl 都按这套结构走。
//
// 题目：三种速率算法各算一遍。分岔点自己会露出来——丢样让 v1 与 v2 分岔，
// 回绕让 v2 与 v3 分岔。样本里埋了 1G/10G 回绕、重启清零、漏采、乱序。
// 判据与坑列表在 ../README.md，参考实现在 ../../07-counters/rate.go。

const wrapWarnRatio = 0.5

// Interval 是一个采集区间上的三份速率结论。
//
// 三份结果都算，分岔点才会自己露出来：
// v1 与 v2 的差别在间隔不等（丢样），v2 与 v3 的差别在回绕。
type Interval struct {
	FromMs     int64   `json:"from_ms"`         // 区间起点
	ToMs       int64   `json:"to_ms"`           // 区间终点
	DtSeconds  float64 `json:"dt_s"`            // 实际间隔
	RawDelta   int64   `json:"delta"`           // 直接相减的差，回绕时是负数
	Naive      float64 `json:"naive_bps"`       // v1：差值 / 标称周期
	Normalized float64 `json:"normalized_bps"`  // v2：差值 / 实际间隔
	WrapInterp float64 `json:"wrap_interp_bps"` // v3：回绕补正后 / 实际间隔

	Wraps      int  `json:"wraps"`       // 补正的回绕圈数
	Reset      bool `json:"reset"`       // 设备重启，计数器归零
	UptimeWrap bool `json:"uptime_wrap"` // sysUpTime 自己回绕了（497 天），不是重启
	Gap        int  `json:"gap"`         // 按标称周期折算，漏采了几轮
	Split      int  `json:"split"`       // v3 把这个区间拆成几段（1 = 没拆）
	Ambiguous  bool `json:"ambiguous"`   // 回绕次数不可判，数值不可信
	Trusted    bool `json:"trusted"`     // 这一段算出来的速率能不能用
	Reordered  bool `json:"reordered"`   // 区间端点里至少一个在文件里是乱序的

	Reason string `json:"reason"` // 一句话判定
}

// SeriesRates 是一个端口整段序列的结论与计数。
type SeriesRates struct {
	Series      Series
	Nominal     time.Duration // 标称采集周期
	WrapPeriod  time.Duration // 满速回绕周期
	WrapKnown   bool          // false 表示量程大到不回绕
	ConfigRisk  bool          // 采集周期踩了半圈红线，跑满就会算错
	Points      []Interval
	Warnings    []string
	Wraps       int
	Resets      int
	UptimeWraps int
	Gaps        int
	Reorders    int
	Untrusted   int
}

// maxRate 是这个端口观测到的最大可信速率，用来判断有没有算过线速。
func (r SeriesRates) maxRate() float64 {
	out := 0.0
	for _, p := range r.Points {
		if p.Trusted && p.WrapInterp > out {
			out = p.WrapInterp
		}
	}
	return out
}

func shortDur(seconds float64) string {
	d := time.Duration(seconds * float64(time.Second))
	switch {
	case d < time.Second:
		return fmt.Sprintf("%.0fms", seconds*1000)
	case d < time.Minute:
		return d.Round(10 * time.Millisecond).String()
	default:
		return d.Round(time.Second).String()
	}
}

// ComputeRates 是你要实现的东西之一。
//
// 输入：一条采样序列（已按时间排好，乱序的条数与位置在 orderSamples 里标了），
// nominal 是标称采集周期，0 表示交给 NominalInterval 推。
// 输出：整段的结论 + 每个区间的三份速率。
//
// 三份速率必须都算，分岔点才有意义：
//   v1 Naive        差值 ÷ 标称周期。丢样时它偏高（分母小），回绕时它是负数
//   v2 Normalized   差值 ÷ 实际间隔。丢样修好了，回绕仍然错
//   v3 WrapInterp   回绕补正后再插值。要处理「补几圈不可判」与「重启归零」
//
// 硬性要求：
//  1. 差值为负先分清是回绕还是重启。看 sysUpTime：归零是重启，连续增长是回绕；
//     sysUpTime 自己回绕（约 497 天）不是重启
//  2. 增量 ≥ 量程的一半时，圈数不可判，这段标 Ambiguous 且 Trusted=false
//  3. 采集周期 ≥ 回绕周期的 1/2 时，把 ConfigRisk 置真（满了就会算错）
//  4. 每个区间写一句 Reason，说清这段为什么可信或不可信
func ComputeRates(s Series, nominal time.Duration) (SeriesRates, error) {
	return SeriesRates{}, fmt.Errorf("未实现：三种速率算法见 README")
}
