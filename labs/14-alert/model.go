package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"
)

// 对象层级。抑制只允许从靠根的一侧压向靠叶的一侧。
const (
	KindSite    = "site"
	KindDevice  = "device"
	KindPort    = "port"
	KindHost    = "host"
	KindService = "service"
)

// 告警类型。父子抑制的类型白名单就建在这组枚举上。
const (
	ClassDeviceReset = "device_reset"
	ClassPortDown    = "port_down"
	ClassLinkFlap    = "link_flap"
	ClassProbeDown   = "probe_down"
	ClassHostDown    = "host_down"
	ClassSvcDown     = "svc_down"
	ClassCongestion  = "congestion"
	ClassLatencyHigh = "latency_high"
	ClassPacketLoss  = "packet_loss"
	ClassCpuHigh     = "cpu_high"
)

// 告警在样本里的真值。只用于算误伤，不参与抑制。
const (
	TruthRoot        = "root"
	TruthChild       = "child"
	TruthIndependent = "independent"
)

// 退化原因。
const (
	DegradeObjectUnknown = "object_unknown"
	DegradeNoAncestor    = "no_ancestor"
)

// explainTable 是抑制的类型白名单：左边这条告警能解释右边哪些告警。
//
// 这张表是策略，不是事实。它拦住的是最常见的一类错压——
// 拿一条「业务拥塞」的告警去压「设备重启」的告警：两条都红，都在同一片区域，
// 但拥塞解释不了重启。反过来 device_reset 能解释的是它下挂的一切。
//
// 表里没有的对，一律不压，宁可多报一条。
var explainTable = map[string]map[string]bool{
	ClassDeviceReset: setOf(ClassPortDown, ClassLinkFlap, ClassProbeDown,
		ClassHostDown, ClassSvcDown, ClassPacketLoss, ClassLatencyHigh),
	ClassPortDown: setOf(ClassLinkFlap, ClassProbeDown, ClassHostDown,
		ClassSvcDown, ClassPacketLoss, ClassLatencyHigh),
	ClassHostDown:   setOf(ClassSvcDown, ClassProbeDown),
	ClassCongestion: setOf(ClassLatencyHigh, ClassPacketLoss),
	ClassCpuHigh:    setOf(ClassLatencyHigh, ClassPacketLoss),
	ClassLinkFlap:   setOf(ClassPacketLoss, ClassLatencyHigh),
}

func setOf(items ...string) map[string]bool {
	m := make(map[string]bool, len(items))
	for _, it := range items {
		m[it] = true
	}
	return m
}

// explains 回答「父能不能解释子」。表里没写就是不能。
func explains(parentClass, childClass string) bool {
	sub, ok := explainTable[parentClass]
	if !ok {
		return false
	}
	return sub[childClass]
}

// Node 是对象树上的一个节点。
//
// Aliases 是唯一入口：告警里写什么名字都得先翻成 ID，翻不出来就走退化路径。
// 别名表缺一条不会报错，只会让这条告警变成「对象未知」——安静地错。
type Node struct {
	ID      string   `json:"id"`
	Kind    string   `json:"kind"`
	Parent  string   `json:"parent"`
	Name    string   `json:"name"`
	Aliases []string `json:"aliases"`
}

// Tree 是拓扑/父子关系的原始形态：一张边表。
type Tree struct {
	Nodes []Node `json:"nodes"`
}

// ObjectTree 是加载后的对象树：O(1) 查节点、查父、查祖先链。
type ObjectTree struct {
	nodes map[string]Node
	// alias 是「告警里的写法」→ 节点 ID。键统一小写去空格。
	alias map[string]string
	// children 只为报告里数影响面用。
	children map[string][]string
}

// Resolve 把告警里的对象写法翻成节点 ID。翻不出来返回空串。
func (t *ObjectTree) Resolve(raw string) (string, bool) {
	if raw == "" {
		return "", false
	}
	id, ok := t.alias[normKey(raw)]
	if !ok {
		return "", false
	}
	return id, true
}

// Get 取节点本身。
func (t *ObjectTree) Get(id string) (Node, bool) {
	n, ok := t.nodes[id]
	return n, ok
}

// Ancestors 返回从直接父到根的节点 ID 列表。
func (t *ObjectTree) Ancestors(id string) []string {
	var out []string
	seen := map[string]bool{id: true}
	cur := id
	for {
		n, ok := t.nodes[cur]
		if !ok || n.Parent == "" {
			return out
		}
		if seen[n.Parent] {
			// 环是脏数据的常态。截断，不 panic。
			return out
		}
		seen[n.Parent] = true
		cur = n.Parent
		out = append(out, cur)
	}
}

// IsAncestor 判断 a 是否是 b 的严格祖先。
//
// 同层与兄弟一律返回 false：同层的两条告警谁也不是谁的原因，
// 让它们互压就是把两个独立故障合成一个。
func (t *ObjectTree) IsAncestor(a, b string) bool {
	if a == "" || b == "" || a == b {
		return false
	}
	for _, anc := range t.Ancestors(b) {
		if anc == a {
			return true
		}
	}
	return false
}

// Path 返回从根到该节点的名字链，报告里用来交代「这条为什么被压」。
func (t *ObjectTree) Path(id string) string {
	chain := []string{}
	if n, ok := t.nodes[id]; ok {
		chain = append(chain, n.Name)
	}
	for _, anc := range t.Ancestors(id) {
		chain = append(chain, t.nodes[anc].Name)
	}
	// 反成根 → 叶。
	for i, j := 0, len(chain)-1; i < j; i, j = i+1, j-1 {
		chain[i], chain[j] = chain[j], chain[i]
	}
	return strings.Join(chain, " > ")
}

// HasParent 回答「这个节点在树里有没有父」。没有父的节点属于树上的孤岛。
func (t *ObjectTree) HasParent(id string) bool {
	n, ok := t.nodes[id]
	return ok && n.Parent != ""
}

// Rename 拆出对象树里的所有节点名，供测试与报告用。
func (t *ObjectTree) Names() []string {
	out := make([]string, 0, len(t.nodes))
	for id := range t.nodes {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// NewObjectTree 校验边表并建索引。
//
// 三个硬约束：ID 唯一、kind 合法、parent 指向的节点必须存在。
// 指向不存在的父，等同于父子关系缺失，必须在这里就拒收——
// 留到抑制阶段，它会变成一条「永远被压」的告警。
func NewObjectTree(tr Tree) (*ObjectTree, error) {
	t := &ObjectTree{
		nodes:    make(map[string]Node, len(tr.Nodes)),
		alias:    make(map[string]string, len(tr.Nodes)*3),
		children: make(map[string][]string, len(tr.Nodes)),
	}
	kinds := map[string]bool{
		KindSite: true, KindDevice: true, KindPort: true, KindHost: true, KindService: true,
	}
	for i, n := range tr.Nodes {
		n.ID = strings.TrimSpace(n.ID)
		if n.ID == "" {
			return nil, fmt.Errorf("第 %d 个节点 ID 为空", i+1)
		}
		if !kinds[n.Kind] {
			return nil, fmt.Errorf("节点 %s 的 kind %q 不认识（site/device/port/host/service）", n.ID, n.Kind)
		}
		if _, dup := t.nodes[n.ID]; dup {
			return nil, fmt.Errorf("节点 ID 重复：%s", n.ID)
		}
		if n.Name == "" {
			n.Name = n.ID
		}
		t.nodes[n.ID] = n
	}
	for _, n := range t.nodes {
		if n.Parent != "" {
			if _, ok := t.nodes[n.Parent]; !ok {
				return nil, fmt.Errorf("节点 %s 的父 %q 不存在（截断的拓扑，必须在样本里补全）", n.ID, n.Parent)
			}
			t.children[n.Parent] = append(t.children[n.Parent], n.ID)
		}
		for _, alias := range append([]string{n.Name, n.ID}, n.Aliases...) {
			key := normKey(alias)
			if key == "" {
				continue
			}
			if prev, dup := t.alias[key]; dup && prev != n.ID {
				return nil, fmt.Errorf("别名 %q 同时指向 %s 与 %s（别名是唯一入口，必须唯一）", alias, prev, n.ID)
			}
			t.alias[key] = n.ID
		}
	}
	for k := range t.children {
		sort.Strings(t.children[k])
	}
	return t, nil
}

// normKey 是别名比对的唯一规范化：小写、去首尾空格。
//
// 再多一步都不敢做。把 Ethernet1/1 猜成 Eth1/1 看着聪明，
// 一旦猜错就把两个口并成一个，后面每条结论都建在猜错的名字上。
// 别名表写全，别让代码猜。
func normKey(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

// Alert 是一条原始告警。
//
// ResolveTS 为空表示到样本结束还没恢复。Truth 是样本给的真值，
// 只用于算误伤数量，抑制逻辑里一个字都不读。
type Alert struct {
	ID        string `json:"id"`
	TS        string `json:"ts"`
	ResolveTS string `json:"resolve_ts,omitempty"`
	Source    string `json:"source"`
	Obj       string `json:"obj"`
	Class     string `json:"class"`
	Severity  string `json:"severity"`
	Summary   string `json:"summary"`
	Truth     string `json:"truth"`

	tsMs      int64
	resolveMs int64
	order     int
}

// loadJSON 读一份单个的 JSON 文件。
func loadJSON(path string, out any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, out)
}

func loadTree(path string) (*ObjectTree, error) {
	var tr Tree
	if err := loadJSON(path, &tr); err != nil {
		return nil, err
	}
	return NewObjectTree(tr)
}

// loadAlerts 读一份告警流（JSONL）。时间戳解析失败直接拒收，不猜。
func loadAlerts(path string) ([]Alert, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var out []Alert
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	line := 0
	for sc.Scan() {
		line++
		raw := strings.TrimSpace(sc.Text())
		if raw == "" || strings.HasPrefix(raw, "//") {
			continue
		}
		var a Alert
		if err := json.Unmarshal([]byte(raw), &a); err != nil {
			return nil, fmt.Errorf("%s 第 %d 行: %w", path, line, err)
		}
		ts, err := parseTS(a.TS)
		if err != nil {
			return nil, fmt.Errorf("%s 第 %d 行 ts=%q: %w", path, line, a.TS, err)
		}
		a.tsMs = ts
		if a.ResolveTS != "" {
			rts, err := parseTS(a.ResolveTS)
			if err != nil {
				return nil, fmt.Errorf("%s 第 %d 行 resolve_ts=%q: %w", path, line, a.ResolveTS, err)
			}
			if rts < ts {
				return nil, fmt.Errorf("%s 第 %d 行 %s 的 resolve_ts 早于 ts（恢复早于发生）", path, line, a.ID)
			}
			a.resolveMs = rts
		}
		if a.Truth == "" {
			a.Truth = TruthIndependent
		}
		switch a.Truth {
		case TruthRoot, TruthChild, TruthIndependent:
		default:
			return nil, fmt.Errorf("%s 第 %d 行 truth=%q 不认识（root/child/independent）", path, line, a.Truth)
		}
		a.order = len(out)
		out = append(out, a)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].tsMs != out[j].tsMs {
			return out[i].tsMs < out[j].tsMs
		}
		return out[i].order < out[j].order
	})
	return out, nil
}

func parseTS(s string) (int64, error) {
	t, err := time.Parse(time.RFC3339, strings.TrimSpace(s))
	if err != nil {
		return 0, err
	}
	return t.UnixMilli(), nil
}

func fmtTS(ms int64) string {
	return time.UnixMilli(ms).UTC().Format(time.RFC3339)
}
