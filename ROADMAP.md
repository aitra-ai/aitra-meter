# Roadmap

Aitra Meter uses [GitHub Issues](https://github.com/aitra-ai/aitra-meter/issues) to track active work. This document describes current focus and future themes. See the [changelog](CHANGELOG.md) for what has shipped.

---

## Current — v0.2.x

Shipped on `main` (latest tag v0.2.4 plus unreleased items — see the [changelog](CHANGELOG.md)):

- Measurement agent (DaemonSet) with NVML (default), AMD, Zeus, and DCGM energy providers
- vLLM and generic-prometheus inference providers — compatible with TGI, SGLang, Ollama, Triton
- Aggregation service with CV gating, attribution resolution, and calibration tier lookup
- SQLite storage — embedded, pure Go, no CGO, no server
- Six dashboard views: J/token live table, cluster trend, series trend, namespace chargeback, idle consumption, carbon and cost
- Prometheus metrics with ServiceMonitor auto-registration, including cost and carbon derivations (`aitra_cost_per_million_tokens_usd`, `aitra_co2_per_token_grams`)
- OTLP export of `gen_ai.infrastructure.energy.*` metrics (opt-in)
- Pre-built Grafana dashboard, reference alerting rules, and per-namespace cost budgets
- KEDA and OpenCost integrations — reference ScaledObjects, combined cost panel, integration guides

---

## Current focus

- Hardware validation on H100 SXM5 at XFusion Singapore Open Lab
- GPU CI runner registration for continuous hardware validation
- Live demo environment ahead of KubeCon + CloudNativeCon Japan, July 2026
- Draft PR against `open-telemetry/semantic-conventions` proposing `gen_ai.infrastructure.energy.*`

---

## Near-term

- **Prefill / decode energy separation** — `aitra_j_per_prefill_token` and `aitra_j_per_decode_token` as separate series. Pending vLLM per-phase instrumentation.
- **Aitra Benchmark publication** — a first-party reference dataset for J/token across common models and hardware. Interim calibration reference is ML.ENERGY v3.0.
- **Self-calibrated tier** — populated from community production measurements after 1,000 measurement windows per combination.
- **Per-request attribution** — requires Aitra Gateway in the request path for request-level token and energy correlation.
- **MIG support — detection and topology** — identify MIG-enabled GPUs, expose topology in the DaemonSet agent, label workloads by MIG instance.
- **MIG attribution** — allocate energy across MIG instances by utilization weight, then by token count.

---

## Future — no version commitment

Sequencing depends on community adoption, partner demand, and Open Lab validation.

**Multi-cluster view** — Thanos or Prometheus `remote_write` across clusters, with per-site carbon intensity and electricity cost kept separate until attributed at org level. Includes cross-cluster J/token comparison and supercluster topology for tensor-parallel jobs spanning cluster boundaries.

**Ray support** — Ray Serve workload discovery and Ray-level J/token attribution alongside the existing namespace-level view.

**Additional inference engine adapters** — engine-specific adapters where `generic-prometheus` misses metadata, primarily TensorRT-LLM.

**OpenCost MCP server integration** — formal integration once the OpenCost MCP server stabilises.

**Langfuse quality-efficiency correlation** — J/token alongside LLM quality scores for the same model and workload.

**Hardware efficiency drift alerting** — J/token trend signal consumed by DCIM tools for capacity planning and refresh decisions.

**Slurm support** — job-level J/token attribution for HPC workloads. Outside the current Kubernetes-native architecture; listed to acknowledge demand without committing to timeline.

---

## What is not on the roadmap

- Routing logic — delegated to Aitra Gateway, LiteLLM, Envoy, or Kong
- Efficiency recommendations and model or configuration suggestions — analysis layer that sits above the meter
- Fleet lifecycle, RMA tracking, hardware refresh cycles — DCIM territory; Aitra Meter exposes a J/token drift signal for DCIM tools to consume
- Real-time budget gate enforcement — requires Aitra Gateway in the request path
- Active user identity tracking — requires Aitra Gateway to inject a user-ID header per request

---

## Contributing

Open a GitHub issue with the `roadmap` label to propose a new capability. Include the use case it enables and the component it affects. Issues with clear use cases and a willing contributor move faster.

Reach us on [CNCF Slack](https://slack.cncf.io/) in the `#soda-aitra` channel.
