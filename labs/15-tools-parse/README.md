# 15-tools-parse · 设备命令输出解析（三厂商）+ mtr 路径解析

第 41、43、45 课的配套实验。喂进三份不同厂商的 `show interfaces counters` 文本和一份 `mtr` 输出，要求解析成统一结构，并在一份「固件升级后列变多、单位从 bytes 改成 packets」的样本上不崩。

平台里总有一条「SSH 执行命令 + 解析文本」的通路，因为队列深度、buffer 使用、MAC 表、BGP 邻居状态这些数 SNMP 采不到。这条通路的失败几乎全出在格式漂移上：列头换了、列序变了、单位改了、某列直接消失。这个实验把四类漂移各埋一处。

## 跑起来

```bash
cd labs/15-tools-parse
make run     # 解析 samples/ 下四份文本，打主表与每跳丢包率，结果落 out/parsed.jsonl
make test    # vet + 单元测试（32 个）
make demo    # 额外打印单位漂移、丢弃清单、派生换算
make clean   # 删 out/
```

零外部依赖，不连设备、不连网。

## 期望输出

`make run`：

```text
设备样本 3 份 / 采集段 4 段 / 端口行 12 条 / mtr 1 次
模板 vendor-a-counters@v1 · vendor-b-counters@v1 · vendor-c-counters@v1 · vendor-c-counters@v2

device     fw      template              port      in_bytes    in_packets  out_bytes  out_packets  in_errors  out_errors  speed  unknown
sw-a3f-07  5.170   vendor-a-counters@v1  GE1/0/1   1288412390  10233412    331204411  2881901      0          0           10G    CRC
sw-a3f-07  5.170   vendor-a-counters@v1  GE1/0/2   882143310   7802114     771203318  6903112      12         3           10G    CRC
sw-a3f-07  5.170   vendor-a-counters@v1  GE1/0/48  9931204     88211       12044112   991204       0          0           1G     CRC
sw-b1f-02  4.8(2)  vendor-b-counters@v1  Gi1/0/1   1290000000  10230000    331200000  2880000      0          0           -      InMcastPkts
sw-b1f-02  4.8(2)  vendor-b-counters@v1  Gi1/0/2   882100000   7800000     771200000  6900000      12         3           -      InMcastPkts
sw-b1f-02  4.8(2)  vendor-b-counters@v1  Gi1/0/49  9930000     88200       12040000   991200       0          0           -      InMcastPkts
sw-c1f-01  3.9     vendor-c-counters@v1  Et1       1288412390  10233412    331204411  -            0          0           -      -
sw-c1f-01  3.9     vendor-c-counters@v1  Et2       882143310   7802114     771203318  -            12         3           -      -
sw-c1f-01  3.9     vendor-c-counters@v1  Et3       9931204     88211       12044112   -            0          0           -      -
sw-c1f-01  4.2     vendor-c-counters@v2  Et1       1299120412  10319311    -          2904771      0          0           10G    FecUncorrected
sw-c1f-01  4.2     vendor-c-counters@v2  Et2       889211004   7864012     -          6919221      12         3           10G    FecUncorrected
sw-c1f-01  4.2     vendor-c-counters@v2  Et3       9981204     88612       -          993011       0          0           1G     FecUncorrected
```

`-` 是「这一列在这份输出里不存在」，不是 0。`sw-c1f-01` 同一台设备的两段采集正好演示了这一点：3.9 版有 `OutOctets` 没有 `OutPkts`，4.2 版反过来。按出口字节求和的下游查询在 4.2 之后会得到空值，而不是 0——这两种结果在图上是两条完全不同的线。

mtr 部分：

```text
mtr  probe-b1f-02 → 10.44.8.9（7 跳 → 10.44.8.9，终点丢包 38.0%，判定 target_loss）
hop  host        loss%  snt  last  avg  best  worst  verdict          note
1    10.20.0.1   0.0    100  0.4   0.5  0.3   1.2    ok
2    10.20.1.9   95.0   20   1.1   1.2  0.9   2.0    icmp_rate_limit  中间跳丢包 95.0% 而终点正常，按 ICMP 限速处理；Snt 只有 20，这个丢包率本身的误差也很大
3    10.20.64.1  0.0    100  2.4   2.5  2.2   3.1    ok
4    ???         0.0    100  0.0   0.0  0.0   0.0    silent_hop       不回探测包（??? 或 *），这一跳没有数据；只要后面的跳有回答，就不算丢包
5    10.33.9.2   1.0    100  6.8   7.0  6.1   9.4    ok               丢包 1.0% 未过 5% 门限，按抖动处理
6    10.33.12.7  38.0   100  8.1   8.4  7.2   15.3   real_loss        丢包 38.0%，后续跳同样在丢，这一跳到终点的损耗是真的
7    10.44.8.9   38.0   100  8.4   8.7  7.5   16.0   real_loss        丢包 38.0%，后续跳同样在丢，这一跳到终点的损耗是真的
结论 target_loss · 终点 10.44.8.9 丢包 38.0%，最后几跳一路丢到这里，这是真丢包；中间跳 2(10.20.1.9) 的高丢包是 ICMP 限速，别当第二处故障；有 1 跳不回 ICMP（跳 4），结论只覆盖有回答的跳
```

`make demo` 再打三段：

```text
单位 / 列漂移（同设备相邻两次采集对比，共 6 处）
  sw-c1f-01 out_bytes：bytes（fw 3.9） → 不可得（fw 4.2）
  sw-c1f-01 out_packets：不可得（fw 3.9） → packets（fw 4.2）
  sw-c1f-01 in_discards：不可得（fw 3.9） → count（fw 4.2）
  sw-c1f-01 queue_drops：不可得（fw 3.9） → count（fw 4.2）
  sw-c1f-01 in_pause：不可得（fw 3.9） → count（fw 4.2）
  sw-c1f-01 out_discards：不可得（fw 3.9） → count（fw 4.2）
  out_bytes 消失意味着按出口字节求和的下游查询会得到空值，不是 0；换字段或加换算，二者都要写进变更单

模板没声明的列（3 个，值留在丢弃清单里）
  sw-a3f-07    vendor-a-counters@v1   CRC=0
  sw-b1f-02    vendor-b-counters@v1   InMcastPkts=388.1K
  sw-c1f-01    vendor-c-counters@v2  FecUncorrected=2
```

第三段是派生换算，同时告诉你为什么它不能进报表：

```text
出口字节的派生值（用同一行入向的均值换算，只用于比量级）
  sw-c1f-01 Et1 fw 4.2：入向 125.89 B/包（1299120412 / 10319311）→ out_packets 2904771 折合 365687913 B（派生）
  sw-c1f-01 Et2 fw 4.2：入向 113.07 B/包（889211004 / 7864012）→ out_packets 6919221 折合 782380222 B（派生）
  sw-c1f-01 Et3 fw 4.2：入向 112.64 B/包（9981204 / 88612）→ out_packets 993011 折合 111852180 B（派生）
  派生值别进报表：出口的平均帧长与入口不一样，小包单向发送很常见
```

固件 4.2 抽掉了 `OutOctets`，拿 `out_packets` 乘入向平均帧长能拿到一个量级正确的数（`365687913` 对真实的 `331204411` 偏高 10%），它够用来判断「这个口有没有流量」，不够写进 SLA 报表。派生列必须带标记入库，否则一年后没人分得清哪个数是量的、哪个是算的。

`avg_frame_bytes` 是另一回事，它是同一行内 bytes ÷ packets，两个数都在时才成立（`sw-a3f-07 GE1/0/1` 是 125.90，`GE1/0/2` 是 113.06）。平均帧长掉到 113 字节，说明这个口的流量以小包为主，同样的比特率下 pps 高得多。

## 解析走列头，不走下标

样本的列头不带单位，单位由模板声明（`template.go` 的 `ColumnSpec.Unit`）。这是这个实验的立论基础，三条理由：

- **列序会变**。同一厂商的不同固件里，`InErrors` 与 `InDiscards` 的先后换过。按下标取列，换一次固件就静默错一列，错得完全看不出来
- **列名不能猜单位**。列名叫 `InOctets` 而数值是包数的固件真实存在，抄模板时改了一半更常见。模板自检 `Template.Validate()` 专门查这一条：字段重复、单位与列头语义冲突，进程序就报错
- **别名要显式**。`InOctets` / `in_octets` / `Input Bytes` 指同一件事，`Headers` 列表的第一个是主名，其余是别名，匹配时忽略大小写与空白

模板的身份证是 `ID@Version`（`vendor-c-counters@v2`），每条解析结果都记着是哪版模板解的。固件升级后排查解析事故，第一个要看的就是这个字段。

选模板靠固件前缀匹配，不解析版本号语义——`4.8(2)`、`5.170`、`3.9` 三种写法，做区间比较必然出错。前缀空表示兜底模板，用上它会打一条警告。

## mtr 的五个 verdict

| verdict | 判据 | 下一步 |
|---|---|---|
| `ok` | 丢包未过 5% 门限 | 无 |
| `icmp_rate_limit` | 本跳高丢包，但**下一跳与终点都干净** | 不是故障，别派单 |
| `silent_hop` | 这一跳不回包（`???` 或 `*`），后面的跳有回答 | 数据缺口，结论只覆盖有回答的跳 |
| `real_loss` | 本跳丢包过门限，**后续跳也一路在丢** | 真丢包，位置在这一跳之后 |
| `target_loss` | 终点丢包过门限 | 真丢包，从最早开始丢的那跳算起 |

一个细节：`Snt` 只有 20 的那一跳，95% 的丢包率本身误差很大（二项分布的置信区间能跨几十个百分点）。判定仍按 95% 输出，`note` 里写明样本基数小——阈值判的是结论，样本量判的是这个结论有多硬。

## 样本格式

`samples/*.txt` 是带指令头的纯文本，一行一条，指令以 `@` 开头：

| 指令 | 含义 |
|---|---|
| `@kind` | `device` 或 `mtr`，决定走哪个解析器 |
| `@device` / `@vendor` / `@firmware` | 设备三元组，选模板的输入 |
| `@command` | 执行的命令原文，落库后审计与复现都靠它 |
| `@table` | 后面跟列头与数据行，直到空行或下一条指令 |

列头下面允许有分隔线与分页符（`----`、`More:`），解析时跳过。短行（尾部列缺失）按缺失处理，不补 0。

## 这个实验故意留的坑

1. **`-` 与 `0` 不是一回事**。`sw-c1f-01` 4.2 版没有 `OutOctets`，输出 `-`；`sw-a3f-07` 的 `in_errors` 是 `0`，是量出来的 0。把两者混成 0，故障口看着像健康的
2. **单位漂移只在相邻两次采集的对比里看得见**。单次采集判不出来，必须记住上一次的模板版本。做法是同一个 `(device, field)` 的 `unit` 变了就打一条漂移记录
3. **`out_bytes` 消失不是「出口字节为 0」**。下游按出口字节求和会得到空值。换字段或加换算，两种做法都要写进变更单，让下游知道
4. **列序无关**。`TestLayoutColumnOrderIndependent` 把样本的列序打乱，结果必须一致。按下标取列的实现过不了这一条
5. **未知列不静默丢**。模板没声明的列进丢弃清单，`make demo` 会打出来（`CRC`、`InMcastPkts`、`FecUncorrected`）。这些列往往正是你要的值——`FecUncorrected` 是 400G 口上最早的健康信号，漏掉它等于漏掉一个故障源
6. **未知指令拒绝解析**。`@command` 或 `@kind` 写错，整段样本拒绝解析并报错，不做「尽力而为」。静默跳过一段数据，事后没人知道那段为什么是空的
7. **模板缺 `Required` 字段要拒绝，不能填 0**。`in_errors` 缺失时填 0，会把一个采集失败伪装成一条健康记录
8. **mtr 中间跳的 95% 不是故障**。只看单跳丢包就会给一跳限速的网关派单。判据必须是「本跳丢包 + 后续跳也丢」
9. **`silent_hop` 那一跳的数字全是 0**，它不代表丢包为零，代表没有数据。拿它算平均丢包率会把结论拉低
10. **厂商后缀 `SIS`**。vendor-b 的输出里 `InMcastPkts` 带单位后缀，`88200` 与 `88200SIS` 指同一个数，模板里的解析要能吃两种写法

## 文件

| 文件 | 作用 |
|---|---|
| `main.go` | 命令行入口、参数、编排 |
| `model.go` | 字段与单位枚举、计数器结构、派生换算（平均帧长） |
| `template.go` | 带版本号的解析模板、固件前缀匹配、模板自检 |
| `parse.go` | 指令头解析、列头分词、按列名切片取列、短行与未知列处理 |
| `mtr.go` | mtr 输出解析与五个 verdict 的判定 |
| `report.go` | 主表、漂移清单、丢弃清单、JSONL 落盘 |
| `samples/vendor-a-counters.txt` | 华为风格 `display interface counters` |
| `samples/vendor-b-counters.txt` | 思科风格 `show interfaces`，含 `SIS` 后缀与 `-` |
| `samples/vendor-c-counters-drift.txt` | 同一台设备固件 3.9 → 4.2 的两段，列与单位都变 |
| `samples/mtr-path.txt` | 7 跳，含 ICMP 限速、silent hop、真丢包、小样本四类 || `out/parsed.jsonl` | 解析结果（`make run` 生成，不进 git） |

`out/parsed.jsonl` 一行一个采集段：

```json
{"kind":"counters","device":"sw-a3f-07","vendor":"vendor-a","firmware":"5.170","command":"display interface counters","template":"vendor-a-counters@v1","port":"GE1/0/1","speed":"10G","counters":{"in_bytes":{"value":1288412390,"unit":"bytes","raw":"1288412390"}},"unknown":[{"name":"CRC","value":"0"}],"avg_frame_bytes":125.90252302946466}
```
