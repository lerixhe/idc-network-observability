# 07-counters · 自己实现速率还原与错包归因

第 18、36 课的习题。`../../07-counters` 是参考实现，这里是留白版：`rate.go` 与 `attr.go` 各留一个函数。

两个任务可以分开做，`make check` 会分别报。

## 任务一：三种速率算法（`rate.go` 的 `ComputeRates`）

一份端口计数器采样，同一段数据算三遍：

| 版本 | 算法 | 丢样时 | 回绕时 |
|---|---|---|---|
| v1 `Naive` | 差值 ÷ 标称周期 | 偏高（分母小） | 负数 |
| v2 `Normalized` | 差值 ÷ 实际间隔 | 对 | 仍错 |
| v3 `WrapInterp` | 回绕补正后 ÷ 实际间隔 | 对 | 对 |

三列并排打出来，分岔点自己会露出来——**这就是为什么三个都要算，只算一个的话你永远不知道哪段数据不可信**。

样本里埋了六类情况：1G 口回绕、10G 口回绕、设备重启清零、漏采（间隔翻倍）、文件内乱序、`sysUpTime` 自己回绕（约 497 天，不是重启）。

四条硬性要求：

1. **差值为负先分清是回绕还是重启。** 看 `sysUpTime`：归零是重启（这一区间 `Reset=true`），连续增长是回绕。`sysUpTime` 自己回绕不算重启。
2. **增量 ≥ 量程的一半时，圈数不可判。** 这一段标 `Ambiguous=true` 且 `Trusted=false`，不要猜一个圈数——猜出来的速率看着很像真的。
3. **采集周期 ≥ 回绕周期的 1/2 时，`ConfigRisk` 置真。** 样本里有一个 10 秒采一次的 10G 口，它的回绕周期只有 3.4 秒。
4. **每个区间写一句 `Reason`**，说清这段为什么可信或不可信。

## 任务二：四类错包归因（`attr.go` 的 `Attribute`）

同一份错包计数，四个方向各给证据：

| 归属 | 判据 |
|---|---|
| `host` | 交换机侧全干净，主机侧 `host_rx_drops`/`host_retransmits` 在涨 |
| `module` | 符号错成片：每个校验失败帧摊到 8 个以上符号错 |
| `cable` | 帧校验零星失败而编码层干净 |
| `congestion` | 队列丢/缓存丢在涨，帧校验干净 |
| `clean` | 帧错率低于 1 ppm，当噪声 |

规则顺序不能调：先排除主机侧，再物理层、再拥塞。理由是坏包会拖垮上层重传，把拥塞指标一起带坏——两者同时出现时先修物理层。

分母别弄错：错误率按**百万帧**算，分母是 `in_ucast_pkts`；利用率按 `in_octets` 与 `window_s` 算。两个分母不通用，这是第 36 课那条规矩的最小例子。

## 跑起来

```bash
cd labs/exercises/07-counters
make run      # 现在的输出是一行「未实现」
make check    # 两份产物分别对齐：rates.jsonl 与 attribution.json
make test     # go vet
make answer   # 退路：参考输出落 out-answer/
```

`make check` 对 `rates.jsonl` 是逐字节比，对 `attribution.json` 也是。`Reasons` 的文字也参与比对，先让 `Verdict`/`Trusted`/各个 ppm 全对，再抠措辞。

## 卡住时看哪

| 现象 | 去哪看 |
|---|---|
| 全段 `Trusted=false` | 圈数不可判的门限定得太宽，或者 `WrapPeriod` 没算对 |
| 回绕段算成负数 | v1 没做，或者 `Reset` 判定把回绕当成重启 |
| 重启的段没标 `Reset` | 只看差值为负不够，要看 `sysUpTime` |
| 归因方向相反 | 顺序错了：先主机、再物理、再拥塞 |
| 想直接看答案 | `../../07-counters/rate.go` 与 `attr.go` |

题目在 `site/docs/03-snmp/18-counters.md` 与 `07-alert/36-metric-definitions.md`。

## 文件

| 文件 | 谁写的 | 说明 |
|---|---|---|
| `rate.go` | **你** | `ComputeRates` 一个函数 |
| `attr.go` | **你** | `Attribute` 一个函数 |
| `model.go` | 给定 | 序列结构、量程与回绕周期、标称周期、乱序标记 |
| `report.go` | 给定 | 三列对比表、警告、对账、归因表 |
| `main.go` | 给定 | 命令行入口 |
| `samples/` | 给定 | 计数器序列、错包样本、注入的真值 |
| `expected/` | 给定 | `rates.jsonl` 与 `attribution.json`，比对基准 |
