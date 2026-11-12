// 实验 12：任务管控仿真——拉取 vs 推送，外加四个失败模式。
//
// 控制面要回答两个问题：变更多久到得了探针，以及到不了的时候长什么样。
// 这个实验把探针、任务、变更全部虚拟化：不发一个真实包，不碰外网，
// 变更延迟、请求数、控制面峰值都是算出来的，能自己代入参数再算一遍。
//
// 产物 batches.jsonl 按 13-pipeline 的数据契约落盘，那个实验直接拿它当输入。
package main

import (
	"flag"
	"fmt"
	"os"
	"time"
)

func main() {
	var (
		probes         = flag.Int("n", 12, "探针数；-n 100000 是十万探针那一档")
		duration       = flag.Duration("duration", 10*time.Minute, "仿真总时长")
		modes          = flag.String("modes", "both", "分发模式：pull / push / both")
		poll           = flag.Duration("poll", 5*time.Second, "拉取式的轮询周期")
		heartbeat      = flag.Duration("heartbeat", 30*time.Second, "长连接的心跳周期")
		pushDelay      = flag.Duration("push-delay", 20*time.Millisecond, "长连接单程推送延迟，抖动不超过它本身")
		reconnect      = flag.Duration("reconnect", 3*time.Second, "探针掉线后重连退避")
		reportInterval = flag.Duration("report-interval", time.Minute, "探针上报周期")
		maxReportDelay = flag.Duration("max-report-delay", 15*time.Minute, "最大补报延迟，超过就丢")
		failModes      = flag.String("fail-modes", "all", "注入哪些失败模式：duplicate,full-repush,flap,stale / all / none")
		flapLimit      = flag.Int("flap-limit", 2, "连续换点多少次之后不再换，转报故障")
		staleTTL       = flag.Duration("stale-ttl", 2*time.Minute, "探针静默多久算过期")
		ctlRate        = flag.Float64("ctl-rate", 500, "控制面每秒最多下发多少条，全量重推的排空时间由它决定")
		seed           = flag.Int64("seed", 1, "随机种子；同种子同参数必定同结果")
		roster         = flag.String("roster", "samples/probes.json", "探针清单")
		tasksPath      = flag.String("tasks", "samples/tasks.json", "任务集")
		outDir         = flag.String("out", "out", "产物目录；空串表示只算不落盘")
		quiet          = flag.Bool("quiet", false, "不打印逐条变更明细")
	)
	flag.Parse()

	pull, push, err := ParseModes(*modes)
	if err != nil {
		fmt.Fprintln(os.Stderr, "失败:", err)
		os.Exit(1)
	}
	fms, err := ParseFailModes(*failModes)
	if err != nil {
		fmt.Fprintln(os.Stderr, "失败:", err)
		os.Exit(1)
	}
	cfg := Config{
		Probes:           *probes,
		Duration:         *duration,
		Pull:             pull,
		Push:             push,
		Poll:             *poll,
		Heartbeat:        *heartbeat,
		PushDelay:        *pushDelay,
		ReconnectBackoff: *reconnect,
		ReportInterval:   *reportInterval,
		MaxReportDelay:   *maxReportDelay,
		FailModes:        fms,
		FlapReplaceLimit: *flapLimit,
		StaleTTL:         *staleTTL,
		CtlRate:          *ctlRate,
		Seed:             *seed,
	}
	if err := cfg.Validate(); err != nil {
		fmt.Fprintln(os.Stderr, "失败:", err)
		os.Exit(1)
	}
	if err := run(cfg, *roster, *tasksPath, *outDir, !*quiet); err != nil {
		fmt.Fprintln(os.Stderr, "失败:", err)
		os.Exit(1)
	}
}

func run(cfg Config, rosterPath, tasksPath, outDir string, verbose bool) error {
	roster, sites, err := LoadRoster(rosterPath)
	if err != nil {
		return err
	}
	tasks, err := LoadTasks(tasksPath)
	if err != nil {
		return err
	}
	sc, err := BuildScenario(cfg, roster, sites, tasks)
	if err != nil {
		return err
	}
	if outDir != "" {
		// 护栏放在仿真前面：十万探针的批次是几百万行，跑到一半 OOM 比拒绝更糟。
		if err := CheckBatchScale(cfg); err != nil {
			return err
		}
	}

	begin := time.Now()
	results := make([]DispatchResult, 0, 2)
	if cfg.Pull {
		results = append(results, SimulatePull(cfg, sc.Probes, sc.Changes))
	}
	if cfg.Push {
		results = append(results, SimulatePush(cfg, sc.Probes, sc.Changes))
	}
	// 第一个启用的模式就是主口径：失败模式检测与上报批次都从它取。
	apps := results[0].Apply
	sig := AnalyzeFailures(cfg, sc, apps)

	var deliveries []Delivery
	var bstats BatchStats
	if outDir != "" {
		deliveries, bstats = MaterializeBatches(cfg, sc.Probes, apps)
	}
	verdict := Reconcile(cfg, sc, apps, results[0].Undeployed, sig, bstats)
	elapsed := time.Since(begin)

	PrintReport(os.Stdout, cfg, sc, results, sig, verdict, ReportOptions{Verbose: verbose, Elapsed: elapsed})

	batchPath, eventPath, err := WriteOutputs(outDir, deliveries, sig.Events)
	if err != nil {
		return err
	}
	if batchPath != "" {
		fmt.Printf("\n产物\n  %s  %d 行批次（13-pipeline 直接吃这份）\n  %s  %d 条信号\n",
			batchPath, len(deliveries), eventPath, len(sig.Events))
	}
	return nil
}
