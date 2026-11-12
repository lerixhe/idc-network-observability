// 实验 13：管道与存储。
//
// 一条数据从探针到磁盘要过四关，四关各有一笔账：
//
//	编码   同一批点用 JSON 与紧凑二进制各编一遍，字节数与耗时并排看
//	幂等   (probe_id, batch_seq, metric_id) 去重，整批重放不翻倍，部分写入能补全
//	窗口   固定窗口 + 水位线，迟到、乱序、重复混着来，丢了多少、偏了多少都要算出来
//	容量   每秒点数 × 每点字节 × 压缩比 × 保留天数，三层保留各一张账
//
// 全程零依赖、不碰外网、不发一个数据包。窗口与水位线的默认值来自 12-task 的契约：
// 断连后可补 15 分钟，水位线就 15 分钟。
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func main() {
	var opt options
	flag.StringVar(&opt.samplesPath, "samples", "samples/batches.jsonl", "上报批次样本，一行一批 JSON")
	flag.StringVar(&opt.outDir, "out", "out", "产物目录，留空不落盘")
	flag.StringVar(&opt.only, "only", "all", "只跑一段：all|codec|dedup|window|store|capacity")
	flag.BoolVar(&opt.quiet, "quiet", false, "只打结论行，不打逐项明细")
	flag.DurationVar(&opt.window, "window", time.Minute, "固定窗口宽度")
	flag.DurationVar(&opt.watermark, "watermark", 15*time.Minute, "水位线宽度，默认等于最大补报延迟")
	flag.DurationVar(&opt.maxBackfill, "max-backfill", 15*time.Minute, "控制面承诺的最大补报延迟")
	flag.StringVar(&opt.compare, "compare", "", "水位线宽度对比列表，逗号分隔；留空按最大补报延迟生成")
	flag.DurationVar(&opt.advance, "advance", 400*24*time.Hour,
		"保留期演练：时钟从最大事件时间往后推多少；默认 400 天，刚好越过 1 分钟层的 1 年保留期")
	flag.Float64Var(&opt.pps, "pps", 12000, "容量估算：明细层每秒点数")
	flag.Float64Var(&opt.bytesPerPoint, "bytes", 0, "容量估算：明细层每点字节，0 表示用 PB1 实测值")
	flag.Float64Var(&opt.compress, "compress", 3.0, "容量估算：压缩比")
	flag.IntVar(&opt.replicas, "replicas", 3, "容量估算：副本数")
	flag.Float64Var(&opt.writeAmp, "write-amp", 2.0, "容量估算：写放大")
	flag.Float64Var(&opt.price, "price", 800, "容量估算：每 TiB 每年，元")
	flag.DurationVar(&opt.detailInterval, "detail-interval", 15*time.Second, "容量估算：明细采样间隔")
	flag.Float64Var(&opt.aggBytesFactor, "agg-bytes-factor", 2.0,
		"容量估算：聚合点每点字节 ÷ 明细点每点字节")
	flag.IntVar(&opt.retainDetail, "retain-detail", 30, "明细层保留天数")
	flag.IntVar(&opt.retain1m, "retain-1m", 365, "1 分钟层保留天数")
	flag.IntVar(&opt.retain5m, "retain-5m", 1095, "5 分钟层保留天数")
	flag.Parse()

	if err := run(os.Stdout, opt); err != nil {
		fmt.Fprintln(os.Stderr, "失败:", err)
		os.Exit(1)
	}
}

type options struct {
	samplesPath string
	outDir      string
	only        string
	quiet       bool

	window      time.Duration
	watermark   time.Duration
	maxBackfill time.Duration
	compare     string
	advance     time.Duration

	pps            float64
	bytesPerPoint  float64
	compress       float64
	replicas       int
	writeAmp       float64
	price          float64
	detailInterval time.Duration
	aggBytesFactor float64
	retainDetail   int
	retain1m       int
	retain5m       int
}

type sections struct {
	codec    bool
	dedup    bool
	window   bool
	store    bool
	capacity bool
}

func parseSections(s string) (sections, error) {
	switch strings.TrimSpace(s) {
	case "", "all":
		return sections{codec: true, dedup: true, window: true, store: true, capacity: true}, nil
	case "codec":
		return sections{codec: true}, nil
	case "dedup":
		return sections{dedup: true}, nil
	case "window":
		return sections{window: true}, nil
	case "store":
		return sections{store: true}, nil
	case "capacity":
		return sections{capacity: true}, nil
	default:
		return sections{}, fmt.Errorf("-only 只认 all|codec|dedup|window|store|capacity，收到 %q", s)
	}
}

func run(w io.Writer, opt options) error {
	sec, err := parseSections(opt.only)
	if err != nil {
		return err
	}
	if opt.window <= 0 || opt.window%time.Second != 0 {
		return fmt.Errorf("-window 必须是整秒且大于 0，当前 %s", opt.window)
	}
	if opt.maxBackfill <= 0 {
		return fmt.Errorf("-max-backfill 必须大于 0，当前 %s", opt.maxBackfill)
	}
	if opt.watermark < 0 {
		return fmt.Errorf("-watermark 不能为负，当前 %s", opt.watermark)
	}
	if opt.detailInterval <= 0 {
		return fmt.Errorf("-detail-interval 必须大于 0，当前 %s", opt.detailInterval)
	}

	recs, rejects, loadStats, err := loadBatches(opt.samplesPath)
	if err != nil {
		return err
	}
	if len(recs) == 0 {
		return fmt.Errorf("%s 里没有通过校验的批次", opt.samplesPath)
	}

	codecRes, err := compareCodecs(recs)
	if err != nil {
		return err
	}

	dedupRes := dedupAll(recs)
	bytesPerPoint := codecRes.BinaryPerPoint()
	if opt.bytesPerPoint > 0 {
		bytesPerPoint = opt.bytesPerPoint
	}

	truth := truthWindow(dedupRes.Records, opt.window)
	win := runWindow(dedupRes.Records, opt.window, opt.watermark, opt.maxBackfill)
	devs := deviation(truth, win)

	widths, err := parseCompare(opt.compare, opt.maxBackfill)
	if err != nil {
		return err
	}
	rows := compareWatermarks(dedupRes.Records, opt.window, widths, opt.maxBackfill, bytesPerPoint)

	_, replayDigest := replayIdempotent(recs, opt.window, opt.watermark, opt.maxBackfill)

	capRep := estimateCapacity(CapacityInput{
		PointsPerSecond:  opt.pps,
		BytesPerPoint:    bytesPerPoint,
		CompressRatio:    opt.compress,
		Replicas:         opt.replicas,
		WriteAmp:         opt.writeAmp,
		PricePerTBYear:   opt.price,
		DetailInterval:   opt.detailInterval,
		AggBytesFactor:   opt.aggBytesFactor,
		RetainDetailDays: opt.retainDetail,
		Retain1mDays:     opt.retain1m,
		Retain5mDays:     opt.retain5m,
	})

	var storeOut *StoreOutcome
	if sec.store && opt.outDir != "" {
		out, err := runStore(filepath.Join(opt.outDir, "store"), win.Aggs, opt, win.MaxEvent)
		if err != nil {
			return err
		}
		storeOut = &out
	}

	printHeader(w, opt, loadStats)
	printRejects(w, rejects, opt.quiet)
	if sec.codec {
		printCodec(w, codecRes)
	}
	if sec.dedup {
		printDedup(w, dedupRes, replayDigest == aggsDigest(win.Aggs), opt.quiet)
	}
	if sec.window {
		printWindow(w, win, devs, rows, opt)
	}
	if storeOut != nil {
		printStore(w, *storeOut, opt)
	}
	if sec.capacity {
		printCapacity(w, capRep)
	}

	var artifacts []string
	if opt.outDir != "" {
		artifacts, err = writeArtifacts(opt, rejects, dedupRes, win, capRep, loadStats)
		if err != nil {
			return err
		}
	}

	if err := assertConservation(loadStats, dedupRes, win); err != nil {
		return err
	}
	if replayDigest != aggsDigest(win.Aggs) {
		return fmt.Errorf("整包重放后聚合不一致：单次 %s ≠ 重放 %s",
			aggsDigest(win.Aggs), replayDigest)
	}
	printSummary(w, loadStats, dedupRes, win, artifacts)
	return nil
}

// replayIdempotent 把整包数据再喂一遍，比较两次的聚合摘要。
// 「回放」在流处理里就是这件事：历史数据重放，结果必须一模一样。
func replayIdempotent(recs []Record, window, watermark, maxBackfill time.Duration) (string, string) {
	pass := func(stream []Record) string {
		d := dedupAll(stream)
		w := runWindow(d.Records, window, watermark, maxBackfill)
		return aggsDigest(w.Aggs)
	}
	twice := make([]Record, 0, 2*len(recs))
	twice = append(twice, recs...)
	twice = append(twice, recs...)
	return pass(recs), pass(twice)
}

// StoreOutcome 是存储那一段的全部数字。
type StoreOutcome struct {
	Root     string
	Stat1m   TierStat
	Stat5m   TierStat
	Replay1m TierStat
	Replay5m TierStat
	Prune1m  PruneStat
	Prune5m  PruneStat
	Rolled   int
	Now      time.Time
}

// runStore 落盘两层、按回放再写一遍、然后按保留期清一次。
func runStore(root string, aggs []Agg, opt options, maxEvent time.Time) (StoreOutcome, error) {
	st := NewStore(root)
	out := StoreOutcome{Root: root}
	var err error
	if out.Stat1m, err = st.Write(tier1m, aggs); err != nil {
		return out, err
	}
	rolled := rollup5m(aggs)
	out.Rolled = len(rolled)
	if out.Stat5m, err = st.Write(tier5m, rolled); err != nil {
		return out, err
	}
	// 回放：同一批聚合再写一遍，段内容必须是同一份，digest 不变才叫回放安全。
	if out.Replay1m, err = st.Write(tier1m, aggs); err != nil {
		return out, err
	}
	if out.Replay5m, err = st.Write(tier5m, rolled); err != nil {
		return out, err
	}

	out.Now = maxEvent.Add(opt.advance)
	if out.Prune1m, err = st.Prune(tier1m, out.Now, days(opt.retain1m)); err != nil {
		return out, err
	}
	if out.Prune5m, err = st.Prune(tier5m, out.Now, days(opt.retain5m)); err != nil {
		return out, err
	}
	return out, nil
}

func days(n int) time.Duration { return time.Duration(n) * 24 * time.Hour }

// parseCompare 解析水位线对比列表。留空就按控制面承诺的补报上限生成四档：
// 一分钟（明显太窄）、上限的三分之一、上限（推荐）、上限的两倍（明显太宽）。
func parseCompare(s string, maxBackfill time.Duration) ([]time.Duration, error) {
	if strings.TrimSpace(s) == "" {
		return []time.Duration{time.Minute, maxBackfill / 3, maxBackfill, 2 * maxBackfill}, nil
	}
	var out []time.Duration
	seen := map[time.Duration]struct{}{}
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		d, err := time.ParseDuration(part)
		if err != nil {
			return nil, fmt.Errorf("-compare 里的 %q 不是合法时长: %w", part, err)
		}
		if d < 0 {
			return nil, fmt.Errorf("-compare 里的 %q 是负数", part)
		}
		if _, ok := seen[d]; ok {
			continue
		}
		seen[d] = struct{}{}
		out = append(out, d)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("-compare 里没有有效时长")
	}
	sortDurations(out)
	return out, nil
}

// assertConservation 核一件事：进来的点和出去的账对得上。
// 少一个点都要报错退出——「静默丢数据」是这个实验要防的第一号事故。
func assertConservation(st LoadStats, d DedupResult, win WindowResult) error {
	validatedPoints := st.ParsedPoints - st.RejectedPoint
	if validatedPoints != d.Stats.PointsIn {
		return fmt.Errorf("点数不守恒：通过校验 %d ≠ 进幂等 %d", validatedPoints, d.Stats.PointsIn)
	}
	sum := d.Stats.PointsAccepted + d.Stats.PointsDuplicated +
		d.Stats.PointsRepeated + d.Stats.PointsConflicted
	if d.Stats.PointsIn != sum {
		return fmt.Errorf("去重不守恒：进 %d ≠ 收 %d + 重放 %d + 重复上报 %d + 冲突 %d",
			d.Stats.PointsIn, d.Stats.PointsAccepted, d.Stats.PointsDuplicated,
			d.Stats.PointsRepeated, d.Stats.PointsConflicted)
	}
	if d.Stats.PointsAccepted != win.PointsIn {
		return fmt.Errorf("去重后点数 %d ≠ 窗口输入 %d", d.Stats.PointsAccepted, win.PointsIn)
	}
	if win.PointsIn != win.PointsKept+win.PointsLate {
		return fmt.Errorf("窗口不守恒：进 %d ≠ 收 %d + 迟 %d",
			win.PointsIn, win.PointsKept, win.PointsLate)
	}
	if len(win.Late) != win.PointsLate {
		return fmt.Errorf("迟到标记数 %d ≠ 迟到点数 %d，有数据被静默丢了", len(win.Late), win.PointsLate)
	}
	return nil
}

// summaryFile 是落盘的机器可读摘要，README 里的数字都从这里来。
type summaryFile struct {
	Samples          string  `json:"samples"`
	Lines            int     `json:"lines"`
	ParsedBatches    int     `json:"parsed_batches"`
	ParsedPoints     int     `json:"parsed_points"`
	RejectedBatches  int     `json:"rejected_batches"`
	RejectedPoints   int     `json:"rejected_points"`
	PointsAccepted   int     `json:"points_accepted"`
	PointsDuplicated int     `json:"points_duplicated"`
	PointsRepeated   int     `json:"points_repeated"`
	PointsConflicted int     `json:"points_conflicted"`
	WindowSeconds    float64 `json:"window_seconds"`
	WatermarkSeconds float64 `json:"watermark_seconds"`
	PointsKept       int     `json:"points_kept"`
	PointsLate       int     `json:"points_late"`
	Aggregates       int     `json:"aggregates"`
	AggregateDigest  string  `json:"aggregate_digest"`
}

func writeArtifacts(opt options, rejects []Reject, d DedupResult, win WindowResult,
	capRep CapacityReport, st LoadStats) ([]string, error) {
	if err := os.MkdirAll(opt.outDir, 0o755); err != nil {
		return nil, err
	}
	var paths []string
	add := func(name string) string {
		p := filepath.Join(opt.outDir, name)
		paths = append(paths, p)
		return p
	}

	if err := writeJSONLines(add("rejected.jsonl"), rejects); err != nil {
		return nil, err
	}

	notes := make([]BatchNote, 0, len(d.Stats.Notes))
	for _, n := range d.Stats.Notes {
		if n.Outcome != outcomeNew || n.Regressed || n.Duplicated > 0 {
			notes = append(notes, n)
		}
	}
	if err := writeJSONLines(add("dedup.jsonl"), notes); err != nil {
		return nil, err
	}
	if err := writeJSONLines(add("late.jsonl"), win.Late); err != nil {
		return nil, err
	}
	if err := writeJSONLines(add("aggregates.jsonl"), win.Aggs); err != nil {
		return nil, err
	}

	capPath := add("capacity.json")
	if err := writeJSONFile(capPath, capRep); err != nil {
		return nil, err
	}

	sum := summaryFile{
		Samples:          opt.samplesPath,
		Lines:            st.Lines,
		ParsedBatches:    st.ParsedBatches,
		ParsedPoints:     st.ParsedPoints,
		RejectedBatches:  st.Rejected,
		RejectedPoints:   st.RejectedPoint,
		PointsAccepted:   d.Stats.PointsAccepted,
		PointsDuplicated: d.Stats.PointsDuplicated,
		PointsRepeated:   d.Stats.PointsRepeated,
		PointsConflicted: d.Stats.PointsConflicted,
		WindowSeconds:    win.Window.Seconds(),
		WatermarkSeconds: win.Watermark.Seconds(),
		PointsKept:       win.PointsKept,
		PointsLate:       win.PointsLate,
		Aggregates:       len(win.Aggs),
		AggregateDigest:  aggsDigest(win.Aggs),
	}
	if err := writeJSONFile(add("summary.json"), sum); err != nil {
		return nil, err
	}
	return paths, nil
}
