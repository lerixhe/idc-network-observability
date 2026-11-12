package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
)

// ValueKind 是 SNMP 的基本类型。真协议里有十几种，这里只留本实验用得到的五种。
//
// 类型不是装饰：counter32 会在 34 秒内回绕一次（1G 口满速），integer 不会。
// 采集器把类型丢了，速率就会算成负数。
type ValueKind string

const (
	KindString    ValueKind = "string"
	KindInteger   ValueKind = "integer"
	KindGauge32   ValueKind = "gauge32"
	KindCounter32 ValueKind = "counter32"
	KindCounter64 ValueKind = "counter64"
	KindTimeTicks ValueKind = "timeticks"
)

// Value 是一个 varbind 的值。带类型标签的联合体，比 any 好调试。
type Value struct {
	Kind ValueKind `json:"kind"`
	Str  string    `json:"str,omitempty"`
	Num  uint64    `json:"num,omitempty"`
}

func StringValue(s string) Value { return Value{Kind: KindString, Str: s} }
func IntValue(n uint64) Value    { return Value{Kind: KindInteger, Num: n} }
func GaugeValue(n uint64) Value  { return Value{Kind: KindGauge32, Num: n} }
func Counter32Value(n uint64) Value {
	return Value{Kind: KindCounter32, Num: n}
}
func Counter64Value(n uint64) Value {
	return Value{Kind: KindCounter64, Num: n}
}
func TimeTicksValue(n uint64) Value { return Value{Kind: KindTimeTicks, Num: n} }

func (v Value) String() string {
	switch v.Kind {
	case KindString:
		return v.Str
	case KindTimeTicks:
		return fmt.Sprintf("%d ticks", v.Num)
	default:
		return fmt.Sprintf("%d", v.Num)
	}
}

// Base 是表的入口 OID。列 OID = Base + 列号，单元格 OID = 列 OID + 行索引。
const (
	ifTableBase  = "1.3.6.1.2.1.2.2.1"
	ifXTableBase = "1.3.6.1.2.1.31.1.1.1"
)

// Column 是一列。表遍历的单位是列，不是行：GetBulk 一遍只能沿着字典序往下走，
// 而同一行的几列在 OID 树上隔着整张表的宽度，只能一列一列走。
type Column struct {
	Name  string
	Table string
	OID   OID
	Kind  ValueKind
	get   func(Interface) Value
}

// Columns 是本实验要采的全部 12 列。ifIndex 不在里面——它是行索引，
// 从单元格 OID 的最后一级读出来，不是走出来的。
var Columns = []Column{
	{"ifDescr", "ifTable", MustOID(ifTableBase + ".2"), KindString,
		func(i Interface) Value { return StringValue(i.IfDescr) }},
	{"ifType", "ifTable", MustOID(ifTableBase + ".3"), KindInteger,
		func(i Interface) Value { return IntValue(uint64(i.IfType)) }},
	{"ifSpeed", "ifTable", MustOID(ifTableBase + ".5"), KindGauge32,
		func(i Interface) Value { return GaugeValue(i.IfSpeedBps) }},
	{"ifAdminStatus", "ifTable", MustOID(ifTableBase + ".7"), KindInteger,
		func(i Interface) Value { return IntValue(uint64(i.IfAdminStatus)) }},
	{"ifOperStatus", "ifTable", MustOID(ifTableBase + ".8"), KindInteger,
		func(i Interface) Value { return IntValue(uint64(i.IfOperStatus)) }},
	{"ifInErrors", "ifTable", MustOID(ifTableBase + ".14"), KindCounter32,
		func(i Interface) Value { return Counter32Value(uint64(i.IfInErrors)) }},
	{"ifOutErrors", "ifTable", MustOID(ifTableBase + ".20"), KindCounter32,
		func(i Interface) Value { return Counter32Value(uint64(i.IfOutErrors)) }},

	{"ifName", "ifXTable", MustOID(ifXTableBase + ".1"), KindString,
		func(i Interface) Value { return StringValue(i.IfName) }},
	{"ifHCInOctets", "ifXTable", MustOID(ifXTableBase + ".6"), KindCounter64,
		func(i Interface) Value { return Counter64Value(i.IfHCInOctets) }},
	{"ifHCOutOctets", "ifXTable", MustOID(ifXTableBase + ".10"), KindCounter64,
		func(i Interface) Value { return Counter64Value(i.IfHCOutOctets) }},
	{"ifHighSpeed", "ifXTable", MustOID(ifXTableBase + ".15"), KindGauge32,
		func(i Interface) Value { return GaugeValue(i.IfHighSpeedMbps) }},
	{"ifAlias", "ifXTable", MustOID(ifXTableBase + ".18"), KindString,
		func(i Interface) Value { return StringValue(i.IfAlias) }},
}

// ColumnByName 按 MIB 名查列，测试与报表都要用。
func ColumnByName(name string) (Column, bool) {
	for _, c := range Columns {
		if c.Name == name {
			return c, true
		}
	}
	return Column{}, false
}

// Interface 是样本里一行端口。字段名用 JSON 的下划线风格，
// 与如果落在真实设备上的 ifTable/ifXTable 列一一对应（见 README 的对照表）。
type Interface struct {
	IfIndex         int    `json:"if_index"`
	IfName          string `json:"if_name"`
	IfAlias         string `json:"if_alias"`
	IfDescr         string `json:"if_descr"`
	IfType          int    `json:"if_type"`
	IfSpeedBps      uint64 `json:"if_speed_bps"`
	IfHighSpeedMbps uint64 `json:"if_high_speed_mbps"`
	IfAdminStatus   int    `json:"if_admin_status"`
	IfOperStatus    int    `json:"if_oper_status"`
	IfHCInOctets    uint64 `json:"if_hc_in_octets"`
	IfHCOutOctets   uint64 `json:"if_hc_out_octets"`
	IfInErrors      uint32 `json:"if_in_errors"`
	IfOutErrors     uint32 `json:"if_out_errors"`
}

// AgentProfile 是一台设备的管理面能力，决定它被分到哪一档。
//
// MaxRepetitions 是设备自己的上限，不是我们的策略：request 里的
// max-repetitions 超过它，设备直接回 tooBig，采集器只能把条数砍半重试。
type AgentProfile struct {
	Tier           string `json:"tier"`
	MaxRepetitions int    `json:"max_repetitions"`
	ResponseMs     int    `json:"response_ms"`
	JitterMs       int    `json:"jitter_ms"`
	Unreachable    bool   `json:"unreachable"`
}

// Device 是一台被采的设备。样本里有表的全部内容，相当于一台设备的应答录存。
type Device struct {
	Name       string       `json:"name"`
	MgmtIP     string       `json:"mgmt_ip"`
	Model      string       `json:"model"`
	Firmware   string       `json:"firmware"`
	SysUptimeS uint64       `json:"sys_uptime_s"`
	SNMP       AgentProfile `json:"snmp"`
	Interfaces []Interface  `json:"interfaces"`
}

// Fleet 是样本文件的外层结构。
type Fleet struct {
	Note        string   `json:"note"`
	CollectedAt string   `json:"collected_at"`
	Devices     []Device `json:"devices"`
}

// LoadFleet 读样本文件并按设备名排序，保证同样的输入有同样的采集顺序。
func LoadFleet(path string) (*Fleet, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读样本 %s: %w", path, err)
	}
	var f Fleet
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("解析样本 %s: %w", path, err)
	}
	if err := f.Validate(); err != nil {
		return nil, fmt.Errorf("样本 %s: %w", path, err)
	}
	sort.Slice(f.Devices, func(i, j int) bool { return f.Devices[i].Name < f.Devices[j].Name })
	return &f, nil
}

// Validate 挡住会让整轮采集静默跑歪的样本：端口索引重复、端口名为空、
// 设备重名。这些错在真机上都要等到「曲线接到别人身上」才发现。
func (f *Fleet) Validate() error {
	if len(f.Devices) == 0 {
		return fmt.Errorf("样本里没有设备")
	}
	seenDev := map[string]bool{}
	for _, d := range f.Devices {
		if d.Name == "" {
			return fmt.Errorf("有设备没有名字")
		}
		if seenDev[d.Name] {
			return fmt.Errorf("设备名 %s 重复", d.Name)
		}
		seenDev[d.Name] = true
		if len(d.Interfaces) == 0 {
			return fmt.Errorf("设备 %s 没有端口", d.Name)
		}
		if d.SNMP.MaxRepetitions <= 0 {
			return fmt.Errorf("设备 %s 的 max_repetitions 必须大于 0", d.Name)
		}
		seenIdx := map[int]bool{}
		seenName := map[string]bool{}
		for _, i := range d.Interfaces {
			if i.IfIndex <= 0 {
				return fmt.Errorf("设备 %s 有端口 ifIndex=%d，必须大于 0", d.Name, i.IfIndex)
			}
			if seenIdx[i.IfIndex] {
				return fmt.Errorf("设备 %s 的 ifIndex %d 重复", d.Name, i.IfIndex)
			}
			seenIdx[i.IfIndex] = true
			if i.IfName == "" {
				return fmt.Errorf("设备 %s 的 ifIndex %d 没有 ifName", d.Name, i.IfIndex)
			}
			if seenName[i.IfName] {
				return fmt.Errorf("设备 %s 的端口名 %s 重复", d.Name, i.IfName)
			}
			seenName[i.IfName] = true
		}
	}
	return nil
}

// DeviceByName 按名字取设备。
func (f *Fleet) DeviceByName(name string) (*Device, bool) {
	for i := range f.Devices {
		if f.Devices[i].Name == name {
			return &f.Devices[i], true
		}
	}
	return nil, false
}

// Entry 是 agent 应答里的一条：一个叶子 OID 和它的值。
type Entry struct {
	OID   OID
	Value Value
}

// Entries 把这台设备的全部可采数据摊平成一张按 OID 排序的叶子表。
//
// 真设备上是 MIB 树，采集器看不见结构，只能靠字典序往下摸。这里摊平了，
// 但排序规则、跨表顺序、表尾之后还有别的 OID 这三件事全部保留——
// GetBulk 的坑都在这些地方。
func (d *Device) Entries() []Entry {
	var es []Entry
	// system 组：真实设备一定有，且在 ifTable 之前。
	es = append(es,
		Entry{MustOID("1.3.6.1.2.1.1.1.0"), StringValue(d.Model)},
		Entry{MustOID("1.3.6.1.2.1.1.3.0"), TimeTicksValue(d.SysUptimeS * 100)},
		Entry{MustOID("1.3.6.1.2.1.1.5.0"), StringValue(d.Name)},
		Entry{MustOID("1.3.6.1.2.1.1.6.0"), StringValue("idc-" + d.MgmtIP)},
	)
	for _, c := range Columns {
		for _, itf := range d.Interfaces {
			es = append(es, Entry{OID: c.OID.Child(uint32(itf.IfIndex)), Value: c.get(itf)})
		}
	}
	// 表尾之后还有别的 OID。真实 MIB 里 1.3.6.1.2.1.4.20.1.1（ipAdEntAddr）
	// 排在 ifTable 与 ifXTable 之间，而 ifXTable 后面紧跟着 ifStackLastChange。
	// 有它们在，GetBulk 永远不会「自然停住」——停不停是采集器自己判的。
	ipIdx := MustOID("1.3.6.1.2.1.4.20.1.1")
	for _, last := range []uint32{11, 21, 31, 41} {
		es = append(es, Entry{
			OID:   ipIdx.Child(10).Child(0).Child(0).Child(last),
			Value: StringValue(fmt.Sprintf("10.0.0.%d", last)),
		})
	}
	es = append(es, Entry{MustOID("1.3.6.1.2.1.31.1.2.0"), TimeTicksValue(881440200)})
	sort.Slice(es, func(i, j int) bool { return es[i].OID.Compare(es[j].OID) < 0 })
	return es
}
