# Aitra Meter

> Open-source Kubernetes-native AI inference energy measurement.

**J/token** — joules per output token — measured continuously across every workload × model × hardware combination in your cluster.

[![License](https://img.shields.io/badge/License-Apache_2.0-blue.svg)](LICENSE)
[![SODA Foundation](https://img.shields.io/badge/SODA-Foundation-teal.svg)](https://github.com/sodafoundation)
[![Go Report Card](https://goreportcard.com/badge/github.com/aitra-ai/aitra-meter)](https://goreportcard.com/report/github.com/aitra-ai/aitra-meter)
[![CI](https://github.com/aitra-ai/aitra-meter/actions/workflows/ci.yml/badge.svg)](https://github.com/aitra-ai/aitra-meter/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/aitra-ai/aitra-meter)](https://github.com/aitra-ai/aitra-meter/releases/latest)

---

## What it does

Prometheus measures your infrastructure. OpenTelemetry `gen_ai.*` measures your AI requests. Neither measures what the infrastructure consumed to serve those requests. `aitra_j_per_token` is the missing primitive.

Aitra Meter connects GPU hardware energy to AI output volume at the token level. It deploys entirely inside a single Kubernetes cluster — one Helm install, no infrastructure changes, no changes to inference application code.

## Quick start

Label your GPU nodes, then install:

```bash
kubectl label node <your-gpu-node> aitra-ai.github.io/gpu=true

helm repo add aitra https://aitra-ai.github.io/helm-charts
helm repo update

helm install aitra-meter aitra/aitra-meter \
  --namespace aitra-system --create-namespace \
  --set cluster.name=my-cluster \
  --set siteConfig.electricityCostPerKwh=0.12 \
  --set siteConfig.carbonIntensityFallback=400
```

All pods reach Running within 60 seconds:

```bash
kubectl get pods -n aitra-system
```

Then open the dashboard:

```bash
kubectl port-forward -n aitra-system svc/aitra-meter-dashboard 3000:3000
# http://localhost:3000
```

See the [getting started guide](docs/guides/getting-started.md) for a complete walkthrough.

## Architecture

```
GPU hardware
  └─ NVML (default — NVIDIA GPUs, pure Go, no sidecar)
  └─ AMD (AMD GPUs via libamd_smi.so, ROCm 6.x+)
  └─ Zeus (community extension — operators running zeusd)

Inference servers
  └─ vLLM / TGI / SGLang / Ollama (/metrics endpoint)

                    │ gRPC WindowReport
                    ▼
          aggregation-service
          J/token · attribution · calibration · CV gating
                    │
          ┌─────────┴──────────┐
          ▼                    ▼
     Prometheus           SQLite (default)
     (live metrics)       (chargeback history)
          │
     ┌────┴──────────────┐
     ▼         ▼         ▼
  Dashboard  Grafana  OTel Collector
  (6 views)           (OTLP export)
```

## Metrics

| Metric | Description |
|---|---|
| `aitra_j_per_token` | Joules per output token — workload × model × hardware |
| `aitra_cluster_j_per_token` | Cluster-wide J/token (Σ energy ÷ Σ tokens) |
| `aitra_co2_per_token_grams` | gCO₂ per token (J/token × grid intensity) |
| `aitra_cost_per_million_tokens_usd` | $/M tokens (J/token × electricity cost) |
| `aitra_idle_power_watts` | GPU power draw when no inference is running |
| `aitra_idle_time_ratio` | Fraction of last hour spent idle per node |
| `aitra_tokens_per_joule` | Inverse efficiency — output per joule |
| `aitra_measurement_cv` | Rolling CV over last 100 windows — stability signal |

Full metrics reference: [docs/reference/metrics.md](docs/reference/metrics.md)

## Dashboard — six views

| View | Question answered |
|---|---|
| 1. J/token live table | For each workload × model × hardware — what is the current J/token? |
| 2a. Cluster trend | What is the cluster's aggregate efficiency trend? |
| 2b. Series trend | How is each combination trending individually? |
| 3. Namespace chargeback | What does each namespace owe this billing period? |
| 4. Idle consumption | How much energy is consumed while producing no tokens? |
| 5. Carbon and cost | What is the carbon and energy cost per token? |

## CNCF integrations

| Project | Integration |
|---|---|
| Prometheus | ServiceMonitor auto-registers with kube-prometheus-stack on install |
| OpenTelemetry | OTLP export of `gen_ai.infrastructure.energy.*` metrics — opt-in |
| Grafana | Pre-built dashboard JSON, auto-provisioned via sidecar |
| KEDA | Scale on `aitra_j_per_token` and `aitra_idle_time_ratio` — reference ScaledObjects in `examples/keda/` |
| OpenCost | Complementary — OpenCost = $/GPU-hr, Aitra Meter = $/M tokens — combined panel in `examples/grafana/` |
| Envoy | Access log ingestion for attribution enrichment on Istio/Envoy mesh clusters |

## Contributing

There are three high-value contribution paths that require no changes to core code:

**Add a new inference server** — implement `InferenceMetricsProvider`, configure metric names for your server's Prometheus endpoint. The generic-prometheus provider is the pattern; TGI, SGLang, and Ollama configs are in `examples/inference-servers/`.

**Add a new energy backend** — implement `EnergyProvider` and register via `init()`. Zeus and NVML are the working examples.

**Add a new measurement agent** — any process that sends `WindowReport` messages to the aggregation service via gRPC appears in the dashboard. The proto is in `api/proto/measurement/v1/`. Write your agent in any language.

See [CONTRIBUTING.md](CONTRIBUTING.md) for full guidance.

## Documentation

### Guides
- [Getting started](docs/guides/getting-started.md) — install and first measurement in 15 minutes
- [Writing a provider](docs/guides/writing-a-provider.md) — add a new inference server or energy backend
- [Operations](docs/guides/operations.md) — upgrading, scaling, air-gapped install
- [KEDA integration](docs/guides/keda-integration.md)
- [OpenCost integration](docs/guides/opencost-integration.md)
- [llm-d integration](docs/guides/llm-d-integration.md) — per-phase prefill/decode J/token for disaggregated serving
- [Troubleshooting](docs/guides/troubleshooting.md)

### Reference
- [Metrics reference](docs/reference/metrics.md) — all Prometheus metrics, types, labels
- [Configuration reference](docs/reference/configuration.md) — all Helm values and CRD fields
- [HTTP API reference](docs/reference/api.md) — aggregation service API
- [Compatibility](docs/reference/compatibility.md) — provider × hardware × inference server matrix
- [Glossary](docs/reference/glossary.md)

### Design
- [Architecture Decision Records](docs/adr/)
- [Technical Specification](docs/spec/aitra-meter-spec-v1.0.md)

### Project
- [Roadmap](ROADMAP.md)
- [Changelog](CHANGELOG.md)
- [Security policy](SECURITY.md)
- [Contributing](CONTRIBUTING.md)
- [Governance](GOVERNANCE.md)

## Project status

Single-cluster measurement is complete and hardware-validated. This project follows semantic versioning. Pre-1.0 minor versions may include interface changes — see the [changelog](CHANGELOG.md) and [roadmap](ROADMAP.md).

## Governance

Aitra Meter is a [SODA Foundation](https://github.com/sodafoundation) project.

## License

Apache 2.0 — see [LICENSE](LICENSE).
