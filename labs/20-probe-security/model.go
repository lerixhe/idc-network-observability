package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ErrSig 表示签名不匹配、过期或重放。
var (
	ErrSigExpired = errors.New("签名已过期")
	ErrSigInvalid = errors.New("签名不匹配")
	ErrSigReplay  = errors.New("重放：nonce 已用过")
	ErrNoKey      = errors.New("密钥版本未知")
)

// Task 是下发到探针的一条探测任务。签名覆盖它的全部业务字段。
type Task struct {
	TaskID   string
	Target   string
	Period   int // 秒
	Deadline int64
	Nonce    string
	KeyID    string
	Sig      string
}

// Signer 持有当前与上一版密钥。轮换期两把都验得过，新签发一律用 current。
type Signer struct {
	current   string
	previous  string
	curID     string
	prevID    string
	seenNonce map[string]int64
}

func NewSigner(curID, curKey, prevID, prevKey string) *Signer {
	return &Signer{current: curKey, previous: prevKey, curID: curID, prevID: prevID, seenNonce: map[string]int64{}}
}

// canonical 是签名原文。字段顺序固定、不含 Sig 本身。
func (t Task) canonical() string {
	parts := []string{
		"v1",
		"task=" + t.TaskID,
		"target=" + t.Target,
		"period=" + strconv.Itoa(t.Period),
		"deadline=" + strconv.FormatInt(t.Deadline, 10),
		"nonce=" + t.Nonce,
	}
	return strings.Join(parts, "&")
}

func mac(key, msg string) string {
	h := hmac.New(sha256.New, []byte(key))
	h.Write([]byte(msg))
	return hex.EncodeToString(h.Sum(nil))
}

// Sign 用当前密钥签发。
func (s *Signer) Sign(t Task) Task {
	t.KeyID = s.curID
	t.Sig = mac(s.current, t.canonical())
	return t
}

// Verify 验签。三类拒绝：未知密钥、过期、签名不符、重放。
func (s *Signer) Verify(t Task, now time.Time) error {
	var key string
	switch t.KeyID {
	case s.curID:
		key = s.current
	case s.prevID:
		if s.previous == "" {
			return ErrNoKey
		}
		key = s.previous
	default:
		return ErrNoKey
	}
	if t.Deadline < now.Unix() {
		return ErrSigExpired
	}
	want := mac(key, t.canonical())
	if !hmac.Equal([]byte(want), []byte(t.Sig)) {
		return ErrSigInvalid
	}
	// nonce 去重：记录用过的 nonce，清理窗口外的。
	if _, dup := s.seenNonce[t.Nonce]; dup {
		return ErrSigReplay
	}
	s.seenNonce[t.Nonce] = t.Deadline
	for n, dl := range s.seenNonce {
		if dl < now.Unix()-600 {
			delete(s.seenNonce, n)
		}
	}
	return nil
}

// Bucket 是令牌桶。capacity 个令牌，每 interval 补一个。
type Bucket struct {
	capacity float64
	tokens   float64
	rate     float64 // 令牌/秒
	last     time.Time
}

func NewBucket(capacity float64, perSecond float64, now time.Time) *Bucket {
	return &Bucket{capacity: capacity, tokens: capacity, rate: perSecond, last: now}
}

// Take 尝试取 n 个令牌。返回是否允许、以及需要等待多久。
func (b *Bucket) Take(n float64, now time.Time) (bool, time.Duration) {
	elapsed := now.Sub(b.last).Seconds()
	if elapsed > 0 {
		b.tokens += elapsed * b.rate
		if b.tokens > b.capacity {
			b.tokens = b.capacity
		}
		b.last = now
	}
	if b.tokens >= n {
		b.tokens -= n
		return true, 0
	}
	need := (n - b.tokens) / b.rate
	return false, time.Duration(need * float64(time.Second))
}

// Limiter 两级：单目标配额与全局配额。任一级不满就拒绝。
type Limiter struct {
	global  *Bucket
	perTgt  float64
	perSec  float64
	targets map[string]*Bucket
}

func NewLimiter(globalCap, globalRate, perTgtCap, perTgtRate float64, now time.Time) *Limiter {
	return &Limiter{
		global:  NewBucket(globalCap, globalRate, now),
		perTgt:  perTgtCap,
		perSec:  perTgtRate,
		targets: map[string]*Bucket{},
	}
}

type Decision struct {
	Allowed bool
	Reason  string
	Wait    time.Duration
}

// Allow 先查单目标，再查全局。单目标桶不放行时不动全局桶。
func (l *Limiter) Allow(target string, now time.Time) Decision {
	b, ok := l.targets[target]
	if !ok {
		b = NewBucket(l.perTgt, l.perSec, now)
		l.targets[target] = b
	}
	if ok, wait := b.Take(1, now); !ok {
		return Decision{Allowed: false, Reason: "单目标配额用尽", Wait: wait}
	}
	if ok, wait := l.global.Take(1, now); !ok {
		// 全局拒绝要把刚扣的单目标令牌还回去，否则单目标桶会被白白消耗。
		b.tokens++
		return Decision{Allowed: false, Reason: "全局配额用尽", Wait: wait}
	}
	return Decision{Allowed: true}
}

// sortedTargets 让输出稳定，测试才能断言。
func sortedTargets(m map[string]*Bucket) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func pct(x float64) string { return fmt.Sprintf("%.1f%%", x) }
