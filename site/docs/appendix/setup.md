---
title: 附录 E · 环境准备
---

21 个实验都是独立 Go module，零外部依赖，不连设备、不连网。装好 Go 之后，三分钟能跑通第一个。

## 一、三分钟验证

```bash
cd labs/00-setup
make run
```

期望看到两行东西：终端一张 p50/p99 的表，以及 `out/samples.jsonl`。

```text
目标               总数     成功     丢包率        p50        p99
self             5      5      0.00%      728µs      1.644ms
```

表格数字每次不同（测的是本机回环），格式一致就行。出现这两样，环境就对了。

## 二、装什么

| 必需 | 版本 | 备注 |
|---|---|---|
| Go | 1.22+ | `go version` 能跑就行；实验不引第三方库，不需要 `go mod tidy` |
| make | 任意 | macOS 装 Xcode 命令行工具；Windows 用 WSL，或用 `go run .` 代替 |

可选：`tcpdump`（看第 42 课的 pcap 样本）、`snmpwalk` / `gnmic`（对照真设备输出）。都不是跑实验的前提。

Go 安装在 `/usr/local/go` 或 `~/.local/go` 都可以，`PATH` 里能找到即可。

## 三、目录与 module

```text
labs/
├── 00-setup/          go.mod  main.go  Makefile  README.md
├── 01-portstate/      go.mod  decide.go  samples/  out/
├── ...
└── 20-probe-security/
```

21 个目录各带一个 `go.mod`，互不依赖。这意味着：

- 你可以只 clone 一个目录出来跑，不影响别人
- 每个实验的产物落在自己的 `out/`，不串味
- 想批量检查全部：在仓库根跑 `bash scripts/check-labs.sh`（逐个 `go vet` + `go test`，约 10 秒）

## 四、四个 make 目标

每个实验的 Makefile 都提供这四个，个别多一两个：

| 目标 | 做什么 |
|---|---|
| `make run` | 用 `samples/` 里的样本跑一遍，结果落 `out/` |
| `make test` | `go vet ./... && go test ./...` |
| `make demo` | 额外打印 README 里引用的那几段（漂移清单、汇总、对比） |
| `make clean` | 删 `out/` |

`make run` 通常会把结论写成 JSONL，`make demo` 把 JSONL 里不好看的部分翻译成人话。两者输出不一致是正常的：`demo` 是为了让人读的。

## 五、常见卡点

### 1. `go: module download` 卡住

实验不引第三方依赖，正常不该访问网络。如果卡住，多半是 `GOPROXY` 指向了内网代理：

```bash
go env GOPROXY          # 看现在指向哪
GOFLAGS=-mod=mod GOPROXY=off make run   # 离线跑一次试试
```

### 2. `make: command not found`

Windows 原生环境没有 make。两条路：装 WSL；或者直接用 `go run .`，参数在 Makefile 里能抄。

### 3. `tcpdump: permission denied`（macOS）

抓包要 root：`sudo tcpdump -i en0 -s 96 -w out.pcap`。第 42、44 课的样本已经录好放在 `samples/`，不看真机也能做题。

### 4. 端口占用（`00-setup`）

`00-setup` 自己起一个本地 listener 做自测，端口是随机的。如果 `make run` 报 `bind: address already in use`，换一次就好——它每次随机取端口，撞上的概率很低但存在。

### 5. 输出里的 `-` 不是 0

好几门实验的表格里有 `-`（`15-tools-parse` 的单位漂移、`07-counters` 的不可得计数）。它表示「这项数据在这份样本里不存在」，与 `0`（量出来的零）是两件事。把两者混成 0，故障口看着像健康的——这是第 41、45 课反复强调的一条。

### 6. `out/` 里没东西

`out/` 不进 git（`.gitignore` 里有），第一次跑之前是空的。`make run` 之后应该有文件；没有就检查 `make` 的退出码，多半是样本路径参数写错了。

## 六、卡住时看哪

每个实验的 README 有一节「这个实验故意留的坑」，通常 8–11 条。做题卡住时按顺序查三处：

1. 该实验 README 的「坑」一节，看是不是踩上去了
2. 该实验的 `expected/` 或 README 的「期望输出」，比对差在哪一行
3. 对应课次正文的「判据」一节，那里通常有折叠的参考答案

21 个实验与课次的对应关系在[附录 D](./index)的总表里。

## 七、如果你想自己改样本

样本都是纯文本或 JSON，直接改就行。改完的两条建议：

- **先改值，别改结构**。把 `rx_power_dbm` 从 −7 改成 −20，看判定树的哪个叶子变了；把列头去掉一列，看解析器是拒绝还是静默错
- **改完跑 `make test`**。每个实验的测试都是按原始样本写的断言，改动会让它们红——红的地方就是那个样本承载的判据，读一遍比看十页正文有用
