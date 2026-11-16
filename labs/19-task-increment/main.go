package main

import (
	"fmt"
	"os"
)

// 场景一：一万台机器变化时，全量重推与差量下发的字节数对比。
func scenarioIncrement() {
	const (
		probes   = 100000
		tasksPer = 30 // 每探针 30 条任务
	)
	before := Plan{Version: 5821}
	for i := 0; i < tasksPer; i++ {
		before.Tasks = append(before.Tasks, Task{TaskID: fmt.Sprintf("L-%d:fw", i), Target: "10.0.0.1", Period: 5})
	}
	// 一次变更：1 条任务移机（删 1 加 1）
	after := Plan{Version: 5822, Tasks: before.Tasks[1:]}
	for i := 0; i < 1; i++ {
		after.Tasks = append(after.Tasks, Task{TaskID: fmt.Sprintf("L-%d:fw", 100+i), Target: "10.0.0.2", Period: 5})
	}

	delta := Diff(before, after)

	fullBytes := before.Bytes() * probes
	deltaBytes := delta.Bytes() * probes
	fmt.Println("增量下发：一次小变更")
	fmt.Printf("  任务集        %d 条/探针，%d 个探针\n", tasksPer, probes)
	fmt.Printf("  全量重推      %d 字节/探针 × %d = %.1f MB\n", before.Bytes(), probes, float64(fullBytes)/1e6)
	fmt.Printf("  差量下发      %d 字节/探针 × %d = %.1f MB（新增 %d、删除 %d）\n",
		delta.Bytes(), probes, float64(deltaBytes)/1e6, len(delta.Added), len(delta.Removed))
	fmt.Printf("  比值          %.1f×\n", float64(fullBytes)/float64(deltaBytes))
	fmt.Printf("  变更比例      %.0f%%（超过 10%% 就退回全量）\n", delta.Churn(tasksPer)*100)
	fmt.Println()

	// 大改场景：任务集整体重排
	bigAfter := Plan{Version: 5823}
	for i := 0; i < tasksPer; i++ {
		bigAfter.Tasks = append(bigAfter.Tasks, Task{TaskID: fmt.Sprintf("N-%d:fw", i), Target: "10.0.0.3", Period: 5})
	}
	bigDelta := Diff(after, bigAfter)
	fmt.Println("增量下发：一次大改（任务集整体重排）")
	fmt.Printf("  差量         %d 字节/探针，变更比例 %.0f%%\n", bigDelta.Bytes(), bigDelta.Churn(tasksPer)*100)
	if bigDelta.Churn(tasksPer) > 0.10 {
		fmt.Println("  结论         超过 10% 阈值，退回全量推送（差量包涨了 12 倍，编码耗时从 2 秒到 40 秒）")
	}
	fmt.Println()
}

// 场景二：坏点两问四格与震荡抑制。
func scenarioDetect() {
	fmt.Println("坏点判定：两问四格")
	cases := []struct {
		name        string
		p           ProbeState
		rackHealthy bool
	}{
		{"单探针全挂、同机架都正常", ProbeState{TotalTargets: 20, FailedTargets: 20, LastHeartbeatOK: false}, true},
		{"同机架多台同时全挂", ProbeState{TotalTargets: 20, FailedTargets: 20, LastHeartbeatOK: false}, false},
		{"探针活着但全挂（位置问题）", ProbeState{TotalTargets: 20, FailedTargets: 20, LastHeartbeatOK: true}, true},
		{"部分目标失败", ProbeState{TotalTargets: 20, FailedTargets: 3, LastHeartbeatOK: true}, true},
	}
	for _, c := range cases {
		v := Judge(c.p, c.rackHealthy)
		fmt.Printf("  %-26s → %-14s 换点=%v\n", c.name, v, ShouldReplace(v))
	}
	fmt.Println()

	// 震荡：同一个任务连续遇到坏点
	r := NewReplacer(3)
	fmt.Println("震荡抑制：同一个任务连续遇到坏点（上限 3 次）")
	moves, stoppedAt := 0, 0
	for i := 0; i < 6; i++ {
		d := r.Decide("L-1043:fw", VerdictProbeFault)
		if d.Move {
			moves++
			fmt.Printf("  第 %d 轮：换点（%s），断流 10 秒\n", i+1, d.Reason)
			continue
		}
		if d.Stop {
			stoppedAt = i + 1
			fmt.Printf("  第 %d 轮：停止（%s）\n", i+1, d.Reason)
			break
		}
	}
	fmt.Printf("  合计：自动换点 %d 次，第 %d 轮转人工；断流共 %d 秒\n", moves, stoppedAt, moves*10)
	fmt.Println("  对比：不抑制时 6 轮全部换点，断流 60 秒，且任务在 6 台机器间迁移")
	fmt.Println()
}

// 场景三：收敛时间的三段拆解。
func scenarioConverge() {
	fmt.Println("一分钟收敛的三段拆解")
	segs := []struct {
		name string
		sec  float64
	}{
		{"拉取周期", 5},
		{"两级缓存未命中回源", 3},
		{"差量包编码", 2},
	}
	total := 0.0
	for _, s := range segs {
		fmt.Printf("  %-20s %.0f 秒\n", s.name, s.sec)
		total += s.sec
	}
	fmt.Printf("  %-20s %.0f 秒（承诺 60 秒，余量给发版与重连）\n", "最坏路径", total)
	fmt.Println()
}

func main() {
	fmt.Println("== 19-task-increment ==")
	fmt.Println()
	scenarioIncrement()
	scenarioDetect()
	scenarioConverge()

	full := planHeaderBytes + 30*taskRecordBytes
	delta := planHeaderBytes + 2*deltaEntryBytes
	summary := fmt.Sprintf("{\n  \"full_bytes_per_probe\": %d,\n  \"delta_bytes_per_probe\": %d,\n  \"ratio\": %.1f,\n  \"max_moves\": 3,\n  \"converge_worst_sec\": 10\n}\n",
		full, delta, float64(full)/float64(delta))
	if err := os.MkdirAll("out", 0o755); err == nil {
		_ = os.WriteFile("out/summary.json", []byte(summary), 0o644)
	}
	fmt.Println("产物：out/summary.json")
}
