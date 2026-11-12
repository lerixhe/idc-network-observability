package main

import (
	"strings"
	"testing"
	"time"
)

func TestDetectDuplicates(t *testing.T) {
	apps := []Application{
		{TaskID: "t-1", MetricID: "if_err_rate", Revision: 2, ProbeID: "p-b", Role: RoleAssign, AppliedAt: 2 * time.Minute},
		{TaskID: "t-1", MetricID: "if_err_rate", Revision: 2, ProbeID: "p-a", Role: RoleAssign, AppliedAt: time.Minute},
	}
	ev := DetectDuplicates(apps, 10*time.Minute, time.Minute)
	if len(ev) != 1 {
		t.Fatalf("两个探针接同一个 revision，应当报 1 条，实际 %d", len(ev))
	}
	e := ev[0]
	if e.Kind != "duplicate-dispatch" {
		t.Fatalf("信号类型应当是 duplicate-dispatch，实际 %s", e.Kind)
	}
	probes, ok := e.Evidence["probes"].([]string)
	if !ok || len(probes) != 2 || probes[0] != "p-a" || probes[1] != "p-b" {
		t.Fatalf("evidence.probes 应当是排序后的两台探针，实际 %v", e.Evidence["probes"])
	}
	// 重叠从较晚那次生效算到仿真结束：10m - 2m = 8m，8m / 1m = 8 个点位。
	if got := e.Evidence["overlap_s"].(float64); got != 480 {
		t.Fatalf("重叠时长应当是 480s，实际 %v", got)
	}
	if got := e.Evidence["duplicated_points"].(int); got != 8 {
		t.Fatalf("重复点位应当是 8 个，实际 %v", got)
	}
}

func TestDetectDuplicatesIgnoresSingleOwner(t *testing.T) {
	apps := []Application{
		{TaskID: "t-1", Revision: 2, ProbeID: "p-a", Role: RoleAssign, AppliedAt: time.Minute},
		{TaskID: "t-2", Revision: 5, ProbeID: "p-b", Role: RoleAssign, AppliedAt: 2 * time.Minute},
		// 收活记录不参与重复判定，同一个任务被撤销过不代表被重复下发。
		{TaskID: "t-1", Revision: 2, ProbeID: "p-b", Role: RoleRevoke, AppliedAt: 3 * time.Minute},
	}
	if ev := DetectDuplicates(apps, 10*time.Minute, time.Minute); len(ev) != 0 {
		t.Fatalf("应当没有重复下发，实际 %d 条：%v", len(ev), ev)
	}
}

func TestDuplicateInjectionIsDetectedExactly(t *testing.T) {
	cfg, sc := buildDefault(t)
	if sc.Truth.DuplicateDispatches == 0 {
		t.Fatal("样本里应当至少注入一次重复下发，否则这条测试是空的")
	}
	pull := SimulatePull(cfg, sc.Probes, sc.Changes)
	ev := DetectDuplicates(pull.Apply, cfg.Duration, cfg.ReportInterval)
	if len(ev) != sc.Truth.DuplicateDispatches {
		t.Fatalf("注入 %d 起，检出 %d 起：检测器漏了", sc.Truth.DuplicateDispatches, len(ev))
	}
	for _, e := range ev {
		if n := e.Evidence["probe_count"].(int); n < 2 {
			t.Fatalf("%s 只落在 %d 个探针上，不该算重复下发", e.Subject, n)
		}
	}
}

func TestDuplicateInjectionOffGivesNoSignal(t *testing.T) {
	cfg := testConfig()
	cfg.FailModes = []FailMode{FailFullRepush}
	roster, sites, tasks := loadSamples(t)
	sc, err := BuildScenario(cfg, roster, sites, tasks)
	if err != nil {
		t.Fatalf("构造场景失败：%v", err)
	}
	if sc.Truth.DuplicateDispatches != 0 {
		t.Fatalf("没开 duplicate 就不该注入，实际 %d", sc.Truth.DuplicateDispatches)
	}
	pull := SimulatePull(cfg, sc.Probes, sc.Changes)
	if ev := DetectDuplicates(pull.Apply, cfg.Duration, cfg.ReportInterval); len(ev) != 0 {
		t.Fatalf("不注入时不该报重复下发，实际 %d 条", len(ev))
	}
}

func TestFullRepushAmplification(t *testing.T) {
	cfg, sc := buildDefault(t)
	r := DetectFullRepush(cfg, len(sc.Tasks), sc.Changes)
	if len(r.Events) != 1 {
		t.Fatalf("应当报 1 条 full-repush，实际 %d 条", len(r.Events))
	}
	if r.Sample.Amplification < 10 {
		t.Fatalf("样本尺度的放大倍数应当超过 10，实际 %.2f", r.Sample.Amplification)
	}
	// 十万探针、一小时窗口、限流 500 条/秒：排空 100000/500 = 200s。
	if r.Scale.Drain != 200*time.Second {
		t.Fatalf("十万探针的排空时间应当是 200s，实际 %s", dur(r.Scale.Drain))
	}
	if r.Scale.PeakQPS != cfg.CtlRate {
		t.Fatalf("限流后的峰值应当是 %v，实际 %v", cfg.CtlRate, r.Scale.PeakQPS)
	}
	// 稳态 = 125000 条变更 / 3600 秒 = 34.72 QPS。
	if got := round2(r.Scale.SteadyQPS); got != 34.72 {
		t.Fatalf("十万探针的稳态 QPS 应当是 34.72，实际 %v", got)
	}
}

func TestFlapSuppressionStopsOscillation(t *testing.T) {
	cfg, sc := buildDefault(t)
	on := SimulateFlap(cfg, sc, true)
	off := SimulateFlap(cfg, sc, false)

	if len(sc.Truth.FlapTasks) == 0 {
		t.Fatal("样本里应当有坏点任务，否则这条测试是空的")
	}
	// 抑制开：换满阈值就不再换，每个坏点任务报一条 task-fault。
	if on.Moves > cfg.FlapReplaceLimit*len(sc.Truth.FlapTasks) {
		t.Fatalf("抑制开时迁移 %d 次，超过阈值 %d × 任务数", on.Moves, cfg.FlapReplaceLimit)
	}
	if on.Faults != len(sc.Truth.FlapTasks) {
		t.Fatalf("每个坏点任务应当报一条 task-fault，实际 %d 条", on.Faults)
	}
	faults := 0
	reassigns := 0
	for _, e := range on.Events {
		switch e.Kind {
		case "task-fault":
			faults++
			if e.Evidence["moves"].(int) != cfg.FlapReplaceLimit {
				t.Fatalf("报故障时的迁移次数应当是阈值 %d，实际 %v", cfg.FlapReplaceLimit, e.Evidence["moves"])
			}
		case "task-reassign":
			reassigns++
		}
	}
	if faults != len(sc.Truth.FlapTasks) {
		t.Fatalf("信号里 task-fault 应当 %d 条，实际 %d", len(sc.Truth.FlapTasks), faults)
	}
	if reassigns != on.Moves {
		t.Fatalf("每次迁移都该有一条 task-reassign：%d 次迁移 vs %d 条信号", on.Moves, reassigns)
	}

	// 抑制关：一直在换，数据丢得更多。
	if off.Moves <= cfg.FlapReplaceLimit {
		t.Fatalf("抑制关时应当继续震荡，实际只迁移 %d 次", off.Moves)
	}
	if off.Faults != 0 {
		t.Fatalf("抑制关时不该报故障，实际 %d 条", off.Faults)
	}
	if on.GapDuration >= off.GapDuration {
		t.Fatalf("抑制开应当少丢数据：开 %s vs 关 %s", dur(on.GapDuration), dur(off.GapDuration))
	}
}

func TestStaleReclaimVariants(t *testing.T) {
	cfg, sc := buildDefault(t)
	if len(sc.Truth.StaleTasks) == 0 {
		t.Fatal("样本里应当有退役探针名下的任务，否则这条测试是空的")
	}
	sr := SimulateStale(cfg, sc, false)
	if sr == nil {
		t.Fatal("样本里有退役探针，应当返回结果")
	}
	if len(sr.Tasks) != len(sc.Truth.StaleTasks) {
		t.Fatalf("退役探针名下应当有 %d 条任务，实际 %d", len(sc.Truth.StaleTasks), len(sr.Tasks))
	}
	// 不回收：任务一直挂着，缺口到仿真结束，没有恢复时刻。
	if sr.Off.StaleAssignments != len(sr.Tasks) {
		t.Fatalf("不回收时 %d 条任务都算陈旧关联，实际 %d", len(sr.Tasks), sr.Off.StaleAssignments)
	}
	if sr.Off.RecoveredAt >= 0 {
		t.Fatalf("不回收时不该有恢复时刻，实际 %s", dur(sr.Off.RecoveredAt))
	}
	// 回收：TTL 到点改派，缺口 = TTL + 改派延迟。
	if sr.On.StaleAssignments != 0 {
		t.Fatalf("回收之后不该再有陈旧关联，实际 %d", sr.On.StaleAssignments)
	}
	wantRecover := sr.DecommissionedAt + cfg.StaleTTL + cfg.Poll/2
	if sr.On.RecoveredAt != wantRecover {
		t.Fatalf("回收后应当在 %s 恢复出数，实际 %s", dur(wantRecover), dur(sr.On.RecoveredAt))
	}
	if sr.On.GapDuration >= sr.Off.GapDuration {
		t.Fatalf("回收应当缩短缺口：开 %s vs 关 %s", dur(sr.On.GapDuration), dur(sr.Off.GapDuration))
	}
	if len(sr.Events) != len(sr.Tasks) {
		t.Fatalf("每条陈旧关联都该有一条 stale-assignment，实际 %d 条", len(sr.Events))
	}
	for _, e := range sr.Events {
		if e.Kind != "stale-assignment" {
			t.Fatalf("信号类型应当是 stale-assignment，实际 %s", e.Kind)
		}
		if reclaimed, _ := e.Evidence["reclaimed"].(bool); reclaimed {
			t.Fatalf("%s：不回收的路径上不该声称已回收", e.Subject)
		}
	}
	// 走回收策略时不该产生陈旧关联信号。
	if resumed := SimulateStale(cfg, sc, true); len(resumed.Events) != 0 {
		t.Fatalf("回收策略下不该报 stale-assignment，实际 %d 条", len(resumed.Events))
	}
}

func TestAnalyzeFailuresEventOrder(t *testing.T) {
	cfg, sc := buildDefault(t)
	pull := SimulatePull(cfg, sc.Probes, sc.Changes)
	sig := AnalyzeFailures(cfg, sc, pull.Apply)
	if len(sig.Events) == 0 {
		t.Fatal("四个模式全开时应当有信号")
	}
	kinds := map[string]bool{}
	for i, e := range sig.Events {
		if e.Seq != i+1 {
			t.Fatalf("事件序号应当连续：第 %d 条的 seq 是 %d", i, e.Seq)
		}
		if i > 0 && e.AtMs < sig.Events[i-1].AtMs {
			t.Fatalf("事件应当按时间排序：第 %d 条 %dms 早于上一条 %dms", i, e.AtMs, sig.Events[i-1].AtMs)
		}
		kinds[e.Kind] = true
	}
	for _, want := range []string{"duplicate-dispatch", "full-repush", "task-reassign", "task-fault", "stale-assignment"} {
		if !kinds[want] {
			t.Fatalf("缺信号 %s，实际收到 %s", want, strings.Join(eventKinds(sig.Events), ","))
		}
	}
}

func eventKinds(events []Event) []string {
	out := make([]string, 0, len(events))
	for _, e := range events {
		out = append(out, e.Kind)
	}
	return out
}
