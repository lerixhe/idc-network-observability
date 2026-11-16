package main

import "testing"

func mkRun(hops ...Hop) *MTRRun {
	r := &MTRRun{Target: "target", Hops: hops}
	r.analyze()
	return r
}

// 样本里那条路径：第 2 跳 95% 但后面都正常，第 4 跳不回，最后两跳 38%。
func TestParseMTRSample(t *testing.T) {
	ds := loadSamples(t)
	if len(ds.MTR) != 1 {
		t.Fatalf("解出 %d 次 mtr，想要 1 次", len(ds.MTR))
	}
	run := ds.MTR[0]

	if len(run.Hops) != 7 {
		t.Fatalf("跳数 = %d，想要 7", len(run.Hops))
	}
	if run.Probe != "probe-b1f-02" || run.Target != "10.44.8.9" {
		t.Errorf("probe/target = %s/%s", run.Probe, run.Target)
	}

	byIndex := map[int]Hop{}
	for _, h := range run.Hops {
		byIndex[h.Index] = h
	}

	// 第 2 跳：中间跳丢包 95%，后面跳到终点都正常 → ICMP 限速。
	if h := byIndex[2]; h.Verdict != HopICMPLimit {
		t.Errorf("第 2 跳判定 = %s，想要 %s（丢包 %.1f%%）", h.Verdict, HopICMPLimit, h.LossPct)
	} else if h.Snt != 20 {
		t.Errorf("第 2 跳 Snt = %d，想要 20", h.Snt)
	}

	// 第 4 跳：??? → 不回探测包，不是丢包。
	if h := byIndex[4]; h.Verdict != HopSilent || h.Responded {
		t.Errorf("第 4 跳判定 = %s，responded = %v", h.Verdict, h.Responded)
	}

	// 第 6、7 跳：38% 一路丢到终点 → 真丢包。
	for _, idx := range []int{6, 7} {
		if h := byIndex[idx]; h.Verdict != HopRealLoss {
			t.Errorf("第 %d 跳判定 = %s，想要 %s", idx, h.Verdict, HopRealLoss)
		}
	}

	if run.Verdict != "target_loss" {
		t.Errorf("整条路径判定 = %s，想要 target_loss", run.Verdict)
	}
}

// 只中间跳丢包、终点干净：这是最典型的误判场景，必须判成 ICMP 限速。
func TestMidHopLossOnlyIsRateLimit(t *testing.T) {
	run := mkRun(
		Hop{Index: 1, Host: "10.0.0.1", Responded: true, LossPct: 0, Snt: 100},
		Hop{Index: 2, Host: "10.0.0.2", Responded: true, LossPct: 60, Snt: 100},
		Hop{Index: 3, Host: "10.0.0.3", Responded: true, LossPct: 0, Snt: 100},
	)
	if run.Verdict != "mid_hop_icmp_limit" {
		t.Fatalf("路径判定 = %s，想要 mid_hop_icmp_limit", run.Verdict)
	}
	if run.Hops[1].Verdict != HopICMPLimit {
		t.Errorf("中间跳判定 = %s", run.Hops[1].Verdict)
	}
}

// 一路干净：不该凭空造出嫌疑。
func TestAllCleanPath(t *testing.T) {
	run := mkRun(
		Hop{Index: 1, Host: "10.0.0.1", Responded: true, LossPct: 0, Snt: 100},
		Hop{Index: 2, Host: "10.0.0.2", Responded: true, LossPct: 0, Snt: 100},
		Hop{Index: 3, Host: "10.0.0.3", Responded: true, LossPct: 0, Snt: 100},
	)
	if run.Verdict != "path_ok" {
		t.Fatalf("路径判定 = %s，想要 path_ok", run.Verdict)
	}
	for _, h := range run.Hops {
		if h.Verdict != HopOK {
			t.Errorf("第 %d 跳判定 = %s", h.Index, h.Verdict)
		}
	}
}

// 最后一跳自己在丢、前一跳干净：也是真丢包，不能归到 ICMP 限速。
func TestTargetLossWithCleanPrevHop(t *testing.T) {
	run := mkRun(
		Hop{Index: 1, Host: "10.0.0.1", Responded: true, LossPct: 0, Snt: 100},
		Hop{Index: 2, Host: "10.0.0.2", Responded: true, LossPct: 0, Snt: 100},
		Hop{Index: 3, Host: "10.0.0.3", Responded: true, LossPct: 30, Snt: 100},
	)
	if run.Verdict != "target_loss" {
		t.Fatalf("路径判定 = %s，想要 target_loss", run.Verdict)
	}
	if run.Hops[2].Verdict != HopRealLoss {
		t.Errorf("终点跳判定 = %s", run.Hops[2].Verdict)
	}
}

// 门限以下的丢包按抖动处理，样本数不足要在 note 里说清。
func TestSmallLossIsJitter(t *testing.T) {
	run := mkRun(
		Hop{Index: 1, Host: "10.0.0.1", Responded: true, LossPct: 0, Snt: 100},
		Hop{Index: 2, Host: "10.0.0.2", Responded: true, LossPct: 2, Snt: 100},
	)
	if run.Hops[1].Verdict != HopOK {
		t.Errorf("2%% 丢包不该判成故障，实际 %s", run.Hops[1].Verdict)
	}
	if run.Verdict != "path_ok" {
		t.Errorf("路径判定 = %s", run.Verdict)
	}
}

// 一个跳都不回的时候，终点主机名取不到，也不能 panic。
func TestNoResponseRun(t *testing.T) {
	run := mkRun(
		Hop{Index: 1, Host: "???", Responded: false, LossPct: 100, Snt: 100},
		Hop{Index: 2, Host: "???", Responded: false, LossPct: 100, Snt: 100},
	)
	if run.Verdict != "path_ok" {
		t.Errorf("全是静默跳时判定 = %s", run.Verdict)
	}
	if run.terminalHost() != "" {
		t.Errorf("没有有回答的跳时应当返回空主机名，实际 %q", run.terminalHost())
	}
}
