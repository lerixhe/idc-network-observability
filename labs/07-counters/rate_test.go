package main

import (
	"math"
	"strings"
	"testing"
	"time"
)

// mustSeries 从样本文件里取一条序列，测试里到处要用。
func mustSeries(t *testing.T, key string) Series {
	t.Helper()
	series, err := loadSeries("samples/counters.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range series {
		if s.Key() == key {
			return s
		}
	}
	t.Fatalf("样本里没有 %s", key)
	return Series{}
}

func mustRates(t *testing.T, s Series) SeriesRates {
	t.Helper()
	r, err := ComputeRates(s, 0)
	if err != nil {
		t.Fatalf("%s 算速率失败：%v", s.Key(), err)
	}
	return r
}

// TestWrapIsWhereV2AndV3Diverge 是三算法对比的核心断言：
// 同一个区间上 v1 与 v2 给出负数，v3 补一个量程后回到满速，分岔点必须落在回绕那一格。
func TestWrapIsWhereV2AndV3Diverge(t *testing.T) {
	cases := []struct {
		key      string
		wrapAt   int     // 第几个区间发生回绕
		wantBps  float64 // 回绕段补正后的速率，也就是端口线速
		wantNorm float64 // v2 在同一格给出的负数：差值 -3044967296 字节，乘 8 除间隔
	}{
		// 1G 口满速：10 秒涨 1.25e9，18.36 秒撞到 2^32；-3044967296×8/10 = -2.436 Gbps
		{"tor-a3f-01/Ethernet1/49", 1, 1e9, -2.4359738368e9},
		// 10G 口满速：1 秒涨 1.25e9，3.44 秒一圈；-3044967296×8/1 = -24.36 Gbps
		{"tor-a3f-02/Ethernet1/1", 2, 10e9, -2.4359738368e10},
	}
	for _, c := range cases {
		r := mustRates(t, mustSeries(t, c.key))
		if r.Wraps != 1 {
			t.Fatalf("%s：检出回绕 %d 圈，期望 1", c.key, r.Wraps)
		}
		p := r.Points[c.wrapAt]
		if p.Wraps != 1 {
			t.Fatalf("%s：第 %d 个区间没标出回绕", c.key, c.wrapAt)
		}
		if p.RawDelta >= 0 {
			t.Fatalf("%s：回绕区间的原始差值是 %d，应当是负数", c.key, p.RawDelta)
		}
		if math.Abs(p.Normalized-c.wantNorm) > math.Abs(c.wantNorm)*1e-6 {
			t.Errorf("%s：v2 在回绕点给出 %.0f bps，期望 %.0f（未补量程，只能是负数）", c.key, p.Normalized, c.wantNorm)
		}
		if math.Abs(p.Naive-p.Normalized) > 1 {
			t.Errorf("%s：v1 = %.0f 与 v2 = %.0f 在回绕点应当一致（都是错的）", c.key, p.Naive, p.Normalized)
		}
		if math.Abs(p.WrapInterp-c.wantBps) > c.wantBps*1e-6 {
			t.Errorf("%s：v3 在回绕点给出 %.0f bps，期望 %.0f", c.key, p.WrapInterp, c.wantBps)
		}
		if !p.Trusted {
			t.Errorf("%s：回绕段的间隔远小于半圈，应当可信，理由却是 %q", c.key, p.Reason)
		}

		// 其余区间三列必须一模一样，否则分岔点就不止一处了。
		for i, q := range r.Points {
			if i == c.wrapAt {
				continue
			}
			if math.Abs(q.Naive-q.Normalized) > 1 || math.Abs(q.Normalized-q.WrapInterp) > 1 {
				t.Errorf("%s：第 %d 个区间不该分岔：v1=%.0f v2=%.0f v3=%.0f", c.key, i, q.Naive, q.Normalized, q.WrapInterp)
			}
		}
	}
}

// TestHalfWrapPeriodIsAWarningNotASilentNumber 覆盖「采集周期 ≥ 回绕周期一半」这条红线：
// 10 秒采一次的 10G 口，一个周期里走完 2.91 圈，任何算法都算不出真相，
// 这时必须出自端口级告警 + 区间级不可信，不许把 3.13 Gbps 当结果交出去。
func TestHalfWrapPeriodIsAWarningNotASilentNumber(t *testing.T) {
	s := mustSeries(t, "tor-a3f-02/Ethernet1/2") // 10G 口，采集周期被错配成 10s
	r := mustRates(t, s)

	if !r.ConfigRisk {
		t.Fatal("采集周期 10s ≥ 回绕周期 3.44s 的一半 1.72s，这个端口应当被标成配置踩红线")
	}
	if !r.WrapKnown || r.WrapPeriod < 3*time.Second || r.WrapPeriod > 4*time.Second {
		t.Fatalf("回绕周期算成 %s，期望 3.44s 左右", r.WrapPeriod)
	}
	if len(r.Warnings) < 2 {
		t.Fatalf("应当有配置红线与不可判两条告警，实际 %d 条：%v", len(r.Warnings), r.Warnings)
	}
	var hasRisk, hasUnjudgeable bool
	for _, w := range r.Warnings {
		if strings.Contains(w, "一半 1.72s") {
			hasRisk = true
		}
		if strings.Contains(w, "量程一半") {
			hasUnjudgeable = true
		}
	}
	if !hasRisk {
		t.Errorf("告警里没有写清「一半 1.72s」这条红线：%v", r.Warnings)
	}
	if !hasUnjudgeable {
		t.Errorf("告警里没有写清「观测增量超过量程一半」：%v", r.Warnings)
	}

	if r.Untrusted != len(r.Points) {
		t.Fatalf("不可信 %d 段 / 共 %d 段，这份样本应当全段不可信", r.Untrusted, len(r.Points))
	}
	for i, p := range r.Points {
		if !p.Ambiguous {
			t.Errorf("第 %d 个区间没标 ambiguous", i)
		}
		if p.Trusted {
			t.Errorf("第 %d 个区间标了可信，圈数不可判的时候不许出数", i)
		}
		if !strings.Contains(p.Reason, "圈数不可判") {
			t.Errorf("第 %d 个区间的理由没说清不可判的原因：%q", i, p.Reason)
		}
		// 真相是 10 Gbps，v3 补一圈只能补到 3.13 Gbps，差 3.2 倍——这就是静默算错的形状。
		if p.WrapInterp <= 0 || p.WrapInterp >= 10e9 {
			t.Errorf("第 %d 个区间的 v3 = %.0f bps，这份样本上它必然低于线速", i, p.WrapInterp)
		}
	}
}

// TestConfigRiskWithoutWrongNumbers 说明端口级告警与区间级可信度是两件事：
// 同一个 10 秒采一次的 10G 口，只跑 1.6 Gbps 时数字是对的，告警照样要发。
func TestConfigRiskWithoutWrongNumbers(t *testing.T) {
	r := mustRates(t, mustSeries(t, "spine-01/Ethernet1/1"))
	if !r.ConfigRisk {
		t.Fatal("10G 口用 10s 采集周期，配置红线必须报")
	}
	if r.Untrusted != 1 {
		t.Fatalf("只有重启那一段该不可信，实际 %d 段", r.Untrusted)
	}
	for i, p := range r.Points {
		if p.Reset {
			continue
		}
		if !p.Trusted {
			t.Errorf("第 %d 个区间（增量 %d 字节）没到半量程，应当可信", i, p.RawDelta)
		}
		if math.Abs(p.WrapInterp-1.6e9) > 1e6 {
			t.Errorf("第 %d 个区间速率 %.0f bps，期望 1.6 Gbps", i, p.WrapInterp)
		}
	}
}

// TestWrapVersusReset 是「差值为负」的两种解释：回绕与重启，靠 sysUpTime 分开。
func TestWrapVersusReset(t *testing.T) {
	// 重启：sysUpTime 从 2,002,000 tick 掉到 300 tick，计数器同时掉到 6 亿。
	reboot := mustRates(t, mustSeries(t, "spine-01/Ethernet1/1"))
	p := reboot.Points[2]
	if !p.Reset {
		t.Fatalf("这一段应当是重启，实际 reason = %q", p.Reason)
	}
	if p.UptimeWrap {
		t.Error("重启被判成了 uptime 回绕")
	}
	if p.Trusted || p.WrapInterp != 0 {
		t.Errorf("重启段的速率应当是 0 且不可信，实际 %.0f bps trusted=%v", p.WrapInterp, p.Trusted)
	}
	if !strings.Contains(p.Reason, "不可信") {
		t.Errorf("重启段的理由里必须写明不可信：%q", p.Reason)
	}
	if reboot.Resets != 1 || reboot.Wraps != 0 {
		t.Errorf("重启 %d 次 / 回绕 %d 圈，期望 1 / 0", reboot.Resets, reboot.Wraps)
	}

	// uptime 回绕：设备连开 497 天，sysUpTime 自己归零，字节计数一直在涨。
	uptimeWrap := mustRates(t, mustSeries(t, "spine-03/Ethernet1/3"))
	q := uptimeWrap.Points[1]
	if !q.UptimeWrap {
		t.Fatalf("这一段应当是 sysUpTime 回绕，实际 reason = %q", q.Reason)
	}
	if q.Reset {
		t.Error("uptime 回绕被判成了设备重启")
	}
	if !q.Trusted {
		t.Error("uptime 回绕不影响计数器，速率应当可信")
	}
	if math.Abs(q.WrapInterp-8e8) > 1e6 {
		t.Errorf("速率 %.0f bps，期望 800 Mbps", q.WrapInterp)
	}
	if uptimeWrap.Resets != 0 || uptimeWrap.UptimeWraps != 1 {
		t.Errorf("重启 %d 次 / uptime 回绕 %d 次，期望 0 / 1", uptimeWrap.Resets, uptimeWrap.UptimeWraps)
	}
}

// TestGapBreaksNaiveNotNormalized 是 v1 与 v2 的分岔点：漏采三轮，v1 把增量摊在标称周期上，放大四倍。
func TestGapBreaksNaiveNotNormalized(t *testing.T) {
	r := mustRates(t, mustSeries(t, "leaf-0207/Ethernet1/7"))
	if r.Gaps != 1 {
		t.Fatalf("丢样段数 = %d，期望 1", r.Gaps)
	}
	p := r.Points[2]
	if p.Gap != 3 {
		t.Errorf("漏采轮数 = %d，期望 3", p.Gap)
	}
	if p.Split != 4 {
		t.Errorf("v3 应当把这个区间拆成 4 段插值，实际 %d 段", p.Split)
	}
	if math.Abs(p.Naive-8e8) > 1e6 {
		t.Errorf("v1 在丢样段给出 %.0f bps，期望 800 Mbps（0.2 Gbps 的四倍）", p.Naive)
	}
	if math.Abs(p.Normalized-2e8) > 1e6 {
		t.Errorf("v2 在丢样段给出 %.0f bps，期望 200 Mbps", p.Normalized)
	}
	if math.Abs(p.WrapInterp-p.Normalized) > 1 {
		t.Errorf("v3 与 v2 在丢样段应当一致（都不补量程）：%.0f vs %.0f", p.WrapInterp, p.Normalized)
	}
	if !p.Trusted {
		t.Errorf("这一段间隔 40s、增量 1e9，没到半量程，应当可信：%q", p.Reason)
	}
}

// TestReorderDoesNotLookLikeWrap 覆盖乱序：按文件顺序算差值会得到负数，看起来像回绕。
func TestReorderDoesNotLookLikeWrap(t *testing.T) {
	r := mustRates(t, mustSeries(t, "leaf-0207/Ethernet1/8"))
	if r.Reorders != 2 {
		t.Fatalf("乱序位置数 = %d，期望 2", r.Reorders)
	}
	if r.Wraps != 0 || r.Resets != 0 {
		t.Fatalf("排序后不该出现回绕或重启，实际回绕 %d / 重启 %d", r.Wraps, r.Resets)
	}
	for i, p := range r.Points {
		if p.RawDelta < 0 {
			t.Errorf("第 %d 个区间差值是负数，说明样本没有按时间戳归位", i)
		}
		if math.Abs(p.WrapInterp-5e8) > 1e6 {
			t.Errorf("第 %d 个区间速率 %.0f bps，期望 500 Mbps", i, p.WrapInterp)
		}
		if !p.Trusted {
			t.Errorf("第 %d 个区间被标成不可信", i)
		}
	}
	if !r.Points[3].Reordered {
		t.Error("30s→40s 这个区间跨过了乱序样本，应当带上乱序标记")
	}
}

// TestHC64NeverWraps 用 64 位计数器做对照：三列结果完全一致，回绕这件事不存在。
func TestHC64NeverWraps(t *testing.T) {
	r := mustRates(t, mustSeries(t, "spine-02/Ethernet1/2"))
	if r.WrapKnown {
		t.Fatalf("64 位字节计数器的回绕周期应当判为没有工程意义，实际 %s", r.WrapPeriod)
	}
	if r.ConfigRisk || len(r.Warnings) > 0 {
		t.Errorf("64 位计数器不该有回绕告警：%v", r.Warnings)
	}
	for i, p := range r.Points {
		if math.Abs(p.Naive-p.Normalized) > 1 || math.Abs(p.Normalized-p.WrapInterp) > 1 {
			t.Errorf("第 %d 个区间三列不一致：%.0f / %.0f / %.0f", i, p.Naive, p.Normalized, p.WrapInterp)
		}
		if math.Abs(p.WrapInterp-6.4e9) > 1e7 {
			t.Errorf("第 %d 个区间速率 %.0f bps，期望 6.4 Gbps", i, p.WrapInterp)
		}
	}
}

// TestComputeRatesRejectsBadSeries 挡住非法样本，别让它算出一张看起来正常的表。
func TestComputeRatesRejectsBadSeries(t *testing.T) {
	s := Series{
		Device: "sw", IfName: "Ethernet1/1", CounterBits: 32, SpeedBps: 1e9,
		Samples: []Sample{{AtMs: 0, Value: 1}, {AtMs: 0, Value: 2}},
	}
	if _, err := ComputeRates(s, 0); err == nil {
		t.Fatal("同一时刻的两个样本应当报错")
	}
	s.Samples = []Sample{{AtMs: 0, Value: 1}, {AtMs: 0, Value: 2}}
	s.Samples[1].AtMs = 1000
	if _, err := ComputeRates(s, 0); err != nil {
		t.Fatalf("合法样本算速率失败：%v", err)
	}
}
