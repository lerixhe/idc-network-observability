package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"sort"
	"time"
)

// 32 位计数器（MIB-II 的 ifInOctets / ifInUcastPkts）的量程。
// 满量程 4294967295，比 4 GiB 少一个字节。1G 口满速时这一圈 34.36 秒走完。
const (
	counter32Bits = 32
	counter32Mod  = uint64(1) << counter32Bits // 4294967296
	counter32Max  = counter32Mod - 1           // 4294967295

	counter64Bits = 64

	// sysUpTime 是 TimeTicks，单位 1/100 秒，同样是 32 位。
	// 满量程 4294967295 tick = 42949672.96 秒 ≈ 497.1 天，设备连续开机 497 天它自己也会回绕。
	ticksPerSecond = 100
	uptimeMod      = uint64(1) << 32
)

// Series 是一个端口的一段计数器采样序列。
//
// CounterBits 决定量程，SpeedBps 决定增量上限，两个数一乘就是回绕周期的倒数。
// 换用 ifHCInOctets（64 位）之后回绕周期变成几千年，速率算法只剩「按时间归一」这一件事。
type Series struct {
	Device      string   `json:"device"`
	IfName      string   `json:"if_name"`
	IfAlias     string   `json:"if_alias"`
	IfIndex     int      `json:"if_index"`
	Counter     string   `json:"counter"`
	CounterBits int      `json:"counter_bits"`
	SpeedBps    uint64   `json:"speed_bps"`
	Note        string   `json:"note"`
	Samples     []Sample `json:"samples"`
}

// Sample 是一次采集。AtMs 是相对样本文件起点的毫秒数。
type Sample struct {
	AtMs           int64  `json:"t_ms"`
	Value          uint64 `json:"value"`
	SysUpTimeTicks uint64 `json:"sys_uptime_ticks"`
}

// Key 是端口主键。采集侧绝不能用 ifIndex 当主键——设备重启后端口编号会重排。
func (s Series) Key() string { return s.Device + "/" + s.IfName }

// BytesPerSecond 是计数器每秒最多能涨多少。字节计数器按线速全部打满算。
func (s Series) BytesPerSecond() float64 { return float64(s.SpeedBps) / 8 }

// modulus 是计数器的量程，超过它就回零。
func (s Series) modulus() uint64 {
	if s.CounterBits == counter64Bits {
		return math.MaxUint64
	}
	return counter32Mod
}

// bps 把「每秒字节增量」翻成 bps。
func (s Series) bps(bytesPerSecond float64) float64 { return bytesPerSecond * 8 }

// WrapPeriod 是计数器从 0 走满一圈再回到 0 的时间，等于量程除以每秒增量。
//
// 第二个返回值是 false 表示这个周期长到没有工程意义（64 位字节计数器满速 1G 约 4676 年），
// 按「不回绕」处理，不必再谈回绕检测。
func WrapPeriod(bits int, perSecond float64) (time.Duration, bool) {
	if perSecond <= 0 {
		return 0, false
	}
	seconds := math.Pow(2, float64(bits)) / perSecond
	if seconds*float64(time.Second) > float64(math.MaxInt64) {
		return 0, false
	}
	return time.Duration(seconds * float64(time.Second)), true
}

// validate 挡住会让实验静默跑出错误结论的样本。
func (s Series) validate() error {
	switch {
	case s.Device == "":
		return errors.New("样本缺少 device")
	case s.IfName == "":
		return fmt.Errorf("%s：样本缺少 if_name", s.Device)
	case s.CounterBits != counter32Bits && s.CounterBits != counter64Bits:
		return fmt.Errorf("%s %s：counter_bits 只能是 32 或 64，实际 %d", s.Device, s.IfName, s.CounterBits)
	case s.SpeedBps == 0:
		return fmt.Errorf("%s %s：speed_bps 不能为 0，否则算不出回绕周期", s.Device, s.IfName)
	case len(s.Samples) < 2:
		return fmt.Errorf("%s %s：至少两个样本才能算出一个区间，实际 %d 个", s.Device, s.IfName, len(s.Samples))
	}
	seen := make(map[int64]bool, len(s.Samples))
	for i, sm := range s.Samples {
		// 32 位计数器的合法取值上限是 2^32-1，不是 2^32：多一个字节就是越界。
		if s.CounterBits == counter32Bits && sm.Value > counter32Max {
			return fmt.Errorf("%s %s：第 %d 个样本值 %d 超出 32 位量程上限 %d", s.Device, s.IfName, i, sm.Value, counter32Max)
		}
		if sm.SysUpTimeTicks > uptimeMod-1 {
			return fmt.Errorf("%s %s：第 %d 个样本的 sysUpTime %d 超出 TimeTicks 量程", s.Device, s.IfName, i, sm.SysUpTimeTicks)
		}
		if seen[sm.AtMs] {
			return fmt.Errorf("%s %s：时间戳 %dms 上出现了两个样本，不知道该用哪个", s.Device, s.IfName, sm.AtMs)
		}
		seen[sm.AtMs] = true
	}
	return nil
}

// orderedSample 带上「文件里出现的位置与时间顺序不一致」的标记。
//
// 采集器合并多个批次时乱序很常见。按文件顺序算差值会得到一串负数，
// 看着像回绕，其实只是记录被追加上一条迟到的样本。乱序要显式标出来，不能静默排序了事。
type orderedSample struct {
	Sample
	moved bool
}

// orderSamples 按时间戳排序，同时数出有多少个样本的位置变了。
func orderSamples(in []Sample) ([]orderedSample, int) {
	idx := make([]int, len(in))
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool { return in[idx[a]].AtMs < in[idx[b]].AtMs })
	out := make([]orderedSample, len(in))
	moved := 0
	for pos, src := range idx {
		out[pos] = orderedSample{Sample: in[src], moved: src != pos}
		if src != pos {
			moved++
		}
	}
	return out, moved
}

// NominalInterval 是样本时间戳间隔的中位数，代表采集器的标称周期。
//
// 用中位数不用均值：丢几轮样之后均值会被拉歪，中位数还是原来那个周期。
func NominalInterval(samples []Sample) time.Duration {
	if len(samples) < 2 {
		return 0
	}
	gaps := make([]int64, 0, len(samples)-1)
	for i := 1; i < len(samples); i++ {
		if d := samples[i].AtMs - samples[i-1].AtMs; d > 0 {
			gaps = append(gaps, d)
		}
	}
	if len(gaps) == 0 {
		return 0
	}
	sort.Slice(gaps, func(i, j int) bool { return gaps[i] < gaps[j] })
	return time.Duration(gaps[len(gaps)/2]) * time.Millisecond
}

// seriesFile 是 samples/counters.json 的外层结构。
type seriesFile struct {
	Series []Series `json:"series"`
}

func loadSeries(path string) ([]Series, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var f seriesFile
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("解析 %s 失败：%w", path, err)
	}
	if len(f.Series) == 0 {
		return nil, fmt.Errorf("%s 里没有 series", path)
	}
	return f.Series, nil
}

// Truth 是样本生成时记下的真实情况：几圈回绕、几次重启、漏了几段、乱序几处。
// 判定层看不到它，跑完拿它对账。
type Truth struct {
	Device      string `json:"device"`
	IfName      string `json:"if_name"`
	Wraps       int    `json:"wraps"`
	Resets      int    `json:"resets"`
	UptimeWraps int    `json:"uptime_wraps"`
	Gaps        int    `json:"gaps"`
	Reorders    int    `json:"reorders"`
}

// Key 与 Series.Key 同形，用来对齐两边。
func (t Truth) Key() string { return t.Device + "/" + t.IfName }

// truthFile 是 samples/truth.json 的外层结构。
type truthFile struct {
	Series []Truth `json:"series"`
}

func loadTruth(path string) (map[string]Truth, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var f truthFile
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("解析 %s 失败：%w", path, err)
	}
	out := make(map[string]Truth, len(f.Series))
	for _, t := range f.Series {
		out[t.Key()] = t
	}
	return out, nil
}
