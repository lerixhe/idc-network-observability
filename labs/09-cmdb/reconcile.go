package main

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// 差异类型。前三类是这一课要输出的三类：僵尸、幽灵、错挂。
// 后两类是身份问题——它们必须在三类差异之前处理掉，否则旧名会被报成僵尸，
// 换板后的端口会被报成一整片错挂。
const (
	DiffZombie   = "zombie"
	DiffGhost    = "ghost"
	DiffMismatch = "mismatch"
	DiffRename   = "rename"
	DiffReindex  = "port_reindex"
)

var kindZH = map[string]string{
	DiffZombie:   "僵尸设备",
	DiffGhost:    "幽灵设备",
	DiffMismatch: "错挂链路",
	DiffRename:   "重复登记/改名",
	DiffReindex:  "端口重排",
}

// 置信度三档。只有三档，不给打分：规则能讲清楚，打分讲不清楚。
const (
	ConfHigh   = "high"
	ConfMedium = "medium"
	ConfLow    = "low"
)

// 纠正动作。机器读 action，人读 action_zh，写库走 patch 的 op。
const (
	ActRetire            = "retire"
	ActRetireAfterUnlink = "retire_after_unlink"
	ActRegister          = "register"
	ActRegisterVerify    = "register_verify"
	ActFixPeer           = "fix_peer"
	ActFixLocalPort      = "fix_local_port"
	ActAlias             = "alias"
	ActManualMerge       = "manual_merge"
	ActUpdateIfIndex     = "update_if_index"
)

var actionZH = map[string]string{
	ActRetire:            "标记 lifecycle=retired，记录保留",
	ActRetireAfterUnlink: "先删掉指向它的端口声明，再标记 retired",
	ActRegister:          "在台账登记这台设备，补齐机架与责任人",
	ActRegisterVerify:    "先补规格信息（序列号/型号/机架）再登记",
	ActFixPeer:           "改写这条声明里的对端设备与端口",
	ActFixLocalPort:      "这条声明的本端端口名登记错了，按现网改写",
	ActAlias:             "旧名标记 retired 并写 alias_of，不删记录",
	ActManualMerge:       "现网证据不足以判断主次，人工确认这几条是不是同一台",
	ActUpdateIfIndex:     "把台账 if_index 更新为现网值（主键仍是 device#port）",
}

// 补丁操作类型。每条补丁改一个对象的一次写入，Set 里放字段与新值。
const (
	OpDeviceLifecycle = "device_lifecycle"
	OpDeviceAlias     = "device_alias"
	OpDeviceCreate    = "device_create"
	OpLinkPeer        = "link_peer"
	OpLinkLocalPort   = "link_local_port"
	OpLinkIfIndex     = "link_if_index"
	OpLinkCreate      = "link_create"
	OpLinkDelete      = "link_delete"
)

// patchFields 是每种操作允许写的字段白名单。
// 补丁里出现白名单以外的字段一律报错——静默忽略等于让人以为改过了。
var patchFields = map[string][]string{
	OpDeviceLifecycle: {"lifecycle"},
	OpDeviceAlias:     {"alias_of"},
	OpDeviceCreate:    {"hostname", "mgmt_ip", "lifecycle", "site", "owner"},
	OpLinkPeer:        {"peer_device", "peer_port"},
	OpLinkLocalPort:   {"port"},
	OpLinkIfIndex:     {"if_index"},
	OpLinkCreate:      {"peer_device", "peer_port", "if_index"},
	OpLinkDelete:      {},
}

// Diff 是一条差异。
type Diff struct {
	ID         string   `json:"id"`
	Kind       string   `json:"kind"`
	KindZH     string   `json:"kind_zh"`
	Target     string   `json:"target"`
	Confidence string   `json:"confidence"`
	Evidence   []string `json:"evidence"`
	Action     string   `json:"action"`
	ActionZH   string   `json:"action_zh"`
	// Fix 是机器读的修正建议：字段 → 目标值。人看的解释在 Evidence 里。
	Fix map[string]string `json:"fix,omitempty"`
}

// Patch 是一条「建议写入」。它描述目标状态，不描述改前状态：
// 改前状态在差异单的 evidence 里，落库时以当时的台账为准。
type Patch struct {
	ID         string            `json:"id"`
	DiffID     string            `json:"diff_id"`
	Op         string            `json:"op"`
	Target     string            `json:"target"`
	Set        map[string]string `json:"set,omitempty"`
	Reason     string            `json:"reason"`
	Confidence string            `json:"confidence"`
}

// Stats 是一次对账的输入规模与跳过计数。
type Stats struct {
	Devices    int
	Links      int
	LldpPorts  int
	ArpEntries int
	// MergedOld 是被合并掉的旧名条数。
	MergedOld int
	// Dangling 是声明指向台账里不存在的设备的条数。
	Dangling int
	// LldpBlind 是 LLDP 看不见但管理面还活着的设备数——这类不判僵尸。
	LldpBlind int
}

// Report 是一次对账的完整结果。
type Report struct {
	Zombies    []Diff
	Ghosts     []Diff
	Mismatches []Diff
	Renames    []Diff
	Reindexes  []Diff
	Patches    []Patch
	// Primary 是旧名 → 主名。只含被合并掉的旧名。
	Primary map[string]string
	Stats   Stats
}

// All 按报告顺序返回全部差异，JSONL 用它。
func (r Report) All() []Diff {
	out := make([]Diff, 0, len(r.Zombies)+len(r.Ghosts)+len(r.Mismatches)+len(r.Renames)+len(r.Reindexes))
	out = append(out, r.Zombies...)
	out = append(out, r.Ghosts...)
	out = append(out, r.Mismatches...)
	out = append(out, r.Renames...)
	out = append(out, r.Reindexes...)
	return out
}

// lldpIndex 是 LLDP 快照的三种索引方式，一次建好反复用。
type lldpIndex struct {
	ports         map[devPort]LldpPort
	localDevices  map[string]bool
	remoteDevices map[string]bool
	mgmtIP        map[string]string
	portCount     map[string]int
}

func indexLLDP(s *LldpSnapshot) lldpIndex {
	x := lldpIndex{
		ports:         make(map[devPort]LldpPort, len(s.Ports)),
		localDevices:  map[string]bool{},
		remoteDevices: map[string]bool{},
		mgmtIP:        map[string]string{},
		portCount:     map[string]int{},
	}
	for _, p := range s.Ports {
		k := devPort{device: p.Device, port: p.Port}
		x.ports[k] = p
		x.localDevices[p.Device] = true
		x.portCount[p.Device]++
		x.rememberIP(p.Device, p.MgmtIP)
		for _, n := range p.Neighbors {
			x.remoteDevices[n.Device] = true
			x.rememberIP(n.Device, n.MgmtIP)
		}
	}
	return x
}

func (x lldpIndex) rememberIP(device, ip string) {
	if device == "" || ip == "" {
		return
	}
	if _, ok := x.mgmtIP[device]; !ok {
		x.mgmtIP[device] = ip
	}
}

// seenInLLDP 判断一台设备在现网有没有痕迹：自己上报过，或被别人报成邻居。
func (x lldpIndex) seenInLLDP(name string) bool {
	return x.localDevices[name] || x.remoteDevices[name]
}

// neighborOf 取某个端口 LLDP 观测到的对端。
//
// 一个口出现多条邻居只取第一条：正常交换口只该看到一个邻居，多于一个说明
// 对端是未受管的交换机或 hub，这时候 LLDP 说什么都不太可信。README 的坑列表
// 里记着这一条。
func (x lldpIndex) neighborOf(k devPort) (devPort, bool) {
	p, ok := x.ports[k]
	if !ok || len(p.Neighbors) == 0 {
		return devPort{}, false
	}
	return devPort{device: p.Neighbors[0].Device, port: p.Neighbors[0].Port}, true
}

// sees 判断 from 端口有没有观测到 target 端口。用来做双向确认。
func (x lldpIndex) sees(from, target devPort) bool {
	got, ok := x.neighborOf(from)
	return ok && got == target
}

// witnesses 列出是哪些端口把 name 报成了邻居。
func (x lldpIndex) witnesses(name string) []string {
	var out []string
	for _, k := range sortedPortKeys(x.ports) {
		p := x.ports[k]
		for _, n := range p.Neighbors {
			if n.Device == name {
				out = append(out, k.String())
				break
			}
		}
	}
	sort.Strings(out)
	return out
}

// linksOf 列出与 name 有关的连线，写成 "本端 <-> 对端"。
func (x lldpIndex) linksOf(name string) []string {
	seen := map[string]bool{}
	var out []string
	for _, k := range sortedPortKeys(x.ports) {
		for _, n := range x.ports[k].Neighbors {
			if k.device != name && n.Device != name {
				continue
			}
			a, b := k.String(), devPort{device: n.Device, port: n.Port}.String()
			if a > b {
				a, b = b, a
			}
			key := a + " <-> " + b
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, key)
		}
	}
	sort.Strings(out)
	return out
}

func sortedPortKeys(m map[devPort]LldpPort) []devPort {
	keys := make([]devPort, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].String() < keys[j].String() })
	return keys
}

func indexARP(s *ArpSnapshot) map[string]ArpEntry {
	m := make(map[string]ArpEntry, len(s.Entries))
	for _, e := range s.Entries {
		m[e.IP] = e
	}
	return m
}

// Reconcile 是对账主入口。顺序固定，不能换：
//
//  1. 先归并身份（serial 相同即同一台）。旧名不先摘出去，它会被报成僵尸，
//     而「僵尸」这个结论一旦落到工单上，值班的人会去现场找一台早就改过名的机器。
//  2. 再判僵尸与幽灵——都是「有没有」的问题，判据只有一张表就够。
//  3. 最后对链路，因为错挂要靠前两步的结果解释：对端是幽灵设备，这条线的
//     差异就该记在幽灵名下，不重复报。
func Reconcile(ledger *Ledger, lldp *LldpSnapshot, arp *ArpSnapshot) (Report, error) {
	idx := indexLLDP(lldp)
	arpByIP := indexARP(arp)

	r := Report{
		Primary: map[string]string{},
		Stats: Stats{
			Devices:    len(ledger.Devices),
			Links:      len(ledger.Links),
			LldpPorts:  len(lldp.Ports),
			ArpEntries: len(arp.Entries),
		},
	}

	var ambiguous map[string]bool
	r.Renames, r.Primary, ambiguous = reconcileIdentity(ledger, idx)
	r.Stats.MergedOld = len(r.Primary)

	decls, dangling := declarations(ledger, r.Primary)
	r.Stats.Dangling = dangling

	var blind int
	r.Zombies, blind = findZombies(ledger, r.Primary, ambiguous, idx, arpByIP, decls)
	r.Stats.LldpBlind = blind

	r.Ghosts = findGhosts(ledger, idx, arpByIP)
	r.Mismatches = findMismatches(decls, idx)
	r.Reindexes = findReindexes(decls, idx)

	r.Patches = buildPatches(ledger, r, idx)
	return r, nil
}

// resolveName 把旧名换成主名。旧名自己做过的端口声明，归并后算在主名头上。
func resolveName(primary map[string]string, name string) string {
	if m, ok := primary[name]; ok {
		return m
	}
	return name
}

// reconcileIdentity 按 serial 分组，每组只留一个主名。
//
// 判据是 serial 而不是 hostname：改过名的设备在现网只会用新名上报，
// 旧名在 LLDP 里彻底消失。用 hostname 做合并键，这一条就变成了
// 「一台僵尸 + 一台幽灵」，而真相是同一台机器改了个名字。
func reconcileIdentity(ledger *Ledger, idx lldpIndex) (diffs []Diff, primary map[string]string, ambiguous map[string]bool) {
	primary = map[string]string{}
	ambiguous = map[string]bool{}

	groups := map[string][]Device{}
	var serials []string
	for _, d := range ledger.Devices {
		if d.AliasOf != "" && d.AliasOf != d.Hostname {
			// 这条补丁已经应用过了（台账里已经有 alias_of），不重复报。
			// 不带这一步，对账过一次的台账每跑一次都还会报一遍同一个旧名。
			primary[d.Hostname] = d.AliasOf
			continue
		}
		if strings.TrimSpace(d.Serial) == "" {
			// 没有 serial 的记录证明不了是同一台，一律不合并。
			continue
		}
		if _, ok := groups[d.Serial]; !ok {
			serials = append(serials, d.Serial)
		}
		groups[d.Serial] = append(groups[d.Serial], d)
	}
	sort.Strings(serials)

	for _, serial := range serials {
		g := groups[serial]
		if len(g) < 2 {
			continue
		}
		names := make([]string, 0, len(g))
		var seen []string
		for _, d := range g {
			names = append(names, d.Hostname)
			if idx.seenInLLDP(d.Hostname) {
				seen = append(seen, d.Hostname)
			}
		}
		sort.Strings(names)
		sort.Strings(seen)

		if len(seen) == 1 {
			cur := seen[0]
			for _, d := range g {
				if d.Hostname == cur {
					continue
				}
				primary[d.Hostname] = cur
				ev := []string{
					fmt.Sprintf("serial %s 与 %s 完全相同，是同一台物理设备的两条记录", serial, cur),
					fmt.Sprintf("LLDP 里只有 %s 上报端口，%s 在现网没有任何痕迹——设备已经按新名在跑", cur, d.Hostname),
				}
				if d.MgmtIP != "" && d.MgmtIP == curDevice(g, cur).MgmtIP {
					ev = append(ev, fmt.Sprintf("两条记录的 mgmt_ip 都是 %s，改名时没做软删，IP 也留在了旧记录上", d.MgmtIP))
				}
				if d.Rack != "" {
					ev = append(ev, fmt.Sprintf("旧记录还占着机架 %s 的位置，按机架统计 U 位会重复计一次", d.Rack))
				}
				diffs = append(diffs, Diff{
					Kind:       DiffRename,
					Target:     d.Hostname,
					Confidence: ConfHigh,
					Evidence:   ev,
					Action:     ActAlias,
					Fix: map[string]string{
						"alias_of":  cur,
						"lifecycle": LifecycleRetired,
					},
				})
			}
			continue
		}

		// 组内 0 个或 2 个以上名字在现网出现：分不出主次。
		// 整组跳过僵尸判定，避免同一台机器报两遍。
		for _, name := range names {
			ambiguous[name] = true
			if name != names[0] {
				primary[name] = names[0]
			}
		}
		reason := "组内没有任何名字出现在 LLDP 里，分不清是刚改名还是早就下架"
		if len(seen) > 1 {
			reason = fmt.Sprintf("组内 %d 个名字同时出现在 LLDP 里（%s），像是两条记录抄了同一个 serial",
				len(seen), strings.Join(seen, "、"))
		}
		diffs = append(diffs, Diff{
			Kind:       DiffRename,
			Target:     names[0],
			Confidence: ConfLow,
			Evidence: []string{
				fmt.Sprintf("serial %s 被 %d 条记录同时占用：%s", serial, len(g), strings.Join(names, "、")),
				reason,
				"这组记录整体跳过僵尸判定，人工确认之后再合并或拆开",
			},
			Action: ActManualMerge,
		})
	}

	sortDiffs(diffs)
	numberDiffs("R", diffs)
	return diffs, primary, ambiguous
}

func curDevice(g []Device, name string) Device {
	for _, d := range g {
		if d.Hostname == name {
			return d
		}
	}
	return Device{}
}

// declarations 把台账的端口声明建成 devPort → 声明，名字已归并成主名。
// 指向台账里不存在的设备的声明直接丢掉并计数：这种声明没法对账，
// 但也不能悄悄当成「没有这条线」。
func declarations(ledger *Ledger, primary map[string]string) (map[devPort]PortDecl, int) {
	known := make(map[string]bool, len(ledger.Devices))
	for _, d := range ledger.Devices {
		known[d.Hostname] = true
	}
	out := make(map[devPort]PortDecl, len(ledger.Links))
	dangling := 0
	for _, p := range ledger.Links {
		p.Device = resolveName(primary, p.Device)
		p.PeerDevice = resolveName(primary, p.PeerDevice)
		if !known[p.Device] || !known[p.PeerDevice] {
			dangling++
			continue
		}
		k := devPort{device: p.Device, port: p.Port}
		out[k] = p
	}
	return out, dangling
}

// findZombies 找「台账在役、现网没有」的设备。
//
// 判据只有一条：LLDP 里既没有它自己上报的端口，也没有任何设备把它报成邻居。
// 置信度再看管理面：ARP 里 IP 与 MAC 都对得上，说明设备还活着，只是 LLDP
// 没覆盖到（没开 LLDP、或者管理网与业务网走了不同的采集器）——这种不判僵尸。
func findZombies(ledger *Ledger, primary map[string]string, ambiguous map[string]bool,
	idx lldpIndex, arpByIP map[string]ArpEntry, decls map[devPort]PortDecl) ([]Diff, int) {

	referenced := map[string][]string{}
	for _, d := range decls {
		referenced[d.PeerDevice] = append(referenced[d.PeerDevice], devPort{d.Device, d.Port}.String())
	}
	for k := range referenced {
		sort.Strings(referenced[k])
	}

	mains := map[string]bool{}
	for _, d := range ledger.Devices {
		mains[resolveName(primary, d.Hostname)] = true
	}
	names := make([]string, 0, len(mains))
	for n := range mains {
		names = append(names, n)
	}
	sort.Strings(names)

	var diffs []Diff
	blind := 0
	for _, name := range names {
		if ambiguous[name] {
			continue
		}
		d, ok := ledgerDevice(ledger, name)
		if !ok {
			continue
		}
		if d.Lifecycle != LifecycleActive {
			// staging 是还没上线的机器，retired 是已经下架的，都不该算僵尸。
			continue
		}
		if idx.seenInLLDP(name) {
			continue
		}
		if e, ok := arpByIP[d.MgmtIP]; ok && d.MgmtIP != "" && strings.EqualFold(e.MAC, d.MgmtMAC) {
			blind++
			continue
		}

		ev := []string{fmt.Sprintf("LLDP 快照里没有 %s 的本端端口，也没有任何设备把它报成邻居", name)}
		switch {
		case d.MgmtIP == "":
			ev = append(ev, "台账没登记管理地址，ARP 无从核对")
		default:
			if e, ok := arpByIP[d.MgmtIP]; ok {
				ev = append(ev, fmt.Sprintf("ARP 里 %s 的 MAC 是 %s，台账登记的是 %s——这个地址已经被别的设备占用",
					d.MgmtIP, e.MAC, d.MgmtMAC))
			} else {
				ev = append(ev, fmt.Sprintf("ARP 表里没有 %s，这个管理地址上没有人应答", d.MgmtIP))
			}
		}

		action, conf := ActRetire, ConfHigh
		if refs := referenced[name]; len(refs) > 0 {
			ev = append(ev, fmt.Sprintf("台账里还有 %d 条端口声明指向它（%s），retire 之前先清掉，否则拓扑里留一个悬空对端",
				len(refs), strings.Join(refs, "、")))
			action, conf = ActRetireAfterUnlink, ConfMedium
		}
		diffs = append(diffs, Diff{
			Kind:       DiffZombie,
			Target:     name,
			Confidence: conf,
			Evidence:   ev,
			Action:     action,
			Fix:        map[string]string{"lifecycle": LifecycleRetired},
		})
	}

	sortDiffs(diffs)
	numberDiffs("Z", diffs)
	return diffs, blind
}

func ledgerDevice(l *Ledger, hostname string) (Device, bool) {
	for _, d := range l.Devices {
		if d.Hostname == hostname {
			return d, true
		}
	}
	return Device{}, false
}

// findGhosts 找「现网有、台账没有」的设备。
//
// 三个独立来源各算一分：自己上报了端口、被别人报成邻居、ARP 里有它的管理地址。
// 两分以上算 high，一分算 medium——一分的那种只出现在 LLDP 里，连型号都没有，
// 直接登记进台账大概率是三条空字段加一个错机架。
func findGhosts(ledger *Ledger, idx lldpIndex, arpByIP map[string]ArpEntry) []Diff {
	known := make(map[string]bool, len(ledger.Devices))
	for _, d := range ledger.Devices {
		known[d.Hostname] = true
	}

	candidates := map[string]bool{}
	for n := range idx.localDevices {
		candidates[n] = true
	}
	for n := range idx.remoteDevices {
		candidates[n] = true
	}

	var names []string
	for n := range candidates {
		if !known[n] {
			names = append(names, n)
		}
	}
	sort.Strings(names)

	var diffs []Diff
	for _, name := range names {
		sources := 0
		var ev []string
		if idx.localDevices[name] {
			sources++
			ev = append(ev, fmt.Sprintf("%s 在 LLDP 里自己上报了 %d 个端口", name, idx.portCount[name]))
		}
		if w := idx.witnesses(name); len(w) > 0 {
			sources++
			ev = append(ev, fmt.Sprintf("被 %s 报成 LLDP 邻居", strings.Join(w, "、")))
		}
		if ip := idx.mgmtIP[name]; ip != "" {
			if e, ok := arpByIP[ip]; ok {
				sources++
				ev = append(ev, fmt.Sprintf("ARP 里 %s 的 MAC 是 %s，管理面活着", ip, e.MAC))
			} else {
				ev = append(ev, fmt.Sprintf("ARP 里没有 %s", ip))
			}
		} else {
			ev = append(ev, "LLDP 没带管理地址（management address TLV 没开），ARP 无从核对")
		}
		ev = append(ev, "台账里没有这台设备的任何记录")
		if links := idx.linksOf(name); len(links) > 0 {
			ev = append(ev, "现网观测到的连线："+strings.Join(links, "、"))
		}

		conf, action := ConfMedium, ActRegisterVerify
		fix := map[string]string{"hostname": name, "lifecycle": LifecycleStaging}
		if sources >= 2 {
			conf, action = ConfHigh, ActRegister
		}
		if ip := idx.mgmtIP[name]; ip != "" {
			fix["mgmt_ip"] = ip
		}
		diffs = append(diffs, Diff{
			Kind:       DiffGhost,
			Target:     name,
			Confidence: conf,
			Evidence:   ev,
			Action:     action,
			Fix:        fix,
		})
	}

	sortDiffs(diffs)
	numberDiffs("G", diffs)
	return diffs
}

// findMismatches 对比台账声明与 LLDP 观测。
//
// 分支顺序就是判据的优先级：
//
//  1. 本端口有 LLDP 邻居，且不是台账写的那个 → 错挂，证据最硬。
//  2. 台账两端自洽，但对面端口看到的不是本端口 → 本端端口名登记错了
//     （换板、搬迁、端口改名都会这样）。
//  3. 台账两端互相矛盾，现网两个端口都没观测 → 只有台账内部的矛盾，
//     置信度 low，只能人工核。
//
// 「现网没有任何观测」不算证据。一台设备下电之后它的下联口在 LLDP 里
// 就是彻底消失，把这当成错挂，僵尸设备会连带刷出十几条假差异。
func findMismatches(decls map[devPort]PortDecl, idx lldpIndex) []Diff {
	keys := make([]devPort, 0, len(decls))
	for k := range decls {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].String() < keys[j].String() })

	var diffs []Diff
	for _, k := range keys {
		d := decls[k]
		want := devPort{device: d.PeerDevice, port: d.PeerPort}

		fwd, fwdOK := idx.neighborOf(k)
		if fwdOK && fwd == want {
			continue
		}

		rev, revOK := decls[want]
		revObs, revObsOK := idx.neighborOf(want)
		back, backOK := decls[fwd]

		// ledgerAgrees：台账两端写得一致（不管写得对不对）。
		ledgerAgrees := revOK && rev.PeerDevice == k.device && rev.PeerPort == k.port

		switch {
		case fwdOK:
			conf := ConfMedium
			ev := []string{
				fmt.Sprintf("台账登记 %s -> %s（%s 登记）", k, want, d.DeclaredAt),
				fmt.Sprintf("LLDP 观测 %s 连的是 %s", k, fwd),
			}
			if idx.sees(fwd, k) {
				conf = ConfHigh
				ev = append(ev, fmt.Sprintf("对端 %s 也观测到 %s，双向确认", fwd, k))
			}
			if backOK && back.PeerDevice == k.device && back.PeerPort == k.port {
				conf = ConfHigh
				ev = append(ev, fmt.Sprintf("台账里 %s 登记的对端也是 %s，只有本端这一条写错", fwd, k))
			} else if !backOK {
				ev = append(ev, fmt.Sprintf("台账里没有 %s 这条声明，这根线只有一端登记过", fwd))
			}
			diffs = append(diffs, Diff{
				Kind:       DiffMismatch,
				Target:     k.String(),
				Confidence: conf,
				Evidence:   ev,
				Action:     ActFixPeer,
				Fix:        map[string]string{"peer_device": fwd.device, "peer_port": fwd.port},
			})

		case ledgerAgrees && revObsOK && revObs != k:
			// 台账两端一致，但对面端口在现网看到的不是本端口。
			diffs = append(diffs, Diff{
				Kind:       DiffMismatch,
				Target:     k.String(),
				Confidence: ConfHigh,
				Evidence: []string{
					fmt.Sprintf("台账登记 %s <-> %s，两端写法一致", k, want),
					fmt.Sprintf("LLDP 里 %s 根本没有端口记录，而 %s 观测到的是 %s", k, want, revObs),
					"台账的端口名停在搬迁之前，现网这台设备的端口是 " + revObs.port,
				},
				Action: ActFixLocalPort,
				Fix:    map[string]string{"port": revObs.port},
			})

		case revOK && !ledgerAgrees:
			// 台账两端互相矛盾：A 说连 B 的 3 口，B 的登记在 5 口。
			diffs = append(diffs, Diff{
				Kind:       DiffMismatch,
				Target:     k.String(),
				Confidence: ConfLow,
				Evidence: []string{
					fmt.Sprintf("台账登记 %s -> %s", k, want),
					fmt.Sprintf("但 %s 登记的对端是 %s，两端对不上", want, devPort{device: rev.PeerDevice, port: rev.PeerPort}),
					fmt.Sprintf("LLDP 里 %s 与 %s 都没有端口记录，现网给不出裁决，只能人工核", k, want),
				},
				Action: ActFixPeer,
			})
		}
	}

	sortDiffs(diffs)
	numberDiffs("M", diffs)
	return diffs
}

// findReindexes 找 ifIndex 重排：端口名没变，索引变了。
//
// 换一块业务板就会发生。台账里的 if_index 只是登记值，主键从来不是它——
// 按 ifIndex 建主键，换板之后新端口的历史会接在旧端口上，曲线看起来完全正常。
func findReindexes(decls map[devPort]PortDecl, idx lldpIndex) []Diff {
	keys := make([]devPort, 0, len(decls))
	for k := range decls {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].String() < keys[j].String() })

	changed := map[string]int{}
	var diffs []Diff
	for _, k := range keys {
		d := decls[k]
		p, ok := idx.ports[k]
		if !ok || d.IfIndex == 0 || p.IfIndex == d.IfIndex {
			continue
		}
		changed[k.device]++
		diffs = append(diffs, Diff{
			Kind:       DiffReindex,
			Target:     k.String(),
			Confidence: ConfHigh,
			Evidence: []string{
				fmt.Sprintf("台账登记 if_index=%d，LLDP 报的是 %d（端口名没变）", d.IfIndex, p.IfIndex),
				"主键用 device#port，ifIndex 只在取数时当定位线索",
			},
			Action: ActUpdateIfIndex,
			Fix:    map[string]string{"if_index": strconv.Itoa(p.IfIndex)},
		})
	}

	for i := range diffs {
		k, err := splitTarget(diffs[i].Target)
		if err != nil {
			continue
		}
		if n := changed[k.device]; n > 1 {
			diffs[i].Evidence = append(diffs[i].Evidence,
				fmt.Sprintf("%s 上有 %d 个端口同时换了 ifIndex，整板重排的特征", k.device, n))
		}
	}

	sortDiffs(diffs)
	numberDiffs("RI", diffs)
	return diffs
}

// buildPatches 把差异翻成「建议写入」。一条差异可以生出多条补丁：
// 一台僵尸设备要改 lifecycle，还得清掉指向它的端口声明。
func buildPatches(ledger *Ledger, r Report, idx lldpIndex) []Patch {
	var out []Patch
	add := func(d Diff, op, target string, set map[string]string) {
		out = append(out, Patch{
			ID:         fmt.Sprintf("P%02d", len(out)+1),
			DiffID:     d.ID,
			Op:         op,
			Target:     target,
			Set:        set,
			Reason:     d.ActionZH,
			Confidence: d.Confidence,
		})
	}

	for _, d := range r.Zombies {
		add(d, OpDeviceLifecycle, d.Target, map[string]string{"lifecycle": LifecycleRetired})
		if d.Action != ActRetireAfterUnlink {
			continue
		}
		// 清引用：指向它的，和它自己登记出去的，两头都要删。
		for _, p := range ledger.Links {
			k := devPort{device: resolveName(r.Primary, p.Device), port: p.Port}
			peer := resolveName(r.Primary, p.PeerDevice)
			if k.device != d.Target && peer != d.Target {
				continue
			}
			add(d, OpLinkDelete, k.String(), nil)
		}
	}

	for _, d := range r.Ghosts {
		add(d, OpDeviceCreate, d.Target, d.Fix)
		if d.Confidence != ConfHigh {
			// 低置信度的幽灵连型号都没有，链路建议等设备登记之后再说。
			continue
		}
		// 台账的链路是各端自报，一根线两条声明，两端都要补。
		for _, link := range idx.linksOf(d.Target) {
			ends := strings.SplitN(link, " <-> ", 2)
			if len(ends) != 2 {
				continue
			}
			for i := range ends {
				k, err := splitTarget(ends[i])
				if err != nil {
					continue
				}
				other, err := splitTarget(ends[1-i])
				if err != nil {
					continue
				}
				// 对端可以是这条补丁要新建的设备，所以 d.Target 也算已登记。
				if !knownOrNew(ledger, d.Target, k.device) || !knownOrNew(ledger, d.Target, other.device) {
					continue
				}
				port, ok := idx.ports[k]
				if !ok {
					continue
				}
				add(d, OpLinkCreate, k.String(), map[string]string{
					"peer_device": other.device,
					"peer_port":   other.port,
					"if_index":    strconv.Itoa(port.IfIndex),
				})
			}
		}
	}

	for _, d := range r.Mismatches {
		op := OpLinkPeer
		if d.Action == ActFixLocalPort {
			op = OpLinkLocalPort
		}
		add(d, op, d.Target, d.Fix)
		if d.Action != ActFixLocalPort {
			continue
		}
		// 本端端口名改了，if_index 也得跟着改：新端口名在 LLDP 里有自己的索引。
		k, err := splitTarget(d.Target)
		if err != nil {
			continue
		}
		port, ok := idx.ports[devPort{device: k.device, port: d.Fix["port"]}]
		if !ok {
			continue
		}
		add(d, OpLinkIfIndex, devPort{device: k.device, port: d.Fix["port"]}.String(),
			map[string]string{"if_index": strconv.Itoa(port.IfIndex)})
	}

	for _, d := range r.Renames {
		if d.Action != ActAlias {
			continue
		}
		add(d, OpDeviceAlias, d.Target, map[string]string{"alias_of": d.Fix["alias_of"]})
		add(d, OpDeviceLifecycle, d.Target, map[string]string{"lifecycle": LifecycleRetired})
	}

	for _, d := range r.Reindexes {
		add(d, OpLinkIfIndex, d.Target, d.Fix)
	}

	return out
}

// knownOrNew 判断一台设备在补丁应用之后会不会存在：要么台账里已经有，
// 要么就是这一轮要新建的幽灵设备。
func knownOrNew(ledger *Ledger, creating, name string) bool {
	if name == creating {
		return true
	}
	_, ok := ledgerDevice(ledger, name)
	return ok
}

func sortDiffs(diffs []Diff) {
	sort.SliceStable(diffs, func(i, j int) bool {
		if diffs[i].Target != diffs[j].Target {
			return diffs[i].Target < diffs[j].Target
		}
		return diffs[i].Kind < diffs[j].Kind
	})
}

func numberDiffs(prefix string, diffs []Diff) {
	for i := range diffs {
		diffs[i].ID = prefix + strconv.Itoa(i+1)
		diffs[i].KindZH = kindZH[diffs[i].Kind]
		if diffs[i].ActionZH == "" {
			diffs[i].ActionZH = actionZH[diffs[i].Action]
		}
	}
}
