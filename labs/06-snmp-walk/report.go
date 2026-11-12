package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ReportInput 是打出整份报表需要的全部东西。
type ReportInput struct {
	Collector   *Collector
	Rebooted    *Collector // 换板重启后的那一轮，可为 nil
	SampleFile  string
	RebootFile  string
	OutDir      string
	FocusDevice string
	WireKind    string
	ClockKind   string
	WallTime    time.Duration
	Outputs     []string
}

// dispWidth 按终端列宽算宽度：CJK 记两列。表头是中文，不打平就对不齐。
func dispWidth(s string) int {
	w := 0
	for _, r := range s {
		if r > 0x2E80 {
			w += 2
			continue
		}
		w++
	}
	return w
}

func pad(s string, width int) string {
	if n := width - dispWidth(s); n > 0 {
		return s + strings.Repeat(" ", n)
	}
	return s
}

func padLeft(s string, width int) string {
	if n := width - dispWidth(s); n > 0 {
		return strings.Repeat(" ", n) + s
	}
	return s
}

// fmtBytes 把字节数换成 GB/MB，表里不出现十一位数字。
func fmtBytes(b uint64) string {
	switch {
	case b >= 1_000_000_000_000:
		return fmt.Sprintf("%.1f TB", float64(b)/1e12)
	case b >= 1_000_000_000:
		return fmt.Sprintf("%.1f GB", float64(b)/1e9)
	case b >= 1_000_000:
		return fmt.Sprintf("%.1f MB", float64(b)/1e6)
	default:
		return fmt.Sprintf("%d B", b)
	}
}

// fmtSpeed 把 Mbps 打成人读的速率。
func fmtSpeed(mbps uint64) string {
	switch {
	case mbps >= 1000:
		return fmt.Sprintf("%dG", mbps/1000)
	default:
		return fmt.Sprintf("%dM", mbps)
	}
}

func fmtMs(d time.Duration) string {
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	return fmt.Sprintf("%.1fs", d.Seconds())
}

// PrintReport 输出整份报表。
func PrintReport(w io.Writer, in ReportInput) {
	c := in.Collector
	sum := c.Summarize()

	fmt.Fprintf(w, "样本 %s（%d 台设备，%d 个端口）· 传输 %s · 时钟%s · %d 轮 × %s\n",
		in.SampleFile, len(c.Fleet.Devices), totalPorts(c.Fleet), in.WireKind, in.ClockKind,
		c.Cfg.Cycles, c.Cfg.Interval)
	fmt.Fprintf(w, "列：ifTable 7 列 + ifXTable 5 列 = %d 列；端口数 × 列数 = %d 个单元格\n",
		len(Columns), totalPorts(c.Fleet)*len(Columns))
	if c.Cfg.RepsOverride > 0 {
		fmt.Fprintf(w, "注意：-reps %d 覆盖了档位条数，设备能力不足会回 tooBig，采集器砍半重试\n", c.Cfg.RepsOverride)
	}
	fmt.Fprintln(w)

	printWalkCost(w, c)
	printDeviceSummary(w, c, sum)
	printCycleMatrix(w, c)
	printPortTable(w, c, in.FocusDevice)
	printReindex(w, c, in.Rebooted, in.RebootFile)

	fmt.Fprintf(w, "\n合计：请求 %d 次，varbind %d 条，拼出 %d 行端口；tooBig %d 次，超时 %d 次，重试 %d 次，排队合计 %s\n",
		sum.Requests, sum.Varbinds, sum.Rows, sum.TooBig, sum.Timeouts, sum.Retries, fmtMs(sum.QueueWait))
	fmt.Fprintf(w, "设备结论：正常 %d / 响应慢 %d / 不可达 %d；账面耗时 %s（墙钟 %s）\n",
		sum.OkDevices, sum.Slow, sum.Unreach, fmtMs(c.EndedAt.Sub(c.StartedAt)), fmtMs(in.WallTime))
	if len(in.Outputs) > 0 {
		fmt.Fprintf(w, "\n产物\n")
		for _, p := range in.Outputs {
			fmt.Fprintf(w, "  %s\n", p)
		}
	}
}

func totalPorts(f *Fleet) int {
	n := 0
	for i := range f.Devices {
		n += len(f.Devices[i].Interfaces)
	}
	return n
}

// printWalkCost 是这一课的核心算术：一套请求形状要花多少 PDU、多少秒、占掉周期的几成。
func printWalkCost(w io.Writer, c *Collector) {
	fmt.Fprintln(w, "包量与耗时（每轮实算：PDU 数、排队、往返、占周期比例）")
	fmt.Fprintf(w, "%s %s %s %s %s %s %s %s %s %s\n",
		pad("device", 15), pad("tier", 5), padLeft("ports", 6), padLeft("reps", 5),
		padLeft("PDU", 5), padLeft("varbind", 8), padLeft("排队", 8), padLeft("往返", 8),
		padLeft("本轮耗时", 10), padLeft("占周期", 7))
	for _, s := range c.Sessions {
		r, ok := firstCollectedRound(c, s.Dev.Name)
		if !ok {
			fmt.Fprintf(w, "%s %s %s %s %s\n", pad(s.Dev.Name, 15), pad(s.Policy.Tier, 5),
				padLeft("-", 6), padLeft("-", 5), "本轮从未采到数据")
			continue
		}
		share := float64(r.Elapsed) / float64(c.Cfg.Interval) * 100
		fmt.Fprintf(w, "%s %s %s %s %s %s %s %s %s %s\n",
			pad(s.Dev.Name, 15), pad(s.Policy.Tier, 5), padLeft(fmt.Sprint(len(s.Dev.Interfaces)), 6),
			padLeft(fmt.Sprint(r.Reps), 5), padLeft(fmt.Sprint(r.Stats.PDUs), 5),
			padLeft(fmt.Sprint(r.Stats.Varbinds), 8),
			padLeft(fmtMs(time.Duration(r.Stats.QueueWait)*time.Millisecond), 8),
			padLeft(fmtMs(time.Duration(r.Stats.Latency)*time.Millisecond), 8),
			padLeft(fmtMs(r.Elapsed), 10),
			padLeft(fmt.Sprintf("%.0f%%", share), 7))
	}
	fmt.Fprintln(w, "  PDU = 列数 × ⌈(端口数+1)/reps⌉；那个 +1 是走出子树的那一条，它不落在任何行上")
	fmt.Fprintln(w, "  排队 = 令牌不够干等的秒数；往返 = 设备真的在干活的时间")
}

func firstCollectedRound(c *Collector, dev string) (CycleResult, bool) {
	for _, r := range c.Results {
		if r.Device == dev && !r.Skipped && r.Rows > 0 {
			return r, true
		}
	}
	return CycleResult{}, false
}

// printDeviceSummary 一台设备一行：档位、端口、端口结论、p95 往返。
func printDeviceSummary(w io.Writer, c *Collector, sum Summary) {
	fmt.Fprintf(w, "\n按设备汇总（%d 轮累计）\n", c.Cfg.Cycles)
	fmt.Fprintf(w, "%s %s %s %s %s %s %s %s %s\n",
		pad("device", 15), pad("tier", 5), padLeft("ports", 6), padLeft("请求", 6),
		padLeft("超时", 6), padLeft("tooBig", 7), padLeft("p95往返", 9), padLeft("行数", 6), "结论")
	for _, s := range c.Sessions {
		fmt.Fprintf(w, "%s %s %s %s %s %s %s %s %s\n",
			pad(s.Dev.Name, 15), pad(s.Policy.Tier, 5), padLeft(fmt.Sprint(len(s.Dev.Interfaces)), 6),
			padLeft(fmt.Sprint(s.Totals.Requests), 6), padLeft(fmt.Sprint(s.Totals.Timeouts), 6),
			padLeft(fmt.Sprint(s.Totals.TooBig), 7),
			padLeft(fmt.Sprintf("%dms", s.LatencyP95()), 9),
			padLeft(fmt.Sprint(len(s.LastPorts)), 6), stateLabel(s.State))
	}
	fmt.Fprintf(w, "  %d 列 × %d 端口 = %d 个单元格；累计 %d 条 varbind，最后一次采到的端口合计 %d 行\n",
		len(Columns), totalPorts(c.Fleet), totalPorts(c.Fleet)*len(Columns), sum.Varbinds, sum.Rows)
}

func stateLabel(s DevState) string {
	switch s {
	case StateOK:
		return "正常"
	case StateSlow:
		return "响应慢"
	case StateUnreachable:
		return "不可达"
	default:
		return string(s)
	}
}

// printCycleMatrix 按轮打印，重点看慢设备怎么降频、不可达怎么被隔离。
func printCycleMatrix(w io.Writer, c *Collector) {
	fmt.Fprintln(w, "\n限流与隔离（每轮每台设备）")
	fmt.Fprintf(w, "%s %s %s %s %s %s %s %s\n",
		padLeft("轮", 3), pad("device", 15), pad("结论", 10), padLeft("请求", 6),
		padLeft("排队", 8), padLeft("超时", 6), padLeft("重试", 6), "说明")
	for _, r := range c.Results {
		if r.Skipped {
			fmt.Fprintf(w, "%s %s %s %s %s %s %s %s\n",
				padLeft(fmt.Sprint(r.Cycle), 3), pad(r.Device, 15), pad("跳过", 10),
				padLeft("-", 6), padLeft("-", 8), padLeft("-", 6), padLeft("-", 6), r.Note)
			continue
		}
		note := r.Note
		if note == "" && len(r.Missing) > 0 {
			note = "缺列 " + strings.Join(r.Missing, ",")
		}
		fmt.Fprintf(w, "%s %s %s %s %s %s %s %s\n",
			padLeft(fmt.Sprint(r.Cycle), 3), pad(r.Device, 15), pad(stateLabel(r.State), 10),
			padLeft(fmt.Sprint(r.Stats.Requests), 6),
			padLeft(fmtMs(time.Duration(r.Stats.QueueWait)*time.Millisecond), 8),
			padLeft(fmt.Sprint(r.Stats.Timeouts), 6), padLeft(fmt.Sprint(r.Stats.Retries), 6), note)
	}
	fmt.Fprintln(w, "  排队不是丢包：令牌不够就等，等到再发。丢的是数据，等回来的是时间")
}

// printPortTable 打一台设备的端口表，含 ifSpeed 顶格那一列的证据。
func printPortTable(w io.Writer, c *Collector, focus string) {
	dev := focus
	if dev == "" {
		for _, s := range c.Sessions {
			if s.State == StateOK {
				dev = s.Dev.Name
				break
			}
		}
		if dev == "" && len(c.Sessions) > 0 {
			dev = c.Sessions[0].Dev.Name
		}
	}
	table, ok := c.PortTable(dev)
	if !ok {
		fmt.Fprintf(w, "\n端口表：%s 没有采到数据\n", dev)
		return
	}
	fmt.Fprintf(w, "\n端口表（%s，最后一次采到的那一轮）\n", dev)
	fmt.Fprintf(w, "%s %s %s %s %s %s %s %s\n",
		padLeft("ifIdx", 5), pad("ifName", 14), padLeft("速率", 5), pad("状态", 8),
		padLeft("if_hc_in", 9), padLeft("if_hc_out", 10), padLeft("errs", 4), "if_alias")
	raw := 0
	for _, p := range table.Ports {
		errs := p.InErrors + p.OutErrors
		alias := p.Alias
		if alias == "" {
			alias = "（无 alias）"
		}
		fmt.Fprintf(w, "%s %s %s %s %s %s %s %s\n",
			padLeft(fmt.Sprint(p.IfIndex), 5), pad(p.Name, 14), padLeft(fmtSpeed(p.SpeedMbps()), 5),
			pad(fmt.Sprintf("%s/%s", adminStr(p.Admin), operStr(p.Oper)), 8),
			padLeft(fmtBytes(p.InOctets), 9), padLeft(fmtBytes(p.OutOctets), 10),
			padLeft(fmt.Sprint(errs), 4), alias)
		if raw == 0 && p.SpeedBps >= 4294967295 {
			raw = 1
			fmt.Fprintf(w, "      ↳ %s 的 ifSpeed 读出 %d（顶格）：32 位、单位 bit/s，超过 4.29 Gbps 的端口一律读成这样；速率只能取 ifXTable 的 ifHighSpeed=%d\n",
				p.Name, p.SpeedBps, p.HighSpeedMbps)
		}
	}
	if n := table.Unnamed(); n > 0 {
		fmt.Fprintf(w, "  其中 %d 个端口没有 if_alias：这类端口换板之后只剩 ifIndex 一条线索\n", n)
	}
	if n := table.Anonymous(); n > 0 {
		fmt.Fprintf(w, "  注意：%d 行没有 ifName（该列遍历失败），这些行拼不出稳定主键\n", n)
	}
}

func adminStr(v int) string {
	if v == 1 {
		return "up"
	}
	return "down"
}

func operStr(v int) string {
	switch v {
	case 1:
		return "up"
	case 2:
		return "down"
	case 5:
		return "dormant"
	case 6:
		return "notPresent"
	case 7:
		return "lowerLayer"
	default:
		return fmt.Sprintf("st%d", v)
	}
}

// printReindex 是这一课最贵的那个坑：设备重启后 ifIndex 重排，历史接错对象。
func printReindex(w io.Writer, before, after *Collector, rebootFile string) {
	if after == nil {
		return
	}
	fmt.Fprintf(w, "\n换板重启之后（%s）：同一个 ifIndex，另一个端口\n", rebootFile)
	devs := make([]string, 0, len(after.Fleet.Devices))
	for _, d := range after.Fleet.Devices {
		devs = append(devs, d.Name)
	}
	sort.Strings(devs)
	for _, name := range devs {
		tb, okb := before.PortTable(name)
		ta, oka := after.PortTable(name)
		if !okb || !oka {
			fmt.Fprintf(w, "  %s：前后有一轮没采到，跳过\n", name)
			continue
		}
		idx := JoinPorts(tb, ta, JoinByIndex)
		byName := JoinPorts(tb, ta, JoinByName)
		byAlias := JoinPorts(tb, ta, JoinByAlias)
		fmt.Fprintf(w, "\n  %s（%d 个端口）\n", name, tb.Len())
		fmt.Fprintf(w, "    按 ifIndex 对接：接上 %d 条，其中 %d 条接的是别的端口；断线 %d 条，新增 %d 条\n",
			len(idx.Pairs), idx.WrongObjects(), len(idx.Dropped), len(idx.Fresh))
		if wrong := wrongPairs(idx); len(wrong) > 0 {
			fmt.Fprintf(w, "    %s %s %s %s\n", padLeft("ifIdx", 6), pad("上一轮", 46), pad("本轮", 46), padLeft("速率比", 7))
			shown := 0
			for _, p := range wrong {
				if shown >= 4 {
					break
				}
				shown++
				fmt.Fprintf(w, "    %s %s %s %s\n",
					padLeft(fmt.Sprint(p.After.IfIndex), 6),
					pad(portLabel(p.Before), 46), pad(portLabel(p.After), 46),
					padLeft(fmt.Sprintf("×%.0f", speedRatio(p.Before, p.After)), 7))
			}
			if n := len(wrong) - shown; n > 0 {
				fmt.Fprintf(w, "    …… 还有 %d 条同类错接，%d 条里 %d 条连速率都对不上\n",
					n, len(wrong), len(idx.BigJumps(4)))
			}
		}
		fmt.Fprintf(w, "    按 ifName 对接：接上 %d 条，错对象 %d 条；断线 %d 条，新增 %d 条\n",
			len(byName.Pairs), byName.WrongObjects(), len(byName.Dropped), len(byName.Fresh))
		if len(byName.Dropped) > 0 {
			fmt.Fprintf(w, "      断线的例子：%s → %s（换板后槽位号变了）\n",
				byName.Dropped[0].Name, byName.Fresh[0].Name)
		}
		fmt.Fprintf(w, "    按 ifAlias 对接：接上 %d 条，错对象 %d 条；断线 %d 条，新增 %d 条\n",
			len(byAlias.Pairs), byAlias.WrongObjects(), len(byAlias.Dropped), len(byAlias.Fresh))
		if jumps := idx.BigJumps(4); len(jumps) > 0 {
			p := jumps[0]
			fmt.Fprintf(w, "    ↳ ifIndex %d 上原来是 %s（%s），换板后是 %s（%s）；\n",
				p.After.IfIndex, p.Before.Name, fmtSpeed(p.Before.SpeedMbps()), p.After.Name, fmtSpeed(p.After.SpeedMbps()))
			fmt.Fprintf(w, "      曲线不会断，也不会报错，只会继续画在别的端口身上\n")
		}
	}
	fmt.Fprintln(w, "\n  三条键三种错法：ifIndex 接错对象、ifName 断线、ifAlias 靠人填。")
	fmt.Fprintln(w, "  生产上的做法是 CMDB 给端口发一个不随设备变的 ID，两个名字都只是它的别名。")
}

// wrongPairs 是接错对象的那些对，按速率差异从大到小排：先看最假的那几条。
func wrongPairs(idx JoinReport) []JoinPair {
	var out []JoinPair
	for _, p := range idx.Pairs {
		if p.Before.Alias != p.After.Alias {
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		ri, rj := speedRatio(out[i].Before, out[i].After), speedRatio(out[j].Before, out[j].After)
		if ri != rj {
			return ri > rj
		}
		return out[i].After.IfIndex < out[j].After.IfIndex
	})
	return out
}

func aliasOrDash(p Port) string {
	if p.Alias == "" {
		return "（无 alias）"
	}
	return p.Alias
}

func portLabel(p Port) string {
	alias := p.Alias
	if alias == "" {
		alias = "（无 alias）"
	}
	return fmt.Sprintf("%s %s %s", p.Name, fmtSpeed(p.SpeedMbps()), alias)
}

func speedRatio(before, after Port) float64 {
	a, b := float64(before.SpeedMbps()), float64(after.SpeedMbps())
	if a <= 0 || b <= 0 {
		return 1
	}
	r := b / a
	if r < 1 {
		r = 1 / r
	}
	return r
}

// ---- 产物 ----

// MetricLine 是 out/if_metrics.jsonl 的一行：一轮里一个端口。
type MetricLine struct {
	Cycle     int    `json:"cycle"`
	AtMs      int64  `json:"at_ms"`
	Device    string `json:"device"`
	Key       string `json:"key"`
	IfIndex   int    `json:"if_index"`
	IfName    string `json:"if_name"`
	IfAlias   string `json:"if_alias"`
	SpeedMbps uint64 `json:"speed_mbps"`
	Admin     int    `json:"if_admin_status"`
	Oper      int    `json:"if_oper_status"`
	InOctets  uint64 `json:"if_hc_in_octets"`
	OutOctets uint64 `json:"if_hc_out_octets"`
	InErrors  uint64 `json:"if_in_errors"`
}

// RoundDoc 是产物里的一轮。
type RoundDoc struct {
	Cycle     int      `json:"cycle"`
	State     string   `json:"state"`
	Skipped   bool     `json:"skipped"`
	Reps      int      `json:"max_repetitions"`
	Columns   int      `json:"columns_ok"`
	Missing   []string `json:"columns_missing,omitempty"`
	Rows      int      `json:"rows"`
	Requests  int      `json:"requests"`
	Varbinds  int      `json:"varbinds"`
	PDUs      int      `json:"pdus"`
	TooBig    int      `json:"too_big"`
	Retries   int      `json:"retries"`
	Timeouts  int      `json:"timeouts"`
	QueueMs   int64    `json:"queue_ms"`
	LatencyMs int64    `json:"latency_ms"`
	ElapsedMs int64    `json:"elapsed_ms"`
	Note      string   `json:"note,omitempty"`
}

// DeviceDoc 是产物里的一台设备。
type DeviceDoc struct {
	Name       string     `json:"name"`
	MgmtIP     string     `json:"mgmt_ip"`
	Model      string     `json:"model"`
	Tier       string     `json:"tier"`
	Policy     TierDoc    `json:"policy"`
	FinalState string     `json:"final_state"`
	LatencyP95 int64      `json:"latency_p95_ms"`
	LastPorts  int        `json:"last_ports"`
	Rounds     []RoundDoc `json:"rounds"`
}

// TierDoc 把档位参数一起存：不记参数，产物事后没法复核。
type TierDoc struct {
	MaxRepetitions int     `json:"max_repetitions"`
	RequestsPerSec float64 `json:"requests_per_sec"`
	Burst          int     `json:"burst"`
	TimeoutMs      int64   `json:"timeout_ms"`
	Attempts       int     `json:"attempts"`
}

// CollectDoc 是 out/collect.json。
type CollectDoc struct {
	Note    string      `json:"note"`
	Sample  string      `json:"sample"`
	Wire    string      `json:"wire"`
	Config  CollectCfg  `json:"config"`
	Cycles  []CycleInfo `json:"cycles"`
	Devices []DeviceDoc `json:"devices"`
	Totals  TotalsDoc   `json:"totals"`
}

// CollectCfg 是产物里的运行参数。
type CollectCfg struct {
	Cycles       int   `json:"cycles"`
	IntervalMs   int64 `json:"interval_ms"`
	RepsOverride int   `json:"reps_override"`
}

// TotalsDoc 是合计。
type TotalsDoc struct {
	Requests  int   `json:"requests"`
	Varbinds  int   `json:"varbinds"`
	Rows      int   `json:"rows"`
	TooBig    int   `json:"too_big"`
	Retries   int   `json:"retries"`
	Timeouts  int   `json:"timeouts"`
	QueueMs   int64 `json:"queue_ms"`
	OkDevices int   `json:"ok_devices"`
	Slow      int   `json:"slow_devices"`
	Unreach   int   `json:"unreachable_devices"`
}

// JoinDoc 是两轮对接的结果。
type JoinDoc struct {
	Mode         string `json:"mode"`
	Pairs        int    `json:"pairs"`
	WrongObjects int    `json:"wrong_objects"`
	Dropped      int    `json:"dropped"`
	Fresh        int    `json:"fresh"`
	BigJumps     int    `json:"big_jumps"`
}

// ReindexItem 是一处 ifIndex 换对象。
type ReindexItem struct {
	IfIndex     int    `json:"if_index"`
	BeforeName  string `json:"before_if_name"`
	BeforeAlias string `json:"before_if_alias"`
	BeforeMbps  uint64 `json:"before_speed_mbps"`
	AfterName   string `json:"after_if_name"`
	AfterAlias  string `json:"after_if_alias"`
	AfterMbps   uint64 `json:"after_speed_mbps"`
}

// ReindexDoc 是 out/reindex.json 里的一台设备。
type ReindexDoc struct {
	Device    string        `json:"device"`
	Ports     int           `json:"ports_before"`
	Reindexes []ReindexItem `json:"reindexes"`
	ByIndex   JoinDoc       `json:"by_index"`
	ByName    JoinDoc       `json:"by_name"`
	ByAlias   JoinDoc       `json:"by_alias"`
}

// WriteOutputs 落三份产物，返回全部路径。
func WriteOutputs(in ReportInput) ([]string, error) {
	if err := os.MkdirAll(in.OutDir, 0o755); err != nil {
		return nil, fmt.Errorf("建目录 %s: %w", in.OutDir, err)
	}
	c := in.Collector
	var paths []string

	metricsPath := filepath.Join(in.OutDir, "if_metrics.jsonl")
	f, err := os.Create(metricsPath)
	if err != nil {
		return nil, fmt.Errorf("写 %s: %w", metricsPath, err)
	}
	enc := json.NewEncoder(f)
	roundAt := make(map[int]int64, len(c.Cycles))
	for _, ci := range c.Cycles {
		roundAt[ci.Cycle] = ci.ActualMs
	}
	n := 0
	for _, r := range c.Results {
		if r.Skipped || r.Rows == 0 {
			continue
		}
		at := roundAt[r.Cycle]
		for _, p := range r.Ports {
			line := MetricLine{
				Cycle: r.Cycle, AtMs: at, Device: p.Device, Key: p.Key(),
				IfIndex: p.IfIndex, IfName: p.Name, IfAlias: p.Alias,
				SpeedMbps: p.SpeedMbps(), Admin: p.Admin, Oper: p.Oper,
				InOctets: p.InOctets, OutOctets: p.OutOctets, InErrors: p.InErrors,
			}
			if err := enc.Encode(line); err != nil {
				f.Close()
				return nil, fmt.Errorf("写 %s: %w", metricsPath, err)
			}
			n++
		}
	}
	if err := f.Close(); err != nil {
		return nil, fmt.Errorf("关 %s: %w", metricsPath, err)
	}
	paths = append(paths, fmt.Sprintf("%s  %d 行（每轮每端口一行，key 是 device/ifName）", metricsPath, n))

	collectPath := filepath.Join(in.OutDir, "collect.json")
	doc := buildCollectDoc(c, in.SampleFile, in.WireKind)
	if err := writeJSON(collectPath, doc); err != nil {
		return nil, err
	}
	paths = append(paths, fmt.Sprintf("%s  设备级账本（档位、每轮 PDU、排队、超时、隔离原因）", collectPath))

	if in.Rebooted != nil {
		reindexPath := filepath.Join(in.OutDir, "reindex.json")
		docs := buildReindexDocs(c, in.Rebooted)
		if err := writeJSON(reindexPath, map[string]any{
			"note":    "同 ifIndex 在两轮之间指向了不同端口。三条键的对接结果都在里面。",
			"devices": docs,
		}); err != nil {
			return nil, err
		}
		paths = append(paths, fmt.Sprintf("%s  %d 台设备的对接差异", reindexPath, len(docs)))
	}
	return paths, nil
}

func writeJSON(path string, v any) error {
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化 %s: %w", path, err)
	}
	if err := os.WriteFile(path, append(raw, '\n'), 0o644); err != nil {
		return fmt.Errorf("写 %s: %w", path, err)
	}
	return nil
}

func buildCollectDoc(c *Collector, sample, wire string) CollectDoc {
	sum := c.Summarize()
	doc := CollectDoc{
		Note:   "一轮采集的账本：请求、PDU、排队、超时。数字对不上就说明采集形状有问题。",
		Sample: sample,
		Wire:   wire,
		Config: CollectCfg{
			Cycles:       c.Cfg.Cycles,
			IntervalMs:   c.Cfg.Interval.Milliseconds(),
			RepsOverride: c.Cfg.RepsOverride,
		},
		Cycles: c.Cycles,
		Totals: TotalsDoc{
			Requests: sum.Requests, Varbinds: sum.Varbinds, Rows: sum.Rows,
			TooBig: sum.TooBig, Retries: sum.Retries, Timeouts: sum.Timeouts,
			QueueMs:   sum.QueueWait.Milliseconds(),
			OkDevices: sum.OkDevices, Slow: sum.Slow, Unreach: sum.Unreach,
		},
	}
	for _, s := range c.Sessions {
		d := DeviceDoc{
			Name: s.Dev.Name, MgmtIP: s.Dev.MgmtIP, Model: s.Dev.Model,
			Tier: s.Policy.Tier,
			Policy: TierDoc{
				MaxRepetitions: s.Policy.MaxRepetitions,
				RequestsPerSec: s.Policy.RequestsPerSec,
				Burst:          s.Policy.Burst,
				TimeoutMs:      s.Policy.Timeout.Milliseconds(),
				Attempts:       s.Policy.Attempts,
			},
			FinalState: string(s.State),
			LatencyP95: s.LatencyP95(),
			LastPorts:  len(s.LastPorts),
		}
		for _, r := range c.Results {
			if r.Device != s.Dev.Name {
				continue
			}
			d.Rounds = append(d.Rounds, RoundDoc{
				Cycle: r.Cycle, State: string(r.State), Skipped: r.Skipped, Reps: r.Reps,
				Columns: r.Columns, Missing: r.Missing, Rows: r.Rows,
				Requests: r.Stats.Requests, Varbinds: r.Stats.Varbinds, PDUs: r.Stats.PDUs,
				TooBig: r.Stats.TooBig, Retries: r.Stats.Retries, Timeouts: r.Stats.Timeouts,
				QueueMs: r.Stats.QueueWait, LatencyMs: r.Stats.Latency,
				ElapsedMs: r.Elapsed.Milliseconds(), Note: r.Note,
			})
		}
		doc.Devices = append(doc.Devices, d)
	}
	return doc
}

func buildReindexDocs(before, after *Collector) []ReindexDoc {
	var docs []ReindexDoc
	names := make([]string, 0, len(after.Fleet.Devices))
	for _, d := range after.Fleet.Devices {
		names = append(names, d.Name)
	}
	sort.Strings(names)
	for _, name := range names {
		tb, okb := before.PortTable(name)
		ta, oka := after.PortTable(name)
		if !okb || !oka {
			continue
		}
		doc := ReindexDoc{Device: name, Ports: tb.Len()}
		for _, r := range Reindexes(tb, ta) {
			doc.Reindexes = append(doc.Reindexes, ReindexItem{
				IfIndex:    r.IfIndex,
				BeforeName: r.Before.Name, BeforeAlias: r.Before.Alias, BeforeMbps: r.Before.SpeedMbps(),
				AfterName: r.After.Name, AfterAlias: r.After.Alias, AfterMbps: r.After.SpeedMbps(),
			})
		}
		doc.ByIndex = joinDoc(JoinPorts(tb, ta, JoinByIndex))
		doc.ByName = joinDoc(JoinPorts(tb, ta, JoinByName))
		doc.ByAlias = joinDoc(JoinPorts(tb, ta, JoinByAlias))
		docs = append(docs, doc)
	}
	return docs
}

func joinDoc(r JoinReport) JoinDoc {
	return JoinDoc{
		Mode:         string(r.Mode),
		Pairs:        len(r.Pairs),
		WrongObjects: r.WrongObjects(),
		Dropped:      len(r.Dropped),
		Fresh:        len(r.Fresh),
		BigJumps:     len(r.BigJumps(4)),
	}
}
