package main

import (
	"fmt"
	"math"
	"time"
)

// wrapWarnRatio 是采集周期相对回绕周期的红线：达到回绕周期的一半就必须告警。
//
// 一半这个数的来历：两个样本之间的增量要是能塞进半个量程，
// 观测到的差值就既可能是「没回绕」，也可能是「回绕一圈之后又涨了这么多」，
// 分母上差一整圈，算出来的速率没有任何意义。所以红线定在半圈，不是整圈。
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

// ComputeRates 把一条采样序列算成三列速率。
func ComputeRates(s Series, nominal time.Duration) (SeriesRates, error) {
	if err := s.validate(); err != nil {
		return SeriesRates{}, err
	}
	ordered, moved := orderSamples(s.Samples)
	if nominal <= 0 {
		flat := make([]Sample, len(ordered))
		for i, o := range ordered {
			flat[i] = o.Sample
		}
		nominal = NominalInterval(flat)
	}
	if nominal <= 0 {
		return SeriesRates{}, fmt.Errorf("%s %s：样本里没有正的时间间隔，推不出标称采集周期", s.Device, s.IfName)
	}

	wrapPeriod, wrapKnown := WrapPeriod(s.CounterBits, s.BytesPerSecond())
	res := SeriesRates{
		Series:     s,
		Nominal:    nominal,
		WrapPeriod: wrapPeriod,
		WrapKnown:  wrapKnown,
		Reorders:   moved,
		Points:     make([]Interval, 0, len(ordered)-1),
	}
	mod := s.modulus()
	unjudgeable, overspeed := 0, 0

	for i := 1; i < len(ordered); i++ {
		prev, cur := ordered[i-1], ordered[i]
		dtMs := cur.AtMs - prev.AtMs
		p := Interval{
			FromMs:    prev.AtMs,
			ToMs:      cur.AtMs,
			DtSeconds: float64(dtMs) / 1000,
			RawDelta:  int64(cur.Value) - int64(prev.Value),
			Split:     1,
			Trusted:   true,
			Reordered: prev.moved || cur.moved,
		}

		// 漏采：实际间隔是标称周期的几倍，就少了几轮。0.5 的容差吃掉采集抖动。
		if rounds := int(math.Round(p.DtSeconds / nominal.Seconds())); rounds > 1 {
			p.Gap = rounds - 1
			res.Gaps++
		}

		// sysUpTime 先判：它决定「计数器变负」到底是回绕还是重启。
		p.Reset, p.UptimeWrap = uptimeVerdict(prev.Sample, cur.Sample, dtMs)

		// v1：差值除以标称周期。不认回绕，也不管这次间隔其实是多少。
		p.Naive = s.bps(float64(p.RawDelta) / nominal.Seconds())
		// v2：差值除以实际间隔。认间隔，仍然不认回绕。
		p.Normalized = s.bps(float64(p.RawDelta) / p.DtSeconds)

		// v3：回绕补正，再按漏采轮数插值。
		corrected := float64(p.RawDelta)
		if p.Reset {
			// 计数器归零，差值没有意义，补一个量程只会把噪声算成流量。
			corrected = 0
		} else if cur.Value < prev.Value {
			if s.CounterBits == counter32Bits {
				p.Wraps = 1
				corrected = float64(cur.Value + mod - prev.Value)
			}
		}
		p.WrapInterp = s.bps(corrected / p.DtSeconds)
		if p.Gap > 0 && !p.Reset {
			p.Split = p.Gap + 1
		}

		// 回绕次数不可判的两种情况，都算「不可信」，不许静默出数。
		// 判据用观测增量本身，不用线速：一个只跑了 1.6 Gbps 的 10G 口，10 秒里只涨 2e9，
		// 离半量程还有距离，这一段就是可信的；跑满了才不可判。配置合不合理是另一件事，
		// 那一条告警在端口级发，不能拿它把每一段都判死。
		switch {
		case !p.Reset && corrected >= float64(mod)/2:
			p.Ambiguous = true
			p.Reason = fmt.Sprintf("观测增量 %.0f ≥ 量程一半 %.0f，圈数不可判，这一段不可信", corrected, float64(mod)/2)
			unjudgeable++
		case !p.Reset && p.WrapInterp > float64(s.SpeedBps)*(1+1e-6):
			p.Ambiguous = true
			p.Reason = fmt.Sprintf("补正后 %.2f Gbps 超过端口线速 %.2f Gbps，圈数不可判，这一段不可信",
				p.WrapInterp/1e9, float64(s.SpeedBps)/1e9)
			overspeed++
		}

		if p.Reset {
			p.Trusted = false
			res.Resets++
		}
		if p.UptimeWrap {
			res.UptimeWraps++
		}
		if p.Wraps > 0 && !p.Ambiguous {
			// 只有补正可信的区间才算进回绕计数；不可判的那几段不进账。
			res.Wraps += p.Wraps
		}
		if p.Ambiguous {
			p.Trusted = false
		}
		if !p.Trusted {
			res.Untrusted++
		}

		if p.Reason == "" {
			p.Reason = reasonFor(s, p, wrapKnown)
		}
		res.Points = append(res.Points, p)
	}

	// 端口级的两条告警各只发一次：配置踩红线是这台设备的采集参数问题，
	// 不随区间重复；不可判的区段数量单独计数，免得刷满一屏。
	if wrapKnown && nominal.Seconds() >= wrapPeriod.Seconds()*wrapWarnRatio {
		res.ConfigRisk = true
		res.Warnings = append(res.Warnings, fmt.Sprintf(
			"%s：采集周期 %s ≥ 满速回绕周期 %s（2^%d / %.0f B/s）的一半 %s，这个端口跑满时圈数不可判；采集周期必须小于 %s，或换 64 位计数器",
			s.Key(), shortDur(nominal.Seconds()), shortDur(wrapPeriod.Seconds()), s.CounterBits, s.BytesPerSecond(),
			shortDur(wrapPeriod.Seconds()*wrapWarnRatio), shortDur(wrapPeriod.Seconds()/2)))
	}
	if unjudgeable > 0 {
		res.Warnings = append(res.Warnings, fmt.Sprintf(
			"%s：%d 段观测增量超过量程一半（%d 位计数器 %d），回绕次数不可判，已标不可信",
			s.Key(), unjudgeable, s.CounterBits, mod/2))
	}
	if overspeed > 0 {
		res.Warnings = append(res.Warnings, fmt.Sprintf(
			"%s：%d 段补正后超过端口线速 %s，说明不止回绕一圈或少记了间隔，已标不可信",
			s.Key(), overspeed, fmtBps(float64(s.SpeedBps))))
	}
	return res, nil
}

// reasonFor 把区间上那一堆标记翻成一句人读的话。
func reasonFor(s Series, p Interval, wrapKnown bool) string {
	switch {
	case p.Reset:
		return "设备重启：sysUpTime 归零，计数器也回到 0，这一段不可信"
	case p.Wraps > 0:
		out := fmt.Sprintf("回绕 %d 圈（+%d），v2 在这一格出负数", p.Wraps, counter32Mod)
		if p.UptimeWrap {
			out += "；sysUpTime 同时回绕，不是重启"
		}
		return out
	case p.UptimeWrap:
		return "sysUpTime 回绕（497 天），计数器仍在增长 → 不是重启，速率可信"
	case p.Reordered && p.Gap > 0:
		return fmt.Sprintf("乱序样本，已按时间戳归位；漏采 %d 轮，v3 拆 %d 段插值", p.Gap, p.Split)
	case p.Reordered:
		return "乱序样本，已按时间戳归位"
	case p.Gap > 0:
		return fmt.Sprintf("漏采 %d 轮，v1 按标称周期算会放大 %d 倍；v3 拆 %d 段插值", p.Gap, p.Split, p.Split)
	case !wrapKnown:
		return "64 位计数器，回绕周期长到不用管"
	default:
		return "正常"
	}
}

// uptimeVerdict 只看 sysUpTime 就分开「重启」和「uptime 自己回绕」。
//
// sysUpTime 是 32 位 TimeTicks，单调不减，减小只有两种解释：
// 设备重启过，或者设备连续开机 497 天让它自己走到了量程尽头。
// 靠算术自洽分开：上一个值加上这段间隔越过 2^32 的，是回绕；没越过的，是重启。
func uptimeVerdict(prev, cur Sample, dtMs int64) (reset, uptimeWrap bool) {
	if cur.SysUpTimeTicks >= prev.SysUpTimeTicks {
		return false, false
	}
	elapsed := uint64(dtMs) / (1000 / ticksPerSecond)
	after := (prev.SysUpTimeTicks + elapsed) % uptimeMod
	tolerance := elapsed/5 + ticksPerSecond // 20% 的采集抖动，再加 1 秒
	if prev.SysUpTimeTicks+elapsed >= uptimeMod && absDiffU(cur.SysUpTimeTicks, after) <= tolerance {
		return false, true
	}
	return true, false
}

func absDiffU(a, b uint64) uint64 {
	if a > b {
		return a - b
	}
	return b - a
}

// shortDur 把秒数说成「34.36s」「1m2s」这类紧凑形式，报告里列宽有限。
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
