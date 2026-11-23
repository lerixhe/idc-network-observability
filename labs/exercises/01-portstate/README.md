# 01-portstate · 自己实现六成因判定树

第 04–07 课的习题。`../../01-portstate` 是参考实现，这里是留白版：判定树那个文件只给签名。

## 任务

实现 `decide.go` 里的 `Decide`，让 `make check` 通过。

输入 `samples/portstate.json`（20 个端口快照）与 `samples/traps.jsonl`（19 条 Trap），输出 `out/verdicts.jsonl`，逐条端口给出：成因、归属、证据、下一步、抖动次数。

判定树长这样，顺序不能随便调：

```text
端口报 down
├─ ifAdminStatus == 2 ───────────────────── ① 本端 admin shutdown → 变更单
└─ ifAdminStatus == 1
   ├─ 对端同链路端口 ifAdminStatus == 2 ───── ② 对端 admin shutdown → 变更单
   └─ 对端 ifAdminStatus == 1
      ├─ entPhysicalTable 无模块实体 / 型号空 ─ ③ 模块不在位 → 硬件
      ├─ DDM 收光低于门限，或收光为 0 ──────── ④ 光功率异常 → 硬件（第 06 课）
      ├─ ifSpeed 低于额定 && SymbolErrors 涨 ─ ⑤ 协商失败 → 配置
      └─ 本端 up、对端 down（或反向）──────── ⑥ 单向链路 → 故障
```

外加两个特例：两端都 up 但收光差超过 10 dB，也判单向链路；六类都覆盖不到的落 `unknown_down`。

## 跑起来

```bash
cd labs/exercises/01-portstate
make run      # 现在的输出是一行「未实现」，退出码 1
make check    # 实现之后跑这个：diff out/ 与 expected/
make test     # go vet，检查你的代码能编译
make answer   # 实在卡住：跑参考实现，把它的输出拷进 out/（不要先看）
```

`make check` 对齐的是整份 JSONL，包括 `evidence` 的文字。`evidence` 的写法在 `expected/verdicts.jsonl` 里能看出规律——判据字段加值，不用长句。差一两句措辞也会红，那时比的是你的表达与作者的表达，不是逻辑。**先让 20 行的 cause/owner 全对，再抠 evidence 的措辞。**

## 需要自己想清的三件事

### 一、链路配对失败怎么办

端口靠 `link_id` 成对。样本里有一条链路只有一端在快照里（对端设备没纳管），还有一条两端都填了同一个 `link_id` 但状态互相矛盾。

参考实现的选择是**严格模式**：一对多或配不上直接返回错误，让台账先修好。理由是猜一个对端会把单向链路判成正常，比报错更坏。你要不要这么选？想清楚再写，README 末尾的「坑 11」讲了替代方案。

### 二、抖动与成因是两件事

`FlapCount`/`Flapping` 只做标记，**不改 Cause**。六成因才是归属的来源。样本里有一条端口被数成 6 次翻转，但它的成因是 `link_ok`——两端都 up，纯粹在抖。

### 三、设备重启会把 Trap 变成噪声

`tor-a3f-02` 在 10:03:05 重启过。之后 120 秒内的 Trap 全是冷启动刷出来的，必须剔掉再数翻转。判别办法在 `sysUpTimeMs` 里：比上一条回落超过 5 秒即认定重启过。

不剔会怎样：`Ethernet1/9` 被数成 7 次翻转，判成 flapping，值班的人半夜收到一条假告警。

## 卡住时看哪

| 现象 | 去哪看 |
|---|---|
| `make run` 说「未实现」 | 正常，你还没写 |
| `make check` 报某几行 cause 不对 | 对照 `expected/verdicts.jsonl` 看那几条端口的字段，反推树走到哪个分支 |
| 完全没思路 | 读 `../../01-portstate/README.md` 的「判定树」与「坑」两节，那里有 11 条 |
| 想直接看答案 | `../../01-portstate/decide.go`，405 行。看完之后重写一遍，别复制 |

题目本身在 `site/docs/01-ports/04-port-down-causes.md` 到 `07-collection-paths.md` 四课里，判据一节有折叠的参考答案。

## 文件

| 文件 | 谁写的 | 说明 |
|---|---|---|
| `decide.go` | **你** | 只有 `Decide` 一个函数要填 |
| `model.go` | 给定 | 数据结构、样本加载、`ifOperStatus` 七值映射 |
| `report.go` | 给定 | 终端表格与 JSONL 落盘，不要改字段名 |
| `main.go` | 给定 | 命令行入口 |
| `samples/` | 给定 | 20 个端口快照 + 19 条 Trap |
| `expected/verdicts.jsonl` | 给定 | 期望输出，`make check` 的比对基准 |
