# IDC Network Observability · IDC 网络可观测入门

> Collect, measure, read, locate.

[![Open in GitHub Codespaces](https://github.com/codespaces/badge.svg)](https://codespaces.new/lerixhe/idc-network-observability)

**Status: v0.1 skeleton** — the opening module is written, `labs/00-setup` runs, the remaining three parts are planned.
Site: <https://lerixhe.github.io/idc-network-observability/> · [中文 README](README.md)

> Series: **Book 1** (this repo) covers observability in lossy IDC networks.
> **Book 2** is [AIDC Observability](https://github.com/lerixhe/aidc-observability):
> from lossy IDC to lossless AI fabrics. They were published in reverse order — read this one first.

---

## Who this is for

Backend developers moving into observability platform engineering. You can write Go or Java,
you have stared at dashboards, you know what a TCP handshake is, and you have never configured
a switch.

This book does not teach OSPF path selection or how to log into a device. It teaches turning a
network site into a system you can **query, alert on, and trace back**: how to collect, how to
define a metric, how to read a curve, how to narrow a fault to a segment. The deliverable is
running code.

## The problem

Application monitoring sees symptoms, not the scene. A service's p99 climbs from 210ms to 780ms,
tracing blames a downstream call, host CPU sits at 40%, the alert list is clean — and the truth is
0.3% packet loss between two servers in the same rack. Three structural differences:

| | Application monitoring | Network observability |
|---|---|---|
| What you observe | Your own process | Shared links and ports; one bad port hurts dozens of services |
| Where data comes from | Passive instrumentation on requests | Active measurement; your sampling period decides what you can see |
| Causal chain | Service to service | Service → NIC → TOR → Spine → egress, five clocks that disagree |

## Structure

```
collect  →  metrics  →  read  →  locate
raw        queryable     verdict   action
```

Four stages in series; a mistake upstream cannot be rescued downstream. Each part ships something
runnable that the next part builds on.

| Part | Topic | Lab | Status |
|---|---|---|---|
| Opening | The IDC map, why app monitoring falls short, the four stages | — | Written |
| 1 · Collect | Four data sources, SNMP to gNMI, PingMesh, a Go probe | `labs/00-setup` | Lab runs, lessons planned |
| 2 · Metrics | Definitions, labels and cardinality, aggregation, pipelines, baselines | `labs/01-metrics` | Planned |
| 3 · Read | Alert suppression, anomaly shapes, ECMP imbalance, microbursts | `labs/02-baseline` | Planned |
| 4 · Locate | Bisection, path tracing, change windows, end-to-end case | `labs/03-bisect` | Planned |

## Layout

```
.
├── site/                  VitePress site (content under site/docs)
├── labs/                  Go labs, one module each
├── scripts/               link checker and lab runner
├── LICENSE                prose: CC BY-SA 4.0
└── LICENSE-CODE           code: MIT
```

## Run it

```bash
# Site
cd site && npm ci && npm run dev

# First lab (Go 1.22+, no external dependencies)
cd labs/00-setup && make run && make test
```

The course text is in Chinese — this English README exists so non-Chinese readers can evaluate
the repo quickly; the labs, their code and their outputs are language-neutral.

## License

Prose [CC BY-SA 4.0](LICENSE), code [MIT](LICENSE-CODE).
