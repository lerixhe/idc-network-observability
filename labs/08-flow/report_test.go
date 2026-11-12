package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHumanBytes(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{
		{0, "0 B"},
		{512, "512 B"},
		{1514, "1.5 KB"},
		{238_000_000, "238.0 MB"},
		{1_500_000_000, "1.50 GB"},
	}
	for _, c := range cases {
		if got := humanBytes(c.in); got != c.want {
			t.Errorf("humanBytes(%d) = %q, 期望 %q", c.in, got, c.want)
		}
	}
}

func TestHumanBps(t *testing.T) {
	if got := humanBps(16.7 * 1000 * 8); got != "133.6 kbps" {
		t.Errorf("humanBps = %q", got)
	}
	if got := humanBps(166.7 * 1000 * 8); got != "1.3 Mbps" {
		t.Errorf("humanBps = %q", got)
	}
}

func TestPrintSamplingReportsDistortion(t *testing.T) {
	sim := sampleSimulation(t)
	var buf bytes.Buffer
	for _, r := range sim.Rates {
		printSampling(&buf, r)
	}
	out := buf.String()
	for _, want := range []string{"采样率 1:100", "最小可观测", "可见流", "大象估算误差"} {
		if !strings.Contains(out, want) {
			t.Errorf("输出里缺 %q", want)
		}
	}
	if !strings.Contains(out, "TopN 失真") {
		t.Errorf("1:10000 那一段应当报出 TopN 失真，实际输出：\n%s", out)
	}
}

func TestPrintSegmentsShowsUntrusted(t *testing.T) {
	exports := []NetFlowExport{
		exportOf(t, "2026-05-12T10:00:00Z", "exp-a", 1,
			tmplSet(300, fiveFields...),
			dataSet(300, []any{"10.0.0.1", "10.0.0.2", "tcp", 100, 1})),
		exportOf(t, "2026-05-12T11:00:00Z", "exp-a", 2,
			dataSet(300, []any{"10.0.0.1", "10.0.0.2", "tcp", 200, 2})),
		exportOf(t, "2026-05-12T11:01:00Z", "exp-a", 3,
			dataSet(400, []any{"10.0.0.1", "10.0.0.2", "tcp", 300, 3})),
	}
	res := DecodeExports(exports, 30*time.Minute)
	var buf bytes.Buffer
	printSegments(&buf, res, 30*time.Minute)
	out := buf.String()
	if strings.Count(out, "不可信") != 2 {
		t.Errorf("应当有 2 段不可信：\n%s", out)
	}
	if !strings.Contains(out, "模板过期") || !strings.Contains(out, "模板缺失") {
		t.Errorf("不可信原因要写清楚：\n%s", out)
	}
	if !strings.Contains(out, "30 分钟") {
		t.Errorf("输出要自报模板寿命口径：\n%s", out)
	}
}

func TestWriteTopNAndSampling(t *testing.T) {
	outDir := t.TempDir()
	stat := SourceStat{
		Name: "netflow", Label: "流记录聚合值", Coverage: 1,
		Bytes: 100, Packets: 2, Units: 1,
		Entries: []TopEntry{{Key: makeKey("10.0.0.1", "10.0.0.2", "tcp", 1, 2), Bytes: 100, Packets: 2, Samples: 1, Share: 1}},
	}
	path, err := writeTopN(outDir, []SourceStat{stat})
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc topnDoc
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Sources) != 1 || doc.Sources[0].Entries[0].Key.DstIP != "10.0.0.2" {
		t.Fatalf("回读不一致: %+v", doc)
	}
	if !strings.Contains(doc.Note, "不可相加") {
		t.Errorf("产物里必须写明两个源不可相加，实际 %q", doc.Note)
	}

	sim := sampleSimulation(t)
	samplingPath, err := writeSampling(outDir, sim)
	if err != nil {
		t.Fatal(err)
	}
	var back Simulation
	raw, err := os.ReadFile(samplingPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if len(back.Rates) != 3 || back.Rates[2].N != 10000 {
		t.Fatalf("采样产物回读不一致: %+v", back.Rates)
	}
}

// 整条链路跑一遍：样本文件 → 解析 → 段账 → TopN → 采样模拟 → 产物。
func TestRunEndToEnd(t *testing.T) {
	outDir := t.TempDir()
	var buf bytes.Buffer
	err := run(
		filepath.Join("samples", "sflow.jsonl"),
		filepath.Join("samples", "netflow.jsonl"),
		filepath.Join("samples", "flows-truth.json"),
		5, 30*time.Minute, "100,10000", 0, 42, "all", true, outDir, &buf,
	)
	if err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{
		"=== 一、解析录好的流样本",
		"采样率分布：0×1，1000×17",
		"模板 300 上次刷新在",
		"模板 400 没收到过",
		"=== 二、采样误差",
		"最小可观测流速率",
		"两个源直接相加",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("输出里缺 %q", want)
		}
	}
	for _, name := range []string{"topn.json", "sampling.json"} {
		if _, err := os.Stat(filepath.Join(outDir, name)); err != nil {
			t.Errorf("产物 %s 没落盘: %v", name, err)
		}
	}
}

func TestRunRejectsBadMode(t *testing.T) {
	var buf bytes.Buffer
	err := run("samples/sflow.jsonl", "samples/netflow.jsonl", "samples/flows-truth.json",
		5, 30*time.Minute, "100", 0, 42, "both", false, "", &buf)
	if err == nil || !strings.Contains(err.Error(), "mode") {
		t.Fatalf("非法 mode 应当报错，实际 %v", err)
	}
}
