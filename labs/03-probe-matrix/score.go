package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"math"
	"os"
)

// 归因标签。这张表就是「谁的问题」的全部答案。
//
// 四个异常标签对应四个不同的处理动作：探针侧查 agent 与主机，主机不可达查
// 供电与带外，对端侧查那台机器的网卡与接入，网络侧查链路段。
// 只输出一个「探测失败」，等于把判断又推回给值班人。
const (
	VerdictOK       = "ok"
	VerdictNetwork  = "network_side"
	VerdictProbe    = "probe_side"
	VerdictTarget   = "target_side"
	VerdictHostDown = "host_down"
)

// verdictZH 是给人看的中文名。表格里用英文标签，汇总里用中文。
var verdictZH = map[string]string{
	VerdictOK:       "正常",
	VerdictNetwork:  "网络侧异常",
	VerdictProbe:    "探针侧异常",
	VerdictTarget:   "对端侧异常",
	VerdictHostDown: "主机不可达",
}

// 评分维度与权重。四项加起来 1.0；缺数据的维度不参与打分，
// 权重按剩下的维度重新归一。
const (
	wLoss     = 0.40
	wLatency  = 0.30
	wJitter   = 0.15
	wCoverage = 0.15
)

// 各维度的满分线与零分线，中间线性插值。
//
// 阈值是有单位的：2% 丢包已经是事故（1% 的丢包落在一条 10G 链路上，就是
// 每秒 12 万个包），定成 10% 等于没有告警。
const (
	lossFailLine    = 0.02 // 丢包 ≥ 2% 时一对探测就算失败，与 failLossRatio 对齐
	lossBadLine     = 0.10 // 丢包 ≥ 10% 时丢包维度零分
	latFullRatio    = 1.35 // p99 / 基线 ≤ 1.35 满分
	latZeroRatio    = 3.00 // p99 / 基线 ≥ 3 零分
	jitterFullRatio = 0.10 // 抖动 / p50 ≤ 0.1 满分
	jitterZeroRatio = 0.60 // 抖动 / p50 ≥ 0.6 零分
)

// 健康分级。判据是分数，不是主观描述。
const (
	HealthOK       = 90
	HealthDegraded = 70
)

// dimMissing 表示这个维度没有数据（比如没给基线）。不打分，而不是给 0 分：
// 0 分把「不知道」说成「很差」，这是监控里最不该犯的错。
const dimMissing = -1.0

// Dim 是一个评分维度。
type Dim struct {
	Name   string
	Score  float64
	Weight float64
}

// weightedScore 按权重合成总分，缺数据的维度把权重重分给其他维度。
func weightedScore(dims []Dim) float64 {
	var sum, wsum float64
	for _, d := range dims {
		if d.Score < 0 {
			continue
		}
		sum += d.Score * d.Weight
		wsum += d.Weight
	}
	if wsum == 0 {
		return dimMissing
	}
	return sum / wsum
}

// healthLabel 把分数翻成分级。
func healthLabel(score float64) string {
	switch {
	case score < 0:
		return "no_data"
	case score >= HealthOK:
		return "healthy"
	case score >= HealthDegraded:
		return "degraded"
	default:
		return "faulty"
	}
}

// lossScore 把丢包率翻成分数。分档而不是线性：
// 60 个包里丢 1 个是 1.7%，丢 2 个是 3.3%，线性插值会把这个差异放大成十几分，
// 工程上它们只是「噪声」和「有点问题」，差不了那么多。分档的口径好解释，
// 值班人问「为什么这台 62 分」能答得出来。
func lossScore(loss float64, sent int) float64 {
	if sent <= 0 {
		return dimMissing
	}
	onePacket := 1.0 / float64(sent)
	switch {
	case loss <= 0:
		return 100
	case loss <= onePacket+1e-9:
		return 85 // 就丢了一个包，落在噪声里，不判异常
	case loss <= lossFailLine:
		return 70
	case loss <= lossBadLine:
		return 40
	default:
		return 0
	}
}

// latencyScore 用 p99 与基线的比值打分。用 p99 不用平均值：
// 平均值会把一次 200ms 的卡顿摊薄成 0.3ms，看起来什么都没发生。
func latencyScore(p99, base int64) float64 {
	if base <= 0 || p99 <= 0 {
		return dimMissing
	}
	r := float64(p99) / float64(base)
	switch {
	case r <= latFullRatio:
		return 100
	case r >= latZeroRatio:
		return 0
	default:
		return 100 * (latZeroRatio - r) / (latZeroRatio - latFullRatio)
	}
}

// jitterScore 用抖动与 p50 的比值打分：绝对值没法跨链路比较，
// 同一条链路上 20µs 的抖动算抖动，跨机房干线上 20µs 什么都不算。
func jitterScore(jitter, p50 int64) float64 {
	if p50 <= 0 || jitter < 0 {
		return dimMissing
	}
	r := float64(jitter) / float64(p50)
	switch {
	case r <= jitterFullRatio:
		return 100
	case r >= jitterZeroRatio:
		return 0
	default:
		return 100 * (jitterZeroRatio - r) / (jitterZeroRatio - jitterFullRatio)
	}
}

// coverageScore 用「实收样本 / 期望样本」打分。这一维存在的理由很朴素：
// 一对探测只报了 12 个包，它的丢包率就没有 60 个包的丢包率可信，
// 分数得把这个不确定性带上。
func coverageScore(sent, expected int) float64 {
	if expected <= 0 {
		return dimMissing
	}
	if sent >= expected {
		return 100
	}
	if sent <= 0 {
		return 0
	}
	return 100 * float64(sent) / float64(expected)
}

// ProbeResult 是一对探针在一个窗口内聚合后的记录。
//
// 窗口 60 秒、间隔 1 秒就是 60 个包。聚合口径对不上，两边的丢包率就是两套数
// （第 36 课）。这里把口径写进样本（incidents.json 的 window_s / interval_s），
// 而不是藏在代码里。
type ProbeResult struct {
	TS     string `json:"ts"`
	Src    string `json:"src"`
	Dst    string `json:"dst"`
	Sent   int    `json:"sent"`
	Recv   int    `json:"recv"`
	P50us  int64  `json:"rtt_p50_us"`
	P99us  int64  `json:"rtt_p99_us"`
	Jitter int64  `json:"jitter_us"`
	Baseus int64  `json:"baseline_rtt_us"`
}

// Loss 是这一对在本窗口的丢包率，0~1。没发包时返回 0，
// 「没发包」和「全丢」是两件事，由 Sent 字段和样本是否存在区分。
func (r ProbeResult) Loss() float64 {
	if r.Sent <= 0 {
		return 0
	}
	lost := r.Sent - r.Recv
	switch {
	case lost < 0:
		lost = 0
	case lost > r.Sent:
		lost = r.Sent
	}
	return float64(lost) / float64(r.Sent)
}

// pairKey 是探测对的唯一键。方向不同就是不同的对。
func pairKey(src, dst string) string {
	return src + "→" + dst
}

// PairScore 是一对的判定：原始数字 + 四维分数 + 归因结论。
type PairScore struct {
	Src      string  `json:"src"`
	Dst      string  `json:"dst"`
	Sent     int     `json:"sent"`
	Recv     int     `json:"recv"`
	Loss     float64 `json:"loss"`
	P50us    int64   `json:"rtt_p50_us"`
	P99us    int64   `json:"rtt_p99_us"`
	Baseus   int64   `json:"baseline_rtt_us"`
	Jitterus int64   `json:"jitter_us"`
	Hops     int     `json:"hops"`
	Coverage float64 `json:"coverage_score"`
	Score    float64 `json:"score"`
	Health   string  `json:"health"`
	Verdict  string  `json:"verdict"`
	Evidence string  `json:"evidence"`
}

// ScorePair 给一对探测打分。hasResult=false 表示本轮没有这一对的记录。
func ScorePair(p ProbePair, r ProbeResult, hasResult bool, expected int) PairScore {
	ps := PairScore{Src: p.Src, Dst: p.Dst, Hops: p.Hops, Score: dimMissing, Health: "no_data"}
	if !hasResult {
		ps.Coverage = 0
		return ps
	}
	ps.Sent, ps.Recv = r.Sent, r.Recv
	ps.Loss = r.Loss()
	ps.P50us, ps.P99us, ps.Baseus, ps.Jitterus = r.P50us, r.P99us, r.Baseus, r.Jitter

	ps.Coverage = coverageScore(r.Sent, expected)
	ps.Score = weightedScore([]Dim{
		{Name: "loss", Score: lossScore(ps.Loss, r.Sent), Weight: wLoss},
		{Name: "latency", Score: latencyScore(r.P99us, r.Baseus), Weight: wLatency},
		{Name: "jitter", Score: jitterScore(r.Jitter, r.P50us), Weight: wJitter},
		{Name: "coverage", Score: ps.Coverage, Weight: wCoverage},
	})
	// 两位小数就够：多出来的位数在 JSONL 里只会让人对账对不上。
	ps.Score = math.Round(ps.Score*100) / 100
	ps.Health = healthLabel(ps.Score)
	return ps
}

// 归因阈值。四个数都不许随手改，改完要重跑样本看误判怎么变。
const (
	// failLossRatio 是一对判「失败」的丢包门限：2%。
	failLossRatio = 0.02
	// nodeFailRatio 是一行 / 一列判「红」的比例阈值：90%。
	nodeFailRatio = 0.90
	// healthyOtherRatio 是判探针侧 / 对端侧所需的反向正常率上限：50%。
	// 松到 50% 是因为矩阵是抽样出来的，一行里本来就有零星失败对。
	healthyOtherRatio = 0.50
	// minNodePairs 是给节点定罪所需的最少对数。三条对就说一个人有问题，
	// 那是抽样噪声，不是故障。
	minNodePairs = 3
)

// nodeStat 是一个节点作为源（行）或作为目标（列）的失败统计。
type nodeStat struct {
	total int
	fail  int
}

func (s nodeStat) rate() float64 {
	if s.total == 0 {
		return 0
	}
	return float64(s.fail) / float64(s.total)
}

// Attribution 是一次归因的全部产出。
type Attribution struct {
	Scores      []PairScore        // 逐对的分数与结论，顺序同矩阵
	NodeVerdict map[string]string  // 节点 → 结论
	RowFail     map[string]float64 // 节点作为探针的失败率
	ColFail     map[string]float64 // 节点作为目标的失败率
	ProbeSide   []string
	TargetSide  []string
	HostDown    []string

	// SharedLink 是全部 network_side 对共用最多的那一段链路。
	// 它只是最大公约数，不是真相：抽样矩阵里两条不同的链路可能承载同一批对。
	SharedLink  string
	SharedPairs int
	NetworkN    int

	// NetworkScore 是剔除探针侧与主机侧之后的网络健康分。
	// 不剔除的话，一次批量关机能把整个网络的分数打到 40 分以下。
	NetworkScore float64
	Excluded     int
}

// Classify 是判定层的主函数：先算每对的数字，再按同源相关性给节点定罪，
// 最后把结论回填到每一对上。
//
// 两轮统计是这里唯一需要解释的设计。第一轮用全部对算行/列失败率，
// 只为找出「整机不可达」的节点；第二轮把已经判死的机器从分母里剔掉再算一遍，
// 否则一台机器关机能让半个集群的行失败率都虚高，进而被误判成探针侧异常。
func Classify(m *Matrix, results map[string]ProbeResult, expected int) Attribution {
	a := Attribution{
		NodeVerdict: make(map[string]string, len(m.Nodes)),
		RowFail:     make(map[string]float64, len(m.Nodes)),
		ColFail:     make(map[string]float64, len(m.Nodes)),
	}

	scores := make([]PairScore, 0, len(m.Pairs))
	failed := make(map[string]bool, len(m.Pairs))
	paths := make(map[string][]string, len(m.Pairs))
	for _, p := range m.Pairs {
		k := pairKey(p.Src, p.Dst)
		r, has := results[k]
		ps := ScorePair(p, r, has, expected)
		scores = append(scores, ps)
		paths[k] = p.Path
		failed[k] = !has || ps.Loss >= failLossRatio
	}

	// 第一轮：不分身份，只看谁的行红了、谁的列红了。
	bySrc, byDst := gatherStats(m, failed, nil)
	hostDown := make(map[string]bool)
	for _, n := range m.Nodes {
		a.RowFail[n] = bySrc[n].rate()
		a.ColFail[n] = byDst[n].rate()
		if bySrc[n].total < minNodePairs || byDst[n].total < minNodePairs {
			continue
		}
		if bySrc[n].rate() >= nodeFailRatio && byDst[n].rate() >= nodeFailRatio {
			hostDown[n] = true
			a.HostDown = append(a.HostDown, n)
			a.NodeVerdict[n] = VerdictHostDown
		}
	}

	// 第二轮：剔掉整机不可达的节点，重新算一次行/列失败率。
	bySrc, byDst = gatherStats(m, failed, hostDown)
	probeSide := make(map[string]bool)
	targetSide := make(map[string]bool)
	for _, n := range m.Nodes {
		if hostDown[n] {
			continue
		}
		a.RowFail[n] = bySrc[n].rate()
		a.ColFail[n] = byDst[n].rate()
		// 探针侧：自己探不出去（行红），别人探得到它（列绿）。
		if bySrc[n].total >= minNodePairs && byDst[n].total >= minNodePairs &&
			bySrc[n].rate() >= nodeFailRatio && byDst[n].rate() < healthyOtherRatio {
			probeSide[n] = true
			a.ProbeSide = append(a.ProbeSide, n)
			a.NodeVerdict[n] = VerdictProbe
		}
		// 对端侧：别人探不到它（列红），它探别人正常（行绿）。
		if bySrc[n].total >= minNodePairs && byDst[n].total >= minNodePairs &&
			byDst[n].rate() >= nodeFailRatio && bySrc[n].rate() < healthyOtherRatio {
			targetSide[n] = true
			a.TargetSide = append(a.TargetSide, n)
			a.NodeVerdict[n] = VerdictTarget
		}
	}

	// 回填每一对的结论。顺序即优先级：先看机器死没死，再看是不是自己的
	// 探针瞎了，最后才怀疑网络。这个顺序本身就是排除法。
	linkCount := make(map[string]int)
	for i := range scores {
		ps := &scores[i]
		k := pairKey(ps.Src, ps.Dst)
		switch {
		case hostDown[ps.Src] || hostDown[ps.Dst]:
			ps.Verdict = VerdictHostDown
			ps.Evidence = hostEvidence(ps.Src, ps.Dst, hostDown)
		case probeSide[ps.Src]:
			ps.Verdict = VerdictProbe
			ps.Evidence = "该探针本轮失败 " + rateText(bySrc[ps.Src]) + "，但它作为目标被探时正常"
		case targetSide[ps.Dst]:
			ps.Verdict = VerdictTarget
			ps.Evidence = "该目标本轮被探失败 " + rateText(byDst[ps.Dst]) + "，但它作为探针时正常"
		case failed[k]:
			ps.Verdict = VerdictNetwork
			ps.Evidence = "单对失败，同源与同目的都正常，范围落在路径上"
			for _, link := range paths[k] {
				linkCount[link]++
			}
			a.NetworkN++
		default:
			ps.Verdict = VerdictOK
			if ps.Score >= 0 && ps.Score < HealthOK {
				ps.Evidence = "无失败对，但质量指标进入 " + ps.Health
			}
		}
		if ps.Verdict == VerdictOK || ps.Verdict == VerdictNetwork {
			if a.NodeVerdict[ps.Dst] == "" && ps.Score >= 0 {
				a.NodeVerdict[ps.Dst] = VerdictOK
			}
		}
	}

	// 共同链路段：在 network_side 的路径上数出现次数最多的那一段。
	// 抽样矩阵里这一招经常给出「最大公约数」而不是真相，第 12 课的 ECMP
	// 会让它彻底失效——这里先给出最好用的那一版。
	for link, n := range linkCount {
		if n > a.SharedPairs {
			a.SharedLink, a.SharedPairs = link, n
		}
	}

	// 网络健康分：只算网络该负责的那些对。探针侧与主机侧的对整批剔除，
	// 它们的分低是探针自己的问题，不是网络的。
	var sum float64
	var n int
	for _, ps := range scores {
		if ps.Verdict == VerdictProbe || ps.Verdict == VerdictHostDown {
			a.Excluded++
			continue
		}
		if ps.Score < 0 {
			continue
		}
		sum += ps.Score
		n++
	}
	if n > 0 {
		a.NetworkScore = sum / float64(n)
	} else {
		a.NetworkScore = dimMissing
	}

	a.Scores = scores
	return a
}

// gatherStats 统计每个节点作为源（行）与作为目标（列）的失败比例。
// exclude 里的节点出现在对里时，这一对整条跳过——已经判死的机器
// 不该再影响别人的分母。
func gatherStats(m *Matrix, failed map[string]bool, exclude map[string]bool) (bySrc, byDst map[string]nodeStat) {
	bySrc = make(map[string]nodeStat, len(m.Nodes))
	byDst = make(map[string]nodeStat, len(m.Nodes))
	for _, p := range m.Pairs {
		if exclude[p.Src] || exclude[p.Dst] {
			continue
		}
		bad := failed[pairKey(p.Src, p.Dst)]
		s := bySrc[p.Src]
		s.total++
		d := byDst[p.Dst]
		d.total++
		if bad {
			s.fail++
			d.fail++
		}
		bySrc[p.Src] = s
		byDst[p.Dst] = d
	}
	return bySrc, byDst
}

// hostEvidence 给整机不可达的对写判据。哪一端死了一目了然。
func hostEvidence(src, dst string, hostDown map[string]bool) string {
	switch {
	case hostDown[src] && hostDown[dst]:
		return "两端都整机不可达，这一对没有观测价值"
	case hostDown[src]:
		return "探针所在的 " + src + " 整机不可达，本轮没有样本"
	default:
		return "目标 " + dst + " 整机不可达，多个探针都探不到它"
	}
}

// rateText 把失败率写成百分比，保留一位小数。
func rateText(s nodeStat) string {
	return fmt.Sprintf("%.1f%%（%d/%d）", s.rate()*100, s.fail, s.total)
}

// LoadResults 读一份探测结果矩阵（JSONL）。同一个键出现两次就是数据源有问题，
// 直接报错而不是后到覆盖先到——静默覆盖会让复盘时对不上账。
func LoadResults(path string) (map[string]ProbeResult, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	out := make(map[string]ProbeResult)
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	line := 0
	for sc.Scan() {
		line++
		text := sc.Bytes()
		if len(text) == 0 {
			continue
		}
		var r ProbeResult
		if err := json.Unmarshal(text, &r); err != nil {
			return nil, fmt.Errorf("%s 第 %d 行：%w", path, line, err)
		}
		k := pairKey(r.Src, r.Dst)
		if _, dup := out[k]; dup {
			return nil, fmt.Errorf("%s 第 %d 行：探测对 %s 重复出现", path, line, k)
		}
		out[k] = r
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%s 里没有记录", path)
	}
	return out, nil
}
