package main

import "fmt"

// 这个文件是留给你的。除 Decide 以外都可以改，但别改 Verdict 的字段名——
// report.go 与 expected/verdicts.jsonl 都按这套字段名走。
//
// 判定树、六种成因、判据与陷阱在 ../README.md 与 ../../01-portstate/README.md 里。
// 想先看答案，去 ../../01-portstate/decide.go。

// 成因标签。六个是真成因，link_ok 与 unknown_down 是树的入口与兜底。
//
// 标签是机器读的（进 JSONL），中文名进表格。一个概念只留一种拼写。
const (
	CauseSelfAdminDown = "self_admin_down"
	CausePeerAdminDown = "peer_admin_down"
	CauseOpticMissing  = "optic_missing"
	CauseOpticPower    = "optic_power"
	CauseNegotiateFail = "negotiate_fail"
	CauseOneWay        = "one_way"
	CauseLinkOK        = "link_ok"
	CauseUnknownDown   = "unknown_down"
)

// 归属：这条 down 该记在谁头上。
const (
	OwnerChange   = "change"
	OwnerHardware = "hardware"
	OwnerConfig   = "config"
	OwnerFault    = "fault"
)

// causeZH / causeOwner / causeNext 是题面给的三张表，不必自己编。
// 你要做的是「判断落在哪一个成因上」，不是重新命名它们。
var causeZH = map[string]string{
	CauseSelfAdminDown: "本端 admin shutdown",
	CausePeerAdminDown: "对端 admin shutdown",
	CauseOpticMissing:  "光模块不在位/未识别",
	CauseOpticPower:    "光功率异常",
	CauseNegotiateFail: "协商失败（速率/双工）",
	CauseOneWay:        "单向链路",
	CauseLinkOK:        "无异常",
	CauseUnknownDown:   "未归因",
}

var causeOwner = map[string]string{
	CauseSelfAdminDown: OwnerChange,
	CausePeerAdminDown: OwnerChange,
	CauseOpticMissing:  OwnerHardware,
	CauseOpticPower:    OwnerHardware,
	CauseNegotiateFail: OwnerConfig,
	CauseOneWay:        OwnerFault,
	CauseUnknownDown:   OwnerFault,
	CauseLinkOK:        "",
}

var causeNext = map[string]string{
	CauseSelfAdminDown: "对变更单窗口，确认是否计划内",
	CausePeerAdminDown: "找对端设备的变更单，两端一起核",
	CauseOpticMissing:  "安排现场插拔或更换模块",
	CauseOpticPower:    "查光纤与两端收发，同型号模块做基线比对",
	CauseNegotiateFail: "改端口速率与双工配置，回滚后复测",
	CauseOneWay:        "查光纤与两端模块收发，重点看收光低的那一端",
	CauseUnknownDown:   "现场查光纤配线，并补采对端",
	CauseLinkOK:        "",
}

// 下面四个常量是判据的一部分，不是随便取的数。改动之前先读 README 的坑列表。
const (
	// symbolErrorDeltaLimit 是 dot3StatsSymbolErrors 单周期增量阈值。
	symbolErrorDeltaLimit = 500
	// sysUpTimeRegressMs：sysUpTime 比上一条回落超过这个值，认定重启过。
	sysUpTimeRegressMs = 5_000
	// agentColdStartMs 是冷启动期。sysUpTime 还在这个数以下的 Trap 一律丢弃。
	agentColdStartMs = 120_000
	// rxPowerDiffLimitDbm 是两端收光差的单向链路门限。
	rxPowerDiffLimitDbm = 10.0
)

// FlapPolicy 是抖动判定口径：WindowMs 跨度内状态翻转次数达到 MinFlips 即判 flapping。
type FlapPolicy struct {
	WindowMs int64
	MinFlips int
}

// defaultFlapPolicy 是命令行默认口径：10 分钟内翻转 4 次。
var defaultFlapPolicy = FlapPolicy{WindowMs: 600_000, MinFlips: 4}

// Verdict 是一个端口的判定结果。字段名与 JSONL 输出绑定，不要改。
type Verdict struct {
	Device    string `json:"device"`
	IfName    string `json:"if_name"`
	IfIndex   int    `json:"if_index"`
	LinkID    string `json:"link_id,omitempty"`
	Admin     string `json:"admin"`
	Oper      string `json:"oper"`
	Cause     string `json:"cause"`
	CauseZH   string `json:"cause_zh"`
	Owner     string `json:"owner"`
	Evidence  string `json:"evidence"`
	NextStep  string `json:"next_step"`
	FlapCount int    `json:"flap_count"`
	Flapping  bool   `json:"flapping"`
}

// Reboot 是一次识别到的设备（或 SNMP 代理）重启。
type Reboot struct {
	Device   string `json:"device"`
	TS       string `json:"ts"`
	UptimeMs int64  `json:"uptime_ms"`
	// Dropped 是该设备因冷启动期被剔除的 Trap 条数。
	Dropped int `json:"dropped"`
}

// Analysis 是一次完整分析的结果。
type Analysis struct {
	Verdicts []Verdict
	Reboots  []Reboot
	// DroppedTraps 是全部因冷启动期剔除的 Trap 条数。
	DroppedTraps int
}

// Decide 是你要实现的东西。
//
// 输入：ports 是同一时刻的快照（20 条），traps 是按到达顺序录的 Trap 序列（19 条，
// 跨设备交错，自己排序），policy 是抖动口径。
// 输出：每条端口一个 Verdict（顺序按 device 名 + if_index 排，与 expected 对齐），
// 外加识别到的重启与剔除条数。
//
// 三条硬性要求，expected/verdicts.jsonl 会逐字节对：
//  1. 端口必须按 LinkID 严格成对。配不上或一对多的链路直接返回错误，
//     不要猜一个对端——猜出来的对端会把单向链路判成正常。
//  2. Evidence 写判据，不写结论。判据是「哪个字段等于几」，结论在 Cause 里。
//  3. FlapCount 只数剔除冷启动期之后的翻转；Flapping 由 policy 判，不改 Cause。
//
// 现在的实现直接返回错误，`make run` 会打印「未实现」并退出 1。实现后 `make check`。
func Decide(ports []PortState, traps []Trap, policy FlapPolicy) (Analysis, error) {
	return Analysis{}, fmt.Errorf("未实现：判定树见 README，完成后跑 make check")
}
