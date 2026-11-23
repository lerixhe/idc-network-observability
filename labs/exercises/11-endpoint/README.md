# 11-endpoint · 自己实现三表联查

第 27 课的习题。`../../11-endpoint` 是参考实现，这里是留白版：`lookup.go` 只给 `Locate` 的签名。

## 任务

实现 `Locate`，回答运维最高频的那个自助查询：**「10.32.7.18 现在在哪个机柜的哪个口？」**

答案靠三张表接力：

```text
IP ──ARP 表──► MAC ──MAC 表（FDB）──► 交换机端口 ──CMDB──► 机柜 / U 位
```

输入是 `samples/` 下的四份快照（两台网关的 ARP、四台二层交换机的 FDB、一份 DHCP 租约、一份端口台账），共 7 个待查 IP。输出 `out/endpoint.jsonl`，每条带 `verdict`、`evidence`、`traps`、`next_step`。

## 跑起来

```bash
cd labs/exercises/11-endpoint
make run      # 现在的输出全是 not_found，每条带一行「未实现」
make check    # 实现之后跑：diff out/ 与 expected/
make test     # go vet
make answer   # 卡住时的退路，输出落 out-answer/
```

## 四个 verdict

| verdict | 什么时候 |
|---|---|
| `located` | ARP → FDB → 台账三跳走通 |
| `arp_only` | ARP 有，FDB 查不到（可能采漏、可能刚迁走） |
| `lease_only` | ARP 已老化，租约还在——**不能答「查不到」** |
| `not_found` | 三张表都没有 |

## 三个陷阱（样本里各埋了一条）

### 一、MAC 漂移

同一个 MAC 在本 VLAN 的两个端口都能查到。虚拟机迁走后，旧交换机的表项要等老化周期跑完才消失，这段时间两处都在。

判据：按 `age_s` 留小的那条，另一条进 `Stale`，并打 `mac_drift`。**不要报错，也不要随便挑一个**——挑错的那个会把运维指到一台已经搬空的机器。

### 二、聚合口只报逻辑口

FDB 里看到的是 `Po1`，台账里的端口-机柜映射只对物理成员口有效。直接查台账必然落空。

判据：命中逻辑口时展开到成员口（台账里有聚合关系），再查位置，并打 `lag_logical_only`。

### 三、租约还在、ARP 没了

设备关机或静默一段时间后 ARP 表项老化消失，但 DHCP 租约还在有效期内。这时答 `not_found` 是错的——租约能告诉你「这个 IP 分给过谁」，是追溯历史的最短路径。

判据：ARP 查不到时回落到租约，非活动租约也要能识别（样本里有一条已过期），并打 `lease_only`。

## 还有两件事要想清

**先试哪个 MAC。** ARP 与租约可能给出不同的 MAC（换了网卡）。ARP 是网关此刻的认知，比租约新，所以要排前面；两者不同时租约那条作为次候选。

**老化门限是真的门限。** `opt.ArpAging` 默认 300 秒，样本里有 ARP 表项恰好落在门限两侧。调大它等于承认自己的采集频率不够——这一条在 `Options` 的注释里写着。

## 卡住时看哪

| 现象 | 去哪看 |
|---|---|
| 全 `not_found` | 正常，还没实现 |
| `located` 有了但机柜是 `-` | 台账那一步没做，或逻辑口没展开 |
| 某个 IP 明明在 ARP 里却查不到 | 看 `AgeS` 与门限，再看 VLAN 过滤 |
| 想直接看答案 | `../../11-endpoint/lookup.go`，458 行 |

题目在 `site/docs/05-cmdb/27-endpoint-locate.md`，坑列表在 `../../11-endpoint/README.md`。

## 文件

| 文件 | 谁写的 | 说明 |
|---|---|---|
| `lookup.go` | **你** | 只有 `Locate` 要填，类型与常量已给 |
| `model.go` | 给定 | 三张表的数据结构与加载、租约状态判定 |
| `report.go` | 给定 | 终端表格与 JSONL 落盘 |
| `main.go` | 给定 | 命令行入口 |
| `samples/` | 给定 | ARP / FDB / 租约 / 台账四份快照 |
| `expected/endpoint.jsonl` | 给定 | 比对基准，7 行 |
