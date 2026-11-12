package main

import (
	"fmt"
	"sort"
	"strings"
)

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

// causeOwner 是成因到归属的唯一映射。端口 up 时没有归属，输出空串。
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

// causeNext 是成因对应的下一步动作。归属回答「谁」，这里回答「做什么」，
// 两个拼起来才是能派工的三元组——只输出一个 down，等于把判断又推回给人。
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

const (
	// symbolErrorDeltaLimit 是 dot3StatsSymbolErrors 单周期增量阈值。
	// 速率对不上但符号错包不涨，多半只是速率配置没生效统计，别急着判协商失败。
	symbolErrorDeltaLimit = 500
	// sysUpTimeRegressMs：sysUpTime 比上一条回落超过这个值，认定重启过。
	sysUpTimeRegressMs = 5_000
	// agentColdStartMs 是冷启动期。sysUpTime 还在这个数以下的 Trap 一律丢弃。
	agentColdStartMs = 120_000
	// rxPowerDiffLimitDbm 是两端收光差的单向链路门限。两端都 up 时链路层
	// 看着正常，一侧收光比另一侧低过 10 dB，那个方向的衰耗已经在账单上了。
	rxPowerDiffLimitDbm = 10.0
)

// FlapPolicy 是抖动判定口径：WindowMs 跨度内状态翻转次数达到 MinFlips 即判 flapping。
// M 和 N 是策略，不同设备档位可以给不同值，所以做成参数而不是常量。
type FlapPolicy struct {
	WindowMs int64
	MinFlips int
}

// FlapConfig 是命令行默认口径：10 分钟内翻转 4 次。
var defaultFlapPolicy = FlapPolicy{WindowMs: 600_000, MinFlips: 4}

// Verdict 是一个端口的判定结果。
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

// Reboot 是一次识别到的设备（或 SNMP 代理）重启。重启后的 Trap 不可信，
// 得先剔掉再数抖动，否则一个重启能刷出一屏假 flapping。
type Reboot struct {
	Device string `json:"device"`
	// TS 是重启后第一条 Trap 的时间。
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

// Decide 是判定树的主入口：先按 link_id 配对，再逐口走树，最后数抖动。
//
// 链路必须严格成对，配不上或一对多都直接报错——台账错了就得先修台账，
// 猜一个对端出来只会把单向链路判成正常。
func Decide(ports []PortState, traps []Trap, policy FlapPolicy) (Analysis, error) {
	peers, err := linkPeers(ports)
	if err != nil {
		return Analysis{}, err
	}
	flips, reboots, dropped := countFlips(traps, byIfIndex(ports), policy)

	verdicts := make([]Verdict, 0, len(ports))
	for _, p := range ports {
		cause, evidence := decidePort(p, peers[p.key()])
		k := p.key()
		verdicts = append(verdicts, Verdict{
			Device:    p.Device,
			IfName:    p.IfName,
			IfIndex:   p.IfIndex,
			LinkID:    p.LinkID,
			Admin:     adminName(p.IfAdminStatus),
			Oper:      operName(p.IfOperStatus),
			Cause:     cause,
			CauseZH:   causeZH[cause],
			Owner:     causeOwner[cause],
			Evidence:  evidence,
			NextStep:  causeNext[cause],
			FlapCount: flips[k],
			Flapping:  flips[k] >= policy.MinFlips,
		})
	}
	sort.SliceStable(verdicts, func(i, j int) bool {
		if verdicts[i].Device != verdicts[j].Device {
			return verdicts[i].Device < verdicts[j].Device
		}
		return verdicts[i].IfIndex < verdicts[j].IfIndex
	})

	return Analysis{Verdicts: verdicts, Reboots: reboots, DroppedTraps: dropped}, nil
}

// linkPeers 把 link_id 两端连起来，返回「端口 → 对端端口」。
func linkPeers(ports []PortState) (map[portKey]*PortState, error) {
	byLink := make(map[string][]int, len(ports))
	seen := make(map[portKey]bool, len(ports))
	for i, p := range ports {
		if p.IfAdminStatus != adminUp && p.IfAdminStatus != adminDown {
			return nil, fmt.Errorf("%s %s: if_admin_status=%d 不是合法取值", p.Device, p.IfName, p.IfAdminStatus)
		}
		if seen[p.key()] {
			return nil, fmt.Errorf("%s %s: 快照里出现了重复端口", p.Device, p.IfName)
		}
		seen[p.key()] = true
		if p.LinkID != "" {
			byLink[p.LinkID] = append(byLink[p.LinkID], i)
		}
	}
	peers := make(map[portKey]*PortState, len(ports))
	for id, members := range byLink {
		if len(members) != 2 {
			return nil, fmt.Errorf("链路 %s 挂了 %d 个端口，实验要求严格成对", id, len(members))
		}
		a, b := &ports[members[0]], &ports[members[1]]
		peers[a.key()] = b
		peers[b.key()] = a
	}
	return peers, nil
}

// decidePort 走一遍判定树，返回成因与判据。
//
// 顺序是这一课的核心，不能随便调：
//
//  1. 人为变更优先。admin shutdown 的口一定收不到光，先看物理层就会
//     把一次计划割接判成光模块故障，故障数里混进一批变更单。
//  2. 再往物理层走：模块在位 → 识别得出型号 → 有收光 → 光功率达标。
//  3. 然后配置层：速率对不上，且符号错包在涨。
//  4. 最后才拿对端比对，剩下的归「单向链路」或未归因。
func decidePort(p PortState, peer *PortState) (cause, evidence string) {
	if p.IfAdminStatus == adminDown {
		return CauseSelfAdminDown, "ifAdminStatus=2（本端手工 shutdown）"
	}
	if peer != nil && peer.IfAdminStatus == adminDown {
		ev := fmt.Sprintf("对端 %s %s ifAdminStatus=2（对端手工 shutdown）", peer.Device, peer.IfName)
		if p.operUp() {
			ev += "；本端仍报 ifOperStatus=up，快照有时差"
		}
		return CausePeerAdminDown, ev
	}

	if p.operUp() {
		return decideUpPort(p, peer)
	}

	// 以下都是本端 admin up、oper 非 up 的情形。
	if p.Optic == nil {
		return CauseOpticMissing, "快照里没有光模块实体（optic 为空）"
	}
	if !p.Optic.Present {
		return CauseOpticMissing, "光模块不在位（present=false）"
	}
	if strings.TrimSpace(p.Optic.Model) == "" {
		return CauseOpticMissing, "光模块在位但型号字段为空（未识别）"
	}
	if p.Optic.RxPowerDbm == nil {
		return CauseOpticPower, "DDM 收光读数缺失，没有可用的光功率"
	}
	if rx := *p.Optic.RxPowerDbm; rx <= p.Optic.RxLowAlarmDbm {
		return CauseOpticPower, fmt.Sprintf("收光 %.1f dBm 低于告警门限 %.1f dBm",
			rx, p.Optic.RxLowAlarmDbm)
	}
	if p.ExpectMbps > 0 && p.IfSpeedMbps != p.ExpectMbps {
		if delta := symbolErrorDelta(p); delta >= symbolErrorDeltaLimit {
			return CauseNegotiateFail, fmt.Sprintf(
				"ifSpeed=%d Mbps 与预期 %d Mbps 不符，dot3StatsSymbolErrors 本周期 +%d（阈值 %d）",
				p.IfSpeedMbps, p.ExpectMbps, delta, symbolErrorDeltaLimit)
		}
	}
	if peer == nil {
		return CauseUnknownDown, fmt.Sprintf("ifOperStatus=%s，对端未纳入快照，没有对端可比", operName(p.IfOperStatus))
	}
	if peer.operUp() {
		return CauseOneWay, fmt.Sprintf("本端 ifOperStatus=%s 而对端 %s %s ifOperStatus=up（本端收不到对端的光）",
			operName(p.IfOperStatus), peer.Device, peer.IfName)
	}
	return CauseUnknownDown, fmt.Sprintf("两端均非 up（本端 %s，对端 %s），模块在位、光功率与速率正常，需现场查光纤配线",
		operName(p.IfOperStatus), operName(peer.IfOperStatus))
}

// decideUpPort 处理本端 up 的端口。端口 up 不等于链路通，这是单向链路那一类
// 唯一能被抓住的位置。两条判据：对端状态不一致，或两端收光差越限。
func decideUpPort(p PortState, peer *PortState) (cause, evidence string) {
	if peer == nil {
		return CauseLinkOK, "本端 ifOperStatus=up；对端未纳入快照，单向无法验证"
	}
	if !peer.operUp() {
		return CauseOneWay, fmt.Sprintf("本端 ifOperStatus=up 而对端 %s %s ifOperStatus=%s（对端收不到本端的光）",
			peer.Device, peer.IfName, operName(peer.IfOperStatus))
	}
	if ev := rxImbalance(p, peer); ev != "" {
		return CauseOneWay, ev
	}
	return CauseLinkOK, "两端 ifOperStatus 均 up"
}

// rxImbalance 判两端收光是否失衡，成立时返回判据，否则返回空串。
//
// 两端都 up 的时候链路层没任何异常，唯一的线索是这一侧收到的光比对面少了
// 10 dB 以上：光衰偏大、端面脏了、模块在老化，都属于「链路还没断，但快了」。
//
// 这个判据的前提是两端同型号。不同型号的正常收光本来就能差 6 dB 以上，
// 拿这个门限去套异型号，会把一批健康链路报成单向。
func rxImbalance(p PortState, peer *PortState) string {
	if p.Optic == nil || peer.Optic == nil {
		return ""
	}
	if p.Optic.RxPowerDbm == nil || peer.Optic.RxPowerDbm == nil {
		return ""
	}
	here, there := *p.Optic.RxPowerDbm, *peer.Optic.RxPowerDbm
	diff := there - here
	if diff < 0 {
		diff = -diff
	}
	if diff <= rxPowerDiffLimitDbm {
		return ""
	}
	low := fmt.Sprintf("%s %s", p.Device, p.IfName)
	if there < here {
		low = fmt.Sprintf("%s %s", peer.Device, peer.IfName)
	}
	return fmt.Sprintf(
		"两端 ifOperStatus 均 up，但收光差 %.1f dB 超过 %.0f dB 门限（本端 %.1f dBm / 对端 %s %s %.1f dBm），低的那端是 %s",
		diff, rxPowerDiffLimitDbm, here, peer.Device, peer.IfName, there, low)
}

// symbolErrorDelta 算本周期 dot3StatsSymbolErrors 增量。计数器回退按 0 处理，
// 否则一次采集侧重置能刷出个天文数字，直接误判协商失败。
func symbolErrorDelta(p PortState) int64 {
	if p.SymbolErrors < p.SymbolErrorsPrev {
		return 0
	}
	return p.SymbolErrors - p.SymbolErrorsPrev
}

// byIfIndex 建立 device + ifIndex → ifName 的映射。Trap 里只有 ifIndex，
// 少了这张表就没法把 Trap 记到具体端口上。
func byIfIndex(ports []PortState) map[indexKey]string {
	m := make(map[indexKey]string, len(ports))
	for _, p := range ports {
		m[indexKey{device: p.Device, ifIndex: p.IfIndex}] = p.IfName
	}
	return m
}

type indexKey struct {
	device  string
	ifIndex int
}

// countFlips 数每个端口在窗口内的状态翻转次数，同时识别设备重启。
//
// 两条规则缺一不可：
//
//  1. sysUpTime 比上一条明显回落，认定设备（或 SNMP 代理）重启过；
//  2. sysUpTime 还在冷启动期内的 Trap 直接丢弃，不计入抖动。
//
// 只做第 1 条，重启后紧跟的那一串假 Trap 照样会算成抖动；只做第 2 条，
// 碰到 sysUpTime 不清零的实现就失效。两条都留着，互为印证。
func countFlips(traps []Trap, idx map[indexKey]string, policy FlapPolicy) (flips map[portKey]int, reboots []Reboot, dropped int) {
	byDevice := make(map[string][]Trap, 4)
	for _, t := range traps {
		byDevice[t.Device] = append(byDevice[t.Device], t)
	}
	devices := make([]string, 0, len(byDevice))
	for dev := range byDevice {
		devices = append(devices, dev)
	}
	sort.Strings(devices)

	usable := make([]Trap, 0, len(traps))
	for _, dev := range devices {
		list := byDevice[dev]
		sort.SliceStable(list, func(i, j int) bool { return list[i].unixMs < list[j].unixMs })

		droppedHere := 0
		prevUp := int64(-1)
		for _, t := range list {
			if prevUp >= 0 && prevUp-t.SysUpTimeMs > sysUpTimeRegressMs {
				reboots = append(reboots, Reboot{Device: dev, TS: t.TS, UptimeMs: t.SysUpTimeMs})
			}
			prevUp = t.SysUpTimeMs

			if t.SysUpTimeMs < agentColdStartMs {
				droppedHere++
				continue
			}
			usable = append(usable, t)
		}
		if droppedHere > 0 {
			if n := len(reboots); n > 0 && reboots[n-1].Device == dev {
				reboots[n-1].Dropped = droppedHere
			}
			dropped += droppedHere
		}
	}

	byPort := make(map[portKey][]int64, len(usable))
	for _, t := range usable {
		name, ok := idx[indexKey{device: t.Device, ifIndex: t.IfIndex}]
		if !ok {
			// 快照里没这个 ifIndex，映射不上就不数——宁可漏，不要记错人。
			continue
		}
		k := portKey{device: t.Device, ifName: name}
		byPort[k] = append(byPort[k], t.unixMs)
	}

	flips = make(map[portKey]int, len(byPort))
	for k, ts := range byPort {
		sort.SliceStable(ts, func(i, j int) bool { return ts[i] < ts[j] })
		flips[k] = maxInWindow(ts, policy.WindowMs)
	}
	return flips, reboots, dropped
}

// maxInWindow 返回任意 WindowMs 跨度内的最大事件数（滑动窗口）。
// 输入必须按时间升序。判抖动用窗口内最多的一次，而不是整段历史的总数。
func maxInWindow(sorted []int64, windowMs int64) int {
	best, left := 0, 0
	for right := range sorted {
		for sorted[right]-sorted[left] > windowMs {
			left++
		}
		if n := right - left + 1; n > best {
			best = n
		}
	}
	return best
}
