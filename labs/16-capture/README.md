# lab 16：抓包三端对照，判断丢包落在哪一段

一条端到端链路，丢包可能发生在四个地方：发送端本机、链路（线缆/中间设备之间）、
中间设备内部、接收端主机。只看一端的计数器永远说不清。本 lab 拿一份三端对照的
JSON 样本，做两件事：把差额按段拆开，再排掉三类常见的"假丢包"。

零第三方依赖，只用标准库 `encoding/json`。

## 怎么跑

```bash
cd labs/16-capture
make run          # 用 samples/case.json 输出归因表
make test         # 单元测试
make demo         # vet + test + 输出
make run SAMPLE=别的样本.json
```

直接跑也行：`go run . -sample samples/case.json`。

## 样本字段

```jsonc
{
  "case": "描述",              // 报告标题
  "window_seconds": 60,        // 计数采集窗口，必须三端同一段
  "flows": [
    {
      "id": "flow 名字",       // 报告里的小标题
      "sender": {
        "port": "leaf01/eth1",
        "tx_packets": 600000,  // 本机发出去的包
        "tx_bytes":   900000000,
        "tx_discards": 0,      // qdisc 丢弃
        "tx_errors": 0
      },
      "path": [                // 顺序即转发方向，至少一跳
        { "device": "spine01", "in_packets": 600000, "out_packets": 600000,
          "in_bytes": 900000000, "out_bytes": 900000000 }
      ],
      "receiver": {
        "port": "host-a/eth0",
        "rx_packets": 600000,  // 交给协议栈的包（不含丢弃）
        "rx_bytes":   900000000,
        "rx_discards": 0,      // NIC 计数层丢弃
        "rx_errors": 0,
        "ethtool": {           // ethtool -S 里那几个关键项
          "ring_rx_entries": 4096,
          "rx_queue_dropped": 0,
          "rx_no_buffer_count": 0,
          "tso": true, "gso": true, "gro": true, "lro": false
        },
        "icmp": { "sent": 120, "rate_limited": 0 }  // rate_limited = 内核限速丢的 ICMP 回包
      }
    }
  ]
}
```

约定：`rx_packets` 是交到协议栈的包数，`rx_discards` / `rx_errors` /
`ethtool` 的队列丢弃是"到了网卡但没上去"的部分，两者不重叠。
`rx_missed_errors` 若已经被内核计入 `rx_errors`，别再写进 `rx_no_buffer_count`，否则会双算。

## 归因口径

从发送端往下走，每一步只算增量差额，六段加起来就是总缺口：

| 段 | 算法 | 说明 |
| --- | --- | --- |
| `sender` | `tx_discards + tx_errors` | 压根没上线 |
| `link` | `tx - sender丢 - 第一跳 in_packets` | 出线到第一跳入线之间 |
| `device` | 逐跳 `in_packets - out_packets` 求和 | 设备内部丢，报告里列出是哪一跳 |
| `receiver` | `rx_discards + rx_errors` | NIC 计数层可见的丢 |
| `host` | `rx_queue_dropped + rx_no_buffer_count` | 到卡了、内核没接住，NIC 计数看不见 |
| `unattributed` | `最后一跳 out - rx - receiver丢 - host丢` | 对不上账的残差，非零就说明采样有问题 |

结论取差额最大的一段；总额为 0 或字节口径无差时报 `none`。

## 三个陷阱

1. **ring buffer 溢出**。`rx_discards` / `rx_errors` 干干净净，`ethtool` 却显示
   `rx_queue_dropped` 上万，ring 只有 512 项。看网卡计数的人会判成"链路丢"，
   实际是主机侧收包能力不足。落点记 `host`，不是 `link`。
2. **offload 假分段**。发送端 TSO/GSO 把大块数据当大包发，接收端 LRO/GRO 又把
   若干小包合成一个，两端的 `_packets` 天生对不上。判据是包丢率和字节丢率背离：
   包丢 33.5%、字节丢 0.2%，就是假信号。**包里对不上就看字节**，别拿包数当证据。
3. **ICMP 限速假丢包**。探测脚本 ping 一轮，回来一半，判定 50% 丢包。实际是内核
   `net.ipv4.icmp_ratelimit` 把回包限速丢了，数据面一个包没丢。样本里
   `icmp.rate_limited > 0` 即触发，报告会明说这是假信号。

## 期望输出

`make run` 五行 flow，关键几行长这样：

```
=== trap-ring-buffer: ... ===
  包: tx 480000 -> rx 470000   丢 10000 (2.083%)
  段                    丢失包
  host               10000
  结论: 落点=host
  陷阱:
    - ring-buffer: ethtool 显示队列丢弃 10000 包（ring_rx_entries=512）...

=== trap-offload: ... ===
  包: tx 200000 -> rx 133000   丢 67000 (33.500%)
  字节: 丢 600000 (0.200%)
  结论: 落点=none
  陷阱:
    - offload-segment: ... 包丢率 33.50% 与字节丢率 0.20% 背离 ...

=== real-loss-device: ... ===
  结论: 落点=device
  说明: 丢包在中间设备内部：leaf07 800 包，查缓存水位与拥塞
```

## 一处坑

窗口对不齐，整张表全废。三端计数器必须在同一时间段采集（同一个起止时间戳做差值），
采集脚本慢一拍、环回口抓早了半秒，`unattributed` 就会冒出一堆残差——看到这个段非零，
先查采集时间，别急着怀疑网络。
