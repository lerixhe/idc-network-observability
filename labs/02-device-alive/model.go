package main

import (
	"errors"
	"time"
)

// State 是判定层对一台设备的结论。探测本身只有「通 / 不通」两个值，
// 状态是连续多次探测加上别人的失败情况推出来的。
type State string

const (
	// StateUnknown 表示平台刚接入这台设备，还没有足够样本下结论。
	StateUnknown State = "unknown"
	StateUp      State = "up"
	StateDown    State = "down"
)

// EventKind 是事件流的种类。设备事件之外，探测侧故障也进同一条流——
// 它影响一整组设备的判定，必须和设备的上下线记在同一个时间轴上。
type EventKind string

const (
	KindDeviceDown     EventKind = "device-down"
	KindDeviceUp       EventKind = "device-up"
	KindProbeFault     EventKind = "probe-fault"
	KindProbeRecovered EventKind = "probe-recovered"
)

// Device 是一台被监测的设备。
//
// Room 与 Probe 构成「同源」分组：同机房、同探针的设备共享管理网与探测路径，
// 它们的探测结果不是相互独立的样本。判定「设备下线」还是「探测侧故障」全靠这个分组。
type Device struct {
	ID    string `json:"id"`
	Room  string `json:"room"`
	Probe string `json:"probe"`
}

// Group 返回同源分组键。
func (d Device) Group() string { return d.Room + "/" + d.Probe }

// Config 是判定策略的全部旋钮。所有参数显式可配，没有藏在代码里的常量。
type Config struct {
	Devices          int           // 设备规模
	Interval         time.Duration // 探测周期 P
	Duration         time.Duration // 仿真总时长
	FailThreshold    int           // 连续 N 次失败判失联
	RecoverThreshold int           // 连续 M 次成功判恢复
	CorrThreshold    float64       // 同源组失败比例 ≥ 该值，判为探测侧故障
	CorrMinPeers     int           // 同源组设备数少于该值，不做相关性判定
	CorrTicks        int           // 同源组连续满足可疑条件多少轮才确认（探测侧故障也要去抖）
	RebootSuspect    time.Duration // 离线时长不超过该值，标疑似重启
	Seed             int64
}

// Validate 挡住会让实验静默跑出错误结论的配置。
func (c Config) Validate() error {
	switch {
	case c.Devices < 1:
		return errors.New("设备数必须大于 0")
	case c.Interval <= 0:
		return errors.New("探测周期必须大于 0")
	case c.Duration < c.Interval:
		return errors.New("仿真时长至少要覆盖一个探测周期")
	case c.FailThreshold < 1:
		return errors.New("判失联的失败次数必须 ≥ 1")
	case c.RecoverThreshold < 1:
		return errors.New("判恢复的成功次数必须 ≥ 1")
	case c.CorrThreshold <= 0 || c.CorrThreshold > 1:
		return errors.New("同源失败比例阈值必须落在 (0, 1]")
	case c.CorrMinPeers < 2:
		return errors.New("同源组最小规模必须 ≥ 2，单台设备谈不上相关性")
	case c.CorrTicks < 1:
		return errors.New("同源组确认轮数必须 ≥ 1")
	case c.CorrTicks >= c.FailThreshold:
		// 参数耦合：确认探测侧故障比判设备失联还慢，整组设备会先被误判下线。
		return errors.New("同源组确认轮数必须小于判失联的失败次数，否则探测侧故障会先被当成设备下线")
	case c.RebootSuspect <= 0:
		return errors.New("疑似重启的时长阈值必须大于 0")
	}
	return nil
}

// Ticks 返回仿真推进的探测轮数。
func (c Config) Ticks() int {
	return int(c.Duration / c.Interval)
}

// AtMs 把第 tick 轮探测换算成虚拟时钟毫秒。虚拟时钟从 0 开始，
// 数字本身不对应真实日期——这个实验的量的是判定逻辑，不是墙上时间。
func (c Config) AtMs(tick int) int64 {
	return int64(tick) * c.Interval.Milliseconds()
}

// Evidence 是判定一条事件时手上握着的证据。事件没有证据不许发出去，
// 否则运维拿到的是一句「它挂了」和一个没法复核的结论。
type Evidence struct {
	ConsecutiveFails int     `json:"consecutive_fails,omitempty"`
	ConsecutiveOKs   int     `json:"consecutive_oks,omitempty"`
	PeerTotal        int     `json:"peer_total,omitempty"`
	PeerFailed       int     `json:"peer_failed"`
	PeerFailRatio    float64 `json:"peer_fail_ratio"`
	DowntimeMs       int64   `json:"downtime_ms,omitempty"`
	SuspectReboot    bool    `json:"suspect_reboot,omitempty"`
}

// Event 是事件流的一行。工单从这里生成，不从状态快照生成。
type Event struct {
	Seq      int       `json:"seq"`
	AtMs     int64     `json:"at_ms"`
	Kind     EventKind `json:"kind"`
	Subject  string    `json:"subject"` // 设备 ID，或同源分组键
	Evidence Evidence  `json:"evidence"`
}

// DeviceState 是快照里的一台设备。状态 + 当前连击数，没有历史。
type DeviceState struct {
	ID      string `json:"id"`
	Room    string `json:"room"`
	Probe   string `json:"probe"`
	State   State  `json:"state"`
	SinceMs int64  `json:"since_ms"`
	Fails   int    `json:"consecutive_fails"`
	OKs     int    `json:"consecutive_oks"`
}

// Counters 是两种输出的差别所在：快照只告诉你「现在多少 up」，
// 计数器告诉你「期间发生过多少次上下线」。
type Counters struct {
	Events         int `json:"events"`
	DeviceDown     int `json:"device_down"`
	DeviceUp       int `json:"device_up"`
	ProbeFault     int `json:"probe_fault"`
	ProbeRecovered int `json:"probe_recovered"`
	DownNow        int `json:"down_now"`
	UpNow          int `json:"up_now"`
	UnknownNow     int `json:"unknown_now"`
	// FlappedDevices 是「期间至少下线过一次、但最终快照里是 up」的设备数。
	// 这个数字只能从事件流算出来——快照里它们和一直健康的设备长得一模一样。
	FlappedDevices int `json:"flapped_devices"`
}

// Snapshot 是某一时刻的全量状态。它是给人看盘用的，不是给工单用的。
type Snapshot struct {
	Scenario string         `json:"scenario"`
	AtMs     int64          `json:"at_ms"`
	Config   SnapshotConfig `json:"config"`
	Counters Counters       `json:"counters"`
	Devices  []DeviceState  `json:"devices"`
}

// SnapshotConfig 把影响解读快照的旋钮一起存下来。换个 N 重跑，
// 快照里的 down 数量就变了，不记参数等于没法复核。
type SnapshotConfig struct {
	Devices          int     `json:"devices"`
	IntervalSeconds  float64 `json:"interval_seconds"`
	DurationSeconds  float64 `json:"duration_seconds"`
	FailThreshold    int     `json:"fail_threshold"`
	RecoverThreshold int     `json:"recover_threshold"`
	CorrThreshold    float64 `json:"corr_threshold"`
	CorrMinPeers     int     `json:"corr_min_peers"`
	CorrTicks        int     `json:"corr_ticks"`
}

// SnapshotConfigOf 从运行配置里摘出要落盘的那几个字段。
func SnapshotConfigOf(c Config) SnapshotConfig {
	return SnapshotConfig{
		Devices:          c.Devices,
		IntervalSeconds:  c.Interval.Seconds(),
		DurationSeconds:  c.Duration.Seconds(),
		FailThreshold:    c.FailThreshold,
		RecoverThreshold: c.RecoverThreshold,
		CorrThreshold:    c.CorrThreshold,
		CorrMinPeers:     c.CorrMinPeers,
		CorrTicks:        c.CorrTicks,
	}
}

// fmtDur 把人读的时长打出来。虚拟时钟的毫秒一律当 time.Duration 处理，
// 免得 90 秒显示成 90000 这种要心算的数字。
func fmtDur(ms int64) string {
	return (time.Duration(ms) * time.Millisecond).String()
}
