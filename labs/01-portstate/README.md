# 01-portstate · 端口 down 的六成因判定

第 04–07 课的配套实验。喂进一份录好的 `ifTable` 快照和一段 Trap 序列，输出每个端口**为什么 down**、这条 down 该记在**谁头上**（变更 / 硬件 / 配置 / 故障）。

`ifAdminStatus=2`（人手工 shutdown）和 `ifOperStatus=2`（链路断了）在报表上都是「down」，含义完全不同。前者是变更，后者是故障。这个实验的全部价值就在这里：把两类分开，再往下分出六种成因。

## 跑起来

```bash
cd labs/01-portstate
make run     # 判定 samples/ 里的快照，结果打到终端
make test    # vet + 单元测试
make demo    # 多打一段归属汇总、抖动清单、设备重启
make clean   # 删 out/
```

零外部依赖，不连网、不连设备。

## 期望输出

`make run`：

```text
20 个端口，19 条 Trap，抖动口径：10m0s 内 4 次翻转

device       port           admin  oper             cause             owner       flips evidence ｜ 下一步
spine-01     Ethernet1/1    up     up               one_way           fault       0     本端 ifOperStatus=up 而对端 tor-a3f-01 Ethernet1/48 ifOperStatus=down（对端收不到本端的光）｜下一步：查光纤与两端模块收发，重点看收光低的那一端
spine-01     Ethernet1/2    up     up               link_ok           -           0     两端 ifOperStatus 均 up
spine-01     Ethernet1/9    up     up               link_ok           -           0     两端 ifOperStatus 均 up
spine-01     Ethernet1/10   up     up               one_way           fault       0     两端 ifOperStatus 均 up，但收光差 13.6 dB 超过 10 dB 门限（本端 -1.8 dBm / 对端 spine-02 Ethernet1/10 -15.4 dBm），低的那端是 spine-02 Ethernet1/10｜下一步：查光纤与两端模块收发，重点看收光低的那一端
spine-02     Ethernet1/9    up     up               link_ok           -           0     两端 ifOperStatus 均 up
spine-02     Ethernet1/10   up     up               one_way           fault       0     两端 ifOperStatus 均 up，但收光差 13.6 dB 超过 10 dB 门限（本端 -15.4 dBm / 对端 spine-01 Ethernet1/10 -1.8 dBm），低的那端是 spine-02 Ethernet1/10｜下一步：查光纤与两端模块收发，重点看收光低的那一端
spine-02     Ethernet1/11   up     lowerLayerDown   unknown_down      fault       0     两端均非 up（本端 lowerLayerDown，对端 down），模块在位、光功率与速率正常，需现场查光纤配线｜下一步：现场查光纤配线，并补采对端
tor-a3f-01   Ethernet1/1    down   down             self_admin_down   change      1     ifAdminStatus=2（本端手工 shutdown）｜下一步：对变更单窗口，确认是否计划内
tor-a3f-01   Ethernet1/2    up     down             peer_admin_down   change      0     对端 tor-a3f-02 Ethernet1/2 ifAdminStatus=2（对端手工 shutdown）｜下一步：找对端设备的变更单，两端一起核
tor-a3f-01   Ethernet1/3    up     notPresent       optic_missing     hardware    0     光模块不在位（present=false）｜下一步：安排现场插拔或更换模块
tor-a3f-01   Ethernet1/4    up     down             optic_missing     hardware    0     光模块在位但型号字段为空（未识别）｜下一步：安排现场插拔或更换模块
tor-a3f-01   Ethernet1/9    up     up               link_ok           -           6*    两端 ifOperStatus 均 up
tor-a3f-01   Ethernet1/48   up     down             optic_power       hardware    1     收光 -40.0 dBm 低于告警门限 -14.0 dBm｜下一步：查光纤与两端收发，同型号模块做基线比对
tor-a3f-02   Ethernet1/2    down   down             self_admin_down   change      1     ifAdminStatus=2（本端手工 shutdown）｜下一步：对变更单窗口，确认是否计划内
tor-a3f-02   Ethernet1/5    up     down             optic_power       hardware    1     收光 -40.0 dBm 低于告警门限 -14.0 dBm｜下一步：查光纤与两端收发，同型号模块做基线比对
tor-a3f-02   Ethernet1/6    up     down             optic_power       hardware    1     收光 -15.8 dBm 低于告警门限 -14.0 dBm｜下一步：查光纤与两端收发，同型号模块做基线比对
tor-a3f-02   Ethernet1/7    up     down             negotiate_fail    config      1     ifSpeed=100 Mbps 与预期 1000 Mbps 不符，dot3StatsSymbolErrors 本周期 +11460（阈值 500）｜下一步：改端口速率与双工配置，回滚后复测
tor-a3f-02   Ethernet1/9    up     up               link_ok           -           1     两端 ifOperStatus 均 up
tor-a3f-02   Ethernet1/46   up     down             unknown_down      fault       0     两端均非 up（本端 down，对端 lowerLayerDown），模块在位、光功率与速率正常，需现场查光纤配线｜下一步：现场查光纤配线，并补采对端
tor-a3f-02   Ethernet1/48   up     up               link_ok           -           0     两端 ifOperStatus 均 up

判定结果已写入 out/verdicts.jsonl
```

`make demo` 在表格后面再加三段：

```text
归属合计：变更 3 / 硬件 5 / 配置 1 / 故障 5 / 无异常 6
成因分布：link_ok 6 / negotiate_fail 1 / one_way 3 / optic_missing 2 / optic_power 3 / peer_admin_down 1 / self_admin_down 2 / unknown_down 2
抖动：tor-a3f-01 Ethernet1/9 窗口内 6 次翻转（≥ 阈值），判 flapping
设备重启：tor-a3f-02 @2026-05-12T10:03:05Z（sysUpTime 12.0s），该设备冷启动期 6 条 Trap 已剔除
共剔除 6 条冷启动期 Trap（仍在 sysUpTime 120s 以内）
```

`flips` 列后面带 `*` 就是 flapping。抖动只做标记，**不改归属**——六成因才是归属的来源。

## 判定树

顺序不能随便调。人为变更是外部可查的事实，物理层要花钱上站，配置错了改一行就回滚。成本低的解释排在前面。

```text
1. ifAdminStatus=2                → 本端 admin shutdown · 变更
2. 对端 ifAdminStatus=2           → 对端 admin shutdown · 变更
3. 光模块不在位 / 型号为空        → 硬件
4. 收光缺失或低于 rx_low_alarm    → 光功率异常 · 硬件
5. ifSpeed 与台账不符
   且 dot3StatsSymbolErrors 增量 ≥ 500
                                  → 协商失败 · 配置
6. 对端 up 而本端非 up（或反过来）→ 单向链路 · 故障
   两端都 up 但收光差 > 10 dB     → 单向链路 · 故障
7. 两端都非 up、设备侧无异常      → 未归因 · 故障（现场查配线）
```

第 1 步压过第 3、4 步。人手工 shutdown 的端口一定没有收光，先查物理层就会把一次计划割接判成光模块故障，每周的故障数里混进一批变更单。

第 6 步有两副面孔。状态对不上是一副，两端 up 但收光差 13.6 dB 是另一副——链路层一点异常都没有，切流量也正常，唯一的信号是这一侧的收光在往下掉。第二副判据的前提是两端模块同型号，见坑列表第 3 条。

| cause | 中文 | 归属 | 样本里命中它的端口 |
|---|---|---|---|
| `self_admin_down` | 本端 admin shutdown | change | `tor-a3f-01 Ethernet1/1`、`tor-a3f-02 Ethernet1/2` |
| `peer_admin_down` | 对端 admin shutdown | change | `tor-a3f-01 Ethernet1/2` |
| `optic_missing` | 光模块不在位/未识别 | hardware | `tor-a3f-01 Ethernet1/3`（不在位）、`Ethernet1/4`（型号空） |
| `optic_power` | 光功率异常 | hardware | `tor-a3f-01 Ethernet1/48`、`tor-a3f-02 Ethernet1/5`、`Ethernet1/6` |
| `negotiate_fail` | 协商失败（速率/双工） | config | `tor-a3f-02 Ethernet1/7` |
| `one_way` | 单向链路 | fault | `spine-01 Ethernet1/1`（状态对不上）、`spine-01 Ethernet1/10` 与 `spine-02 Ethernet1/10`（收光差 13.6 dB） |
| `link_ok` | 无异常 | — | 6 个端口 |
| `unknown_down` | 未归因 | fault | `tor-a3f-02 Ethernet1/46`、`spine-02 Ethernet1/11` |

每条判定输出三元组：成因、归属、下一步动作。归属回答「谁」，`next_step` 回答「做什么」，两个拼起来才派得动工。表格里动作拼在判据后面，JSONL 里是独立字段。

## 样本格式

### `samples/portstate.json`

一个 JSON 数组，一行一个端口状态，代表 `2026-05-12T10:12:00Z` 那一刻的快照。20 个端口，四台设备。

| 字段 | 含义 |
|---|---|
| `device` / `if_name` | 端口主键。不用 `ifIndex`，换块业务板它会重排 |
| `if_index` | 端口索引，用来把 Trap 映射回端口名 |
| `if_admin_status` | 1=up，2=down |
| `if_oper_status` | 1=up，2=down，3=testing，4=unknown，5=dormant，6=notPresent，7=lowerLayerDown |
| `if_speed_mbps` | 协商出来的速率 |
| `expect_mbps` | 台账登记的预期速率，0 表示没登记 |
| `dot3_symbol_errors` / `..._prev` | 符号错包当前值与上一周期值，增量判协商失败 |
| `link_id` | 链路编号。**同一链路两端填同一个值**，空串表示台账里没配对 |
| `optic` | 光模块。`present`=在位，`model` 空=未识别，`rx_power_dbm`=null 表示 DDM 读不到，`rx_low_alarm_dbm` 是告警门限 |

`link_id` 是单向链路判定的前置条件。端口不成对挂在链路上，「本端 up 对端 down」这句话根本写不出来——这是第 25 课那个「模型错了后面全错」的最小例子。

### `samples/traps.jsonl`

一行一条 Trap，19 条，按到达顺序录的（跨设备交错，代码内部自己排序）。

```json
{"ts":"2026-05-12T10:03:05Z","device":"tor-a3f-02","if_index":9,"kind":"link_down","sys_uptime_ms":12000}
```

| 字段 | 含义 |
|---|---|
| `ts` | RFC3339，方便手读 |
| `device` / `if_index` | Trap 里只有 ifIndex，端口名得靠快照映射 |
| `kind` | `link_down` / `link_up` |
| `sys_uptime_ms` | 该设备此刻的 sysUpTime，识别重启用 |

### 抖动口径

`-flap-window`（默认 10m）内状态翻转次数达到 `-flap-min`（默认 4）就判 flapping。M 和 N 是策略不是常量，不同设备档位可以给不同值：

```bash
make try   # 等价于 go run . -flap-min 2，看判定怎么变
```

冷启动期与重启判定写死成常量，因为它们描述的是设备行为，不是业务策略：`sysUpTime` 比上一条回落超过 5 秒即认定重启过，`sysUpTime` 还在 120 秒以内的 Trap 一律丢弃。

## 这个实验故意留的坑

1. **admin shutdown 的端口没有光**。`tor-a3f-01 Ethernet1/2` 的收光是 `-40 dBm`，正确答案是「对端 admin shutdown」。判定树把物理层排在人为变更前面，这个端口就变成了一条假的硬件故障。
2. **同一根坏线，两端结论不同**。`tor-a3f-01 Ethernet1/48` 判 `optic_power`（硬件），对端 `spine-01 Ethernet1/1` 判 `one_way`（故障）。这不是 bug：收不到光的那一端只看得见「无光」，收得到光的那一端才发现「我能收，对端收不到」。单向链路报在哪一端，取决于采集覆盖面。
3. **收光差判据在两端模块型号不同时会误判**。10 dB 这个门限只在同型号模块之间成立：`spine-01 Ethernet1/10` 与 `spine-02 Ethernet1/10` 都是 `SFP-10G-LR`，差 13.6 dB 就是真失衡。换成一端 `SFP-10G-SR` 一端 `SFP-10G-LR`，正常收光本来就能差 6 dB 以上，链路一长轻松破 10 dB，判出来全是单向。代码没比型号——`rxImbalance` 只管读数求差，型号校验留给落地时补。前提一句话：判这条之前先比型号，不同型号的链路整条剔除出这个判据。
4. **端口 up 不代表收光健康**。`spine-02 Ethernet1/10` 的收光 `-15.4 dBm` 已经越过 `-14 dBm` 告警门限，端口照样 up。判定树第 4 步只在 oper 非 up 时才跑，所以它没被判成光功率异常。端口状态不负责报警，越限但没断的劣化是第 06 课的活。
5. **`ifOperStatus` 有七个值**。`tor-a3f-01 Ethernet1/3` 是 `notPresent`，`spine-02 Ethernet1/11` 是 `lowerLayerDown`。只认 up/down，这两个值会被压平成「down」，`lowerLayerDown` 那个「底层断了」的关键线索当场丢掉。
6. **设备重启后的 Trap 全是噪声**。`tor-a3f-02` 在 `10:03:05` 重启，之后 6 条 Trap 都是冷启动刷出来的。不做剔除，`Ethernet1/9` 会被数成 7 次翻转，判成 flapping——一次重启报一条假抖动。剔除后只剩 1 次，不判。
7. **Trap 里只有 ifIndex**。映射错了，抖动计数全记在别的端口上，而且数字看起来完全正常。
8. **速率对不上不等于协商失败**。`tor-a3f-02 Ethernet1/46` 的 `if_speed_mbps=0` 而台账写 10000，符号错包不涨，端口是真 down。光看速率就判协商失败，这条就要记成配置问题。判据要求「速率不符」和「符号错包在涨」同时成立。
9. **电口没有光模块**。样本全是光口，`optic` 为空一律判 `optic_missing`。真机上 RJ45 端口的 `optic` 本来就是空的，照这个逻辑每个电口都是「模块缺失」——落地前先按介质分叉。
10. **快照没有 `ifLastChange`，也没有采样时刻**。所以分不清「起机后就没 up 过」（多半是模块或配置）与「up 过又掉下来」（事件型故障），也判不了两个源的采样时间是否对齐。样本里 20 个端口一律当成同一时刻采集。
11. **Trap 与轮询的对账没做**。样本里 `spine-01 Ethernet1/1` 已经断了，但它一条 Trap 都没产生（它还 up）。反过来，「Trap 说 down 而快照说 up」（已恢复）和「快照说 down 但没收到 Trap」（Trap 丢了）这两种差异，这个实验只留了数据结构，没写判定——那是第 07 课的活。

## 文件

| 文件 | 作用 |
|---|---|
| `main.go` | 命令行入口，参数与输出编排 |
| `model.go` | 快照与 Trap 的数据结构、样本加载、`ifOperStatus` 七值映射 |
| `decide.go` | 六成因判定树、链路配对、抖动计数与重启剔除 |
| `report.go` | 终端表格、归属汇总、JSONL 落盘 |
| `model_test.go` | 加载与取值映射 |
| `decide_test.go` | 判定树 21 个分支、收光差判据、链路配对、抖动窗口、样本集成断言 |
| `report_test.go` | 表格与 JSONL 输出 |
| `samples/portstate.json` | 20 个端口的 ifTable 快照 |
| `samples/traps.jsonl` | 19 条 Trap，含 admin shutdown、单向链路、设备重启三类陷阱 |
| `out/verdicts.jsonl` | 判定结果（`make run` 生成，不进 git） |

`out/verdicts.jsonl` 一行一个端口：

```json
{"device":"tor-a3f-01","if_name":"Ethernet1/1","if_index":1,"admin":"down","oper":"down","cause":"self_admin_down","cause_zh":"本端 admin shutdown","owner":"change","evidence":"ifAdminStatus=2（本端手工 shutdown）","next_step":"对变更单窗口，确认是否计划内","flap_count":1,"flapping":false}
```
