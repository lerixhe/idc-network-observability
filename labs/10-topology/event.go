package main

import (
	"fmt"
	"sort"
	"strings"
)

// sourceKinds 把各源的原生 kind 归到三个事件族。
//
// 这一张表就是「归一化」的全部内容。四源的原生说法：
//
//	trap    link_down / link_up
//	syslog  IF_DOWN_LINK_FAILURE / IF_UP / IF_DOWN_ADMIN_DISABLED
//	counter counter_reset（ifCounterDiscontinuityTime 重启）
//	probe   probe_down / probe_up
//
// 归到同一族才可能撞上同一个指纹。归错了，要么把不相关的事并成一条，
// 要么同一件事永远并不上——两种错都不报错，只是事件数悄悄不对。
var sourceKinds = map[string]map[string]string{
	SourceTrap: {
		"link_down": FamilyLinkDown,
		"link_up":   FamilyLinkUp,
	},
	SourceSyslog: {
		"IF_DOWN_LINK_FAILURE":   FamilyLinkDown,
		"IF_UP":                  FamilyLinkUp,
		"IF_DOWN_ADMIN_DISABLED": FamilyAdminDown,
	},
	SourceCounter: {
		"counter_reset": FamilyLinkDown,
	},
	SourceProbe: {
		"probe_down": FamilyLinkDown,
		"probe_up":   FamilyLinkUp,
	},
}

// EventPolicy 是去重口径。三个都是参数，不是常量——它们描述策略，与设备档位有关。
type EventPolicy struct {
	// DedupWindowMs 是同指纹合并的时间窗。锚点固定，不随合并滑动。
	DedupWindowMs int64
	// BurstWindowMs 是设备级聚合的窗口。同设备同族的事件落在这个跨度内才算一批。
	BurstWindowMs int64
	// BurstMinPorts 是折叠成设备级事件所需的端口数下限。
	BurstMinPorts int
}

// defaultEventPolicy：5 分钟内同指纹合并；60 秒内 ≥ 8 个端口同族出事就折叠成设备级。
var defaultEventPolicy = EventPolicy{
	DedupWindowMs: 300_000,
	BurstWindowMs: 60_000,
	BurstMinPorts: 8,
}

// Event 是一条去重后的事件。
//
// Scope=port 是一条端口事件，可能由四个源合成；Scope=device 是一次设备级聚合，
// PortCount 记它吃掉了多少个端口。两种都用 RawCount 记原始条数——
// 值班人得知道「这一条顶掉了 192 条」。
type Event struct {
	TS        string   `json:"ts"`
	LastTS    string   `json:"last_ts"`
	Device    string   `json:"device"`
	Port      string   `json:"port,omitempty"`
	Family    string   `json:"family"`
	Scope     string   `json:"scope"`
	PortCount int      `json:"port_count,omitempty"`
	RawCount  int      `json:"raw_count"`
	Sources   []string `json:"sources"`
	Kinds     []string `json:"kinds"`
	// Fingerprint 是去重键：族|设备|端口（设备级是 族|设备|*）。时间不进指纹。
	Fingerprint string `json:"fingerprint"`
	Evidence    string `json:"evidence"`

	tsMs int64
}

// EventSet 是一次去重的结果。
type EventSet struct {
	Events  []Event
	Notices []Notice

	// Raw 是喂进来的原始条数，PortEvents 是端口级事件条数，Unresolved 是归一化失败条数。
	Raw        int
	PortEvents int
	Unresolved int
	OutOfOrder int
}

// normedEvent 是一条归一化后的原始事件。
type normedEvent struct {
	tsMs     int64
	ts       string
	device   string
	port     string
	family   string
	source   string
	kind     string
	deviceOK bool
	portOK   bool
	order    int
}

// Dedupe 是事件去重入口：归一化 → 指纹去重 → 设备级聚合。
//
// 三步各解决一件事，缺一个都不行：
//
//  1. 归一化：四个源对同一台设备、同一个端口有四种写法。不归一化，
//     连「这两条说的是同一个端口」都判断不出来，去重无从谈起。
//  2. 指纹去重：同设备同端口同族、落在时间窗内，就是一件事，合成一条。
//  3. 设备级聚合：一台交换机 48 个口同秒全 down，是 48 件事还是 1 件事？
//     对值班人来说是 1 件（设备重启或上联断了）。端口级事件先出来，
//     再把同一窗口内同族的批量端口事件折叠成一条，带端口计数。
func Dedupe(dir *Directory, streams []EventStream, policy EventPolicy) (EventSet, error) {
	set := EventSet{}
	notices := &noticeList{}

	var raws []RawEvent
	for _, s := range streams {
		raws = append(raws, s.Raw...)
		set.OutOfOrder += s.OutOfOrder
	}
	set.Raw = len(raws)

	list := make([]normedEvent, 0, len(raws))
	for i, r := range raws {
		tsMs, err := parseTS(r.TS)
		if err != nil {
			return set, fmt.Errorf("第 %d 条事件 ts=%q: %w", i+1, r.TS, err)
		}
		bySource, ok := sourceKinds[r.Source]
		if !ok {
			return set, fmt.Errorf("第 %d 条事件来源 %q 不认识", i+1, r.Source)
		}
		family, ok := bySource[r.Kind]
		if !ok {
			notices.add(NoticeKindConflict, SevWarn, r.Source+"/"+r.Kind,
				fmt.Sprintf("来源 %s 的 kind %q 不在映射表里，归不了族，事件丢弃", r.Source, r.Kind))
			set.Unresolved++
			continue
		}

		dev, deviceOK := dir.Device(r.Device)
		if !deviceOK {
			dev = strings.TrimSpace(r.Device)
		}
		port := strings.TrimSpace(r.Port)
		portOK := false
		if deviceOK {
			if name, ok := dir.Port(dev, port); ok {
				port, portOK = name, true
			}
		}
		if !deviceOK {
			notices.add(NoticeDeviceUnknown, SevWarn, dev,
				fmt.Sprintf("%s 报的设备 %q 不在台账里，事件保留但归不到对象上", r.Source, r.Device))
			set.Unresolved++
		} else if !portOK {
			notices.add(NoticePortUnknown, SevWarn, dev+":"+port,
				fmt.Sprintf("%s 报的端口 %q 在 %s 的端口表里翻不出来，事件保留原文（别名表缺这一条）",
					r.Source, r.Port, dev))
			set.Unresolved++
		}

		list = append(list, normedEvent{
			tsMs: tsMs, ts: r.TS,
			device: dev, port: port, family: family,
			source: r.Source, kind: r.Kind,
			deviceOK: deviceOK, portOK: portOK,
			order: i,
		})
	}

	// 乱序在 loadEventStream 里按流算完了。这里只记一条提示，
	// 然后统一按时间重排——不重排，同一件事会因为「晚到的那条先处理」
	// 而被切成两条事件。
	if set.OutOfOrder > 0 {
		notices.add(NoticeOutOfOrder, SevInfo, "event-stream",
			fmt.Sprintf("到达顺序与时间顺序倒置 %d 条，已按时间戳重排后再去重", set.OutOfOrder))
	}

	// 时间排序，同毫秒按到达顺序，保证结果可复现。
	sort.SliceStable(list, func(i, j int) bool {
		if list[i].tsMs != list[j].tsMs {
			return list[i].tsMs < list[j].tsMs
		}
		return list[i].order < list[j].order
	})

	// 指纹去重。窗口锚点是组内第一条的时间，不滑动。
	//
	// 锚点滑动会把一条持续两小时的抖动压成一条事件，那是另一个极端：
	// 该报的没报。锚点固定，5 分钟以后的同指纹事件另起一条。
	type groupKey struct {
		device, port, family string
	}
	groups := make(map[groupKey][]normedEvent, len(list))
	keys := make([]groupKey, 0, len(list))
	for _, e := range list {
		k := groupKey{device: e.device, port: e.port, family: e.family}
		if _, ok := groups[k]; !ok {
			keys = append(keys, k)
		}
		groups[k] = append(groups[k], e)
	}

	events := make([]Event, 0, len(keys))
	for _, k := range keys {
		chain := groups[k]
		start := 0
		for i := 1; i <= len(chain); i++ {
			if i < len(chain) && chain[i].tsMs-chain[start].tsMs <= policy.DedupWindowMs {
				continue
			}
			events = append(events, makePortEvent(chain[start:i]))
			start = i
		}
	}
	set.PortEvents = len(events)

	// 同端口不同族：不合并，只提示。link_down 与 admin_down 是两件事。
	detectKindConflicts(events, policy, notices)

	// 设备级聚合。
	kept, bursts := foldBursts(events, policy)
	set.Events = append(kept, bursts...)
	sort.SliceStable(set.Events, func(i, j int) bool {
		if set.Events[i].tsMs != set.Events[j].tsMs {
			return set.Events[i].tsMs < set.Events[j].tsMs
		}
		if set.Events[i].Device != set.Events[j].Device {
			return set.Events[i].Device < set.Events[j].Device
		}
		return set.Events[i].Family < set.Events[j].Family
	})
	set.Notices = notices.sorted()
	return set, nil
}

// makePortEvent 把同一指纹在窗口内的一串原始事件合成一条。
func makePortEvent(chain []normedEvent) Event {
	first := chain[0]
	e := Event{
		TS:          first.ts,
		LastTS:      chain[len(chain)-1].ts,
		Device:      first.device,
		Port:        first.port,
		Family:      first.family,
		Scope:       ScopePort,
		RawCount:    len(chain),
		Fingerprint: fingerprintOf(first.device, first.port, first.family),
		tsMs:        first.tsMs,
	}

	srcSet := map[string]bool{}
	kindSet := map[string]bool{}
	for _, c := range chain {
		srcSet[c.source] = true
		kindSet[c.source+"/"+c.kind] = true
	}
	for s := range srcSet {
		e.Sources = append(e.Sources, s)
	}
	for k := range kindSet {
		e.Kinds = append(e.Kinds, k)
	}
	sort.Strings(e.Sources)
	sort.Strings(e.Kinds)

	if len(chain) == 1 {
		e.Evidence = fmt.Sprintf("单源事件（%s），没有其他源印证", first.kind)
	} else {
		e.Evidence = fmt.Sprintf("%d 个源指向同一件事：%s", len(chain), strings.Join(e.Kinds, " / "))
	}
	return e
}

// fingerprintOf 是指纹唯一的一处定义。
//
// 字段选择是全部功夫所在：
//
//	族      换族就不是一件事（link_down 与 admin_down 不合并）
//	设备    规范名，不是原始写法（否则四个源永远并不上）
//	端口    规范名，不是 ifIndex（换板卡就重排）
//
// 刻意不进去的：时间戳（进去就一条都並不上了）、源（进去四源变四条）、
// 序号（每条都不同）、原文（里面带时间与计数器值）。
func fingerprintOf(device, port, family string) string {
	if port == "" {
		return family + "|" + device + "|*"
	}
	return family + "|" + device + "|" + port
}

// detectKindConflicts 找同设备同端口、窗口内出现不同族的事件。
//
// 这类冲突不能自动合并。trap 说 link_down、syslog 说 IF_DOWN_ADMIN_DISABLED，
// 合到哪一族都行，但结论完全不同：一个记故障，一个记变更。
// 事件流水线在这里只能上报冲突，判断留给人——合并错了，故障统计就废了。
func detectKindConflicts(events []Event, policy EventPolicy, notices *noticeList) {
	buckets := map[string][]Event{}
	for _, e := range events {
		if e.Scope != ScopePort {
			continue
		}
		k := e.Device + "|" + e.Port
		buckets[k] = append(buckets[k], e)
	}
	for _, list := range buckets {
		for i := 0; i < len(list); i++ {
			for j := i + 1; j < len(list); j++ {
				a, b := list[i], list[j]
				if a.Family == b.Family {
					continue
				}
				if absMs(b.tsMs-a.tsMs) > policy.DedupWindowMs {
					continue
				}
				x, y := a, b
				if x.Family > y.Family {
					x, y = y, x
				}
				notices.add(NoticeKindConflict, SevWarn, x.Device+" "+x.Port,
					fmt.Sprintf("窗口内两个族：%s（%s，%s）与 %s（%s，%s），不合并——"+
						"一个是故障一个是变更，合并错了统计就废了",
						x.Family, x.TS, strings.Join(x.Sources, ","),
						y.Family, y.TS, strings.Join(y.Sources, ",")))
			}
		}
	}
}

// foldBursts 把「一台设备大量端口在同一窗口内同族出事」折叠成一条设备级事件。
//
// 为什么必须折叠：48 口同秒全 down，端口级事件是 48 条。值班人看到 48 条
// 告警会先骂监控，再一条条点掉——而真相是设备重启或上联断了，一件事。
// 折叠后是一条「tor-a3f-03 48 个端口全 down」，指向一个根因。
//
// 折叠条件是端口数下限，不是 ports 全部。48 口里 3 个口 down 是三条独立的
// 端口故障，不该折叠。
func foldBursts(events []Event, policy EventPolicy) (kept []Event, bursts []Event) {
	type devFamily struct {
		device, family string
	}
	groups := map[devFamily][]Event{}
	order := []devFamily{}
	for _, e := range events {
		if e.Scope != ScopePort || e.Port == "" {
			// 端口没归一化的事件不参与聚合：连是哪个口都没定，聚进去只是把脏数据藏起来。
			kept = append(kept, e)
			continue
		}
		k := devFamily{device: e.Device, family: e.Family}
		if _, ok := groups[k]; !ok {
			order = append(order, k)
		}
		groups[k] = append(groups[k], e)
	}

	for _, k := range order {
		chain := groups[k]
		sort.SliceStable(chain, func(i, j int) bool {
			if chain[i].tsMs != chain[j].tsMs {
				return chain[i].tsMs < chain[j].tsMs
			}
			return chain[i].Port < chain[j].Port
		})

		i := 0
		for i < len(chain) {
			j := i
			ports := map[string]bool{}
			raw := 0
			for j < len(chain) && chain[j].tsMs-chain[i].tsMs <= policy.BurstWindowMs {
				ports[chain[j].Port] = true
				raw += chain[j].RawCount
				j++
			}
			if len(ports) < policy.BurstMinPorts {
				kept = append(kept, chain[i])
				i++
				continue
			}
			bursts = append(bursts, makeBurstEvent(chain[i:j], ports, raw, policy))
			i = j
		}
	}
	return kept, bursts
}

// makeBurstEvent 生成设备级事件。端口清单只留前三个，其余进计数。
func makeBurstEvent(chain []Event, ports map[string]bool, raw int, policy EventPolicy) Event {
	first, last := chain[0], chain[len(chain)-1]
	srcSet := map[string]bool{}
	kindSet := map[string]bool{}
	for _, e := range chain {
		for _, s := range e.Sources {
			srcSet[s] = true
		}
		for _, k := range e.Kinds {
			kindSet[k] = true
		}
	}
	sources := make([]string, 0, len(srcSet))
	for s := range srcSet {
		sources = append(sources, s)
	}
	sort.Strings(sources)
	kinds := make([]string, 0, len(kindSet))
	for k := range kindSet {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)

	names := make([]string, 0, len(ports))
	for p := range ports {
		names = append(names, p)
	}
	sort.Strings(names)
	head := names
	if len(head) > 3 {
		head = head[:3]
	}
	more := ""
	if len(names) > len(head) {
		more = fmt.Sprintf(" 等 %d 个", len(names))
	}

	return Event{
		TS:          first.TS,
		LastTS:      last.TS,
		Device:      first.Device,
		Family:      first.Family,
		Scope:       ScopeDevice,
		PortCount:   len(ports),
		RawCount:    raw,
		Sources:     sources,
		Kinds:       kinds,
		Fingerprint: fingerprintOf(first.Device, "", first.Family),
		Evidence: fmt.Sprintf("%d 个端口在 %ds 内同族触发（%s%s），折叠成设备级事件——"+
			"先查设备本身：重启、上联、电源",
			len(ports), policy.BurstWindowMs/1000, strings.Join(head, "、"), more),
		tsMs: first.tsMs,
	}
}

func absMs(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}
