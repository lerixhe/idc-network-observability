---
name: IDC 网络可观测入门 · Labs

> 一本书的实验环境：Go 1.22 足矣，不需要特权，不需要外网。

## 用法

每个 lab 是一个独立 Go module，各自能跑：

```bash
cd labs/01-portstate
make run
```

一次性检查全部：

```bash
./scripts/check-labs.sh   # 逐个 go vet + go test
```

## 实验清单

| 实验 | 对应章节 | 做什么 |
|---|---|---|
| `00-setup` | 15（第一个探针） | 并发打点、算 p50/p99、样本落成 JSONL |
| `01-portstate` | 04–07 | 端口 down 的六成因判定树，输出成因、归属与下一步；样本混了 admin shutdown、单向链路、SNMP 代理重启三类陷阱 |
| `02-device-alive` | 08、09 | 一万台设备存活监测模拟：探测调度、去抖、探测侧故障识别、上下线事件；四类剧本各自的误报代价写在 README 里 |

后续实验按 `drafts/book1/00-outline.md` 的实验矩阵逐个落地，编号与课文对得上。

## 留坑是设计的一部分

每个实验的样本都故意做过脏：`01-portstate` 里同一根纤两端判出不同成因，`02-device-alive` 里 CPU 打满的设备会被误判成重启。踩过一遍比看十遍正文管用——每个 lab 的 README 都有「这个实验故意留的坑」一节，做完再去对照。
