package main

import (
	"errors"
	"testing"
	"time"
)

func baseTime() time.Time { return time.Date(2026, 5, 12, 10, 0, 0, 0, time.UTC) }

func newTask(now time.Time, nonce string) Task {
	return Task{TaskID: "L-1043:fw", Target: "10.8.1.9", Period: 5,
		Deadline: now.Add(120 * time.Second).Unix(), Nonce: nonce}
}

func TestSignVerifyRoundTrip(t *testing.T) {
	now := baseTime()
	s := NewSigner("k2", "two", "k1", "one")
	tk := s.Sign(newTask(now, "n-1"))
	if err := s.Verify(tk, now); err != nil {
		t.Fatalf("签名应通过，却报 %v", err)
	}
}

func TestTamperedRejected(t *testing.T) {
	now := baseTime()
	s := NewSigner("k2", "two", "k1", "one")
	tk := s.Sign(newTask(now, "n-2"))
	tk.Target = "10.8.1.10"
	if err := s.Verify(tk, now); !errors.Is(err, ErrSigInvalid) {
		t.Fatalf("篡改目标应报签名不匹配，得到 %v", err)
	}
}

func TestExpiredRejected(t *testing.T) {
	now := baseTime()
	s := NewSigner("k2", "two", "k1", "one")
	tk := newTask(now, "n-3")
	tk.Deadline = now.Add(-time.Second).Unix()
	tk = s.Sign(tk)
	if err := s.Verify(tk, now); !errors.Is(err, ErrSigExpired) {
		t.Fatalf("过期应被拒，得到 %v", err)
	}
}

func TestReplayRejected(t *testing.T) {
	now := baseTime()
	s := NewSigner("k2", "two", "k1", "one")
	tk := s.Sign(newTask(now, "n-4"))
	if err := s.Verify(tk, now); err != nil {
		t.Fatalf("首次应通过：%v", err)
	}
	if err := s.Verify(tk, now); !errors.Is(err, ErrSigReplay) {
		t.Fatalf("重放应被拒，得到 %v", err)
	}
}

func TestRotationBothKeysWork(t *testing.T) {
	now := baseTime()
	s := NewSigner("k2", "two", "k1", "one")

	old := newTask(now, "n-5")
	old.KeyID = "k1"
	old.Sig = mac("one", old.canonical())
	if err := s.Verify(old, now); err != nil {
		t.Fatalf("共存期内旧密钥应验得过，得到 %v", err)
	}

	unknown := newTask(now, "n-6")
	unknown.KeyID = "k0"
	unknown.Sig = mac("zero", unknown.canonical())
	if err := s.Verify(unknown, now); !errors.Is(err, ErrNoKey) {
		t.Fatalf("未知密钥应被拒，得到 %v", err)
	}
}

func TestBucketRefills(t *testing.T) {
	now := baseTime()
	b := NewBucket(10, 10, now) // 容量 10，每秒补 10
	for i := 0; i < 10; i++ {
		if ok, _ := b.Take(1, now); !ok {
			t.Fatalf("第 %d 个令牌应放行", i+1)
		}
	}
	if ok, wait := b.Take(1, now); ok || wait <= 0 {
		t.Fatalf("桶空后应拒绝并给出等待时间，得到 ok=%v wait=%v", ok, wait)
	}
	later := now.Add(500 * time.Millisecond)
	if ok, _ := b.Take(5, later); !ok {
		t.Fatal("半秒后应补出 5 个令牌")
	}
}

func TestPerTargetBlocksFirst(t *testing.T) {
	now := baseTime()
	// 全局很宽，单目标很窄：拒绝应归因到单目标。
	l := NewLimiter(1000, 1000, 2, 2, now)
	for i := 0; i < 2; i++ {
		if d := l.Allow("A", now); !d.Allowed {
			t.Fatalf("前两个应放行，第 %d 个被拒：%s", i+1, d.Reason)
		}
	}
	d := l.Allow("A", now)
	if d.Allowed || d.Reason != "单目标配额用尽" {
		t.Fatalf("第三个应被单目标拦住，得到 %+v", d)
	}
	// 另一个目标不受影响
	if d := l.Allow("B", now); !d.Allowed {
		t.Fatal("另一个目标应放行")
	}
}

func TestGlobalBlocksAndRefundsPerTarget(t *testing.T) {
	now := baseTime()
	// 单目标宽（10），全局窄（3）
	l := NewLimiter(3, 3, 10, 10, now)
	if d := l.Allow("A", now); !d.Allowed {
		t.Fatal("第一个应放行")
	}
	if d := l.Allow("B", now); !d.Allowed {
		t.Fatal("第二个应放行")
	}
	if d := l.Allow("C", now); !d.Allowed {
		t.Fatal("第三个应放行")
	}
	d := l.Allow("A", now)
	if d.Allowed || d.Reason != "全局配额用尽" {
		t.Fatalf("第四个应被全局拦住，得到 %+v", d)
	}
	// 被全局拒绝时，单目标令牌要退回：A 的桶先扣 1 再退回 1，
	// 所以停在第一次成功后的 9，而不是被两次拒绝继续消耗到 8。
	if got := l.targets["A"].tokens; got != 9 {
		t.Fatalf("全局拒绝后单目标桶应退回一次扣减，剩 %v", got)
	}
}
