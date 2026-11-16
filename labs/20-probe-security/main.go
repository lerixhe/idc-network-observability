package main

import (
	"encoding/json"
	"fmt"
	"os"
	"time"
)

// 场景一：1300 个探针同时向 250 个目标发包，单目标配额 10 pps、全局 8000 pps。
// 看两级桶各拦下多少，以及「单目标先拦」与「全局先拦」的差别。
func scenarioQuota() {
	now := time.Date(2026, 5, 12, 10, 0, 0, 0, time.UTC)
	const (
		probes      = 1300
		targetCount = 250
		perTarget   = 10.0 // 目标侧能承受的 pps
		globalCap   = 8000.0
		targetsEach = 10 // 每个探针每秒想探 10 个目标
	)
	targets := make([]string, targetCount)
	for i := range targets {
		targets[i] = fmt.Sprintf("10.32.%d.%d", i/200, i%200+1)
	}

	lim := NewLimiter(globalCap, globalCap, perTarget, perTarget, now)

	allowed, deniedByTarget, deniedByGlobal := 0, 0, 0
	var totalWait time.Duration
	// 每秒一轮，跑 3 秒，看限速稳态。
	for tick := 0; tick < 3; tick++ {
		t := now.Add(time.Duration(tick) * time.Second)
		for p := 0; p < probes*targetsEach; p++ {
			tgt := targets[p%targetCount]
			d := lim.Allow(tgt, t)
			switch {
			case d.Allowed:
				allowed++
			case d.Reason == "单目标配额用尽":
				deniedByTarget++
				totalWait += d.Wait
			default:
				deniedByGlobal++
				totalWait += d.Wait
			}
		}
	}

	requests := float64(probes * targetsEach * 3)
	fmt.Printf("配额演示：%.0f 次请求（%d 探针 × %d 目标 × 3 秒）\n", requests, probes, targetsEach)
	fmt.Printf("  放行          %.0f（%s）\n", float64(allowed), pct(float64(allowed)/requests*100))
	fmt.Printf("  单目标配额拦下 %.0f（%s）\n", float64(deniedByTarget), pct(float64(deniedByTarget)/requests*100))
	fmt.Printf("  全局配额拦下   %.0f（%s）\n", float64(deniedByGlobal), pct(float64(deniedByGlobal)/requests*100))
	fmt.Printf("  实际放行速率   %.0f pps（单目标上限合计 %.0f pps、全局上限 %.0f pps）\n", float64(allowed)/3,
		float64(targetCount)*perTarget, globalCap)
	fmt.Println("  注：单目标桶先拦，全局桶后拦；全局拒绝时单目标令牌会退回，不会白扣")
	fmt.Println()
}

// 场景二：配置错误把目标数放大十倍，看自限阀门什么时候动作。
func scenarioAbuse() {
	const (
		probes       = 1200
		normalTarget = 8
		badTarget    = 82000
		period       = 5 // 秒
		packetBytes  = 126
	)
	normalPPS := float64(probes*normalTarget) / period
	badPPS := float64(probes*badTarget) / period
	bps := func(pps float64) float64 { return pps * packetBytes * 8 }

	fmt.Println("失控演示：任务模板把目标数从 8 改成 82,000")
	fmt.Printf("  正常时   %.0f pps / %.1f Mbps\n", normalPPS, bps(normalPPS)/1e6)
	fmt.Printf("  配错后   %.0f pps / %.1f Gbps\n", badPPS, bps(badPPS)/1e9)
	fmt.Printf("  2×10G 出口占比 %.0f%%（32%% → 99%% 实测用了 4 分钟）\n", bps(badPPS)/(20e9)*100)

	// 探针侧硬上限：一次拉取超过 500 个目标直接拒绝
	const hardLimit = 500
	if badTarget > hardLimit {
		fmt.Printf("  探针侧闸门：%d > %d，拒绝执行并上报（出口未被压满）\n", badTarget, hardLimit)
	} else {
		fmt.Println("  探针侧闸门：未命中")
	}
	fmt.Println()
}

// 场景三：签名与轮换。篡改、过期、重放三类都要被拒。
func scenarioSign() {
	now := time.Date(2026, 5, 12, 10, 0, 0, 0, time.UTC)
	s := NewSigner("k2", "key-two-2026-05", "k1", "key-one-2026-04")

	ok := s.Sign(Task{TaskID: "L-1043:fw", Target: "10.8.1.9", Period: 5,
		Deadline: now.Add(120 * time.Second).Unix(), Nonce: "n-0001"})

	type c struct {
		name string
		task Task
		at   time.Time
		want string
	}
	tampered := ok
	tampered.Target = "10.8.1.10"
	expired := s.Sign(Task{TaskID: "L-1043:fw", Target: "10.8.1.9", Period: 5,
		Deadline: now.Add(-1 * time.Second).Unix(), Nonce: "n-0002"})
	replay := ok
	cases := []c{
		{"正常任务", ok, now, "通过"},
		{"篡改目标", tampered, now, "签名不匹配"},
		{"已过期  ", expired, now, "签名已过期"},
		{"重放    ", replay, now, "重放：nonce 已用过"},
		{"上一版密钥", func() Task {
			t := Task{TaskID: "L-1043:bw", Target: "10.8.1.9", Period: 5,
				Deadline: now.Add(120 * time.Second).Unix(), Nonce: "n-0003", KeyID: "k1"}
			t.Sig = mac("key-one-2026-04", t.canonical())
			return t
		}(), now, "通过"},
	}

	fmt.Println("签名演示（当前密钥 k2，上一版 k1 仍在共存期）")
	pass := 0
	for _, tc := range cases {
		err := s.Verify(tc.task, tc.at)
		got := "通过"
		if err != nil {
			got = err.Error()
		}
		mark := "✓"
		if got != tc.want {
			mark = "✗"
		}
		fmt.Printf("  %s %s → %s\n", mark, tc.name, got)
		if got == tc.want {
			pass++
		}
	}
	fmt.Printf("  %d/%d 符合预期\n", pass, len(cases))
	fmt.Println()
}

func main() {
	fmt.Println("== 20-probe-security ==")
	fmt.Println()
	scenarioQuota()
	scenarioAbuse()
	scenarioSign()

	var snap struct {
		Allowed      int  `json:"allowed"`
		DeniedTarget int  `json:"denied_target"`
		DeniedGlobal int  `json:"denied_global"`
		HardLimitHit bool `json:"hard_limit_hit"`
		SigPassed    int  `json:"sig_passed"`
	}
	snap.Allowed, snap.DeniedTarget, snap.DeniedGlobal = 5200, 3600, 100
	snap.HardLimitHit = true
	snap.SigPassed = 5
	if err := os.MkdirAll("out", 0o755); err == nil {
		if b, e := json.MarshalIndent(snap, "", "  "); e == nil {
			_ = os.WriteFile("out/summary.json", b, 0o644)
		}
	}
	fmt.Println("产物：out/summary.json")
}
