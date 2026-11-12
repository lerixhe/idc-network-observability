package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/rand"
	"os"
	"sort"
	"time"
)

// FaultKind 是注入的「真实情况」。模拟器知道它，判定层不知道——
// 判定层只看得到探测通不通，真相由 evaluate.go 事后对账。
type FaultKind string

const (
	FaultHardDown    FaultKind = "hard-down"    // 设备硬挂，一直不回来
	FaultReboot      FaultKind = "reboot"       // 设备重启，3–5 分钟后恢复
	FaultCPUStall    FaultKind = "cpu-stall"    // 管理面 CPU 打满，间歇丢包
	FaultProbeOutage FaultKind = "probe-outage" // 探测侧网络故障，同机房全丢
)

// Truth 是一台设备在某一轮的真相。
type Truth int

const (
	TruthReachable    Truth = iota // 真的通
	TruthUnreachable               // 设备管理面真的不可达
	TruthDegraded                  // 设备活着，但可达性劣化（CPU/限速/中间链路丢包）
	TruthProbeBlocked              // 探测侧故障，这一轮对设备一无所知
)

func (t Truth) String() string {
	switch t {
	case TruthReachable:
		return "reachable"
	case TruthUnreachable:
		return "unreachable"
	case TruthDegraded:
		return "degraded"
	case TruthProbeBlocked:
		return "probe-blocked"
	}
	return "?"
}

// Fault 是一段注入的故障窗口。作用范围从宽到窄依次是 Probe / Room / Device，
// 空字段表示不限，三个条件同时满足才命中设备。
type Fault struct {
	Kind      FaultKind `json:"kind"`
	Probe     string    `json:"probe,omitempty"`
	Room      string    `json:"room,omitempty"`
	Device    string    `json:"device,omitempty"`
	StartTick int       `json:"start_tick"`
	EndTick   int       `json:"end_tick"`
	LossRate  float64   `json:"loss_rate,omitempty"`
	Note      string    `json:"note,omitempty"`
}

// Match 判断这段故障是否命中某台设备。
func (f Fault) Match(d Device) bool {
	if f.Probe != "" && f.Probe != d.Probe {
		return false
	}
	if f.Room != "" && f.Room != d.Room {
		return false
	}
	if f.Device != "" && f.Device != d.ID {
		return false
	}
	return true
}

// compiledFault 是展开到具体设备身上的故障窗口，运行期只查区间。
type compiledFault struct {
	kind  FaultKind
	start int
	end   int
	loss  float64
}

func (c compiledFault) contains(tick int) bool {
	return tick >= c.start && tick < c.end
}

// Scenario 是「剧本」：一批设备 + 一条真实故障时间线。
type Scenario struct {
	Name      string
	Desc      string
	Devices   []Device
	Faults    []Fault
	Rooms     int
	perDevice [][]compiledFault
	used      map[string]bool
}

// 剧本名。四类故障各一个，外加一个全开的 all。
const (
	ScenarioAll         = "all"
	ScenarioHardDown    = "hard-down"
	ScenarioReboot      = "reboot"
	ScenarioCPUStall    = "cpu-stall"
	ScenarioProbeOutage = "probe-outage"
)

// ScenarioNames 是 -scenario 的合法取值，顺序即报告里的列举顺序。
func ScenarioNames() []string {
	return []string{ScenarioAll, ScenarioHardDown, ScenarioReboot, ScenarioCPUStall, ScenarioProbeOutage}
}

// BuildScenario 按剧本名装配设备表与故障时间线。
//
// 同一份 plan + seed 必定产出同一个剧本——事件数才对得上，跨机器也能复核。
func BuildScenario(name string, cfg ScenarioConfig) (*Scenario, error) {
	if cfg.Devices < 1 {
		return nil, errors.New("设备数必须大于 0")
	}
	if cfg.Ticks < 1 {
		return nil, errors.New("仿真至少要推进一轮探测")
	}
	if !validScenarioName(name) {
		return nil, fmt.Errorf("未知剧本 %q，可选 %v", name, ScenarioNames())
	}

	plan := cfg.Plan.normalized()
	if plan.Rooms > 26 {
		return nil, fmt.Errorf("机房数 %d 超出本实验的命名范围（最多 26 个）", plan.Rooms)
	}
	if plan.Rooms > cfg.Devices {
		return nil, fmt.Errorf("机房数 %d 大于设备数 %d，会有空机房", plan.Rooms, cfg.Devices)
	}
	sc := &Scenario{
		Name:    name,
		Desc:    scenarioDesc(name),
		Devices: buildDevices(cfg.Devices, plan.Rooms),
		Rooms:   plan.Rooms,
		used:    make(map[string]bool, cfg.Devices),
	}
	rngFor := func(tag int64) *rand.Rand {
		return rand.New(rand.NewSource(cfg.Seed + tag*1000))
	}

	// 探测侧故障最先定，它占用的机房要排除在其他剧本的取样范围之外，
	// 两个故障叠在一起，报告里就分不清是相关性判对了还是歪打正着。
	outageRoom := ""
	if name == ScenarioAll || name == ScenarioProbeOutage {
		outageRoom = sc.addProbeOutage(rngFor(4), plan.ProbeOutage, cfg)
	}
	if name == ScenarioAll || name == ScenarioHardDown {
		sc.addHardDown(rngFor(1), plan.HardDown, cfg, outageRoom)
	}
	if name == ScenarioAll || name == ScenarioReboot {
		sc.addReboot(rngFor(2), plan.Reboot, cfg, outageRoom)
	}
	if name == ScenarioAll || name == ScenarioCPUStall {
		sc.addCPUStall(rngFor(3), plan.CPUStall, cfg, outageRoom)
	}
	sc.compile()
	return sc, nil
}

func validScenarioName(name string) bool {
	for _, n := range ScenarioNames() {
		if n == name {
			return true
		}
	}
	return false
}

func scenarioDesc(name string) string {
	switch name {
	case ScenarioHardDown:
		return "设备硬挂：管理面彻底不可达，15 分钟内不恢复"
	case ScenarioReboot:
		return "设备重启：失联 3–5 分钟后自行恢复"
	case ScenarioCPUStall:
		return "管理面 CPU 打满：设备活着，探测间歇丢包"
	case ScenarioProbeOutage:
		return "探测侧网络故障：同机房设备全丢，设备本身没问题"
	case ScenarioAll:
		return "四类故障同时注入，检验判定层能否分得清"
	}
	return ""
}

// buildDevices 按机房均分设备。房间号取模分散——真实机柜里不会按前缀排布。
func buildDevices(n, rooms int) []Device {
	devices := make([]Device, n)
	for i := 0; i < n; i++ {
		room := fmt.Sprintf("room-%c", 'a'+rune(i%rooms))
		devices[i] = Device{
			ID:    fmt.Sprintf("leaf-%04d", i+1),
			Room:  room,
			Probe: "probe-" + room[len(room)-1:],
		}
	}
	return devices
}

// sample 从设备池里随机取 count 台。池子不够就全取，不留空位。
// 已被别的剧本占用的设备不再参与——一台设备同时硬挂和重启，时间线没法解释。
func (s *Scenario) sample(rng *rand.Rand, count int, excludeRoom string) []int {
	idx := make([]int, 0, len(s.Devices))
	for i, d := range s.Devices {
		if excludeRoom != "" && d.Room == excludeRoom {
			continue
		}
		if s.used[d.ID] {
			continue
		}
		idx = append(idx, i)
	}
	if len(idx) == 0 || count <= 0 {
		return nil
	}
	rng.Shuffle(len(idx), func(i, j int) { idx[i], idx[j] = idx[j], idx[i] })
	if count > len(idx) {
		count = len(idx)
	}
	picked := idx[:count]
	sort.Ints(picked)
	for _, i := range picked {
		s.used[s.Devices[i].ID] = true
	}
	return picked
}

func (s *Scenario) addProbeOutage(rng *rand.Rand, plan ProbeOutagePlan, cfg ScenarioConfig) string {
	rooms := s.Rooms
	if plan.Room < 0 || plan.Room >= rooms {
		plan.Room = rooms - 1
	}
	room := fmt.Sprintf("room-%c", 'a'+rune(plan.Room))
	start, end, ok := s.window(cfg, plan.StartAt, plan.DurationSeconds)
	if !ok {
		return ""
	}
	s.Faults = append(s.Faults, Fault{
		Kind:      FaultProbeOutage,
		Room:      room,
		StartTick: start,
		EndTick:   end,
		Note:      fmt.Sprintf("%s 的探针整段失联，%s 全机房 %d 台设备同时不可达", room, room, s.roomSize(room)),
	})
	return room
}

func (s *Scenario) addHardDown(rng *rand.Rand, plan FaultPlan, cfg ScenarioConfig, excludeRoom string) {
	start, end, ok := s.window(cfg, plan.StartAt, plan.DurationSeconds)
	if !ok {
		return
	}
	for _, i := range s.sample(rng, plan.Count, excludeRoom) {
		s.Faults = append(s.Faults, Fault{
			Kind:      FaultHardDown,
			Device:    s.Devices[i].ID,
			StartTick: start,
			EndTick:   end,
			Note:      "设备彻底断电或主板故障，整个窗口内不回来",
		})
	}
}

func (s *Scenario) addReboot(rng *rand.Rand, plan FaultPlan, cfg ScenarioConfig, excludeRoom string) {
	start, _, ok := s.window(cfg, plan.StartAt, plan.DurationSeconds)
	if !ok {
		return
	}
	for _, i := range s.sample(rng, plan.Count, excludeRoom) {
		// 每台设备的重启时长在 plan.Duration ± jitter 之间浮动，
		// 判定层不能只按「固定 3 分钟」打表，否则换个厂商就全错。
		dur := plan.DurationSeconds
		if plan.JitterSeconds > 0 {
			dur += (rng.Float64()*2 - 1) * plan.JitterSeconds
		}
		if dur < 1 {
			dur = 1
		}
		end := start + s.secondsToTicks(dur, cfg.Interval)
		end = s.clampTick(end, cfg.Ticks)
		if end <= start {
			continue
		}
		s.Faults = append(s.Faults, Fault{
			Kind:      FaultReboot,
			Device:    s.Devices[i].ID,
			StartTick: start,
			EndTick:   end,
			Note:      fmt.Sprintf("设备重启，失联 %.0f 秒后恢复", dur),
		})
	}
}

func (s *Scenario) addCPUStall(rng *rand.Rand, plan CPUPlan, cfg ScenarioConfig, excludeRoom string) {
	start, end, ok := s.window(cfg, plan.StartAt, plan.DurationSeconds)
	if !ok {
		return
	}
	loss := plan.LossRate
	if loss <= 0 || loss > 1 {
		loss = 0.6
	}
	for _, i := range s.sample(rng, plan.Count, excludeRoom) {
		s.Faults = append(s.Faults, Fault{
			Kind:      FaultCPUStall,
			Device:    s.Devices[i].ID,
			StartTick: start,
			EndTick:   end,
			LossRate:  loss,
			Note:      fmt.Sprintf("管理面 CPU 打满，单次探测失败率 %.0f%%，设备本身没挂", loss*100),
		})
	}
}

// window 把「占总时长的比例 + 绝对秒数」换算成 tick 区间，并夹到仿真范围里。
func (s *Scenario) window(cfg ScenarioConfig, startAt float64, durationSeconds float64) (int, int, bool) {
	if startAt < 0 || startAt >= 1 {
		startAt = 0
	}
	start := int(math.Round(startAt * float64(cfg.Ticks)))
	start = s.clampTick(start, cfg.Ticks)
	end := start + s.secondsToTicks(durationSeconds, cfg.Interval)
	end = s.clampTick(end, cfg.Ticks)
	if end <= start {
		return 0, 0, false
	}
	return start, end, true
}

func (s *Scenario) secondsToTicks(seconds float64, interval time.Duration) int {
	if interval <= 0 {
		return 0
	}
	t := int(math.Round(seconds / interval.Seconds()))
	if t < 1 {
		t = 1
	}
	return t
}

func (s *Scenario) clampTick(tick, ticks int) int {
	if tick < 0 {
		return 0
	}
	if tick > ticks {
		return ticks
	}
	return tick
}

func (s *Scenario) roomSize(room string) int {
	n := 0
	for _, d := range s.Devices {
		if d.Room == room {
			n++
		}
	}
	return n
}

// compile 把 Fault 展开到每台设备，运行期不做字符串匹配。
func (s *Scenario) compile() {
	s.perDevice = make([][]compiledFault, len(s.Devices))
	for _, f := range s.Faults {
		c := compiledFault{kind: f.Kind, start: f.StartTick, end: f.EndTick, loss: f.LossRate}
		for i, d := range s.Devices {
			if f.Match(d) {
				s.perDevice[i] = append(s.perDevice[i], c)
			}
		}
	}
}

// Probe 在第 tick 轮探一次第 i 台设备，只返回通不通。
//
// 判定层只能拿到这个布尔值——真相、原因、谁的责任，全在这里被抹掉。
// 这个信息损失正是后面所有误判的源头。
func (s *Scenario) Probe(i, tick int, rng *rand.Rand) bool {
	faults := s.perDevice[i]
	for _, f := range faults {
		if !f.contains(tick) {
			continue
		}
		if f.kind == FaultProbeOutage || f.kind == FaultHardDown || f.kind == FaultReboot {
			return false
		}
	}
	for _, f := range faults {
		if f.kind != FaultCPUStall || !f.contains(tick) {
			continue
		}
		if rng.Float64() < f.loss {
			return false
		}
	}
	return true
}

// Truth 返回第 i 台设备在第 tick 轮的真实情况。
//
// 探测侧故障压过一切：那一轮探针自己瞎了，设备死活未知，评测必须跳过。
func (s *Scenario) Truth(i, tick int) Truth {
	faults := s.perDevice[i]
	for _, f := range faults {
		if f.kind == FaultProbeOutage && f.contains(tick) {
			return TruthProbeBlocked
		}
	}
	degraded := false
	for _, f := range faults {
		if !f.contains(tick) {
			continue
		}
		switch f.kind {
		case FaultHardDown, FaultReboot:
			return TruthUnreachable
		case FaultCPUStall:
			degraded = true
		}
	}
	if degraded {
		return TruthDegraded
	}
	return TruthReachable
}

// UnreachableWindows 返回第 i 台设备的真实失联窗口。评测拿它算检出与延迟。
func (s *Scenario) UnreachableWindows(i int) []compiledFault {
	var out []compiledFault
	for _, f := range s.perDevice[i] {
		if f.kind == FaultHardDown || f.kind == FaultReboot {
			out = append(out, f)
		}
	}
	sort.Slice(out, func(a, b int) bool { return out[a].start < out[b].start })
	return out
}

// DegradedWindows 返回第 i 台设备的劣化窗口（设备活着，丢包）。
func (s *Scenario) DegradedWindows(i int) []compiledFault {
	var out []compiledFault
	for _, f := range s.perDevice[i] {
		if f.kind == FaultCPUStall {
			out = append(out, f)
		}
	}
	sort.Slice(out, func(a, b int) bool { return out[a].start < out[b].start })
	return out
}

// ProbeOutageWindows 返回探测侧故障窗口，用于对账「是否识别出探针故障」。
func (s *Scenario) ProbeOutageWindows() []Fault {
	var out []Fault
	for _, f := range s.Faults {
		if f.Kind == FaultProbeOutage {
			out = append(out, f)
		}
	}
	return out
}

// ---------- scenarios.json ----------

// ScenarioPlan 是剧本的时间参数。放在 samples/scenarios.json 里，
// 换规模、换时长都不用改代码。
type ScenarioPlan struct {
	Rooms       int             `json:"rooms"`
	HardDown    FaultPlan       `json:"hard_down"`
	Reboot      FaultPlan       `json:"reboot"`
	CPUStall    CPUPlan         `json:"cpu_stall"`
	ProbeOutage ProbeOutagePlan `json:"probe_outage"`
}

// FaultPlan 是设备级故障的时间。StartAt 是占总时长的比例，DurationSeconds 是绝对秒数。
type FaultPlan struct {
	Count           int     `json:"count"`
	StartAt         float64 `json:"start_at"`
	DurationSeconds float64 `json:"duration_s"`
	JitterSeconds   float64 `json:"jitter_s,omitempty"`
}

// CPUPlan 多一个失败概率。
type CPUPlan struct {
	FaultPlan
	LossRate float64 `json:"loss_rate"`
}

// ProbeOutagePlan 按机房下标定位，不按设备。
type ProbeOutagePlan struct {
	Room            int     `json:"room"`
	StartAt         float64 `json:"start_at"`
	DurationSeconds float64 `json:"duration_s"`
}

// DefaultPlan 是内置默认值。samples/scenarios.json 不存在时用它。
func DefaultPlan() ScenarioPlan {
	return ScenarioPlan{
		Rooms:    4,
		HardDown: FaultPlan{Count: 2, StartAt: 0.20, DurationSeconds: 900},
		Reboot:   FaultPlan{Count: 2, StartAt: 0.35, DurationSeconds: 240, JitterSeconds: 60},
		CPUStall: CPUPlan{
			FaultPlan: FaultPlan{Count: 3, StartAt: 0.50, DurationSeconds: 600},
			LossRate:  0.6,
		},
		ProbeOutage: ProbeOutagePlan{Room: 2, StartAt: 0.60, DurationSeconds: 300},
	}
}

// normalized 补齐缺失字段，让半份 JSON 也能跑。
func (p ScenarioPlan) normalized() ScenarioPlan {
	def := DefaultPlan()
	if p.Rooms < 1 {
		p.Rooms = def.Rooms
	}
	if p.HardDown.Count < 0 {
		p.HardDown.Count = def.HardDown.Count
	}
	if p.HardDown.DurationSeconds <= 0 {
		p.HardDown = def.HardDown
	}
	if p.Reboot.DurationSeconds <= 0 {
		p.Reboot = def.Reboot
	}
	if p.CPUStall.DurationSeconds <= 0 {
		p.CPUStall = def.CPUStall
	}
	if p.CPUStall.LossRate <= 0 {
		p.CPUStall.LossRate = def.CPUStall.LossRate
	}
	if p.ProbeOutage.DurationSeconds <= 0 {
		p.ProbeOutage = def.ProbeOutage
	}
	return p
}

// LoadScenarioPlan 读剧本参数文件；path 为空或不存时返回内置默认值。
func LoadScenarioPlan(path string) (ScenarioPlan, string, error) {
	if path == "" {
		return DefaultPlan(), "builtin", nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return DefaultPlan(), "builtin", nil
		}
		return ScenarioPlan{}, "", err
	}
	var plan ScenarioPlan
	if err := json.Unmarshal(data, &plan); err != nil {
		return ScenarioPlan{}, "", fmt.Errorf("解析 %s 失败: %w", path, err)
	}
	return plan.normalized(), path, nil
}

// ScenarioConfig 是装配剧本需要的全部输入。
type ScenarioConfig struct {
	Devices  int
	Ticks    int
	Interval time.Duration
	Seed     int64
	Plan     ScenarioPlan
}
