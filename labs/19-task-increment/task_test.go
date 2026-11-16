package main

import "testing"

func mkPlan(version int, ids ...string) Plan {
	p := Plan{Version: version}
	for _, id := range ids {
		p.Tasks = append(p.Tasks, Task{TaskID: id, Target: "10.0.0.1", Period: 5})
	}
	return p
}

func TestDiffCountsAddedRemoved(t *testing.T) {
	from := mkPlan(1, "A", "B", "C")
	to := mkPlan(2, "A", "D", "E")
	d := Diff(from, to)
	if len(d.Added) != 2 || len(d.Removed) != 2 {
		t.Fatalf("期望新增 2 删除 2，得到 %+v", d)
	}
	if d.Added[0] != "D" || d.Added[1] != "E" {
		t.Fatalf("新增应排序，得到 %v", d.Added)
	}
}

func TestDiffByIDNotPosition(t *testing.T) {
	// 同样的集合、不同顺序：TaskID 相同 → 差量为空。
	from := mkPlan(1, "A", "B")
	to := mkPlan(2, "B", "A")
	d := Diff(from, to)
	if len(d.Added)+len(d.Removed) != 0 {
		t.Fatalf("顺序变化不应产生差量，得到 %+v", d)
	}
}

func TestDeltaMuchSmallerThanFull(t *testing.T) {
	from := mkPlan(1, "A", "B", "C", "D", "E")
	to := mkPlan(2, "A", "B", "C", "D", "E", "F")
	d := Diff(from, to)
	if d.Bytes() >= from.Bytes() {
		t.Fatalf("一增一删的差量应小于全量：delta=%d full=%d", d.Bytes(), from.Bytes())
	}
}

func TestChurnThreshold(t *testing.T) {
	from := mkPlan(1, "A", "B", "C", "D", "E")
	to := mkPlan(2, "F", "G", "H", "I", "J")
	d := Diff(from, to)
	if got := d.Churn(5); got < 0.99 {
		t.Fatalf("整体重排的变更比例应接近 100%%，得到 %.2f", got)
	}
}

func TestJudgeFourCells(t *testing.T) {
	cases := []struct {
		name        string
		p           ProbeState
		rackHealthy bool
		want        Verdict
	}{
		{"单点全挂", ProbeState{TotalTargets: 10, FailedTargets: 10, LastHeartbeatOK: false}, true, VerdictProbeFault},
		{"同机架全挂", ProbeState{TotalTargets: 10, FailedTargets: 10, LastHeartbeatOK: false}, false, VerdictEnvFault},
		{"活着但全挂", ProbeState{TotalTargets: 10, FailedTargets: 10, LastHeartbeatOK: true}, true, VerdictNetworkFault},
		{"部分失败", ProbeState{TotalTargets: 10, FailedTargets: 2}, true, VerdictTargetFault},
		{"健康", ProbeState{TotalTargets: 10, FailedTargets: 0}, true, VerdictHealthy},
	}
	for _, c := range cases {
		if got := Judge(c.p, c.rackHealthy); got != c.want {
			t.Errorf("%s：期望 %s，得到 %s", c.name, c.want, got)
		}
	}
}

func TestOnlyProbeFaultTriggersReplace(t *testing.T) {
	for _, v := range []Verdict{VerdictEnvFault, VerdictNetworkFault, VerdictTargetFault, VerdictHealthy} {
		if ShouldReplace(v) {
			t.Errorf("%s 不该换点", v)
		}
	}
	if !ShouldReplace(VerdictProbeFault) {
		t.Error("探针侧故障应换点")
	}
}

func TestReplacerStopsAfterMaxMoves(t *testing.T) {
	r := NewReplacer(3)
	moves := 0
	for i := 0; i < 10; i++ {
		d := r.Decide("T1", VerdictProbeFault)
		if d.Move {
			moves++
			continue
		}
		if !d.Stop {
			t.Fatalf("第 %d 轮既没换点也没停：%+v", i+1, d)
		}
		break
	}
	if moves != 3 {
		t.Fatalf("应换 3 次后停止，实际换 %d 次", moves)
	}
}

func TestReplacerNotTriggeredByOtherVerdicts(t *testing.T) {
	r := NewReplacer(1)
	if d := r.Decide("T2", VerdictNetworkFault); d.Move || d.Stop {
		t.Fatalf("网络侧故障不应触发换点，得到 %+v", d)
	}
}
