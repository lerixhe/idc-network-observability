---
title: 附录
---

五页内容，三个独立页面加本目录：

| 页 | 内容 |
|---|---|
| [A 术语表](./glossary) | 92 条，按主题分组，每条一句话加首课号 |
| [C 基准配置](./benchmark) | 全书容量与开销算术的**唯一**定义处 |
| [E 环境准备](./setup) | 跑实验要装什么，卡住时看哪一节 |
| D 课号 ↔ 实验总表 | 在本页下半部分，56 课对 21 个实验 |
| B 工具清单 | 并入 [E 环境准备](./setup) 的第二节 |

## B · 工具清单

跑本书实验需要的、以及生产里会用到的。

| 类别 | 工具 | 版本/说明 |
|---|---|---|
| 语言 | Go | 1.22+，实验全部用 Go，不引第三方库 |
| 构建 | make | 每个实验三个目标：`run` / `test` / `demo` |
| 抓包 | tcpdump、Wireshark | 看 pcap 样本，权限问题见 E 页 |
| 探测 | ping、mtr、iperf3 | 第 41–43 课的对照工具 |
| 主机侧 | ethtool、ss、ip | 第 44 课 |
| 时序与可视化 | Prometheus、ClickHouse、Grafana | 生产侧，实验里用文件替代 |
| 设备接口 | snmpwalk、gnmic | 实验用内置 agent 模拟，不必真设备 |

## D · 课号 ↔ 实验总表

21 个实验，56 课。一个实验通常服务连续几课，表里按课列全。`—` 表示这一课没有配套实验，理由写在课末的「配套实验」一节里。

| 课 | 标题 | 配套实验 |
|---|---|---|
| 00 | 为什么后端写的那套监控，在 IDC 里不够用 | [`00-setup`](https://github.com/lerixhe/idc-network-observability/tree/main/labs/00-setup) |
| 01 | 一个 IDC 的地图 | [`00-setup`](https://github.com/lerixhe/idc-network-observability/tree/main/labs/00-setup) |
| 02 | 网络运维的一天 | — |
| 03 | 全景：九个系统与三层视角 | — |
| 04 | 交换机端口 UP/DOWN 的六种成因 | [`01-portstate`](https://github.com/lerixhe/idc-network-observability/tree/main/labs/01-portstate) |
| 05 | 端口抖动：比 down 更危险的那类故障 | [`01-portstate`](https://github.com/lerixhe/idc-network-observability/tree/main/labs/01-portstate) |
| 06 | 光模块：端口 down 之前的早期信号 | [`01-portstate`](https://github.com/lerixhe/idc-network-observability/tree/main/labs/01-portstate) |
| 07 | 链路状态的三条采集通路 | [`01-portstate`](https://github.com/lerixhe/idc-network-observability/tree/main/labs/01-portstate) |
| 08 | 设备存活：管理面可达性 | [`02-device-alive`](https://github.com/lerixhe/idc-network-observability/tree/main/labs/02-device-alive) |
| 09 | 一万台设备的存活监测工程 | [`02-device-alive`](https://github.com/lerixhe/idc-network-observability/tree/main/labs/02-device-alive) |
| 10 | 自证清白：测量即证据 | [`03-probe-matrix`](https://github.com/lerixhe/idc-network-observability/tree/main/labs/03-probe-matrix) |
| 11 | PingMesh 与探测矩阵 | [`03-probe-matrix`](https://github.com/lerixhe/idc-network-observability/tree/main/labs/03-probe-matrix) |
| 12 | ECMP 让探测说谎 | [`04-ecmp-hash`](https://github.com/lerixhe/idc-network-observability/tree/main/labs/04-ecmp-hash) |
| 13 | INT 与带内遥测 | [`05-int-sim`](https://github.com/lerixhe/idc-network-observability/tree/main/labs/05-int-sim) |
| 14 | 探针自己可信吗 | [`03-probe-matrix`](https://github.com/lerixhe/idc-network-observability/tree/main/labs/03-probe-matrix) |
| 15 | 写一个生产可用的探针 | [`00-setup`](https://github.com/lerixhe/idc-network-observability/tree/main/labs/00-setup) |
| 16 | SNMP 解剖：OID、MIB、表遍历 | [`06-snmp-walk`](https://github.com/lerixhe/idc-network-observability/tree/main/labs/06-snmp-walk) |
| 17 | SNMP 采集工程 | [`06-snmp-walk`](https://github.com/lerixhe/idc-network-observability/tree/main/labs/06-snmp-walk) |
| 18 | 计数器语义：回绕、速率、错包 | [`07-counters`](https://github.com/lerixhe/idc-network-observability/tree/main/labs/07-counters) |
| 19 | gNMI 与订阅式采集 | [`06-snmp-walk`](https://github.com/lerixhe/idc-network-observability/tree/main/labs/06-snmp-walk) |
| 20 | sFlow、NetFlow、IPFIX 是三种东西 | [`08-flow`](https://github.com/lerixhe/idc-network-observability/tree/main/labs/08-flow) |
| 21 | 采样率的陷阱 | [`08-flow`](https://github.com/lerixhe/idc-network-observability/tree/main/labs/08-flow) |
| 22 | 流采集的工程与判断 | [`08-flow`](https://github.com/lerixhe/idc-network-observability/tree/main/labs/08-flow) |
| 23 | CMDB 是主数据 | [`09-cmdb`](https://github.com/lerixhe/idc-network-observability/tree/main/labs/09-cmdb) |
| 24 | 设备台账建模 | [`09-cmdb`](https://github.com/lerixhe/idc-network-observability/tree/main/labs/09-cmdb) |
| 25 | 链路与拓扑建模 | [`09-cmdb`](https://github.com/lerixhe/idc-network-observability/tree/main/labs/09-cmdb) |
| 26 | 拓扑自动发现：设备之间怎么连的 | [`09-cmdb`](https://github.com/lerixhe/idc-network-observability/tree/main/labs/09-cmdb)、[`10-topology`](https://github.com/lerixhe/idc-network-observability/tree/main/labs/10-topology) |
| 27 | 终端定位：IP → 交换机 → 端口 | [`11-endpoint`](https://github.com/lerixhe/idc-network-observability/tree/main/labs/11-endpoint) |
| 28 | 台账对账与变更、影响面 | [`09-cmdb`](https://github.com/lerixhe/idc-network-observability/tree/main/labs/09-cmdb) |
| 29 | syslog 与 Trap：把噪声变成事件 | [`10-topology`](https://github.com/lerixhe/idc-network-observability/tree/main/labs/10-topology) |
| 30 | 探针管控：拉取还是推送 | [`12-task`](https://github.com/lerixhe/idc-network-observability/tree/main/labs/12-task) |
| 31 | 任务编排的失败模式 | [`12-task`](https://github.com/lerixhe/idc-network-observability/tree/main/labs/12-task) |
| 32 | 探针鉴权与速率整形 | [`12-task`](https://github.com/lerixhe/idc-network-observability/tree/main/labs/12-task)、[`20-probe-security`](https://github.com/lerixhe/idc-network-observability/tree/main/labs/20-probe-security) |
| 33 | 编码与流处理 | [`13-pipeline`](https://github.com/lerixhe/idc-network-observability/tree/main/labs/13-pipeline) |
| 34 | 时序库、表设计与查询 | [`13-pipeline`](https://github.com/lerixhe/idc-network-observability/tree/main/labs/13-pipeline) |
| 35 | 容量规划与成本 | [`13-pipeline`](https://github.com/lerixhe/idc-network-observability/tree/main/labs/13-pipeline) |
| 36 | 指标口径：同一个词三种答案 | [`07-counters`](https://github.com/lerixhe/idc-network-observability/tree/main/labs/07-counters) |
| 37 | 标签与基数 | [`13-pipeline`](https://github.com/lerixhe/idc-network-observability/tree/main/labs/13-pipeline) |
| 38 | 告警引擎：规则、抑制、关联 | [`14-alert`](https://github.com/lerixhe/idc-network-observability/tree/main/labs/14-alert) |
| 39 | 基线：阈值为什么必然误报 | [`14-alert`](https://github.com/lerixhe/idc-network-observability/tree/main/labs/14-alert) |
| 40 | 形态与微突发 | — |
| 41 | 八类工具：答得了什么，答不了什么 | [`15-tools-parse`](https://github.com/lerixhe/idc-network-observability/tree/main/labs/15-tools-parse) |
| 42 | 抓包：tcpdump 与 tshark | [`16-capture`](https://github.com/lerixhe/idc-network-observability/tree/main/labs/16-capture) |
| 43 | 路径工具：traceroute、mtr、paris | [`15-tools-parse`](https://github.com/lerixhe/idc-network-observability/tree/main/labs/15-tools-parse) |
| 44 | 主机与网卡：ethtool、ss、/proc | [`16-capture`](https://github.com/lerixhe/idc-network-observability/tree/main/labs/16-capture) |
| 45 | 命令输出解析与平台集成 | [`15-tools-parse`](https://github.com/lerixhe/idc-network-observability/tree/main/labs/15-tools-parse) |
| 46 | 时间对齐：五层时钟与因果判定 | [`16-capture`](https://github.com/lerixhe/idc-network-observability/tree/main/labs/16-capture) |
| 47 | 分段二分与决策表 | [`17-bisect`](https://github.com/lerixhe/idc-network-observability/tree/main/labs/17-bisect) |
| 48 | 端到端案例：0.3% 的丢包 | [`18-case`](https://github.com/lerixhe/idc-network-observability/tree/main/labs/18-case) |
| 49 | 需求翻译与量级估算 | — |
| 50 | 探测引擎：把 90% 的链路看见 | — |
| 51 | 三十八万任务的增量与坏点替换 | [`19-task-increment`](https://github.com/lerixhe/idc-network-observability/tree/main/labs/19-task-increment) |
| 52 | 探针安全与可信 | [`20-probe-security`](https://github.com/lerixhe/idc-network-observability/tree/main/labs/20-probe-security) |
| 53 | 数据通道与热切灾备 | — |
| 54 | 阈值是怎么定下来的 | — |
| 55 | 控制台与复盘三则 | — |

其中 04–07、18、27、41–45 这十一课还有**留白版习题**（`labs/exercises/`）：同样的样本、同样的期望输出，只留一个函数给读者填，`make check` 逐字节比对。四个习题对应 `01-portstate`、`07-counters`、`11-endpoint`、`15-tools-parse`。

另有两处不在上表里：`labs/13-pipeline` 的 `-only capacity` 段服务 49 课与 53 课的账，`labs/04-ecmp-hash` 的三条覆盖率曲线是 50 课那三次迭代的技术底稿。
