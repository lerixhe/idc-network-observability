# 06-snmp-walk · GetBulk 表遍历、档位限流与主键映射

第 16、17、19 课的配套实验。一条完整的采集链：按列把 `ifTable` + `ifXTable` 摸下来（模拟 GetBulk 的分批）、按设备能力分档限流（排队不丢包）、把 `ifIndex` 这张当轮句柄翻译成稳定主键。

最后拿一份「换板重启之后」的样本，看同一个 `ifIndex` 怎么指向了另一个端口。

默认全程用样本 + 内置 agent，虚拟时钟，不连网、不碰设备。

## 跑起来

```bash
cd labs/06-snmp-walk
make run     # 6 轮全量采集，五台设备三种命运
make test    # vet + 单元测试
make demo    # 端口表换成老设备 tor-a3f-02（看没有 alias 的口）
make try     # max-repetitions 一律顶到 64，看 B/C 档怎么退
make udp     # 内置 agent 挂到 127.0.0.1，采集器走真实 UDP 往返
make clean   # 删 out/
```

零外部依赖，不解析 BER，不发一个真实 SNMP 报文。

## 期望输出

`make run`（节选）：

```text
样本 samples/fleet.json（5 台设备，100 个端口）· 传输 sample · 时钟虚拟 · 6 轮 × 30s
列：ifTable 7 列 + ifXTable 5 列 = 12 列；端口数 × 列数 = 1200 个单元格

包量与耗时（每轮实算：PDU 数、排队、往返、占周期比例）
device          tier   ports  reps   PDU  varbind     排队     往返   本轮耗时  占周期
leaf-legacy-01  C         12     8    24      156      0ms    37.4s      37.4s    125%
spine-01        A         24    64    12      300      0ms    578ms      578ms      2%
tor-a3f-01      B         24    16    24      300    729ms     1.9s       2.6s      9%
tor-a3f-02      C         24     8    48      300    13.0s    10.2s      23.2s     77%
tor-b7f-01      B          -     - 本轮从未采到数据
  PDU = 列数 × ⌈(端口数+1)/reps⌉；那个 +1 是走出子树的那一条，它不落在任何行上
  排队 = 令牌不够干等的秒数；往返 = 设备真的在干活的时间

按设备汇总（6 轮累计）
device          tier   ports   请求   超时  tooBig   p95往返   行数 结论
leaf-legacy-01  C         12     72      0       0    1848ms     12 响应慢
spine-01        A         24     72      0       0      64ms     24 正常
tor-a3f-01      B         24    144      0       0     109ms     24 正常
tor-a3f-02      C         24    288      0       0     266ms     24 正常
tor-b7f-01      B         16     12     12       0    1200ms      0 不可达
  12 列 × 100 端口 = 1200 个单元格；累计 5868 条 varbind，最后一次采到的端口合计 84 行

限流与隔离（每轮每台设备）
 轮 device          结论         请求     排队   超时   重试 说明
  1 leaf-legacy-01  响应慢         24      0ms      0      0 本轮占 37.4s，超过周期 30.0s
  1 spine-01        正常           12      0ms      0      0
  1 tor-a3f-01      正常           24    729ms      0      0
  1 tor-a3f-02      正常           48    13.0s      0      0
  1 tor-b7f-01      不可达          6      0ms      6      3 连续 3 列取不到，中止本轮；整轮零行，隔离；第 6 轮再探
  2 leaf-legacy-01  响应慢         24      0ms      0      0 本轮占 37.2s，超过周期 30.0s；连续 2 轮判慢，降频跳过 3 轮
  2 tor-b7f-01      跳过            -        -      -      - 跳过（不可达，第 6 轮再采）
  3 leaf-legacy-01  跳过            -        -      -      - 跳过（响应慢，第 6 轮再采）
  …
  6 leaf-legacy-01  响应慢         24      0ms      0      0 本轮占 37.6s，超过周期 30.0s；连续 3 轮判慢，降频跳过 3 轮
  6 tor-b7f-01      不可达          6      0ms      6      3 连续 3 列取不到，中止本轮；整轮零行，隔离；第 11 轮再探
```

端口表打一台正常设备的全量 24 行，其中一行会顺手证明 `ifSpeed` 为什么不能用：

```text
端口表（spine-01，最后一次采到的那一轮）
ifIdx ifName          速率 状态      if_hc_in  if_hc_out errs if_alias
    1 Ethernet1/1     100G up/up     187.4 GB   245.1 GB    0 to-tor-a3f-01:Et1/1
      ↳ Ethernet1/1 的 ifSpeed 读出 4294967295（顶格）：32 位、单位 bit/s，超过 4.29 Gbps 的端口一律读成这样；速率只能取 ifXTable 的 ifHighSpeed=100000
```

重排那一节是整份报表的落点：

```text
换板重启之后（samples/fleet-rebooted.json）：同一个 ifIndex，另一个端口

  leaf-legacy-01（12 个端口）
    按 ifIndex 对接：接上 12 条，其中 0 条接的是别的端口；断线 0 条，新增 0 条
    按 ifName 对接：接上 0 条，错对象 0 条；断线 12 条，新增 12 条
      断线的例子：GE0/0/1 → GE0/1/1（换板后槽位号变了）
    按 ifAlias 对接：接上 12 条，错对象 0 条；断线 0 条，新增 0 条

  tor-a3f-01（24 个端口）
    按 ifIndex 对接：接上 16 条，其中 16 条接的是别的端口；断线 8 条，新增 8 条
     ifIdx 上一轮                                         本轮                                            速率比
        15 Ethernet1/15 10G to-srv-a3f-01-15-nic0         Ethernet1/23 100G to-spine-01:Et1/1                ×10
        16 Ethernet1/16 10G to-srv-a3f-01-16-nic0         Ethernet1/24 100G to-spine-01:Et1/2                ×10
        23 Ethernet1/23 100G to-spine-01:Et1/1            Ethernet1/3 10G to-srv-a3f-01-03-nic0              ×10
        24 Ethernet1/24 100G to-spine-01:Et1/2            Ethernet1/4 10G to-srv-a3f-01-04-nic0              ×10
    …… 还有 12 条同类错接，16 条里 4 条连速率都对不上
    按 ifName 对接：接上 24 条，错对象 0 条；断线 0 条，新增 0 条
    按 ifAlias 对接：接上 24 条，错对象 0 条；断线 0 条，新增 0 条
```

`ifIndex 15` 上一轮是一个 10G 服务器口，换板之后这个索引上挂的是 100G 上联。曲线不会断，也不会报错，只会继续画在别的端口身上。

## 一次表遍历走的是列，不是行

一个表在 OID 树上不是一个整齐的二维数组。`ifTable` 的 24 个端口，在树上排成 7 段，每段 24 个叶子：

```text
ifDescr.1 .. ifDescr.24        （1.3.6.1.2.1.2.2.1.2.x）
ifType.1  .. ifType.24         （1.3.6.1.2.1.2.2.1.3.x）
...
ifName.1  .. ifName.24         （1.3.6.1.2.1.31.1.1.1.1.x）
ifAlias.1 .. ifAlias.24        （1.3.6.1.2.1.31.1.1.1.18.x）
```

一次 GetBulk 拿不到「第 1 行到第 8 行的全部列」，它只能顺着字典序往下走。所以采集器一列一列地走，最后靠 `ifIndex`（单元格 OID 的最后一级）把 12 个格子拼成一行。

```text
每列：GetBulk(max-repetitions=reps)
   → 收到一批 varbind
   → 全都以列的 OID 开头？继续从最后一条之后取
   → 有一条不再以列的 OID 开头？这列走完了
   → 设备回 tooBig？reps 砍半，重来这一页
   → endOfMibView / 没有前进？停
```

三个细节决定这一列会不会采错：

1. **停不停是采集器判的，不是设备判的。** `ifTable` 之后紧跟着 `ipAdEntAddr`，`ifXTable` 之后还有 `ifStackLastChange`。设备永远「还有下一条」，判断前缀的活只能自己干。
2. **`+1` 那一页。** 24 个端口、reps=8，要走 4 页：3 页取数，第 4 页只是确认已经走出子树。这页不产出任何数据，但少走它就没法区分「正好取满」和「还有更多」。
3. **`ifIndex` 不是走出来的列。** 它是单元格 OID 的最后一级。样本里 12 个可遍历的列全都不含它。

## 分档限流：把「采得全」和「别采死」分开

老 TOR 的 SNMP 是单线程的。一次 GetBulk 要 50 条，管理面 CPU 就 30%，那时候连 trap 都开始丢。档位不是设备型号，是「这台设备的管理面能承受多少」。

| 档位 | `max-repetitions` | 请求/秒 | 桶容量 | 超时 | 重试 | 依据 |
|---|---|---|---|---|---|---|
| A | 64 | 20 | 8 | 1.0s | 2 | 现代交换机，管理面多核 |
| B | 16 | 8 | 4 | 1.2s | 2 | 上一代接入/汇聚，单核管理面 |
| C | 8 | 2 | 2 | 2.0s | 3 | 2013 年前后的老 TOR，单线程 SNMP |

档位是策略，写错必须报错。静默按 A 档去打一台老设备，一晚就能把它的管理面打死。

`max-repetitions` 除了被我们的档位限制，还被设备自己的能力限制：样本里每台设备带一个 `max_repetitions` 上限，请求超过它就回 `tooBig`，采集器只能砍半重试。`make try` 就是把条数一律顶到 64，看三档设备各自退到几步：

```text
device          tier  tooBig  最终 reps
spine-01        A         0         64   本来就是 64，没被拒
tor-a3f-01      B         2         16   64→32→16
tor-a3f-02      C         3          8   64→32→16→8
leaf-legacy-01  C         3          8   同一台 C 档设备，第 1 轮第一列就被连拒三次
```

背退的结果存在会话里（`Session.Reps`），所以 6 轮下来的 `tooBig` 只有第 1 轮那几次——不是每列、每轮都去撞一次墙。

### 包量实算

`PDU = 列数 × ⌈(端口数+1)/reps⌉`，12 列、24 个 10G 口：

| 档位 | reps | PDU/列 | PDU/轮 | 限流下最快 | 与周期的关系 |
|---|---|---|---|---|---|
| A | 64 | 1 | 12 | 12 ÷ 20 = 0.6s | 周期的 2% |
| B | 16 | 2 | 24 | 24 ÷ 8 = 3.0s | 周期的 10% |
| C | 8 | 4 | 48 | 48 ÷ 2 = 24s | 周期的 80% |

最后一行就是问题所在。**同一批数据，C 档设备一轮要 24 秒，占了 30 秒周期的八成**；周期压到 10 秒，它就永远采不完。实测 `tor-a3f-02`（24 口，C 档）排队 13.0s、往返 10.2s、合计 23.2s，占周期 77%——那 13 秒的排队不是浪费，是这台设备的处理速度只有 2 请求/秒。

反过来，把 C 档设备的参数提到 A 档：12 个请求在 0.6 秒内打完，等于每秒 20 个请求打在一个每秒只能处理 2 个的管理面上，积压 10 倍。CPU 打满之后先丢的是 trap，然后是我们的下一个请求。

### 排队，不丢

限流器是令牌桶，做法是**预约**：每次请求先问「我最早什么时候能发」，拿到的时刻晚于现在就地等。丢请求看起来更省事，代价是采集结果出现空洞，而且事后补不回来——端口在那里，数据没了。排队至少保证「慢，但全会到」。

`tor-a3f-02` 六轮累计 288 个请求，一个没丢，全变成了 24 行 × 12 列。

### 三种设备，三种处置

| 结论 | 判据 | 处置 | 样本里的设备 |
|---|---|---|---|
| 正常 | 零超时，平均往返 ≤ 500ms | 照常每轮采 | `spine-01`、`tor-a3f-01`、`tor-a3f-02` |
| 响应慢 | 有超时，或平均往返 > 500ms，但仍有数据 | 连续 2 轮判慢 → 跳过 3 轮 | `leaf-legacy-01`（1.6s ± 0.3s） |
| 不可达 | 整轮零行 | 立即隔离，每 5 轮探一次 | `tor-b7f-01` |

「响应慢」和「不可达」必须分开统计。前者该查管理面 CPU、ACL、SNMP 引擎；后者该查电源和带外。合成一个「异常」，两件事都查不了。

`leaf-legacy-01` 一轮 37.4 秒，本身就超过 30 秒周期——这种设备靠调参数救不回来，只能降频，或者把它拆到独立的采集任务里去。

## 主键：ifIndex 是句柄，不是身份

一次采集有 1200 个单元格，落到时序库里要有主键。三个候选：

| 键 | 是什么 | 什么情况下失效 | 失效后果 |
|---|---|---|---|
| `ifIndex` | 设备给这一行的本地索引 | 换板、重启、插拔业务板 | **接错对象**：不报错，曲线接到别人身上 |
| `ifName` | 厂商的端口名，`Ethernet1/1` / `GE0/0/1` | 换主控板、槽位号变、堆叠重编号 | 断线：历史还在库里，新数据另起一条 |
| `ifAlias` | 人写的描述 | 没人填、多人填重（`to-core` 出现三次）、改名 | 兜底失效或接错 |

样本里的两份数据把三种错法各演一遍：

```text
tor-a3f-01   换板重启，24 个端口的 ifIndex 全变了
             按 ifIndex 对接：16 条接上，16 条全是别的端口（8 条断线、8 条凭空多出来）
             按 ifName 对接：24 条全接上，0 错对象

leaf-legacy-01 换主控板，端口名从 GE0/0/x 变成 GE0/1/x
             按 ifIndex 对接：12 条接上，0 错（索引没变，名字变了）
             按 ifName 对接：0 条接上，12 条全断
             按 ifAlias 对接：12 条全接回
```

结论只有一条：**指标主键既不能是 ifIndex，也不能只靠 ifName**。生产上的做法是 CMDB 给端口发一个不随设备变的 ID（设备序列号 + 端口物理位置），`ifName` 和 `ifAlias` 都只是它的别名。第 23 课那个「换板后曲线接错对象」的事故，根因就在这里。

本实验的主键取 `device + "/" + ifName`，同时把 `ifIndex` 一起写进指标行。索引在指标里是证据，不是键：

```json
{"cycle":1,"at_ms":0,"device":"spine-01","key":"spine-01/Ethernet1/1","if_index":1,"if_name":"Ethernet1/1","if_alias":"to-tor-a3f-01:Et1/1","speed_mbps":100000,"if_admin_status":1,"if_oper_status":1,"if_hc_in_octets":187400000000,"if_hc_out_octets":245100000000,"if_in_errors":0}
```

## 样本格式

### `samples/fleet.json`

```json
{
  "note": "ifTable + ifXTable 的应答样本。真链路是 BER/ASN.1，这里用 JSON 存同一张 varbind 表。",
  "collected_at": "2026-05-12T10:12:00Z",
  "devices": [
    {
      "name": "tor-a3f-02",
      "mgmt_ip": "10.0.1.22",
      "model": "Cisco Nexus 3048",
      "firmware": "7.0(3)I7(9)",
      "sys_uptime_s": 201338771,
      "snmp": {"tier": "C", "max_repetitions": 8, "response_ms": 210, "jitter_ms": 60, "unreachable": false},
      "interfaces": [
        {"if_index": 1, "if_name": "Ethernet1/1", "if_alias": "to-srv-a3f-02-01-nic0", "...": "..."}
      ]
    }
  ]
}
```

| 字段 | 对应 MIB / 含义 |
|---|---|
| `sys_uptime_s` | `sysUpTime.0`，换算成 ticks；用来对「设备是否重启过」 |
| `snmp.tier` | 档位，决定 reps / 速率 / 超时（A / B / C） |
| `snmp.max_repetitions` | **设备自己**的上限，请求超过它回 `tooBig` |
| `snmp.response_ms` / `jitter_ms` | 管理面时延模型，配 `jitter` 才有 p95 可看 |
| `snmp.unreachable` | true = 永不回包（管理口不通 / SNMP 没开） |
| `if_index` | `ifTable.1.ifIndex`，也是所有列的行索引 |
| `if_name` | `ifXTable.1.ifName`，主键的原料 |
| `if_alias` | `ifXTable.1.ifAlias`，兜底键 |
| `if_descr` | `ifTable.1.ifDescr`，厂商字符串（含槽位/子卡信息） |
| `if_type` | `ifTable.1.ifType`，6 = ethernetCsmacd |
| `if_speed_bps` | `ifTable.1.ifSpeed`，**32 位、单位 bit/s**，10G 以上顶格 |
| `if_high_speed_mbps` | `ifXTable.1.ifHighSpeed`，单位 Mbps，速率要用这个 |
| `if_admin_status` / `if_oper_status` | `ifTable.1.ifAdminStatus` / `ifOperStatus` |
| `if_hc_in_octets` / `if_hc_out_octets` | `ifXTable.1.ifHCInOctets` / `ifHCOutOctets`，counter64 |
| `if_in_errors` / `if_out_errors` | `ifTable.1.ifInErrors` / `ifOutErrors`，counter32 |

### `samples/fleet-rebooted.json`

同一批设备换板重启之后的表。只放变化了的两台：`tor-a3f-01`（ifIndex 重排）、`leaf-legacy-01`（ifName 换槽位号）。采集器为它单独跑一轮，两轮结果做三把键的对接。

### 为什么 wire 层是 JSON

SNMP 的报文是 BER 编码的 ASN.1。写一个能编解码 BER 的最小实现要 400 行以上，其中 300 行在处理长度字节、整数符号位、OID 的 40×X+Y 首字节——这些与本实验要讲的三件事（按列遍历、限流、主键）没有一行关系。

所以这一层用 JSON 存同一批 varbind，但把 GetBulk 的语义一字不改地留着：字典序、从 `start` 之后开始、`max-repetitions` 上限、`tooBig`、`endOfMibView`、表尾之后还有别的 OID。`make udp` 把这份 JSON 挂到 127.0.0.1 的真实 UDP 套接字上，超时和往返是真的——它验证得了超时、丢包、迟到应答，验证不了 BER 的长度编码陷阱。

换真实设备要补的只有 `transport.go` 那一个 `Transport` 实现。

## 这个实验故意留的坑

1. **`ifSpeed` 是 32 位、单位 bit/s，10G 就顶格。** 样本里 100G 口的 `ifSpeed` 读出 `4294967295`，和 10G 口一个数。只看这一列，10G 和 100G 分不出来。速率必须用 `ifXTable.ifHighSpeed`（Mbps，32 位够用到 4T）。
2. **`ifAlias` 可以作为兜底，但不能当主键。** 样本里 `tor-a3f-02` 有两个口是空的；真机上更常见的是「to-core」这种写法在一台设备上出现三次。`PortTable.DupAlias` 就是在数重复 alias，查空串一律不命中。
3. **`ifName` 也会变。** 换主控板后槽位号从 0 变 1，整表 12 条历史当场断线——样本里 `leaf-legacy-01` 就是这一出。它有 alias 兜底，没 alias 的端口就彻底丢了。
4. **表尾判定只能靠前缀。** 少写这一处判断，`ifTable` 的最后一页会混进 `ipAdEntAddr` 的行，`ifXTable` 的最后一页会混进 `ifStackLastChange`。数据会变多，看起来还更全。
5. **`tooBig` 只能砍半重试，不能放弃。** 放弃一列等于这一轮所有端口少一个字段；砍半到 1 条还不行，那才是真采不了。
6. **门槛全是策略。** 500ms 判慢、连续 2 轮降频、每 5 轮探一次、连续 3 列中止、档位表里那三行数字——每一换环境就该重定。写死在算式里就没法调。
7. **不可达的设备会白烧 6 次超时。** 第 1 轮只打了 6 个请求就中止（3 列 × 2 次尝试），换成 B 档 1.2 秒超时就是 7.2 秒。隔离之后这笔开销降到每 5 轮一次，这就是隔离省下来的钱。
8. **慢设备会把后面的设备挤晚。** 采集器是单协程顺序采的，`leaf-legacy-01` 占掉 37 秒，同一轮里排在它后面的设备就晚 37 秒开始。真实的采集器要按设备并发，代价是限流桶和时钟都得变成每设备一份——本实验只留了顺序采这一种，为的是账好算。
9. **`sysUpTime` 在 agent 里，采集器没去取。** 判定「重启过」的常用做法是看它是否归零，本实验判断重排只看两份表的差异。真机上两条线索要合起来用：`sysUpTime` 归零 + ifName 集合没变 + ifIndex 映射变了 = 重排。
10. **虚拟时钟让「采不完」变得不疼。** 样本模式下一秒跑完 6 轮，`leaf-legacy-01` 那 125% 的周期占用只是账面上的数字。真机上它意味着采集任务永远在追赶自己。

## 文件

| 文件 | 作用 |
|---|---|
| `oid.go` | OID 类型、字典序比较、子树前缀判定 |
| `mib.go` | 12 列的定义、样本结构与校验、设备叶子表展开 |
| `agent.go` | 极简 agent：GetBulk 语义、能力上限、时延与不可达 |
| `walk.go` | 按列遍历、`tooBig` 背退、行拼接 |
| `tier.go` | 分档参数表与令牌桶限流 |
| `clock.go` | 虚拟时钟与真实时钟 |
| `transport.go` | 样本传输、UDP 回环 agent 与客户端 |
| `collector.go` | 每设备会话：限流、重试、判慢、降频、隔离 |
| `identity.go` | 稳定主键、三种键的对接、重排检测 |
| `report.go` | 终端报表与三份产物 |
| `main.go` | 参数装配 |
| `samples/fleet.json` | 5 台设备共 100 个端口的应答样本 |
| `samples/fleet-rebooted.json` | 换板重启后的两份表（ifIndex 重排、ifName 改名） |
| `out/if_metrics.jsonl` | 每轮每端口一行（`make run` 生成，不进 git） |
| `out/collect.json` | 设备级账本：档位、每轮 PDU、排队、超时、隔离原因 |
| `out/reindex.json` | 三把键的对接差异与重排明细 |

`*_test.go` 按源码名一一对应，另加 `transport_test.go`（UDP 回环）与 `report_test.go`（报表与产物）。
