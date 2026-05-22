# Aitra Meter

> Open-source Kubernetes-native AI inference efficiency measurement.

**J/token** — joules per output token — measured continuously across every workload × model × hardware combination in your cluster.

[![License](https://img.shields.io/badge/License-Apache_2.0-blue.svg)](LICENSE)
[![SODA Foundation](https://img.shields.io/badge/SODA-Foundation-teal.svg)](https://sodafoundation.io)
[![Project Status](https://img.shields.io/badge/status-pre--release-orange.svg)]()

---

## What it does

Aitra Meter is the missing observability layer for AI inference. GPU utilisation is not inference efficiency. Throughput is not cost per token. Node-hour billing is not output economics. Aitra Meter connects hardware energy consumption to AI output volume at the token level — the primitive that was missing from the CNCF observability stack.

It deploys entirely inside a single Kubernetes cluster. One Helm install. No infrastructure changes required.

## The measurement frame

Every J/token measurement is tagged with three dimensions:

- **Workload** — chat · code · reasoning (from pod annotation `aitra.io/workload`)
- **Model** — from vLLM metric label
- **Hardware** — from Kubernetes node label

## Quick start

```bash
helm repo add aitra https://charts.aitra.io
helm install aitra-meter aitra/aitra-meter \
  --namespace aitra-system --create-namespace \
  --set cluster.name=my-cluster \
  --set siteConfig.gridZone=SG \
  --set siteConfig.electricityCostPerKwh=0.12
```

## What gets measured

| Metric | Description |
|---|---|
| `aitra_j_per_token` | Joules per output token by workload × model × hardware |
| `aitra_cluster_j_per_token` | Cluster-wide J/token (Σ energy ÷ Σ tokens) |
| `aitra_co2_per_token_grams` | gCO₂ per token (J/token × grid intensity) |
| `aitra_cost_per_million_tokens_usd` | $/M tokens (J/token × electricity cost) |
| `aitra_idle_power_watts` | GPU power draw when no inference is running |
| `aitra_namespace_energy_joules_total` | Total energy consumed per namespace |

## Architecture

```
GPU nodes (DaemonSet · NVML · Zeus)
vLLM pods (/metrics · token count)
        │
        ▼
Aitra Meter — aggregation service
(J/token · attribution · calibration · namespace resolution)
        │
   ┌────┴────┐
   ▼         ▼
Prometheus  ClickHouse
   │         │
   └────┬────┘
        ▼
Dashboard · Grafana · OTel · Lago
```

## CNCF integrations

| Project | Role |
|---|---|
| Prometheus | ServiceMonitor auto-registers with kube-prometheus-stack |
| OpenTelemetry | Collector sidecar for OTel-native stacks |
| Envoy | Access log ingestion + ext_proc/ext_authz via Aitra Gateway |
| OpenCost | Complementary — OpenCost = $/GPU-hr, Aitra Meter = $/M tokens |
| KEDA | Scale inference workloads based on J/token and idle metrics |
| Thanos | Phase 2 multi-cluster federation |

## Dashboard views — Phase 1

| View | Question answered |
|---|---|
| 1. J/token table | For each workload × model × hardware, what is the current J/token? |
| 2a. Cluster over time | What is the cluster's energy consumption trend? |
| 2b. By series | How is each combination trending individually? |
| 3. Namespace chargeback | What does each namespace owe this billing period? |
| 4. Idle consumption | How much energy is consumed while producing no tokens? |
| 5. Carbon and cost | What is the carbon and energy cost per token? |

## Documentation

- [Technical Specification v1.0](docs/spec/aitra-meter-spec-v1.0.md)
- [Project Blueprint](docs/blueprint/aitra-meter-blueprint.md)
- [Architecture Decision Records](docs/adr/)
- [Contributing](CONTRIBUTING.md)
- [Governance](GOVERNANCE.md)

## Project status

Phase 1 (single-cluster) is in active development. See the [Technical Specification](docs/spec/aitra-meter-spec-v1.0.md) for full scope and acceptance criteria. Phase 2 (multi-cluster federation, Thanos, supercluster topology) is planned.

## Governance

Aitra Meter is a [SODA Foundation](https://sodafoundation.io) project. Research association: Tsinghua University / LF Research. Infrastructure: XFusion Singapore Open Lab.

## License

Apache 2.0 — see [LICENSE](LICENSE).
