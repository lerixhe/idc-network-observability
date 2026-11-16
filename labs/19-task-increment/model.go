package main

import (
	"fmt"
	"sort"
)

// Task 是一条探测任务。TaskID 必须可推导（第 51 课那条坑），不能自增。
type Task struct {
	TaskID string
	Target string
	Period int
}

// Plan 是一个探针手上的任务集，带版本号。
type Plan struct {
	Version int
	Tasks   []Task
}

// Bytes 把任务集编码成字节数。真实系统是 protobuf，这里用一个固定模型：
// 每条任务 40 字节的定长记录，加上版本头 16 字节。
const (
	taskRecordBytes = 40
	planHeaderBytes = 16
)

func (p Plan) Bytes() int { return planHeaderBytes + len(p.Tasks)*taskRecordBytes }

// Delta 是差量包：新增与删除的 TaskID 列表，每条 20 字节。
type Delta struct {
	FromVersion int
	ToVersion   int
	Added       []string
	Removed     []string
}

const deltaEntryBytes = 20

func (d Delta) Bytes() int {
	return planHeaderBytes + (len(d.Added)+len(d.Removed))*deltaEntryBytes
}

// Diff 算两个版本之间的差量，按 TaskID 比较（不是按位置）。
func Diff(from, to Plan) Delta {
	fromSet := make(map[string]Task, len(from.Tasks))
	for _, t := range from.Tasks {
		fromSet[t.TaskID] = t
	}
	toSet := make(map[string]Task, len(to.Tasks))
	for _, t := range to.Tasks {
		toSet[t.TaskID] = t
	}
	d := Delta{FromVersion: from.Version, ToVersion: to.Version}
	for id := range toSet {
		if _, ok := fromSet[id]; !ok {
			d.Added = append(d.Added, id)
		}
	}
	for id := range fromSet {
		if _, ok := toSet[id]; !ok {
			d.Removed = append(d.Removed, id)
		}
	}
	sort.Strings(d.Added)
	sort.Strings(d.Removed)
	return d
}

// Churn 返回变更比例，用于「超过阈值退回全量」的判断。
func (d Delta) Churn(baseSize int) float64 {
	if baseSize == 0 {
		return 1
	}
	return float64(len(d.Added)+len(d.Removed)) / float64(baseSize)
}

// ProbeState 是探针在某一轮的探测结果。
type ProbeState struct {
	ProbeID string
	Rack    string
	// FailedTargets 是这一轮探测失败的目标数，TotalTargets 是总目标数。
	FailedTargets int
	TotalTargets  int
	// LastHeartbeatOK 表示探针自己还活着（心跳正常）。
	LastHeartbeatOK bool
}

// Verdict 是坏点判定的结论。
type Verdict string

const (
	VerdictHealthy      Verdict = "健康"
	VerdictProbeFault   Verdict = "探针侧故障"
	VerdictEnvFault     Verdict = "环境故障（同机架同时全失败）"
	VerdictNetworkFault Verdict = "网络侧故障（该机器位置有问题）"
	VerdictTargetFault  Verdict = "目标侧故障"
)

// Judge 实现第 51 课的两问四格：
//
//	问一：这个探针到其他目标也全失败吗？
//	问二：同机架其他探针正常吗？
//
// rackHealthy 是第二问的答案（同机架其他探针是否正常）。
func Judge(p ProbeState, rackHealthy bool) Verdict {
	if p.TotalTargets == 0 || p.FailedTargets == 0 {
		return VerdictHealthy
	}
	allFailed := p.FailedTargets == p.TotalTargets
	if !allFailed {
		return VerdictTargetFault
	}
	switch {
	case !rackHealthy:
		return VerdictEnvFault
	case !p.LastHeartbeatOK:
		return VerdictProbeFault
	default:
		return VerdictNetworkFault
	}
}

// ShouldReplace 只有一种判定允许换点。
func ShouldReplace(v Verdict) bool { return v == VerdictProbeFault }

// Replacer 维护每个任务的换点计数，实现「换三次就停」。
type Replacer struct {
	maxMoves int
	moves    map[string]int
	stopped  map[string]bool
}

func NewReplacer(maxMoves int) *Replacer {
	return &Replacer{maxMoves: maxMoves, moves: map[string]int{}, stopped: map[string]bool{}}
}

type MoveDecision struct {
	Move   bool
	Stop   bool
	Reason string
}

// Decide 决定是否把任务从坏点换走。连续换到上限后停止，转人工。
func (r *Replacer) Decide(taskID string, v Verdict) MoveDecision {
	if r.stopped[taskID] {
		return MoveDecision{Move: false, Stop: true, Reason: "已停止自动替换，等待人工"}
	}
	if !ShouldReplace(v) {
		return MoveDecision{Move: false, Reason: "判定不是探针侧故障"}
	}
	r.moves[taskID]++
	if r.moves[taskID] > r.maxMoves {
		r.stopped[taskID] = true
		return MoveDecision{Move: false, Stop: true, Reason: fmt.Sprintf("连续换点 %d 次到上限，转人工", r.moves[taskID]-1)}
	}
	return MoveDecision{Move: true, Reason: fmt.Sprintf("第 %d 次换点", r.moves[taskID])}
}
