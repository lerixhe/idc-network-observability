package main

import (
	"os"
	"path/filepath"
	"testing"
)

// goodHop 造一跳合法且有数据的元数据。时间戳从 1e12 起，只保证差值可用。
func goodHop(index int, device string, ingress, egress, depth int64) HopMeta {
	return HopMeta{
		Index: index, Device: device, Role: "spine",
		IngressPort: "Ethernet1/1", EgressPort: "Ethernet1/2",
		IntEnabled: true, ClockDomain: "ptp", LinkLengthM: 30,
		EgressBps:       100_000_000_000,
		IngressTsNs:     1_000_000_000_000 + ingress,
		EgressTsNs:      1_000_000_000_000 + egress,
		QueueDepthBytes: depth, QueueLimitBytes: 67_108_864,
	}
}

// blindHop 造一跳没有 INT 数据的元数据。
func blindHop(index int, device string) HopMeta {
	return HopMeta{
		Index: index, Device: device, Role: "border",
		IngressPort: "Ethernet1/1", EgressPort: "Ethernet1/2",
		IntEnabled: false, ClockDomain: "ntp", LinkLengthM: 6000,
		EgressBps: 100_000_000_000,
	}
}

func TestLoadTracesSamples(t *testing.T) {
	traces, err := loadTraces("samples/traces.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(traces) != 2 {
		t.Fatalf("样本应当是 2 条 trace，实际 %d", len(traces))
	}
	if err := validateAll(traces, defaultClockAccuracy); err != nil {
		t.Fatalf("样本本身不合法: %v", err)
	}
	full, partial := traces[0], traces[1]
	if full.ID != "t-full" || len(full.Hops) != 6 {
		t.Errorf("第一条应当是 t-full 的 6 跳，实际 %s / %d 跳", full.ID, len(full.Hops))
	}
	blind := 0
	for _, h := range partial.Hops {
		if !h.IntEnabled {
			blind++
		}
	}
	if blind != 2 {
		t.Errorf("t-partial 应当有 2 跳无数据，实际 %d", blind)
	}
	if full.Flow.Proto != "tcp" || full.Flow.Dst == "" {
		t.Errorf("flow 解析不对: %+v", full.Flow)
	}
}

func TestLoadTruthSamples(t *testing.T) {
	truth, err := loadTruth("samples/truth.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(truth) != 2 {
		t.Fatalf("真值应当是 2 条 trace，实际 %d", len(truth))
	}
	ft, ok := truth["t-full"]
	if !ok {
		t.Fatal("真值里缺 t-full")
	}
	var sum int64
	for _, h := range ft.Hops {
		if h.ResidentNs != h.QueueNs+h.ForwardNs {
			t.Errorf("hop %d 的真值不自洽：驻留 %d ≠ 排队 %d + 处理 %d",
				h.Index, h.ResidentNs, h.QueueNs, h.ForwardNs)
		}
		sum += h.ResidentNs + h.LinkPropNs
	}
	if sum != ft.E2ETrueNs {
		t.Errorf("真值端到端 %d ≠ 逐跳之和 %d", ft.E2ETrueNs, sum)
	}
}

func TestClockAccuracy(t *testing.T) {
	acc := defaultClockAccuracy
	if ns, err := acc.Accuracy("ptp"); err != nil || ns != 1_000 {
		t.Errorf("ptp 精度应当是 1000 ns，实际 %d, %v", ns, err)
	}
	if ns, err := acc.Accuracy("ntp"); err != nil || ns != 1_000_000 {
		t.Errorf("ntp 精度应当是 1 ms，实际 %d, %v", ns, err)
	}
	if _, err := acc.Accuracy("gps"); err == nil {
		t.Error("不认识的时钟域应当报错，不能静默当成 0")
	}
}

// TestValidateRejects 逐个打穿校验分支。每条对应一类真实的脏样本。
func TestValidateRejects(t *testing.T) {
	base := func() Trace {
		return Trace{ID: "t", Hops: []HopMeta{
			goodHop(1, "sw-a", 0, 3000, 1000),
			goodHop(2, "sw-b", 5000, 9000, 1000),
		}}
	}
	cases := []struct {
		name   string
		mutate func(*Trace)
	}{
		{"没有 id", func(tr *Trace) { tr.ID = "" }},
		{"只有一跳", func(tr *Trace) { tr.Hops = tr.Hops[:1] }},
		{"index 跳号", func(tr *Trace) { tr.Hops[1].Index = 3 }},
		{"没有设备名", func(tr *Trace) { tr.Hops[0].Device = "" }},
		{"出口速率为 0", func(tr *Trace) { tr.Hops[0].EgressBps = 0 }},
		{"未知时钟域", func(tr *Trace) { tr.Hops[0].ClockDomain = "beidou" }},
		{"标记有数据却没时间戳", func(tr *Trace) { tr.Hops[0].IngressTsNs = 0 }},
		{"出口不晚于入口", func(tr *Trace) { tr.Hops[1].EgressTsNs = tr.Hops[1].IngressTsNs }},
		{"队列深度为负", func(tr *Trace) { tr.Hops[0].QueueDepthBytes = -1 }},
		{"队列深度超容量", func(tr *Trace) { tr.Hops[0].QueueDepthBytes = tr.Hops[0].QueueLimitBytes + 1 }},
		{"未启用却夹带时间戳", func(tr *Trace) {
			tr.Hops[1].IntEnabled = false
		}},
		{"首跳没有数据", func(tr *Trace) {
			tr.Hops[0] = blindHop(1, "old-a")
			tr.Hops[1] = blindHop(2, "old-b")
			tr.Hops = append(tr.Hops, goodHop(3, "sw-c", 9000, 12000, 100))
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tr := base()
			c.mutate(&tr)
			if err := validateAll([]Trace{tr}, defaultClockAccuracy); err == nil {
				t.Error("应当报错，不能静默放过")
			}
		})
	}
}

func TestValidateRejectsDuplicateID(t *testing.T) {
	tr := Trace{ID: "dup", Hops: []HopMeta{
		goodHop(1, "sw-a", 0, 3000, 1000),
		goodHop(2, "sw-b", 5000, 9000, 1000),
	}}
	if err := validateAll([]Trace{tr, tr}, defaultClockAccuracy); err == nil {
		t.Error("重复的 trace id 应当报错")
	}
}

// TestValidateAcceptsBlindMiddleHops 中间跳没数据是合法的常态，不是错误。
func TestValidateAcceptsBlindMiddleHops(t *testing.T) {
	tr := Trace{ID: "t", Hops: []HopMeta{
		goodHop(1, "sw-a", 0, 3000, 1000),
		blindHop(2, "old-a"),
		goodHop(3, "sw-c", 9000, 12000, 1000),
	}}
	if err := validateAll([]Trace{tr}, defaultClockAccuracy); err != nil {
		t.Errorf("中间跳没有 INT 数据不该报错: %v", err)
	}
}

func TestLinksOf(t *testing.T) {
	tr := Trace{Hops: []HopMeta{
		goodHop(1, "sw-a", 0, 3000, 1000),
		blindHop(2, "old-a"),
	}}
	links := tr.linksOf()
	if len(links) != 2 || links[0] != 30 || links[1] != 6000 {
		t.Errorf("linksOf = %v, 期望 [30 6000]", links)
	}
}

func TestLoadTracesFromFileBadJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "traces.json")
	if err := os.WriteFile(path, []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadTraces(path); err == nil {
		t.Error("坏 JSON 应当报错")
	}
	if err := os.WriteFile(path, []byte(`{"traces":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadTraces(path); err == nil {
		t.Error("空 traces 应当报错")
	}
}
