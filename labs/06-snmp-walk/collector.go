package main

import (
	"fmt"
	"sort"
	"time"
)

// DevState 是一台设备这一轮采完之后给出的结论。
//
// 三个值分别对应三种处置：接着采、降频、隔离。
// 「不可达」和「响应慢」必须分开统计——前者该查电源和带外，
// 后者该查管理面 CPU，合成一个「异常」两件事都查不了。
type DevState string

const (
	StateOK          DevState = "ok"
	StateSlow        DevState = "slow"
	StateUnreachable DevState = "unreachable"
)

// 判定与降频的门槛。这些是策略，改一行就能重跑，写死在算式里就没法调了。
const (
	// maxFailedColumns 是一轮里连续几列拿不到就中止整台设备。
	// 12 列里丢 3 列，剩下的数据也没法用了，再打下去只是把设备压得更死。
	maxFailedColumns = 3
	// slowLatency 是「响应慢」的门槛：平均往返超过它就不算健康。
	slowLatency = 500 * time.Millisecond
	// slowStreakToDegrade 是连续几轮判慢之后开始降频。
	slowStreakToDegrade = 2
	// degradeSkipCycles 是降频时跳过几轮。
	degradeSkipCycles = 3
	// quarantinePatrolEvery 是被隔离的设备每隔几轮探一次。
	quarantinePatrolEvery = 4
)

// Config 是一次采集的全部旋钮。
type Config struct {
	Cycles          int
	Interval        time.Duration
	RepsOverride    int           // >0 时忽略档位，直接要这么多条（演练 tooBig 背退）
	TimeoutOverride time.Duration // >0 时忽略档位超时
	Seed            int64
}

// Validate 挡住会让实验跑出假结论的配置。
func (c Config) Validate() error {
	switch {
	case c.Cycles < 1:
		return fmt.Errorf("采集轮数必须 ≥ 1")
	case c.Interval <= 0:
		return fmt.Errorf("采集周期必须大于 0")
	case c.RepsOverride < 0:
		return fmt.Errorf("max-repetitions 覆盖值不能为负")
	case c.TimeoutOverride < 0:
		return fmt.Errorf("超时覆盖值不能为负")
	}
	return nil
}

// Session 是一台设备的采集状态。它跨轮存在：限流桶、慢/不可达计数、
// 隔离到期时间都在这里——这些状态如果每轮重建，降频和隔离就无从谈起。
type Session struct {
	Dev    *Device
	Policy TierPolicy
	Reps   int

	Lim *RateLimiter

	State      DevState
	SlowStreak int
	FailStreak int
	SkipUntil  int // 到第几轮之前都跳过

	Totals WalkStats
	// LatencySamples 是全部请求的往返毫秒，报表里算 p95。
	LatencySamples []int64
	LastPorts      []Port
}

// Timeout 是这次会话实际用的超时。
func (s *Session) Timeout(cfg Config) time.Duration {
	if cfg.TimeoutOverride > 0 {
		return cfg.TimeoutOverride
	}
	return s.Policy.Timeout
}

// CycleResult 是「一台设备在一轮里」的完整结论。
type CycleResult struct {
	Cycle   int
	Device  string
	Tier    string
	Reps    int
	State   DevState
	Skipped bool
	Note    string
	Columns int      // 采成功的列数
	Missing []string // 没采到的列
	Rows    int      // 拼出多少行端口
	Ports   []Port
	Stats   WalkStats
	Elapsed time.Duration // 这台设备这一轮占掉的时钟（排队 + 往返）
}

// CycleInfo 是每轮的时间账。慢设备会把后面的设备挤晚，
// 这一列数字就是「采不完」的证据。
type CycleInfo struct {
	Cycle       int
	ScheduledMs int64
	ActualMs    int64
	LateMs      int64
}

// Collector 把传输层、时钟、限流与判定串起来。
type Collector struct {
	Fleet     *Fleet
	Transport Transport
	Clock     Clock
	Cfg       Config

	Sessions  []*Session
	Results   []CycleResult
	Cycles    []CycleInfo
	LastPorts map[string][]Port
	StartedAt time.Time
	EndedAt   time.Time
}

// NewCollector 建采集器并给每台设备算档位。
// 档位来自样本里设备自报的能力（相当于 CMDB 里登记的型号与固件），
// 档位认不出来就整场失败——静默降级成 A 档会打死老设备。
func NewCollector(f *Fleet, tr Transport, clk Clock, cfg Config) (*Collector, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	c := &Collector{
		Fleet:     f,
		Transport: tr,
		Clock:     clk,
		Cfg:       cfg,
		LastPorts: make(map[string][]Port, len(f.Devices)),
		StartedAt: clk.Now(),
	}
	for i := range f.Devices {
		d := &f.Devices[i]
		p, err := PolicyFor(d.SNMP.Tier)
		if err != nil {
			return nil, fmt.Errorf("设备 %s: %w", d.Name, err)
		}
		reps := p.MaxRepetitions
		if cfg.RepsOverride > 0 {
			reps = cfg.RepsOverride
		}
		c.Sessions = append(c.Sessions, &Session{
			Dev:    d,
			Policy: p,
			Reps:   reps,
			Lim:    NewRateLimiter(p),
			State:  StateOK,
		})
	}
	sort.Slice(c.Sessions, func(i, j int) bool { return c.Sessions[i].Dev.Name < c.Sessions[j].Dev.Name })
	return c, nil
}

// Run 按轮推进。每一轮先把时钟拨到计划时刻，再逐台设备采。
func (c *Collector) Run() {
	for cycle := 1; cycle <= c.Cfg.Cycles; cycle++ {
		scheduled := c.StartedAt.Add(time.Duration(cycle-1) * c.Cfg.Interval)
		if d := scheduled.Sub(c.Clock.Now()); d > 0 {
			c.Clock.Wait(d)
		}
		info := CycleInfo{Cycle: cycle, ScheduledMs: int64(time.Duration(cycle-1) * c.Cfg.Interval / time.Millisecond)}
		info.ActualMs = int64(c.Clock.Now().Sub(c.StartedAt) / time.Millisecond)
		info.LateMs = info.ActualMs - info.ScheduledMs
		for _, s := range c.Sessions {
			res := c.collectOne(s, cycle)
			c.Results = append(c.Results, res)
			if res.Rows > 0 && len(res.Ports) > 0 {
				c.LastPorts[s.Dev.Name] = res.Ports
				s.LastPorts = res.Ports
			}
		}
		c.Cycles = append(c.Cycles, info)
	}
	c.EndedAt = c.Clock.Now()
}

// collectOne 采一台设备的一轮。
func (c *Collector) collectOne(s *Session, cycle int) CycleResult {
	res := CycleResult{Cycle: cycle, Device: s.Dev.Name, Tier: s.Policy.Tier, Reps: s.Reps, State: s.State}
	if cycle <= s.SkipUntil {
		res.Skipped = true
		res.Reps = 0
		res.Note = fmt.Sprintf("跳过（%s，第 %d 轮再采）", stateNote(s.State), s.SkipUntil+1)
		return res
	}
	res.Reps = s.Reps
	begin := c.Clock.Now()

	var rows RowSet = make(map[uint32]*TableRow)
	failed := 0
	for _, col := range Columns {
		var st WalkStats
		vbs, err := WalkColumn(col, s.Reps, func(req BulkRequest) (Response, error) {
			return c.request(s, req, &st)
		}, &st)
		res.Stats.Merge(st)
		s.Totals.Merge(st)
		if err != nil {
			res.Missing = append(res.Missing, col.Name)
			failed++
			if failed >= maxFailedColumns {
				res.Note = fmt.Sprintf("连续 %d 列取不到，中止本轮", failed)
				break
			}
			continue
		}
		res.Columns++
		s.Reps = st.Reps // 背退后的条数留到下一列、下一轮
		if rows == nil {
			rows = make(map[uint32]*TableRow)
		}
		rows.Add(col, vbs)
	}
	res.Ports = PortsFromRows(s.Dev.Name, rows.Sorted())
	res.Rows = len(res.Ports)
	res.Elapsed = c.Clock.Now().Sub(begin)
	res.State = classify(res)
	if res.Elapsed > c.Cfg.Interval {
		// 一轮采集比周期还长，下一轮的起点已经不成立了。
		// 这种设备只能降频或分列分片，否则永远在追赶自己上一轮。
		res.Note = appendNote(res.Note, fmt.Sprintf("本轮占 %s，超过周期 %s",
			fmtMs(res.Elapsed), fmtMs(c.Cfg.Interval)))
	}

	c.applyState(s, &res, cycle)
	return res
}

// request 发一次请求：限流排队 → 传输 → 超时重试。所有账记在 st 上。
func (c *Collector) request(s *Session, req BulkRequest, st *WalkStats) (Response, error) {
	timeout := s.Timeout(c.Cfg)
	var lastErr error
	for attempt := 1; attempt <= s.Policy.Attempts; attempt++ {
		if ready := s.Lim.Reserve(c.Clock.Now()); ready.After(c.Clock.Now()) {
			wait := ready.Sub(c.Clock.Now())
			c.Clock.Wait(wait)
			st.QueueWait += wait.Milliseconds()
		}
		resp, elapsed := c.Transport.Request(s.Dev.Name, req, timeout)
		c.Clock.Spent(elapsed)
		st.Requests++
		st.Latency += elapsed.Milliseconds()
		s.LatencySamples = append(s.LatencySamples, elapsed.Milliseconds())
		if resp.Error == ErrTimeout {
			st.Timeouts++
			lastErr = fmt.Errorf("%s 超时（%.1fs）", s.Dev.Name, timeout.Seconds())
			if attempt < s.Policy.Attempts {
				st.Retries++
				continue
			}
			return resp, lastErr
		}
		return resp, nil
	}
	return Response{Error: ErrTimeout}, lastErr
}

// classify 给出这一轮的结论。判据只有两条，都要看数字，不看感觉。
func classify(res CycleResult) DevState {
	if res.Rows == 0 {
		return StateUnreachable
	}
	n := int64(res.Stats.Requests)
	if n == 0 {
		n = 1
	}
	avg := time.Duration(res.Stats.Latency/n) * time.Millisecond
	if res.Stats.Timeouts > 0 || avg > slowLatency {
		return StateSlow
	}
	return StateOK
}

// applyState 更新设备状态并安排下一轮是采还是跳过。
func (c *Collector) applyState(s *Session, res *CycleResult, cycle int) {
	switch res.State {
	case StateOK:
		s.State = StateOK
		s.SlowStreak = 0
		s.FailStreak = 0
	case StateSlow:
		s.State = StateSlow
		s.SlowStreak++
		s.FailStreak = 0
		if s.SlowStreak >= slowStreakToDegrade && cycle+degradeSkipCycles > s.SkipUntil {
			s.SkipUntil = cycle + degradeSkipCycles
			res.Note = appendNote(res.Note, fmt.Sprintf("连续 %d 轮判慢，降频跳过 %d 轮", s.SlowStreak, degradeSkipCycles))
		}
	case StateUnreachable:
		s.State = StateUnreachable
		s.FailStreak++
		s.SlowStreak = 0
		if s.FailStreak >= 1 {
			s.SkipUntil = cycle + quarantinePatrolEvery
			res.Note = appendNote(res.Note, fmt.Sprintf("整轮零行，隔离；第 %d 轮再探", s.SkipUntil+1))
		}
	}
}

func appendNote(note, add string) string {
	if note == "" {
		return add
	}
	return note + "；" + add
}

func stateNote(s DevState) string {
	switch s {
	case StateSlow:
		return "响应慢"
	case StateUnreachable:
		return "不可达"
	default:
		return "正常"
	}
}

// PortTable 取一台设备最后一次采到的端口表。
func (c *Collector) PortTable(device string) (*PortTable, bool) {
	ports, ok := c.LastPorts[device]
	if !ok {
		return nil, false
	}
	return NewPortTable(device, ports), true
}

// LatencyP95 取一台设备全部请求往返的 p95，毫秒。
func (s *Session) LatencyP95() int64 {
	if len(s.LatencySamples) == 0 {
		return 0
	}
	xs := append([]int64(nil), s.LatencySamples...)
	sort.Slice(xs, func(i, j int) bool { return xs[i] < xs[j] })
	idx := int(float64(len(xs)) * 0.95)
	if idx >= len(xs) {
		idx = len(xs) - 1
	}
	return xs[idx]
}

// Summary 是整场采集的合计，报表与产物都要。
type Summary struct {
	Requests  int
	Varbinds  int
	Rows      int
	TooBig    int
	Retries   int
	Timeouts  int
	QueueWait time.Duration
	Devices   int
	OkDevices int
	Slow      int
	Unreach   int
}

// Summarize 按设备最后一次结论合计。
func (c *Collector) Summarize() Summary {
	var s Summary
	s.Devices = len(c.Sessions)
	for _, sess := range c.Sessions {
		s.Requests += sess.Totals.Requests
		s.Varbinds += sess.Totals.Varbinds
		s.TooBig += sess.Totals.TooBig
		s.Retries += sess.Totals.Retries
		s.Timeouts += sess.Totals.Timeouts
		s.QueueWait += time.Duration(sess.Totals.QueueWait) * time.Millisecond
		s.Rows += len(sess.LastPorts)
		switch sess.State {
		case StateOK:
			s.OkDevices++
		case StateSlow:
			s.Slow++
		case StateUnreachable:
			s.Unreach++
		}
	}
	return s
}
