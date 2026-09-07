# Positioning

Where Aitra Meter fits in the observability landscape, and how it differs from the tools it is most often compared to.

The one-sentence version:

> Aitra Meter is a Prometheus exporter for AI inference efficiency.

It measures joules per output token (J/token) — the metric DCGM Exporter and OpenCost do not produce — and exposes it alongside cost and carbon derivations in a format compatible with kube-prometheus-stack.

---

## The comparison at a glance

| Tool | What it measures | Unit of analysis |
|---|---|---|
| [DCGM Exporter](https://github.com/NVIDIA/dcgm-exporter) | GPU hardware metrics | GPU device |
| [kube-state-metrics](https://github.com/kubernetes/kube-state-metrics) | Kubernetes resource state | Kubernetes object |
| [OpenCost](https://opencost.io/) | Cloud and GPU-hour cost allocation | Workload × dollar |
| [Kepler](https://sustainable-computing.io/) | Energy consumption per container | Container × joule |
| **Aitra Meter** | AI output efficiency | Output token × joule (J/token, $/1M tokens, gCO₂/token) |

None of these tools replaces another. A GPU cluster running inference workloads can usefully run all five: DCGM Exporter for hardware health, kube-state-metrics for cluster state, OpenCost for spend, Kepler for general energy accounting, and Aitra Meter for what the energy actually bought — tokens.

---

## Versus each adjacent tool

### DCGM Exporter

DCGM Exporter exposes GPU hardware telemetry: utilisation, power draw, temperature, memory, ECC errors. It knows everything about the GPU and nothing about the workload's output. It can tell you a GPU drew 400 W; it cannot tell you whether that 400 W produced ten tokens or ten thousand.

Aitra Meter sits one layer up: it joins energy (from NVML, AMD SMI, Zeus, or DCGM Exporter itself) with token counts from the inference server and publishes the ratio. In fact, one of Aitra Meter's energy providers scrapes a node-local dcgm-exporter — on clusters that already run DCGM Exporter, Aitra Meter consumes it rather than competing with it. See the [energy providers guide](../guides/energy-providers.md).

### kube-state-metrics

kube-state-metrics converts the state of Kubernetes API objects — deployments, pods, nodes, resource requests — into metrics. It measures what the cluster *is*, not what the cluster *does*. There is no overlap with Aitra Meter beyond both being Prometheus exporters; they answer different questions and are expected to coexist in any kube-prometheus-stack install.

Aitra Meter does read Kubernetes object metadata (namespace, workload, model annotations) — but only as attribution labels on its measurements, not as metrics in themselves.

### OpenCost

OpenCost allocates cloud cost to workloads: it answers "what did this namespace's GPU-hours cost?" Aitra Meter answers "what did this namespace's tokens cost to produce?" The difference matters because the two can move in opposite directions: a namespace can look cheaper by GPU-hour while being more expensive per token, when its serving configuration (batching, quantisation, model choice) is less efficient.

A useful shorthand is "think of it like OpenCost, but measuring output efficiency rather than cloud spend." Note that this is an analogy, not a substitution: OpenCost allocates spend, Aitra Meter measures output efficiency, and the intended state is both installed together. For that reason "OpenCost for AI inference" should not be used as a standalone tagline — it suggests the tools are alternatives when they are complements. Use it only in explanatory contexts where the distinction is spelled out.

Running both is documented in the [OpenCost integration guide](../guides/opencost-integration.md), including a combined Grafana panel (`examples/grafana/opencost-aitra-combined.json`).

### Kepler

Kepler (Kubernetes Efficient Power Level Exporter) is the tool closest to Aitra Meter in subject matter: both attribute energy to Kubernetes workloads. Kepler estimates power for *every* container on a node — CPU, memory, and GPU — using kernel-level signals and power models, and stops at joules.

Aitra Meter is narrower and goes one step further: it only measures GPU inference workloads, reads GPU energy counters directly at measurement-window boundaries (rather than estimating from activity models), and divides by output tokens to produce J/token. If you need cluster-wide energy accounting for all workloads, Kepler is the right tool. If you need to know how efficiently your inference stack converts energy into tokens — and what that costs in dollars and grams of CO₂ — that is Aitra Meter's job. Running both is reasonable: Kepler for the whole-cluster energy picture, Aitra Meter for the inference-efficiency picture.

---

## What Aitra Meter is not

Consistent with the [roadmap](../../ROADMAP.md) non-goals:

- **Not a cost allocator.** It derives $/1M tokens from measured energy and a configured electricity price; it does not ingest cloud billing data. That is OpenCost's job.
- **Not a general energy meter.** It measures GPU inference workloads, not every container. That is Kepler's job.
- **Not a hardware monitor.** It exposes idle power and serving ratios as by-products of measurement, but GPU health telemetry is DCGM Exporter's job.
- **Not a router or optimiser.** It measures; it does not act on measurements.

---

## Copy guidelines

When writing about Aitra Meter (README, website, talks, blog posts):

- Headline: **"A Prometheus exporter for AI inference efficiency."** Accurate and searchable.
- Lead with the metric: J/token, with $/1M tokens and gCO₂/token as derivations.
- "The metric DCGM Exporter and OpenCost do not produce" is a factual comparison and acceptable.
- "OpenCost for AI inference" only in explanatory contexts, never as the headline (see above).
- No superlatives. No "the only" claims. No "first" claims without a cited reference.
- Do not claim validation that has not happened. Hardware validation status lives in the [roadmap](../../ROADMAP.md); copy must not run ahead of it.

---

## Proposed website copy (aitra.ai)

The aitra.ai website is maintained outside this repository. The copy below is the proposed replacement for the Aitra Meter section, kept here so website updates can be reviewed against the same no-overclaiming principle as the README.

**Hero headline:**

> Aitra Meter — a Prometheus exporter for AI inference efficiency.

**Hero subheadline:**

> Measure joules per output token across every workload, model, and GPU in your cluster. One Helm install. No code changes.

**Positioning block (below the hero):**

> Your monitoring stack already tells you what your GPUs are doing and what they cost:
>
> - DCGM Exporter → GPU hardware metrics
> - kube-state-metrics → Kubernetes resource state
> - OpenCost → GPU-hour cost
> - **Aitra Meter → AI output efficiency (J/token, $/1M tokens, gCO₂/token)**
>
> Aitra Meter adds the missing column: what the energy actually produced.

**Explanatory paragraph (optional, for a "how it compares" page):**

> Think of it like OpenCost, but measuring output efficiency rather than cloud spend. OpenCost tells you what a namespace's GPU-hours cost; Aitra Meter tells you what its tokens cost to produce. They are designed to run together — the combined view shows when cheaper hardware is producing more expensive tokens.
