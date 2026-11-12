package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// 端到端：整条流水线跑一遍，产物落全，守恒等式成立。
func TestRunWritesArtifactsAndConservesPoints(t *testing.T) {
	outDir := t.TempDir()
	var buf bytes.Buffer
	opt := options{
		samplesPath:    samplePath,
		outDir:         outDir,
		only:           "all",
		window:         time.Minute,
		watermark:      15 * time.Minute,
		maxBackfill:    15 * time.Minute,
		advance:        400 * 24 * time.Hour,
		pps:            12000,
		compress:       3,
		replicas:       3,
		writeAmp:       2,
		price:          800,
		detailInterval: 15 * time.Second,
		aggBytesFactor: 2,
		retainDetail:   30,
		retain1m:       365,
		retain5m:       1095,
	}
	if err := run(&buf, opt); err != nil {
		t.Fatalf("run 失败: %v\n输出:\n%s", err, buf.String())
	}

	for _, name := range []string{
		"rejected.jsonl", "dedup.jsonl", "late.jsonl", "aggregates.jsonl",
		"capacity.json", "summary.json",
	} {
		if _, err := os.Stat(filepath.Join(outDir, name)); err != nil {
			t.Fatalf("产物 %s 没落盘: %v", name, err)
		}
	}
	if entries, err := os.ReadDir(filepath.Join(outDir, "store", tier5m)); err != nil || len(entries) == 0 {
		t.Fatalf("5 分钟层的段没落盘: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(outDir, "summary.json"))
	if err != nil {
		t.Fatalf("读 summary.json: %v", err)
	}
	var sum summaryFile
	if err := json.Unmarshal(raw, &sum); err != nil {
		t.Fatalf("解析 summary.json: %v", err)
	}
	if sum.PointsKept+sum.PointsLate+sum.PointsDuplicated+sum.PointsRepeated+
		sum.PointsConflicted != sum.ParsedPoints-sum.RejectedPoints {
		t.Fatalf("summary 里的点数不守恒：%+v", sum)
	}
	if sum.PointsLate != 6 {
		t.Fatalf("15 分钟水位线下应只有那批迟到 20 分钟的被挡：%d", sum.PointsLate)
	}
	if sum.WindowSeconds != 60 || sum.WatermarkSeconds != 900 {
		t.Fatalf("窗口/水位线没写对：%v / %v", sum.WindowSeconds, sum.WatermarkSeconds)
	}

	out := buf.String()
	for _, want := range []string{
		"一、编码开销", "二、批次与幂等", "三、窗口与乱序", "四、存储", "五、容量估算",
		"点数守恒", "整包重放：聚合 digest 不变 ✓",
	} {
		if !bytes.Contains([]byte(out), []byte(want)) {
			t.Fatalf("输出里缺这一段：%q", want)
		}
	}
}

// 窄水位线跑同一个样本，丢的数据量必须真报出来。
func TestRunNarrowWatermarkReportsLoss(t *testing.T) {
	opt := options{
		samplesPath:    samplePath,
		only:           "window",
		window:         time.Minute,
		watermark:      time.Second,
		maxBackfill:    15 * time.Minute,
		pps:            12000,
		compress:       3,
		replicas:       3,
		writeAmp:       2,
		price:          800,
		detailInterval: 15 * time.Second,
		aggBytesFactor: 2,
		retainDetail:   30,
		retain1m:       365,
		retain5m:       1095,
	}
	var buf bytes.Buffer
	if err := run(&buf, opt); err != nil {
		t.Fatalf("run 失败: %v", err)
	}
	out := buf.String()
	if !bytes.Contains([]byte(out), []byte("窄了丢数据")) {
		t.Fatalf("没打出窄水位线的代价：\n%s", out)
	}
	if !bytes.Contains([]byte(out), []byte("✗")) {
		t.Fatalf("水位线窄于补报承诺时应标 ✗：\n%s", out)
	}
}

func TestRunRejectsBadOnlyFlag(t *testing.T) {
	opt := options{samplesPath: samplePath, only: "nonsense", window: time.Minute, maxBackfill: time.Minute}
	if err := run(&bytes.Buffer{}, opt); err == nil {
		t.Fatal("-only 收到非法值应报错")
	}
	opt = options{samplesPath: samplePath, only: "all", window: 1500 * time.Millisecond, maxBackfill: time.Minute}
	if err := run(&bytes.Buffer{}, opt); err == nil {
		t.Fatal("窗口不是整秒应报错")
	}
}

func TestParseCompare(t *testing.T) {
	widths, err := parseCompare("1m, 5m,1m,15m", time.Minute)
	if err != nil {
		t.Fatalf("解析: %v", err)
	}
	if len(widths) != 3 {
		t.Fatalf("重复的档位应去掉，实际 %d 档：%v", len(widths), widths)
	}
	if widths[0] != time.Minute || widths[2] != 15*time.Minute {
		t.Fatalf("应升序且去重：%v", widths)
	}
	if _, err := parseCompare("abc", time.Minute); err == nil {
		t.Fatal("非法时长应报错")
	}
}
