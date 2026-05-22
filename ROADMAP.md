# Roadmap

## Phase 1 — Single cluster (current)

Focus: get the measurement right for one cluster before adding complexity.

- [x] EnergyProvider and InferenceMetricsProvider pluggable interfaces
- [x] Zeus energy provider (default)
- [x] vLLM inference provider (default)
- [x] generic-prometheus inference provider (TGI, SGLang, Ollama, Triton)
- [ ] Measurement agent DaemonSet implementation
- [ ] Aggregation service implementation (J/token, attribution, calibration)
- [ ] ClickHouse schema and writer
- [ ] Prometheus metrics exposition
- [ ] MeasurementPolicy and SiteConfig CRD controllers
- [ ] Dashboard — View 1: J/token by workload × model × hardware
- [ ] Dashboard — View 2a: Cluster consumption over time
- [ ] Dashboard — View 2b: Consumption by series
- [ ] Dashboard — View 3: Namespace chargeback
- [ ] Dashboard — View 4: Idle consumption
- [ ] Dashboard — View 5: Carbon and cost per token
- [ ] Helm chart complete with all templates
- [ ] Grafana dashboard JSON pre-built
- [ ] Prometheus alerting rules reference
- [ ] v0.1.0 release

## Phase 2 — Multi-cluster federation

- [ ] Thanos sidecar integration for cross-cluster Prometheus federation
- [ ] Cross-cluster J/token comparison view
- [ ] Supercluster topology (cross-cluster tensor parallelism)
- [ ] Workload routing recommendation view (based on J/token across clusters)
- [ ] Prefill vs decode energy separation
- [ ] Aitra Benchmark publication and integration
- [ ] OpenCost MCP server formal integration
- [ ] Per-request attribution within continuous batching (requires Aitra Gateway)
- [ ] Hardware efficiency drift alerting via DCIM API
- [ ] Langfuse quality + efficiency correlation views
- [ ] v1.0.0 release

## Phase 3 — Fleet intelligence

- [ ] Cross-site carbon-aware workload routing
- [ ] Capacity planning recommendations based on historical J/token
- [ ] Multi-cloud TCO normalisation
- [ ] ESG reporting export (GRI, CDP compatible)

## What is not on the roadmap

- Routing logic — delegated to Aitra Gateway, LiteLLM, Envoy, Kong
- Fleet lifecycle, RMA tracking — DCIM territory
- Real-time budget enforcement — requires Aitra Gateway
