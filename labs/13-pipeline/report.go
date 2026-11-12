package main

import (
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"time"
)

// 打印层。所有表格先放 ASCII 列，中文只放行尾或单独的说明行——
// 终端里中文字符宽度按 2 计，混在中间列会把对齐带偏。

func printHeader(w io.Writer, opt options, st LoadStats) {
	validated := st.ParsedPoints - st.RejectedPoint
	fmt.Fprintln(w, "13-pipeline · 编码开销 / 幂等重放 / 窗口乱序 / 容量估算")
	fmt.Fprintf(w, "样本 %s：%d 行，解析成功 %d 批 / %s 点，通过校验 %d 批 / %s 点，拒收 %d 批 / %s 点\n",
		opt.samplesPath, st.Lines, st.ParsedBatches, fmtCount(st.ParsedPoints),
		st.Records, fmtCount(validated), st.Rejected, fmtCount(st.RejectedPoint))
}

func printRejects(w io.Writer, rejects []Reject, quiet bool) {
	if len(rejects) == 0 {
		return
	}
	fmt.Fprintf(w, "\n拒收 %d 批（逐条落 out/rejected.jsonl），按理由分组：\n", len(rejects))
	counts := map[string]int{}
	var order []string
	for _, r := range rejects {
		if _, ok := counts[r.Reason]; !ok {
			order = append(order, r.Reason)
		}
		counts[r.Reason]++
	}
	for _, reason := range order {
		fmt.Fprintf(w, "  %-40s %2d 批\n", reason, counts[reason])
	}
	if quiet {
		return
	}
	fmt.Fprintln(w, "  明细：")
	for _, r := range rejects {
		fmt.Fprintf(w, "    行 %-4d seq=%-4d %-22s %s", r.Line, r.BatchSeq, r.ProbeID, r.Reason)
		if r.Detail != "" {
			fmt.Fprintf(w, "（%s）", r.Detail)
		}
		fmt.Fprintln(w)
	}
}

func printCodec(w io.Writer, r CodecResult) {
	fmt.Fprintf(w, "\n一、编码开销（同一份 %d 批 / %s 点）\n", r.Batches, fmtCount(r.Points))
	fmt.Fprintf(w, "  %-24s %10s %10s %9s %9s %8s\n", "格式", "字节", "每点字节", "编码", "解码", "省下")
	fmt.Fprintf(w, "  %-24s %10d %10.1f %9s %9s %8s\n",
		"JSON encoding/json", r.JSONBytes, r.JSONPerPoint(),
		fmtDur(r.JSONEncode), fmtDur(r.JSONDecode), "1.00×")
	fmt.Fprintf(w, "  %-24s %10d %10.1f %9s %9s %7.2f×\n",
		"PB1 字符串表+varint", r.BinaryBytes, r.BinaryPerPoint(),
		fmtDur(r.BinaryEncode), fmtDur(r.BinaryDecode), r.Ratio())
	fmt.Fprintf(w, "  JSON 的键名与标点占 %.1f%%（每点 %d 字节固定开销），重复的 probe_id/metric_id 值占 %.1f%%（每点 %.1f 字节）\n",
		r.JSONKeyPct(), len(jsonPointSkeleton), r.JSONRepeatPct(),
		div(float64(r.JSONRepeatBytes), float64(r.Points)))
	fmt.Fprintf(w, "  PB1 的字符串表 %d 项共 %s，一次编码只写一份；点位的时间戳存相对批次的毫秒差，值按 0.001 精度 varint\n",
		r.StringTableItems, fmtBytes(float64(r.StringTableBytes)))
	fmt.Fprintf(w, "  耗时取 7 次中位数，机器不同绝对值不同；字节数与比值的结论稳定\n")
}

func printDedup(w io.Writer, d DedupResult, replayOK bool, quiet bool) {
	s := d.Stats
	newBatches := s.BatchesAccepted - s.CompleteBatches
	fmt.Fprintf(w, "\n二、批次与幂等（幂等键 (probe_id, batch_seq, metric_id)）\n")
	fmt.Fprintf(w, "  批次 %d：新 %d · 整批重放 %d · 部分补全 %d · 冲突 %d · 批次号回退 %d\n",
		s.BatchesIn, newBatches, s.ReplayBatches, s.CompleteBatches, s.ConflictBatches, s.RegressedBatches)
	fmt.Fprintf(w, "  点位 %s：进聚合 %s · 幂等键命中 %s · 重复上报 %s · 冲突拒收 %s\n",
		fmtCount(s.PointsIn), fmtCount(s.PointsAccepted), fmtCount(s.PointsDuplicated),
		fmtCount(s.PointsRepeated), fmtCount(s.PointsConflicted))
	fmt.Fprintf(w, "  整包重放：聚合 digest 不变 %s\n", okText(replayOK))
	if s.RestartConflicts > 0 {
		fmt.Fprintf(w, "  冲突里有 %d 批是探针重启后批次号从 1 重数：契约里没有 boot_id，(probe_id, batch_seq) 保不住唯一性\n",
			s.RestartConflicts)
	}
	if quiet {
		return
	}
	fmt.Fprintln(w, "  重放 / 补全 / 冲突明细：")
	for _, n := range s.Notes {
		if n.Outcome == outcomeNew && !n.Regressed && n.Duplicated == 0 {
			continue
		}
		fmt.Fprintf(w, "    行 %-4d %-22s #%-4d %-6s 收 %-3d 重放 %-3d 重报 %-3d %s\n",
			n.Line, n.ProbeID, n.BatchSeq, n.Outcome, n.Accepted, n.Duplicated, n.Repeated, n.Detail)
	}
}

func printWindow(w io.Writer, win WindowResult, devs []MetricDeviation, rows []WatermarkRow, opt options) {
	fmt.Fprintf(w, "\n三、窗口与乱序（窗口 %s，水位线 %s，最大补报延迟 %s，去重后 %s 点 / %d 个窗口）\n",
		shortDur(win.Window), shortDur(win.Watermark), shortDur(win.MaxBackfill),
		fmtCount(win.PointsIn), win.Windows)
	for _, r := range rows {
		mark := ""
		if r.Watermark == win.Watermark {
			mark = "  ← 配的"
		}
		fmt.Fprintf(w, "  水位线 %-6s 丢点 %-4d（超补报 %d）· 丢字节 %-8s · 最大偏差 %+7.2f%%（%s）· 可见延迟 %-6s · 平均滞后 %-5s%s\n",
			shortDur(r.Watermark), r.PointsLate, r.OverBackfill, fmtBytes(float64(r.LateBytes)),
			r.MaxDeviation, dashIfEmpty(r.WorstMetric), shortDur(r.VisibleLag),
			shortDur(r.MeanLateness), mark)
	}

	// 逐指标偏差放在丢得最多的那一档上，那一档才看得出代价。
	if worst := worstRow(rows); worst != nil && !opt.quiet {
		fmt.Fprintf(w, "  逐指标偏差（水位线 %s 对无界水位线，即「一个点都不丢」的真值）：\n", shortDur(worst.Watermark))
		fmt.Fprintf(w, "    %-14s %14s %14s %12s %10s\n", "metric", "真值", "收下", "丢失", "偏差")
		for _, d := range worst.Devs {
			fmt.Fprintf(w, "    %-14s %14.1f %14.1f %12.1f %+9.2f%%\n",
				d.MetricID, d.TruthSum, d.KeptSum, d.LostSum, d.TotalPct)
		}
	}

	printTradeoff(w, rows, win)

	closedWindow, overBackfill := countLateReasons(win.Late)
	fmt.Fprintf(w, "  迟到 %d 点全部有标记（out/late.jsonl）：窗口已关闭 %d · 超出最大补报延迟 %d\n",
		win.PointsLate, closedWindow, overBackfill)
	fmt.Fprintf(w, "  最大迟到 %s，结果可见延迟的地板是「窗口 %s + 水位线 %s」\n",
		shortDur(win.MaxLateness), shortDur(win.Window), shortDur(win.Watermark))
}

// printTradeoff 把「窄了丢数据、宽了丢时间」这两句换成这一份样本算出来的数。
func printTradeoff(w io.Writer, rows []WatermarkRow, win WindowResult) {
	if len(rows) == 0 {
		return
	}
	narrow, wide := rows[0], rows[len(rows)-1]
	fmt.Fprintf(w, "  窄了丢数据：%s 丢 %d 点（%.2f%% 的点没了，最差指标少算 %.2f%%）\n",
		shortDur(narrow.Watermark), narrow.PointsLate,
		div(float64(narrow.PointsLate)*100, float64(win.PointsIn)), absFloat(narrow.MaxDeviation))
	fmt.Fprintf(w, "  宽了丢时间：%s 一个不丢，但结果要多等 %s 才可见\n",
		shortDur(wide.Watermark), shortDur(wide.VisibleLag))
	if win.Watermark >= win.MaxBackfill {
		fmt.Fprintf(w, "  配的 %s ≥ 最大补报延迟 %s %s：关得住控制面承诺的全部补报\n",
			shortDur(win.Watermark), shortDur(win.MaxBackfill), okText(true))
	} else {
		fmt.Fprintf(w, "  配的 %s < 最大补报延迟 %s %s：明知要迟到的数据，水位线提前关门\n",
			shortDur(win.Watermark), shortDur(win.MaxBackfill), okText(false))
	}
}

func printStore(w io.Writer, out StoreOutcome, opt options) {
	fmt.Fprintf(w, "\n四、存储（纯文件分段，一层一个目录：%s/<层>/w<窗口起点>.jsonl）\n", out.Root)
	fmt.Fprintf(w, "  %-8s %6s %10s %12s %18s\n", "层", "段数", "聚合数", "字节", "digest")
	fmt.Fprintf(w, "  %-8s %6d %10d %12s %18s\n", tier1m, out.Stat1m.Segments, out.Stat1m.Aggs,
		fmtBytes(float64(out.Stat1m.Bytes)), out.Stat1m.Digest)
	fmt.Fprintf(w, "  %-8s %6d %10d %12s %18s\n", tier5m, out.Stat5m.Segments, out.Stat5m.Aggs,
		fmtBytes(float64(out.Stat5m.Bytes)), out.Stat5m.Digest)
	fmt.Fprintf(w, "  1 分钟 → 5 分钟是无损降采样：sum 与 count 可加、min/max 取极值，%d 个聚合并成 %d 个\n",
		out.Stat1m.Aggs, out.Rolled)
	fmt.Fprintf(w, "  回放重写：%s %s → %s，%s %s → %s %s\n",
		tier1m, out.Stat1m.Digest, out.Replay1m.Digest, tier5m, out.Stat5m.Digest, out.Replay5m.Digest,
		okText(out.Stat1m.Digest == out.Replay1m.Digest && out.Stat5m.Digest == out.Replay5m.Digest))
	fmt.Fprintf(w, "  保留期演练：把时钟推到 %s（最大事件时间 + %s）\n",
		out.Now.Format(time.RFC3339), shortDur(opt.advance))
	fmt.Fprintf(w, "    %s 保留 %d 天 → 删 %d 段 / 腾 %s，剩 %d 段\n",
		tier1m, opt.retain1m, out.Prune1m.Removed, fmtBytes(float64(out.Prune1m.FreedBytes)), out.Prune1m.Kept)
	fmt.Fprintf(w, "    %s 保留 %d 天 → 删 %d 段 / 腾 %s，剩 %d 段\n",
		tier5m, opt.retain5m, out.Prune5m.Removed, fmtBytes(float64(out.Prune5m.FreedBytes)), out.Prune5m.Kept)
}

func printCapacity(w io.Writer, rep CapacityReport) {
	in := rep.Input
	fmt.Fprintf(w, "\n五、容量估算（明细 %.0f 点/秒 · 每点 %.1f 字节 · 压缩比 %.1f · %d 副本 · 写放大 %.1f · ¥%.0f/TiB/年）\n",
		in.PointsPerSecond, in.BytesPerPoint, in.CompressRatio, in.Replicas, in.WriteAmp, in.PricePerTBYear)
	fmt.Fprintf(w, "  序列数 = %.0f 点/秒 × %s = %.0f；聚合层不是「每秒一个点」，是「每个序列一分钟一个点」\n",
		in.PointsPerSecond, shortDur(in.DetailInterval), rep.Series)
	fmt.Fprintf(w, "  %-8s %6s %9s %9s %8s %11s %11s %11s\n",
		"层级", "间隔", "点/秒", "每点字节", "保留", "原始", "落盘", "年成本")
	for _, t := range rep.Tiers {
		fmt.Fprintf(w, "  %-8s %6s %9.0f %9.1f %6d 天 %11s %11s %10.0f 元\n",
			t.Name, shortDur(t.Interval), t.PointsPerSecond, t.BytesPerPoint, t.RetentionDays,
			fmtBytes(t.RawBytes), fmtBytes(t.StoredBytes), t.CostPerYear)
	}
	fmt.Fprintf(w, "  三层合计：落盘 %s，%.0f 元/年\n", fmtBytes(rep.TotalStoredBytes), rep.TotalCostPerYear)
	fmt.Fprintf(w, "  明细留满一年：落盘 %s，%.0f 元/年，比 %d 天多花 %.0f 元\n",
		fmtBytes(rep.DetailOneYear.StoredBytes), rep.DetailOneYear.CostPerYear,
		rep.Input.RetainDetailDays, rep.DetailExtraCost)
	half := rep.Input
	half.CompressRatio = rep.Input.CompressRatio * 2
	halved := estimateCapacity(half)
	fmt.Fprintf(w, "  压缩比 %.1f → %.1f：三层年成本 %.0f → %.0f 元，压缩比是唯一能打折的参数\n",
		rep.Input.CompressRatio, half.CompressRatio, rep.TotalCostPerYear, halved.TotalCostPerYear)
}

func printSummary(w io.Writer, st LoadStats, d DedupResult, win WindowResult, artifacts []string) {
	validated := st.ParsedPoints - st.RejectedPoint
	fmt.Fprintln(w, "\n对账")
	fmt.Fprintf(w, "  点数守恒：校验通过 %s = 幂等重放 %s + 重复上报 %s + 冲突拒收 %s + 窗口接受 %s + 迟到标记 %s %s\n",
		fmtCount(validated), fmtCount(d.Stats.PointsDuplicated), fmtCount(d.Stats.PointsRepeated),
		fmtCount(d.Stats.PointsConflicted), fmtCount(win.PointsKept), fmtCount(win.PointsLate),
		okText(validated == d.Stats.PointsDuplicated+d.Stats.PointsRepeated+
			d.Stats.PointsConflicted+win.PointsKept+win.PointsLate))
	fmt.Fprintf(w, "  迟到不静默：%d 个迟到点，%d 条标记 %s\n",
		win.PointsLate, len(win.Late), okText(win.PointsLate == len(win.Late)))
	if len(artifacts) > 0 {
		fmt.Fprintf(w, "  落盘：%d 个文件 → %s/（分段在 store/ 下，一层一个目录）\n",
			len(artifacts), filepath.Dir(artifacts[0]))
	}
}

// --- 小工具 ---

func fmtBytes(v float64) string {
	const unit = 1024.0
	units := []string{"B", "KB", "MB", "GB", "TB", "PB"}
	i := 0
	for v >= unit && i < len(units)-1 {
		v /= unit
		i++
	}
	if i == 0 {
		return fmt.Sprintf("%.0f B", v)
	}
	return fmt.Sprintf("%.1f %s", v, units[i])
}

func fmtCount(n int) string {
	s := fmt.Sprintf("%d", n)
	if n < 0 {
		return s
	}
	out := make([]byte, 0, len(s)+len(s)/3)
	for i, c := range []byte(s) {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, c)
	}
	return string(out)
}

// shortDur 压到秒级。窗口与水位线都是整秒的，小数点没有意义；
// 超过两天的量按天写，保留期演练那句「+ 9600h0m0s」没人想看。
func shortDur(d time.Duration) string {
	if d <= 0 {
		return "0s"
	}
	if d >= 48*time.Hour {
		return fmt.Sprintf("%.0f 天", d.Hours()/24)
	}
	if d < time.Second {
		return d.String()
	}
	return d.Round(time.Second).String()
}

func fmtDur(d time.Duration) string {
	if d < time.Millisecond {
		return fmt.Sprintf("%dµs", d.Microseconds())
	}
	return fmt.Sprintf("%.2fms", float64(d.Microseconds())/1000)
}

func okText(ok bool) string {
	if ok {
		return "✓"
	}
	return "✗"
}

func dashIfEmpty(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func sortDurations(ds []time.Duration) {
	sort.Slice(ds, func(i, j int) bool { return ds[i] < ds[j] })
}

// worstRow 找丢点最多的一档，用于打印逐指标偏差。
func worstRow(rows []WatermarkRow) *WatermarkRow {
	var best *WatermarkRow
	for i := range rows {
		if best == nil || rows[i].PointsLate > best.PointsLate {
			best = &rows[i]
		}
	}
	if best == nil || best.PointsLate == 0 {
		return nil
	}
	return best
}

// countLateReasons 把迟到标记按理由分组。两种理由的修法完全不同：
// 窗口已关闭 → 放宽水位线；超出补报延迟 → 改控制面的补报承诺。
func countLateReasons(late []LateMark) (closed, overBackfill int) {
	for _, l := range late {
		if l.Reason == lateOverBackfill {
			overBackfill++
			continue
		}
		closed++
	}
	return closed, overBackfill
}
