# Roadmap

## Current release — single-cluster measurement

Aitra Meter measures J/token (joules per output token) continuously across every workload × model × hardware combination in a Kubernetes cluster. The current release ships:

- Measurement agent (DaemonSet) with Zeus (default) and NVML energy providers
- vLLM and generic-prometheus inference providers — compatible with TGI, SGLang, Ollama, Triton
- Aggregation service with CV gating, attribution resolution, and calibration tier lookup
- SQLite storage (embedded, default — all measurement records persisted)
- Six dashboard views: J/token live table, cluster trend, series trend, namespace chargeback, idle consumption, carbon and cost
- Prometheus metrics with ServiceMonitor auto-registration
- OTLP export of `gen_ai.infrastructure.energy.*` metrics (opt-in)
- Pre-built Grafana dashboard, alerting rules, KEDA ScaledObject examples, OpenCost combined panel
- Hardware-validated on H100 SXM5

See the [changelog](CHANGELOG.md) for the full list of changes per release.

## Next milestone — production hardening

- Prefill vs decode energy separation — requires per-phase token counts from vLLM or Aitra Gateway in the request path
- Self-calibrated tier populated from community production measurements
- Aitra Benchmark publication — first reference dataset for J/token across common models and hardware
- Per-request attribution within continuous batching (requires Aitra Gateway)

## Phase 2 — multi-cluster federation

- Thanos sidecar integration for cross-cluster Prometheus federation
- Cross-cluster J/token comparison view
- Supercluster topology — cross-cluster tensor-parallel measurement
- Workload routing recommendation view based on J/token across clusters
- OpenCost MCP server formal integration
- Hardware efficiency drift alerting via DCIM API
- Langfuse quality-efficiency correlation views

## Phase 3 — fleet intelligence

- Cross-site carbon-aware workload routing
- Capacity planning recommendations based on historical J/token trends
- Multi-cloud TCO normalisation
- ESG reporting export (GRI, CDP compatible)

## What is not on the roadmap

- Routing logic — delegated to Aitra Gateway, LiteLLM, Envoy, or Kong
- Fleet lifecycle, RMA tracking, hardware refresh cycles — DCIM territory; Aitra Meter exposes a J/token drift signal for DCIM tools to consume
- Real-time budget gate enforcement — requires Aitra Gateway in the request path
- Active user identity tracking — requires Aitra Gateway to inject a user-ID header per request

## Contributing to the roadmap

Open a GitHub issue with the `roadmap` label to propose a new capability. Include the use case it enables and the component it affects. Issues with clear use cases and a willing contributor move faster.
