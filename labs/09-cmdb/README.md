# 09-cmdb · 脏台账对账

第 23–26、28 课配套实验。三份快照进去，三类差异出来：**僵尸**（台账在役、现网没有）、**幽灵**（现网在、台账没有）、**错挂**（台账与 LLDP 观测不符）。每一条差异带置信度和纠正建议，最后汇成一份「建议写入」的补丁。

补丁不落库。默认 dry-run，加 `-apply -confirm` 也只写到副本上；台账文件在任何路径下都不被改写。

零外部依赖，不连网、不连设备。

## 跑起来

```bash
cd labs/09-cmdb
make run     # 对账一遍：三类差异 + 身份问题 + 建议补丁，台账不动
make test    # go vet + go test
make demo    # 闭环：出差异单 → 补丁应用到副本 → 对副本再对一次账
make apply   # 落库（写副本 out/cmdb.applied.json），要 -confirm
make guard   # 故意把台账本身当写库目标，看那把锁怎么拦
make clean   # 删 out/
```

`make guard` 会失败，这是它的用途。

## 期望输出

`make run`：

```text
台账 10 条设备记录 / 13 条端口声明；LLDP 15 个端口；ARP 8 条
LLDP 采集于 2026-05-12T09:12:00Z，ARP 采集于 2026-05-12T09:13:00Z（网关 border-01）

== 僵尸设备（台账在役，现网无痕迹） · 3 条 ==
id   target                   conf   依据 / 建议
Z1   agg-core-01              high   LLDP 快照里没有 agg-core-01 的本端端口，也没有任何设备把它报成邻居
                                     ARP 里 10.20.0.51 的 MAC 是 00:1b:21:aa:99:51，台账登记的是 00:1b:21:aa:00:51——这个地址已经被别的设备占用
                                     建议：标记 lifecycle=retired，记录保留（retire）
Z2   tor-3f-07                high   LLDP 快照里没有 tor-3f-07 的本端端口，也没有任何设备把它报成邻居
                                     ARP 表里没有 10.20.3.17，这个管理地址上没有人应答
                                     建议：标记 lifecycle=retired，记录保留（retire）
Z3   tor-a3f-06               medium LLDP 快照里没有 tor-a3f-06 的本端端口，也没有任何设备把它报成邻居
                                     ARP 表里没有 10.20.3.16，这个管理地址上没有人应答
                                     台账里还有 1 条端口声明指向它（spine-02#Ethernet1/8），retire 之前先清掉，否则拓扑里留一个悬空对端
                                     建议：先删掉指向它的端口声明，再标记 retired（retire_after_unlink）

== 幽灵设备（现网在，台账没有） · 2 条 ==
id   target                   conf   依据 / 建议
G1   tor-a3f-04               high   tor-a3f-04 在 LLDP 里自己上报了 1 个端口
                                     被 spine-01#Ethernet1/9 报成 LLDP 邻居
                                     ARP 里 10.20.3.14 的 MAC 是 00:1b:21:aa:03:14，管理面活着
                                     台账里没有这台设备的任何记录
                                     现网观测到的连线：spine-01#Ethernet1/9 <-> tor-a3f-04#Ethernet1/49
                                     建议：在台账登记这台设备，补齐机架与责任人（register）
G2   tor-a3f-05               medium tor-a3f-05 在 LLDP 里自己上报了 1 个端口
                                     LLDP 没带管理地址（management address TLV 没开），ARP 无从核对
                                     台账里没有这台设备的任何记录
                                     建议：先补规格信息（序列号/型号/机架）再登记（register_verify）

== 错挂链路（台账与 LLDP 观测不符） · 4 条 ==
id   target                   conf   依据 / 建议
M1   spine-01#Ethernet1/5     high   台账登记 spine-01#Ethernet1/5 -> tor-a3f-01#Ethernet1/6（2024-11-18 登记）
                                     LLDP 观测 spine-01#Ethernet1/5 连的是 tor-a3f-01#Ethernet1/1
                                     对端 tor-a3f-01#Ethernet1/1 也观测到 spine-01#Ethernet1/5，双向确认
                                     台账里 tor-a3f-01#Ethernet1/1 登记的对端也是 spine-01#Ethernet1/5，只有本端这一条写错
                                     建议：改写这条声明里的对端设备与端口（fix_peer）
M2   spine-02#Ethernet1/7     high   台账登记 spine-02#Ethernet1/7 -> tor-a3f-03#Ethernet1/1（2025-01-20 登记）
                                     LLDP 观测 spine-02#Ethernet1/7 连的是 tor-a3f-03#Ethernet1/49
                                     对端 tor-a3f-03#Ethernet1/49 也观测到 spine-02#Ethernet1/7，双向确认
                                     台账里没有 tor-a3f-03#Ethernet1/49 这条声明，这根线只有一端登记过
                                     建议：改写这条声明里的对端设备与端口（fix_peer）
M3   tor-a3f-02#Ethernet1/2   medium 台账登记 tor-a3f-02#Ethernet1/2 -> spine-02#Ethernet1/7（2026-03-02 登记）
                                     LLDP 观测 tor-a3f-02#Ethernet1/2 连的是 spine-02#Ethernet1/6
                                     台账里没有 spine-02#Ethernet1/6 这条声明，这根线只有一端登记过
                                     建议：改写这条声明里的对端设备与端口（fix_peer）
M4   tor-a3f-03#Ethernet1/1   high   台账登记 tor-a3f-03#Ethernet1/1 <-> spine-02#Ethernet1/7，两端写法一致
                                     LLDP 里 tor-a3f-03#Ethernet1/1 根本没有端口记录，而 spine-02#Ethernet1/7 观测到的是 tor-a3f-03#Ethernet1/49
                                     台账的端口名停在搬迁之前，现网这台设备的端口是 Ethernet1/49
                                     建议：这条声明的本端端口名登记错了，按现网改写（fix_local_port）

== 身份问题（改名 / 端口重排） · 3 条 ==
id   target                   conf   依据 / 建议
R1   tor-3f-02                high   serial FDO2312C12 与 tor-a3f-02 完全相同，是同一台物理设备的两条记录
                                     LLDP 里只有 tor-a3f-02 上报端口，tor-3f-02 在现网没有任何痕迹——设备已经按新名在跑
                                     两条记录的 mgmt_ip 都是 10.20.3.12，改名时没做软删，IP 也留在了旧记录上
                                     旧记录还占着机架 A3F-02 的位置，按机架统计 U 位会重复计一次
                                     建议：旧名标记 retired 并写 alias_of，不删记录（alias）
RI1  tor-a3f-02#Ethernet1/1   high   台账登记 if_index=1，LLDP 报的是 51（端口名没变）
                                     主键用 device#port，ifIndex 只在取数时当定位线索
                                     tor-a3f-02 上有 2 个端口同时换了 ifIndex，整板重排的特征
                                     建议：把台账 if_index 更新为现网值（主键仍是 device#port）（update_if_index）
RI2  tor-a3f-02#Ethernet1/2   high   台账登记 if_index=2，LLDP 报的是 52（端口名没变）
                                     主键用 device#port，ifIndex 只在取数时当定位线索
                                     tor-a3f-02 上有 2 个端口同时换了 ifIndex，整板重排的特征
                                     建议：把台账 if_index 更新为现网值（主键仍是 device#port）（update_if_index）

已归并的旧名 1 条，僵尸判定按主名做：
  tor-3f-02 -> tor-a3f-02

== 建议写入的补丁 · 18 条（dry-run，未落库） ==
P01  device_lifecycle   agg-core-01                high    lifecycle=retired
P02  device_lifecycle   tor-3f-07                  high    lifecycle=retired
P03  device_lifecycle   tor-a3f-06                 medium  lifecycle=retired
P04  link_delete        spine-02#Ethernet1/8       medium  （删掉这条声明）
P05  link_delete        tor-a3f-06#Ethernet1/1     medium  （删掉这条声明）
P06  device_create      tor-a3f-04                 high    hostname=tor-a3f-04 lifecycle=staging mgmt_ip=10.20.3.14
P07  link_create        spine-01#Ethernet1/9       high    if_index=9 peer_device=tor-a3f-04 peer_port=Ethernet1/49
P08  link_create        tor-a3f-04#Ethernet1/49    high    if_index=49 peer_device=spine-01 peer_port=Ethernet1/9
P09  device_create      tor-a3f-05                 medium  hostname=tor-a3f-05 lifecycle=staging
P10  link_peer          spine-01#Ethernet1/5       high    peer_device=tor-a3f-01 peer_port=Ethernet1/1
P11  link_peer          spine-02#Ethernet1/7       high    peer_device=tor-a3f-03 peer_port=Ethernet1/49
P12  link_peer          tor-a3f-02#Ethernet1/2     medium  peer_device=spine-02 peer_port=Ethernet1/6
P13  link_local_port    tor-a3f-03#Ethernet1/1     high    port=Ethernet1/49
P14  link_if_index      tor-a3f-03#Ethernet1/49    high    if_index=49
P15  device_alias       tor-3f-02                  high    alias_of=tor-a3f-02
P16  device_lifecycle   tor-3f-02                  high    lifecycle=retired
P17  link_if_index      tor-a3f-02#Ethernet1/1     high    if_index=51
P18  link_if_index      tor-a3f-02#Ethernet1/2     high    if_index=52

产物：out/diffs.jsonl、out/cmdb-patch.jsonl
台账 samples/cmdb.json 未被改写（sha256 5a275b7be30c）
```

`make demo` 的后半段是这套补丁的验收标准——应用完再对一次账，差异必须是零：

```text
===== 应用补丁到副本 out/cmdb.applied.json =====
台账副本已写入 out/cmdb.applied.json：12 条设备记录、13 条端口声明（原台账未动）
台账 samples/cmdb.json 未被改写（sha256 5a275b7be30c）

===== 对副本再对一次账 =====
台账 12 条设备记录 / 13 条端口声明；LLDP 15 个端口；ARP 8 条
LLDP 采集于 2026-05-12T09:12:00Z，ARP 采集于 2026-05-12T09:13:00Z（网关 border-01）

== 僵尸设备（台账在役，现网无痕迹） · 0 条 ==
无

== 幽灵设备（现网在，台账没有） · 0 条 ==
无

== 错挂链路（台账与 LLDP 观测不符） · 0 条 ==
无

== 身份问题（改名 / 端口重排） · 0 条 ==
无

已归并的旧名 1 条，僵尸判定按主名做：
  tor-3f-02 -> tor-a3f-02

== 建议写入的补丁 · 0 条（dry-run，未落库） ==
无
台账 out/cmdb.applied.json 未被改写（sha256 1c6a58341a3b）
```

三个数：进去 10 条设备记录，18 条补丁，出来 12 条设备记录、零差异。18 条里只删了 2 条端口声明（一台僵尸下联的一根线，两端各一条），**一条设备记录都没删**——退服走 `lifecycle`，不删记录。

## 三类差异怎么判

### 僵尸：LLDP 沉默 + 管理面沉默

判据两条同时成立：

1. LLDP 快照里既没有它自己上报的本端端口，也没有任何设备把它报成邻居；
2. ARP 表里它的管理地址不存在，或者这个地址的 MAC 与台账不符。

第 2 条只管一件事：把「设备没了」和「LLDP 没采到」分开。ARP 里 IP 与 MAC 都对得上，说明管理面活着，设备大概率在跑，只是 LLDP 没覆盖到那个网段——这种进 `lldp_blind` 计数，不进僵尸清单。

置信度看台账自己：没有任何端口声明指向它，`high`，建议 `retire`；还有声明指着它，`medium`，建议先清引用再 retire。台账外面挂着一堆外键，删一个对象之前先查谁引用它。

### 幽灵：三个来源各算一分

LLDP 里出现、台账里没有的设备，三个独立来源各算一分：

| 来源 | 含义 |
|---|---|
| 自己上报了端口 | 设备开着 LLDP，报了本端端口与 ifIndex |
| 被别人报成邻居 | 至少一台已经纳管的设备看到了它 |
| ARP 命中其管理地址 | LLDP 的 management address TLV 给了 IP，ARP 里查得到 |

两分以上 `high`，建议 `register`，补丁里带管理地址与两端链路。一分 `medium`，建议 `register_verify`——只在一个来源里出现的设备，型号、序列号、机架全是空的，直接登记进台账就是三条空字段加一个错机架。

### 错挂：按端口声明逐条对

台账的端口声明是各设备自报的，一根线两条记录。对账时逐条比「本端 LLDP 邻居」和「台账写的对端」：

| 情况 | 结论 | 置信度 |
|---|---|---|
| 本端口 LLDP 有邻居，与台账不符 | 对端写错，`fix_peer` | 对端 LLDP 也看到本端口 → `high`；只有单侧观测 → `medium` |
| 台账两端写得一致，但对端端口看到的不是本端口 | 本端端口名写错，`fix_local_port` | `high` |
| 台账两端互相矛盾，现网两个端口都没观测 | 只有台账内部的矛盾 | `low` |
| 本端口没有 LLDP 记录，对端登记的就是本端口 | 一致，不报 | — |
| 本端口没有 LLDP 记录，对端也没登记 | 不报 | — |

最后一行是关键。`spine-02#Ethernet1/8` 台账写着连 `tor-a3f-06`，现网 LLDP 里彻底没有邻居。把它当成错挂，一台僵尸设备会连带刷出十几条假差异。**「没有观测」不构成证伪**，只构成沉默。

### 身份问题：先于三类差异跑

两件事必须在僵尸与错挂之前处理完，否则差异单从第一行就是错的。

**改名**：按 serial 分组，组内只有一条记录的名字出现在 LLDP 里，其余的都是改名前的残留。建议不是删记录，是 `lifecycle=retired` + `alias_of`——历史指标、历史告警、历史工单都还指着旧名，删掉记录等于制造一批指向空气的外键。

**端口重排**：端口名没变、`if_index` 变了。换一块业务板就会发生。台账里的 `if_index` 只是登记值，补丁把它改成现网值；主键从头到尾都是 `device#port`。

## 为什么对账不自动改写台账

对账的输出是判断，不是事实。四条理由：

**一、同一份证据支撑两种修法**。`tor-a3f-03#Ethernet1/1` 这条声明，实际设备上的端口叫 `Ethernet1/49`。可以改本端端口名，也可以删掉这条声明重新登记。两条路都能让差异消失，代价不同：前者保留 `declared_at` 的历史，后者干净但丢一次变更痕迹。机器选一条，就是在赌。

**二、误判的成本不对称**。漏报一条僵尸，多一条噪声告警，值班的人骂一句。误报一次下架，一台在跑的机器被移出采集，三个月后出了故障没人看得见。方向上偏保守才是对的。

**三、台账是主数据，改写要连带处理一堆外键**。指标平台的曲线、告警规则的对象选择器、工单系统的设备字段、资产折旧、机架 U 位统计，全挂在 `hostname` 上。一次自动改写要同时处理这些，出问题的地方在别处，报错在这一处。

**四、LLDP 覆盖率不保证**。这套判据的前提是「LLDP 沉默 ≈ 设备不在」。覆盖率 60% 的环境里，这个前提不成立，自动改写会成批下架活设备。覆盖率这件事得人先核。

代码里的落法有三层：

```text
1. 默认 dry-run：差异单与补丁写文件，台账文件连打开都不打开（写完复核 sha256）
2. -apply 必须配 -confirm，否则退出码 2，一个字节都不写
3. 写库路径不允许等于台账路径（filepath.Abs + Clean 比较），绕 ../ 也拦
```

`make guard` 打的就是第 3 层：

```text
拒绝写库: 拒绝写入 samples/cmdb.json：这是台账本身。补丁只能写到副本，落库走人工确认
```

真正的落库要走人工评审、变更窗口、备份，一步不能少。实验只到「生成一份可以直接 diff 的副本」这一步，剩下的交给流程——这也是第 28 课那道判据：哪些表有外键指向 CMDB 对象，对象被删除或改名时这些表怎么处理。

## 样本格式

### `samples/cmdb.json` — 台账

10 条设备记录、13 条端口声明。

```json
{
  "generated_at": "2026-05-12T08:00:00Z",
  "devices": [
    {"hostname": "tor-a3f-02", "mgmt_ip": "10.20.3.12", "mgmt_mac": "00:1b:21:aa:03:12",
     "serial": "FDO2312C12", "model": "N9K-C93180YC", "site": "idc-a", "rack": "A3F-02",
     "lifecycle": "active", "owner": "计算一组"}
  ],
  "links": [
    {"device": "tor-a3f-02", "port": "Ethernet1/1", "if_index": 1,
     "peer_device": "spine-01", "peer_port": "Ethernet1/6", "declared_at": "2026-03-02"}
  ]
}
```

| 字段 | 含义 |
|---|---|
| `hostname` | 设备名。台账唯一，但不是物理唯一——改名会留下两条 |
| `mgmt_ip` / `mgmt_mac` | 管理地址与 MAC。判僵尸靠这一对，只对 IP 会漏 |
| `serial` | 物理唯一键。合并改名记录只用它 |
| `lifecycle` | `active` / `staging` / `retired`。`staging` 与 `retired` 不参与僵尸判定 |
| `rack` / `owner` | 机架与责任人。缺这两项的新记录等于三条空字段 |
| `alias_of` | 旧名指向现名。样本里没有，`make apply` 之后副本里才会出现 |
| 端口声明 | `device`+`port` 是本端，`peer_device`+`peer_port` 是台账写的对端。**一个端口一条，两端各写一条** |

台账是各设备自报的端口表，不是全局链路表。这个设计决定了它是脏的：两端都写对才自洽，写岔了台账自己没有裁决能力——第 25 课「链路是对象不是属性」的反面教材。

### `samples/lldp.json` — LLDP 快照

15 个端口。每台开了 LLDP 的设备，配上它看到的邻居。

```json
{"device": "spine-01", "port": "Ethernet1/9", "if_index": 9, "mgmt_ip": "10.20.0.11",
 "neighbors": [{"device": "tor-a3f-04", "port": "Ethernet1/49", "mgmt_ip": "10.20.3.14"}]}
```

| 字段 | 含义 |
|---|---|
| `if_index` | LLDP 本地端口号，多数实现直接是 ifIndex。换板就重排 |
| `mgmt_ip` | 来自 management address TLV。没开这个 TLV 就是空串，ARP 无从核对 |
| `neighbors` | 端口上看到的邻居。空数组表示没有邻居（对端没开 LLDP，或者对端口下电了） |

一个端口理论上只该看到一个邻居。接未受管交换机或 hub 时会看到多个，代码取第一条。

### `samples/arp.json` — 网关 ARP 表

8 条，全部来自 `border-01`。

```json
{"ip": "10.20.0.51", "mac": "00:1b:21:aa:99:51", "vlan": 100}
```

只干两件事：证明这个管理地址还有人应答，以及证明这个地址上的人换了。

## 这个实验故意留的坑

1. **只看 IP 判存活，漏掉一台僵尸**。`agg-core-01` 的 `10.20.0.51` 还在 ARP 表里，MAC 换成了 `00:1b:21:aa:99:51`。地址被新设备抢了。判据里去掉 MAC 比对，这台机器永远不会被报出来。
2. **改名会变成「一台僵尸 + 一台幽灵」**。`tor-3f-02` 改名 `tor-a3f-02`，旧记录没删。按 `hostname` 合并，同一台机器变成两条差异，而且一条指向「已经下架」、一条指向「从未登记」。合并键只能是 serial，而且合并必须跑在三类差异之前。
3. **LLDP 沉默不等于设备没了**。沉默还可能只是没开 LLDP、或者采集器没覆盖那个网段。判僵尸之前先看管理面（ARP + MAC），这类单列 `lldp_blind` 计数。落地第一件事是核 LLDP 覆盖率。
4. **下电设备的下联口不该报错挂**。`spine-02#Ethernet1/8` 台账写着连 `tor-a3f-06`，现网 LLDP 里彻底没有邻居。当成错挂，一台僵尸连带刷出十几条假差异。
5. **端口名登记错，改对端是错的**。`tor-a3f-03#Ethernet1/1` 的实际端口名叫 `Ethernet1/49`。逐条改对端端口号，改完之后台账描述的是两个不存在的连接。这里要改本端端口名，代码里是 `link_local_port`，和 `link_peer` 是两个 op。
6. **ifIndex 重排之后端口名没变**。`tor-a3f-02` 换了块板，`Ethernet1/1` 的索引从 1 变成 51。按 `ifIndex` 建主键，新端口的历史会接在旧端口上，曲线看着完全正常。第 16 课那个坑的现网版本。
7. **serial 为空的记录不能合并**。真实台账里总有一批设备的序列号是空串、`N/A`、`—`。把这些当同一个 serial 分组，一晚上能把半个机房的设备合并成一台。
8. **僵尸的「引用检查」不是洁癖**。`tor-a3f-06` 下架了，台账里 `spine-02#Ethernet1/8` 还声称连着它。只改 `lifecycle`，拓扑里就留一个悬空对端，下一课画图的时候它会被画成一个悬空的节点。
9. **幽灵的置信度靠来源数，不分权重**。三分制（自报 / 被邻居看到 / ARP 命中）只在 LLDP 覆盖率高的环境里准。覆盖一半的环境，`medium` 会占大头，全交给人工确认，这一类差异就没有价值了。
10. **补丁只写目标状态，不写改前值**。改前值在差异单的 evidence 里，落库以当时台账为准。要写改前值，就得处理「两轮补丁之间台账变了」的冲突——那是另一个实验的活。
11. **`-apply -confirm` 之后台账还是没变**。这看起来像 bug，是设计：这个实验只到「生成副本」这一步。真落库要人工评审之后另跑一次，带着备份和变更单。

## 文件

| 文件 | 作用 |
|---|---|
| `main.go` | 命令行入口与流程编排，`run()` 拆出来给测试跑两条完整路径 |
| `model.go` | 三份快照的结构、加载、台账结构校验 |
| `reconcile.go` | 身份归并、三类差异判定、置信度、纠正建议、补丁生成 |
| `apply.go` | 补丁应用（字段白名单）、写库路径护栏、台账落盘 |
| `report.go` | 终端差异单、补丁清单、JSONL 落盘 |
| `model_test.go` | 主键与加载、台账校验、样本规模 |
| `reconcile_test.go` | 样本上的三类差异条目与置信度、对照组不误报、判据边界 |
| `apply_test.go` | 补丁收敛性、白名单、路径护栏、dry-run 台账字节不变 |
| `report_test.go` | 表格对齐、空段落、JSONL 往返 |
| `samples/cmdb.json` | 脏台账：10 条设备记录、13 条端口声明 |
| `samples/lldp.json` | 15 个端口的 LLDP 快照 |
| `samples/arp.json` | 8 条网关 ARP 记录 |
| `out/diffs.jsonl` | 差异单，一行一条（`make run` 生成，不进 git） |
| `out/cmdb-patch.jsonl` | 建议写入的补丁（`make run` 生成） |
| `out/cmdb.applied.json` | 应用补丁后的台账副本（`make apply` 生成） |

`out/diffs.jsonl` 一行一条：

```json
{"id":"M1","kind":"mismatch","kind_zh":"错挂链路","target":"spine-01#Ethernet1/5","confidence":"high","evidence":["台账登记 spine-01#Ethernet1/5 -> tor-a3f-01#Ethernet1/6（2024-11-18 登记）","LLDP 观测 spine-01#Ethernet1/5 连的是 tor-a3f-01#Ethernet1/1","对端 tor-a3f-01#Ethernet1/1 也观测到 spine-01#Ethernet1/5，双向确认","台账里 tor-a3f-01#Ethernet1/1 登记的对端也是 spine-01#Ethernet1/5，只有本端这一条写错"],"action":"fix_peer","action_zh":"改写这条声明里的对端设备与端口","fix":{"peer_device":"tor-a3f-01","peer_port":"Ethernet1/1"}}
```

`out/cmdb-patch.jsonl` 一行一条：

```json
{"id":"P10","diff_id":"M1","op":"link_peer","target":"spine-01#Ethernet1/5","set":{"peer_device":"tor-a3f-01","peer_port":"Ethernet1/1"},"reason":"改写这条声明里的对端设备与端口","confidence":"high"}
```

补丁的 `op` 与允许写的字段：

| op | 允许的字段 | 干什么 |
|---|---|---|
| `device_lifecycle` | `lifecycle` | 改生命周期，退服走这里 |
| `device_alias` | `alias_of` | 旧名指向现名 |
| `device_create` | `hostname` `mgmt_ip` `lifecycle` `site` `owner` | 新建设备记录 |
| `link_peer` | `peer_device` `peer_port` | 改对端 |
| `link_local_port` | `port` | 改本端端口名 |
| `link_if_index` | `if_index` | 改索引 |
| `link_create` | `peer_device` `peer_port` `if_index` | 新建端口声明 |
| `link_delete` | — | 删掉一条端口声明 |

白名单以外的字段一律报错。静默忽略一个字段，比报错危险得多：人以为改过了。
