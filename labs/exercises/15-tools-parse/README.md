# 15-tools-parse · 自己实现列头驱动的解析

第 41、43、45 课的习题。`../../15-tools-parse` 是参考实现，这里是留白版：`parse.go` 留两个函数。

## 任务

三份不同厂商的 `show interfaces counters` 文本，要解析成同一套结构。固件升级后列变多、列序变了、单位从 bytes 改成 packets，**解析器不能崩，也不能静默错**。

要填两个函数，一前一后：

| 函数 | 输入 | 输出 |
|---|---|---|
| `buildLayout` | 列头原文 + 选中的模板 + 对齐方式 | 一串 `column`，每列带行内字节区间、名字、语义字段、单位 |
| `parseDeviceRow` | 一行数据 + 上一函数给出的布局 | 这一行的 `PortCounters` + 未被模板声明的列名清单 |

模板（`template.go`）与样本格式都已经给了。**答案是「按列头的位置切，不按列的顺序切」**——这一句听起来简单，做错的方式有六七种，样本里各埋了一份。

## 跑起来

```bash
cd labs/exercises/15-tools-parse
make run      # 现在会报「未实现：列头驱动布局」
make check    # 实现之后：diff out/parsed.jsonl 与 expected/
make test     # go vet
make answer   # 退路：参考输出落 out-answer/
make demo     # 实现之后能看到单位漂移清单与丢弃清单
```

## `buildLayout` 的四条要求

### 一、按列头切，不按位置切

列头行里每列占一段字节区间（左对齐的表格靠空白分隔，右对齐的数值还能对上）。取值的办法是「这一列的起点到下一列的起点」。

按下标取列（`strings.Fields` 之后取第 n 个）在两种情况下会全错：

- 某个值为空，`Fields` 少一个元素，后面所有列左移一位
- 列序换了（同一厂商不同固件真的会换）

`tokenizeHeader` 已经给了，它保留每列的 `start`/`end`，正是为了防止这件事。

### 二、单位从模板读，不从列名猜

`template.go` 的 `ColumnSpec.Unit` 是唯一来源。样本里有一份固件，列名叫 `InOctets` 而数值是包数——猜列名会在那一刻静默出错，而且错得看起来很正常。

### 三、模板没声明的列也要进布局

那一列的 `field` 留空，值仍然交给 `parseDeviceRow` 去放进丢弃清单。直接跳过它，等于把 `FecUncorrected` 这类「模板还没跟上、但正是你要的值」的列扔掉。

### 四、模板 `Required` 里的字段缺列，直接报错

不做「尽力而为」。缺 `in_errors` 就填 0，会把一次采集失败伪装成一条健康记录。

## `parseDeviceRow` 的三条要求

### 一、占位符不写字段

`-`、`--`、空，都表示「这一列这次没数」。`parseCounter` 返回 `ok=false` 时**不要写 0**。样本里 `sw-c1f-01` 的固件 4.2 整个 `OutOctets` 列不可得，写成 0 会让下游求和得到一条假曲线。

### 二、未被模板声明的列，值留下、名字进清单

进 `pc.Unknown` 与返回的 `unknown` 切片。这一条是「不静默吞掉」的落地。

### 三、取不到端口名直接报错

一条没有主键的记录进库，后面永远查不出来。

## 卡住时看哪

| 现象 | 去哪看 |
|---|---|
| 全列错位一位 | 用了 `Fields` 而不是列头区间；检查空值那列 |
| 某一列的值是上一列的数 | 区间取错了：`end` 应该等于下一列的 `start`，不是本列起点加长度 |
| 单位全是 bytes | 从列名猜了，没读模板的 `unit` |
| 12 行里有 3 行缺 `OutOctets` | 这不是 bug，是固件 4.2 真的没有这一列 |
| 想直接看答案 | `../../15-tools-parse/parse.go` 的 `buildLayout` 与 `parseDeviceRow` |

题目在 `site/docs/08-troubleshoot/45-cli-parsing.md`，坑列表在 `../../15-tools-parse/README.md`（10 条）。

## 文件

| 文件 | 谁写的 | 说明 |
|---|---|---|
| `parse.go` | **你** | `buildLayout` 与 `parseDeviceRow` 两个函数 |
| `template.go` | 给定 | 带版本号的模板、固件前缀匹配、模板自检 |
| `model.go` | 给定 | 字段与单位枚举、计数器结构、单位漂移检测 |
| `mtr.go` | 给定 | mtr 解析与五个 verdict（这一课不用改） |
| `report.go` | 给定 | 主表、漂移清单、丢弃清单、JSONL 落盘 |
| `main.go` | 给定 | 命令行入口 |
| `samples/` | 给定 | 三厂商计数器文本（含一份固件升级对照）+ 一份 mtr |
| `expected/parsed.jsonl` | 给定 | 比对基准，12 条端口 + 1 段 mtr |
