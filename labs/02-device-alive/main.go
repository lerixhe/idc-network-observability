// 实验 02：一万台设备的存活监测。
//
// 一次探测只有两个结果：通、不通。这个实验把「不通」拆成三种原因——
// 设备死了、设备在重启、探针自己瞎了——手段只有一个：同源相关性。
//
// 模拟器注入真实故障时间线，判定层只看探测结果，最后拿两份对账。
// 全程虚拟时钟，不发一个真实数据包，不碰外网。
package main

import (
	"flag"
	"fmt"
	"math/rand"
	"os"
	"strings"
	"time"
)

func main() {
	var (
		devices      = flag.Int("n", 400, "设备数；-n 10000 是这一课的目标规模")
		interval     = flag.Duration("interval", 30*time.Second, "探测周期 P")
		duration     = flag.Duration("duration", 30*time.Minute, "仿真总时长")
		failN        = flag.Int("fail", 3, "连续 N 次失败判失联")
		okM          = flag.Int("ok", 2, "连续 M 次成功判恢复")
		corr         = flag.Float64("corr", 0.5, "同源组失败比例阈值，达到即判探测侧故障")
		corrMin      = flag.Int("corr-min", 4, "同源组设备数下限，少于它不做相关性判定")
		corrTicks    = flag.Int("corr-ticks", 2, "同源组连续满足可疑条件多少轮才确认探测侧故障；必须小于 -fail")
		rebootWindow = flag.Duration("reboot-suspect", 6*time.Minute, "离线时长不超过它，恢复时标疑似重启")
		seed         = flag.Int64("seed", 1, "随机种子；同种子同剧本必定同结果")
		scenarioName = flag.String("scenario", ScenarioAll, "剧本："+strings.Join(ScenarioNames(), " / "))
		planFile     = flag.String("plan", "samples/scenarios.json", "剧本时间参数文件；文件不存在则用内置默认值")
		outDir       = flag.String("out", "out", "产物目录")
		quiet        = flag.Bool("quiet", false, "不打印事件明细")
	)
	flag.Parse()

	cfg := Config{
		Devices:          *devices,
		Interval:         *interval,
		Duration:         *duration,
		FailThreshold:    *failN,
		RecoverThreshold: *okM,
		CorrThreshold:    *corr,
		CorrMinPeers:     *corrMin,
		CorrTicks:        *corrTicks,
		RebootSuspect:    *rebootWindow,
		Seed:             *seed,
	}
	if err := run(cfg, *scenarioName, *planFile, *outDir, !*quiet); err != nil {
		fmt.Fprintln(os.Stderr, "失败:", err)
		os.Exit(1)
	}
}

func run(cfg Config, scenarioName, planFile, outDir string, verbose bool) error {
	if err := cfg.Validate(); err != nil {
		return err
	}

	plan, planSource, err := LoadScenarioPlan(planFile)
	if err != nil {
		return err
	}
	sc, err := BuildScenario(scenarioName, ScenarioConfig{
		Devices:  cfg.Devices,
		Ticks:    cfg.Ticks(),
		Interval: cfg.Interval,
		Seed:     cfg.Seed,
		Plan:     plan,
	})
	if err != nil {
		return err
	}

	m := NewMonitor(cfg, sc.Devices)
	rng := rand.New(rand.NewSource(cfg.Seed))
	begin := time.Now()
	m.Run(func(i, tick int) bool { return sc.Probe(i, tick, rng) })
	elapsed := time.Since(begin)

	ev := Evaluate(sc, m)
	PrintReport(os.Stdout, sc, m, ev, ReportOptions{
		Verbose:      verbose,
		ScenarioFile: planSource,
		Elapsed:      elapsed,
	})

	snap := m.Snapshot(sc.Name)
	eventsPath, snapPath, err := WriteOutputs(outDir, snap, m.Events())
	if err != nil {
		return err
	}
	fmt.Printf("\n产物\n  %s  %d 条事件（工单来源）\n  %s  %d 台设备终态（看板来源）\n",
		eventsPath, len(m.Events()), snapPath, len(snap.Devices))
	return nil
}
