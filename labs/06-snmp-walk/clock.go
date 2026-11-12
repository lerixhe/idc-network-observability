package main

import "time"

// Clock 把「时间怎么走」抽象出来，一个实验两种跑法。
//
// 样本模式下时钟只记账：排队 24 秒、往返 210 ms 都是账面上的数字，
// 程序跑完不到一毫秒；UDP 模式下时钟是真的，该 sleep 就 sleep。
// 采集逻辑一行都不用改——这就是虚拟时钟的价值。
type Clock interface {
	Now() time.Time
	// Wait 是排队等待：这段时间里我们什么都没干，只是在等令牌。
	Wait(d time.Duration)
	// Spent 是已经花掉的时间（请求往返）：真实时钟下它已经过去了，不能再等一次。
	Spent(d time.Duration)
}

// VirtualClock 是账本时钟，起点由样本决定。
type VirtualClock struct{ now time.Time }

// NewVirtualClock 从某一时刻开始走。
func NewVirtualClock(start time.Time) *VirtualClock { return &VirtualClock{now: start} }

func (c *VirtualClock) Now() time.Time        { return c.now }
func (c *VirtualClock) Wait(d time.Duration)  { c.now = c.now.Add(d) }
func (c *VirtualClock) Spent(d time.Duration) { c.now = c.now.Add(d) }

// RealClock 是真时钟，UDP 模式用。
type RealClock struct{}

func (RealClock) Now() time.Time        { return time.Now() }
func (RealClock) Wait(d time.Duration)  { time.Sleep(d) }
func (RealClock) Spent(_ time.Duration) {}
