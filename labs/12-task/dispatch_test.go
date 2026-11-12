package main

import (
	"testing"
	"time"
)

// oneProbe 造一台探针，离线窗口可选。
func oneProbe(id string, offline ...Window) Probe {
	return Probe{ProbeID: id, Site: "room-a", Capacity: 8, Version: "2.4.0", Offline: offline, DecommissionedAt: -1}
}

// singleChange 造一条落在某台探针上的内容变更。
func singleChange(probeID string, at time.Duration) Change {
	return Change{
		Seq: 1, TaskID: "t-1", MetricID: "if_err_rate", Revision: 2,
		Kind: KindRevise, At: at, FromProbe: probeID, ToProbe: probeID,
	}
}

func TestPullLatencyWithinOnePoll(t *testing.T) {
	cfg := testConfig()
	probes := []Probe{oneProbe("p-1")}
	var changes []Change
	for i := 1; i <= 20; i++ {
		changes = append(changes, singleChange("p-1", time.Duration(i)*17*time.Second))
	}
	res := SimulatePull(cfg, probes, changes)
	if res.Stats.N != len(changes) {
		t.Fatalf("应当统计到 %d 条变更，实际 %d", len(changes), res.Stats.N)
	}
	for _, a := range res.Apply {
		if a.Latency <= 0 || a.Latency > cfg.Poll {
			t.Fatalf("拉取延迟必须落在 (0, %s]，实际 %s（变更 %s）", cfg.Poll, a.Latency, dur(a.ChangeAt))
		}
	}
	if res.Stats.Max > cfg.Poll {
		t.Fatalf("最大延迟 %s 超过轮询周期 %s", res.Stats.Max, cfg.Poll)
	}
}

func TestPushLatencyBeatsPull(t *testing.T) {
	cfg, sc := buildDefault(t)
	pull := SimulatePull(cfg, sc.Probes, sc.Changes)
	push := SimulatePush(cfg, sc.Probes, sc.Changes)

	if push.Stats.P50 >= pull.Stats.P50 {
		t.Fatalf("推送 p50 %s 应当低于拉取 p50 %s", push.Stats.P50, pull.Stats.P50)
	}
	// 变更产生时探针在线的那些，推送延迟就是单程延迟加抖动，不超过 2 倍单程。
	byID := map[string]Probe{}
	for _, p := range sc.Probes {
		byID[p.ProbeID] = p
	}
	online := 0
	for _, a := range push.Apply {
		if a.Role != RoleAssign || a.Dup {
			continue
		}
		p := byID[a.ProbeID]
		if !p.online(a.ChangeAt) {
			continue
		}
		online++
		if a.Latency > 2*cfg.PushDelay {
			t.Fatalf("%s 在线时的推送延迟 %s 超过 2 倍单程延迟 %s", a.TaskID, dur(a.Latency), dur(2*cfg.PushDelay))
		}
	}
	if online == 0 {
		t.Fatal("样本应当有落在在线探针上的变更")
	}
	if push.totalRequests() >= pull.totalRequests() {
		t.Fatalf("推送请求数 %d 应当少于拉取 %d", push.totalRequests(), pull.totalRequests())
	}
	// 尾巴不由分发模式决定：两条路的 max 都是「离线时长」，样本里那条变更被塞进
	// 离线窗口，所以两者的最大值都超过一分钟。推送的短处不在这里，在连接与内存。
	if pull.Stats.Max <= time.Minute || push.Stats.Max <= time.Minute {
		t.Fatalf("离线窗口里的那条变更应当把两条路的尾延迟都拉到一分钟以上：拉取 %s，推送 %s",
			dur(pull.Stats.Max), dur(push.Stats.Max))
	}
}

func TestOfflineProbeTailAndStale(t *testing.T) {
	cfg := testConfig()
	cfg.MaxReportDelay = time.Minute
	probes := []Probe{oneProbe("p-1", Window{From: 3 * time.Minute, To: 5 * time.Minute})}
	changes := []Change{singleChange("p-1", 3*time.Minute+30*time.Second)}

	pull := SimulatePull(cfg, probes, changes)
	if pull.Stats.N != 1 {
		t.Fatalf("应当统计到 1 条变更，实际 %d", pull.Stats.N)
	}
	got := pull.Apply[0].Latency
	// 探针 5m0s 回来，但它按自己的相位轮询，下一轮才看见这条变更。
	want := 5*time.Minute + cfg.Poll - (3*time.Minute + 30*time.Second)
	if got != want {
		t.Fatalf("离线探针的拉取延迟应当是 %s（等它回来再下一轮），实际 %s", dur(want), dur(got))
	}
	if pull.Stale != 1 {
		t.Fatalf("延迟 %s 超过补报窗口 %s，应当记一条 Stale，实际 %d", dur(got), dur(cfg.MaxReportDelay), pull.Stale)
	}

	push := SimulatePush(cfg, probes, changes)
	gotPush := push.Apply[0].Latency
	// 掉线后要等它回来 + 重连退避 + 单程延迟，落在 [90s+退避, 90s+退避+单程] 区间。
	low := 90*time.Second + cfg.ReconnectBackoff
	if gotPush < low || gotPush > low+2*cfg.PushDelay {
		t.Fatalf("推送尾延迟应当在 [%s, %s]，实际 %s", dur(low), dur(low+cfg.PushDelay), dur(gotPush))
	}
	if push.HandshakeRequests < 2 {
		t.Fatalf("有一次离线就应当数到重连握手，实际 %d 次", push.HandshakeRequests)
	}
}

func TestRequestCountsCompare(t *testing.T) {
	cfg := testConfig()
	probes := make([]Probe, 8)
	for i := range probes {
		probes[i] = oneProbe(string(rune('a' + i)))
	}
	var changes []Change
	for i := 0; i < 8; i++ {
		changes = append(changes, singleChange(probes[i].ProbeID, time.Duration(i+1)*time.Minute))
	}
	pull := SimulatePull(cfg, probes, changes)
	push := SimulatePush(cfg, probes, changes)

	// 拉取每台每 Poll 一次：10 分钟 / 5 秒 = 120 轮，多算一轮边界。
	wantPull := int64(len(probes)) * int64(cfg.Duration/cfg.Poll)
	if pull.PollRequests < wantPull || pull.PollRequests > wantPull+int64(len(probes)) {
		t.Fatalf("拉取轮询数应当约等于 %d，实际 %d", wantPull, pull.PollRequests)
	}
	wantHeartbeat := int64(len(probes)) * int64(cfg.Duration/cfg.Heartbeat)
	if push.HeartbeatRequests != wantHeartbeat {
		t.Fatalf("心跳数应当是 %d，实际 %d", wantHeartbeat, push.HeartbeatRequests)
	}
	if push.DispatchMessages != int64(len(changes)) {
		t.Fatalf("下发消息数应当等于变更数 %d，实际 %d", len(changes), push.DispatchMessages)
	}
}

func TestPollPhasesStaggered(t *testing.T) {
	cfg := testConfig()
	seen := map[time.Duration]bool{}
	for i := 0; i < 10; i++ {
		ph := pollPhase(i, cfg.Poll)
		if ph >= cfg.Poll || ph < 0 {
			t.Fatalf("第 %d 台的相位 %s 落在 [0, %s) 之外", i, ph, cfg.Poll)
		}
		if seen[ph] {
			t.Fatalf("第 %d 台的相位 %s 与前面的重复，十万探针会撞在同一刻", i, ph)
		}
		seen[ph] = true
	}
}

func TestDecommissionedProbeNeverApplies(t *testing.T) {
	cfg := testConfig()
	p := oneProbe("p-1")
	p.DecommissionedAt = 5 * time.Minute
	probes := []Probe{p}
	changes := []Change{singleChange("p-1", 6*time.Minute)}

	pull := SimulatePull(cfg, probes, changes)
	if pull.Undeployed != 1 || pull.Stats.N != 0 {
		t.Fatalf("已退役探针上的变更应当下发不到，实际 Undeployed=%d N=%d", pull.Undeployed, pull.Stats.N)
	}
	push := SimulatePush(cfg, probes, changes)
	if push.Undeployed != 1 {
		t.Fatalf("推送模式也应当记一条下发不到，实际 %d", push.Undeployed)
	}
}

// TestTailedChangeShowsUpAsGap 验证样本里刻意安排的那条「落进离线窗口」的变更：
// 默认 15 分钟补报上限下它只是迟到，把上限压到 1 分钟就变成缺口。
func TestTailedChangeShowsUpAsGap(t *testing.T) {
	cfg, sc := buildDefault(t)
	if sc.Truth.TailChanges == 0 {
		t.Fatal("样本里应当把离线探针名下的一条变更塞进离线窗口，否则尾延迟没素材")
	}

	byID := map[string]Probe{}
	for _, p := range sc.Probes {
		byID[p.ProbeID] = p
	}
	// 每条被塞进窗口的变更，目标探针在那一刻确实是离线的。
	for _, p := range sc.Probes {
		if len(p.Offline) == 0 {
			continue
		}
		for _, ch := range sc.Changes {
			if ch.ToProbe != p.ProbeID {
				continue
			}
			w := p.Offline[0]
			if ch.At < w.From || ch.At >= w.To {
				continue
			}
			if p.online(ch.At) {
				t.Fatalf("%s 的变更 %s 落在离线窗口里，探针却是在线状态", ch.TaskID, dur(ch.At))
			}
		}
	}

	loose := SimulatePull(cfg, sc.Probes, sc.Changes)
	if loose.Stale != 0 {
		t.Fatalf("补报上限 15 分钟时不该有变更超窗，实际 %d 条", loose.Stale)
	}
	cfg.MaxReportDelay = time.Minute
	tight := SimulatePull(cfg, sc.Probes, sc.Changes)
	if tight.Stale < sc.Truth.TailChanges {
		t.Fatalf("补报上限压到 1 分钟后应当有 %d 条超窗，实际 %d 条", sc.Truth.TailChanges, tight.Stale)
	}
}
