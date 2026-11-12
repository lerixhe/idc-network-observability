package main

import (
	"testing"
	"time"
)

func TestPolicyForTiers(t *testing.T) {
	cases := []struct {
		tier     string
		reps     int
		rps      float64
		timeout  time.Duration
		attempts int
	}{
		{"A", 64, 20, time.Second, 2},
		{"B", 16, 8, 1200 * time.Millisecond, 2},
		{"C", 8, 2, 2 * time.Second, 3},
	}
	for _, c := range cases {
		p, err := PolicyFor(c.tier)
		if err != nil {
			t.Fatalf("档位 %s 取不到: %v", c.tier, err)
		}
		if p.MaxRepetitions != c.reps || p.RequestsPerSec != c.rps || p.Timeout != c.timeout || p.Attempts != c.attempts {
			t.Fatalf("档位 %s 参数 = %+v，与表里写的不一致", c.tier, p)
		}
		if p.Note == "" {
			t.Fatalf("档位 %s 要写清凭什么这么定", c.tier)
		}
	}
	// 档位写错必须报错：静默按 A 档打老设备，一晚就能把管理面打死。
	if _, err := PolicyFor("D"); err == nil {
		t.Fatal("未知档位应该报错")
	}
	if _, err := PolicyFor(""); err == nil {
		t.Fatal("空档位应该报错")
	}
}

// 令牌桶：满桶时连发 burst 个立刻放行，第 burst+1 个必须等 1/rate 秒。
func TestRateLimiterBurstThenPace(t *testing.T) {
	p, _ := PolicyFor("C") // 2 请求/秒，桶容量 2
	lim := NewRateLimiter(p)
	now := time.Date(2026, 5, 12, 10, 0, 0, 0, time.UTC)

	for i := 0; i < p.Burst; i++ {
		if ready := lim.Reserve(now); !ready.Equal(now) {
			t.Fatalf("桶里还有令牌时第 %d 个请求应立刻放行，实际要等到 %s", i, ready)
		}
	}
	ready := lim.Reserve(now)
	if want := now.Add(500 * time.Millisecond); !ready.Equal(want) {
		t.Fatalf("第 %d 个请求应排在 %s，实际 %s", p.Burst+1, want, ready)
	}
	next := lim.Reserve(now)
	if want := now.Add(time.Second); !next.Equal(want) {
		t.Fatalf("再下一个请求应排在 %s，实际 %s（排队而不是丢弃）", want, next)
	}
}

// 排队不丢：连发 10 个请求全部拿到放行时刻，最后那个等 4 秒。
func TestRateLimiterQueuesInsteadOfDropping(t *testing.T) {
	p, _ := PolicyFor("C")
	lim := NewRateLimiter(p)
	now := time.Date(2026, 5, 12, 10, 0, 0, 0, time.UTC)
	var last time.Time
	for i := 0; i < 10; i++ {
		last = lim.Reserve(now)
	}
	// 10 个请求、桶 2、速率 2/s：桶清空后 8 个各等 0.5s。
	if want := now.Add(4 * time.Second); !last.Equal(want) {
		t.Fatalf("第 10 个请求应排在 %s，实际 %s", want, last)
	}
}

// 时间往前走够了，桶会补回来，不该无限累积过去欠的等待。
func TestRateLimiterRefills(t *testing.T) {
	p, _ := PolicyFor("A") // 20 请求/秒，桶 8
	lim := NewRateLimiter(p)
	now := time.Date(2026, 5, 12, 10, 0, 0, 0, time.UTC)
	for i := 0; i < 30; i++ {
		lim.Reserve(now)
	}
	later := now.Add(5 * time.Second) // 5 秒足够补满桶
	if ready := lim.Reserve(later); !ready.Equal(later) {
		t.Fatalf("5 秒后桶应已补满，请求应立刻放行，实际等到 %s", ready)
	}
	if got := lim.Tokens(); got < float64(p.Burst)-1.5 {
		t.Fatalf("补满后令牌应接近桶容量 %d，实际 %.2f", p.Burst, got)
	}
}
