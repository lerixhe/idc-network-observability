package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// 来源名。四份拓扑快照与四类事件源，一个概念只留一种拼写。
const (
	SourceLLDP  = "lldp"
	SourceSTP   = "stp"
	SourceRoute = "route"
	SourceMAC   = "mac"

	SourceTrap    = "trap"
	SourceSyslog  = "syslog"
	SourceCounter = "counter"
	SourceProbe   = "probe"
)

// 置信度只有两档：高（LLDP 直接报的）与低（MAC 表反推的）。
//
// 中间那一档故意不设。STP 与路由邻居给不出「谁连谁」，只给得出
// 「这条链路的某一端处于什么角色」「这个三层邻居是活的」，它们进
// VerifiedBy，不进 Confidence——一档置信度对应一种能直接落库的结论。
const (
	ConfHigh = "high"
	ConfLow  = "low"
)

// 链路类型。inferred 表示对端端口未知，链路上有个问号。
const (
	KindPhysical = "physical"
	KindLag      = "lag"
	KindInferred = "inferred"
)

// 事件族。各源的原生 kind 先归到这三族，指纹才可能撞上。
const (
	FamilyLinkDown  = "link_down"
	FamilyLinkUp    = "link_up"
	FamilyAdminDown = "admin_down"
)

// 事件作用域。port 是单端口事件，device 是多端口同秒聚合后的设备级事件。
const (
	ScopePort   = "port"
	ScopeDevice = "device"
)

// 提示严重级。warn 要人看一眼，info 只做记录，都不改结论。
const (
	SevWarn = "warn"
	SevInfo = "info"
)

// 提示分类。拓扑与事件共用一份清单，冲突永远只进清单，不改结论。
const (
	NoticePeerMismatch    = "peer_mismatch"
	NoticeOneSided        = "lldp_one_sided"
	NoticeSTPBlocked      = "stp_blocked"
	NoticeMACConflict     = "mac_conflict"
	NoticeMACInferred     = "mac_inferred"
	NoticeDeviceUnknown   = "device_unknown"
	NoticePortUnknown     = "port_alias_unknown"
	NoticeRouteMismatch   = "route_mismatch"
	NoticeRouteNoPhysical = "route_no_physical"
	NoticeKindConflict    = "kind_conflict"
	NoticeOutOfOrder      = "out_of_order"
)

// Notice 是一条提示。Class 给机器过滤，Detail 给人看。
type Notice struct {
	Class    string `json:"class"`
	Severity string `json:"severity"`
	// Subject 是这条提示挂在哪：拓扑用链路 ID，事件用「设备 端口」。
	Subject string `json:"subject"`
	Detail  string `json:"detail"`
}

// CMDB 是台账快照：设备、管理 IP/MAC、端口别名、聚合口定义。
//
// 别名表是本实验所有归一化的唯一来源。归一化做错，后面每一步都在错的名字上算。
type CMDB struct {
	Devices []Device `json:"devices"`
	Lags    []Lag    `json:"lags"`
}

// Device 是一台设备。Name 是规范名，其余都是别人嘴里的叫法。
type Device struct {
	Name     string     `json:"name"`
	Aliases  []string   `json:"aliases"`
	MgmtIPs  []string   `json:"mgmt_ips"`
	MgmtMAC  string     `json:"mgmt_mac"`
	RouterID string     `json:"router_id"`
	Vendor   string     `json:"vendor"`
	Ports    []CMDBPort `json:"ports"`
}

// CMDBPort 是一个端口。Aliases 收各厂商的写法，IfIndex 用来反查只报索引的源。
type CMDBPort struct {
	Name    string   `json:"name"`
	Aliases []string `json:"aliases"`
	IfIndex int      `json:"if_index"`
}

// Lag 是一个聚合口。Members 是成员口的规范名。
//
// 聚合关系只认这张表，端口自己不带 lag 字段——两处都能定义聚合，
// 迟早会出现两处不一致，然后在「哪个是逻辑口」上吵半天。
type Lag struct {
	Name    string   `json:"name"`
	Device  string   `json:"device"`
	Members []string `json:"members"`
}

type devPort struct {
	device string
	name   string // 已转小写
}

type ifIndexKey struct {
	device  string
	ifIndex int
}

// Directory 是归一化器：别名 → 规范名。
//
// 设备名认别名与带外 IP，端口名认别名与 ifIndex，聚合口认成员归属。
// 查不到就是查不到，绝不猜一个出来。
type Directory struct {
	deviceByAlias map[string]string
	portByAlias   map[devPort]string
	portByIndex   map[ifIndexKey]string
	lagOfPort     map[devPort]string
	lagMembers    map[devPort][]string
	isLagPort     map[devPort]bool
	deviceByMAC   map[string]string
	deviceByRID   map[string]string
	devices       map[string]bool
}

// NewDirectory 建归一化器。台账脏就直接报错，别带着脏台账往下跑。
func NewDirectory(c CMDB) (*Directory, error) {
	d := &Directory{
		deviceByAlias: make(map[string]string, len(c.Devices)*3),
		portByAlias:   make(map[devPort]string, len(c.Devices)*8),
		portByIndex:   make(map[ifIndexKey]string, len(c.Devices)*8),
		lagOfPort:     make(map[devPort]string, 8),
		lagMembers:    make(map[devPort][]string, 8),
		isLagPort:     make(map[devPort]bool, 8),
		deviceByMAC:   make(map[string]string, len(c.Devices)),
		deviceByRID:   make(map[string]string, len(c.Devices)),
		devices:       make(map[string]bool, len(c.Devices)),
	}

	for _, dev := range c.Devices {
		if strings.TrimSpace(dev.Name) == "" {
			return nil, fmt.Errorf("台账里有设备没有 name")
		}
		if d.devices[dev.Name] {
			return nil, fmt.Errorf("台账里设备 %s 重复登记", dev.Name)
		}
		d.devices[dev.Name] = true
		d.deviceByAlias[strings.ToLower(dev.Name)] = dev.Name
		for _, a := range dev.Aliases {
			if err := d.aliasDevice(a, dev.Name); err != nil {
				return nil, err
			}
		}
		for _, ip := range dev.MgmtIPs {
			if err := d.aliasDevice(ip, dev.Name); err != nil {
				return nil, err
			}
		}
		if dev.MgmtMAC != "" {
			d.deviceByMAC[strings.ToLower(dev.MgmtMAC)] = dev.Name
		}
		if dev.RouterID != "" {
			d.deviceByRID[dev.RouterID] = dev.Name
		}
		for _, p := range dev.Ports {
			if strings.TrimSpace(p.Name) == "" {
				return nil, fmt.Errorf("%s: 端口没有 name", dev.Name)
			}
			name, err := d.aliasPort(dev.Name, p.Name, p.Name)
			if err != nil {
				return nil, err
			}
			for _, a := range p.Aliases {
				if _, err := d.aliasPort(dev.Name, a, name); err != nil {
					return nil, err
				}
			}
			if p.IfIndex > 0 {
				k := ifIndexKey{device: dev.Name, ifIndex: p.IfIndex}
				if prev, ok := d.portByIndex[k]; ok && prev != name {
					return nil, fmt.Errorf("%s: ifIndex %d 同时挂在 %s 与 %s 上", dev.Name, p.IfIndex, prev, name)
				}
				d.portByIndex[k] = name
			}
		}
	}

	for _, lag := range c.Lags {
		if !d.devices[lag.Device] {
			return nil, fmt.Errorf("聚合口 %s 挂在台账里没有的设备 %s 上", lag.Name, lag.Device)
		}
		if _, err := d.aliasPort(lag.Device, lag.Name, lag.Name); err != nil {
			return nil, err
		}
		d.isLagPort[devPort{device: lag.Device, name: lag.Name}] = true
		for _, m := range lag.Members {
			member, ok := d.Port(lag.Device, m)
			if !ok {
				return nil, fmt.Errorf("聚合口 %s 的成员 %s 不在 %s 的端口表里", lag.Name, m, lag.Device)
			}
			k := devPort{device: lag.Device, name: member}
			if prev, ok := d.lagOfPort[k]; ok && prev != lag.Name {
				return nil, fmt.Errorf("%s %s 同时属于 %s 与 %s", lag.Device, member, prev, lag.Name)
			}
			d.lagOfPort[k] = lag.Name
			d.lagMembers[devPort{device: lag.Device, name: lag.Name}] = append(
				d.lagMembers[devPort{device: lag.Device, name: lag.Name}], member)
		}
	}
	return d, nil
}

func (d *Directory) aliasDevice(alias, name string) error {
	key := strings.ToLower(strings.TrimSpace(alias))
	if key == "" {
		return nil
	}
	if prev, ok := d.deviceByAlias[key]; ok && prev != name {
		return fmt.Errorf("设备别名 %q 同时指向 %s 与 %s", alias, prev, name)
	}
	d.deviceByAlias[key] = name
	return nil
}

func (d *Directory) aliasPort(device, alias, name string) (string, error) {
	key := strings.ToLower(strings.TrimSpace(alias))
	if key == "" {
		return "", fmt.Errorf("%s: 端口别名是空串", device)
	}
	k := devPort{device: device, name: key}
	if prev, ok := d.portByAlias[k]; ok && prev != name {
		return "", fmt.Errorf("%s: 端口别名 %q 同时指向 %s 与 %s", device, alias, prev, name)
	}
	d.portByAlias[k] = name
	return name, nil
}

// Device 把设备原始串归一到规范名。别名、主机名、带外 IP 都认。
func (d *Directory) Device(raw string) (string, bool) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", false
	}
	name, ok := d.deviceByAlias[strings.ToLower(s)]
	return name, ok
}

// DeviceByMAC 按管理 MAC 反查设备。MAC 表反推链路就靠它。
func (d *Directory) DeviceByMAC(mac string) (string, bool) {
	name, ok := d.deviceByMAC[strings.ToLower(strings.TrimSpace(mac))]
	return name, ok
}

// DeviceByRouterID 按 router-id 反查设备。路由邻居只给 router-id。
func (d *Directory) DeviceByRouterID(id string) (string, bool) {
	name, ok := d.deviceByRID[strings.TrimSpace(id)]
	return name, ok
}

// Port 把端口原始串归一到规范名。
//
// 纯数字当 ifIndex 查——有些设备的 lldpRemPortId 就是一个编号，
// 而 syslog 与计数器源也常常只给 ifIndex。
func (d *Directory) Port(device, raw string) (string, bool) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", false
	}
	if n, err := strconv.Atoi(s); err == nil {
		if name, ok := d.portByIndex[ifIndexKey{device: device, ifIndex: n}]; ok {
			return name, true
		}
	}
	name, ok := d.portByAlias[devPort{device: device, name: strings.ToLower(s)}]
	return name, ok
}

// fold 把成员口折叠到逻辑口。不参与聚合的端口原样返回。
//
// 这是聚合口对齐的唯一入口：一端报 Eth-Trunk1、另一端报 Ethernet1/10，
// 折叠之后才落到同一条链路上。
func (d *Directory) fold(device, port string) linkEnd {
	if lag, ok := d.LagOf(device, port); ok {
		return linkEnd{Device: device, Port: lag}
	}
	return linkEnd{Device: device, Port: port}
}

// LagOf 返回端口所属的逻辑口。不参与聚合的端口返回 false。
func (d *Directory) LagOf(device, port string) (string, bool) {
	lag, ok := d.lagOfPort[devPort{device: device, name: port}]
	return lag, ok
}

// Members 返回一个聚合口的成员口（规范名，按台账顺序）。
func (d *Directory) Members(device, lag string) []string {
	return d.lagMembers[devPort{device: device, name: lag}]
}

// IsLag 判断这个端口名本身是不是一个逻辑口。
func (d *Directory) IsLag(device, port string) bool {
	return d.isLagPort[devPort{device: device, name: port}]
}

// Managed 判断设备在不在台账里。不在台账还出现在 LLDP 里，就是幽灵设备。
func (d *Directory) Managed(device string) bool {
	return d.devices[device]
}

// LLDPEntry 是一行 lldpRemTable 快照：本端端口看到的一个邻居。
//
// 一行只代表一端的断言。两端都报、报的一致，才算双向确认。
type LLDPEntry struct {
	TS           string `json:"ts"`
	LocalDevice  string `json:"local_device"`
	LocalPort    string `json:"local_port"`
	RemoteDevice string `json:"remote_device"`
	// RemotePortID 是 lldpRemPortId，可能是名字、编号、甚至 MAC。
	RemotePortID string `json:"remote_port_id"`
	// RemotePortDesc 是 lldpRemPortDesc，部分设备只报 ID 不报描述。
	RemotePortDesc string `json:"remote_port_desc"`
	// TimeMark 是 lldpRemTimeMark。同一代在 msgTxInterval × txHold 后整体失效。
	TimeMark int `json:"time_mark"`
}

// STPEntry 是一台设备一个端口的生成树角色。
//
// STP 给不出「对端是谁」，只能给「我这端在树里是什么角色」。
// 所以它只能校验 LLDP，不能自己产生链路。
type STPEntry struct {
	TS         string `json:"ts"`
	Device     string `json:"device"`
	Port       string `json:"port"`
	Role       string `json:"role"`  // root / designated / alternate / backup / disabled
	State      string `json:"state"` // forwarding / blocking / discarding / learning
	RootBridge string `json:"root_bridge"`
}

// RouteEntry 是一个三层邻接。LocalIf 是跑邻居关系的本端接口。
type RouteEntry struct {
	TS           string `json:"ts"`
	Device       string `json:"device"`
	LocalIf      string `json:"local_if"`
	PeerRouterID string `json:"peer_router_id"`
	Protocol     string `json:"protocol"`
	State        string `json:"state"`
}

// MACEntry 是一行 MAC 表（FDB）。Mac 落在哪个端口，就说明那个端口后面有这台设备。
type MACEntry struct {
	TS     string `json:"ts"`
	Device string `json:"device"`
	Port   string `json:"port"`
	MAC    string `json:"mac"`
	Vlan   int    `json:"vlan"`
}

// RawEvent 是一条未归一化的原始事件，四个源共用一种信封。
//
// Seq 是源侧的序号，Msg 是原文。两者都不能进指纹：序号每条不同，
// 原文里带时间戳，进了指纹就一条都并不上。
type RawEvent struct {
	TS     string `json:"ts"`
	Source string `json:"source"`
	Device string `json:"device"`
	Port   string `json:"port"`
	Kind   string `json:"kind"`
	Msg    string `json:"msg"`
	Seq    int    `json:"seq"`
}

// loadJSON 读一个 JSON 文件。
func loadJSON(path string, v any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, v); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

// scanJSONL 逐行读 JSONL，回调拿到行号与内容，报错能指到行。
func scanJSONL(path string, fn func(line int, text string) error) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for n := 1; sc.Scan(); n++ {
		text := strings.TrimSpace(sc.Text())
		if text == "" {
			continue
		}
		if err := fn(n, text); err != nil {
			return err
		}
	}
	return sc.Err()
}

func loadLLDP(path string) ([]LLDPEntry, error) {
	var out []LLDPEntry
	err := scanJSONL(path, func(line int, text string) error {
		var e LLDPEntry
		if err := json.Unmarshal([]byte(text), &e); err != nil {
			return fmt.Errorf("%s 第 %d 行: %w", path, line, err)
		}
		out = append(out, e)
		return nil
	})
	return out, err
}

func loadSTP(path string) ([]STPEntry, error) {
	var out []STPEntry
	err := scanJSONL(path, func(line int, text string) error {
		var e STPEntry
		if err := json.Unmarshal([]byte(text), &e); err != nil {
			return fmt.Errorf("%s 第 %d 行: %w", path, line, err)
		}
		out = append(out, e)
		return nil
	})
	return out, err
}

func loadRoutes(path string) ([]RouteEntry, error) {
	var out []RouteEntry
	err := scanJSONL(path, func(line int, text string) error {
		var e RouteEntry
		if err := json.Unmarshal([]byte(text), &e); err != nil {
			return fmt.Errorf("%s 第 %d 行: %w", path, line, err)
		}
		out = append(out, e)
		return nil
	})
	return out, err
}

func loadMACs(path string) ([]MACEntry, error) {
	var out []MACEntry
	err := scanJSONL(path, func(line int, text string) error {
		var e MACEntry
		if err := json.Unmarshal([]byte(text), &e); err != nil {
			return fmt.Errorf("%s 第 %d 行: %w", path, line, err)
		}
		out = append(out, e)
		return nil
	})
	return out, err
}

// EventStream 是一条事件流。
//
// 为什么要分「流」而不是把文件拼成一个大数组：乱序是按流算的。
// 两份样本拼在一起数倒置，数出来的是文件的排列顺序，不是采集侧的真实乱序。
type EventStream struct {
	Path       string
	Raw        []RawEvent
	OutOfOrder int
}

// loadEventStream 读一条事件流，顺手把时间戳解成毫秒、校验来源名、数乱序。
func loadEventStream(path string) (EventStream, error) {
	stream := EventStream{Path: path}
	prevMax := int64(-1 << 62)
	err := scanJSONL(path, func(line int, text string) error {
		var e RawEvent
		if err := json.Unmarshal([]byte(text), &e); err != nil {
			return fmt.Errorf("%s 第 %d 行: %w", path, line, err)
		}
		tsMs, err := parseTS(e.TS)
		if err != nil {
			return fmt.Errorf("%s 第 %d 行: ts 不是 RFC3339: %w", path, line, err)
		}
		if _, ok := sourceKinds[e.Source]; !ok {
			return fmt.Errorf("%s 第 %d 行: 不认识的 source %q", path, line, e.Source)
		}
		// 迟到超过 1 秒就算倒置，不把同秒内的多源写入算成乱序。
		if tsMs < prevMax-1_000 {
			stream.OutOfOrder++
		} else if tsMs > prevMax {
			prevMax = tsMs
		}
		stream.Raw = append(stream.Raw, e)
		return nil
	})
	if err != nil {
		return EventStream{}, err
	}
	return stream, nil
}

// parseTS 只认 RFC3339。样本里的时间要能手读，不用 epoch。
func parseTS(s string) (int64, error) {
	ts, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return 0, err
	}
	return ts.UnixMilli(), nil
}
