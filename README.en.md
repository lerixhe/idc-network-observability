# IDC Network Observability · IDC 网络可观测入门

> Ports, probes, collection, topology, pipelines, alerts, localization.

[![Open in GitHub Codespaces](https://github.com/codespaces/badge.svg)](https://codespaces.new/lerixhe/idc-network-observability)

**Status: v0.1 skeleton** — the opening two lessons are written, all ten part overviews are in place,
`labs/00-setup` runs, the remaining lessons are planned.
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

Three layers of "is the network up", the ground the whole book stands on:

| Layer | What it measures | Typical false alarm |
|---|---|---|
| Device liveness | Can you ping the management IP, log in over SSH/SNMP | Device CPU saturated; ACL blocking the management plane |
| Port state | `ifOperStatus`, optical power | Admin shutdown counted as a fault; only one side observed |
| Path quality | Latency, loss, jitter between two points | Broken probe; ECMP covered on one branch only |

The three layers routinely disagree. A platform's job is to align them, not to let each speak alone.

## Structure

Nine parts by system, one part by a real build. Grouped by system rather than by data-flow stage —
port monitoring, SNMP collection and flow data are three different systems with different
protocols, code and failure modes.

| Part | Lessons | Topic | Lab |
|---|---|---|---|
| 1 · The site | 00–03 | IDC map, an on-call day, nine systems and three layers | — |
| 2 · Ports & device liveness | 04–09 | Six causes of down, flapping, optics, three collection paths | `01-portstate` `02-device-alive` |
| 3 · Active probing | 10–15 | Evidence packs, probe mesh, ECMP, INT, probe trust, writing a probe | `03`–`05` `00-setup` |
| 4 · Device metrics | 16–19 | OIDs and table walks, CPU protection, counter wrap, gNMI | `06-snmp-walk` `07-counters` |
| 5 · Flow data | 20–22 | sFlow vs NetFlow, sampling traps, UDP has no retransmit | `08-flow` |
| 6 · CMDB & topology | 23–29 | Inventory, links, two topology graphs, IP-to-port, reconciliation | `09`–`11` |
| 7 · Platform engineering | 30–35 | Two-way control of 100k probes, watermarks, time-series design, cost | `10-task` `11-pipeline` |
| 8 · Metrics & alerting | 36–40 | Conflicting definitions, cardinality, parent-child suppression, baselines | `12-alert` |
| 9 · Troubleshooting | 41–48 | Symptom-to-tool, capture, time alignment, bisection, a full case | `15`–`18` |
| 10 · Case study | 49–55 | A link-quality platform from zero: constraints, decisions, costs, incidents | `19` `20` |

The case study is 12% of the book, deliberately not a third. It only adds three things — trade-offs,
costs, incident chains. Stretch it further and you end up restating mechanics the reader already
knows.

Every lesson carries one hard checkable outcome. If you cannot state it, the lesson is still an idea.

## Layout

```
.
├── site/                  VitePress site (content under site/docs, one directory per part)
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

## Roadmap

| Version | Scope |
|---|---|
| v0.1 | Skeleton: site, ten part overviews, opening two lessons, `labs/00-setup`, CI (current) |
| v0.2 | Parts 1–2 written (00–09) + `01-portstate`, `02-device-alive` |
| v0.3 | Parts 3–4 written (10–19) + `03`–`07` |
| v0.4 | Parts 5–6 written (20–29) + `08`–`11` |
| v0.5 | Parts 7–8 written (30–40) + `12`–`14` |
| v0.6 | Part 9 written (41–48) + `15`–`18` |
| v1.0 | Part 10 written (49–55) + `19`, `20`, appendix; structure frozen |

Only `00-setup` exists as code today. Every lab is a separate module, must run offline, and ships
traps on purpose — a deliberately dirty inventory, misaligned samples. Stepping in once beats
reading the chapter twice.

## License

Prose [CC BY-SA 4.0](LICENSE), code [MIT](LICENSE-CODE).
