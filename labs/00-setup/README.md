# 00-setup · 第一个探针

第一篇「采集」的起步实验。五分钟内拿到第一份**原始测量样本**，并算出一份 p50 / p99 / 丢包率的小结。

## 跑起来

```bash
cd labs/00-setup
make run     # 对本地 listener 自测，不碰外网
make test    # vet + 单元测试
```

输出两份东西：

```
目标               总数     成功     丢包率        p50        p99
self             5      5      0.00%      728µs      1.644ms
```

以及 `out/samples.jsonl`，一条测量一行：

```json
{"target":"self","addr":"127.0.0.1:50237","seq":0,"unix_ms":1794466766606,"rtt_us":321,"ok":true}
```

## 测真实目标

```bash
make try
# 等价于：
go run . -targets samples/probe-matrix.json -count 10 -timeout 300ms -workers 16
```

`samples/probe-matrix.json` 里换成一串真实的可拨号地址即可（TCP 端口开着就行，SSH 的 22 最省事）。

## 这个实验故意留下的三个坑

1. **TCP 建连的耗时 ≠ 网络时延**。它包含对端 accept 的处理时间。想量纯网络时延，得上 ICMP 或者带内遥测——第一篇后面会讲。
2. **平均值没有输出**。你没有看错，故意的。一次 200ms 的尖刺混在 100 个 2ms 的样本里，平均值只涨 2ms，你会以为一切正常。
3. **`ok: false` 不等于丢包**。对端端口没开、防火墙丢弃、探测超时，在你的汇总里统统算成"丢包"。指标口径的坑，从第一条样本就开始了。

## 文件

| 文件 | 作用 |
|---|---|
| `main.go` | 目标加载、并发探测、样本落盘 |
| `stats.go` | 分位数与丢包率的口径实现 |
| `setup_test.go` | 口径与探测路径的单元测试 |
