package main

import (
	"os"
	"testing"
	"time"
)

// testConfig 是测试用的基准参数，与 `make run` 的默认值一致。
func testConfig() Config {
	return Config{
		Probes:           12,
		Duration:         10 * time.Minute,
		Pull:             true,
		Push:             true,
		Poll:             5 * time.Second,
		Heartbeat:        30 * time.Second,
		PushDelay:        20 * time.Millisecond,
		ReconnectBackoff: 3 * time.Second,
		ReportInterval:   time.Minute,
		MaxReportDelay:   15 * time.Minute,
		FailModes:        append([]FailMode(nil), allFailModes...),
		FlapReplaceLimit: 2,
		StaleTTL:         2 * time.Minute,
		CtlRate:          500,
		Seed:             1,
	}
}

// loadSamples 读仓库里的样本，测的就是读者会拿到的那份数据。
func loadSamples(t *testing.T) ([]Probe, []string, []Task) {
	t.Helper()
	roster, sites, err := LoadRoster("samples/probes.json")
	if err != nil {
		t.Fatalf("读探针清单失败：%v", err)
	}
	tasks, err := LoadTasks("samples/tasks.json")
	if err != nil {
		t.Fatalf("读任务集失败：%v", err)
	}
	return roster, sites, tasks
}

func buildDefault(t *testing.T) (Config, *Scenario) {
	t.Helper()
	cfg := testConfig()
	roster, sites, tasks := loadSamples(t)
	sc, err := BuildScenario(cfg, roster, sites, tasks)
	if err != nil {
		t.Fatalf("构造场景失败：%v", err)
	}
	return cfg, sc
}

func TestConfigValidate(t *testing.T) {
	base := testConfig()
	if err := base.Validate(); err != nil {
		t.Fatalf("默认参数应当通过校验：%v", err)
	}
	cases := []struct {
		name string
		mod  func(*Config)
	}{
		{"探针数为零", func(c *Config) { c.Probes = 0 }},
		{"仿真太短", func(c *Config) { c.Duration = 30 * time.Second }},
		{"两种模式都没选", func(c *Config) { c.Pull, c.Push = false, false }},
		{"补报延迟为零", func(c *Config) { c.MaxReportDelay = 0 }},
		{"抑制阈值为零", func(c *Config) { c.FlapReplaceLimit = 0 }},
		{"控制面限流为零", func(c *Config) { c.CtlRate = 0 }},
	}
	for _, c := range cases {
		cfg := base
		c.mod(&cfg)
		if err := cfg.Validate(); err == nil {
			t.Errorf("%s：应当被校验挡住，实际通过", c.name)
		}
	}
}

func TestParseFailModes(t *testing.T) {
	if got, err := ParseFailModes("all"); err != nil || len(got) != 4 {
		t.Fatalf("all 应当给出四个模式，得到 %v / %v", got, err)
	}
	if got, err := ParseFailModes("none"); err != nil || len(got) != 0 {
		t.Fatalf("none 应当给空，得到 %v / %v", got, err)
	}
	got, err := ParseFailModes("flap,stale,flap")
	if err != nil {
		t.Fatalf("解析 flap,stale,flap 失败：%v", err)
	}
	if len(got) != 2 {
		t.Fatalf("重复的模式应当去重，得到 %v", got)
	}
	if _, err := ParseFailModes("bogus"); err == nil {
		t.Fatal("未知模式应当报错，不能静默忽略")
	}
}

func TestLoadSamplesShape(t *testing.T) {
	roster, sites, tasks := loadSamples(t)
	if len(roster) != 12 {
		t.Fatalf("样本应当有 12 台探针，实际 %d", len(roster))
	}
	if len(sites) != 3 {
		t.Fatalf("样本应当有 3 个机房，实际 %d", len(sites))
	}
	if len(tasks) != 14 {
		t.Fatalf("样本应当有 14 条任务，实际 %d", len(tasks))
	}
	byID := map[string]Probe{}
	for _, p := range roster {
		byID[p.ProbeID] = p
	}
	for _, task := range tasks {
		if _, ok := byID[task.ProbeID]; !ok {
			t.Fatalf("任务 %s 挂在未知探针 %s 上", task.TaskID, task.ProbeID)
		}
		if task.Revision != 1 {
			t.Fatalf("任务 %s 的初始 revision 应当是 1，实际 %d", task.TaskID, task.Revision)
		}
	}
	decomm := "probe-c-03"
	if byID[decomm].DecommissionedAt < 0 {
		t.Fatalf("%s 应当是退役探针，样本里的过期任务靠它注入", decomm)
	}
}

func TestLoadTasksRejectsDuplicateMetric(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/tasks.json"
	body := `{"tasks":[
		{"task_id":"t-1","metric_id":"if_util.a1.eth1-1","target":"a1/eth1","probe_id":"p-1"},
		{"task_id":"t-2","metric_id":"if_util.a1.eth1-1","target":"a1/eth2","probe_id":"p-1"}
	]}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("写临时样本失败：%v", err)
	}
	if _, err := LoadTasks(path); err == nil {
		t.Fatal("两条任务撞同一个 metric_id 应当报错：幂等键会碰撞，管道会压掉一条")
	}
}

func TestBuildScenarioDeterministic(t *testing.T) {
	cfg, sc1 := buildDefault(t)
	roster, sites, tasks := loadSamples(t)
	sc2, err := BuildScenario(cfg, roster, sites, tasks)
	if err != nil {
		t.Fatalf("第二次构造失败：%v", err)
	}
	if len(sc1.Changes) != len(sc2.Changes) {
		t.Fatalf("同种子应当给出相同数量的变更：%d vs %d", len(sc1.Changes), len(sc2.Changes))
	}
	for i := range sc1.Changes {
		a, b := sc1.Changes[i], sc2.Changes[i]
		if a.TaskID != b.TaskID || a.At != b.At || a.ToProbe != b.ToProbe || a.Revision != b.Revision {
			t.Fatalf("第 %d 条变更不一致：%+v vs %+v", i, a, b)
		}
		if i > 0 && sc1.Changes[i].At < sc1.Changes[i-1].At {
			t.Fatalf("变更应当按时间排序，第 %d 条 %s 早于上一条 %s", i, dur(a.At), dur(sc1.Changes[i-1].At))
		}
	}
}

func TestScenarioChangeKinds(t *testing.T) {
	_, sc := buildDefault(t)
	moved := map[string]int{}
	for _, ch := range sc.Changes {
		switch ch.Kind {
		case KindMove:
			if ch.FromProbe == ch.ToProbe {
				t.Fatalf("换点变更 %s 的起点与终点相同：%s", ch.TaskID, ch.ToProbe)
			}
			moved[ch.TaskID]++
		case KindRevise:
			if ch.FromProbe != ch.ToProbe {
				t.Fatalf("内容变更不该换点：%s", ch.TaskID)
			}
		default:
			t.Fatalf("未知变更类型 %q", ch.Kind)
		}
	}
	if len(moved) == 0 {
		t.Fatal("每 4 条任务一次换点，样本上应当有换点变更")
	}
	for task, n := range moved {
		if n > 1 {
			t.Fatalf("任务 %s 被换了 %d 次点，本实验只注入一次换点", task, n)
		}
	}
	if sc.Truth.Changes != sc.Truth.Revises+sc.Truth.Moves {
		t.Fatalf("变更总数 %d 应当等于内容变更 %d + 换点 %d",
			sc.Truth.Changes, sc.Truth.Revises, sc.Truth.Moves)
	}
	if len(sc.Truth.DuplicateTasks) != sc.Truth.DuplicateDispatches {
		t.Fatalf("注入 %d 次重复下发，任务名单却有 %d 个",
			sc.Truth.DuplicateDispatches, len(sc.Truth.DuplicateTasks))
	}
}

func TestExpandProbesRejectsTruncation(t *testing.T) {
	cfg := testConfig()
	cfg.Probes = 4
	roster, sites, tasks := loadSamples(t)
	if _, err := BuildScenario(cfg, roster, sites, tasks); err == nil {
		t.Fatal("探针数少于样本清单时应当报错：截断会丢掉样本里的离线与退役坑")
	}
}

func TestExpandProbesAddsSynthetic(t *testing.T) {
	cfg := testConfig()
	cfg.Probes = 200
	roster, sites, tasks := loadSamples(t)
	sc, err := BuildScenario(cfg, roster, sites, tasks)
	if err != nil {
		t.Fatalf("扩到 200 台失败：%v", err)
	}
	if len(sc.Probes) != 200 {
		t.Fatalf("应当有 200 台探针，实际 %d", len(sc.Probes))
	}
	for i, p := range sc.Probes {
		if p.ProbeID == "" || p.Capacity <= 0 {
			t.Fatalf("第 %d 台探针不完整：%+v", i, p)
		}
	}
	// 补出来的探针各配一条任务：十万探针那档要是只有 14 条任务，变更数就全是摆设。
	wantTasks := len(tasks) + (cfg.Probes - len(roster))
	if len(sc.Tasks) != wantTasks {
		t.Fatalf("补出来的探针应当各带一条任务：%d 条，实际 %d", wantTasks, len(sc.Tasks))
	}
	if len(sc.Changes) != wantTasks+(wantTasks+3)/4 {
		t.Fatalf("变更数应当是每任务一次内容变更 + 每 4 条一次换点：期望 %d，实际 %d",
			wantTasks+(wantTasks+3)/4, len(sc.Changes))
	}
}

func TestBuildScenarioRejectsUnknownProbe(t *testing.T) {
	cfg := testConfig()
	roster, sites := []Probe{oneProbe("p-1"), oneProbe("p-2")}, []string{"room-a"}
	cfg.Probes = 2
	tasks := []Task{{TaskID: "t-1", MetricID: "if_util", Target: "leaf/1", ProbeID: "p-9", Revision: 1}}
	if _, err := BuildScenario(cfg, roster, sites, tasks); err == nil {
		t.Fatal("任务挂在清单外的探针上应当报错")
	}
}
