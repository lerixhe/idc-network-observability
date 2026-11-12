package main

import (
	"fmt"
	"sort"
)

// Port 是采完一轮之后一个端口的样子。它有两个身份：
//
//	IfIndex —— 当轮的句柄，只在这一次会话里有效，设备重启就重排；
//	Name/Alias —— 端口的名字，跨重启有效，是时序数据的落点。
//
// 把这两个混成一个，就是本实验后半段要演的那个事故。
type Port struct {
	Device        string
	IfIndex       int
	Name          string
	Alias         string
	SpeedBps      uint64
	HighSpeedMbps uint64
	Admin         int
	Oper          int
	InOctets      uint64
	OutOctets     uint64
	InErrors      uint64
	OutErrors     uint64
}

// Key 是采集器写指标时用的主键：设备名 + ifName。
//
// 前缀设备名是必须的：每台设备的端口名都一样（Ethernet1/1），
// 不带设备名的键在库里会互相覆盖，而且覆盖得毫无声响。
func (p Port) Key() string { return p.Device + "/" + p.Name }

// SpeedMbps 取端口速率。优先 ifHighSpeed：ifSpeed 是 32 位、单位 bit/s，
// 10G 就顶到 4294967295 了，100G 口读出来和 10G 一个数。
func (p Port) SpeedMbps() uint64 {
	if p.HighSpeedMbps > 0 {
		return p.HighSpeedMbps
	}
	return p.SpeedBps / 1_000_000
}

// PortFromRow 把一行 varbind 转成 Port。缺列时字段是零值——
// 某个列遍历失败，这一格就是空的，报表上要看得出那是「没采到」而不是「值为 0」。
func PortFromRow(device string, row *TableRow) Port {
	p := Port{Device: device, IfIndex: int(row.Index)}
	if v, ok := row.Get("ifName"); ok {
		p.Name = v.Str
	}
	if v, ok := row.Get("ifAlias"); ok {
		p.Alias = v.Str
	}
	if v, ok := row.Get("ifSpeed"); ok {
		p.SpeedBps = v.Num
	}
	if v, ok := row.Get("ifHighSpeed"); ok {
		p.HighSpeedMbps = v.Num
	}
	if v, ok := row.Get("ifAdminStatus"); ok {
		p.Admin = int(v.Num)
	}
	if v, ok := row.Get("ifOperStatus"); ok {
		p.Oper = int(v.Num)
	}
	if v, ok := row.Get("ifHCInOctets"); ok {
		p.InOctets = v.Num
	}
	if v, ok := row.Get("ifHCOutOctets"); ok {
		p.OutOctets = v.Num
	}
	if v, ok := row.Get("ifInErrors"); ok {
		p.InErrors = v.Num
	}
	if v, ok := row.Get("ifOutErrors"); ok {
		p.OutErrors = v.Num
	}
	return p
}

// PortsFromRows 按 ifIndex 顺序把行转成端口列表。
func PortsFromRows(device string, rows []*TableRow) []Port {
	out := make([]Port, 0, len(rows))
	for _, r := range rows {
		out = append(out, PortFromRow(device, r))
	}
	return out
}

// PortTable 是一台设备某一轮的端口表，带三种取法：
// 按 ifIndex（当轮句柄）、按 ifName（主键）、按 ifAlias（兜底）。
type PortTable struct {
	Device  string
	Ports   []Port
	byIndex map[int]int
	byName  map[string]int
	byAlias map[string]int
	// DupAlias 是 alias 重复的个数。alias 是人手填的，
	// 「to-core」这种写法在一台设备上出现三次很正常，所以它只能是兜底，不能是主键。
	DupAlias int
}

// NewPortTable 建索引。ifName 为空的行不进 byName——没有名字的端口
// 只能靠 ifIndex 找，那意味着这行数据的键本身就有问题。
func NewPortTable(device string, ports []Port) *PortTable {
	t := &PortTable{
		Device:  device,
		Ports:   ports,
		byIndex: make(map[int]int, len(ports)),
		byName:  make(map[string]int, len(ports)),
		byAlias: make(map[string]int, len(ports)),
	}
	for i, p := range ports {
		t.byIndex[p.IfIndex] = i
		if p.Name != "" {
			t.byName[p.Name] = i
		}
		if p.Alias != "" {
			if _, dup := t.byAlias[p.Alias]; dup {
				t.DupAlias++
				continue
			}
			t.byAlias[p.Alias] = i
		}
	}
	sort.Slice(t.Ports, func(i, j int) bool { return t.Ports[i].IfIndex < t.Ports[j].IfIndex })
	return t
}

// Len 是端口数。
func (t *PortTable) Len() int { return len(t.Ports) }

// ByIndex 按 ifIndex 取。
func (t *PortTable) ByIndex(idx int) (Port, bool) {
	i, ok := t.byIndex[idx]
	if !ok {
		return Port{}, false
	}
	return t.Ports[i], true
}

// ByName 按 ifName 取。
func (t *PortTable) ByName(name string) (Port, bool) {
	i, ok := t.byName[name]
	if !ok {
		return Port{}, false
	}
	return t.Ports[i], true
}

// ByAlias 按 ifAlias 取。
func (t *PortTable) ByAlias(alias string) (Port, bool) {
	i, ok := t.byAlias[alias]
	if !ok || alias == "" {
		return Port{}, false
	}
	return t.Ports[i], true
}

// Unnamed 是没有 alias 的端口数。这类端口一旦 ifName 也变了，
// 就只剩 ifIndex 一条线索——而 ifIndex 正是最不能信的。
func (t *PortTable) Unnamed() int {
	n := 0
	for _, p := range t.Ports {
		if p.Alias == "" {
			n++
		}
	}
	return n
}

// Anonymous 是没有 ifName 的行数。ifName 列没采到时全表都是空：
// 行拼得出来，主键拼不出来。报表必须把这件事说出来，否则这份数据会被当成正常。
func (t *PortTable) Anonymous() int {
	n := 0
	for _, p := range t.Ports {
		if p.Name == "" {
			n++
		}
	}
	return n
}

// JoinMode 决定两轮数据按什么键对接。
type JoinMode string

const (
	JoinByIndex JoinMode = "index"
	JoinByName  JoinMode = "name"
	JoinByAlias JoinMode = "alias"
)

// JoinPair 是接上的一对。
type JoinPair struct {
	Key    string
	Before Port
	After  Port
}

// JoinReport 是两轮数据对接的结果。三个数字分别对应三种错法：
// 接错对象（Wrong）、历史断线（Dropped）、凭空多出来（Fresh）。
type JoinReport struct {
	Mode    JoinMode
	Pairs   []JoinPair
	Dropped []Port // 上一轮有、这一轮没有
	Fresh   []Port // 这一轮有、上一轮没有
}

// JoinPorts 按指定键把 two 轮端口表接起来。
//
// 生产上这一步就是「把新采到的点写进时序库」：键选对了，曲线连续；
// 键选错了，曲线也连续——只是接在别的端口身上。这里把两种键都跑一遍，
// 差别写在报表里。
func JoinPorts(before, after *PortTable, mode JoinMode) JoinReport {
	rep := JoinReport{Mode: mode}
	matched := make(map[string]bool, len(before.Ports))
	for _, b := range before.Ports {
		key, ok := keyOf(b, mode)
		if !ok {
			rep.Dropped = append(rep.Dropped, b)
			continue
		}
		var a Port
		switch mode {
		case JoinByIndex:
			a, ok = after.ByIndex(b.IfIndex)
		case JoinByName:
			a, ok = after.ByName(b.Name)
		case JoinByAlias:
			a, ok = after.ByAlias(b.Alias)
		}
		if !ok {
			rep.Dropped = append(rep.Dropped, b)
			continue
		}
		matched[key] = true
		rep.Pairs = append(rep.Pairs, JoinPair{Key: key, Before: b, After: a})
	}
	for _, a := range after.Ports {
		key, ok := keyOf(a, mode)
		if !ok {
			rep.Fresh = append(rep.Fresh, a)
			continue
		}
		if !matched[key] {
			rep.Fresh = append(rep.Fresh, a)
		}
	}
	sort.Slice(rep.Pairs, func(i, j int) bool {
		if rep.Pairs[i].After.Device != rep.Pairs[j].After.Device {
			return rep.Pairs[i].After.Device < rep.Pairs[j].After.Device
		}
		return rep.Pairs[i].After.IfIndex < rep.Pairs[j].After.IfIndex
	})
	return rep
}

func keyOf(p Port, mode JoinMode) (string, bool) {
	switch mode {
	case JoinByIndex:
		return fmt.Sprintf("%s#%d", p.Device, p.IfIndex), true
	case JoinByName:
		if p.Name == "" {
			return "", false
		}
		return p.Device + "/" + p.Name, true
	case JoinByAlias:
		if p.Alias == "" {
			return "", false
		}
		return p.Device + "/" + p.Alias, true
	}
	return "", false
}

// WrongObjects 是「接上了，但不是同一个端口」的条数。
//
// 判据用 alias：它描述这根线通到哪，是端口最稳的标签。两轮 alias 不同，
// 说明这个键在新的一轮指向了别的端口，历史曲线已经接错对象。
func (r JoinReport) WrongObjects() int {
	n := 0
	for _, p := range r.Pairs {
		if p.Before.Alias != p.After.Alias {
			n++
		}
	}
	return n
}

// BigJumps 返回速率差 ≥ minRatio 倍的那些对接。口径跳变本身不是错，
// 但「同一个键上的端口速率从 10G 变 100G」是错接的强信号——现实里没人
// 给端口换介质还能保持同一个名字。
func (r JoinReport) BigJumps(minRatio float64) []JoinPair {
	var out []JoinPair
	for _, p := range r.Pairs {
		a, b := float64(p.Before.SpeedMbps()), float64(p.After.SpeedMbps())
		if a <= 0 || b <= 0 {
			continue
		}
		ratio := b / a
		if ratio < 1 {
			ratio = 1 / ratio
		}
		if ratio >= minRatio {
			out = append(out, p)
		}
	}
	return out
}

// Reindex 是一处「同一个 ifIndex 现在指向另一个端口」。
type Reindex struct {
	Device     string
	IfIndex    int
	Before     Port
	After      Port
	SpeedRatio float64
}

// Reindexes 列出两轮之间 ifIndex 换了对象的全部位置，按设备名 + 索引排序。
func Reindexes(before, after *PortTable) []Reindex {
	rep := JoinPorts(before, after, JoinByIndex)
	var out []Reindex
	for _, p := range rep.Pairs {
		if p.Before.Alias == p.After.Alias {
			continue
		}
		r := Reindex{Device: p.After.Device, IfIndex: p.After.IfIndex, Before: p.Before, After: p.After}
		a, b := float64(p.Before.SpeedMbps()), float64(p.After.SpeedMbps())
		if a > 0 && b > 0 {
			ratio := b / a
			if ratio < 1 {
				ratio = 1 / ratio
			}
			r.SpeedRatio = ratio
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Device != out[j].Device {
			return out[i].Device < out[j].Device
		}
		return out[i].IfIndex < out[j].IfIndex
	})
	return out
}
