package main

import (
	"fmt"
	"math"
	"time"
)

// TierPolicy 是一个能力档位的采集参数。
//
// 档位不是设备的型号，是「这台设备的管理面能承受多少」：老 TOR 的 SNMP
// 是单线程的，一次 GetBulk 要 50 条就把 CPU 打到 30%，那不是设备的错，
// 是我们的请求形状错了。
type TierPolicy struct {
	Tier           string
	MaxRepetitions int           // 一次 GetBulk 要几行
	RequestsPerSec float64       // 一秒允许几个请求
	Burst          int           // 令牌桶容量：允许的瞬时突发
	Timeout        time.Duration // 单次请求超时
	Attempts       int           // 单次请求最多尝试几次（含首次）
	Note           string        // 这个档位凭什么这么定
}

// tierPolicies 是分档表。数字是策略，不是协议规定——换设备、换固件就得重定。
//
// 依据：A 档设备一次 GetBulk 64 条返回 6 KB 左右，管理面毫无压力；
// C 档是 2013 年前后的接入交换机，单线程处理 SNMP，实测每秒 2 个请求是
// 安全线，超了就开始丢 trap、CPU 报警。
var tierPolicies = map[string]TierPolicy{
	"A": {Tier: "A", MaxRepetitions: 64, RequestsPerSec: 20, Burst: 8,
		Timeout: 1000 * time.Millisecond, Attempts: 2,
		Note: "现代交换机，管理面多核；64 条/请求、20 请求/秒"},
	"B": {Tier: "B", MaxRepetitions: 16, RequestsPerSec: 8, Burst: 4,
		Timeout: 1200 * time.Millisecond, Attempts: 2,
		Note: "上一代接入/汇聚，单核管理面；16 条/请求、8 请求/秒"},
	"C": {Tier: "C", MaxRepetitions: 8, RequestsPerSec: 2, Burst: 2,
		Timeout: 2000 * time.Millisecond, Attempts: 3,
		Note: "老 TOR，单线程 SNMP；8 条/请求、2 请求/秒，超了就开始丢 trap"},
}

// PolicyFor 取档位参数。档位写错要当场报错，不能静默降级成 A 档——
// 那样一台老设备会被当新设备打，一晚就能把管理面打死。
func PolicyFor(tier string) (TierPolicy, error) {
	p, ok := tierPolicies[tier]
	if !ok {
		return TierPolicy{}, fmt.Errorf("未知档位 %q（只有 A / B / C）", tier)
	}
	if p.MaxRepetitions <= 0 || p.RequestsPerSec <= 0 || p.Burst < 1 || p.Timeout <= 0 || p.Attempts < 1 {
		return TierPolicy{}, fmt.Errorf("档位 %s 的参数非法", tier)
	}
	return p, nil
}

// RateLimiter 是令牌桶。做法是「预约」：每次请求先问「我最早什么时候能发」，
// 拿到的时刻晚于现在就地排队等，不丢请求。
//
// 丢请求看起来更省事，代价是采集结果出现空洞，而且空洞事后补不回来：
// 端口在那里，数据没了。排队至少保证「慢，但全会到」。
type RateLimiter struct {
	rate   float64   // 每秒补充的令牌
	burst  float64   // 桶容量
	tokens float64   // 当前令牌
	last   time.Time // 上次结算时刻
}

// NewRateLimiter 造一个满桶的限流器。
func NewRateLimiter(p TierPolicy) *RateLimiter {
	return &RateLimiter{rate: p.RequestsPerSec, burst: float64(p.Burst), tokens: float64(p.Burst)}
}

// Reserve 消耗一个令牌，返回这次请求最早可以发出的时刻。
//
// 桶里还有令牌就立刻放行；没有就把结算时刻推到未来——后面的请求排在
// 这一条后面，天然先来先服务。排队而不是丢弃：丢的是一个端口的一格数据，
// 等到的是时间。
func (r *RateLimiter) Reserve(now time.Time) time.Time {
	if r.last.IsZero() {
		r.last = now
	}
	if elapsed := now.Sub(r.last); elapsed > 0 {
		r.tokens = math.Min(r.burst, r.tokens+elapsed.Seconds()*r.rate)
	}
	if now.After(r.last) {
		r.last = now
	}
	if r.tokens >= 1 {
		r.tokens--
		return now
	}
	ready := r.last.Add(time.Duration((1 - r.tokens) / r.rate * float64(time.Second)))
	r.last = ready
	r.tokens = 0
	return ready
}

// Tokens 是调试用：想知道「现在桶里还剩几个令牌」，看它。
func (r *RateLimiter) Tokens() float64 { return r.tokens }
