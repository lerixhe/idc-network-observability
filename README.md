# IDC 网络可观测入门 · IDC Network Observability

> 采集、指标、判读与定位。

[![Open in GitHub Codespaces](https://github.com/codespaces/badge.svg)](https://codespaces.new/lerixhe/idc-network-observability)

**状态：v0.1 骨架** —— 开篇三课已成稿，实验 `00-setup` 可跑，后三篇为课程规划。
站点：<https://lerixhe.github.io/idc-network-observability/> · [English README](README.en.md)

> 系列定位：**第一册**（本书）讲有损网络里的可观测，**第二册**是
> [智算网络可观测指南](https://github.com/lerixhe/aidc-observability)：从有损 IDC 到无损智算。
> 出版顺序和阅读顺序是反的，该先读这本。

---

## 这本书写给谁

写业务代码、想转到可观测平台研发岗的人。会 Go 或 Java、看过监控面板、知道 TCP 三次握手，但没配过交换机。

不要求你会 OSPF 选路，也不教你怎么登录设备改配置——那是网络工程师的本职。这本书教的是把网络现场变成一套**能查询、能告警、能追溯**的系统：数据怎么采、指标怎么定义、曲线怎么判读、故障怎么分段。交付物是可运行的代码。

## 解决什么问题

应用侧监控看得见症状，看不见现场。订单接口 p99 从 210ms 涨到 780ms，链路追踪说慢在下游服务，宿主机 CPU 40%，告警列表干净——真相是同机架两台机器之间 0.3% 丢包。三处结构性差异摆在那里：

| | 应用监控 | 网络可观测 |
|---|---|---|
| 观测对象 | 自己的进程 | 共享的链路与端口，一个端口坏影响几十个业务 |
| 数据来源 | 请求被动打点 | 主动测量，采样周期决定了你能看见什么 |
| 因果链 | 服务到服务 | 服务 → 网卡 → TOR → Spine → 出口，五层时钟还不对齐 |

## 全书骨架

```
采集  →  指标  →  判读  →  定位
原始测量   可聚合时序   结论      动作
```

四道工序串行，上游错了下游救不回来。每一篇交出一个能跑的东西，下一篇接着用。

| 篇 | 讲什么 | 配套实验 | 状态 |
|---|---|---|---|
| 开篇 | IDC 地图、与应用监控的差异、四个环节 | — | 已成稿 |
| 第一篇 · 采集 | 四类数据源、SNMP 到 gNMI、PingMesh、写一个 Go 探针 | `labs/00-setup` | 实验中，课程待写 |
| 第二篇 · 指标 | 指标口径、标签与基数、聚合位置、时序管道、基线 | `labs/01-metrics` | 规划中 |
| 第三篇 · 判读 | 告警抑制、异常形态、ECMP 哈希不均、微突发 | `labs/02-baseline` | 规划中 |
| 第四篇 · 定位 | 分段二分、路径追踪、变更窗口、端到端案例 | `labs/03-bisect` | 规划中 |

## 目录结构

```
.
├── site/                  VitePress 文档站（内容在 site/docs）
├── labs/                  Go 实验代码，每个 lab 一个独立 module
├── scripts/               链接体检与 lab 检查脚本
├── LICENSE                文字内容：CC BY-SA 4.0
└── LICENSE-CODE           代码：MIT
```

## 跑起来

```bash
# 站点
cd site && npm ci && npm run dev

# 第一个实验（只需要 Go 1.22+，零外部依赖）
cd labs/00-setup && make run && make test
```

Codespaces 一键即开：上面的徽章点进去，容器里已经装好 Go。

## 路线图

| 版本 | 内容 | 状态 |
|---|---|---|
| v0.1 | 骨架：站点、开篇三课、`labs/00-setup`、CI 三件套 | **当前** |
| v0.2 | 第一篇成稿 + `labs/01-metrics` | 规划中 |
| v0.3 | 第二篇成稿，指标口径全篇打通 | 规划中 |
| v0.4 | 第三、四篇成稿 + 两个新实验 | 规划中 |
| v1.0 | 结构冻结，端到端案例闭环 | 规划中 |

## 许可

文字内容 [CC BY-SA 4.0](LICENSE)，代码 [MIT](LICENSE-CODE)。
