package main

import (
	"math/rand"
	"sort"
	"time"
)

// 一条长连接在控制面上的固定开销。十万连接这条账全靠这三个数：
//   - 连接状态：位点、任务表、租约，约 2 KiB
//   - 收发缓冲：读写各 2 KiB
//   - 协程栈：Go 的初始栈 8 KiB
//
// 合计 14 KiB。十万连接 1.4 GiB，还没算探针表与路由索引。
const (
	connStateBytes     = 2 * 1024
	connBufferBytes    = 4 * 1024
	connGoroutineBytes = 8 * 1024
	connTotalBytes     = connStateBytes + connBufferBytes + connGoroutineBytes
)

// pollPhase 把各探针的轮询时刻打散到 poll 周期里。
//
// 不打散的话，十万探针会在整秒上同时醒，控制面每一秒吃一发脉冲、剩下四秒闲着。
// 打散之后同样的平均 QPS，峰值是 1/64，这就是超时与排队的差别。
func pollPhase(i int, poll time.Duration) time.Duration {
	return poll * time.Duration(i%64) / 64
}

// nextPollTick 返回严格晚于 t 的第一个轮询时刻。
//
// 严格晚于是有意的：变更落地的瞬间，那一轮轮询已经拿着旧数据走了，
// 探针只能等下一轮。写成不早于就会算出 0 延迟，把分布的下界抹掉。
func nextPollTick(t, phase, poll time.Duration) time.Duration {
	if t < phase {
		return phase
	}
	k := (t-phase)/poll + 1
	return phase + k*poll
}

// DispatchResult 是一种分发模式的仿真结果。
type DispatchResult struct {
	Mode   string
	Label  string
	Probes int
	// Apply 是从控制面视角看到的应用记录，含派活与收活两种。
	Apply []Application
	// Stats 只统计主路径派活的延迟，重复下发那份额外的不算进来。
	Stats LatencyStats
	// Stale 是生效延迟超过最大补报延迟的变更数。
	Stale int
	// Undeployed 是探针已退役、变更永远下发不到的次数。
	Undeployed int

	PollRequests       int64
	HeartbeatRequests  int64
	HandshakeRequests  int64
	DispatchMessages   int64
	ConnectionSeconds  float64
	PeakDispatchPerSec int
}

func (r DispatchResult) totalRequests() int64 {
	return r.PollRequests + r.HeartbeatRequests + r.HandshakeRequests + r.DispatchMessages
}

// Extrapolate 把结果按探针数放大，十万探针的数就是这么算出来的：
// 请求数与内存跟探针数成正比，延迟分布不随规模变（仿真里控制面没有排队）。
// 返回每小时请求数与长连接占用内存。
func (r DispatchResult) Extrapolate(probes int, cfg Config) (hourly int64, connBytes int64) {
	if r.Probes == 0 || cfg.Duration <= 0 {
		return 0, 0
	}
	f := float64(probes) / float64(r.Probes) * (float64(time.Hour) / float64(cfg.Duration))
	hourly = int64(float64(r.totalRequests()) * f)
	if r.Mode == string(ModePush) {
		connBytes = int64(probes) * connTotalBytes
	}
	return hourly, connBytes
}

// SimulatePull 跑拉取式：探针每 Poll 醒一次，问控制面「我有新活吗」。
//
// 变更到生效 = 到下一个轮询时刻为止。所以延迟上限就是轮询周期，
// 探针离线时上限变成「离线时长 + 一个轮询周期」。
func SimulatePull(cfg Config, probes []Probe, changes []Change) DispatchResult {
	idx := make(map[string]int, len(probes))
	for i, p := range probes {
		idx[p.ProbeID] = i
	}
	res := DispatchResult{Mode: string(ModePull), Label: pullLabel(cfg.Poll), Probes: len(probes)}

	applyAt := func(probe Probe, i int, at time.Duration) (time.Duration, bool) {
		cur := nextPollTick(at, pollPhase(i, cfg.Poll), cfg.Poll)
		for guard := 0; guard <= len(probe.Offline)+2; guard++ {
			online, ok := probe.NextOnlineAt(cur)
			if !ok {
				return 0, false
			}
			if online == cur {
				return cur, true
			}
			cur = nextPollTick(online, pollPhase(i, cfg.Poll), cfg.Poll)
		}
		return 0, false
	}

	for _, ch := range changes {
		pi, ok := idx[ch.ToProbe]
		if !ok {
			continue
		}
		applied, ok := applyAt(probes[pi], pi, ch.At)
		if !ok {
			res.Undeployed++
			continue
		}
		res.Apply = append(res.Apply, Application{
			TaskID: ch.TaskID, MetricID: ch.MetricID, Revision: ch.Revision,
			ProbeID: ch.ToProbe, Kind: ch.Kind, Role: RoleAssign,
			ChangeAt: ch.At, AppliedAt: applied, Latency: applied - ch.At,
			Stale: applied-ch.At > cfg.MaxReportDelay,
		})
		if ch.Kind == KindMove {
			if fi, ok := idx[ch.FromProbe]; ok && ch.FromProbe != ch.ToProbe {
				if rev, ok := applyAt(probes[fi], fi, ch.At); ok {
					res.Apply = append(res.Apply, Application{
						TaskID: ch.TaskID, MetricID: ch.MetricID, Revision: ch.Revision,
						ProbeID: ch.FromProbe, Kind: ch.Kind, Role: RoleRevoke,
						ChangeAt: ch.At, AppliedAt: rev, Latency: rev - ch.At,
						Stale: rev-ch.At > cfg.MaxReportDelay,
					})
				}
				if ch.DupInject {
					// 撤销消息丢了：旧点也收到新 revision，于是两个探针同时接活。
					if dup, ok := applyAt(probes[fi], fi, ch.At); ok {
						res.Apply = append(res.Apply, Application{
							TaskID: ch.TaskID, MetricID: ch.MetricID, Revision: ch.Revision,
							ProbeID: ch.FromProbe, Kind: ch.Kind, Role: RoleAssign,
							ChangeAt: ch.At, AppliedAt: dup, Latency: dup - ch.At,
							Stale: dup-ch.At > cfg.MaxReportDelay, Dup: true,
						})
					}
				}
			}
		}
	}

	// 请求数：在线期间每 Poll 问一次。
	for i, p := range probes {
		phase := pollPhase(i, cfg.Poll)
		for t := phase; t <= cfg.Duration; t += cfg.Poll {
			if p.online(t) {
				res.PollRequests++
			}
		}
	}
	res.DispatchMessages = int64(len(res.Apply))
	res.HeartbeatRequests = 0
	res.HandshakeRequests = 0
	res.ConnectionSeconds = 0
	res.finish(cfg)
	return res
}

// SimulatePush 跑长连接推送式：控制面握着十万条连接，有变更立刻 write。
//
// 变更到生效 = 单程网络延迟 + 一次处理抖动。探针掉线之后不是立刻回来：
// 重连要退避，所以尾延迟由「离线时长 + 退避 + 单程延迟」决定。
func SimulatePush(cfg Config, probes []Probe, changes []Change) DispatchResult {
	idx := make(map[string]int, len(probes))
	for i, p := range probes {
		idx[p.ProbeID] = i
	}
	res := DispatchResult{Mode: string(ModePush), Label: pushLabel(cfg), Probes: len(probes)}
	jitter := rand.New(rand.NewSource(cfg.Seed + 7))

	// latency 算一条消息从变更产生到探针生效的间隔。
	latency := func(p Probe, at time.Duration) (time.Duration, time.Duration, bool) {
		d := cfg.PushDelay
		if cfg.PushDelay > 0 {
			d += time.Duration(jitter.Int63n(int64(cfg.PushDelay)))
		}
		if p.online(at) {
			return at + d, d, true
		}
		onlineAt, ok := p.NextOnlineAt(at)
		if !ok {
			return 0, 0, false
		}
		// 掉线后重连要退避，退避完才能把积压的消息推下去。
		applied := onlineAt + cfg.ReconnectBackoff + d
		return applied, applied - at, true
	}

	for _, ch := range changes {
		pi, ok := idx[ch.ToProbe]
		if !ok {
			continue
		}
		applied, lat, ok := latency(probes[pi], ch.At)
		if !ok {
			res.Undeployed++
			continue
		}
		res.Apply = append(res.Apply, Application{
			TaskID: ch.TaskID, MetricID: ch.MetricID, Revision: ch.Revision,
			ProbeID: ch.ToProbe, Kind: ch.Kind, Role: RoleAssign,
			ChangeAt: ch.At, AppliedAt: applied, Latency: lat,
			Stale: lat > cfg.MaxReportDelay,
		})
		if ch.Kind == KindMove {
			if fi, ok := idx[ch.FromProbe]; ok && ch.FromProbe != ch.ToProbe {
				if rev, lat2, ok := latency(probes[fi], ch.At); ok {
					res.Apply = append(res.Apply, Application{
						TaskID: ch.TaskID, MetricID: ch.MetricID, Revision: ch.Revision,
						ProbeID: ch.FromProbe, Kind: ch.Kind, Role: RoleRevoke,
						ChangeAt: ch.At, AppliedAt: rev, Latency: lat2,
						Stale: lat2 > cfg.MaxReportDelay,
					})
				}
				if ch.DupInject {
					if dup, lat3, ok := latency(probes[fi], ch.At); ok {
						res.Apply = append(res.Apply, Application{
							TaskID: ch.TaskID, MetricID: ch.MetricID, Revision: ch.Revision,
							ProbeID: ch.FromProbe, Kind: ch.Kind, Role: RoleAssign,
							ChangeAt: ch.At, AppliedAt: dup, Latency: lat3,
							Stale: lat3 > cfg.MaxReportDelay, Dup: true,
						})
					}
				}
			}
		}
	}

	// 请求数：心跳按在线时长算，外加建连与重连。
	online := time.Duration(0)
	for _, p := range probes {
		seg := p.onlineDuration(0, cfg.Duration)
		online += seg
		res.HeartbeatRequests += int64(seg / cfg.Heartbeat)
		res.HandshakeRequests++
		res.HandshakeRequests += int64(len(p.Offline))
		if p.DecommissionedAt >= 0 && p.DecommissionedAt < cfg.Duration {
			res.HandshakeRequests--
		}
	}
	res.ConnectionSeconds = online.Seconds()
	res.DispatchMessages = int64(len(res.Apply))
	res.finish(cfg)
	return res
}

// finish 收尾：算分位、直方图、峰值。
func (r *DispatchResult) finish(cfg Config) {
	sort.SliceStable(r.Apply, func(i, j int) bool {
		if r.Apply[i].AppliedAt != r.Apply[j].AppliedAt {
			return r.Apply[i].AppliedAt < r.Apply[j].AppliedAt
		}
		return r.Apply[i].TaskID < r.Apply[j].TaskID
	})
	lats := make([]time.Duration, 0, len(r.Apply))
	for _, a := range r.Apply {
		if a.Role != RoleAssign || a.Dup {
			continue
		}
		lats = append(lats, a.Latency)
		if a.Stale {
			r.Stale++
		}
	}
	r.Stats = NewLatencyStats(lats)
	_ = cfg
}

func pullLabel(poll time.Duration) string {
	return "拉取 " + dur(poll)
}

func pushLabel(cfg Config) string {
	return "长连接 " + dur(cfg.PushDelay)
}
