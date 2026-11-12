package main

// probePacketBytes 是一个探测包的线上长度：
// 以太网头 14 + IP 头 20 + UDP 头 8 + 载荷 32 = 74 字节。
// 载荷 32 字节够塞序列号与时间戳，再大就是给设备添负担。
const probePacketBytes = 74

// aggWindowS 是聚合窗口：60 秒一条记录，和「丢包率按分钟看」的口径对齐。
const aggWindowS = 60

// 一条记录落成 JSONL 的编码长度，用来估存储。数字来自本实验 out/ 的实测值取整，
// 不是拍脑袋——换个字段格式就要重算。
const (
	aggRecordBytes = 130
	rawRecordBytes = 90
)

// tierWeight 是「谁探谁」的抽样比例。表里只写横向与下行组合：
// 目标层级比源小就是上行，一律必探（1.0），不看这张表。
//
// server 探 server 写 1.0：同机架已经被排掉，剩下的跨机架对就是那张全网状的实体。
// 这一格一抽样，端到端覆盖率立刻掉一半。
// 同层之间不列就是 0：spine 与 spine、border 与 border 都没有直连，
// 探出来的路径要绕一层再绕回来，测到的不是真实转发路径。
var tierWeight = map[string]map[string]float64{
	roleServer: {roleTOR: 0.50, roleServer: 1.0},
	roleTOR:    {roleTOR: 0.10, roleServer: 0.10},
	roleSpine:  {roleTOR: 0.25, roleServer: 0.05},
	roleBorder: {roleSpine: 0.25, roleTOR: 0.25, roleServer: 0.05},
}

// isUpstream 判「这条探测是不是从下往上」。上行方向必探：
// TOR 的上行链路只有从下边探才看得见。
//
// server 探 TOR 是个例外：只有探本机架的 TOR 才算上行，而那条会被同机架规则
// 先拦下来；探别的机架的 TOR 是横向流量，按表抽一半。
func isUpstream(src, dst Node) bool {
	if roleTier[dst.Role] >= roleTier[src.Role] {
		return false
	}
	return !(src.Role == roleServer && dst.Role == roleTOR)
}

// probeWeight 返回 src 探 dst 的抽样比例，0 表示不探。
//
// 规则顺序不能换。同机架不探压过一切：同机架的流量根本不过上行链路，
// 探它测到的是本机网卡和 TOR 的本地转发，那是主机侧观测的活（第 44 课）。
// 上行必探排第二：TOR 的上行链路只有从服务器往上探才看得见，
// 服务器探自己机架的 TOR 又被否决了，这条上行是唯一出口。
func probeWeight(src, dst Node) float64 {
	if src.Name == dst.Name {
		return 0
	}
	if src.Rack != "" && src.Rack == dst.Rack {
		return 0
	}
	if isUpstream(src, dst) {
		return 1
	}
	return tierWeight[src.Role][dst.Role]
}

// sampleHit 按比例抽样。同一个 (src,dst) 每次都得到同一个结果——
// 探测矩阵明天再生成一遍不能变成另一套，否则覆盖率曲线就是在画噪声。
func sampleHit(src, dst string, ratio float64) bool {
	if ratio <= 0 {
		return false
	}
	if ratio >= 1 {
		return true
	}
	return pseudoUnit(src+"→"+dst) < ratio
}

// ProbePair 是一条探测任务：谁探谁、跨过哪些链路。
type ProbePair struct {
	Src    string   `json:"src"`
	Dst    string   `json:"dst"`
	Weight float64  `json:"weight"`
	Hops   int      `json:"hops"`
	Path   []string `json:"path"`
}

// Cost 是一份矩阵的代价。三个口径都要算，因为它们量级差得远：
// 带宽几乎可以忽略，包速率压设备 CPU，存储才是账单（第 35 课）。
type Cost struct {
	IntervalS      int     `json:"interval_s"`
	PacketBytes    int     `json:"packet_bytes"`
	Pairs          int     `json:"pairs"`
	Pps            float64 `json:"pps"`
	KbitPerS       float64 `json:"kbit_per_s"`
	TotalBWGbps    float64 `json:"total_bw_gbps"`
	BWSharePPM     float64 `json:"bw_share_ppm"`
	RecordsPerDay  float64 `json:"records_per_day"`
	AggBytesPerDay float64 `json:"agg_bytes_per_day"`
	RawBytesPerDay float64 `json:"raw_bytes_per_day"`
}

// Matrix 是生成结果加覆盖率与成本。
type Matrix struct {
	Site           string      `json:"site"`
	Nodes          []string    `json:"nodes"`
	Pairs          []ProbePair `json:"pairs"`
	OrderedPairs   int         `json:"ordered_pairs"`
	NodeTotal      int         `json:"node_total"`
	NodeCovered    int         `json:"node_covered"`
	LinkTotal      int         `json:"link_total"`
	LinkCovered    int         `json:"link_covered"`
	UncoveredLinks []string    `json:"uncovered_links,omitempty"`
	Cost           Cost        `json:"cost"`
}

// PairRate 是节点对覆盖率：选中的对数 / 全量有序对数。
func (m *Matrix) PairRate() float64 {
	if m.OrderedPairs == 0 {
		return 0
	}
	return float64(len(m.Pairs)) / float64(m.OrderedPairs)
}

// LinkRate 是链路覆盖率：被至少一条探测路径跨过的链路数 / 总链路数。
//
// 这是两个覆盖率里更要紧的那个。节点对覆盖率回答「探了多少对」，
// 链路覆盖率回答「有没有整条链路从来没被跨过」——后者直接决定第 10 课
// 的举证包能自证到什么程度。
func (m *Matrix) LinkRate() float64 {
	if m.LinkTotal == 0 {
		return 0
	}
	return float64(m.LinkCovered) / float64(m.LinkTotal)
}

// NodeRate 是节点覆盖率：出现在某条探测对里的节点比例。
func (m *Matrix) NodeRate() float64 {
	if m.NodeTotal == 0 {
		return 0
	}
	return float64(m.NodeCovered) / float64(m.NodeTotal)
}

// GenerateMatrix 把拓扑翻成探测矩阵。
//
// N² 两两探测在这个规模上还好算，十万台就是 10^10 对，一秒一个包也是
// 10^10 pps，没有设备扛得住。规则的作用就是把 N² 压到可付的数量级。
func GenerateMatrix(t *Topology, intervalS int) (*Matrix, error) {
	m := &Matrix{
		Site:      t.Site,
		Nodes:     make([]string, 0, len(t.Nodes)),
		NodeTotal: len(t.Nodes),
		LinkTotal: len(t.links),
	}
	for _, n := range t.Nodes {
		m.Nodes = append(m.Nodes, n.Name)
	}

	coveredLinks := make(map[string]bool, len(t.links))
	coveredNodes := make(map[string]bool, len(t.Nodes))
	for _, src := range t.Nodes {
		for _, dst := range t.Nodes {
			if src.Name == dst.Name {
				continue
			}
			m.OrderedPairs++
			w := probeWeight(src, dst)
			if !sampleHit(src.Name, dst.Name, w) {
				continue
			}
			path, err := t.Path(src.Name, dst.Name)
			if err != nil {
				return nil, err
			}
			m.Pairs = append(m.Pairs, ProbePair{
				Src:    src.Name,
				Dst:    dst.Name,
				Weight: w,
				Hops:   len(path),
				Path:   path,
			})
			coveredNodes[src.Name] = true
			coveredNodes[dst.Name] = true
			for _, k := range path {
				coveredLinks[k] = true
			}
		}
	}
	m.NodeCovered = len(coveredNodes)
	m.LinkCovered = len(coveredLinks)
	for _, l := range t.links {
		if !coveredLinks[linkKey(l.A, l.B)] {
			m.UncoveredLinks = append(m.UncoveredLinks, linkKey(l.A, l.B))
		}
	}
	m.Cost = estimateCost(t, len(m.Pairs), intervalS)
	return m, nil
}

// estimateCost 算包速率、带宽与存储。
//
// 三个算式都能自己代一遍：
//
//	pps     = 对数 / 间隔秒数
//	kbit/s  = pps × 74 字节 × 8 / 1000
//	带宽占比 = 探测 kbit/s ÷ 拓扑总带宽（双向都算）
//	记录数/天 = 对数 × 86400 ÷ 60
//	存储/天 = 记录数 × 单条字节数
func estimateCost(t *Topology, pairs, intervalS int) Cost {
	if intervalS < 1 {
		intervalS = 1
	}
	c := Cost{IntervalS: intervalS, PacketBytes: probePacketBytes, Pairs: pairs}
	c.Pps = float64(pairs) / float64(intervalS)
	c.KbitPerS = c.Pps * probePacketBytes * 8 / 1000

	for _, l := range t.links {
		c.TotalBWGbps += float64(l.BWGbps)
	}
	if c.TotalBWGbps > 0 {
		// 探测占用的比例，用百万分比表示：百分比在这个量级上打印出来全是 0.0000%。
		c.BWSharePPM = c.KbitPerS * 1000 / (c.TotalBWGbps * 1e9) * 1e6
	}

	c.RecordsPerDay = float64(pairs) * 86400 / aggWindowS
	c.AggBytesPerDay = c.RecordsPerDay * aggRecordBytes
	c.RawBytesPerDay = c.Pps * 86400 * rawRecordBytes
	return c
}

// hopEstimate 是按跳数估的基线时延，仿真用。每跳 22 µs，两端各 30 µs 固定开销：
// 同机架跨 TOR 四跳 ≈ 118 µs，跨 pod 六跳 ≈ 162 µs，都在 IDC 实测区间里。
func hopEstimate(hops int) int64 {
	return int64(30 + 22*hops)
}
