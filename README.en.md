# IDC Network Observability · IDC 网络可观测入门

> Ports, probes, collection, topology, pipelines, alerts, localization.

[![Open in GitHub Codespaces](https://github.com/codespaces/badge.svg)](https://codespaces.new/lerixhe/idc-network-observability)

**56 lessons (00–55) written, 21 Go labs runnable.** Site: <https://lerixhe.github.io/idc-network-observability/> · [中文 README](README.md)

This is Book 1 of the series. Book 2 is [AIDC Observability](https://github.com/lerixhe/aidc-observability/):
lossless AI fabrics — RoCEv2, PFC/ECN failure modes, gNMI/INT telemetry, RCA automation.
Both books share one structure; read this one first.

The course text is in Chinese. This English README exists so non-Chinese readers can evaluate the
repo quickly; the labs, their code and their outputs are language-neutral.

---

## Who this is for

Backend developers moving into observability platform engineering. You can write Go or Java, you
have stared at dashboards, you know what a TCP handshake is, and you have never configured a switch.

This book does not teach OSPF path selection or how to log into a device. It teaches turning a
network site into a system you can **query, alert on, and trace back**: how to collect, how to
define a metric, how to read a curve, how to narrow a fault to a segment. You end up with a set
of code that runs.

## The problem

Application monitoring sees symptoms, not the scene. A service's p99 climbs from 210 ms to 780 ms,
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
| 2 · Ports & device liveness | 04–09 | Six causes of down, flapping, optics, three collection paths, watching ten thousand devices | `01-portstate` `02-device-alive` |
| 3 · Active probing | 10–15 | Evidence packs, probe matrix, ECMP, INT, probe trust, writing a probe | `03`–`05` `00-setup` |
| 4 · Device metrics | 16–19 | OIDs and table walks, CPU protection, counter wrap, gNMI | `06-snmp-walk` `07-counters` |
| 5 · Flow data | 20–22 | sFlow vs NetFlow, sampling traps, UDP has no retransmit | `08-flow` |
| 6 · CMDB & topology | 23–29 | Inventory, links, two topology graphs, IP-to-port, reconciliation, events | `09`–`11` |
| 7 · Platform engineering | 30–35 | Two-way control of 100k probes, watermarks, time-series design, cost | `10-task` `11-pipeline` |
| 8 · Metrics & alerting | 36–40 | Conflicting definitions, cardinality, parent-child suppression, baselines, microbursts | `12-alert` |
| 9 · Troubleshooting | 41–48 | Symptom-to-tool, capture, time alignment, bisection, an end-to-end case | `15`–`18` |
| 10 · Case study | 49–55 | A link-quality platform from zero: constraints, decisions, costs, incidents | `19` `20` |

## Labs

21 independent Go modules under `labs/`, from `00-setup` to `20-probe-security`. Go 1.22 and the
standard library only: no privileges, no network, runs offline.

```bash
cd labs/01-portstate && make run     # decision tree for six causes of a down port
cd labs/14-alert && make run         # parent-child suppression: 87 alerts down to 5
./scripts/check-labs.sh              # go vet + go test, one lab at a time
```

Every lab's sample data is deliberately dirty: the same fiber judged differently at each end, a
device at full CPU mistaken for a reboot, one machine with two names and three zombies. Each lab
README carries a "traps left on purpose" section — step in once, then check the answer.

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

The Codespaces badge above leads to a container with Go preinstalled.

## License

Prose [CC BY-SA 4.0](LICENSE), code [MIT](LICENSE-CODE).

## Contributing

The content is complete; corrections are welcome as issues or pull requests. Name the lesson and
attach a checkable source:

- Mechanism and protocol claims need a verifiable source (RFC / IEEE standard / paper / vendor docs)
- Examples use RFC 5737 documentation addresses. Do not submit real device configurations,
  internal addresses, or unpublished architecture details
- Local development: `cd site && npm ci && npm run dev`
