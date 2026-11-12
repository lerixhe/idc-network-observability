package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
)

// 注入的事件类型。四类覆盖了「探测失败」背后的四种真相：
// 机器死了、agent 死了、只有接收方向不通、路径上有条链路在丢包。
const (
	eventHostDown  = "host_down"
	eventAgentDown = "probe_agent_down"
	eventNICRxDown = "nic_rx_down"
	eventLinkLoss  = "link_loss"
)

// Incident 是一次注入的事件。判定层看不到这一段，
// 真相只在最后对账时露一次面。
type Incident struct {
	Kind      string     `json:"kind"`
	Nodes     []string   `json:"nodes"`
	Links     [][]string `json:"links"`
	Loss      float64    `json:"loss"`
	LatencyUs int64      `json:"latency_us"`
	Note      string     `json:"note"`
}

// Scenario 是一份剧本：拓扑之外的所有输入。窗口与间隔放在样本里，
// 因为它们同时决定「每对发几个包」和成本估算，写在两处迟早对不上。
type Scenario struct {
	WindowStart string     `json:"window_start"`
	WindowS     int        `json:"window_s"`
	IntervalS   int        `json:"interval_s"`
	Incidents   []Incident `json:"incidents"`
}

// LoadScenario 读剧本并做结构校验。节点与链路是否存在交给 Simulate 验，
// 那里才拿得到拓扑。
func LoadScenario(path string) (Scenario, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Scenario{}, err
	}
	var sc Scenario
	if err := json.Unmarshal(data, &sc); err != nil {
		return Scenario{}, fmt.Errorf("解析 %s：%w", path, err)
	}
	if sc.WindowStart == "" {
		return Scenario{}, fmt.Errorf("%s：缺 window_start", path)
	}
	if sc.WindowS <= 0 || sc.IntervalS <= 0 {
		return Scenario{}, fmt.Errorf("%s：window_s 与 interval_s 必须为正", path)
	}
	if sc.WindowS < sc.IntervalS {
		return Scenario{}, fmt.Errorf("%s：窗口 %ds 短于探测间隔 %ds，一个包都发不出去", path, sc.WindowS, sc.IntervalS)
	}
	if len(sc.Incidents) == 0 {
		return Scenario{}, fmt.Errorf("%s：没有注入任何事件", path)
	}
	for i, in := range sc.Incidents {
		switch in.Kind {
		case eventHostDown, eventAgentDown, eventNICRxDown:
			if len(in.Nodes) == 0 {
				return Scenario{}, fmt.Errorf("%s：第 %d 条 %s 没写 nodes", path, i+1, in.Kind)
			}
		case eventLinkLoss:
			if len(in.Links) == 0 {
				return Scenario{}, fmt.Errorf("%s：第 %d 条 link_loss 没写 links", path, i+1)
			}
			for _, pair := range in.Links {
				if len(pair) != 2 {
					return Scenario{}, fmt.Errorf("%s：第 %d 条 link_loss 的 links 要写成 [[A,B]]", path, i+1)
				}
			}
			if in.Loss <= 0 || in.Loss >= 1 {
				return Scenario{}, fmt.Errorf("%s：第 %d 条 link_loss 的 loss 要在 (0,1) 之间", path, i+1)
			}
		default:
			return Scenario{}, fmt.Errorf("%s：第 %d 条事件类型 %q 不认识", path, i+1, in.Kind)
		}
	}
	return sc, nil
}

// Truth 是注入的真相。生产上你永远拿不到这一段，所以它只用来对账，
// 不许反向喂给判定层——喂了就等于开卷考试。
type Truth struct {
	HostDown  map[string]bool
	AgentDown map[string]bool
	NICRxDown map[string]bool
	LinkLoss  map[string]bool
}

// Simulate 跑一轮探测仿真：按剧本决定哪些对丢包、哪些对没有记录。
//
// 它是整本书里唯一「发」包的地方，而且一个真包都不发——全部算出来的。
// 这样样本可复现，测试才能写死断言。
func Simulate(t *Topology, m *Matrix, sc Scenario) ([]ProbeResult, Truth, error) {
	sent := sc.WindowS / sc.IntervalS
	tr := Truth{
		HostDown:  map[string]bool{},
		AgentDown: map[string]bool{},
		NICRxDown: map[string]bool{},
		LinkLoss:  map[string]bool{},
	}
	latency := map[string]int64{}
	lossRate := map[string]float64{}

	for _, in := range sc.Incidents {
		switch in.Kind {
		case eventHostDown, eventAgentDown, eventNICRxDown:
			for _, n := range in.Nodes {
				if _, ok := t.byName[n]; !ok {
					return nil, Truth{}, fmt.Errorf("事件里的节点 %s 不在拓扑里", n)
				}
				switch in.Kind {
				case eventHostDown:
					tr.HostDown[n] = true
				case eventAgentDown:
					tr.AgentDown[n] = true
				default:
					tr.NICRxDown[n] = true
				}
			}
		case eventLinkLoss:
			for _, pair := range in.Links {
				k := linkKey(pair[0], pair[1])
				found := false
				for _, l := range t.links {
					if linkKey(l.A, l.B) == k {
						found = true
						break
					}
				}
				if !found {
					return nil, Truth{}, fmt.Errorf("事件里的链路 %s 不在拓扑里", k)
				}
				tr.LinkLoss[k] = true
				lossRate[k] = in.Loss
				latency[k] = in.LatencyUs
			}
		}
	}

	out := make([]ProbeResult, 0, len(m.Pairs))
	for _, p := range m.Pairs {
		// 探针没上报：整机不可达和 agent 挂掉在矩阵里长得一样，
		// 区别要靠它作为目标时别人探不探得到。
		if tr.HostDown[p.Src] || tr.AgentDown[p.Src] {
			continue
		}
		seed := pairKey(p.Src, p.Dst)
		baseline := hopEstimate(p.Hops)

		loss := 0.0
		extra := int64(0)
		if tr.HostDown[p.Dst] || tr.NICRxDown[p.Dst] {
			loss = 1
		}
		for _, k := range p.Path {
			if v, hit := lossRate[k]; hit {
				if v > loss {
					loss = v
				}
				if latency[k] > extra {
					extra = latency[k]
				}
			}
		}

		// 逐包判定，丢包数才是离散的：60 个包里 25% 的丢包率会落在 12~19 个之间，
		// 直接按期望值 round 一下就永远是 15，看不出真实观测的抖动。
		lost := 0
		for i := 0; i < sent; i++ {
			if pseudoUnit(fmt.Sprintf("%s|%d", seed, i)) < loss {
				lost++
			}
		}
		recv := sent - lost
		if recv < 0 {
			recv = 0
		}

		r := ProbeResult{
			TS:     sc.WindowStart,
			Src:    p.Src,
			Dst:    p.Dst,
			Sent:   sent,
			Recv:   recv,
			Baseus: baseline,
		}
		if recv == 0 {
			// 全丢就没有时延可谈。写 0，由评分层当成「没有数据」，
			// 而不是写 0 微秒再被算成一条快得离谱的链路。
			out = append(out, r)
			continue
		}

		p50 := baseline + extra
		p50 = int64(float64(p50) * (1 + 0.06*pseudoUnit(seed+"|p50")))
		p99 := int64(float64(p50) * (1.25 + 0.35*pseudoUnit(seed+"|p99")))
		jitter := int64(float64(p50) * (0.05 + 0.06*pseudoUnit(seed+"|jit")))
		if lost > 0 {
			// 有丢包的窗口，剩下的包时延也会偏高——拥塞不是只丢不涨。
			p50 = int64(float64(p50) * (1 + 0.4*loss))
			p99 = int64(float64(p99) * (1 + 0.8*loss))
		}
		r.P50us, r.P99us, r.Jitter = p50, p99, jitter
		out = append(out, r)
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].Src != out[j].Src {
			return out[i].Src < out[j].Src
		}
		return out[i].Dst < out[j].Dst
	})
	return out, tr, nil
}

// Reconcile 把判定结果与注入真相对账。这一步只在实验里做得到：
// 生产上你不知道漏了多少，所以这里的漏报数只是这套参数在这份剧本下的表现。
type Reconcile struct {
	HostDownHit, HostDownFalse []string
	ProbeHit, ProbeFalse       []string
	TargetHit, TargetFalse     []string
	LinkHit                    int // network_side 里路径确实跨过丢包链路的对
	LinkFalse                  int // network_side 里没跨过丢包链路的对（假阳性）
	LinkMissed                 int // 跨了丢包链路却没判成 network_side 的对（漏判）
	LinkOverridden             int // 跨了丢包链路，但已被主机/探针/对端结论覆盖的对
}

// LinkTruth 是真实跨过丢包链路的对数：判对的和漏掉的加起来。
func (r Reconcile) LinkTruth() int { return r.LinkHit + r.LinkMissed }

// Reconcile 对账。集合比较用名字，网络侧按路径是否跨过注入链路判定。
func Evaluate(m *Matrix, a Attribution, tr Truth) Reconcile {
	var rec Reconcile
	paths := make(map[string][]string, len(m.Pairs))
	for _, p := range m.Pairs {
		paths[pairKey(p.Src, p.Dst)] = p.Path
	}

	rec.HostDownHit, rec.HostDownFalse = compareSets(a.HostDown, tr.HostDown)
	rec.ProbeHit, rec.ProbeFalse = compareSets(a.ProbeSide, tr.AgentDown)
	rec.TargetHit, rec.TargetFalse = compareSets(a.TargetSide, tr.NICRxDown)

	// LinkTruth 是真实跨过丢包链路的对数：判对的 + 漏掉的 + 被更早结论盖掉的。
	for _, ps := range a.Scores {
		crossed := false
		for _, k := range paths[pairKey(ps.Src, ps.Dst)] {
			if tr.LinkLoss[k] {
				crossed = true
				break
			}
		}
		switch {
		case ps.Verdict == VerdictNetwork && crossed:
			rec.LinkHit++
		case ps.Verdict == VerdictNetwork:
			rec.LinkFalse++
		case !crossed:
		case ps.Verdict == VerdictOK:
			rec.LinkMissed++
		default:
			// 路径确实跨了坏链路，但这对已经被「主机不可达 / 探针侧 / 对端侧」
			// 先一步定性。不算漏判——排除法本来就该先给更硬的原因让路。
			rec.LinkOverridden++
		}
	}
	return rec
}

// compareSets 拿判定出的集合和真值集合比，返回命中与误判。
func compareSets(got []string, want map[string]bool) (hit, falsePositive []string) {
	seen := make(map[string]bool, len(got))
	for _, n := range got {
		seen[n] = true
		if want[n] {
			hit = append(hit, n)
		} else {
			falsePositive = append(falsePositive, n)
		}
	}
	for n := range want {
		if !seen[n] {
			falsePositive = append(falsePositive, n+"(漏判)")
		}
	}
	sort.Strings(hit)
	sort.Strings(falsePositive)
	return hit, falsePositive
}
