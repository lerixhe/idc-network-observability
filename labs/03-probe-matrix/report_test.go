package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// runSamplePipeline 把样本跑成一份归因结果，报告类测试都用它当输入。
func runSamplePipeline(t *testing.T) (*Topology, *Matrix, []ProbeResult, Attribution, Truth) {
	t.Helper()
	tp, err := LoadTopology("samples/topology.json")
	if err != nil {
		t.Fatalf("加载拓扑失败：%v", err)
	}
	sc, err := LoadScenario("samples/incidents.json")
	if err != nil {
		t.Fatalf("加载剧本失败：%v", err)
	}
	m, err := GenerateMatrix(tp, sc.IntervalS)
	if err != nil {
		t.Fatalf("生成矩阵失败：%v", err)
	}
	results, truth, err := Simulate(tp, m, sc)
	if err != nil {
		t.Fatalf("仿真失败：%v", err)
	}
	a := Classify(m, indexResults(results), sc.WindowS/sc.IntervalS)
	return tp, m, results, a, truth
}

func TestPrintMatrixReport(t *testing.T) {
	tp, m, _, _, _ := runSamplePipeline(t)
	var buf bytes.Buffer
	printMatrixReport(&buf, tp, m)
	out := buf.String()

	for _, want := range []string{
		"36 个节点", "44 条链路",
		"节点对覆盖", "链路覆盖", "节点覆盖",
		"包速率", "带宽", "聚合存储", "逐包原始", "全 mesh 对照",
		"pps =",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("矩阵报告里没有 %q：\n%s", want, out)
		}
	}
	// 未覆盖链路为空时要明说，不能静默不打这一行。
	if !strings.Contains(out, "未覆盖链路  无") {
		t.Fatalf("全景覆盖时也该打一行「未覆盖链路  无」：\n%s", out)
	}
}

func TestPrintScoreTableGroups(t *testing.T) {
	_, _, _, a, _ := runSamplePipeline(t)
	var buf bytes.Buffer
	printScoreTable(&buf, a, 3)
	out := buf.String()

	// 四类结论都要有一节，否则值班人只看到「一堆红的」。
	for _, want := range []string{"[host_down]", "[probe_side]", "[target_side]", "[network_side]", "[正常]"} {
		if !strings.Contains(out, want) {
			t.Fatalf("表格里没有 %s 这一节：\n%s", want, out)
		}
	}
	if !strings.Contains(out, "整机不可达") || !strings.Contains(out, "作为目标被探时正常") {
		t.Fatalf("判据文案缺失：\n%s", out)
	}
	// -limit 要真的生效，不然 700 多行会把终端刷没。
	if strings.Count(out, "\n") > 120 {
		t.Fatalf("limit=3 时输出 %d 行，太长了", strings.Count(out, "\n"))
	}
}

func TestPrintAttributionAndReconcile(t *testing.T) {
	_, m, _, a, truth := runSamplePipeline(t)
	var buf bytes.Buffer
	printAttribution(&buf, a)
	printReconcile(&buf, Evaluate(m, a, truth))
	out := buf.String()

	for _, want := range []string{
		"归因合计", "网络侧异常", "探针侧异常", "对端侧异常", "主机不可达",
		"共同链路段", "网络健康分", "与真相对照", "命中 3", "误判 0",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("汇总里没有 %q：\n%s", want, out)
		}
	}
}

func TestWriteOutputsRoundTrip(t *testing.T) {
	_, m, results, a, _ := runSamplePipeline(t)
	dir := t.TempDir()

	matrixPath, err := writeMatrix(dir, m)
	if err != nil {
		t.Fatalf("写矩阵失败：%v", err)
	}
	raw, err := os.ReadFile(matrixPath)
	if err != nil {
		t.Fatalf("读回矩阵失败：%v", err)
	}
	var back Matrix
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("矩阵 JSON 解不开：%v", err)
	}
	if len(back.Pairs) != len(m.Pairs) {
		t.Fatalf("矩阵落盘后对数变了：%d vs %d", len(back.Pairs), len(m.Pairs))
	}
	if back.Cost.Pps != m.Cost.Pps {
		t.Fatalf("成本字段没落盘：%v vs %v", back.Cost.Pps, m.Cost.Pps)
	}

	resultsPath, err := writeResults(dir, results)
	if err != nil {
		t.Fatalf("写结果失败：%v", err)
	}
	// 自己写出去的东西必须能被自己的读取器读回来，否则「可回放」是句空话。
	loaded, err := LoadResults(resultsPath)
	if err != nil {
		t.Fatalf("读回探测结果失败：%v", err)
	}
	if len(loaded) != len(results) {
		t.Fatalf("结果落盘后条数变了：%d vs %d", len(loaded), len(results))
	}
	first := results[0]
	got, ok := loaded[pairKey(first.Src, first.Dst)]
	if !ok {
		t.Fatalf("读回后找不到 %s", pairKey(first.Src, first.Dst))
	}
	if got.Sent != first.Sent || got.Recv != first.Recv || got.P99us != first.P99us {
		t.Fatalf("读回的记录与写入的不一致：%+v vs %+v", got, first)
	}

	scoresPath, err := writeScores(dir, a.Scores)
	if err != nil {
		t.Fatalf("写评分失败：%v", err)
	}
	lines, err := os.ReadFile(scoresPath)
	if err != nil {
		t.Fatalf("读评分失败：%v", err)
	}
	got2 := strings.Count(strings.TrimRight(string(lines), "\n"), "\n") + 1
	if got2 != len(a.Scores) {
		t.Fatalf("评分 JSONL 有 %d 行，期望 %d 行", got2, len(a.Scores))
	}

	// 报告里要能看到落地文件，路径得能指到真实存在的位置。
	if _, err := os.Stat(filepath.Join(dir, "results.jsonl")); err != nil {
		t.Fatalf("results.jsonl 不在预期位置：%v", err)
	}
}

func TestLoadResultsRejectsDuplicates(t *testing.T) {
	body := `{"ts":"t0","src":"a","dst":"b","sent":60,"recv":60,"rtt_p50_us":100,"rtt_p99_us":120,"jitter_us":8,"baseline_rtt_us":100}
{"ts":"t0","src":"a","dst":"b","sent":60,"recv":59,"rtt_p50_us":100,"rtt_p99_us":130,"jitter_us":9,"baseline_rtt_us":100}
`
	path := writeTemp(t, "dup.jsonl", body)
	if _, err := LoadResults(path); err == nil {
		t.Fatal("同一个探测对出现两次必须报错")
	}
}
