// 实验 06：SNMP 表遍历、档位限流与主键映射。
//
// 三件事串成一条链：GetBulk 按列把 ifTable + ifXTable 摸下来，
// 按设备能力分档限流（排队不丢包），再用 ifName/ifAlias 把 ifIndex 这张
// 当轮句柄翻译成稳定主键。最后拿一份「换板重启之后」的样本，看同一个
// ifIndex 怎么指向了另一个端口。
//
// 默认全程用样本 + 内置 agent，虚拟时钟，不连网、不碰设备。
package main

import (
	"flag"
	"fmt"
	"os"
	"time"
)

type options struct {
	fleetPath  string
	rebootPath string
	outDir     string
	focus      string
	only       string
	wire       string
	agentAddr  string
	cfg        Config
	rebootRun  bool
}

func main() {
	var (
		fleetPath  = flag.String("fleet", "samples/fleet.json", "设备应答样本")
		rebootPath = flag.String("rebooted", "samples/fleet-rebooted.json", "换板重启后的样本（留空跳过重排演示）")
		cycles     = flag.Int("cycles", 6, "采集轮数")
		interval   = flag.Duration("interval", 30*time.Second, "采集周期")
		reps       = flag.Int("reps", 0, "覆盖档位的 max-repetitions（>0 时生效，用来演练 tooBig 背退）")
		timeout    = flag.Duration("timeout", 0, "覆盖档位的请求超时（>0 时生效）")
		seed       = flag.Int64("seed", 1, "随机种子；同种子同样本必定同结果")
		outDir     = flag.String("out", "out", "产物目录")
		focus      = flag.String("device", "", "端口表重点打哪台设备；默认取第一台正常的")
		only       = flag.String("only", "", "只采这一台设备（调试与 UDP 模式用）")
		wire       = flag.String("wire", "sample", "传输层：sample（内存 agent）/ udp（127.0.0.1 上的真实套接字）")
		agentAddr  = flag.String("agent", "127.0.0.1:0", "udp 模式下内置 agent 的监听地址")
	)
	flag.Parse()

	rebootSet := false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == "rebooted" {
			rebootSet = true
		}
	})

	opts := options{
		fleetPath:  *fleetPath,
		rebootPath: *rebootPath,
		outDir:     *outDir,
		focus:      *focus,
		only:       *only,
		wire:       *wire,
		agentAddr:  *agentAddr,
		cfg: Config{
			Cycles:          *cycles,
			Interval:        *interval,
			RepsOverride:    *reps,
			TimeoutOverride: *timeout,
			Seed:            *seed,
		},
		rebootRun: !rebootSet,
	}
	if opts.wire == "udp" && !rebootSet {
		// 真实时钟下把换板那两台再采一遍要几十秒，默认跳过。
		opts.rebootPath = ""
	}
	if err := run(opts); err != nil {
		fmt.Fprintln(os.Stderr, "失败:", err)
		os.Exit(1)
	}
}

func run(opts options) error {
	begin := time.Now()

	fleet, err := LoadFleet(opts.fleetPath)
	if err != nil {
		return err
	}
	if opts.only != "" {
		if err := keepOnly(fleet, opts.only); err != nil {
			return err
		}
	}
	start, err := sampleStart(fleet)
	if err != nil {
		return err
	}

	clk, transport, closeFn, clockKind, err := setupTransport(opts, fleet, start)
	if err != nil {
		return err
	}
	defer closeFn()

	col, err := NewCollector(fleet, transport, clk, opts.cfg)
	if err != nil {
		return err
	}
	col.Run()

	in := ReportInput{
		Collector:   col,
		SampleFile:  opts.fleetPath,
		RebootFile:  opts.rebootPath,
		OutDir:      opts.outDir,
		FocusDevice: opts.focus,
		WireKind:    opts.wire,
		ClockKind:   clockKind,
	}

	if opts.rebootPath != "" {
		reboot, err := loadRebooted(opts, start)
		if err != nil {
			return err
		}
		in.Rebooted = reboot
	}

	in.WallTime = time.Since(begin)
	PrintReport(os.Stdout, in)

	paths, err := WriteOutputs(in)
	if err != nil {
		return err
	}
	fmt.Printf("\n产物\n")
	for _, p := range paths {
		fmt.Printf("  %s\n", p)
	}
	return nil
}

// setupTransport 选传输层与时钟。样本模式用虚拟时钟，udp 模式用真时钟。
func setupTransport(opts options, fleet *Fleet, start time.Time) (Clock, Transport, func(), string, error) {
	switch opts.wire {
	case "sample":
		return NewVirtualClock(start), NewSampleTransport(fleet, opts.cfg.Seed), func() {}, "虚拟", nil
	case "udp":
		srv, err := ServeUDP(opts.agentAddr, fleet, opts.cfg.Seed)
		if err != nil {
			return nil, nil, nil, "", err
		}
		client, err := DialUDP(srv.Addr())
		if err != nil {
			srv.Close()
			return nil, nil, nil, "", err
		}
		fmt.Printf("内置 agent 监听 %s，采集器走真实 UDP 往返\n\n", srv.Addr())
		return RealClock{}, client, func() { client.Close(); srv.Close() }, "真实", nil
	default:
		return nil, nil, nil, "", fmt.Errorf("未知传输层 %q（只有 sample / udp）", opts.wire)
	}
}

// loadRebooted 采一轮换板重启后的样本。它只用一轮：要看的是「同一批端口现在
// 挂在哪些 ifIndex 上」，不是又一个时间序列。
func loadRebooted(opts options, start time.Time) (*Collector, error) {
	fleet, err := LoadFleet(opts.rebootPath)
	if err != nil {
		return nil, err
	}
	if opts.only != "" {
		// 只采一台时，换板样本里没它就不做重排演示。
		if err := keepOnly(fleet, opts.only); err != nil {
			return nil, nil
		}
	}
	rebootStart, err := sampleStart(fleet)
	if err != nil {
		return nil, err
	}
	if rebootStart.Before(start) {
		rebootStart = start.Add(24 * time.Hour)
	}
	cfg := opts.cfg
	cfg.Cycles = 1
	col, err := NewCollector(fleet, NewSampleTransport(fleet, opts.cfg.Seed+1), NewVirtualClock(rebootStart), cfg)
	if err != nil {
		return nil, err
	}
	col.Run()
	return col, nil
}

// keepOnly 把样本收窄到一台设备。-only 是调试旋钮，不是采集策略。
func keepOnly(f *Fleet, name string) error {
	d, ok := f.DeviceByName(name)
	if !ok {
		return fmt.Errorf("样本里没有设备 %s", name)
	}
	f.Devices = []Device{*d}
	return nil
}

// sampleStart 用样本里的采集时刻当虚拟时钟起点，报表上的时间才对得上。
func sampleStart(f *Fleet) (time.Time, error) {
	if f.CollectedAt == "" {
		return time.Date(2026, 5, 12, 10, 12, 0, 0, time.UTC), nil
	}
	t, err := time.Parse(time.RFC3339, f.CollectedAt)
	if err != nil {
		return time.Time{}, fmt.Errorf("样本的 collected_at %q 不是 RFC3339: %w", f.CollectedAt, err)
	}
	return t, nil
}
