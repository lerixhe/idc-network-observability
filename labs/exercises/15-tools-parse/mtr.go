package main

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
)

// mtr 的判定门限。5% 是经验值：低于 5% 的丢包在 100 个探测包里只有不到 5 个，
// 抖动与真实丢包分不开。丢包率这种指标，样本数不够时门限比算法重要。
const (
	lossThresholdPct = 5.0 // 超过它才算「在丢」
	sampleEnoughSnt  = 30  // 少于这么多包，丢包率的置信区间宽得没意义
	hopLossSilentPct = 100.0
)

// HopVerdict 是一跳的判定。名字直接进报告，所以用词要是运维嘴里的话。
type HopVerdict string

const (
	HopOK        HopVerdict = "ok"
	HopSilent    HopVerdict = "silent_hop"      // 不回 ICMP，不是丢包
	HopICMPLimit HopVerdict = "icmp_rate_limit" // 中间跳丢包而终点正常
	HopRealLoss  HopVerdict = "real_loss"       // 一路丢到终点
)

// Hop 是一条 mtr 跳。LossPct 是这一跳的丢包率，Snt 是发出并收到回答的探测数——
// 这两个数一起才有意义，95% 丢包配 20 个样本，基本是噪音。
type Hop struct {
	Index     int        `json:"index"`
	Host      string     `json:"host"`
	Responded bool       `json:"responded"`
	LossPct   float64    `json:"loss_pct"`
	Snt       int        `json:"snt"`
	Last      float64    `json:"last_ms"`
	Avg       float64    `json:"avg_ms"`
	Best      float64    `json:"best_ms"`
	Wrst      float64    `json:"worst_ms"`
	StDev     float64    `json:"stdev_ms"`
	Verdict   HopVerdict `json:"verdict"`
	Note      string     `json:"note"`
}

// MTRRun 是一次 mtr 采集。Verdict 是整条路径的结论，
// 它比每跳的判定重要：运维要的是一句话，不是一张表。
type MTRRun struct {
	Probe   string `json:"probe"`
	Target  string `json:"target"`
	Source  string `json:"source"`
	Hops    []Hop  `json:"hops"`
	Verdict string `json:"verdict"`
	Note    string `json:"note"`
}

// 跳行前缀：`  1.|-- 10.20.0.1`。索引与主机名在数值列左边，宽度不定，
// 所以单独解析，不跟数值列一起切片。
var hopPrefixRe = regexp.MustCompile(`^\s*(\d+)\.\s*\|?-*\s*(\S+)\s*$`)

// parseMTRTable 解析一段 mtr 输出。
//
// mtr 的数值列右对齐到列头右边界，跟设备计数器表的左对齐相反，
// 所以取值区间按「上一列列头的末尾 → 本列列头的末尾」算。
func parseMTRTable(name string, meta sampleMeta, header string, rows []string, lineNo int) (*MTRRun, error) {
	lossIdx := strings.Index(header, "Loss%")
	if lossIdx < 0 {
		return nil, fmt.Errorf("%s 第 %d 行: mtr 列头里没有 Loss%%，拿到的列头是 %q", name, lineNo, header)
	}

	run := &MTRRun{Probe: meta.device, Target: meta.target, Source: name}
	if run.Probe == "" {
		run.Probe = "unknown-probe"
	}

	cols := numericLayout(header, lossIdx)
	if len(cols) < 2 {
		return nil, fmt.Errorf("%s 第 %d 行: mtr 数值列只有 %d 个，至少要 Loss%% 与 Snt", name, lineNo, len(cols))
	}

	for i, line := range rows {
		hop, err := parseMTRRow(line, lossIdx, cols)
		if err != nil {
			return nil, fmt.Errorf("%s 第 %d 行（第 %d 条跳）: %w", name, lineNo+1+i, i+1, err)
		}
		run.Hops = append(run.Hops, hop)
	}
	if len(run.Hops) == 0 {
		return nil, fmt.Errorf("%s: mtr 段没有跳", name)
	}
	if run.Target == "" {
		run.Target = run.terminalHost()
	}
	run.analyze()
	return run, nil
}

// numericLayout 把列头从 from 位置起的每个 token 变成一列，右对齐区间。
// colNames 里只认这几个名字，多出来的列（不同 mtr 版本加字段）忽略。
func numericLayout(header string, from int) []column {
	var tokens []headerToken
	for _, tk := range tokenizeHeader(header) {
		if tk.start >= from {
			tokens = append(tokens, tk)
		}
	}

	cols := make([]column, 0, len(tokens))
	for i, tk := range tokens {
		prevEnd := from
		if i > 0 {
			prevEnd = tokens[i-1].end
		}
		cols = append(cols, column{
			name:  tk.text,
			start: prevEnd,
			end:   tk.end,
		})
	}
	return cols
}

// parseMTRRow 解析一行跳。前缀取索引与主机名，数值列按列头名字取值，
// 名字对不上就跳过——mtr 换版本加列不该让整份数据解不出来。
func parseMTRRow(line string, prefixEnd int, cols []column) (Hop, error) {
	hop := Hop{}

	prefix := line
	if prefixEnd < len(line) {
		prefix = line[:prefixEnd]
	} else {
		prefix = line
	}
	m := hopPrefixRe.FindStringSubmatch(prefix)
	if m == nil {
		return hop, fmt.Errorf("不是 mtr 跳行：%q（前缀区 %q）", strings.TrimSpace(line), strings.TrimSpace(prefix))
	}
	hop.Index, _ = strconv.Atoi(m[1])
	hop.Host = m[2]
	hop.Responded = hop.Host != "???" && !strings.Contains(hop.Host, "*")

	got := map[string]bool{}
	for _, c := range cols {
		raw := sliceColumn(line, c)
		v, ok, err := parseCounter(strings.TrimSuffix(raw, "%"), UnitUnset)
		if err != nil {
			return hop, fmt.Errorf("列 %s: %w", c.name, err)
		}
		if !ok {
			continue
		}
		switch strings.ToLower(c.name) {
		case "loss%":
			hop.LossPct = v.Value
		case "snt":
			hop.Snt = int(v.Value)
		case "last":
			hop.Last = v.Value
		case "avg":
			hop.Avg = v.Value
		case "best":
			hop.Best = v.Value
		case "wrst":
			hop.Wrst = v.Value
		case "stdev":
			hop.StDev = v.Value
		default:
			continue
		}
		got[strings.ToLower(c.name)] = true
	}
	if !got["loss%"] || !got["snt"] {
		return hop, fmt.Errorf("第 %d 跳缺 Loss%% 或 Snt（Host=%s）", hop.Index, hop.Host)
	}
	return hop, nil
}

// terminalHost 是最后一个有回答的跳的主机名，用来填 @target 缺失时的终点。
func (r *MTRRun) terminalHost() string {
	for i := len(r.Hops) - 1; i >= 0; i-- {
		if r.Hops[i].Responded {
			return r.Hops[i].Host
		}
	}
	return ""
}

// analyze 给每跳定性，再给整条路径一个结论。
//
// 这一段是这个实验的靶心：中间跳丢包而终点正常，几乎都是设备的 ICMP 限速，
// 不是真丢包。误判的代价是派人去查一台根本没问题的设备。
func (r *MTRRun) analyze() {
	target := r.Hops[len(r.Hops)-1]

	for i := range r.Hops {
		h := &r.Hops[i]
		h.Verdict, h.Note = judgeHop(h, r.Hops[i+1:], i == len(r.Hops)-1)
	}

	var midLimited []string
	for _, h := range r.Hops {
		if h.Verdict == HopICMPLimit {
			midLimited = append(midLimited, fmt.Sprintf("%d(%s)", h.Index, h.Host))
		}
	}

	switch {
	case target.Responded && target.LossPct >= lossThresholdPct:
		r.Verdict = "target_loss"
		r.Note = fmt.Sprintf("终点 %s 丢包 %.1f%%，最后几跳一路丢到这里，这是真丢包", target.Host, target.LossPct)
		if len(midLimited) > 0 {
			r.Note += fmt.Sprintf("；中间跳 %s 的高丢包是 ICMP 限速，别当第二处故障", strings.Join(midLimited, "、"))
		}
	case len(midLimited) > 0:
		r.Verdict = "mid_hop_icmp_limit"
		r.Note = fmt.Sprintf("中间跳 %s 丢包但终点正常，按 ICMP 限速处理，不是端到端丢包",
			strings.Join(midLimited, "、"))
	default:
		r.Verdict = "path_ok"
		r.Note = fmt.Sprintf("终点 %s 丢包 %.1f%%，路径上没有持续丢包", target.Host, target.LossPct)
	}

	silent := 0
	for _, h := range r.Hops {
		if h.Verdict == HopSilent {
			silent++
		}
	}
	if silent > 0 {
		r.Note += fmt.Sprintf("；有 %d 跳不回 ICMP（%s），结论只覆盖有回答的跳",
			silent, silentHosts(r.Hops))
	}
}

// judgeHop 对一跳定性。后续跳的丢包率决定了这一跳的高丢包是真是假：
// 后面跳都正常，说明包确实过去了，这一跳丢的是「回探测包的 ICMP」。
func judgeHop(h *Hop, rest []Hop, isTarget bool) (HopVerdict, string) {
	if !h.Responded {
		return HopSilent, "不回探测包（??? 或 *），这一跳没有数据；只要后面的跳有回答，就不算丢包"
	}
	if h.LossPct >= hopLossSilentPct {
		return HopICMPLimit, "100% 丢包却还有后续跳，典型 ICMP 限速，落表的概率是 0"
	}
	if h.LossPct >= lossThresholdPct {
		laterOK := false
		for _, o := range rest {
			if o.Responded && o.LossPct < lossThresholdPct {
				laterOK = true
				break
			}
		}
		if !isTarget && laterOK {
			note := fmt.Sprintf("中间跳丢包 %.1f%% 而终点正常，按 ICMP 限速处理；上一跳与下一跳的计数才是证据",
				h.LossPct)
			if h.Snt < sampleEnoughSnt {
				note += fmt.Sprintf("；Snt 只有 %d，这个丢包率本身的误差也很大", h.Snt)
			}
			return HopICMPLimit, note
		}
		note := fmt.Sprintf("丢包 %.1f%%，后续跳同样在丢，这一跳到终点的损耗是真的", h.LossPct)
		if h.Snt < sampleEnoughSnt {
			note += fmt.Sprintf("；Snt 只有 %d，先加样本再下结论", h.Snt)
		}
		return HopRealLoss, note
	}
	if h.LossPct > 0 {
		return HopOK, fmt.Sprintf("丢包 %.1f%% 未过 %.0f%% 门限，按抖动处理", h.LossPct, lossThresholdPct)
	}
	return HopOK, ""
}

func silentHosts(hops []Hop) string {
	var names []string
	for _, h := range hops {
		if h.Verdict == HopSilent {
			names = append(names, fmt.Sprintf("%d", h.Index))
		}
	}
	return "跳 " + strings.Join(names, "、")
}

// Summary 是 mtr 段在报告里的摘要行。
func (r MTRRun) Summary() string {
	if len(r.Hops) == 0 {
		return "（没有跳）"
	}
	last := r.Hops[len(r.Hops)-1]
	return fmt.Sprintf("%d 跳 → %s，终点丢包 %.1f%%，判定 %s",
		last.Index, r.Target, last.LossPct, r.Verdict)
}

// avgJitter 是路径上所有有回答跳的平均 StdDev，用来看「慢」是不是抖动。
// 只做展示，不参与判定。
func (r MTRRun) avgJitter() float64 {
	var sum float64
	var n int
	for _, h := range r.Hops {
		if !h.Responded {
			continue
		}
		sum += h.StDev
		n++
	}
	if n == 0 {
		return math.NaN()
	}
	return sum / float64(n)
}
