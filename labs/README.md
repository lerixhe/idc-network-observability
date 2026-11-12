name: IDC 网络可观测入门 · Labs

> 一本书的实验环境：Go 1.22 足矣，不需要特权，不需要外网。

## 用法

每个 lab 是一个独立 Go module，各自能跑：

```bash
cd labs/00-setup
make run
```

一次性检查全部：

```bash
./scripts/check-labs.sh   # 逐个 go vet + go test
```

## 实验清单

| 实验 | 对应章节 | 做什么 |
|---|---|---|
| `00-setup` | 第一篇 · 采集 | 第一个探针：并发打点、算 p50/p99、样本落成 JSONL |

后续实验随篇章落地：`01-metrics`（口径与时序）、`02-baseline`（基线）、`03-bisect`（分段定位）。
