package main

import (
	"bytes"
	"encoding/json"
	"math"
	"net"
	"os"
	"strings"
	"testing"
	"time"
)

func TestPercentile(t *testing.T) {
	cases := []struct {
		name    string
		samples []int64
		p       float64
		want    int64
	}{
		{"空输入", nil, 99, 0},
		{"单样本 p99", []int64{42}, 99, 42},
		{"十样本 p50", []int64{10, 20, 30, 40, 50, 60, 70, 80, 90, 100}, 50, 50},
		{"十样本 p99", []int64{10, 20, 30, 40, 50, 60, 70, 80, 90, 100}, 99, 100},
		{"乱序输入 p90", []int64{90, 10, 50, 30, 70}, 90, 90},
		{"p0 取下界", []int64{7, 8, 9}, 0, 7},
	}
	for _, c := range cases {
		if got := Percentile(c.samples, c.p); got != c.want {
			t.Errorf("%s: Percentile(%v, %v) = %d, 期望 %d", c.name, c.samples, c.p, got, c.want)
		}
	}
}

func TestLossRate(t *testing.T) {
	cases := []struct {
		total, ok int
		want      float64
	}{
		{10, 10, 0},
		{10, 9, 0.1},
		{0, 0, 0},
		{10, 0, 1},
		{10, 12, 0}, // 成功数不该超过总数，越界按全成功处理
	}
	for _, c := range cases {
		if got := LossRate(c.total, c.ok); math.Abs(got-c.want) > 1e-9 {
			t.Errorf("LossRate(%d, %d) = %v, 期望 %v", c.total, c.ok, got, c.want)
		}
	}
}

func TestProbeOnce(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()

	s := probeOnce(Target{Name: "self", Addr: ln.Addr().String()}, 0, time.Second)
	if !s.OK {
		t.Fatalf("本地 listener 应当探测成功，实际错误: %s", s.Err)
	}
	if s.RTTus <= 0 {
		t.Errorf("RTT 应当是正数，实际 %d", s.RTTus)
	}
}

func TestProbeOnceTimeout(t *testing.T) {
	// 127.0.0.1:1 通常无人监听，建连会被拒绝；无论拒绝还是超时，都算失败样本
	s := probeOnce(Target{Name: "dead", Addr: "127.0.0.1:1"}, 0, 100*time.Millisecond)
	if s.OK {
		t.Skip("本机 127.0.0.1:1 居然可连，跳过")
	}
	if s.Err == "" {
		t.Error("失败样本必须带上错误原因")
	}
}

func TestProbeAllCountsAndEndpoints(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()

	targets := []Target{{Name: "self", Addr: ln.Addr().String()}}
	samples := probeAll(targets, 5, time.Millisecond, time.Second, 2)
	if len(samples) != 5 {
		t.Fatalf("应当产生 5 条样本，实际 %d", len(samples))
	}
	for i, s := range samples {
		if s.Seq != i {
			t.Errorf("第 %d 条样本的序号是 %d", i, s.Seq)
		}
	}
}

func TestWriteJSONL(t *testing.T) {
	samples := []Sample{
		{Target: "a", Addr: "1.1.1.1:80", Seq: 0, UnixMs: 1, RTTus: 120, OK: true},
		{Target: "a", Addr: "1.1.1.1:80", Seq: 1, UnixMs: 2, RTTus: 0, OK: false, Err: "timeout"},
	}
	var buf bytes.Buffer
	if err := writeJSONL(&buf, samples); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("应当写 2 行，实际 %d", len(lines))
	}
	var back Sample
	if err := json.Unmarshal([]byte(lines[1]), &back); err != nil {
		t.Fatal(err)
	}
	if back.Err != "timeout" || back.OK {
		t.Errorf("失败样本回读不一致: %+v", back)
	}
}

func TestLoadTargetsFromFile(t *testing.T) {
	path := t.TempDir() + "/targets.json"
	content := `[{"name":"tor-a","addr":"10.0.0.1:22"}]`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	targets, cleanup, err := loadTargets(path)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if len(targets) != 1 || targets[0].Name != "tor-a" {
		t.Fatalf("目标表解析不对: %+v", targets)
	}
}
