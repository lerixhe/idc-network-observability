// 实验 00：第一个探针。
//
// 干三件事：对一组目标做 TCP 建连探测、记录每次的往返时延（RTT）、
// 把原始样本落成 JSONL，再算一份汇总（p50 / p99 / 丢包率）。
//
// 默认不碰外网：起一个本地 listener 当被探测对象，跑完就退。
// 想测真机就传 -targets，格式见 samples/probe-matrix.json。
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Target 是被探测对象：给人看的名字 + 可拨号的地址。
type Target struct {
	Name string `json:"name"`
	Addr string `json:"addr"`
}

// Sample 是一条原始测量。故意不聚合——聚合放到第二篇的指标层，
// 采集层的职责只是「不失真地记下来」。
type Sample struct {
	Target string `json:"target"`
	Addr   string `json:"addr"`
	Seq    int    `json:"seq"`
	UnixMs int64  `json:"unix_ms"`
	RTTus  int64  `json:"rtt_us"`
	OK     bool   `json:"ok"`
	Err    string `json:"err,omitempty"`
}

func main() {
	var (
		targetsFile = flag.String("targets", "", "探测目标表（JSON），留空则对本地 listener 自测")
		count       = flag.Int("count", 20, "每个目标探测次数")
		interval    = flag.Duration("interval", 50*time.Millisecond, "同一目标的探测间隔")
		timeout     = flag.Duration("timeout", 500*time.Millisecond, "单次探测超时")
		workers     = flag.Int("workers", 8, "并发探测的协程数")
		outDir      = flag.String("out", "out", "样本输出目录")
	)
	flag.Parse()

	targets, cleanup, err := loadTargets(*targetsFile)
	if err != nil {
		fmt.Fprintln(os.Stderr, "读取目标失败:", err)
		os.Exit(1)
	}
	defer cleanup()

	samples := probeAll(targets, *count, *interval, *timeout, *workers)

	if err := writeSamples(*outDir, samples); err != nil {
		fmt.Fprintln(os.Stderr, "写样本失败:", err)
		os.Exit(1)
	}

	printReport(targets, samples)
	fmt.Printf("\n样本已写入 %s\n", filepath.Join(*outDir, "samples.jsonl"))
}

// loadTargets 读目标表；没给文件就用本地 listener 自测，保证实验零外部依赖。
func loadTargets(path string) ([]Target, func(), error) {
	if path == "" {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return nil, nil, err
		}
		go func() {
			for {
				conn, err := ln.Accept()
				if err != nil {
					return
				}
				_ = conn.Close()
			}
		}()
		return []Target{{Name: "self", Addr: ln.Addr().String()}}, func() { _ = ln.Close() }, nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	var targets []Target
	if err := json.Unmarshal(data, &targets); err != nil {
		return nil, nil, err
	}
	if len(targets) == 0 {
		return nil, nil, errors.New("目标表是空的")
	}
	return targets, func() {}, nil
}

// probeAll 按目标并发探测。每个目标按 interval 串行打点，目标之间并行，
// 协程数由 workers 限制——这就是生产中「百万任务调度」的雏形。
func probeAll(targets []Target, count int, interval, timeout time.Duration, workers int) []Sample {
	if workers < 1 {
		workers = 1
	}
	sem := make(chan struct{}, workers)
	out := make(chan Sample, count*len(targets))

	var wg sync.WaitGroup
	for _, t := range targets {
		wg.Add(1)
		go func(t Target) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			for i := 0; i < count; i++ {
				out <- probeOnce(t, i, timeout)
				if i < count-1 {
					time.Sleep(interval)
				}
			}
		}(t)
	}

	go func() {
		wg.Wait()
		close(out)
	}()

	samples := make([]Sample, 0, count*len(targets))
	for s := range out {
		samples = append(samples, s)
	}
	return samples
}

// probeOnce 一次 TCP 建连探测：dial 成功即关，耗时就当 RTT。
// 用 TCP 而不是 ICMP，是为了不依赖 raw socket 权限，容器里也能跑。
func probeOnce(t Target, seq int, timeout time.Duration) Sample {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	start := time.Now()
	d := net.Dialer{}
	conn, err := d.DialContext(ctx, "tcp", t.Addr)
	rtt := time.Since(start)

	s := Sample{
		Target: t.Name,
		Addr:   t.Addr,
		Seq:    seq,
		UnixMs: start.UnixMilli(),
		RTTus:  rtt.Microseconds(),
		OK:     err == nil,
	}
	if err != nil {
		s.Err = err.Error()
		return s
	}
	_ = conn.Close()
	return s
}

func writeSamples(dir string, samples []Sample) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.Create(filepath.Join(dir, "samples.jsonl"))
	if err != nil {
		return err
	}
	defer f.Close()
	return writeJSONL(f, samples)
}

func writeJSONL(w io.Writer, samples []Sample) error {
	enc := json.NewEncoder(w)
	for _, s := range samples {
		if err := enc.Encode(s); err != nil {
			return err
		}
	}
	return nil
}
