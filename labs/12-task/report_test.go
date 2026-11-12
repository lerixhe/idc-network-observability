package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

// TestEndToEnd 走一遍 main 的完整链路，顺带验证对账全绿。
func TestEndToEnd(t *testing.T) {
	cfg, sc := buildDefault(t)
	pull := SimulatePull(cfg, sc.Probes, sc.Changes)
	push := SimulatePush(cfg, sc.Probes, sc.Changes)
	sig := AnalyzeFailures(cfg, sc, pull.Apply)
	deliveries, bstats := MaterializeBatches(cfg, sc.Probes, pull.Apply)
	v := Reconcile(cfg, sc, pull.Apply, pull.Undeployed, sig, bstats)

	for _, l := range v.Lines() {
		if !l.OK {
			t.Errorf("对账对不上：%s（%s，应当 %s）", l.What, l.Got, l.Want)
		}
	}

	var buf bytes.Buffer
	PrintReport(&buf, cfg, sc, []DispatchResult{pull, push}, sig, v, ReportOptions{Verbose: true, Elapsed: 2 * time.Millisecond})
	report := buf.String()
	for _, want := range []string{
		"二、变更到生效", "拉取 5s", "长连接 20ms", "三、十万探针的账",
		"四、四个失败模式", "duplicate-dispatch", "task-fault", "stale-assignment",
		"五、与真相对账", "六、逐条变更", "仿真耗时",
	} {
		if !strings.Contains(report, want) {
			t.Errorf("报告里缺 %q", want)
		}
	}

	dir := t.TempDir()
	batchPath, eventPath, err := WriteOutputs(dir, deliveries, sig.Events)
	if err != nil {
		t.Fatalf("落产物失败：%v", err)
	}
	batches := readBatches(t, batchPath)
	if len(batches) != len(deliveries) {
		t.Fatalf("batches.jsonl 应当有 %d 行，实际 %d", len(deliveries), len(batches))
	}
	if events := readEvents(t, eventPath); len(events) != len(sig.Events) {
		t.Fatalf("events.jsonl 应当有 %d 行，实际 %d", len(sig.Events), len(events))
	}

	// 幂等键在同一批内必须唯一，否则管道一压就把有效点位压掉了。
	seen := map[string]bool{}
	for _, b := range batches {
		for _, p := range b.Points {
			k := b.IdempotencyKey(p)
			if seen[k] {
				t.Fatalf("幂等键重复：%s", k)
			}
			seen[k] = true
		}
	}
	if bstats.Produced != len(batches) {
		t.Fatalf("生产 %d 批，落盘 %d 行", bstats.Produced, len(batches))
	}
	if bstats.LateBatches == 0 {
		t.Fatal("样本里有离线探针，应当有迟到批次，13-pipeline 的乱序才有素材")
	}
	// 到达顺序里必须能看见「生产的更早、到的更晚」。
	outOfOrder := false
	for i := 1; i < len(batches); i++ {
		prev, cur := batches[i-1].ProducedOffset(), batches[i].ProducedOffset()
		if cur < prev {
			outOfOrder = true
			break
		}
	}
	if !outOfOrder {
		t.Fatal("批次应当出现实际到达顺序与生产顺序不一致的情况")
	}
}

func readBatches(t *testing.T, path string) []ReportBatch {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("打开 %s 失败：%v", path, err)
	}
	defer f.Close()
	var out []ReportBatch
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 1<<20)
	for sc.Scan() {
		line := sc.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var b ReportBatch
		if err := json.Unmarshal(line, &b); err != nil {
			t.Fatalf("第 %d 行不是合法批次：%v（%s）", len(out)+1, err, line)
		}
		out = append(out, b)
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("读 %s 失败：%v", path, err)
	}
	return out
}

func readEvents(t *testing.T, path string) []Event {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读 %s 失败：%v", path, err)
	}
	var out []Event
	for i, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if line == "" {
			continue
		}
		var e Event
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatalf("第 %d 行不是合法信号：%v（%s）", i+1, err, line)
		}
		out = append(out, e)
	}
	return out
}

func TestWriteOutputsEmptyDirSkips(t *testing.T) {
	bp, ep, err := WriteOutputs("", nil, nil)
	if err != nil {
		t.Fatalf("空目录应当直接跳过：%v", err)
	}
	if bp != "" || ep != "" {
		t.Fatalf("空目录不该返回路径，得到 %q / %q", bp, ep)
	}
}

func TestHistogramCoversAllSamples(t *testing.T) {
	in := []time.Duration{
		500 * time.Millisecond, 3 * time.Second, 8 * time.Second,
		30 * time.Second, 2 * time.Minute, 20 * time.Minute,
	}
	buckets := Histogram(in)
	total := 0
	for _, b := range buckets {
		total += b.Count
	}
	if total != len(in) {
		t.Fatalf("直方图应当覆盖全部 %d 个样本，实际 %d", len(in), total)
	}
	if buckets[0].Label != "<1s" || buckets[len(buckets)-1].Label != "≥5m" {
		t.Fatalf("分档首尾应当是 <1s / ≥5m，实际 %s / %s", buckets[0].Label, buckets[len(buckets)-1].Label)
	}
	if buckets[len(buckets)-1].Count != 1 {
		t.Fatalf("20 分钟的样本应当落在最后一档，实际 %d 个", buckets[len(buckets)-1].Count)
	}
}

func TestLatencyStatsPercentiles(t *testing.T) {
	in := []time.Duration{1 * time.Second, 2 * time.Second, 3 * time.Second, 4 * time.Second, 100 * time.Second}
	s := NewLatencyStats(in)
	if s.N != 5 || s.Min != time.Second || s.Max != 100*time.Second {
		t.Fatalf("基本统计不对：%+v", s)
	}
	if s.P50 != 3*time.Second {
		t.Fatalf("p50 应当是 3s，实际 %s", s.P50)
	}
	if s.P99 != 100*time.Second {
		t.Fatalf("p99 应当落在最大值上，实际 %s", s.P99)
	}
}
