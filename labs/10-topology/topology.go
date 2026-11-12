package main

import (
	"fmt"
	"sort"
	"strings"
)

// linkEnd 是链路的一端，两个字段都是归一化后的规范名。
//
// 端口为空表示「这一端确定、对端端口未知」——MAC 反推只能得到这种结果。
type linkEnd struct {
	Device string `json:"device"`
	Port   string `json:"port"`
}

func (e linkEnd) String() string {
	if e.Port == "" {
		return e.Device + ":?"
	}
	return e.Device + ":" + e.Port
}

func equalEnd(a, b linkEnd) bool {
	return a.Device == b.Device && a.Port == b.Port
}

// lessEnd 给链路两端一个稳定顺序，同一个端对无论从哪边报出来都落到同一个 key。
func lessEnd(a, b linkEnd) bool {
	if a.Device != b.Device {
		return a.Device < b.Device
	}
	return a.Port < b.Port
}

// linkKey 是无序端对。链路的身份是「两端连在一起」，与方向无关。
type linkKey struct {
	a, b linkEnd
}

func makeLinkKey(x, y linkEnd) linkKey {
	if lessEnd(y, x) {
		x, y = y, x
	}
	return linkKey{a: x, b: y}
}

// MemberPair 是聚合链路下面的一对物理成员口。逻辑口对齐了，成员口还得留底。
type MemberPair struct {
	A linkEnd `json:"a"`
	B linkEnd `json:"b"`
}

// Link 是融合后的一条链路。
type Link struct {
	ID         string   `json:"id"`
	A          linkEnd  `json:"a"`
	B          linkEnd  `json:"b"`
	Kind       string   `json:"kind"`
	Confidence string   `json:"confidence"`
	Sources    []string `json:"sources"`
	// VerifiedBy 是校验通过的源。它不改结论，只加可信度。
	VerifiedBy []string `json:"verified_by"`
	// Bidirectional 表示两端都报了对方。只有单侧断言的链路仍然成立，但少一半证据。
	Bidirectional bool `json:"bidirectional"`
	// DeclaredFrom 是真正报了这条链路的那几端。单侧链路靠它回答「是谁看见的」，
	// 这一点很关键：看见的和没看见的，该去查的设备不是同一台。
	DeclaredFrom []string `json:"declared_from"`
	// Redundant 表示 STP 在这一端是 blocking/alternate。这是冗余链路，不是错。
	Redundant bool `json:"redundant"`
	// PeerUnknown 表示对端设备不在台账里。
	PeerUnknown bool `json:"peer_unknown"`
	// Question 表示这条链路只有低置信度来源支撑，输出时打问号。
	Question bool         `json:"question"`
	Members  []MemberPair `json:"members,omitempty"`
	Evidence string       `json:"evidence"`
}

// Topology 是一次融合的结果。
type Topology struct {
	Links   []Link
	Notices []Notice

	// 下面四个数给读数的人对账用：喂进去多少条断言，出多少条链路。
	LLDPRows   int
	LLDPUsable int
	MACRows    int
	MACUsed    int
}

// lldpObs 是一条归一化后的 LLDP 断言。
type lldpObs struct {
	from, to        linkEnd // 折叠到逻辑口之后
	rawFrom         linkEnd // 折叠前的真实端口
	rawTo           linkEnd
	remoteUnmanaged bool
}

// Fuse 是融合入口。四份快照进来，链路与提示清单出去。
//
// 裁决规则只有一条：按置信度取高，绝不投票。
//
//   - LLDP 报了什么就是什么，置信度高，直接出链路；
//   - MAC 反推只补 LLDP 没覆盖的端口，置信度低，输出打问号；
//   - STP 与路由邻居不产生链路，只校验已有链路；
//   - 低置信度与高置信度冲突时，高置信度赢，低置信度的证据进提示清单。
//
// 投票在拓扑融合里是错的。三个源里两个说 A、一个说 B，票多的未必对：
// MAC 表会漂移，STP 只看得见树看不见物理，而 LLDP 是唯一一个
// 「对端亲口说出自己是谁」的源。按票数算，等于把最准的那个源稀释掉。
func Fuse(dir *Directory, lldp []LLDPEntry, stp []STPEntry, routes []RouteEntry, macs []MACEntry) Topology {
	t := Topology{LLDPRows: len(lldp), MACRows: len(macs)}
	notices := &noticeList{}

	// 一、LLDP 归一化。归一化不了的断言直接丢掉，并记一条提示——
	// 硬猜一个端口名出来，后面每条结论都建在猜的那个名字上。
	obs := make([]lldpObs, 0, len(lldp))
	// declared 是「本端端口声明了哪些对端」，双向校验与错挂检测都靠它。
	declared := make(map[linkEnd]map[linkEnd]bool, len(lldp))
	for _, e := range lldp {
		local, ok := dir.Device(e.LocalDevice)
		if !ok {
			notices.add(NoticeDeviceUnknown, SevWarn, e.LocalDevice,
				fmt.Sprintf("LLDP 本端设备 %q 不在台账里，整条断言丢弃", e.LocalDevice))
			continue
		}
		localPort, ok := dir.Port(local, e.LocalPort)
		if !ok {
			notices.add(NoticePortUnknown, SevWarn, local+":"+e.LocalPort,
				fmt.Sprintf("%s 的端口 %q 不在台账端口表里，整条断言丢弃（别名表缺这一条）", local, e.LocalPort))
			continue
		}

		remote, managed := dir.Device(e.RemoteDevice)
		if !managed {
			// 幽灵设备：台账里没有，但 LLDP 亲口报了它的名字。照抄，不丢。
			remote = strings.TrimSpace(e.RemoteDevice)
		}
		remotePort, ok := resolveRemotePort(dir, remote, managed, e)
		if !ok {
			notices.add(NoticePortUnknown, SevWarn, remote,
				fmt.Sprintf("%s 报的对端端口 id=%q desc=%q 在台账里翻不出来，整条断言丢弃",
					local+":"+e.LocalPort, e.RemotePortID, e.RemotePortDesc))
			continue
		}

		from := dir.fold(local, localPort)
		to := dir.fold(remote, remotePort)
		obs = append(obs, lldpObs{
			from: from, to: to,
			rawFrom:         linkEnd{Device: local, Port: localPort},
			rawTo:           linkEnd{Device: remote, Port: remotePort},
			remoteUnmanaged: !managed,
		})
		if declared[from] == nil {
			declared[from] = map[linkEnd]bool{}
		}
		declared[from][to] = true

		if !managed {
			notices.add(NoticeDeviceUnknown, SevWarn, to.String(),
				fmt.Sprintf("%s 报的对端 %s 不在台账里（幽灵设备），链路保留、设备待建档", from, remote))
		}
	}
	t.LLDPUsable = len(obs)

	// 二、错挂检测：对端自己也报了邻居，但报的不是我。
	// 这是第 28 课三类差异里的「错挂」，必须显式列出来，不能靠人扫表发现。
	reported := map[string]bool{}
	for _, o := range obs {
		theirs, ok := declared[o.to]
		if !ok || theirs[o.from] {
			continue
		}
		// 对端报的邻居里没有我，把它的声明列出来，人才知道现场该核哪两个口。
		var others []string
		for end := range theirs {
			others = append(others, end.String())
		}
		sort.Strings(others)
		subject := makeLinkKey(o.from, o.to)
		key := subject.a.String() + " - " + subject.b.String()
		if reported[key] {
			continue
		}
		reported[key] = true
		notices.add(NoticePeerMismatch, SevWarn, key,
			fmt.Sprintf("%s 说连 %s，但 %s 说自己连 %s（两端说法对不上，按 LLDP 保留，需现场核）",
				o.rawFrom, o.rawTo, o.to, strings.Join(others, "、")))
	}

	// 三、LLDP 链路。同一个 linkKey 下的多条断言合并——聚合口的成员口
	// 折叠之后会落到同一个 key 上，成员明细留底。
	type builder struct {
		key     linkKey
		obs     []lldpObs
		members []MemberPair
	}
	byKey := make(map[linkKey]*builder, len(obs))
	order := make([]linkKey, 0, len(obs))
	for _, o := range obs {
		k := makeLinkKey(o.from, o.to)
		b, ok := byKey[k]
		if !ok {
			b = &builder{key: k}
			byKey[k] = b
			order = append(order, k)
		}
		b.obs = append(b.obs, o)
		if !equalEnd(o.rawFrom, o.from) || !equalEnd(o.rawTo, o.to) {
			b.members = append(b.members, MemberPair{A: o.rawFrom, B: o.rawTo})
		}
	}

	links := make([]Link, 0, len(order))
	byEnd := make(map[linkEnd]*Link, len(order)*2)
	for _, k := range order {
		b := byKey[k]
		l := Link{
			ID:         k.a.String() + " ↔ " + k.b.String(),
			A:          k.a,
			B:          k.b,
			Kind:       linkKind(dir, k),
			Confidence: ConfHigh,
			Sources:    []string{SourceLLDP},
			Members:    b.members,
		}
		l.Bidirectional = declared[k.a][k.b] && declared[k.b][k.a]
		if declared[k.a][k.b] {
			l.DeclaredFrom = append(l.DeclaredFrom, k.a.String())
		}
		if declared[k.b][k.a] {
			l.DeclaredFrom = append(l.DeclaredFrom, k.b.String())
		}
		for _, o := range b.obs {
			l.PeerUnknown = l.PeerUnknown || o.remoteUnmanaged
		}
		l.Evidence = lldpEvidence(l)
		links = append(links, l)
	}
	// byEnd 要在 links 落定后建指针，扩容后指针会变。
	for i := range links {
		byEnd[links[i].A] = &links[i]
		byEnd[links[i].B] = &links[i]
	}

	for i := range links {
		l := &links[i]
		if l.Bidirectional || len(l.DeclaredFrom) == 0 {
			continue
		}
		seen := l.DeclaredFrom[0]
		silent := l.A.String()
		if silent == seen {
			silent = l.B.String()
		}
		notices.add(NoticeOneSided, SevInfo, l.ID,
			fmt.Sprintf("只有 %s 报了这条链路，%s 没报回来（对端 LLDP 未开、或邻居条目已超时）",
				seen, silent))
	}

	// 四、STP 校验。blocked 不代表报错，代表这条链路是冗余路径。
	verifyBySTP(dir, stp, byEnd, notices, &links)

	// 五、路由邻居校验。它证明三层通，不证明二层直连。
	verifyByRoute(dir, routes, byEnd, notices, &links)

	// 六、MAC 反推。低置信度，只补 LLDP 没覆盖的端口。
	t.MACUsed = fuseMAC(dir, macs, byEnd, notices, &links)

	sort.SliceStable(links, func(i, j int) bool {
		if !equalEnd(links[i].A, links[j].A) {
			return lessEnd(links[i].A, links[j].A)
		}
		return lessEnd(links[i].B, links[j].B)
	})
	for i := range links {
		sort.Strings(links[i].Sources)
		sort.Strings(links[i].VerifiedBy)
	}

	t.Links = links
	t.Notices = notices.sorted()
	return t
}

// resolveRemotePort 解析对端端口名。
//
// 优先 lldpRemPortDesc，再退回 lldpRemPortId。有些设备只报一个编号，
// 那就当 ifIndex 查台账。三个都翻不出来才算失败。
func resolveRemotePort(dir *Directory, device string, managed bool, e LLDPEntry) (string, bool) {
	if !managed {
		// 台账里没这台设备，端口名只能是 LLDP 报的原文，没有别名表可用。
		if s := strings.TrimSpace(e.RemotePortDesc); s != "" {
			return s, true
		}
		if s := strings.TrimSpace(e.RemotePortID); s != "" {
			return s, true
		}
		return "", false
	}
	if s := strings.TrimSpace(e.RemotePortDesc); s != "" {
		if name, ok := dir.Port(device, s); ok {
			return name, true
		}
	}
	if s := strings.TrimSpace(e.RemotePortID); s != "" {
		if name, ok := dir.Port(device, s); ok {
			return name, true
		}
	}
	return "", false
}

// linkKind 判这条链路是物理口还是逻辑口。
//
// 只折了一端也算逻辑链路：一端报 Port-Channel、另一端报 Eth-Trunk，
// 中间那条线在模型里必须是「逻辑链路 + 成员链路」，不能是两条物理链路。
func linkKind(dir *Directory, k linkKey) string {
	if dir.IsLag(k.a.Device, k.a.Port) || dir.IsLag(k.b.Device, k.b.Port) {
		return KindLag
	}
	return KindPhysical
}

func lldpEvidence(l Link) string {
	how := "LLDP 双向确认（两端都报对方）"
	if !l.Bidirectional {
		how = "LLDP 单侧断言（对端没报回来）"
	}
	if l.Kind == KindLag {
		return fmt.Sprintf("%s；聚合口已折叠到逻辑口，%d 对成员口留底", how, len(l.Members))
	}
	return how
}

// verifyBySTP 用生成树角色校验链路。
//
// 只做一件事：链路某一端在 STP 里是 blocking/alternate，说明它是冗余路径。
// 这不是错误——双上联的备用线本来就该被 STP 挡住。反过来，如果把它当异常
// 上报，每次上架一台双上联的机器都会触发一次告警，两周以后没人看这条规则了。
func verifyBySTP(dir *Directory, stp []STPEntry, byEnd map[linkEnd]*Link, notices *noticeList, links *[]Link) {
	if len(stp) == 0 {
		return
	}
	type role struct{ role, state string }
	portRole := make(map[linkEnd]role, len(stp))
	for _, e := range stp {
		dev, ok := dir.Device(e.Device)
		if !ok {
			notices.add(NoticeDeviceUnknown, SevWarn, e.Device,
				fmt.Sprintf("STP 快照里的设备 %q 不在台账里", e.Device))
			continue
		}
		port, ok := dir.Port(dev, e.Port)
		if !ok {
			notices.add(NoticePortUnknown, SevWarn, dev+":"+e.Port,
				fmt.Sprintf("STP 快照里的端口 %q 不在 %s 的端口表里", e.Port, dev))
			continue
		}
		portRole[dir.fold(dev, port)] = role{role: e.Role, state: e.State}
	}

	for i := range *links {
		l := &(*links)[i]
		for _, end := range []linkEnd{l.A, l.B} {
			r, ok := portRole[end]
			if !ok {
				continue
			}
			if !isBlocking(r.role, r.state) {
				continue
			}
			l.Redundant = true
			if !hasString(l.VerifiedBy, SourceSTP) {
				l.VerifiedBy = append(l.VerifiedBy, SourceSTP)
			}
			notices.add(NoticeSTPBlocked, SevInfo, l.ID,
				fmt.Sprintf("%s 在 STP 里是 %s/%s，这条链路是冗余路径（blocked 不等于错，别当故障报）",
					end, r.role, r.state))
		}
	}
}

func isBlocking(role, state string) bool {
	switch strings.ToLower(state) {
	case "blocking", "discarding":
		return true
	}
	switch strings.ToLower(role) {
	case "alternate", "backup":
		return true
	}
	return false
}

// verifyByRoute 用三层邻接校验链路。
//
// 路由邻居看见的是对端 router-id，不是物理口。它能做两件事：
// 印证跨设备聚合的对端是谁，以及指出「三层邻接有、二层链路没有」的缺口。
// Loopback 上的邻接直接跳过——那是逻辑接口，跟物理拓扑无关。
func verifyByRoute(dir *Directory, routes []RouteEntry, byEnd map[linkEnd]*Link, notices *noticeList, links *[]Link) {
	for _, e := range routes {
		if strings.HasPrefix(strings.ToLower(e.LocalIf), "loopback") || strings.TrimSpace(e.LocalIf) == "" {
			continue
		}
		dev, ok := dir.Device(e.Device)
		if !ok {
			notices.add(NoticeDeviceUnknown, SevWarn, e.Device,
				fmt.Sprintf("路由邻居快照里的设备 %q 不在台账里", e.Device))
			continue
		}
		port, ok := dir.Port(dev, e.LocalIf)
		if !ok {
			notices.add(NoticePortUnknown, SevInfo, dev+":"+e.LocalIf,
				fmt.Sprintf("路由邻居的本端接口 %q 不是台账里的物理口，跳过校验", e.LocalIf))
			continue
		}
		peer, ok := dir.DeviceByRouterID(e.PeerRouterID)
		if !ok {
			notices.add(NoticeDeviceUnknown, SevInfo, e.PeerRouterID,
				fmt.Sprintf("%s 的邻居 router-id %s 不在台账里", dev, e.PeerRouterID))
			continue
		}

		end := dir.fold(dev, port)
		l, ok := byEnd[end]
		if !ok {
			notices.add(NoticeRouteNoPhysical, SevInfo, end.String(),
				fmt.Sprintf("%s 与 %s 有 %s 邻接（本端 %s），但没有任何 LLDP 链路指向 %s——"+
					"可能跨传输、可能 LLDP 没开",
					dev, peer, strings.ToUpper(e.Protocol), port, peer))
			continue
		}
		other := l.B
		if equalEnd(l.B, end) {
			other = l.A
		}
		if other.Device == peer {
			if !hasString(l.VerifiedBy, SourceRoute) {
				l.VerifiedBy = append(l.VerifiedBy, SourceRoute)
			}
			continue
		}
		notices.add(NoticeRouteMismatch, SevInfo, l.ID,
			fmt.Sprintf("%s 的三层邻接指向 %s，二层链路却指向 %s（子接口、跨设备聚合、或采集错）",
				end, peer, other.Device))
	}
}

// fuseMAC 做 MAC 反推，返回真正用上的 MAC 表行数。
//
// 规则：某个端口学到某台设备的管理 MAC，就推断这个端口后面挂着那台设备。
//
// MAC 反推只能给出一端：MAC 表告诉你「这个口后面有 spine-01」，
// 但它不知道 spine-01 的哪个口。所以反推出的链路对端端口是问号。
// 它只能补 LLDP 的空，不能改 LLDP 的结论——MAC 会漂移、会泛洪、会单向学习。
func fuseMAC(dir *Directory, macs []MACEntry, byEnd map[linkEnd]*Link, notices *noticeList, links *[]Link) int {
	used := 0
	seen := map[string]bool{}
	for _, e := range macs {
		peer, ok := dir.DeviceByMAC(e.MAC)
		if !ok {
			continue
		}
		dev, ok := dir.Device(e.Device)
		if !ok {
			notices.add(NoticeDeviceUnknown, SevWarn, e.Device,
				fmt.Sprintf("MAC 表快照里的设备 %q 不在台账里", e.Device))
			continue
		}
		port, ok := dir.Port(dev, e.Port)
		if !ok {
			notices.add(NoticePortUnknown, SevWarn, dev+":"+e.Port,
				fmt.Sprintf("MAC 表快照里的端口 %q 不在 %s 的端口表里", e.Port, dev))
			continue
		}
		if peer == dev {
			// 自己学到自己的管理 MAC：同一个广播域里的绕行学习，不是直连证据。
			continue
		}
		used++
		end := dir.fold(dev, port)

		if l, ok := byEnd[end]; ok {
			other := l.B
			if equalEnd(l.B, end) {
				other = l.A
			}
			if other.Device == peer {
				if !hasString(l.VerifiedBy, SourceMAC) {
					l.VerifiedBy = append(l.VerifiedBy, SourceMAC)
				}
				continue
			}
			// 冲突：LLDP 说这条链路连的是别人。LLDP 赢，MAC 只进提示。
			key := "mac:" + end.String() + ":" + peer
			if seen[key] {
				continue
			}
			seen[key] = true
			notices.add(NoticeMACConflict, SevInfo, l.ID,
				fmt.Sprintf("%s 学到 %s 的管理 MAC %s（反推指向 %s），但 LLDP 说这条链路另一端是 %s——"+
					"按 LLDP 保留，MAC 只进提示（漂移或泛洪）",
					end, peer, e.MAC, peer, other.Device))
			continue
		}

		// LLDP 没覆盖这个端口。MAC 反推是唯一的线索，出链路，打问号。
		//
		// 方向固定为「学到 MAC 的那一端 → 设备」。它是唯一有方向的链路类型：
		// 对端端口未知，把已知的那端放前面才读得通。
		to := linkEnd{Device: peer}
		l := Link{
			ID:            end.String() + " ↔ " + to.String(),
			A:             end,
			B:             to,
			Kind:          KindInferred,
			Confidence:    ConfLow,
			Sources:       []string{SourceMAC},
			DeclaredFrom:  []string{end.String()},
			Question:      true,
			Bidirectional: false,
			Evidence: fmt.Sprintf("MAC 表反推：%s 学到 %s 的管理 MAC %s，对端端口未知（LLDP 没覆盖）",
				end, peer, e.MAC),
		}
		*links = append(*links, l)
		idx := len(*links) - 1
		byEnd[l.A] = &(*links)[idx]
		notices.add(NoticeMACInferred, SevInfo, l.ID,
			fmt.Sprintf("%s 只有 MAC 反推支撑，对端端口是问号，置信度 %s——先去把 LLDP 打开再谈拓扑",
				end, ConfLow))
	}
	return used
}

// noticeList 收集提示并做去重。同一条冲突被两个源各发现一次是常态。
type noticeList struct {
	items []Notice
	seen  map[string]bool
}

func (n *noticeList) add(class, severity, subject, detail string) {
	if n.seen == nil {
		n.seen = map[string]bool{}
	}
	key := class + "|" + subject + "|" + detail
	if n.seen[key] {
		return
	}
	n.seen[key] = true
	n.items = append(n.items, Notice{Class: class, Severity: severity, Subject: subject, Detail: detail})
}

// sorted 按「严重级降序、分类、对象」排，warn 永远在前。
func (n *noticeList) sorted() []Notice {
	return sortNotices(n.items)
}

// sortNotices 是提示清单的唯一排序口径。报表也用它，
// 免得「正文里已排好、单独调用报表时又乱回去」。
func sortNotices(items []Notice) []Notice {
	out := make([]Notice, len(items))
	copy(out, items)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Severity != out[j].Severity {
			return out[i].Severity == SevWarn
		}
		if out[i].Class != out[j].Class {
			return out[i].Class < out[j].Class
		}
		return out[i].Subject < out[j].Subject
	})
	return out
}

func hasString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
