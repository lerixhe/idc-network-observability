// 实验 03：探测矩阵与健康评分。
//
// 这个文件只管拓扑：节点、链路、从拓扑上推出来的路径。
// 探测规则在 matrix.go，评分与归因在 score.go，剧本与对账在 scenario.go。
package main

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"os"
	"sort"
)

// 节点角色。层级（tier）由角色推出来，样本里不再写一遍——
// 同一件事两个真相源，迟早对不上。
const (
	roleBorder = "border"
	roleSpine  = "spine"
	roleTOR    = "tor"
	roleServer = "server"
)

// roleTier：数字越小越靠上。上行 = 目标的 tier 更小。
var roleTier = map[string]int{
	roleBorder: 1,
	roleSpine:  2,
	roleTOR:    3,
	roleServer: 4,
}

// Node 是拓扑里的一个节点。Rack 只有 tor 与 server 有：TOR 和它下挂的
// 服务器同属一个机架，这条归属是「同机架不探」的唯一依据。
//
// 没有地址字段：这个实验纯本地计算，一个包都不发。地址归采集器管（00-setup）。
type Node struct {
	Name string `json:"name"`
	Role string `json:"role"`
	Pod  string `json:"pod"`
	Rack string `json:"rack"`
}

// Uplink 是一条物理链路。接入链路（server ↔ tor）也要写进来：
// 覆盖率的分母得算全，漏掉接入链路，百分比好看，实际漏了半个网络。
type Uplink struct {
	From   string `json:"from"`
	To     string `json:"to"`
	BWGbps int    `json:"bw_gbps"`
}

// Link 是一条无序链路：A↔B 与 B↔A 是同一条。
type Link struct {
	A, B   string
	BWGbps int
}

// linkSep 是链路 key 的分隔符。用字符而不是字符串，是为了让表格里一眼看出
// 「这是链路」而不是「这是箭头」——探测对用 →，链路用 ↔。
const linkSep = "↔"

// linkKey 把两端名排序后拼起来，保证同一根链路只有一个 key。
func linkKey(a, b string) string {
	if a > b {
		a, b = b, a
	}
	return a + linkSep + b
}

// Topology 是加载后的拓扑，建好索引就能查。
type Topology struct {
	Site    string   `json:"site"`
	Nodes   []Node   `json:"nodes"`
	Uplinks []Uplink `json:"uplinks"`

	byName    map[string]Node
	byRole    map[string][]string
	torOfRack map[string]string
	links     []Link
	adj       map[string][]string
}

// LoadTopology 读样本并校验。校验不通过就报错，不做「尽量跑」——
// 拓扑错一条链路，后面所有路径、覆盖率、归因全跟着错。
func LoadTopology(path string) (*Topology, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var t Topology
	if err := json.Unmarshal(data, &t); err != nil {
		return nil, fmt.Errorf("解析 %s：%w", path, err)
	}
	if err := t.build(); err != nil {
		return nil, fmt.Errorf("%s：%w", path, err)
	}
	return &t, nil
}

func (t *Topology) build() error {
	if len(t.Nodes) == 0 {
		return fmt.Errorf("节点表是空的")
	}
	t.byName = make(map[string]Node, len(t.Nodes))
	t.byRole = make(map[string][]string, len(roleTier))
	t.torOfRack = make(map[string]string)

	for _, n := range t.Nodes {
		if _, dup := t.byName[n.Name]; dup {
			return fmt.Errorf("节点重名：%s", n.Name)
		}
		if _, ok := roleTier[n.Role]; !ok {
			return fmt.Errorf("节点 %s 的角色 %q 不认识（border / spine / tor / server）", n.Name, n.Role)
		}
		if n.Role == roleTOR || n.Role == roleServer {
			if n.Rack == "" {
				return fmt.Errorf("节点 %s 是 %s，必须写 rack", n.Name, n.Role)
			}
		}
		if n.Role == roleTOR {
			if prev, dup := t.torOfRack[n.Rack]; dup {
				return fmt.Errorf("机架 %s 有两台 TOR：%s 和 %s", n.Rack, n.Name, prev)
			}
			t.torOfRack[n.Rack] = n.Name
		}
		t.byName[n.Name] = n
		t.byRole[n.Role] = append(t.byRole[n.Role], n.Name)
	}

	// 每个有 server 的机架必须有 TOR，否则算不出接入链路。
	for _, n := range t.Nodes {
		if n.Role == roleServer && t.torOfRack[n.Rack] == "" {
			return fmt.Errorf("服务器 %s 所在机架 %s 没有 TOR", n.Name, n.Rack)
		}
	}

	t.adj = make(map[string][]string, len(t.Nodes))
	seen := make(map[string]bool, len(t.Uplinks))
	for _, u := range t.Uplinks {
		a, ok := t.byName[u.From]
		if !ok {
			return fmt.Errorf("链路 %s↔%s：端点 %s 不在节点表里", u.From, u.To, u.From)
		}
		b, ok := t.byName[u.To]
		if !ok {
			return fmt.Errorf("链路 %s↔%s：端点 %s 不在节点表里", u.From, u.To, u.To)
		}
		// 只允许相邻层直连。server 直连 spine 这类跨层连接一出现，
		// 说明样本抄错了，早点拦下来。
		if d := roleTier[a.Role] - roleTier[b.Role]; d != 1 && d != -1 {
			return fmt.Errorf("链路 %s(%s)↔%s(%s) 跨了不止一层", a.Name, a.Role, b.Name, b.Role)
		}
		k := linkKey(a.Name, b.Name)
		if seen[k] {
			return fmt.Errorf("链路重复：%s", k)
		}
		seen[k] = true
		t.links = append(t.links, Link{A: a.Name, B: b.Name, BWGbps: u.BWGbps})
		t.adj[a.Name] = append(t.adj[a.Name], b.Name)
		t.adj[b.Name] = append(t.adj[b.Name], a.Name)
	}
	if len(t.links) == 0 {
		return fmt.Errorf("链路表是空的")
	}

	// 服务器必须连到本机架的 TOR。接错机架，接入链路就挂到别人头上了。
	// 这条校验放在孤岛检查前面：一个孤立的 TOR 往往是接入链路漏写的副作用，
	// 「srv-1 没连到本机架 TOR」比「tor-1 没有连任何链路」更能指向错在哪。
	for _, name := range t.byRole[roleServer] {
		s := t.byName[name]
		tor := t.torOfRack[s.Rack]
		if !seen[linkKey(name, tor)] {
			return fmt.Errorf("服务器 %s（机架 %s）没连到本机架 TOR %s", name, s.Rack, tor)
		}
	}
	for _, n := range t.Nodes {
		if len(t.adj[n.Name]) == 0 {
			return fmt.Errorf("节点 %s 没有连任何链路", n.Name)
		}
	}

	// 邻居表排序，让 BFS 的展开在相同输入下稳定；真正的选路由哈希决定。
	for name := range t.adj {
		sort.Strings(t.adj[name])
	}
	return nil
}

// Links 返回全部链路，顺序同样本。
func (t *Topology) Links() []Link { return t.links }

// pseudoUnit 把任意字符串映射到 [0,1)。用它代替 math/rand：
// 同一个输入永远给同一个数，样本跑一百遍结果一样，测试才写得住。
//
// FNV 过一遍 splitmix64 的收尾混合才敢当随机数用：FNV 的雪崩性不够，
// 相邻输入（"pair|0"、"pair|1"）的输出高位几乎一样，直接取高位会让一串
// 逐包判定全落在同一个值附近——一个 12% 的丢包链路能算出 83% 的丢包率。
func pseudoUnit(seed string) float64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(seed))
	x := h.Sum64()
	x ^= x >> 30
	x *= 0xbf58476d1ce4e5b9
	x ^= x >> 27
	x *= 0x94d049bb133111eb
	x ^= x >> 31
	return float64(x>>11) / float64(uint64(1)<<53)
}

// Path 返回一对探测在拓扑上跨过的链路 key 序列，从 src 到 dst。
//
// 这里用 BFS 求最短路径。真实网络里同一个源到同一个目的走哪条上行，
// 由五元组哈希决定；这里把邻居的展开顺序按名字哈希排一遍，
// 等价于「每条对固定捆在一条哈希路径上」。第 12 课会说明这条假设
// 怎么让探测说谎：另一条等价路径坏了，覆盖曲线一点反应都没有。
func (t *Topology) Path(src, dst string) ([]string, error) {
	if _, ok := t.byName[src]; !ok {
		return nil, fmt.Errorf("路径端点 %s 不在拓扑里", src)
	}
	if _, ok := t.byName[dst]; !ok {
		return nil, fmt.Errorf("路径端点 %s 不在拓扑里", dst)
	}
	if src == dst {
		return nil, fmt.Errorf("路径两端是同一个节点：%s", src)
	}

	seed := src + "→" + dst
	prev := map[string]string{src: ""}
	queue := []string{src}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if cur == dst {
			break
		}
		nbs := append([]string(nil), t.adj[cur]...)
		sort.Slice(nbs, func(i, j int) bool {
			return pseudoUnit(seed+"|"+nbs[i]) < pseudoUnit(seed+"|"+nbs[j])
		})
		for _, nb := range nbs {
			if _, seen := prev[nb]; seen {
				continue
			}
			prev[nb] = cur
			queue = append(queue, nb)
		}
	}
	if _, ok := prev[dst]; !ok {
		return nil, fmt.Errorf("%s 到 %s 在拓扑上不通", src, dst)
	}

	nodes := []string{dst}
	for cur := prev[dst]; cur != src; cur = prev[cur] {
		nodes = append(nodes, cur)
	}
	nodes = append(nodes, src)
	for i, j := 0, len(nodes)-1; i < j; i, j = i+1, j-1 {
		nodes[i], nodes[j] = nodes[j], nodes[i]
	}

	keys := make([]string, 0, len(nodes)-1)
	for i := 0; i+1 < len(nodes); i++ {
		keys = append(keys, linkKey(nodes[i], nodes[i+1]))
	}
	return keys, nil
}
