package main

import (
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func buildTestScenario(t *testing.T, name string, devices, ticks int) *Scenario {
	t.Helper()
	sc, err := BuildScenario(name, ScenarioConfig{
		Devices:  devices,
		Ticks:    ticks,
		Interval: 30 * time.Second,
		Seed:     1,
		Plan:     DefaultPlan(),
	})
	if err != nil {
		t.Fatalf("装配剧本 %s 失败: %v", name, err)
	}
	return sc
}

func TestBuildDevicesSpreadByRoom(t *testing.T) {
	devices := buildDevices(8, 4)
	if len(devices) != 8 {
		t.Fatalf("设备数 %d，期望 8", len(devices))
	}
	if devices[0].ID != "leaf-0001" || devices[7].ID != "leaf-0008" {
		t.Errorf("设备命名不对: %s / %s", devices[0].ID, devices[7].ID)
	}
	// 4 个机房各 2 台；探针与机房一一配对。
	perRoom := map[string]int{}
	for _, d := range devices {
		perRoom[d.Room]++
		if d.Probe != "probe-"+d.Room[len(d.Room)-1:] {
			t.Errorf("%s 的探针 %s 与机房 %s 不配对", d.ID, d.Probe, d.Room)
		}
	}
	if len(perRoom) != 4 {
		t.Fatalf("机房数 %d，期望 4", len(perRoom))
	}
	for room, n := range perRoom {
		if n != 2 {
			t.Errorf("%s 有 %d 台设备，期望 2", room, n)
		}
	}
}

func TestBuildScenarioDeterministic(t *testing.T) {
	a := buildTestScenario(t, ScenarioAll, 40, 60)
	b := buildTestScenario(t, ScenarioAll, 40, 60)
	if !reflect.DeepEqual(a.Faults, b.Faults) {
		t.Fatalf("同一个 seed 两次装配结果不一致:\n%+v\n%+v", a.Faults, b.Faults)
	}
}

func TestEachScenarioInjectsItsOwnFaults(t *testing.T) {
	cases := []struct {
		name string
		kind FaultKind
	}{
		{ScenarioHardDown, FaultHardDown},
		{ScenarioReboot, FaultReboot},
		{ScenarioCPUStall, FaultCPUStall},
		{ScenarioProbeOutage, FaultProbeOutage},
	}
	for _, c := range cases {
		sc := buildTestScenario(t, c.name, 40, 60)
		n := 0
		for _, f := range sc.Faults {
			if f.Kind == c.kind {
				n++
			}
		}
		if n == 0 {
			t.Errorf("剧本 %s 没有注入 %s 故障", c.name, c.kind)
		}
		for _, f := range sc.Faults {
			if f.Kind != c.kind {
				t.Errorf("剧本 %s 混入了 %s 故障", c.name, f.Kind)
			}
		}
	}
}

func TestAllScenarioTargetsAreDisjoint(t *testing.T) {
	sc := buildTestScenario(t, ScenarioAll, 40, 60)
	seen := map[string]FaultKind{}
	for _, f := range sc.Faults {
		if f.Device == "" {
			continue
		}
		if prev, ok := seen[f.Device]; ok {
			t.Errorf("%s 同时被 %s 和 %s 命中，时间线无法解释", f.Device, prev, f.Kind)
		}
		seen[f.Device] = f.Kind
	}
	// 探测侧故障所在的机房不能被其他剧本取样。
	for _, f := range sc.Faults {
		if f.Kind == FaultProbeOutage {
			continue
		}
		for _, d := range sc.Devices {
			if d.ID != f.Device {
				continue
			}
			for _, o := range sc.ProbeOutageWindows() {
				if d.Room == o.Room {
					t.Errorf("%s 在探测侧故障机房 %s 内，不该被选作设备级故障目标", d.ID, d.Room)
				}
			}
		}
	}
}

func TestScenarioWindowsAreClamped(t *testing.T) {
	// 仿真只有 10 轮（5 分钟），默认的 15 分钟硬挂窗口必须被夹进范围。
	sc := buildTestScenario(t, ScenarioHardDown, 40, 10)
	for _, f := range sc.Faults {
		if f.StartTick < 0 || f.EndTick > 10 || f.EndTick <= f.StartTick {
			t.Errorf("窗口没被夹住: %+v", f)
		}
	}
}

func TestProbeRespondsToEachFaultKind(t *testing.T) {
	cfg := ScenarioConfig{Devices: 8, Ticks: 60, Interval: 30 * time.Second, Seed: 1, Plan: DefaultPlan()}
	sc, err := BuildScenario(ScenarioHardDown, cfg)
	if err != nil {
		t.Fatal(err)
	}
	rng := rand.New(rand.NewSource(1))

	target := sc.Faults[0]
	idx := -1
	for i, d := range sc.Devices {
		if d.ID == target.Device {
			idx = i
		}
	}
	if idx < 0 {
		t.Fatal("找不到被注入故障的设备")
	}
	if sc.Probe(idx, target.StartTick, rng) {
		t.Error("故障窗口内第 1 轮探测应当失败")
	}
	if !sc.Probe(idx, target.EndTick, rng) {
		t.Error("故障窗口外探测应当成功")
	}
	if got := sc.Truth(idx, target.StartTick); got != TruthUnreachable {
		t.Errorf("窗口内真相 %s，期望 unreachable", got)
	}
	if got := sc.Truth(idx, 0); got != TruthReachable {
		t.Errorf("窗口外真相 %s，期望 reachable", got)
	}
}

func TestTruthProbeOutageWinsOverDeviceFault(t *testing.T) {
	// 人为构造重叠：探针故障与设备硬挂压在同一轮。
	sc := &Scenario{
		Devices: []Device{{ID: "leaf-0001", Room: "room-a", Probe: "probe-a"}},
		Faults: []Fault{
			{Kind: FaultHardDown, Device: "leaf-0001", StartTick: 0, EndTick: 10},
			{Kind: FaultProbeOutage, Room: "room-a", StartTick: 0, EndTick: 5},
		},
	}
	sc.compile()
	if got := sc.Truth(0, 1); got != TruthProbeBlocked {
		t.Errorf("重叠窗口内真相 %s，期望 probe-blocked（探针自己瞎了，设备死活未知）", got)
	}
	if got := sc.Truth(0, 7); got != TruthUnreachable {
		t.Errorf("探针恢复后真相 %s，期望 unreachable", got)
	}
}

func TestTruthDegradedForCPUStall(t *testing.T) {
	sc := &Scenario{
		Devices: []Device{{ID: "leaf-0001", Room: "room-a", Probe: "probe-a"}},
		Faults: []Fault{
			{Kind: FaultCPUStall, Device: "leaf-0001", StartTick: 2, EndTick: 8, LossRate: 0.6},
		},
	}
	sc.compile()
	if got := sc.Truth(0, 5); got != TruthDegraded {
		t.Errorf("CPU 打满期间真相 %s，期望 degraded", got)
	}
	if got := sc.Truth(0, 1); got != TruthReachable {
		t.Errorf("窗口外真相 %s，期望 reachable", got)
	}
}

func TestFaultMatch(t *testing.T) {
	d := Device{ID: "leaf-0007", Room: "room-b", Probe: "probe-b"}
	cases := []struct {
		name  string
		fault Fault
		want  bool
	}{
		{"宽度不限", Fault{}, true},
		{"探针命中", Fault{Probe: "probe-b"}, true},
		{"探针不命中", Fault{Probe: "probe-a"}, false},
		{"机房命中", Fault{Room: "room-b"}, true},
		{"机房不命中", Fault{Room: "room-a"}, false},
		{"设备命中", Fault{Device: "leaf-0007"}, true},
		{"设备不命中", Fault{Device: "leaf-0008"}, false},
		{"条件同时满足", Fault{Room: "room-b", Probe: "probe-b", Device: "leaf-0007"}, true},
		{"一个条件不满足", Fault{Room: "room-b", Probe: "probe-a"}, false},
	}
	for _, c := range cases {
		if got := c.fault.Match(d); got != c.want {
			t.Errorf("%s：Match = %v，期望 %v", c.name, got, c.want)
		}
	}
}

func TestLoadScenarioPlanDefaults(t *testing.T) {
	plan, source, err := LoadScenarioPlan("")
	if err != nil {
		t.Fatal(err)
	}
	if source != "builtin" {
		t.Errorf("来源 %s，期望 builtin", source)
	}
	if plan.Rooms != DefaultPlan().Rooms {
		t.Errorf("默认机房数 %d", plan.Rooms)
	}

	// 文件不存在也走默认值，不让缺文件挡住实验。
	plan, source, err = LoadScenarioPlan(filepath.Join(t.TempDir(), "nope.json"))
	if err != nil {
		t.Fatal(err)
	}
	if source != "builtin" || plan.Rooms != DefaultPlan().Rooms {
		t.Errorf("缺文件时应当回落默认值，实际 %s %+v", source, plan)
	}
}

func TestLoadScenarioPlanFromFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "plan.json")
	content := `{
	  "rooms": 3,
	  "hard_down": {"count": 1, "start_at": 0.25, "duration_s": 600},
	  "cpu_stall": {"count": 2, "start_at": 0.4, "duration_s": 300, "loss_rate": 0.35}
	}`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	plan, source, err := LoadScenarioPlan(path)
	if err != nil {
		t.Fatal(err)
	}
	if source != path {
		t.Errorf("来源 %s，期望 %s", source, path)
	}
	if plan.Rooms != 3 {
		t.Errorf("机房数 %d，期望 3", plan.Rooms)
	}
	if plan.HardDown.Count != 1 || plan.HardDown.DurationSeconds != 600 {
		t.Errorf("硬挂参数没读对: %+v", plan.HardDown)
	}
	if plan.CPUStall.LossRate != 0.35 {
		t.Errorf("失败率 %v，期望 0.35", plan.CPUStall.LossRate)
	}
	// 文件里没写的字段回落默认值，半份 JSON 也能跑。
	if plan.Reboot.DurationSeconds != DefaultPlan().Reboot.DurationSeconds {
		t.Errorf("缺失的重启参数没有回落默认值: %+v", plan.Reboot)
	}
}

func TestLoadScenarioPlanBadJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadScenarioPlan(path); err == nil {
		t.Error("坏 JSON 应当报错")
	}
}

func TestBuildScenarioRejectsUnknownName(t *testing.T) {
	_, err := BuildScenario("no-such-scenario", ScenarioConfig{
		Devices: 4, Ticks: 10, Interval: 30 * time.Second, Plan: DefaultPlan(),
	})
	if err == nil {
		t.Fatal("未知剧本应当报错")
	}
}

func TestBuildScenarioRejectsTooManyRooms(t *testing.T) {
	plan := DefaultPlan()
	plan.Rooms = 30
	_, err := BuildScenario(ScenarioAll, ScenarioConfig{
		Devices: 400, Ticks: 10, Interval: 30 * time.Second, Plan: plan,
	})
	if err == nil {
		t.Fatal("机房数超出命名范围应当报错")
	}
}
