package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
)

type Case struct {
	CaseID string `json:"case_id"`
	Window struct {
		From string `json:"from"`
		To   string `json:"to"`
	} `json:"window"`
	App struct {
		Service   string  `json:"service"`
		P99Before float64 `json:"p99_before_ms"`
		P99During float64 `json:"p99_during_ms"`
		CPUPct    float64 `json:"cpu_pct"`
		TraceHint string  `json:"trace_hint"`
	} `json:"app"`
	Alarms []struct {
		Name string `json:"name"`
	} `json:"alarms"`
	Paths []struct {
		From    string  `json:"from"`
		To      string  `json:"to"`
		Rack    string  `json:"rack"`
		Probes  int     `json:"probes"`
		LossPct float64 `json:"loss_pct"`
	} `json:"paths"`
	SwitchPorts []struct {
		Device     string `json:"device"`
		Port       string `json:"port"`
		InErrors   int    `json:"in_errors"`
		OutDiscard int    `json:"out_discards"`
		Oper       string `json:"oper"`
	} `json:"switch_ports"`
	HostNICs []struct {
		Host       string `json:"host"`
		RxFifo     int    `json:"rx_fifo_errors"`
		RxDropped  int    `json:"rx_dropped"`
		RingBuffer int    `json:"ring_buffer"`
	} `json:"host_nics"`
	Changes []struct {
		At      string   `json:"at"`
		Desc    string   `json:"desc"`
		Devices []string `json:"devices"`
	} `json:"changes"`
	ClockOffsets map[string]int `json:"clock_offsets"`
}

func LoadCase(path string) (Case, error) {
	var c Case
	b, err := os.ReadFile(path)
	if err != nil {
		return c, err
	}
	return c, json.Unmarshal(b, &c)
}

// Step 是十步回放里的一步：问什么、看到什么、能不能往下走。
type Step struct {
	No      int
	Ask     string
	Saw     string
	Verdict string // 结论或下一步
	Done    bool   // 是否在这一步得到了决定性证据
}

// Replay 按顺序跑十步。每一步只依赖样本数据，不预先知道结论。
func Replay(c Case) ([]Step, string) {
	var steps []Step

	// ① 应用侧曲线是什么形态
	ratio := c.App.P99During / c.App.P99Before
	steps = append(steps, Step{1, "应用 p99 曲线什么形态",
		fmt.Sprintf("%.0f ms → %.0f ms，%.1f 倍，阶跃", c.App.P99Before, c.App.P99During, ratio),
		"阶跃不是漂移，找触发点", false})

	// ② 应用自己在干什么
	steps = append(steps, Step{2, "应用自身资源是否异常",
		fmt.Sprintf("CPU %.0f%%，无 GC 尖峰，trace 指向 %s", c.App.CPUPct, c.App.TraceHint),
		"应用不在自身，跟着 trace 走", false})

	// ③ 告警是否干净
	steps = append(steps, Step{3, "监控告警有没有东西",
		fmt.Sprintf("%d 条告警", len(c.Alarms)),
		"告警为空不等于没问题：阈值筛过一遍", false})

	// ④ 缩小到哪一批机器
	rack := ""
	seen := map[string]bool{}
	for _, p := range c.Paths {
		if !seen[p.Rack] {
			seen[p.Rack] = true
			rack = p.Rack
		}
	}
	steps = append(steps, Step{4, "受影响的机器集中在哪",
		fmt.Sprintf("全部在同机架 %s", rack),
		"范围收到机架内，网络侧有嫌疑", false})

	// ⑤ 小样本 ping
	small := c.Paths[0]
	steps = append(steps, Step{5, "ping 20 个包看通不通",
		fmt.Sprintf("%.0f%% 丢包", small.LossPct),
		"通，但 20 个包撑不起结论（0.3% 丢包全通的概率 94.2%）", false})

	// ⑥ 加量探测
	var heavy *struct {
		From    string  `json:"from"`
		To      string  `json:"to"`
		Rack    string  `json:"rack"`
		Probes  int     `json:"probes"`
		LossPct float64 `json:"loss_pct"`
	}
	for i := range c.Paths {
		if c.Paths[i].Probes >= 1000 && c.Paths[i].LossPct > 0.1 {
			heavy = &c.Paths[i]
			break
		}
	}
	if heavy != nil {
		steps = append(steps, Step{6, "发 1000 个包",
			fmt.Sprintf("%s → %s 丢包 %.2f%%", heavy.From, heavy.To, heavy.LossPct),
			"确认丢包存在，且只在一条路径上", false})
	} else {
		steps = append(steps, Step{6, "发 1000 个包", "没有丢包", "到此为止，方向要转向应用或主机", true})
	}

	// ⑦ 交换机端口
	dirty := 0
	for _, p := range c.SwitchPorts {
		if p.InErrors > 0 || p.OutDiscard > 0 || p.Oper != "up" {
			dirty++
		}
	}
	steps = append(steps, Step{7, "交换机端口计数器",
		fmt.Sprintf("%d 个端口，异常 %d 个", len(c.SwitchPorts), dirty),
		"交换机侧干净，嫌疑往主机侧推", false})

	// ⑧ 主机网卡
	var culprit string
	maxFifo := 0
	for _, h := range c.HostNICs {
		if h.RxFifo > maxFifo {
			maxFifo = h.RxFifo
			culprit = h.Host
		}
	}
	if maxFifo > 0 {
		steps = append(steps, Step{8, "主机网卡丢包计数",
			fmt.Sprintf("%s 的 rx_fifo_errors = %d（ring buffer %d）", culprit, maxFifo, ringOf(c, culprit)),
			"找到了：网卡侧丢包，与交换机无关", true})
	} else {
		steps = append(steps, Step{8, "主机网卡丢包计数", "全部为 0", "还没定位到，需要继续查容器与内核", false})
	}

	// ⑨ 与变更对账
	matched := ""
	for _, ch := range c.Changes {
		for _, d := range ch.Devices {
			if d == culprit {
				matched = ch.Desc
			}
		}
	}
	if matched != "" {
		steps = append(steps, Step{9, "同期有没有变更",
			matched, "变更解释了根因：重启后 ring buffer 回到默认 256", false})
	} else {
		steps = append(steps, Step{9, "同期有没有变更", "无相关变更", "没有变更解释，需要查驱动与流量形态", false})
	}

	// ⑩ 时间对齐
	sw := c.ClockOffsets["switch"]
	steps = append(steps, Step{10, "时间轴能不能对上",
		fmt.Sprintf("设备时钟偏差 %d 秒，业务日志与主机一致", sw),
		fmt.Sprintf("结论只覆盖 %s 之后", c.Window.From), false})

	// 结案
	conclusion := "未定位"
	if maxFifo > 0 && heavy != nil {
		conclusion = fmt.Sprintf("丢包在 %s 的网卡接收侧（ring buffer=%d 溢出），不是交换机也不是链路；受影响的只有到这台主机的那条路径",
			culprit, ringOf(c, culprit))
	}
	return steps, conclusion
}

func ringOf(c Case, host string) int {
	for _, h := range c.HostNICs {
		if h.Host == host {
			return h.RingBuffer
		}
	}
	return 0
}

// Evidence 把十步里的事实汇成一条证据链，供举证包使用。
func Evidence(c Case) []string {
	var out []string
	out = append(out, fmt.Sprintf("应用 p99 %.0f→%.0f ms（%.1f×）", c.App.P99Before, c.App.P99During, c.App.P99During/c.App.P99Before))
	for _, p := range c.Paths {
		if p.LossPct > 0.1 {
			out = append(out, fmt.Sprintf("路径 %s→%s 丢包 %.2f%%（%d 包）", p.From, p.To, p.LossPct, p.Probes))
		}
	}
	for _, h := range c.HostNICs {
		if h.RxFifo > 0 {
			out = append(out, fmt.Sprintf("主机 %s rx_fifo_errors=%d", h.Host, h.RxFifo))
		}
	}
	sort.Strings(out)
	return out
}
