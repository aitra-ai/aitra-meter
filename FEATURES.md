# Aitra Meter — Feature List

A cumulative inventory of everything Aitra Meter does, from the `0.1.0` foundation
through the `0.2.x` line to the current `0.3.0` additions. Era tags — *(0.1)*,
*(0.2)*, *(0.3)* — indicate roughly when each capability landed. For the
chronological record see [CHANGELOG.md](CHANGELOG.md).

## Core measurement agent (per-node DaemonSet)

- Per-node measurement loop: `BeginWindow → wait → EndWindow → token delta → ReportWindow` over gRPC *(0.1)*
- First-window zero-delta handled correctly (`accepted=false`, continues) *(0.1)*
- Idle windows (zero tokens, positive joules) reported separately for idle-power tracking *(0.1)*
- Graceful shutdown — final window drained with a 5s timeout *(0.1)*
- Fully-wired CLI: `--node`/`NODE_NAME`, `--energy-provider`, `--inference-provider`, `--aggregator`, `--window-seconds`, `--inference-endpoint` *(0.1)*

## Energy providers (pluggable `EnergyProvider`)

- **NVML** (default) — NVIDIA's official Go binding; hardware energy accumulator (`nvmlDeviceGetTotalEnergyConsumption`), multi-device summing, idle power *(0.1 / 0.2)*
- **AMD** — CGO binding to `libamd_smi.so` (`amdsmi_dev_get_energy_count`, power fallback); gated behind the `amd` build tag *(0.2 / 0.3)*
- **Zeus** — community extension, implemented via `zeusd` Unix-socket IPC *(0.3)*
- **DCGM** — scrapes a node-local `dcgm-exporter` Prometheus endpoint (vendor-neutral NVIDIA path) *(0.3)*
- Registry + `RegisterEnergy` extension point for custom providers *(0.1)*

## Inference providers (pluggable `InferenceMetricsProvider`)

- **vLLM** — scrapes `/metrics` *(0.1)*
- **generic-prometheus** — configurable metric names for TGI/SGLang/Ollama/Triton/custom; multi-series summing via shared `promtext` parser (correct for tensor-parallel / sharded deployments) *(0.2 / 0.3)*

## Aggregation service

- CV tracker — O(1) ring buffer, 100-window default, 3% stability threshold *(0.1)*
- Attribution resolver — namespace, workload, model, hardware, precision, team, cost-centre from Kubernetes metadata *(0.1)*
- Calibration table — four-tier lookup (`aitra_benchmark → reference → self_calibrated → uncalibrated`) *(0.1)*
- J/token computation (Σ energy ÷ Σ tokens; cluster aggregate as ratio of sums) *(0.1)*
- `GET /api/v1/namespaces` chargeback API; `/healthz`, `/readyz` *(0.1)*
- Node hardware tier resolved separately from the scheduling label *(0.3)*
- Per-GPU energy attribution model — sum across GPUs only for TP>1 (ADR-0007) *(0.3)*

## Metrics (Prometheus exposition)

- Full metric set across agent + aggregation (J/token, energy, tokens, CV, idle power) *(0.1)*
- Derived efficiency metrics: `aitra_tokens_per_joule`, `aitra_gpu_utilization_efficiency` *(0.2)*
- Model-level efficiency family: `aitra_model_tokens_total`, `aitra_model_energy_per_1m_tokens` *(0.3)*
- Cost/carbon derivation (SiteConfig-driven): `aitra_cost_per_million_tokens_usd`, `aitra_co2_per_token_grams`, `aitra_tenant_cost_usd_total` *(0.3)*
- Serving/idle ratios: `aitra_gpu_serving_utilization_ratio`, `aitra_idle_time_ratio` *(0.3)*

## Storage

- Pluggable `Backend` interface + registry *(0.1)*
- **SQLite** backend (pure-Go `modernc.org/sqlite`, file-backed, full `WriteBatch` / `QueryChargeback`) *(0.1 / 0.2)*
- **Memory** backend (tests) *(0.1)*

## Alerting

- Reference alerting rules: `AIEfficiencyRegression`, `GPUIdleExcessive`, `MeasurementUnstable` *(0.2)*
- `TenantCostBudgetExceeded` + `TTFTRegression`, one runbook per alert, Helm `costBudgets` per-namespace budget mechanism *(0.3)*

## Dashboards

- Built-in Next.js dashboard — J/token live table (calibration / CV badges), cluster & per-workload trends, chargeback with PUE slider + CSV export, GPU power stacked area, carbon/cost views *(0.1)*
- Full metric coverage + faithful Grafana-graph mirror; Vercel-deployable behind a tunnel *(0.3)*
- Pre-built Grafana dashboard JSON *(0.2)*; Grafana as the **default** surface with audience-specific dashboards + Helm auto-provisioning *(0.3)*

## Integrations

- **OpenTelemetry / OTLP** — export of `gen_ai.infrastructure.energy.*` metrics (opt-in) *(0.2)*
- **OpenCost** — cost-per-token recording rule + derived panel column + guide *(0.3)*
- **KEDA** — scale-on-idle and efficiency-regression `ScaledObject` examples + guide *(0.2)*

## Kubernetes / Helm / packaging

- Helm chart: `values.yaml`, RBAC, ServiceMonitor, `MeasurementPolicy` + `SiteConfig` CRDs, `values-minimal.yaml` *(0.1)*
- `docker-compose.yml` local dev stack + synthetic seed data + Playwright E2E *(0.1)*
- Release plumbing: Dockerfiles, unpinned image tags, chart bumped to 0.8.0; Go 1.26 base images; CI green under golangci-lint v2 *(0.3)*

## Docs & design

- Getting-started, metrics / configuration / compatibility references, provider-writing guide *(0.1–0.3)*
- Architecture Decision Records, including ADR-0006 (vendor energy APIs) and ADR-0007 (per-GPU attribution) *(0.2–0.3)*

---

## Not yet on `main`

Open PRs that would extend this list once merged:

- Kepler energy provider (#67)
- SGLang / Triton inference providers (#69)
- Per-phase prefill/decode attribution (#70)
- MIG per-slice attribution (#71)
