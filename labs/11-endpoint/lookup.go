package main

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

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

// Locate 跑一遍 IP → MAC → 交换机端口 → 机柜/U 位的接力，
// 每一环都把证据写进结果，断在哪一环看 Verdict。
func Locate(ds *Dataset, ip string, opt Options) Result {
	if opt.ArpAging <= 0 {
		opt.ArpAging = defaultArpAging
	}
	now := ds.TS
	r := Result{IP: ip, Verdict: VerdictNotFound, Traps: []string{}}

	arp, arpFound := ds.pickARP(ip)
	lease, leaseFound := ds.leaseState(ip, now)

	if arpFound {
		r.VLAN = arp.VLAN
	}
	if r.VLAN == 0 && leaseFound {
		r.VLAN = lease.Latest.VLAN
	}

	arpUsable := false
	if arpFound {
		arpUsable = appendArpEvidence(&r, arp, now, opt.ArpAging)
	}
	if leaseFound {
		appendLeaseEvidence(&r, lease, now)
	}

	cands := buildMACCandidates(arp, arpFound, lease, leaseFound)
	if len(cands) == 0 {
		if !arpFound {
			r.Evidence = append(r.Evidence, "ARP  两台网关的 ARP 表里没有这个 IP")
		}
		if !leaseFound {
			r.Evidence = append(r.Evidence, "租约 DHCP 日志里没有这个 IP 的任何事件")
		}
		r.NextStep = "先确认这个 IP 属于哪个网段、网关是哪台；跨三层查要把所有三层设备的 ARP 表采全，漏一台就有一批 IP 永远定位不到"
		return r
	}
	r.MAC = cands[0].mac

	m, hit := ds.matchFDB(cands, r.VLAN)
	if !hit {
		return finishWithoutFdb(ds, r, arpFound, arpUsable, lease, leaseFound)
	}

	// 以下三张表都接上了。
	r.Verdict = VerdictLocated
	r.MAC = m.mac
	r.Switch = m.main.Device
	r.Port = m.main.Port

	if len(m.stale) > 0 {
		r.Traps = append(r.Traps, TrapMacDrift)
		for _, s := range m.stale {
			r.Stale = append(r.Stale, StaleHit{Device: s.Device, Port: s.Port, VLAN: s.VLAN, AgeS: s.AgeS})
		}
		parts := []string{fmt.Sprintf("%s %s（age %ds，最新）", m.main.Device, m.main.Port, m.main.AgeS)}
		for _, s := range m.stale {
			parts = append(parts, fmt.Sprintf("%s %s（age %ds）", s.Device, s.Port, s.AgeS))
		}
		r.Evidence = append(r.Evidence, fmt.Sprintf(
			"FDB  vlan %d 命中 %d 条：%s——同一个 MAC 落在多个端口，按 age 取最新的，其余标 stale",
			m.main.VLAN, len(m.stale)+1, strings.Join(parts, " · ")))
	} else {
		r.Evidence = append(r.Evidence, fmt.Sprintf(
			"FDB  %s vlan %d %s 学到 MAC %s，表项 age %ds",
			m.main.Device, m.main.VLAN, m.main.Port, m.mac, m.main.AgeS))
	}
	if !m.exact {
		r.Evidence = append(r.Evidence, fmt.Sprintf(
			"FDB  命中表项在 vlan %d，而 ARP 里这个 IP 属于 vlan %d——广播域对不上，位置要人工核",
			m.main.VLAN, r.VLAN))
	}
	if m.src == "lease" {
		r.Evidence = append(r.Evidence, "MAC  ARP 表项已不可用，反查用的是 DHCP 租约给的 MAC "+m.mac)
	}

	applyInventory(&r, ds, m.main.Device, m.main.Port)
	fillNextStep(&r)
	return r
}

// finishWithoutFdb 处理「ARP 这一环或者整个二楼都没有」的三种收尾。
// 这三种长得很像，结论完全不同，分开答才有用。
func finishWithoutFdb(ds *Dataset, r Result, arpFound, arpUsable bool, lease LeaseState, leaseFound bool) Result {
	switch {
	case arpUsable:
		r.Verdict = VerdictArpOnly
		r.Evidence = append(r.Evidence, fmt.Sprintf(
			"FDB  %d 台二层交换机的快照里都没有 MAC %s 的表项：ARP 有、二层表没有",
			ds.switchCount(), r.MAC))
		r.NextStep = "ARP 与租约都新鲜，断在 MAC 表这一环：先确认这台终端的接入交换机在采集清单里，再查它是否落在没采的 VLAN"
	case leaseFound && lease.Active:
		l := lease.Latest
		r.Verdict = VerdictLeaseOnly
		r.Traps = append(r.Traps, TrapLeaseOnly)
		r.Evidence = append(r.Evidence, fmt.Sprintf(
			"FDB  %d 台二层交换机的快照里没有 MAC %s 的表项，ARP 表项也已过老化门限——租约在、现网未见",
			ds.switchCount(), r.MAC))
		r.NextStep = fmt.Sprintf(
			"别回「查不到」：DHCP 租约 %s 还挂在 %s（%s）名下，终端多半关机或网线拔了；按主机名找值班台账，位置按上次上线记录核",
			l.IP, normalizeMAC(l.MAC), l.Hostname)
	default:
		r.Verdict = VerdictNotFound
		if !arpFound {
			r.Evidence = append(r.Evidence, "ARP  两台网关的 ARP 表里没有这个 IP")
		}
		if !leaseFound {
			r.Evidence = append(r.Evidence, "租约 DHCP 日志里没有这个 IP 的任何事件")
		}
		r.NextStep = "三张表都空：先确认这个 IP 属于哪个网段、网关是哪台；跨三层查要把所有三层设备的 ARP 表采全"
	}
	return r
}

// pickARP 取这个 IP 最新的一条 ARP 表项。两台网关都记到同一个 IP 是常态
// （VRRP 双活、同一个 VRF 两边都有会话），取 last_seen 最新的那条。
func (d *Dataset) pickARP(ip string) (ArpEntry, bool) {
	var best ArpEntry
	found := false
	for _, e := range d.ARP {
		if e.IP != ip {
			continue
		}
		if !found || e.LastSeen.After(best.LastSeen) {
			best, found = e, true
		}
	}
	return best, found
}

// appendArpEvidence 判断 ARP 表项能不能当依据，并把理由写进证据。
// 老化门限是我们的容忍度，state 是设备侧给的狀態：incomplete 一定不可用，
// 其余看 age——门限放宽到 1 小时，一个 12 分钟前的 stale 表项就又能用了。
func appendArpEvidence(r *Result, arp ArpEntry, now time.Time, aging time.Duration) bool {
	age := now.Sub(arp.LastSeen)
	if arp.State == "incomplete" {
		r.Evidence = append(r.Evidence, fmt.Sprintf(
			"ARP  %s vlan %d %s state=incomplete：网关在问，链路上没人应答",
			arp.Gateway, arp.VLAN, arp.IfName))
		return false
	}
	if age <= aging {
		note := ""
		if arp.State != "reachable" {
			note = "；设备侧标的是 " + arp.State + "，网关还没重新确认过"
		}
		r.Evidence = append(r.Evidence, fmt.Sprintf(
			"ARP  %s vlan %d %s state=%s，last_seen %s前，未过老化门限 %s%s",
			arp.Gateway, arp.VLAN, arp.IfName, arp.State, durText(age), durText(aging), note))
		return true
	}
	r.Evidence = append(r.Evidence, fmt.Sprintf(
		"ARP  %s vlan %d %s state=%s，last_seen %s前，已过老化门限 %s（表项不可作定位依据）",
		arp.Gateway, arp.VLAN, arp.IfName, arp.State, durText(age), durText(aging)))
	return false
}

func appendLeaseEvidence(r *Result, lease LeaseState, now time.Time) {
	l := lease.Latest
	if !lease.Active {
		r.Evidence = append(r.Evidence, fmt.Sprintf(
			"租约 %s 最新事件 %s @%s（%s），不作为依据",
			l.IP, l.Event, l.TS.Format(time.RFC3339), lease.Reason))
		return
	}
	r.Evidence = append(r.Evidence, fmt.Sprintf(
		"租约 %s %s → %s（%s）vlan %d，%s 到期，剩余 %s",
		l.Server, l.IP, normalizeMAC(l.MAC), l.Hostname, l.VLAN,
		l.Expire.Format(time.RFC3339), durText(l.Expire.Sub(now))))
}

// macCand 是一个候选 MAC 及其来源。ARP 的一条永远排前面，
// 租约那条只在 MAC 不同（换了网卡）时才追加。
type macCand struct {
	mac string
	src string
}

func buildMACCandidates(arp ArpEntry, arpFound bool, lease LeaseState, leaseFound bool) []macCand {
	var out []macCand
	if arpFound {
		if m := normalizeMAC(arp.MAC); m != "" {
			out = append(out, macCand{mac: m, src: "arp"})
		}
	}
	if leaseFound && lease.Active {
		m := normalizeMAC(lease.Latest.MAC)
		if m == "" {
			return out
		}
		if len(out) == 0 || out[0].mac != m {
			out = append(out, macCand{mac: m, src: "lease"})
		}
	}
	return out
}

// fdbMatch 是一次 MAC 表反查的结果。stale 是被比下去的旧表项。
type fdbMatch struct {
	main  FdbHit
	mac   string
	stale []FdbHit
	src   string
	exact bool
}

// matchFDB 按候选顺序反查，第一个有命中的胜出。
// 先用 ARP 的 MAC：它是网关此刻的认知，比租约新。
func (d *Dataset) matchFDB(cands []macCand, vlan int) (fdbMatch, bool) {
	for _, c := range cands {
		hits, exact := d.fdbByMAC(c.mac, vlan)
		if len(hits) == 0 {
			continue
		}
		return fdbMatch{main: hits[0], mac: c.mac, stale: hits[1:], src: c.src, exact: exact}, true
	}
	return fdbMatch{}, false
}

// fdbByMAC 返回某个 MAC 的表项，age 小的排前面。
// 同 MAC 跨 VLAN 的表项不算漂移（那是 PVID 翻译或伪造），先按 VLAN 过滤；
// 一条都没有时再放宽，并回报 exact=false 让调用方提示广播域不对。
func (d *Dataset) fdbByMAC(mac string, vlan int) (hits []FdbHit, exact bool) {
	var all []FdbHit
	for _, e := range d.FDB {
		if normalizeMAC(e.MAC) != mac {
			continue
		}
		all = append(all, FdbHit{Device: e.Switch, Port: e.Port, VLAN: e.VLAN, AgeS: e.AgeS})
	}
	if len(all) == 0 {
		return nil, false
	}
	sort.SliceStable(all, func(i, j int) bool {
		if all[i].AgeS != all[j].AgeS {
			return all[i].AgeS < all[j].AgeS
		}
		if all[i].Device != all[j].Device {
			return all[i].Device < all[j].Device
		}
		return all[i].Port < all[j].Port
	})
	if vlan == 0 {
		return all, false
	}
	for _, h := range all {
		if h.VLAN == vlan {
			hits = append(hits, h)
		}
	}
	if len(hits) == 0 {
		return all, false
	}
	return hits, true
}

// switchCount 是 FDB 快照覆盖到的二层设备数，漏采时要能在输出里看出来。
func (d *Dataset) switchCount() int {
	seen := map[string]bool{}
	for _, e := range d.FDB {
		seen[e.Switch] = true
	}
	return len(seen)
}

// applyInventory 把交换机端口翻成机柜 U 位。聚合逻辑口要展开到成员口，
// 逻辑口自己没位置——它没有物理对端。
func applyInventory(r *Result, ds *Dataset, device, port string) {
	rec, found := ds.Inv.Port(device, port)
	switch {
	case !found:
		if isAggregatePortName(port) {
			r.Traps = append(r.Traps, TrapLagLogical)
		}
		r.Evidence = append(r.Evidence, fmt.Sprintf(
			"台账 没有 %s %s 这条记录，机柜 U 位无从落", device, port))
	case len(rec.Members) > 0:
		applyLag(r, ds, rec)
	default:
		r.Rack, r.U = rec.Rack, rec.U
		r.PortDesc = rec.Desc
		r.Evidence = append(r.Evidence, fmt.Sprintf(
			"台账 %s %s → %s  %s", rec.Device, rec.Port, rackText(rec.Rack, rec.U), rec.Desc))
	}
}

func applyLag(r *Result, ds *Dataset, rec PortRecord) {
	r.Traps = append(r.Traps, TrapLagLogical)
	r.PortDesc = rec.Desc

	var spots []RackSpot
	for _, name := range rec.Members {
		mrec, ok := ds.Inv.Port(rec.Device, name)
		if !ok {
			continue
		}
		spots = append(spots, RackSpot{
			Device: rec.Device, Port: name, Rack: mrec.Rack, U: mrec.U, Desc: mrec.Desc,
		})
	}
	r.Candidates = spots

	switch {
	case len(spots) == 0:
		r.Evidence = append(r.Evidence, fmt.Sprintf(
			"台账 %s %s 是聚合逻辑口，members 里的成员口在台账里一条都没有，回不到物理位置",
			rec.Device, rec.Port))
	case sameSpot(spots):
		r.Rack, r.U = spots[0].Rack, spots[0].U
		r.Evidence = append(r.Evidence, fmt.Sprintf(
			"台账 %s %s 是聚合逻辑口，FDB 只报逻辑口、逻辑口没有机柜 U 位；按 members 回到成员口 %s，位置一致取 %s",
			rec.Device, rec.Port, spotList(spots), rackText(r.Rack, r.U)))
	default:
		r.Evidence = append(r.Evidence, fmt.Sprintf(
			"台账 %s %s 的成员口机柜 U 位不一致：%s——回到成员口仍定不到唯一位置，要现场核",
			rec.Device, rec.Port, spotList(spots)))
	}
}

// sameSpot 判断成员口报的位置是否同一个。两个成员口接同一台服务器的双网卡时，
// 答案确定；不一致说明台账错挂或机器插错了机柜。
func sameSpot(spots []RackSpot) bool {
	if len(spots) == 0 || spots[0].Rack == "" {
		return false
	}
	for _, s := range spots[1:] {
		if s.Rack != spots[0].Rack || s.U != spots[0].U {
			return false
		}
	}
	return true
}

func spotList(spots []RackSpot) string {
	parts := make([]string, 0, len(spots))
	for _, s := range spots {
		parts = append(parts, fmt.Sprintf("%s → %s", s.Port, rackText(s.Rack, s.U)))
	}
	return strings.Join(parts, " · ")
}

// fillNextStep 只给「已定位」的结论配动作。没定位的三种各自在 finishWithoutFdb 里写好，
// 因为它们的动作不一样，不是「上站」两个字能盖住的。
func fillNextStep(r *Result) {
	var parts []string
	if r.Rack != "" {
		parts = append(parts, "按 "+rackText(r.Rack, r.U)+" 上站")
	} else {
		parts = append(parts, "位置没落下来：先补台账这条端口记录，再派人")
	}
	for _, t := range r.Traps {
		switch t {
		case TrapMacDrift:
			old := make([]string, 0, len(r.Stale))
			for _, s := range r.Stale {
				old = append(old, s.Device+" "+s.Port)
			}
			parts = append(parts, "旧口 "+strings.Join(old, "、")+" 是迁移余温，别按它派人")
		case TrapLagLogical:
			parts = append(parts, "聚合口按成员口报修，别让现场在逻辑口上找")
		}
	}
	r.NextStep = strings.Join(parts, "；")
}
