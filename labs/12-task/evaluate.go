package main

import (
	"fmt"
)

// Verdict 是判定结果与注入真值的对账。
//
// 这个实验和 02-device-alive 一样：模拟器注入真相，判定层只看信号，
// 最后两边逐条对。对不上的地方就是判定层的漏洞。
type Verdict struct {
	Changes    int
	Applied    int
	Undeployed int

	DupInjected int
	DupDetected int

	RepushEvents int

	FlapMovesOn  int
	FlapMovesOff int
	FlapFaults   int

	StaleInjected int
	StaleDetected int

	Batches BatchStats
}

// Reconcile 把注入真值与检出结果摆到一起。
func Reconcile(cfg Config, sc *Scenario, apps []Application, undeployed int, sig *Signals, bs BatchStats) Verdict {
	applied := 0
	for _, a := range apps {
		if a.Role == RoleAssign && !a.Dup {
			applied++
		}
	}
	v := Verdict{
		Changes:    len(sc.Changes),
		Applied:    applied,
		Undeployed: undeployed,
		Batches:    bs,
	}
	v.DupInjected = sc.Truth.DuplicateDispatches
	v.DupDetected = len(sig.Duplicates)
	if sig.Repush != nil {
		v.RepushEvents = len(sig.Repush.Events)
	}
	if sig.FlapOn != nil {
		v.FlapMovesOn = sig.FlapOn.Moves
		v.FlapFaults = sig.FlapOn.Faults
	}
	if sig.FlapOff != nil {
		v.FlapMovesOff = sig.FlapOff.Moves
	}
	if sig.Stale != nil {
		v.StaleInjected = sig.Stale.Off.StaleAssignments
		v.StaleDetected = len(sig.Stale.Events)
	}
	return v
}

// Line 是一条对账结论：ok 为真说明判定层没有漏。
type Line struct {
	What string
	Got  string
	Want string
	OK   bool
}

// Lines 生成对账的每一行，report 只负责排版。
func (v Verdict) Lines() []Line {
	got := fmt.Sprintf("%d/%d", v.Applied, v.Changes)
	want := "全部生效"
	if v.Undeployed > 0 {
		got = fmt.Sprintf("%d/%d（%d 条下发不到）", v.Applied, v.Changes, v.Undeployed)
		want = "生效 + 下发不到 = 变更总数"
	}
	out := []Line{{
		What: "变更下发到生效",
		Got:  got,
		Want: want,
		OK:   v.Applied+v.Undeployed == v.Changes,
	}}
	out = append(out, Line{
		What: "重复下发检出",
		Got:  fmt.Sprintf("%d", v.DupDetected),
		Want: fmt.Sprintf("注入 %d", v.DupInjected),
		OK:   v.DupDetected == v.DupInjected,
	})
	if v.RepushEvents > 0 || v.FlapMovesOff > 0 || v.StaleInjected > 0 {
		out = append(out, Line{
			What: "全量重推信号",
			Got:  fmt.Sprintf("%d 条", v.RepushEvents),
			Want: "1 条（放大倍数超 10）",
			OK:   v.RepushEvents <= 1,
		})
	}
	if v.FlapMovesOff > 0 {
		out = append(out, Line{
			What: "震荡被抑制",
			Got:  fmt.Sprintf("%d 次迁移", v.FlapMovesOn),
			Want: fmt.Sprintf("≤ 抑制阈值，无抑制时 %d 次", v.FlapMovesOff),
			OK:   v.FlapMovesOn <= v.FlapMovesOff,
		})
	}
	if v.StaleInjected > 0 {
		out = append(out, Line{
			What: "过期任务检出",
			Got:  fmt.Sprintf("%d", v.StaleDetected),
			Want: fmt.Sprintf("注入 %d", v.StaleInjected),
			OK:   v.StaleDetected == v.StaleInjected,
		})
	}
	if v.Batches.Produced > 0 {
		out = append(out, Line{
			What: "上报批次",
			Got:  fmt.Sprintf("生产 %d / 投递 %d / 丢弃 %d", v.Batches.Produced, v.Batches.Delivered, v.Batches.Dropped),
			Want: "生产 = 投递 + 丢弃",
			OK:   v.Batches.Produced == v.Batches.Delivered+v.Batches.Dropped,
		})
	}
	return out
}
