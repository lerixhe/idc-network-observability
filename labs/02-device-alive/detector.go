package main

// probeFunc 是一次探测。判定层拿到的全部信息就是这个布尔值：
// 没有错误码，没有时延，没有「为什么不通」。
type probeFunc func(index, tick int) bool

// StateChange 是一次状态跃迁。评测靠它重建任意时刻的状态，
// 比存一份每轮全量状态省得多——一万台设备跑一天也只有几百次跃迁。
type StateChange struct {
	Tick  int   `json:"tick"`
	State State `json:"state"`
}

// DeviceTrack 是一台设备的判定轨迹。
type DeviceTrack struct {
	Device    Device
	State     State
	Fails     int // 当前连续失败次数
	OKs       int // 当前连续成功次数
	SinceTick int // 进入当前状态的那一轮
	Changes   []StateChange
}

// GroupTrack 是一个同源分组：同机房、同探针。
//
// 同组设备的探测结果不独立——管理网一抖，整组一起丢。
// 判定的难点不在「没人回」，在「到底是谁不在了」。
type GroupTrack struct {
	Key          string
	Total        int
	Suspect      bool
	SuspectSince int
	Peers        []int // 组内设备下标
	EnterCount   int   // 被判定为探测侧故障的次数
	Hits         int   // 连续满足可疑条件的轮数
	Misses       int   // 连续不满足的轮数
}

// Monitor 是判定层。它只做三件事：去抖、算同源相关性、发事件。
type Monitor struct {
	cfg        Config
	devices    []Device
	groupKey   []string
	tracks     []DeviceTrack
	groups     map[string]*GroupTrack
	groupOrder []string
	events     []Event
	seq        int
}

// NewMonitor 按设备表建轨道。设备顺序就是遍历顺序，同一份输入必定同一份输出。
func NewMonitor(cfg Config, devices []Device) *Monitor {
	m := &Monitor{
		cfg:      cfg,
		devices:  devices,
		groupKey: make([]string, len(devices)),
		tracks:   make([]DeviceTrack, len(devices)),
		groups:   make(map[string]*GroupTrack),
	}
	for i, d := range devices {
		key := d.Group()
		m.groupKey[i] = key
		g, ok := m.groups[key]
		if !ok {
			g = &GroupTrack{Key: key}
			m.groups[key] = g
			m.groupOrder = append(m.groupOrder, key)
		}
		g.Total++
		g.Peers = append(g.Peers, i)
		m.tracks[i] = DeviceTrack{
			Device:  d,
			State:   StateUnknown,
			Changes: []StateChange{{Tick: 0, State: StateUnknown}},
		}
	}
	return m
}

// tickStat 是一轮探测的汇总量。每轮复用，不重复分配。
type tickStat struct {
	failed  map[string]int
	ratio   map[string]float64
	suspect map[string]bool
}

// Run 推进整段仿真。每一轮的顺序固定：先探测，再按组算相关性，
// 最后逐台推进状态机——顺序反了就会拿上一轮的组信息判这一轮的设备。
func (m *Monitor) Run(probe probeFunc) {
	ticks := m.cfg.Ticks()
	ok := make([]bool, len(m.devices))
	st := &tickStat{
		failed:  make(map[string]int, len(m.groups)),
		ratio:   make(map[string]float64, len(m.groups)),
		suspect: make(map[string]bool, len(m.groups)),
	}
	for tick := 0; tick < ticks; tick++ {
		for i := range m.devices {
			ok[i] = probe(i, tick)
		}
		m.scoreGroups(tick, ok, st)
		m.advanceDevices(tick, ok, st)
	}
}

// scoreGroups 给每个同源分组打一轮分，顺手发探测侧故障的上下事件。
func (m *Monitor) scoreGroups(tick int, ok []bool, st *tickStat) {
	for _, k := range m.groupOrder {
		st.failed[k] = 0
	}
	for i := range m.devices {
		if !ok[i] {
			st.failed[m.groupKey[i]]++
		}
	}
	for _, k := range m.groupOrder {
		g := m.groups[k]
		failed := st.failed[k]
		ratio := 0.0
		if g.Total > 0 {
			ratio = float64(failed) / float64(g.Total)
		}
		ratioOK := g.Total >= m.cfg.CorrMinPeers && ratio >= m.cfg.CorrThreshold
		st.ratio[k] = ratio

		// 探测侧故障也要去抖：一次全组丢包可能只是网络抖了一下，
		// 一轮就发事件，探针侧抖一次就是几百条假故障。
		if ratioOK {
			g.Hits++
			g.Misses = 0
		} else {
			g.Misses++
			g.Hits = 0
		}

		switch {
		case !g.Suspect && g.Hits >= m.cfg.CorrTicks:
			g.Suspect = true
			g.EnterCount++
			g.SuspectSince = tick - g.Hits + 1
			if g.SuspectSince < 0 {
				g.SuspectSince = 0
			}
			st.suspect[k] = true
			m.emit(Event{
				AtMs:    m.cfg.AtMs(tick),
				Kind:    KindProbeFault,
				Subject: k,
				Evidence: Evidence{
					PeerTotal:     g.Total,
					PeerFailed:    failed,
					PeerFailRatio: ratio,
				},
			})
		case g.Suspect && g.Misses >= m.cfg.CorrTicks:
			downtime := m.cfg.AtMs(tick - g.SuspectSince)
			g.Suspect = false
			g.SuspectSince = 0
			st.suspect[k] = false
			m.emit(Event{
				AtMs:    m.cfg.AtMs(tick),
				Kind:    KindProbeRecovered,
				Subject: k,
				Evidence: Evidence{
					PeerTotal:     g.Total,
					PeerFailed:    failed,
					PeerFailRatio: ratio,
					DowntimeMs:    downtime,
				},
			})
		default:
			st.suspect[k] = g.Suspect
		}
	}
}

// advanceDevices 是去抖状态机本身。
func (m *Monitor) advanceDevices(tick int, ok []bool, st *tickStat) {
	for i := range m.devices {
		key := m.groupKey[i]
		// 探测侧故障期间整轮观测作废：失败不累计（可能根本不是设备的错），
		// 成功也不累计（探针半瞎时，「通」的可信度同样打折）。
		if st.suspect[key] {
			continue
		}

		t := &m.tracks[i]
		g := m.groups[key]
		peer := Evidence{
			PeerTotal:     g.Total,
			PeerFailed:    st.failed[key],
			PeerFailRatio: st.ratio[key],
		}

		if ok[i] {
			t.Fails = 0
			t.OKs++
			switch {
			case t.State == StateDown && t.OKs >= m.cfg.RecoverThreshold:
				downtime := m.cfg.AtMs(tick - t.SinceTick)
				m.setState(t, tick, StateUp)
				ev := peer
				ev.ConsecutiveOKs = t.OKs
				ev.DowntimeMs = downtime
				ev.SuspectReboot = downtime <= m.cfg.RebootSuspect.Milliseconds()
				m.emit(Event{AtMs: m.cfg.AtMs(tick), Kind: KindDeviceUp, Subject: t.Device.ID, Evidence: ev})
			case t.State == StateUnknown:
				// 刚接入的正常设备不产生上线事件，否则平台重启一次就是一万条。
				m.setState(t, tick, StateUp)
			}
			continue
		}

		t.OKs = 0
		t.Fails++
		if t.State != StateDown && t.Fails >= m.cfg.FailThreshold {
			m.setState(t, tick, StateDown)
			ev := peer
			ev.ConsecutiveFails = t.Fails
			m.emit(Event{AtMs: m.cfg.AtMs(tick), Kind: KindDeviceDown, Subject: t.Device.ID, Evidence: ev})
		}
	}
}

func (m *Monitor) setState(t *DeviceTrack, tick int, s State) {
	if t.State == s {
		return
	}
	t.State = s
	t.SinceTick = tick
	t.Changes = append(t.Changes, StateChange{Tick: tick, State: s})
}

func (m *Monitor) emit(e Event) {
	m.seq++
	e.Seq = m.seq
	m.events = append(m.events, e)
}

// Events 返回事件流。顺序即发生顺序，Seq 单调递增。
func (m *Monitor) Events() []Event { return m.events }

// Config 返回判定策略，报告与对账都要用到。
func (m *Monitor) Config() Config { return m.cfg }

// Tracks 返回全部设备的判定轨迹。
func (m *Monitor) Tracks() []DeviceTrack { return m.tracks }

// Groups 按稳定顺序返回同源分组。
func (m *Monitor) Groups() []*GroupTrack {
	out := make([]*GroupTrack, 0, len(m.groupOrder))
	for _, k := range m.groupOrder {
		out = append(out, m.groups[k])
	}
	return out
}

// StateAt 重建某台设备在第 tick 轮之后的状态。评测用它做逐轮对账。
func (t DeviceTrack) StateAt(tick int) State {
	state := StateUnknown
	for _, c := range t.Changes {
		if c.Tick > tick {
			break
		}
		state = c.State
	}
	return state
}

// Snapshot 生成最后一轮的快照。
func (m *Monitor) Snapshot(scenarioName string) Snapshot {
	states := make([]DeviceState, len(m.tracks))
	counters := Counters{}
	for _, e := range m.events {
		counters.Events++
		switch e.Kind {
		case KindDeviceDown:
			counters.DeviceDown++
		case KindDeviceUp:
			counters.DeviceUp++
		case KindProbeFault:
			counters.ProbeFault++
		case KindProbeRecovered:
			counters.ProbeRecovered++
		}
	}
	for i, t := range m.tracks {
		states[i] = DeviceState{
			ID:      t.Device.ID,
			Room:    t.Device.Room,
			Probe:   t.Device.Probe,
			State:   t.State,
			SinceMs: m.cfg.AtMs(t.SinceTick),
			Fails:   t.Fails,
			OKs:     t.OKs,
		}
		switch t.State {
		case StateDown:
			counters.DownNow++
		case StateUp:
			counters.UpNow++
		default:
			counters.UnknownNow++
		}
		if t.State != StateDown && downBefore(t.Changes) {
			counters.FlappedDevices++
		}
	}
	return Snapshot{
		Scenario: scenarioName,
		AtMs:     m.cfg.AtMs(m.cfg.Ticks()),
		Config:   SnapshotConfigOf(m.cfg),
		Counters: counters,
		Devices:  states,
	}
}

func downBefore(changes []StateChange) bool {
	for _, c := range changes {
		if c.State == StateDown {
			return true
		}
	}
	return false
}
