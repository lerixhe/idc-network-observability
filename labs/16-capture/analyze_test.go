package main

import "testing"

// 正常链路：三端数字对齐，不该报任何陷阱。
func TestCleanLink(t *testing.T) {
	s := Sample{Case: "clean", WindowSeconds: 60, Flows: []Flow{{
		ID:     "clean",
		Sender: Sender{Port: "leaf01/eth1", TxPackets: 600000, TxBytes: 900000000},
		Path: []Hop{
			{Device: "spine01", InPackets: 600000, OutPackets: 600000, InBytes: 900000000, OutBytes: 900000000},
			{Device: "leaf07", InPackets: 600000, OutPackets: 600000, InBytes: 900000000, OutBytes: 900000000},
		},
		Receiver: Receiver{Port: "host-a/eth0", RxPackets: 600000, RxBytes: 900000000,
			Ethtool: Ethtool{RingRxEntries: 4096, TSO: true, GSO: true, GRO: true}},
	}}}

	r := Analyze(s)[0]
	if r.Verdict != SegNone {
		t.Fatalf("结论应为 %q，实际 %q（%s）", SegNone, r.Verdict, r.Summary)
	}
	if r.LostPackets != 0 || r.LostBytes != 0 {
		t.Fatalf("不该有丢包，实际包 %d 字节 %d", r.LostPackets, r.LostBytes)
	}
	if len(r.Traps) != 0 {
		t.Fatalf("不该命中陷阱，实际 %v", r.Traps)
	}
}

// 陷阱一：ring buffer 溢出，网卡计数干净但 ethtool 显示队列丢弃。
func TestTrapRingBuffer(t *testing.T) {
	s := Sample{Case: "ring", WindowSeconds: 60, Flows: []Flow{{
		ID:     "ring",
		Sender: Sender{Port: "leaf02/eth1", TxPackets: 480000, TxBytes: 720000000},
		Path: []Hop{
			{Device: "spine01", InPackets: 480000, OutPackets: 480000},
			{Device: "leaf07", InPackets: 480000, OutPackets: 480000},
		},
		Receiver: Receiver{Port: "host-b/eth0", RxPackets: 470000, RxBytes: 705000000,
			Ethtool: Ethtool{RingRxEntries: 512, RxQueueDropped: 10000}},
	}}}

	r := Analyze(s)[0]
	if !hasTrap(r.Traps, TrapRingBuffer) {
		t.Fatalf("应命中 %s，实际 %v", TrapRingBuffer, r.Traps)
	}
	if r.Verdict != SegHost {
		t.Fatalf("落点应为 %q，实际 %q", SegHost, r.Verdict)
	}
	if got := segValue(r.Segments, SegHost); got != 10000 {
		t.Fatalf("主机段丢包应为 10000，实际 %d", got)
	}
}

// 陷阱二：offload 假分段，包数差 33% 但字节几乎无差，不能判丢包。
func TestTrapOffloadSegment(t *testing.T) {
	s := Sample{Case: "offload", WindowSeconds: 60, Flows: []Flow{{
		ID:     "offload",
		Sender: Sender{Port: "leaf03/eth1", TxPackets: 200000, TxBytes: 300000000},
		Path: []Hop{
			{Device: "spine01", InPackets: 200000, OutPackets: 200000},
			{Device: "leaf07", InPackets: 200000, OutPackets: 200000},
		},
		Receiver: Receiver{Port: "host-c/eth0", RxPackets: 133000, RxBytes: 299400000,
			Ethtool: Ethtool{TSO: true, GSO: true, GRO: true, LRO: true}},
	}}}

	r := Analyze(s)[0]
	if !hasTrap(r.Traps, TrapOffload) {
		t.Fatalf("应命中 %s，实际 %v", TrapOffload, r.Traps)
	}
	if r.Verdict != SegNone {
		t.Fatalf("字节口径无丢，落点应为 %q，实际 %q（%s）", SegNone, r.Verdict, r.Summary)
	}
	if r.ByteLossPct > offloadDeltaPct {
		t.Fatalf("字节丢率应远小于 %.1f%%，实际 %.3f%%", offloadDeltaPct, r.ByteLossPct)
	}
}

// 陷阱三：ICMP 限速，探测端把没回来的 ICMP 当丢包。
func TestTrapICMPRateLimit(t *testing.T) {
	s := Sample{Case: "icmp", WindowSeconds: 60, Flows: []Flow{{
		ID:     "icmp",
		Sender: Sender{Port: "leaf04/eth1", TxPackets: 300000, TxBytes: 450000000},
		Path: []Hop{
			{Device: "spine01", InPackets: 300000, OutPackets: 300000},
			{Device: "leaf07", InPackets: 300000, OutPackets: 300000},
		},
		Receiver: Receiver{Port: "host-d/eth0", RxPackets: 300000, RxBytes: 450000000,
			ICMP: ICMP{Sent: 9000, RateLimited: 42}},
	}}}

	r := Analyze(s)[0]
	if !hasTrap(r.Traps, TrapICMP) {
		t.Fatalf("应命中 %s，实际 %v", TrapICMP, r.Traps)
	}
	if r.Verdict != SegNone {
		t.Fatalf("数据面无丢包，落点应为 %q，实际 %q", SegNone, r.Verdict)
	}
}

// 中间设备真丢，要落在 device 段并指出是哪一跳。
func TestDeviceLoss(t *testing.T) {
	s := Sample{Case: "device", WindowSeconds: 60, Flows: []Flow{{
		ID:     "device",
		Sender: Sender{Port: "leaf05/eth1", TxPackets: 900000, TxBytes: 1350000000},
		Path: []Hop{
			{Device: "spine01", InPackets: 900000, OutPackets: 900000},
			{Device: "leaf07", InPackets: 900000, OutPackets: 899200},
		},
		Receiver: Receiver{Port: "host-e/eth0", RxPackets: 899200, RxBytes: 1348800000,
			Ethtool: Ethtool{TSO: true, GSO: true, GRO: true}},
	}}}

	r := Analyze(s)[0]
	if r.Verdict != SegDevice {
		t.Fatalf("落点应为 %q，实际 %q", SegDevice, r.Verdict)
	}
	if len(r.HopDrops) != 2 || r.HopDrops[1].Device != "leaf07" || r.HopDrops[1].Lost != 800 {
		t.Fatalf("逐跳差额不对: %+v", r.HopDrops)
	}
}

// 样本文件本身也要能跑通，保证 samples/case.json 与代码同步。
func TestSampleFile(t *testing.T) {
	s, err := LoadSampleFile("samples/case.json")
	if err != nil {
		t.Fatalf("加载样本失败: %v", err)
	}
	results := Analyze(s)
	if len(results) != len(s.Flows) {
		t.Fatalf("结果数 %d 与 flow 数 %d 不一致", len(results), len(s.Flows))
	}
	codes := map[string]int{}
	for _, r := range results {
		for _, tr := range r.Traps {
			codes[tr.Code]++
		}
	}
	for _, want := range []string{TrapRingBuffer, TrapOffload, TrapICMP} {
		if codes[want] == 0 {
			t.Fatalf("样本应包含 %s 陷阱，实际 %v", want, codes)
		}
	}
}
