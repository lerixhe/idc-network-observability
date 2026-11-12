package main

import (
	"math/rand"
	"sort"
	"time"
)

// 应答错误码。真协议里 get 类请求的错误状态还有 noSuchName、genErr 等，
// 本实验只要两种：取太多被拒（tooBig）和根本没有回包（timeout）。
const (
	ErrTooBig  = "tooBig"
	ErrTimeout = "timeout"
)

// Varbind 是应答里的一条 OID=值。
// EndOfMibView 为 true 表示「后面没有了」，真协议里是 v2c 的 endOfMibView 异常值。
type Varbind struct {
	OID          OID   `json:"oid"`
	Value        Value `json:"value"`
	EndOfMibView bool  `json:"end_of_mib_view,omitempty"`
}

// BulkRequest 是一次 GetBulk。
//
// Start 是「从这个 OID 之后开始取」，不是「取这个 OID」——GetBulk 是
// GetNext 的批量版，语义上永远往前不回头。写错成 Get 语义，遍历会死循环。
type BulkRequest struct {
	Device         string `json:"device"`
	Start          OID    `json:"start"`
	MaxRepetitions int    `json:"max_repetitions"`
}

// Response 是一份应答。
type Response struct {
	Varbinds  []Varbind `json:"varbinds"`
	Error     string    `json:"error,omitempty"`
	ElapsedMs int64     `json:"elapsed_ms"`
}

// Agent 是一台设备的极简 SNMP agent：一张排好序的叶子 OID 表，加一份管理面性格。
//
// 它不解析 BER。真链路上这一层是 ASN.1 编码解码，与本实验要讲的
// 遍历、限流、主键三件事无关，见 README「为什么 wire 层是 JSON」。
type Agent struct {
	Device  string
	Profile AgentProfile
	Entries []Entry
	rng     *rand.Rand
}

// NewAgent 用样本里的一台设备搭一个 agent。rand 种子固定，同样的样本跑出同样的抖动。
func NewAgent(d *Device, seed int64) *Agent {
	return &Agent{
		Device:  d.Name,
		Profile: d.SNMP,
		Entries: d.Entries(),
		rng:     rand.New(rand.NewSource(seed)),
	}
}

// GetBulk 返回 OID 严格大于 start 的最多 reps 条。纯内存查询，不计时。
func (a *Agent) GetBulk(start OID, reps int) []Varbind {
	if reps <= 0 {
		return nil
	}
	idx := sort.Search(len(a.Entries), func(i int) bool {
		return a.Entries[i].OID.Compare(start) > 0
	})
	out := make([]Varbind, 0, reps)
	for i := idx; i < len(a.Entries) && len(out) < reps; i++ {
		out = append(out, Varbind{OID: a.Entries[i].OID.Clone(), Value: a.Entries[i].Value})
	}
	if len(out) < reps {
		// 走到 MIB 末尾：v2c 用 endOfMibView 显式告诉你没有了，
		// 而不是少返回几条让你自己猜是超时还是到头。
		out = append(out, Varbind{EndOfMibView: true})
	}
	return out
}

// Simulate 完整模拟一次请求往返：能力上限、响应时延、不可达、超时。
//
// 返回的 Response.ElapsedMs 是这次往返花了多久；超过 timeout 的应答
// 一律折算成 timeout——真实采集器在超时之后就算收到回包也不会用它。
func (a *Agent) Simulate(req BulkRequest, timeout time.Duration) Response {
	if a.Profile.Unreachable {
		return Response{Error: ErrTimeout, ElapsedMs: timeout.Milliseconds()}
	}
	if req.MaxRepetitions > a.Profile.MaxRepetitions {
		// 设备只有这么大能力：max-repetitions 报大了它装不下，
		// 回 tooBig 而不是「努力返回一部分」。
		return Response{
			Error:     ErrTooBig,
			ElapsedMs: int64(a.Profile.ResponseMs / 2),
		}
	}
	delay := time.Duration(a.Profile.ResponseMs)*time.Millisecond + a.jitter()
	if delay > timeout {
		return Response{Error: ErrTimeout, ElapsedMs: timeout.Milliseconds()}
	}
	return Response{Varbinds: a.GetBulk(req.Start, req.MaxRepetitions), ElapsedMs: delay.Milliseconds()}
}

// jitter 是管理面抖动的来源：同一台设备连续两次请求的耗时也不会一样。
func (a *Agent) jitter() time.Duration {
	if a.Profile.JitterMs <= 0 || a.rng == nil {
		return 0
	}
	n := a.rng.Intn(2*a.Profile.JitterMs+1) - a.Profile.JitterMs
	return time.Duration(n) * time.Millisecond
}
