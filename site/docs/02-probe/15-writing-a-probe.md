---
title: 15 · 写一个生产可用的探针
---

上一课用健康评分把不可信的探针摘出去，这一课换个方向：让探针从一开始就别产生那些异常。

## 先定三个结构，再写第一行代码

探针是一套流水线：任务进来，调度，发包，判定，上报。三种协议混在一个 `for` 循环里，第二个月就改不动。

```go
// Probe 是一种探测协议。三种协议的差别全部关在 Send 里。
type Probe interface {
	Name() string // 改名等于换了一种测量
	Send(ctx context.Context, job Job) Result
	Cost() int // 这一包消耗多少令牌：ICMP 记 1，TCP 记 2
}

// Job 是一条任务。所有影响结果的参数都在这里，缺一个就不可回放。
type Job struct {
	ProbeID  string
	Target   netip.Addr // IP 字面量，不在探针上解析域名
	Proto    string     // icmp / udp / tcp
	DstPort  uint16     // icmp 填 0
	SrcPort  uint16     // 固定源端口 = 可复现；第 12 课靠它散哈希
	Interval time.Duration
	Timeout  time.Duration
	Payload  []byte // 长度与内容都影响结果，必须固定
}

// Result 是一条上报。字段只增不改，旧字段的语义永远不变。
type Result struct {
	ProbeID  string `json:"probe_id"`
	Target   string `json:"target"`
	Proto    string `json:"proto"`
	SrcPort  uint16 `json:"src_port"`
	Seq      uint32 `json:"seq"`
	SentWall int64  `json:"sent_wall_ns"` // 挂钟，跨源对齐用
	SentMono int64  `json:"sent_mono_ns"` // 单调钟，只算 RTT
	RTTns    int64  `json:"rtt_ns"`
	Status   uint8  `json:"status"`
	ErrCode  string `json:"err_code,omitempty"`
	UptimeS  int64  `json:"uptime_s"` // 第 14 课要的那一个字段
	Version  string `json:"version"`
}
```

挂钟与单调钟分开是硬要求。挂钟会被 NTP 拨，用它算 RTT 会出现负数和尖刺；单调钟不跳，但跨机器没有可比性。两个都存，各干各的活。

## 三种协议，三种「成功」

同一句「探测失败」，在三个协议里是三种事。

| | ICMP echo | UDP | TCP |
|---|---|---|---|
| 成功的依据 | 收到 echo reply | 没收到端口不可达 | 三次握手完成 |
| 权限 | 原始套接字，root 或 `CAP_NET_RAW` | 普通套接字 | 普通套接字 |
| 中间设备爱干的事 | 限速、优先级最低 | 静默丢弃端口不可达 | 放行，防火墙通常留 80/443 |
| 超时的含义 | 包丢了，或回应丢了 | 三种可能，且分不开 | SYN 丢了，或 SYN 队列满 |
| 时延里掺了什么 | 纯网络 + 对端 ICMP 处理 | 纯网络 | 网络 + 对端内核建连处理 |

**UDP 的成功判据是反的。** 它靠 ICMP port unreachable 反推「对端主机活着、端口没开」。RFC 1122 要求主机发，大量设备关掉了：防火墙、负载均衡、云安全组、一部分交换机。这些网段上 UDP 探测永远超时，你拿不到证据说明是包丢了还是设备静默。结论：只在能确认对端会发 port unreachable 的网段用。

**TCP 的时延掺了对端内核时间。** 建连耗时 = 网络 RTT + 对端 SYN 队列排队 + SYN-ACK 的发送调度。空载机器上是几十微秒，SYN 队列有积压时能到几十毫秒。`00-setup` 的 README 第 1 条坑写的就是这个。

三个系统调用的 `error` 长得一样，含义不一样，判定函数不能共用：

```go
// classify 在协议语义这一层分道。上层别用 if err != nil 判失败。
func classify(proto string, err error) Status {
	if err == nil {
		return StatusOK
	}
	var ne net.Error
	switch {
	case errors.As(err, &ne) && ne.Timeout():
		// 包出去了，回声没回来。
		if proto == "udp" {
			return StatusUnknown // 丢包还是对端静默，分不清
		}
		return StatusTimeout
	case errors.Is(err, syscall.ECONNREFUSED):
		// TCP：RST，对端活着，端口没监听。
		// UDP：ICMP port unreachable，对端活着，端口没监听。
		return StatusRefused
	case errors.Is(err, syscall.EHOSTUNREACH), errors.Is(err, syscall.ENETUNREACH):
		return StatusUnreach // 路由不存在，或缺省网关替你回的 ICMP
	default:
		return StatusError // 本机资源不足、权限不对，别混进网络统计
	}
}
```

`StatusUnknown` 必须存在。硬算成丢包，UDP 网段的丢包率会永远偏高，高得稳定，稳定到没人去查。`00-setup` 第 3 条坑同理。

退避只给三种失败：`StatusTimeout` 连续 3 次、`StatusUnreach`、`StatusError`；阶梯 1×、2×、4×，封顶 5 分钟。`StatusRefused` 不退避，对端回了个包。一轮内不重传，重传会把丢包率算小一半。

## 限流：两级，缺一级都不行

探测包能打满设备。第 32 课讲平台侧的两级令牌桶，探针侧是它的另一半：**全局包速率**与**每目标在途上限**。全局管「别把自己变成扫描器」，每目标在途管「别让慢目标拖住调度器」。

```go
// bucket 令牌桶：每秒补 rate 个，最多攒 burst 个。
type bucket struct {
	mu     sync.Mutex
	tokens float64
	burst  float64
	rate   float64 // 每秒
	last   time.Time
}

func (b *bucket) take(n float64) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := time.Now()
	b.tokens = min(b.burst, b.tokens+now.Sub(b.last).Seconds()*b.rate)
	b.last = now
	if b.tokens < n {
		return false
	}
	b.tokens -= n
	return true
}
```

第二级用带缓冲的 channel：每目标一个 `chan struct{}`，容量即在途数，容量 1 即严格串行；`select` 带 `default`，非阻塞。

拿不到令牌就**丢弃这一轮，记一条 `skipped`**。排队会让延迟越滚越大，包发出去时链路已经变了，数据还在往上走，只是全错。

一台探针 100 个目标、每目标每秒 1 包，稳态 100 包/秒；全局桶设 500 包/秒、burst 100，用来吸收 TCP 探测（Cost 2）与重试峰值。burst 再大会自己造出微突发，而它正是你要测的东西（第 40 课）。

## 时间戳打错一处，时延恒定少估一个值

一个 RTT 是两个时间戳相减，位置错了就是系统性偏差。

```go
sent := time.Now()         // ① 用户态，进 syscall 之前
err := conn.Write(payload) // ② 包进内核、排队、过网卡
recv := time.Now()         // ③ syscall 返回之后，用户态
```

三个位置，三种结论：

| 打在哪 | RTT 里含什么 | 偏差方向 |
|---|---|---|
| ① 发送前 + ③ 收到后 | 内核发送路径 + 网卡排队 + 对端 + 用户态唤醒 | 高估，方差被调度放大 |
| ② 发送后 + ③ 收到后 | 只剩网络与对端 | 少估发送路径那一段 |
| ① + 内核收包时间（`SO_TIMESTAMPNS`） | 少掉两端 syscall 与唤醒 | 最接近真实 |

少估的量能算。千兆网卡上，从用户态 `write` 返回到包出现在线上，正常 30–80 µs，队列积压时到毫秒级。链路真时延 200 µs，少估 60 µs 就是 30% 误差，而且**恒定**——所有包往同一方向偏，任何基线算法都发现不了。

秒级时间戳（`Unix()` 不带纳秒）更糙：误差均匀落在 [0, 1) 秒，期望 0.5 秒 = 500,000 µs。同一条 200 µs 的链路被记成上下抖 500 ms，放大 2500 倍，看图像极端拥塞。

`00-setup` 用的是 ①+③，够比快慢，不够量绝对值。

## 编码与可回放：52 字节，四个参数不能少

同样一个 `Result`，两种编码：

```text
JSON  {"probe_id":"3f02-11","target":"10.2.3.4","proto":"icmp","src_port":33001,
       "seq":123456,"sent_wall_ns":1794466766606123456,"sent_mono_ns":881234567,
       "rtt_ns":321000,"status":0,"uptime_s":691200,"version":"1.4.2"}
       ≈ 230 字节

紧凑二进制  8 probe_id + 4 target(IPv4) + 1 proto + 2 src_port + 4 seq
          + 8 sent_wall + 8 sent_mono + 4 rtt + 1 status + 8 uptime + 4 version
          = 52 字节
```

230 / 52 = 4.4 倍。十万探针、每探针 1 Hz，一天 86.4 亿条：JSON 装 86.4 亿 × 230 B ≈ 1.99 TB，二进制 449 GB。

先别急着全换：JSONL 用 `jq` 就能查，二进制得先有工具。做法是**上报走二进制，落盘与回放留 JSONL**，`00-setup` 的 `out/samples.jsonl` 是后者。

回放是排障的最后一招：拿时间窗内的样本重跑聚合，确认结论没算错。样本要自带全部参数：**源端口**（随机源端口等于随机 ECMP 分支，重跑测的是另一条路，第 12 课）、**payload 长度与内容**（变了就变分片行为）、**超时值**（改一次，丢包率直接变）、**目标 IP 字面量**（用域名，解析随 TTL 和 DNS 轮转漂）。

第 12 课要求散哈希，源端口必须变。两条不冲突：**随机的结果要记下来**。任务带一个端口段，探针在段内取，取到哪个写进样本。可回放的意思是「知道实际用了什么」。

## 插件化：三个接口切开，别切成五层

| 层 | 只干一件事 | 换它的理由 |
|---|---|---|
| 调度 | 决定下一轮发哪些 Job | 固定间隔换成分层间隔（第 11 课） |
| 协议 | `Probe.Send` 发一包、判一次 | 加 TWAMP、加 HTTP 拨测 |
| 上报 | 缓冲、批量、重试、压缩 | HTTP 批量换 gRPC 流（第 30 课） |

三层之间只传 `Job` 和 `Result`，这是职责边界：调度只产出 `Job`，不碰套接字；协议只回 `Result`，不管有没有人在等；上报只消费 `Result`，不改一个字节。

切过头的例子见过一次：把「取时间戳」也做成接口，结果每个实现各维护一套时间戳语义，ICMP 打内核态、TCP 打用户态，回放时两条曲线对不齐。想让协议层知道「上报还剩多少缓冲」的改动，会在这里卡住。

## `00-setup` 是这个课的最小版

`labs/00-setup` 一百多行，四件事都在：

| 它做了什么 | 对应哪一节 | 缺什么 |
|---|---|---|
| `probeAll` 用 channel 信号量限 workers，目标间并行、目标内串行 | 限流第二级 | 没有全局包速率；在途上限是全局的，一个慢目标占住一个 worker |
| `Percentile` 用最近秩法只出 p50/p99，不出平均值 | 口径 | 样本不足 100 条时 p99 就是最大值 |
| `writeJSONL` 一条一行落盘 | 可回放 | 没记 payload、超时值、源端口 |
| `probeOnce` 用 TCP 建连耗时替代 RTT | 协议差异 | 掺了对端 accept 时间；`ok: false` 不分原因 |

从这四条补起，就能上生产。

下一课换到设备侧，从 SNMP 的一个 OID 开始。

## 判据

给一份自己的探针接口定义：三个结构齐，三种协议的判定不共用一套代码。

验收：

1. 任务结构里每个字段能说出「少了它会怎样」，至少四条
2. 上报结构里挂钟与单调钟分开，说出各自被谁用
3. `StatusUnreach`、`StatusRefused`、`StatusUnknown` 各在什么情况下产生，哪种协议根本产生不了其中一个
4. 你的限流器取不到令牌时是丢还是排，说出这个选择在数据上留下的痕迹

::: details 参考

1. 源端口（决定 ECMP 分支）、payload 长度（决定分片）、超时值（决定丢包率）、目标 IP 字面量（避开 DNS 轮转）。
2. `sent_wall_ns` 给跨源对齐与存储分区（第 46 课）；`sent_mono_ns` 只算 RTT。挂钟被 NTP 往回拨 500 ms 时，用它算 RTT 会得到负数。
3. TCP 靠 RST，UDP 靠 ICMP port unreachable，都能给出 `Refused`；ICMP 没有端口概念，产生不了。`StatusUnknown` 只有 UDP 需要，它的失败判据依赖一个会被静默的 ICMP。
4. 丢：丢包率里出现缺口，旁边有 `skipped` 计数可查。排：时延曲线出平台期，一批包的时间戳挤在一起，看图像链路变好，实际是队列在替你缓冲。

:::

## 小结

三个结构先定。接口定错，后面每加一个协议都要打补丁。

三种协议三种成功：ICMP 等回声，UDP 等拒绝，TCP 等握手。失败判定按协议分道，`StatusUnknown` 必须存在。退避只给含糊的失败，`Refused` 不退避。

限流两级，取不到就丢并记数。时间戳打在 syscall 前后差一段恒定少估；秒级时间戳差 500 ms。

编码 230 字节对 52 字节。可回放靠四个参数，随机的东西要落下痕迹。`labs/00-setup` 是最小版，补四条就能上生产。

## 思考

1. 你的上报里有没有单调钟？只用挂钟算 RTT，NTP 每次校准会在曲线上留下什么形状？
2. UDP 探测的成功判据是「收到端口不可达」，如果某个网段里一半设备静默，那一半的丢包率该怎么算、口径说明怎么写？
3. 拿不到令牌时选择丢弃，对「微突发」这类故障的检测会造成什么后果？
