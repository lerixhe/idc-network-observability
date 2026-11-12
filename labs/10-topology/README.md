# 10-topology · 拓扑多源融合与事件去重

第 26、29 课的配套实验。一个 module，两段能力，共同点只有一个：**都靠 CMDB 的别名表把别人嘴里的名字翻译成规范名**。

| | 拓扑融合 | 事件去重 |
|---|---|---|
| 回答 | 交换机和交换机之间怎么连 | 这四条消息是不是一回事 |
| 输入 | LLDP / STP / 路由邻居 / MAC 表四份快照 | Trap / syslog / 计数器归零 / 探测失败四源事件 |
| 输出 | 链路清单（带置信度）+ 冲突清单 | 事件清单（带原始计数）+ 冲突清单 |
| 核心手段 | 按置信度取高，不投票 | 指纹 + 时间窗，两级聚合 |

名字翻译错了，两段一起错：拓扑连到三年前报废的设备上，事件记在别人家的端口上。两种错都不抛异常，只是数字看起来很正常。

## 跑起来

```bash
cd labs/10-topology
make run        # 两段都跑，结果打到终端，产物落 out/
make topology   # 只跑拓扑融合
make events     # 只跑事件去重
make try        # 合并窗口从 5 分钟压到 30 秒，看哪些事件裂开
make demo       # README 引用的那份输出：全套 + 两套参数对照
make test       # vet + 单元测试
make clean      # 删 out/
```

零外部依赖，不连网、不连设备，样本是手读得了的 JSONL。

## 期望输出

`make run` 第一段（原文）：

```text
=== 一、拓扑融合：四份快照 → 设备拓扑 + 冲突清单 ===
LLDP 17 行 / STP 4 行 / 路由邻居 3 行 / MAC 4 行

#    a                          b                          kind      conf  flags                    sources
1    acc-a3f-09:Ethernet1/1     spine-01:Ethernet1/2       physical  high  one-sided,unknown-peer   lldp
2    spine-01:Ethernet1/1       tor-a3f-01:Ethernet1/49    physical  high  -                        lldp
3    spine-01:Ethernet1/48      tor-a3f-01:Ethernet1/48    physical  high  -                        lldp
4    spine-01:Ethernet1/5       tor-a3f-02:Ethernet1/5     physical  high  one-sided                lldp
5    spine-01:Ethernet1/6       tor-a3f-02:Ethernet1/6     physical  high  -                        lldp
6    spine-01:Ethernet1/9       spine-02:Ethernet1/9       physical  high  redundant                lldp
7    spine-01:Port-Channel10    tor-a3f-01:Eth-Trunk1      lag       high  -                        lldp
8    spine-02:Ethernet1/5       tor-a3f-02:Ethernet1/5     physical  high  one-sided                lldp
9    tor-a3f-01:Ethernet1/7     tor-a3f-02:Ethernet1/7     physical  high  -                        lldp
10   tor-a3f-04:Ethernet1/1     spine-01:?                 inferred  low   ?                        mac

链路合计 10 条：物理 8 / 聚合 1 / 反推 1；其中问号 1；冗余 1；单侧断言 3
来源账：LLDP 17 行（可用 16）/ MAC 4 行（用上 3）——STP 与路由邻居只做校验，不出链路
聚合口对齐：spine-01:Port-Channel10 ↔ tor-a3f-01:Eth-Trunk1
  LLDP 实际报的对：spine-01:Ethernet1/10 ↔ tor-a3f-01:Eth-Trunk1、spine-01:Ethernet1/11 ↔ tor-a3f-01:Eth-Trunk1
  台账成员 spine-01:Port-Channel10 = [Ethernet1/10, Ethernet1/11]
  台账成员 tor-a3f-01:Eth-Trunk1 = [Ethernet1/10, Ethernet1/11]

提示清单 10 条（device_unknown 1 / lldp_one_sided 3 / mac_conflict 1 / mac_inferred 1 / peer_mismatch 1 / port_alias_unknown 1 / route_mismatch 1 / stp_blocked 1）：
  [warn    ] device_unknown      spine-01:Ethernet1/2 报的对端 acc-a3f-09 不在台账里（幽灵设备），链路保留、设备待建档
  [warn    ] peer_mismatch       spine-01:Ethernet1/5 说连 tor-a3f-02:Ethernet1/5，但 tor-a3f-02:Ethernet1/5 说自己连 spine-02:Ethernet1/5（两端说法对不上，按 LLDP 保留，需现场核）
  [warn    ] port_alias_unknown  spine-02:Ethernet1/11 报的对端端口 id="Eth1/11x" desc="" 在台账里翻不出来，整条断言丢弃
  [info    ] lldp_one_sided      只有 spine-01:Ethernet1/2 报了这条链路，acc-a3f-09:Ethernet1/1 没报回来（对端 LLDP 未开、或邻居条目已超时）
  [info    ] lldp_one_sided      只有 spine-01:Ethernet1/5 报了这条链路，tor-a3f-02:Ethernet1/5 没报回来（对端 LLDP 未开、或邻居条目已超时）
  [info    ] lldp_one_sided      只有 tor-a3f-02:Ethernet1/5 报了这条链路，spine-02:Ethernet1/5 没报回来（对端 LLDP 未开、或邻居条目已超时）
  [info    ] mac_conflict        tor-a3f-02:Ethernet1/7 学到 spine-01 的管理 MAC 00:1b:21:aa:00:11（反推指向 spine-01），但 LLDP 说这条链路另一端是 tor-a3f-01——按 LLDP 保留，MAC 只进提示（漂移或泛洪）
  [info    ] mac_inferred        tor-a3f-04:Ethernet1/1 只有 MAC 反推支撑，对端端口是问号，置信度 low——先去把 LLDP 打开再谈拓扑
  [info    ] route_mismatch      tor-a3f-02:Ethernet1/7 的三层邻接指向 spine-01，二层链路却指向 tor-a3f-01（子接口、跨设备聚合、或采集错）
  [info    ] stp_blocked         spine-02:Ethernet1/9 在 STP 里是 alternate/blocking，这条链路是冗余路径（blocked 不等于错，别当故障报）
```

`make run` 第二段：

```text
=== 二、事件去重：四源事件 → 同一件事一条事件 ===
合并窗口 5m0s；设备级聚合：60s 内 ≥ 8 个端口同族触发
指纹字段：族 | 设备规范名 | 端口规范名（时间、来源、序号、原文都不进指纹）

#    ts                    device         port          family      raw  fingerprint                    sources
1    2026-05-12T10:13:58Z  tor-a3f-01     Ethernet1/7   link_up     2    link_up|tor-a3f-01|Ethernet1/7 syslog,trap
2    2026-05-12T10:14:00Z  tor-a3f-03     *48 ports     link_down   192  link_down|tor-a3f-03|*         counter,probe,syslog,trap
3    2026-05-12T10:14:02Z  tor-a3f-03     Ethernet1/1   admin_down  1    admin_down|tor-a3f-03|Ethernet1/1 syslog
4    2026-05-12T10:14:03Z  tor-a3f-01     Ethernet1/48  link_down   4    link_down|tor-a3f-01|Ethernet1/48 counter,probe,syslog,trap
5    2026-05-12T10:15:00Z  tor-a3f-01     Ethernet1/1   link_down   1    link_down|tor-a3f-01|Ethernet1/1 trap
6    2026-05-12T10:15:30Z  tor-a3f-01     Ethernet1/1   admin_down  1    admin_down|tor-a3f-01|Ethernet1/1 syslog
7    2026-05-12T10:16:00Z  tor-a3f-02     Ethernet1/5   link_down   3    link_down|tor-a3f-02|Ethernet1/5 syslog,trap
8    2026-05-12T10:17:00Z  tor-a3f-02     Gi1/0/99      link_down   1    link_down|tor-a3f-02|Gi1/0/99  syslog
9    2026-05-12T10:17:20Z  tor-a3f-07     Ethernet1/3   link_down   1    link_down|tor-a3f-07|Ethernet1/3 trap
10   2026-05-12T10:19:00Z  tor-a3f-04     Ethernet1/1   link_down   2    link_down|tor-a3f-04|Ethernet1/1 syslog,trap
11   2026-05-12T10:20:10Z  tor-a3f-01     Ethernet1/48  link_down   1    link_down|tor-a3f-01|Ethernet1/48 trap
12   2026-05-12T10:26:00Z  tor-a3f-02     Ethernet1/5   link_down   2    link_down|tor-a3f-02|Ethernet1/5 counter,probe

原始 211 条 → 端口级 59 条 → 输出 12 条（端口 11 + 设备级 1），归一化失败 2 条，乱序 6 条
原始条数落到事件上的 211 条，压缩比 17.6:1

提示清单 5 条（device_unknown 1 / kind_conflict 2 / out_of_order 1 / port_alias_unknown 1）：
  [warn    ] device_unknown      trap 报的设备 "tor-a3f-07" 不在台账里，事件保留但归不到对象上
  [warn    ] kind_conflict       窗口内两个族：admin_down（2026-05-12T10:15:30Z，syslog）与 link_down（2026-05-12T10:15:00Z，trap），不合并——一个是故障一个是变更，合并错了统计就废了
  [warn    ] kind_conflict       窗口内两个族：admin_down（2026-05-12T10:14:02Z，syslog）与 link_down（2026-05-12T10:14:00Z，counter,probe,syslog,trap），不合并——一个是故障一个是变更，合并错了统计就废了
  [warn    ] port_alias_unknown  syslog 报的端口 "Gi1/0/99" 在 tor-a3f-02 的端口表里翻不出来，事件保留原文（别名表缺这一条）
  [info    ] out_of_order        到达顺序与时间顺序倒置 6 条，已按时间戳重排后再去重
```

第 2 行那条 `*48 ports` 是 192 条原始事件折出来的。`make demo` 的对照一（端口数下限抬到 64，不再折叠）会把同一批数据摊成 48 行：

```text
#    ts                    device         port          family      raw  fingerprint                    sources
1    2026-05-12T10:13:58Z  tor-a3f-01     Ethernet1/7   link_up     2    link_up|tor-a3f-01|Ethernet1/7 syslog,trap
2    2026-05-12T10:14:00Z  tor-a3f-03     Ethernet1/1   link_down   4    link_down|tor-a3f-03|Ethernet1/1 counter,probe,syslog,trap
3    2026-05-12T10:14:00Z  tor-a3f-03     Ethernet1/10  link_down   4    link_down|tor-a3f-03|Ethernet1/10 counter,probe,syslog,trap
…
50   2026-05-12T10:14:02Z  tor-a3f-03     Ethernet1/1   admin_down  1    admin_down|tor-a3f-03|Ethernet1/1 syslog
```

48 条告警指着同一台交换机的 48 个口。值班人会先骂监控，再一条条点掉——而真相是设备重启。折叠掉的就是这个。

## 一、拓扑融合：四个源，各管一段

| 来源 | 能给什么 | 给不了什么 | 在本实验里 |
|---|---|---|---|
| LLDP / CDP | 直连邻居、对端端口名 | 未开启的设备；聚合口的报法因厂商而异 | 出链路，置信度 **high** |
| STP | 本端端口在树里的角色 | 对端是谁 | 只校验：blocked 判冗余 |
| 路由邻居 | 三层邻接的对端 router-id | 物理连接 | 只校验：印证跨设备聚合、指出二层缺口 |
| MAC 表反推 | 「这个口后面有那台设备」 | 对端端口、链路属性 | 只补 LLDP 的空，置信度 **low**，打问号 |

裁决只有一条规则：**按置信度取高，不投票**。

三个源里两个说 A、一个说 B，票多的未必对。MAC 表会漂移、会泛洪、会单向学习；STP 只看得见树、看不见物理；LLDP 是唯一一个「对端亲口说出自己是谁」的源。按票数算，等于把最准的那个源稀释掉。

冲突不进结论，只进提示清单：

- **两端说法不一致**（`peer_mismatch`）：A 说连 B 的 5 口，B 说连 C 的 5 口。两条链路各有一侧证据，都留着，点名说清楚。
- **STP blocked**（`stp_blocked`）：LLDP 说这条链路在，STP 说这端 blocked。这是冗余路径，标 `redundant`，置信度不降。当异常报的话，每次上架双上联都会告警一次。
- **MAC 与 LLDP 打架**（`mac_conflict`）：LLDP 赢，MAC 进提示。样本里 `tor-a3f-02 Ethernet1/7` 学到 `spine-01` 的管理 MAC，真机上这要么是漂移，要么是 MAC 表溢出泛洪——后一种是故障前兆。
- **幽灵设备**（`device_unknown`）：LLDP 报了对端名字，台账里没有。链路保留，设备待建档。
- **端口名翻不出来**（`port_alias_unknown`）：整条断言丢弃。硬猜一个端口名，后面每条结论都建在猜的那个名字上。

### 聚合口：逻辑口与成员口都要留

两端对同一个 LAG 的报法经常不一致。思科报成员口，华为报 Eth-Trunk1。折叠之前，两条链路永远不会相遇：

```text
spine-01 Ethernet1/10 → tor-a3f-01 Eth-Trunk1    ← 思科侧报成员口，华为侧报逻辑口
spine-01 Ethernet1/11 → tor-a3f-01 Eth-Trunk1
tor-a3f-01 Eth-Trunk1 → spine-01 Port-Channel10  ← 华为报对端逻辑口

折叠后：
spine-01 Port-Channel10 ↔ tor-a3f-01 Eth-Trunk1   · kind=lag · 2 对成员口留底
```

折叠靠台账的 `lags` 表。台账里成员写错一个，整条逻辑链路当场消失，而且看不出来——只是链路总数少一条。

## 二、事件去重：同一件事一条事件

三步，顺序不能换。

**第一步，归一化。** 四个源对同一台设备、同一个端口有四种写法：

| 源 | 设备写法 | 端口写法 |
|---|---|---|
| trap | `TOR-A3F-01` | `Ethernet1/48` |
| syslog | `tor-a3f-01.room3.example.net` | `Eth1/48` |
| counter | `10.20.3.11`（带外 IP） | `48`（ifIndex） |
| probe | `tor-a3f-01` | `ethernet1/48` |

四种都归到 `tor-a3f-01` + `Ethernet1/48`。不归一化，连「这两条说的是同一个端口」都判断不出来。

**第二步，指纹去重。** 指纹字段只有三个：

| 字段 | 为什么进去 |
|---|---|
| 族 `family` | 换族就不是一件事（`link_down` 与 `admin_down` 不合并） |
| 设备规范名 | 用原始写法，四个源永远并不上 |
| 端口规范名 | 用 ifIndex，换块业务板就重排到别人头上 |

刻意不进去的四个：**时间戳**（进去就一条都並不上了）、**来源**（进去四源变四条）、**序号**（每条都不同）、**原文**（里面带时间和计数器值）。

时间窗 `-dedup-window`（默认 5m）**锚点固定，不滑动**。滑动锚点会把一条持续两小时的抖动压成一条事件，那是另一个极端：该报的没报。

**第三步，设备级聚合。** 一台交换机 48 个口同秒全 down，端口级事件是 48 条。对值班人来说是一件：设备重启、上联断、电源。同设备同族、落在 `-burst-window`（默认 60s）内、端口数 ≥ `-burst-min-ports`（默认 8）就折叠成一条设备级事件，带端口计数。

折叠的条件是端口数下限，不是全部。48 口里 3 个口 down 是三条独立故障，不折叠。

### 事件族的映射表

归一化的全部内容就是这张表：

| 来源 | 原生 kind | 族 |
|---|---|---|
| trap | `link_down` / `link_up` | `link_down` / `link_up` |
| syslog | `IF_DOWN_LINK_FAILURE` / `IF_UP` | `link_down` / `link_up` |
| syslog | `IF_DOWN_ADMIN_DISABLED` | `admin_down` |
| counter | `counter_reset` | `link_down` |
| probe | `probe_down` / `probe_up` | `link_down` / `link_up` |

`counter_reset` 与 `probe_down` 归到 `link_down` 是**策略**，不是事实。见坑列表第 10、11 条。

## 样本格式

### `samples/cmdb.json`

台账。所有归一化的唯一来源。

```json
{
  "name": "tor-a3f-01",
  "aliases": ["TOR-A3F-01", "tor-a3f-01.room3.example.net"],
  "mgmt_ips": ["10.20.3.11"],
  "mgmt_mac": "00:1b:21:bb:11:01",
  "router_id": "10.20.3.11",
  "vendor": "huawei",
  "ports": [{"name": "Ethernet1/48", "aliases": ["Eth1/48"], "if_index": 48}]
}
```

| 字段 | 用途 |
|---|---|
| `name` | 规范名，唯一标识。其余字段都是别人嘴里的叫法 |
| `aliases` / `mgmt_ips` | 设备名的别名表。带外 IP 必须收进来，counter 源就是按 IP 报的 |
| `mgmt_mac` | MAC 反推靠它把「学到某个 MAC」翻成「某台设备」 |
| `router_id` | 路由邻居只给 router-id |
| `ports[].aliases` | 端口名别名表。`Eth1/48` / `Gi1/0/48` / `48` 三种写法都要能落到同一行 |
| `ports[].if_index` | 只报编号的源靠它反查端口名 |
| `lags[]` | 聚合口与成员口。**聚合关系只认这张表**，端口自己不带 lag 字段 |

六台设备、69 个端口、两个聚合口。`acc-a3f-09` 故意不在台账里（幽灵设备），`tor-a3f-07` 也不在（已下架的僵尸设备）。

### `samples/lldp.jsonl`

一行一条**单端断言**：本端端口看到的一个邻居。17 行。

| 字段 | 含义 |
|---|---|
| `local_device` / `local_port` | 本端，`lldpRemTable` 的索引 |
| `remote_device` | `lldpRemSysName` |
| `remote_port_id` | `lldpRemPortId`，可能是名字、编号、甚至 MAC |
| `remote_port_desc` | `lldpRemPortDesc`，部分设备只报 ID 不报描述 |
| `time_mark` | `lldpRemTimeMark`，同代条目在 `msgInterval × txHold`（常见 30×4=120s）后整体失效 |

一行只代表一端的说法。两端都报、报的一致，才算双向确认。样本里埋了四类：只报编号的（`"48"`）、只报别名的（`"Eth1/6"`）、报对端逻辑口的（`"Eth-Trunk1"`）、翻不出来的（`"Eth1/11x"`）。

### `samples/stp.jsonl`、`samples/route.jsonl`、`samples/mac.jsonl`

```json
{"device":"spine-02","port":"Ethernet1/9","role":"alternate","state":"blocking","root_bridge":"spine-01"}
{"device":"tor-a3f-02","local_if":"Ethernet1/7","peer_router_id":"10.20.0.11","protocol":"ospf","state":"full"}
{"device":"tor-a3f-04","port":"Ethernet1/1","mac":"00:1B:21:AA:00:11","vlan":100}
```

STP 只给本端端口角色，给不出对端是谁。路由邻居只给 router-id，`local_if` 是 Loopback 的直接跳过。MAC 表里学到的是**管理 MAC**，不是数据 MAC。

### `samples/events.jsonl` 与 `samples/events-burst.jsonl`

四源共用一种信封，19 条 + 192 条。

```json
{"ts":"2026-05-12T10:14:03Z","source":"trap","device":"TOR-A3F-01","port":"Ethernet1/48","kind":"link_down","msg":"SNMP trap linkDown ifIndex 48","seq":8801}
```

| 字段 | 含义 |
|---|---|
| `source` | `trap` / `syslog` / `counter` / `probe` |
| `kind` | 源的原生 kind，靠上面那张映射表归族 |
| `msg` / `seq` | 原文与源侧序号。**两者都不进指纹** |
| `port` | 可能是端口名、别名、ifIndex 字符串 |

`events.jsonl` 是主样本，一堆陷阱：跨源重复投递、族冲突、超窗重复、别名缺失、设备缺失、乱序六条。`events-burst.jsonl` 是压力样本：一台交换机 48 口 × 4 源同秒全 down。

## 参数

| 参数 | 默认 | 含义 |
|---|---|---|
| `-dedup-window` | 5m | 同指纹合并的时间窗。锚点固定 |
| `-burst-window` | 60s | 设备级聚合窗口 |
| `-burst-min-ports` | 8 | 折叠成设备级事件所需的端口数下限 |
| `-mode` | all | `all` / `topology` / `events` |
| `-out` | out | 输出目录，给空串则不落盘 |

三个时间/数量参数都是策略，不是常量。不同设备档位可以给不同值：

```bash
make try   # 等价于 go run . -mode events -dedup-window 30s
```

窗口一压，本来并在一起的事件就裂开了——裂开几条，取决于你的重传间隔和采集周期。

## 这个实验故意留的坑

1. **别名表是单点故障，而且失败得很安静**。缺一条别名，事件保留原文（`Gi1/0/99`）、拓扑丢一条断言（`Eth1/11x`），两边都只多一行提示。数字看起来正常，少的是链路数。
2. **两端说法不一致时，代码不裁决**。样本里 `spine-01` 与 `tor-a3f-02` 对 5 口的说法不同，两条链路都留着，提示里点名。谁对谁错得现场核——代码判不出来，判错了比不判更糟。
3. **STP 的 blocked 是设计，不是故障**。双上联的备用线本来就该被挡住。当异常报，每次上架双上联的机器都会告警一次，两周后没人看这条规则了。
4. **MAC 反推只能给一端**。它不知道对端端口，所以链路带问号；这条链路的带宽、双工、错包一个都没有。`tor-a3f-04` 就是这台关了 LLDP 的老设备。
5. **MAC 与 LLDP 冲突时按 LLDP 保留**，但那条提示不该被无视。样本里 `tor-a3f-02 Ethernet1/7` 学到 `spine-01` 的管理 MAC，可能是漂移，也可能是 MAC 表溢出泛洪——后者是故障前兆。
6. **聚合口对齐靠台账**。一端报 `Eth-Trunk1`、一端报成员口，折叠之后才相遇。台账成员写错一个，整条逻辑链路消失。
7. **聚合链路的成员明细只到对端逻辑口一级**。样本里华为侧只报 `Eth-Trunk1`，代码不猜它是哪个成员口——猜错了，后面按成员口做的流量分析全错位。
8. **幽灵设备的端口名没法和台账对齐**，只能照抄 LLDP 报的原文。下游按端口名做的规则（端口别名、台账对账、定位查询）对它全部失效。
9. **路由邻居只做校验**。样本里 `tor-a3f-02 Ethernet1/7` 的 OSPF 邻居指向 `spine-01`，二层链路指向 `tor-a3f-01`——可能是子接口，可能是跨设备聚合，也可能采集错了。代码只提示，不裁决。
10. **计数器归零映射到 `link_down` 族**。这是策略：归零常是端口重协商的副产物，但也可能是 SNMP 引擎重启。样本里 `tor-a3f-02 Ethernet1/5` 在 10:26 的 counter + probe 合成了一条；只想要端口 down 的话，这个映射得改。
11. **探测失败映射到 `link_down` 族**。探测超时可能是对端关机、路由变了、防火墙丢包，不一定是本端口 down。合并是策略选择，代价是故障统计里混进一批非端口故障。
12. **指纹不带时间，所以重复和抖动分不开**。同一个口每 6 分钟 down 一次，是两条事件（样本里 `tor-a3f-01 Ethernet1/48` 就是这样）。想让抖动只报一条，得再加一层「同端口 N 次内收敛」，那是第 38 课的活。
13. **设备级聚合是贪心扫描**，从窗口最早的一条起算。窗口边界跨过时，一批可能被拆成两段。真实流处理用滑动窗口计数，这里没做。
14. **48 条折叠成 1 条之后，端口清单只剩前三个**。完整清单在 JSONL 的 `evidence` 里没有，要查得回原始事件流。生产上得另存一份明细。
15. **事件与轮询数据没有对账**。「Trap 说 down 而快照说 up」（已恢复）、「快照说 down 但没收到 Trap」（Trap 丢了）这两种差异，实验只留了冲突清单的结构，没写判定。

## 文件

| 文件 | 作用 |
|---|---|
| `model.go` | 台账与四份快照的数据结构、样本加载、别名归一化器（`Directory`） |
| `topology.go` | 四源融合、置信度裁决、错挂检测、聚合口折叠、MAC 反推 |
| `event.go` | 事件族映射、指纹定义、窗口去重、族冲突检测、设备级聚合 |
| `report.go` | 终端表格、汇总、提示清单、JSONL 落盘 |
| `main.go` | 命令行入口，`-mode` 编排两段 |
| `model_test.go` | 别名归一化（设备/端口/ifIndex/聚合）、脏台账拒收、样本加载 |
| `topology_test.go` | 不投票、聚合口对齐、STP 冗余、错挂、幽灵设备、别名救场、问号链路 |
| `event_test.go` | 指纹字段、四源合一、48 口折叠、窗口边界、族冲突、乱序按流统计 |
| `report_test.go` | 表格列、问号标记、warn 排在 info 前、JSONL 格式 |
| `samples/cmdb.json` | 台账：6 台设备、69 个端口、2 个聚合口，含幽灵与僵尸设备 |
| `samples/lldp.jsonl` | 17 条单端 LLDP 断言 |
| `samples/stp.jsonl` | 4 条端口角色（含一条 alternate/blocking） |
| `samples/route.jsonl` | 3 条三层邻接（含一条 Loopback） |
| `samples/mac.jsonl` | 4 条 MAC 表行 |
| `samples/events.jsonl` | 19 条四源事件，六条乱序 |
| `samples/events-burst.jsonl` | 192 条压力样本：48 口 × 4 源同秒全 down |

`make run` 落四个文件：

| 文件 | 内容 |
|---|---|
| `out/links.jsonl` | 每条链路一行：`id` / `a` / `b` / `kind` / `confidence` / `bidirectional` / `redundant` / `question` / `declared_from` / `sources` / `verified_by` / `members` / `evidence` |
| `out/topology-notices.jsonl` | 拓扑冲突与校验提示 |
| `out/events.jsonl` | 每条事件一行：`ts` / `last_ts` / `device` / `port` / `family` / `scope` / `port_count` / `raw_count` / `sources` / `kinds` / `fingerprint` / `evidence` |
| `out/event-notices.jsonl` | 事件冲突与乱序提示 |
