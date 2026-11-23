package main

import (
	"fmt"
	"time"
)

// 这个文件是留给你的。除了 Locate 的实现，其余部分（类型、常量、
// rackText、switchCount）都不要改——report.go 与 expected/endpoint.jsonl
// 都按这套结构走。
//
// 题目在 ../README.md 与 site/docs/05-cmdb/27-endpoint-locate.md，
// 三个陷阱（mac_drift、lag_logical_only、lease_only）的判据在 README 的坑列表里。
// 参考实现在 ../../11-endpoint/lookup.go。

// 三个陷阱的标记。输出里出现这三个词就是命中，测试按这三个词断言。
const (
	// TrapMacDrift 同一个 MAC 在本 VLAN 里出现在两个端口。虚拟机迁走后，
	// 旧交换机的表项要等老化周期跑完才消失，这段时间两处都能查到。
	TrapMacDrift = "mac_drift"
	// TrapLagLogical 聚合口的 FDB 表项只记逻辑口。逻辑口没有物理对端，
	// 机柜 U 位得从成员口反查。
	TrapLagLogical = "lag_logical_only"
	// TrapLeaseOnly ARP 已老化、二层表也没有，但 DHCP 租约还在。
	// 这时候答「查不到」是错的。
	TrapLeaseOnly = "lease_only"
)

// 联查结论只有四种。多一种就说明有一个阶段没想清楚。
const (
	VerdictLocated   = "located"    // 三张表接力走通
	VerdictArpOnly   = "arp_only"   // ARP 有，FDB 没有
	VerdictLeaseOnly = "lease_only" // ARP 老化，DHCP 租约还在
	VerdictNotFound  = "not_found"  // 三张表都没有
)

// Options 是联查里唯一能调的策略。老化门限决定「表项算不算数」，
// 调大它等于承认自己的采集频率不够。M 是策略不是常量。
type Options struct {
	ArpAging time.Duration
}

// RackSpot 是一条能落地的物理位置：某台交换机的某个成员口，在哪个机柜的哪个 U 位。
type RackSpot struct {
	Device string `json:"device"`
	Port   string `json:"port"`
	Rack   string `json:"rack"`
	U      int    `json:"u"`
	Desc   string `json:"desc,omitempty"`
}

// FdbHit 是一条 MAC 表命中，内部用。
type FdbHit struct {
	Device string
	Port   string
	VLAN   int
	AgeS   int
}

// StaleHit 是被判为旧的那条 FDB 表项。它不是错的，只是还没老化完。
type StaleHit struct {
	Device string `json:"device"`
	Port   string `json:"port"`
	VLAN   int    `json:"vlan"`
	AgeS   int    `json:"age_s"`
}

// Result 是一条 IP 的联查结论。Evidence 按执行顺序编号，
// 读者能一步步看出答案是在哪一环断的。
type Result struct {
	IP         string     `json:"ip"`
	VLAN       int        `json:"vlan"`
	MAC        string     `json:"mac,omitempty"`
	Verdict    string     `json:"verdict"`
	Switch     string     `json:"switch,omitempty"`
	Port       string     `json:"port,omitempty"`
	PortDesc   string     `json:"port_desc,omitempty"`
	Rack       string     `json:"rack,omitempty"`
	U          int        `json:"u,omitempty"`
	Candidates []RackSpot `json:"rack_candidates,omitempty"`
	Stale      []StaleHit `json:"stale,omitempty"`
	Traps      []string   `json:"traps"`
	Evidence   []string   `json:"evidence"`
	NextStep   string     `json:"next_step"`
}

// RackU 是给人看的机柜 U 位。
func (r Result) RackU() string { return rackText(r.Rack, r.U) }

func rackText(rack string, u int) string {
	if rack == "" {
		return "-"
	}
	if u == 0 {
		return rack
	}
	return fmt.Sprintf("%s/U%02d", rack, u)
}

// macCand 是一个候选 MAC 及其来源。ARP 的一条永远排前面，
// 租约那条只在 MAC 不同（换了网卡）时才追加。
type macCand struct {
	mac string
	src string
}

// fdbMatch 是一次 MAC 表反查的结果。stale 是被比下去的旧表项。
type fdbMatch struct {
	main  FdbHit
	mac   string
	stale []FdbHit
	src   string
	exact bool
}

// switchCount 是 FDB 快照覆盖到的二层设备数，漏采时要能在输出里看出来。
func (d *Dataset) switchCount() int {
	seen := map[string]bool{}
	for _, e := range d.FDB {
		seen[e.Switch] = true
	}
	return len(seen)
}

// Locate 是你要实现的东西。
//
// 输入：ds 是三张表的快照（ARP、FDB、DHCP 租约）加端口台账，ip 是待查的终端地址，
// opt.ArpAging 决定「表项算不算数」。
// 输出：一条 Result。Verdict 四选一（located / arp_only / lease_only / not_found），
// Evidence 按执行顺序编号，Traps 里塞命中的陷阱标记。
//
// 四条硬性要求，expected/endpoint.jsonl 会逐字节对：
//  1. 先试 ARP 的 MAC，再试租约的 MAC。ARP 是网关此刻的认知，比租约新。
//  2. 同一个 MAC 在本 VLAN 内命中两个端口 = mac_drift。留下 age 小的那条，
//     其余进 Stale，不要报错也不要猜。
//  3. FDB 命中逻辑口（Po / LAG）时要展开到成员口再查台账，并打 lag_logical_only。
//  4. ARP 老化但 DHCP 租约还在时，答案是 lease_only，不是 not_found。
func Locate(ds *Dataset, ip string, opt Options) Result {
	if opt.ArpAging <= 0 {
		opt.ArpAging = defaultArpAging
	}
	return Result{
		IP:       ip,
		Verdict:  VerdictNotFound,
		Traps:    []string{},
		NextStep: fmt.Sprintf("未实现：联查逻辑见 README；样本时间基准 %s", ds.TS.Format(time.RFC3339)),
	}
}
