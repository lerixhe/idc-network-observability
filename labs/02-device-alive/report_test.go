package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWriteOutputs(t *testing.T) {
	sc, m, _ := runScenario(t, ScenarioAll, 40, 60, nil)
	dir := t.TempDir()

	snap := m.Snapshot(sc.Name)
	eventsPath, snapPath, err := WriteOutputs(dir, snap, m.Events())
	if err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(eventsPath)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != len(m.Events()) {
		t.Fatalf("事件流 %d 行，期望 %d 行", len(lines), len(m.Events()))
	}
	var last Event
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &last); err != nil {
		t.Fatalf("事件流最后一行解析失败: %v", err)
	}
	if last.Seq != len(m.Events()) {
		t.Errorf("最后一条事件序号 %d，期望 %d", last.Seq, len(m.Events()))
	}

	snapRaw, err := os.ReadFile(snapPath)
	if err != nil {
		t.Fatal(err)
	}
	var back Snapshot
	if err := json.Unmarshal(snapRaw, &back); err != nil {
		t.Fatalf("快照解析失败: %v", err)
	}
	if len(back.Devices) != 40 {
		t.Errorf("快照设备数 %d，期望 40", len(back.Devices))
	}
	if back.Counters.Events != len(m.Events()) {
		t.Errorf("快照里的计数器 %d，期望 %d", back.Counters.Events, len(m.Events()))
	}
	if back.Scenario != ScenarioAll {
		t.Errorf("快照里的剧本名 %s", back.Scenario)
	}
	if back.Config.FailThreshold != m.Config().FailThreshold {
		t.Errorf("快照没带上判定参数: %+v", back.Config)
	}
}

func TestWriteOutputsCreatesDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "out")
	snap := Snapshot{Scenario: ScenarioAll, Devices: []DeviceState{}}
	eventsPath, snapPath, err := WriteOutputs(dir, snap, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{eventsPath, snapPath} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s 应当被创建: %v", p, err)
		}
	}
}

func TestPrintReport(t *testing.T) {
	sc, m, ev := runScenario(t, ScenarioAll, 40, 60, nil)
	var buf bytes.Buffer
	PrintReport(&buf, sc, m, ev, ReportOptions{
		Verbose:      true,
		ScenarioFile: "samples/scenarios.json",
		Elapsed:      12 * time.Millisecond,
	})
	out := buf.String()

	want := []string{
		"剧本：all",
		"去抖 N=3 次失败判失联",
		"注入的真实故障",
		"hard-down",
		"cpu-stall",
		"probe-outage",
		"事件流",
		"判定输出",
		"与真相对照",
		"真实失联窗口 4 个",
		"劣化误判",
		"快照说明不了的事",
		"samples/scenarios.json",
	}
	for _, s := range want {
		if !strings.Contains(out, s) {
			t.Errorf("报告缺少 %q\n---\n%s", s, out)
		}
	}
}

func TestPrintReportQuietHidesEvents(t *testing.T) {
	sc, m, ev := runScenario(t, ScenarioHardDown, 20, 60, nil)
	var buf bytes.Buffer
	PrintReport(&buf, sc, m, ev, ReportOptions{Verbose: false})
	if strings.Contains(buf.String(), "事件流（") {
		t.Error("-quiet 时不该打印事件明细")
	}
	if !strings.Contains(buf.String(), "判定输出") {
		t.Error("-quiet 时仍要打印统计")
	}
}

func TestPrintReportOnShortSimulation(t *testing.T) {
	// 仿真只有 5 轮（2.5 分钟），默认剧本的窗口会被夹到边界内，报告仍应能出来。
	cfg := baseConfig(5)
	sc, err := BuildScenario(ScenarioAll, ScenarioConfig{
		Devices:  40,
		Ticks:    5,
		Interval: cfg.Interval,
		Seed:     1,
		Plan:     DefaultPlan(),
	})
	if err != nil {
		t.Fatal(err)
	}
	m := NewMonitor(cfg, sc.Devices)
	m.Run(func(i, tick int) bool { return true })
	var buf bytes.Buffer
	PrintReport(&buf, sc, m, Evaluate(sc, m), ReportOptions{})
	if !strings.Contains(buf.String(), "剧本参数来源") {
		t.Error("报告应当标出参数来源")
	}
	for _, f := range sc.Faults {
		ticks := cfg.Ticks()
		if f.StartTick < 0 || f.EndTick > ticks || f.EndTick <= f.StartTick {
			t.Errorf("窗口没被夹进仿真范围: %+v", f)
		}
	}
}

func TestFormatEvidence(t *testing.T) {
	cases := []struct {
		name string
		ev   Evidence
		want []string
	}{
		{"下线", Evidence{ConsecutiveFails: 3, PeerTotal: 10, PeerFailed: 1, PeerFailRatio: 0.1}, []string{"fails=3", "group=1/10(10%)"}},
		{"恢复", Evidence{ConsecutiveOKs: 2, DowntimeMs: 90000, SuspectReboot: true}, []string{"oks=2", "downtime=1m30s", "suspect=reboot"}},
		{"探针故障", Evidence{PeerTotal: 100, PeerFailed: 100, PeerFailRatio: 1}, []string{"group=100/100(100%)"}},
		{"空证据", Evidence{}, nil},
	}
	for _, c := range cases {
		got := formatEvidence(c.ev)
		if len(c.want) == 0 {
			if got != "" {
				t.Errorf("%s：期望空字符串，实际 %q", c.name, got)
			}
			continue
		}
		for _, w := range c.want {
			if !strings.Contains(got, w) {
				t.Errorf("%s：%q 里缺少 %q", c.name, got, w)
			}
		}
	}
}
