package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ReportOptions 是报告里那几个不影响结论、只影响呈现的参数。
type ReportOptions struct {
	Verbose      bool          // 逐条打印事件明细
	ScenarioFile string        // 剧本参数来源，builtin 表示用内置默认值
	Elapsed      time.Duration // 仿真实际耗时，用于吞吐换算
}

// WriteOutputs 落两份产物：事件流与状态快照。
//
// 两份文件的用途完全不同：events.jsonl 是工单来源，snapshot.json 是看板来源。
// 每台设备一行事件，追加写一条就够，不需要 UPDATE 同一个 key。
func WriteOutputs(dir string, snap Snapshot, events []Event) (string, string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", "", err
	}
	eventPath := filepath.Join(dir, "events.jsonl")
	f, err := os.Create(eventPath)
	if err != nil {
		return "", "", err
	}
	defer f.Close()
	if err := writeEvents(f, events); err != nil {
		return "", "", err
	}

	snapPath := filepath.Join(dir, "snapshot.json")
	g, err := os.Create(snapPath)
	if err != nil {
		return "", "", err
	}
	defer g.Close()
	if err := writeSnapshot(g, snap); err != nil {
		return "", "", err
	}
	return eventPath, snapPath, nil
}

func writeEvents(w io.Writer, events []Event) error {
	bw := bufio.NewWriter(w)
	enc := json.NewEncoder(bw)
	for _, e := range events {
		if err := enc.Encode(e); err != nil {
			return err
		}
	}
	return bw.Flush()
}

func writeSnapshot(w io.Writer, snap Snapshot) error {
	bw := bufio.NewWriter(w)
	enc := json.NewEncoder(bw)
	enc.SetIndent("", "  ")
	if err := enc.Encode(snap); err != nil {
		return err
	}
	return bw.Flush()
}

// PrintReport 打印一份能直接贴进周报的报告。
func PrintReport(w io.Writer, sc *Scenario, m *Monitor, ev Eval, opts ReportOptions) {
	cfg := m.Config()
	ticks := cfg.Ticks()
	probes := int64(ticks) * int64(len(sc.Devices))
	snap := m.Snapshot(sc.Name)

	fmt.Fprintf(w, "剧本：%s —— %s\n", sc.Name, sc.Desc)
	fmt.Fprintf(w, "设备 %d 台 / %d 个机房；探测周期 %s，仿真 %s（%d 轮，%d 次探测）\n",
		len(sc.Devices), sc.Rooms, cfg.Interval, cfg.Duration, ticks, probes)
	fmt.Fprintf(w, "去抖 N=%d 次失败判失联，M=%d 次成功判恢复；同源组失败比例 ≥ %.0f%% 且规模 ≥ %d 台、连续 %d 轮判探测侧故障\n",
		cfg.FailThreshold, cfg.RecoverThreshold, cfg.CorrThreshold*100, cfg.CorrMinPeers, cfg.CorrTicks)
	fmt.Fprintf(w, "疑似重启阈值 %s（离线时长不超过它，恢复时标 suspect_reboot）\n", cfg.RebootSuspect)
	fmt.Fprintf(w, "剧本参数来源：%s，seed=%d\n", opts.ScenarioFile, cfg.Seed)
	if opts.Elapsed > 0 {
		fmt.Fprintf(w, "仿真耗时 %s（%.0f 万次探测/秒）\n",
			opts.Elapsed.Round(time.Millisecond), float64(probes)/opts.Elapsed.Seconds()/10000)
	}

	printInjected(w, sc, cfg)
	if opts.Verbose {
		printEvents(w, m.Events())
	}
	printCounters(w, snap)
	printTruth(w, ev, cfg)
	printSnapshotGap(w, snap)
}

// printInjected 列出注入的真实故障。这一段是模拟器视角，生产上不存在。
func printInjected(w io.Writer, sc *Scenario, cfg Config) {
	fmt.Fprintf(w, "\n注入的真实故障（判定层看不到这一段）\n")
	for _, kind := range []FaultKind{FaultHardDown, FaultReboot, FaultCPUStall, FaultProbeOutage} {
		items := make([]Fault, 0, 4)
		for _, f := range sc.Faults {
			if f.Kind == kind {
				items = append(items, f)
			}
		}
		if len(items) == 0 {
			continue
		}
		subjects := make([]string, 0, 3)
		for i, f := range items {
			if i == 3 {
				subjects = append(subjects, fmt.Sprintf("等 %d 个", len(items)))
				break
			}
			if f.Device != "" {
				subjects = append(subjects, f.Device)
			} else {
				subjects = append(subjects, f.Room)
			}
		}
		first := items[0]
		extra := ""
		if kind == FaultCPUStall {
			extra = fmt.Sprintf("，单次失败率 %.0f%%", first.LossRate*100)
		}
		fmt.Fprintf(w, "  %-12s %d 个窗口  %-28s %s – %s%s\n",
			kind, len(items), strings.Join(subjects, ","),
			fmtDur(cfg.AtMs(first.StartTick)), fmtDur(cfg.AtMs(first.EndTick)), extra)
	}
}

// printEvents 打印事件流明细。表头用 ASCII，避免中英混排把列挤歪。
func printEvents(w io.Writer, events []Event) {
	fmt.Fprintf(w, "\n事件流（%d 条）\n", len(events))
	fmt.Fprintf(w, "  %3s  %-9s %-17s %-19s %s\n", "seq", "at", "kind", "subject", "evidence")
	for _, e := range events {
		fmt.Fprintf(w, "  %3d  %-9s %-17s %-19s %s\n",
			e.Seq, fmtDur(e.AtMs), e.Kind, e.Subject, formatEvidence(e.Evidence))
	}
}

func formatEvidence(e Evidence) string {
	parts := make([]string, 0, 5)
	if e.ConsecutiveFails > 0 {
		parts = append(parts, fmt.Sprintf("fails=%d", e.ConsecutiveFails))
	}
	if e.ConsecutiveOKs > 0 {
		parts = append(parts, fmt.Sprintf("oks=%d", e.ConsecutiveOKs))
	}
	if e.PeerTotal > 0 {
		parts = append(parts, fmt.Sprintf("group=%d/%d(%.0f%%)", e.PeerFailed, e.PeerTotal, e.PeerFailRatio*100))
	}
	if e.DowntimeMs > 0 {
		parts = append(parts, "downtime="+fmtDur(e.DowntimeMs))
	}
	if e.SuspectReboot {
		parts = append(parts, "suspect=reboot")
	}
	return strings.Join(parts, " ")
}

func printCounters(w io.Writer, snap Snapshot) {
	c := snap.Counters
	fmt.Fprintf(w, "\n判定输出\n")
	fmt.Fprintf(w, "  事件总数 %d：device-down %d，device-up %d，probe-fault %d，probe-recovered %d\n",
		c.Events, c.DeviceDown, c.DeviceUp, c.ProbeFault, c.ProbeRecovered)
	fmt.Fprintf(w, "  最终快照 at %s：up %d，down %d，unknown %d\n",
		fmtDur(snap.AtMs), c.UpNow, c.DownNow, c.UnknownNow)
}

func printTruth(w io.Writer, ev Eval, cfg Config) {
	fmt.Fprintf(w, "\n与真相对照\n")
	fmt.Fprintf(w, "  真实失联窗口 %d 个，检出 %d 个，漏报 %d 个\n", ev.TrueWindows, ev.Detected, ev.Missed)
	if len(ev.DelaysMs) > 0 {
		fmt.Fprintf(w, "  发现延迟 p50 %s / p95 %s（理论最坏 = N × P = %d × %s = %s）\n",
			fmtDur(ev.DelayP(50)), fmtDur(ev.DelayP(95)),
			cfg.FailThreshold, cfg.Interval, cfg.Interval*time.Duration(cfg.FailThreshold))
	}
	fmt.Fprintf(w, "  误报设备 %d 台（没有真实故障却下线过）\n", ev.FalseDownDevices)
	if ev.DegradedTotal > 0 {
		fmt.Fprintf(w, "  劣化误判 %d / %d 轮次（%.1f%%）—— CPU 打满被当成失联的比例\n",
			ev.DegradedTicks, ev.DegradedTotal, 100*float64(ev.DegradedTicks)/float64(ev.DegradedTotal))
	}
	fmt.Fprintf(w, "  探测侧故障 %d 个窗口，识别 %d 个", ev.ProbeWindows, ev.ProbeReported)
	if len(ev.ProbeDetectMs) > 0 {
		fmt.Fprintf(w, "，识别延迟 p50 %s", fmtDur(ev.ProbeDetectP(50)))
	}
	fmt.Fprintf(w, "\n  作废观测 %d 轮次（探测侧故障窗口内，设备判定整体停摆）\n", ev.BlockedTicks)
}

// printSnapshotGap 说明为什么快照不能当工单来源。
func printSnapshotGap(w io.Writer, snap Snapshot) {
	if snap.Counters.FlappedDevices == 0 {
		fmt.Fprintf(w, "\n快照说明不了的事\n  本期没有设备在 final 快照前恢复过，两份输出恰好等价。\n")
		return
	}
	fmt.Fprintf(w, "\n快照说明不了的事\n  %d 台设备在快照里是 up，期间都下线过。终态和一直健康的设备长得一模一样，\n  工单只能从事件流开。\n",
		snap.Counters.FlappedDevices)
}
