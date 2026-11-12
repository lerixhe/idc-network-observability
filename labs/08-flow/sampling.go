package main

import (
	"encoding/json"
	"fmt"
	"math"
	"math/rand"
	"net"
	"os"
	"sort"
)

// 真值模型：一份「实际发生了什么」的流量清单，用来和采样后的估算值对账。
//
// 真实环境里拿不到这份真值——这正是采样误差难对付的地方：你只能看到采样结果，
// 没有东西告诉你漏了什么。实验里必须先有真值才能把失真量出来。

// TruthFlow 是一条真实流的定义。PPS 是每秒报文数，不是带宽：
// 采样是按包抽的，流能不能被看见取决于包速率，与字节数无关。
type TruthFlow struct {
	Name    string  `json:"name"`
	SrcIP   string  `json:"src_ip"`
	DstIP   string  `json:"dst_ip"`
	Proto   string  `json:"proto"`
	SrcPort int     `json:"src_port"`
	DstPort int     `json:"dst_port"`
	PPS     float64 `json:"pps"`
}

// Fanout 是「一窝老鼠」的生成子句：同一模板复制 count 份，只换源 IP。
// 180 行手写样本没人看得下去，用一句子句展开，键仍然各不相同。
type Fanout struct {
	Prefix    string  `json:"prefix"`
	Count     int     `json:"count"`
	PPSEach   float64 `json:"pps_each"`
	SrcIPBase string  `json:"src_ip_base"`
	DstIP     string  `json:"dst_ip"`
	Proto     string  `json:"proto"`
	SrcPort   int     `json:"src_port"`
	DstPort   int     `json:"dst_port"`
}

// TruthSet 是真值文件的外层结构。
type TruthSet struct {
	Note            string      `json:"note"`
	WindowSeconds   float64     `json:"window_seconds"`
	MeanPacketBytes float64     `json:"mean_packet_bytes"`
	Flows           []TruthFlow `json:"flows"`
	Fanout          *Fanout     `json:"fanout"`
}

func loadTruth(path string) (TruthSet, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return TruthSet{}, err
	}
	var t TruthSet
	if err := json.Unmarshal(data, &t); err != nil {
		return TruthSet{}, fmt.Errorf("%s: %w", path, err)
	}
	if err := t.validate(); err != nil {
		return TruthSet{}, fmt.Errorf("%s: %w", path, err)
	}
	return t, nil
}

func (t TruthSet) validate() error {
	switch {
	case t.WindowSeconds <= 0:
		return fmt.Errorf("window_seconds 必须大于 0，实际 %v", t.WindowSeconds)
	case t.MeanPacketBytes <= 0:
		return fmt.Errorf("mean_packet_bytes 必须大于 0，实际 %v", t.MeanPacketBytes)
	case len(t.Flows) == 0:
		return fmt.Errorf("flows 是空的")
	}
	for i, f := range t.Flows {
		if f.Name == "" {
			return fmt.Errorf("第 %d 条流没有 name", i+1)
		}
		if f.PPS < 0 {
			return fmt.Errorf("%s: pps 是负数 %v", f.Name, f.PPS)
		}
		if f.SrcIP == "" || f.DstIP == "" {
			return fmt.Errorf("%s: 缺 src_ip 或 dst_ip", f.Name)
		}
	}
	if f := t.Fanout; f != nil {
		if f.Count <= 0 {
			return fmt.Errorf("fanout.count 必须大于 0，实际 %d", f.Count)
		}
		if f.PPSEach < 0 {
			return fmt.Errorf("fanout.pps_each 是负数 %v", f.PPSEach)
		}
		if f.Prefix == "" {
			return fmt.Errorf("fanout.prefix 为空，展开后的流没有名字")
		}
		if _, err := fanoutIPs(f.SrcIPBase, f.Count); err != nil {
			return err
		}
	}
	return nil
}

// fanoutIPs 从基址往后数出 count 个 IPv4 地址。
//
// 只支持同一段 /24：跨段的展开会让「这窝老鼠是同一批业务」这句话失去意义，
// 而且读样本的人一眼看不出 10.40.0.254 后面接的是 10.40.1.0。
func fanoutIPs(base string, count int) ([]string, error) {
	ip := net.ParseIP(base).To4()
	if ip == nil {
		return nil, fmt.Errorf("fanout.src_ip_base %q 不是 IPv4", base)
	}
	if int(ip[3])+count > 255 {
		return nil, fmt.Errorf("fanout 跨 /24 不支持：基址末字节 %d + 条数 %d > 255", ip[3], count)
	}
	out := make([]string, count)
	for i := 0; i < count; i++ {
		out[i] = net.IPv4(ip[0], ip[1], ip[2], ip[3]+byte(i+1)).String()
	}
	return out, nil
}

// synthesize 把显式流和 fanout 子句合成一份完整清单，并检查五元组不重复。
func (t TruthSet) synthesize() ([]TruthFlow, error) {
	flows := make([]TruthFlow, 0, len(t.Flows)+16)
	flows = append(flows, t.Flows...)
	if f := t.Fanout; f != nil {
		ips, err := fanoutIPs(f.SrcIPBase, f.Count)
		if err != nil {
			return nil, err
		}
		for i, ip := range ips {
			flows = append(flows, TruthFlow{
				Name:    fmt.Sprintf("%s-%03d", f.Prefix, i+1),
				SrcIP:   ip,
				DstIP:   f.DstIP,
				Proto:   f.Proto,
				SrcPort: f.SrcPort,
				DstPort: f.DstPort,
				PPS:     f.PPSEach,
			})
		}
	}
	seenName := make(map[string]bool, len(flows))
	seenKey := make(map[FlowKey]string, len(flows))
	for _, f := range flows {
		if seenName[f.Name] {
			return nil, fmt.Errorf("流名字重复: %s", f.Name)
		}
		seenName[f.Name] = true
		key := makeKey(f.SrcIP, f.DstIP, f.Proto, f.SrcPort, f.DstPort)
		if other, ok := seenKey[key]; ok {
			return nil, fmt.Errorf("%s 与 %s 的五元组相同，TopN 上会合并成一条", f.Name, other)
		}
		seenKey[key] = f.Name
	}
	return flows, nil
}

// FlowEstimate 是采样之后对一条流的估计，与真值并列。
type FlowEstimate struct {
	Name        string  `json:"name"`
	Key         FlowKey `json:"flow"`
	TruePackets int64   `json:"true_packets"`
	TrueBytes   int64   `json:"true_bytes"`
	Sampled     int64   `json:"sampled"`
	EstBytes    int64   `json:"est_bytes"`
	EstPackets  int64   `json:"est_packets"`
	// RelErr 是这次估计的相对标准误差上界 1/√c，c 是抽样个数。
	RelErr   float64 `json:"rel_err"`
	TrueRank int     `json:"true_rank"`
	EstRank  int     `json:"est_rank"`
}

// RateResult 是一个采样率下的一次完整实验。
type RateResult struct {
	N              int            `json:"sampling_rate"`
	WindowSeconds  float64        `json:"window_seconds"`
	TruePackets    int64          `json:"true_packets"`
	TrueBytes      int64          `json:"true_bytes"`
	SampledPackets int64          `json:"sampled_packets"`
	SampledBytes   int64          `json:"sampled_bytes"`
	Flows          []FlowEstimate `json:"flows"`
	TopN           int            `json:"top_n"`
	// Total / Visible 是全量流数与抽到过样本的流数。差出来的那批,
	// 在采集器眼里根本不存在。
	Total   int `json:"total_flows"`
	Visible int `json:"visible_flows"`
	// TopNOverlap 是估算 TopN 里真值也在 TopN 的条数。
	TopNOverlap int `json:"top_n_overlap"`
	// Elephant 是真值里最大的那条流（TrueRank=1）的估计误差。
	ElephantName string  `json:"elephant_name"`
	ElephantErr  float64 `json:"elephant_rel_err"`
	ElephantEst  int64   `json:"elephant_est_bytes"`
	ElephantTrue int64   `json:"elephant_true_bytes"`
	// MinObservablePPS 是窗口内至少采到 1 个样本所需的最低包速率。
	MinObservablePPS float64 `json:"min_observable_pps"`
	MinObservableBps float64 `json:"min_observable_bps"`
}

// Simulation 是全部采样率跑完的结果。
type Simulation struct {
	WindowSeconds   float64      `json:"window_seconds"`
	MeanPacketBytes float64      `json:"mean_packet_bytes"`
	Flows           int          `json:"flows"`
	Rates           []RateResult `json:"rates"`
}

// Simulate 按每个采样率各跑一遍抽样。
//
// 抽样模型：对每个报文独立掷骰子，保留概率 1/N，与 sFlow 的随机采样同族。
// 同一条流抽到 c 个样本的概率服从二项分布，估算字节数 = 样本字节 × N，
// 它的相对标准误差约为 1/√c。
func Simulate(truth TruthSet, rates []int, topN int, seed int64) (Simulation, error) {
	if topN <= 0 {
		return Simulation{}, fmt.Errorf("topN 必须大于 0，实际 %d", topN)
	}
	if len(rates) == 0 {
		return Simulation{}, fmt.Errorf("采样率列表是空的")
	}
	flows, err := truth.synthesize()
	if err != nil {
		return Simulation{}, err
	}

	pktBytes := int64(math.Round(truth.MeanPacketBytes))
	sim := Simulation{
		WindowSeconds:   truth.WindowSeconds,
		MeanPacketBytes: truth.MeanPacketBytes,
		Flows:           len(flows),
	}

	// 一个 rng 从头用到尾：同一个 seed 跑出来的三行数字可以互相对照。
	// 换 seed 数字会变，结论不变——这正是估算类指标的性质。
	rng := rand.New(rand.NewSource(seed))

	for _, n := range rates {
		if n <= 0 {
			return Simulation{}, fmt.Errorf("采样率 1:%d 不合法", n)
		}
		res := RateResult{N: n, WindowSeconds: truth.WindowSeconds, TopN: topN, Total: len(flows)}
		res.MinObservablePPS = MinObservablePPS(1, n, truth.WindowSeconds)
		res.MinObservableBps = res.MinObservablePPS * truth.MeanPacketBytes * 8

		p := 1 / float64(n)
		est := make([]FlowEstimate, len(flows))
		for i, f := range flows {
			packets := int64(math.Round(f.PPS * truth.WindowSeconds))
			var sampled int64
			for k := int64(0); k < packets; k++ {
				if rng.Float64() < p {
					sampled++
				}
			}
			est[i] = FlowEstimate{
				Name:        f.Name,
				Key:         makeKey(f.SrcIP, f.DstIP, f.Proto, f.SrcPort, f.DstPort),
				TruePackets: packets,
				TrueBytes:   packets * pktBytes,
				Sampled:     sampled,
				EstBytes:    sampled * pktBytes * int64(n),
				EstPackets:  sampled * int64(n),
				RelErr:      relErr(sampled),
			}
			res.SampledPackets += sampled
			res.SampledBytes += sampled * pktBytes
			res.TruePackets += packets
			res.TrueBytes += packets * pktBytes
			if sampled > 0 {
				res.Visible++
			}
		}

		assignRanks(est)
		for _, e := range est {
			if e.EstRank <= topN && e.TrueRank <= topN {
				res.TopNOverlap++
			}
			if e.TrueRank == 1 {
				res.ElephantName = e.Name
				res.ElephantTrue = e.TrueBytes
				res.ElephantEst = e.EstBytes
				if e.TrueBytes > 0 {
					res.ElephantErr = math.Abs(float64(e.EstBytes)-float64(e.TrueBytes)) / float64(e.TrueBytes)
				}
			}
		}

		sort.SliceStable(est, func(i, j int) bool { return est[i].EstRank < est[j].EstRank })
		res.Flows = est
		sim.Rates = append(sim.Rates, res)
	}
	return sim, nil
}

// assignRanks 同时给真值排名与估算排名，从 1 开始。
// 估算是 0 字节的流也会拿到名次——采集器不知道它存在，但真值里它确实排第几，
// 两个名次放在一起才看得出失真。
func assignRanks(flows []FlowEstimate) {
	byTrue := make([]int, len(flows))
	byEst := make([]int, len(flows))
	for i := range byTrue {
		byTrue[i] = i
		byEst[i] = i
	}
	sort.SliceStable(byTrue, func(a, b int) bool {
		x, y := flows[byTrue[a]], flows[byTrue[b]]
		if x.TrueBytes != y.TrueBytes {
			return x.TrueBytes > y.TrueBytes
		}
		return x.Key.String() < y.Key.String()
	})
	for rank, idx := range byTrue {
		flows[idx].TrueRank = rank + 1
	}
	sort.SliceStable(byEst, func(a, b int) bool {
		x, y := flows[byEst[a]], flows[byEst[b]]
		if x.EstBytes != y.EstBytes {
			return x.EstBytes > y.EstBytes
		}
		return x.Key.String() < y.Key.String()
	})
	for rank, idx := range byEst {
		flows[idx].EstRank = rank + 1
	}
}

// MinObservablePPS 是「窗口 T 内至少采到 k 个样本」所需的最小流速率。
//
// 每秒 r 个包的流，在 1:N 采样下每秒产出 r/N 个样本，窗口 T 秒共 r·T/N 个。
// 令 r·T/N = k 得：
//
//	r_min = k · N / T
//
// 代入 N=1000、T=60s、k=1：r_min = 16.7 包/秒，约 133 kbps（按 1000 字节包长）。
// 这就是 1:1000 采样的可见性底线：低于它的流，TopN 上永远看不到。
func MinObservablePPS(k, n int, windowSeconds float64) float64 {
	if windowSeconds <= 0 || n <= 0 || k <= 0 {
		return 0
	}
	return float64(k) * float64(n) / windowSeconds
}
