package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// ReportOptions 控制报告里打印到哪一层。
type ReportOptions struct {
	Verbose bool
	Elapsed time.Duration
}

// PrintReport 打印整份报告。
func PrintReport(w io.Writer, cfg Config, sc *Scenario, results []DispatchResult, sig *Signals, v Verdict, opts ReportOptions) {
	fmt.Fprintf(w, "12-task · 任务管控仿真：拉取 vs 推送\n\n")

	fmt.Fprintf(w, "探针 %d 台 / %d 个机房；任务 %d 条；仿真 %s\n",
		len(sc.Probes), countSites(sc.Probes), len(sc.Tasks), dur(cfg.Duration))
	fmt.Fprintf(w, "变更 %d 条（内容变更 %d + 换点 %d）；上报周期 %s；最大补报延迟 %s\n",
		len(sc.Changes), sc.Truth.Revises, sc.Truth.Moves, dur(cfg.ReportInterval), dur(cfg.MaxReportDelay))
	fmt.Fprintf(w, "失败模式：%s\n", strings.Join(failModeNames(cfg.FailModes), "、"))

	fmt.Fprintf(w, "\n一、探针清单能看见什么\n")
	fmt.Fprintf(w, "  在役 %d 台；带离线窗口 %d 台；已退役 %d 台（%s）\n",
		len(sc.Probes)-sc.Truth.DecommissionedProbes, sc.Truth.OfflineProbes,
		sc.Truth.DecommissionedProbes, strings.Join(sc.Truth.StaleProbes, ","))
	for _, p := range sc.Probes {
		if len(p.Offline) == 0 && p.DecommissionedAt < 0 {
			continue
		}
		var segs []string
		for _, wnd := range p.Offline {
			segs = append(segs, dur(wnd.From)+"–"+dur(wnd.To))
		}
		extra := ""
		if p.DecommissionedAt >= 0 {
			extra = " 退役@" + dur(p.DecommissionedAt)
		}
		if len(segs) > 0 {
			extra += " 离线 " + strings.Join(segs, " ")
		}
		fmt.Fprintf(w, "    %-14s %-8s%s\n", p.ProbeID, p.Site, extra)
	}

	printDispatch(w, cfg, results)
	printScale(w, cfg, results)
	printFailures(w, cfg, sc, sig)
	printVerdict(w, v)

	if opts.Elapsed > 0 {
		fmt.Fprintf(w, "\n仿真耗时 %s（%d 台探针、%d 条任务、%d 条变更，全程虚拟时钟）\n",
			dur(opts.Elapsed), len(sc.Probes), len(sc.Tasks), len(sc.Changes))
	}

	if opts.Verbose {
		printChangeDetail(w, results)
	}
}

// printDispatch 是分发模式的对比表。只跑一种模式时就是一张单列表。
func printDispatch(w io.Writer, cfg Config, results []DispatchResult) {
	fmt.Fprintf(w, "\n二、变更到生效\n")
	fmt.Fprintf(w, "  %s %s %s %s %s %s %s %s\n",
		padCJK("模式", 16), padRight("请求数", 13), padRight("变更", 11),
		padRight("p50", 9), padRight("p90", 9), padRight("p95", 9),
		padRight("max", 9), padRight("超补报窗口", 11))
	for _, r := range results {
		fmt.Fprintf(w, "  %s %s %s %s %s %s %s %s\n",
			padCJK(r.Label, 16), padRight(fmtInt(r.totalRequests()), 13),
			padRight(strconv.Itoa(r.Stats.N), 11),
			padRight(dur(r.Stats.P50), 9), padRight(dur(r.Stats.P90), 9),
			padRight(dur(r.Stats.P95), 9), padRight(dur(r.Stats.Max), 9),
			padRight(strconv.Itoa(r.Stale), 11))
	}

	fmt.Fprintf(w, "\n  请求构成\n")
	for _, r := range results {
		if r.Mode == string(ModePull) {
			fmt.Fprintf(w, "    拉取：轮询 %s 次 + 下发 %d 条 = %s\n",
				fmtInt(r.PollRequests), r.DispatchMessages, fmtInt(r.totalRequests()))
			continue
		}
		fmt.Fprintf(w, "    推送：心跳 %s 次 + 建连 %d 次 + 下发 %d 条 = %s（连接持有合计 %s = %d 条 × %s）\n",
			fmtInt(r.HeartbeatRequests), r.HandshakeRequests, r.DispatchMessages,
			fmtInt(r.totalRequests()), dur(time.Duration(r.ConnectionSeconds*float64(time.Second))),
			r.Probes, dur(cfg.Duration))
	}

	labels := make([]string, 0, len(results))
	hists := make([][]Bucket, len(results))
	for i, r := range results {
		labels = append(labels, r.Label)
		hists[i] = Histogram(latencies(r, false))
	}
	fmt.Fprintf(w, "\n  延迟直方图（%s）\n", strings.Join(labels, " | "))
	for i, b := range hists[0] {
		var line strings.Builder
		fmt.Fprintf(&line, "    %-6s", b.Label)
		for j := range results {
			fmt.Fprintf(&line, " %4d %-12s", hists[j][i].Count, bar(hists[j][i].Count))
		}
		fmt.Fprintln(w, strings.TrimRight(line.String(), " "))
	}
	fmt.Fprintf(w, "  尾巴都是同一类：变更落在探针离线的时刻，只能等它回来；超过 %s 就直接成缺口\n",
		dur(cfg.MaxReportDelay))
	if results[0].Stale == 0 {
		fmt.Fprintf(w, "  本次没有变更超过补报窗口。想看它的样子：make tail（把上限压到 1m）\n")
	}
	if len(results) == 2 && results[1].totalRequests() > 0 {
		fmt.Fprintf(w, "  请求数比（%s / %s）%.1f 倍：拉取赢在简单，推送赢在延迟与请求数\n",
			results[0].Label, results[1].Label,
			float64(results[0].totalRequests())/float64(results[1].totalRequests()))
	}
}

// printScale 是十万探针那笔账。规模变，请求数与内存变，延迟分布不变。
func printScale(w io.Writer, cfg Config, results []DispatchResult) {
	fmt.Fprintf(w, "\n三、十万探针的账（按本次参数外推，延迟分布不随规模变）\n")
	fmt.Fprintf(w, "  %s", padCJK("项", 24))
	for _, r := range results {
		fmt.Fprintf(w, " %s", padCJK(r.Label, 20))
	}
	fmt.Fprintf(w, "\n")

	hourly := make([]int64, len(results))
	conn := make([]int64, len(results))
	qps := make([]int64, len(results))
	for i, r := range results {
		hourly[i], conn[i] = r.Extrapolate(scaleProbes, cfg)
		qps[i] = int64(float64(hourly[i])/3600 + 0.5)
	}
	row := func(name string, cells []string) {
		fmt.Fprintf(w, "  %s", padCJK(name, 24))
		for _, c := range cells {
			fmt.Fprintf(w, " %s", padRight(c, 20))
		}
		fmt.Fprintf(w, "\n")
	}
	row("每小时请求数", mapInts(hourly, fmtInt))
	row("平均 QPS", mapInts(qps, fmtInt))
	row("控制面长连接", mapInts(conn, func(v int64) string {
		if v == 0 {
			return "0"
		}
		return fmtInt(scaleProbes)
	}))
	row("连接内存", mapInts(conn, func(v int64) string {
		if v == 0 {
			return "—"
		}
		return byteText(v)
	}))
	row("变更到生效 p50", mapResults(results, func(r DispatchResult) string { return dur(r.Stats.P50) }))
	row("变更到生效 max", mapResults(results, func(r DispatchResult) string { return dur(r.Stats.Max) }))

	fmt.Fprintf(w, "  一条连接 14 KiB = 状态 2 KiB + 收发缓冲 4 KiB + 协程栈 8 KiB；十万条 = %s\n",
		byteText(int64(scaleProbes*connTotalBytes)))
	fmt.Fprintf(w, "  拉取不打散轮询时刻，十万探针在整秒上同时醒，峰值 QPS 等于平均值；按 64 个相位打散后是 1/64\n")
}

// dispWidth 算显示宽度：中文按两格。用 %-Ns 对齐中文列会一路歪，只能自己数。
func dispWidth(s string) int {
	w := 0
	for _, r := range s {
		if r < 128 {
			w++
			continue
		}
		w += 2
	}
	return w
}

func padCJK(s string, width int) string {
	if d := dispWidth(s); d < width {
		return s + strings.Repeat(" ", width-d)
	}
	return s
}

// padRight 与 padCJK 同义，区别只是调用处的意图：纯 ASCII 列用它。
func padRight(s string, width int) string { return padCJK(s, width) }

func mapInts(in []int64, f func(int64) string) []string {
	out := make([]string, len(in))
	for i, v := range in {
		out[i] = f(v)
	}
	return out
}

func mapResults(in []DispatchResult, f func(DispatchResult) string) []string {
	out := make([]string, len(in))
	for i, r := range in {
		out[i] = f(r)
	}
	return out
}

// printFailures 打四个失败模式。
func printFailures(w io.Writer, cfg Config, sc *Scenario, sig *Signals) {
	fmt.Fprintf(w, "\n四、四个失败模式\n")

	// 1 重复下发
	fmt.Fprintf(w, "\n  1) 重复下发：同一个任务被两个探针同时接到\n")
	if !cfg.Has(FailDuplicate) {
		fmt.Fprintf(w, "     未注入（-fail-modes 里没有 duplicate）\n")
	} else {
		fmt.Fprintf(w, "     注入 %d 起（每 2 次换点丢一次撤销消息），检出 %d 起\n",
			sc.Truth.DuplicateDispatches, len(sig.Duplicates))
		for i, e := range sig.Duplicates {
			if i == 3 {
				fmt.Fprintf(w, "     ……余下 %d 起见 events.jsonl\n", len(sig.Duplicates)-i)
				break
			}
			fmt.Fprintf(w, "     %s 探针 %v 重叠 %vs 重复点位 %v 个\n",
				e.Subject, e.Evidence["probes"], e.Evidence["overlap_s"], e.Evidence["duplicated_points"])
		}
		fmt.Fprintf(w, "     信号 duplicate-dispatch；幂等键按 probe_id 去重，跨探针的这份重复它压不住\n")
	}

	// 2 全量重推
	fmt.Fprintf(w, "\n  2) 全量重推：一条全局变更把全部任务重发一遍\n")
	if sig.Repush == nil {
		fmt.Fprintf(w, "     未注入（-fail-modes 里没有 full-repush）\n")
	} else {
		s := sig.Repush.Sample
		t := sig.Repush.Scale
		fmt.Fprintf(w, "     本样本：%d 条任务 / %d 条变更 / %s 窗口，稳态 %s QPS，限流后峰值 %s QPS，放大 %s 倍，排空 %s（样本太小，倍数只看量级）\n",
			s.Tasks, s.Changes, dur(cfg.Duration), round2Text(s.SteadyQPS), round2Text(s.PeakQPS),
			round2Text(s.Amplification), dur(s.Drain))
		fmt.Fprintf(w, "     十万探针：%d 条任务 / %s 窗口，稳态 %s QPS，峰值 %s QPS，放大 %s 倍，排空 %s\n",
			t.Tasks, dur(scaleWindow), round2Text(t.SteadyQPS), round2Text(t.PeakQPS),
			round2Text(t.Amplification), dur(t.Drain))
		fmt.Fprintf(w, "     信号 full-repush；排空期间增量变更排在队尾，等 %s 才算下发\n", dur(t.Drain))
	}

	// 3 坏点替换震荡
	fmt.Fprintf(w, "\n  3) 坏点替换震荡：目标设备整段不应答，换哪台探针都失败\n")
	if sig.FlapOn == nil {
		fmt.Fprintf(w, "     未注入（-fail-modes 里没有 flap）\n")
	} else {
		fmt.Fprintf(w, "     坏点任务 %s\n", strings.Join(sc.Truth.FlapTasks, "、"))
		for _, tr := range sig.FlapOn.PerTask {
			fmt.Fprintf(w, "     %-20s 抑制开：迁移 %d 次 → %s，断档 %d 段 %s\n",
				tr.TaskID, tr.Moves, strings.Join(tr.Trail, "→"), tr.GapSegments, dur(tr.GapDuration))
		}
		for _, tr := range sig.FlapOff.PerTask {
			fmt.Fprintf(w, "     %-20s 抑制关：迁移 %d 次，断档 %d 段 %s，%s\n",
				tr.TaskID, tr.Moves, tr.GapSegments, dur(tr.GapDuration), tr.Stopped)
		}
		fmt.Fprintf(w, "     信号 task-reassign / task-fault；抑制阈值 %d 次，连续换点不再换，转报目标设备故障\n",
			cfg.FlapReplaceLimit)
		fmt.Fprintf(w, "     少丢的数据：抑制开 %s，抑制关 %s，差 %s\n",
			dur(sig.FlapOn.GapDuration), dur(sig.FlapOff.GapDuration),
			dur(sig.FlapOff.GapDuration-sig.FlapOn.GapDuration))
	}

	// 4 过期任务不回收
	fmt.Fprintf(w, "\n  4) 过期任务不回收：探针下线了，任务还挂在它名下\n")
	if sig.Stale == nil {
		fmt.Fprintf(w, "     本次没有可注入的退役探针（样本里没有 decommissioned_at_s 且名下有任务）\n")
	} else {
		s := sig.Stale
		fmt.Fprintf(w, "     %s 在 %s 退役，名下 %d 条任务；静默超过 TTL %s 判定过期\n",
			s.ProbeID, dur(s.DecommissionedAt), len(s.Tasks), dur(s.TTL))
		fmt.Fprintf(w, "     不回收：%d 条任务仍挂在退役探针上，静默 %s，缺口到仿真结束\n",
			s.Off.StaleAssignments, dur(s.Off.GapDuration))
		fmt.Fprintf(w, "     回收：TTL 到点改派，缺口 %s，%s 恢复出数\n",
			dur(s.On.GapDuration), dur(s.On.RecoveredAt))
		fmt.Fprintf(w, "     信号 stale-assignment；判据只有一条：最后一次心跳距今超过 TTL\n")
	}
}

// printVerdict 打与真值的对账。
func printVerdict(w io.Writer, v Verdict) {
	fmt.Fprintf(w, "\n五、与真相对账\n")
	for _, l := range v.Lines() {
		mark := "对上"
		if !l.OK {
			mark = "对不上"
		}
		fmt.Fprintf(w, "  %s%s%s%s\n", padCJK(l.What, 20), padCJK(l.Got, 32), padCJK(l.Want, 38), mark)
	}
	if v.Batches.Produced > 0 {
		fmt.Fprintf(w, "  批次补报：最大迟到 %s，迟到超一个上报周期 %d 批，超补报窗口丢弃 %d 批\n",
			dur(v.Batches.MaxLateness), v.Batches.LateBatches, v.Batches.OverWindow)
	}
}

// printChangeDetail 打逐条变更的生效情况。
func printChangeDetail(w io.Writer, results []DispatchResult) {
	fmt.Fprintf(w, "\n六、逐条变更\n")
	fmt.Fprintf(w, "  %s%s%s", padCJK("task/rev", 22), padCJK("kind", 9), padCJK("变更时刻", 12))
	for _, r := range results {
		fmt.Fprintf(w, " %s", padCJK(r.Label+"延迟", 18))
	}
	fmt.Fprintf(w, "\n")
	byMode := make([]map[string]time.Duration, len(results))
	for i, r := range results {
		byMode[i] = appLatency(r)
	}
	for _, a := range assignments(results[0]) {
		k := fmt.Sprintf("%s/rev%d", a.TaskID, a.Revision)
		fmt.Fprintf(w, "  %s%s%s", padCJK(k, 22), padCJK(string(a.Kind), 9), padCJK(dur(a.ChangeAt), 12))
		for _, m := range byMode {
			fmt.Fprintf(w, " %s", padRight(dur(m[k]), 18))
		}
		fmt.Fprintf(w, "\n")
	}
}

// assignments 取主路径的派活记录，重复下发那份额外的不算。
func assignments(r DispatchResult) []Application {
	out := make([]Application, 0, len(r.Apply))
	for _, a := range r.Apply {
		if a.Role == RoleAssign && !a.Dup {
			out = append(out, a)
		}
	}
	return out
}

func appLatency(r DispatchResult) map[string]time.Duration {
	out := make(map[string]time.Duration, len(r.Apply))
	for _, a := range r.Apply {
		if a.Role == RoleAssign && !a.Dup {
			out[fmt.Sprintf("%s/rev%d", a.TaskID, a.Revision)] = a.Latency
		}
	}
	return out
}

// latencies 取出主路径的延迟序列。
func latencies(r DispatchResult, all bool) []time.Duration {
	out := make([]time.Duration, 0, len(r.Apply))
	for _, a := range r.Apply {
		if !all && (a.Role != RoleAssign || a.Dup) {
			continue
		}
		out = append(out, a.Latency)
	}
	return out
}

// WriteOutputs 落两份产物。batches.jsonl 是 13-pipeline 的输入，events.jsonl 是信号流。
func WriteOutputs(dir string, deliveries []Delivery, events []Event) (batchPath, eventPath string, err error) {
	if dir == "" {
		return "", "", nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", "", fmt.Errorf("建目录 %s 失败：%w", dir, err)
	}
	batchPath = filepath.Join(dir, "batches.jsonl")
	if err := writeJSONL(batchPath, func(enc *json.Encoder) error {
		for _, d := range deliveries {
			if err := enc.Encode(d.Batch); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return "", "", err
	}
	eventPath = filepath.Join(dir, "events.jsonl")
	if err := writeJSONL(eventPath, func(enc *json.Encoder) error {
		for _, e := range events {
			if err := enc.Encode(e); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return "", "", err
	}
	return batchPath, eventPath, nil
}

func writeJSONL(path string, fn func(*json.Encoder) error) error {
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("建 %s 失败：%w", path, err)
	}
	defer f.Close()
	bw := bufio.NewWriter(f)
	enc := json.NewEncoder(bw)
	enc.SetEscapeHTML(false)
	if err := fn(enc); err != nil {
		return fmt.Errorf("写 %s 失败：%w", path, err)
	}
	return bw.Flush()
}

// ── 排版小工具 ──────────────────────────────────────────────────

func failModeNames(modes []FailMode) []string {
	if len(modes) == 0 {
		return []string{"none"}
	}
	out := make([]string, 0, len(modes))
	for _, m := range modes {
		out = append(out, string(m))
	}
	return out
}

func countSites(probes []Probe) int {
	seen := make(map[string]bool, 4)
	for _, p := range probes {
		seen[p.Site] = true
	}
	return len(seen)
}

// fmtInt 按千分位分组，读十万这个量级时省一次数手指。
func fmtInt(v int64) string {
	s := strconv.FormatInt(v, 10)
	neg := strings.HasPrefix(s, "-")
	if neg {
		s = s[1:]
	}
	var b strings.Builder
	for i, ch := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(ch)
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}

func byteText(b int64) string {
	switch {
	case b >= 1<<30:
		return fmt.Sprintf("%.1f GiB", float64(b)/(1<<30))
	case b >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(b)/(1<<20))
	case b >= 1<<10:
		return fmt.Sprintf("%.1f KiB", float64(b)/(1<<10))
	}
	return fmt.Sprintf("%d B", b)
}

func round2Text(v float64) string {
	if v >= 100 {
		return fmt.Sprintf("%.0f", v)
	}
	return strconv.FormatFloat(round2(v), 'f', 2, 64)
}

// bar 画一根直方图柱，一格代表一个样本，最多 40 格。
func bar(n int) string {
	if n <= 0 {
		return ""
	}
	if n > 40 {
		return strings.Repeat("█", 40) + "…"
	}
	return strings.Repeat("█", n)
}
