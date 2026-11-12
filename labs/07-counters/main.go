// 实验 07：计数器速率还原。
//
// 一份端口计数器采样，三种算法各算一遍速率：简单差值、按时间归一、回绕检测加插值。
// 三列结果并排打出来，分岔点自己会露出来——丢样让 v1 与 v2 分岔，回绕让 v2 与 v3 分岔。
//
// 同一份样本里还埋了设备重启、sysUpTime 回绕、乱序、丢样，以及一个 10 秒采一次的 10G 口。
// 最后一段错包计数单独归因：线缆、模块、拥塞、主机，四个方向各自的证据长什么样。
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

func main() {
	var (
		samplesPath = flag.String("samples", "samples/counters.json", "计数器采样序列")
		errPath     = flag.String("errors", "samples/errors.json", "错包计数样本")
		truthPath   = flag.String("truth", "samples/truth.json", "样本注入的真值，用于对账；留空跳过")
		outDir      = flag.String("out", "out", "产物目录，留空不落盘")
		portFilter  = flag.String("port", "", "只跑匹配这个子串的端口（device/if_name）")
		quiet       = flag.Bool("quiet", false, "只打结论行，不打每个区间的明细")
		wrapTable   = flag.Bool("wraps", false, "打印回绕周期表并退出")
		poll        = flag.Duration("poll", 0, "采集周期，配合 -wraps 判断配置是否踩红线")
	)
	flag.Parse()

	if err := run(os.Stdout, options{
		samplesPath: *samplesPath,
		errPath:     *errPath,
		truthPath:   *truthPath,
		outDir:      *outDir,
		portFilter:  *portFilter,
		quiet:       *quiet,
		wrapTable:   *wrapTable,
		poll:        *poll,
	}); err != nil {
		fmt.Fprintln(os.Stderr, "失败:", err)
		os.Exit(1)
	}
}

type options struct {
	samplesPath string
	errPath     string
	truthPath   string
	outDir      string
	portFilter  string
	quiet       bool
	wrapTable   bool
	poll        time.Duration
}

// wrapTableSpeeds 是回绕周期表要覆盖的端口速率。1G 与 10G 是这一课的两个基准点。
var wrapTableSpeeds = []uint64{1e9, 10e9, 25e9, 40e9, 100e9, 400e9}

func run(w io.Writer, opt options) error {
	if opt.wrapTable {
		printWrapTable(w, wrapTableSpeeds, opt.poll)
		return nil
	}

	series, err := loadSeries(opt.samplesPath)
	if err != nil {
		return err
	}
	selected := make([]Series, 0, len(series))
	for _, s := range series {
		if opt.portFilter != "" && !strings.Contains(s.Key(), opt.portFilter) {
			continue
		}
		selected = append(selected, s)
	}
	if len(selected) == 0 {
		return fmt.Errorf("%s 里没有匹配 %q 的端口", opt.samplesPath, opt.portFilter)
	}

	results := make([]SeriesRates, 0, len(selected))
	for _, s := range selected {
		r, err := ComputeRates(s, 0)
		if err != nil {
			return err
		}
		results = append(results, r)
	}

	fmt.Fprintf(w, "计数器速率还原：%d 个端口 / %d 个区间\n", len(results), countIntervals(results))
	printCompare(w, results, !opt.quiet)

	fmt.Fprintln(w, "\n采集周期红线")
	warnCount := printWarnings(w, results)
	if warnCount > 0 {
		fmt.Fprintf(w, "  以上 %d 条告警；对应的区段落盘时 trusted=false，面板宁可留缺口，不要补数\n", warnCount)
	}

	mismatch := 0
	if opt.truthPath != "" {
		truth, err := loadTruth(opt.truthPath)
		if err != nil {
			return err
		}
		mismatch = printReconcile(w, results, truth)
	}

	attrs, err := loadErrCounters(opt.errPath)
	if err != nil {
		return err
	}
	attributed := make([]Attribution, 0, len(attrs))
	for _, c := range attrs {
		if opt.portFilter != "" && !strings.Contains(c.Key(), opt.portFilter) {
			continue
		}
		attributed = append(attributed, Attribute(c))
	}
	printAttribution(w, attributed)

	if opt.outDir != "" {
		ratesPath, err := writeRates(opt.outDir, results)
		if err != nil {
			return fmt.Errorf("写速率结论失败：%w", err)
		}
		attrResultPath, err := writeAttribution(opt.outDir, attributed)
		if err != nil {
			return fmt.Errorf("写归因结论失败：%w", err)
		}
		fmt.Fprintf(w, "\n产物：%s、%s\n", ratesPath, attrResultPath)
	}

	// 对账不上说明判定逻辑与样本注入的真值不一致，这是需要人看的事，不能当成功退出。
	if mismatch > 0 {
		return fmt.Errorf("与真值对账有 %d 处不一致", mismatch)
	}
	return nil
}

func countIntervals(results []SeriesRates) int {
	n := 0
	for _, r := range results {
		n += len(r.Points)
	}
	return n
}
