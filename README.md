# IDC 网络可观测指南 · IDC Network Observability

> 端口、探测、采集、拓扑、管道、告警、定位。

[![Open in GitHub Codespaces](https://github.com/codespaces/badge.svg)](https://codespaces.new/lerixhe/idc-network-observability)

**56 讲（00–55）全部成稿，21 个 Go 实验全部可跑。** 站点：<https://lerixhe.github.io/idc-network-observability/> · [English README](README.en.md)

本仓是系列第一册。第二册 [智算网络可观测入门](https://github.com/lerixhe/aidc-observability/)
往无损智算走：RoCEv2、PFC/ECN 的失败模式、gNMI/INT 遥测、RCA 自动化。两册结构一致，
先读这一册再读那一册。

---

## 这本书写给谁

写业务代码、想转到可观测平台研发岗的人。会 Go 或 Java、看过监控面板、知道 TCP 三次握手，但没配过交换机。

不要求会 OSPF 选路，也不教怎么登录设备改配置——那是网络工程师的本职。这本书教的是把网络现场
变成一套**能查询、能告警、能追溯**的系统：数据怎么采、指标怎么定义、曲线怎么判读、故障怎么分段。
读完手上要落下一套能跑的代码。

## 解决什么问题

应用侧监控看得见症状，看不见现场。订单接口 p99 从 210ms 涨到 780ms，链路追踪说慢在下游服务，
宿主机 CPU 40%，告警列表干净——真相是同机架两台机器之间 0.3% 丢包。三处结构性差异摆在那里：

| | 应用监控 | 网络可观测 |
|---|---|---|
| 观测对象 | 自己的进程 | 共享的链路与端口，一个端口坏影响几十个业务 |
| 数据来源 | 请求被动打点 | 主动测量，采样周期决定了你能看见什么 |
| 因果链 | 服务到服务 | 服务 → 网卡 → TOR → Spine → 出口，五层时钟还不对齐 |

「网络通不通」有三层视角，全书的底座：

| 视角 | 测什么 | 典型误报 |
|---|---|---|
| 设备存活 | 管理 IP 能不能 ping 通、SSH/SNMP 能不能登录 | 设备 CPU 打满超时；ACL 误封管理面 |
| 端口状态 | `ifOperStatus`、光功率 | 端口被 admin shutdown 当成故障；只看了一侧 |
| 路径质量 | 两点之间的时延、丢包、抖动 | 探针自身故障；ECMP 只覆盖一条分支 |

三层结论经常互相矛盾。平台的价值在于把三层对齐，而不是各说各话。

## 全书结构

九部分讲系统，一部分讲一次真实的拼装。按系统分篇，不按工序——端口监测、SNMP 采集、流数据
各是一套东西，协议、代码、失败方式、值班手册全不一样。

| 部分 | 课号 | 讲什么 | 实验 |
|---|---|---|---|
| 一 · 认识现场 | 00–03 | IDC 地图、运维的一天、九个系统与三层视角 | — |
| 二 · 端口与设备存活 | 04–09 | down 的六成因、抖动、光模块、三条通路、一万台设备怎么盯 | `01-portstate` `02-device-alive` |
| 三 · 主动链路探测 | 10–15 | 举证包、探测矩阵、ECMP、INT、探针可信度、写探针 | `03`–`05` `00-setup` |
| 四 · 设备指标采集 | 16–19 | OID 与表遍历、设备 CPU 保护、计数器回绕、gNMI | `06-snmp-walk` `07-counters` |
| 五 · 流数据 | 20–22 | sFlow 与 NetFlow、采样率陷阱、UDP 无重传 | `08-flow` |
| 六 · CMDB 与拓扑 | 23–29 | 台账、链路、两张拓扑图、IP 到端口、对账、事件 | `09`–`11` |
| 七 · 平台工程 | 30–35 | 十万探针的双向管控、流处理与水位线、时序表、成本 | `10-task` `11-pipeline` |
| 八 · 指标与告警 | 36–40 | 口径分歧、标签基数、父子抑制、基线、形态与微突发 | `12-alert` |
| 九 · 排障与定位 | 41–48 | 症状到工具、抓包、时间对齐、分段二分、端到端案例 | `15`–`18` |
| 十 · 实战 | 49–55 | 一个链路质量平台从 0 到 1：约束、决策、代价、事故 | `19` `20` |

## 实验代码

`labs/` 下 21 个独立 Go module，从 `00-setup` 到 `20-probe-security`。只要 Go 1.22 与标准库，
不需要特权、不需要外网，离线可跑。

```bash
cd labs/01-portstate && make run     # 端口 down 的六成因判定树
cd labs/14-alert && make run         # 父子抑制：87 条压到 5 条
./scripts/check-labs.sh              # 逐个 go vet + go test
```

每个实验的样本都故意做过脏：同一根纤两端判出不同成因、CPU 打满的设备被误判成重启、
一台机器两个名字加三台僵尸。每个 lab 的 README 都有「故意留的坑」一节，做完再对照。

## 目录结构

```
.
├── site/                  VitePress 文档站（内容在 site/docs，按部分分目录）
├── labs/                  Go 实验代码，每个 lab 一个独立 module
├── scripts/               链接体检与实验检查脚本
├── LICENSE                文字内容：CC BY-SA 4.0
└── LICENSE-CODE           代码：MIT
```

## 跑起来

```bash
# 站点
cd site && npm ci && npm run dev

# 第一个实验（Go 1.22+，零外部依赖）
cd labs/00-setup && make run && make test
```

点上面的徽章走 Codespaces，容器里已经装好 Go。

## 许可

文字内容 [CC BY-SA 4.0](LICENSE)，代码 [MIT](LICENSE-CODE)。

## 贡献

内容已成稿，接受纠错类 Issue 与 PR。请标明具体课次，并附可核实的依据：

- 机制与协议类断言请给可验证来源（RFC / IEEE 标准 / 论文 / 厂商公开文档）
- 文中示例统一使用 RFC 5737 文档地址段，请勿提交真实设备配置、内网地址或未公开的架构细节
- 本地开发：`cd site && npm ci && npm run dev`
