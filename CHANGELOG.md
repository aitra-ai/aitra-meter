# Changelog

All notable changes to Aitra Meter are documented here.

Format follows [Keep a Changelog](https://keepachangelog.com/en/1.0.0/).
Versioning follows [Semantic Versioning](https://semver.org/).

Pre-release tags: `-alpha` tags are internal milestones. `-beta` tags are public releases with published images and Helm chart.

---

## [Unreleased]

### Added
- **DCGM energy provider** — pure-Go `EnergyProvider` that scrapes a node-local dcgm-exporter Prometheus endpoint; selectable via `-energy-provider dcgm` / `energyProvider.type: dcgm`. (#59)
- **Model-level AI efficiency metric family** — `aitra_model_tokens_total` and `aitra_model_energy_per_1m_tokens`, plus SiteConfig-driven cost/carbon derivation (`aitra_cost_per_million_tokens_usd`, `aitra_co2_per_token_grams`, `aitra_tenant_cost_usd_total`) and per-node serving/idle ratios (`aitra_gpu_serving_utilization_ratio`, `aitra_idle_time_ratio`). (#60)
- **Cost-budget and TTFT alerts** — `TenantCostBudgetExceeded` and `TTFTRegression` reference alerts, a runbook per alert, and a Helm-driven per-namespace budget mechanism (`costBudgets`). (#61)

### Fixed
- Repository builds again under `-mod=readonly` (CI default): completed `go.sum` and pruned an unused `testcontainers-go`/Docker dependency tree via `go mod tidy`.
- `generic-prometheus` provider test referenced an invalid method on `*struct{}` and did not compile — replaced with a normal test helper.
- Lint job restored: migrated `.golangci.yml` to the golangci-lint v2 schema (`version: "2"`) and fixed the issues it surfaced (gosec G112 `ReadHeaderTimeout` on the metrics server, govet copylocks in a test, staticcheck empty branch, gofmt/goimports formatting).

### Changed
- AMD SMI energy provider is now gated behind the `amd` build tag (`go build -tags amd`) with a no-cgo stub for default builds, so `go build ./...` and `go test ./...` no longer require ROCm/AMD SMI headers in CI.

---

## [0.1.0-alpha] — 2026-06-09

First tagged release. All Docker-only acceptance criteria closed.

### Added

**Measurement agent**
- Core per-node measurement loop — `BeginWindow → wait → EndWindow → compute token delta → ReportWindow` over gRPC
- First-window zero-delta handled correctly — aggregation service returns `accepted=false`, agent continues at debug
- Idle windows (zero tokens, positive joules) reported separately for idle power tracking
- Graceful shutdown — final window drained with a 5s `context.Background()` timeout before exit
- `cmd/measurement-agent/main.go` — fully wired: `--node`/`NODE_NAME`, `--energy-provider`, `--inference-provider`, `--aggregator`, `--window-seconds`, `--inference-endpoint`

**Energy providers**
- NVML energy provider — pure Go, no CGO, no Python dependency, Linux only. `DeviceGetTotalEnergyConsumption` at window boundaries, multi-device summing, idle power via `DeviceGetPowerUsage`. Integration tests with `//go:build linux && integration` tag.
- Zeus energy provider — stub (compiles, socket IPC TODOs marked). Full implementation in v0.2.0-alpha.

**Inference providers**
- vLLM inference provider — scrapes `/metrics` Prometheus endpoint, 17 table-driven tests
- Generic-prometheus inference provider — stub (compiles, configurable metric names, scraping TODOs marked). Full implementation in v0.3.0-alpha.

**Aggregation service**
- CV tracker — ring buffer, O(1), 100-window default, 3% threshold
- Attribution resolver — namespace, workload, model, hardware, precision, team, cost-centre from Kubernetes metadata
- Calibration table — four-tier lookup: `aitra_benchmark` → `reference` → `self_calibrated` → `uncalibrated`
- J/token computation — Σ energy ÷ Σ tokens per window, cluster aggregate as ratio of sums
- Full Prometheus metrics exposition — 17 metrics across agent and aggregation service
- `GET /api/v1/namespaces` chargeback handler

**Storage**
- Pluggable `Backend` interface with registry (`Register` / `New`)
- SQLite backend — pure Go via `modernc.org/sqlite`, file-backed at `/data/aitra.db`, full `WriteBatch` and `QueryChargeback`, stores all measurement records
- Memory backend — in-process, tests only
- `internal/model/record.go` — zero-dependency `MeasurementRecord`, `CalibrationTier`, `AttributionMethod` types

**Dashboard**
- View 1: J/token live table — calibration tier badges, CV stability dots, delta vs baseline
- View 2a: cluster J/token trend with time window toggle (1h / 6h / 24h / 7d)
- View 2b: per-workload × model × hardware multi-line trend
- View 3: namespace chargeback with PUE slider, cost/kWh input, CSV export — all client-side, zero re-fetch
- View 4: GPU power stacked area chart — serving vs idle
- View 5: carbon and cost per token with derivation formula inline, grid intensity source badge
- Empty state on View 1 — "Waiting for first measurement window" with getting-started pointer
- `data-testid="view-N-ready"` attributes on all views for Playwright assertions

**Infrastructure**
- `docker-compose.yml` — local dev stack: Prometheus, aggregation service (SQLite), dashboard
- `dev/seed.go` — inserts 103k synthetic rows across 30 days
- Playwright E2E tests — render time (5s), PUE slider response (<200ms), derivation formula inline, unknown workload row
- SQLite integration test — `TestChargebackQuery30Day`, 52k rows, <10s
- `helm lint` and `helm template --dry-run` added to CI pipeline

**Helm chart**
- `values.yaml`, RBAC, ServiceMonitor, MeasurementPolicy CRD, SiteConfig CRD
- `values-minimal.yaml` — six values for a working first install

**Documentation**
- Getting-started guide (15 minutes to first measurement, Zeus sidecar verification step)
- Metrics reference, configuration reference, proto reference
- `examples/inference-servers/` — TGI, SGLang, Ollama configs
- `examples/site-config-singapore.yaml`, `examples/inference-deployment-annotated.yaml`

### Fixed
- Typo `eneregyProviders` → `energyProviders` in `internal/provider/registry.go:38` caused `NewEnergy` to always return "unknown provider"
- Literal newline in `internal/provider/inference/vllm/vllm.go:95` prevented compilation
- `github.com/NVIDIA/go-nvml` uses `RTLD_DEEPBIND` (Linux-only CGO constant) — added `//go:build linux` and split side-effect import into `cmd/measurement-agent/providers_linux.go`
- Import cycle between `internal/storage` and `internal/aggregation` — resolved by extracting shared types to `internal/model`
- Graceful shutdown used cancelled context for final `ReportWindow` — fixed with `context.WithTimeout(context.Background(), 5s)`

### Changed
- Annotation prefix migrated from `aitra.io` to `aitra-ai.github.io`
- API group migrated from `aitra.io/v1alpha1` to `aitra-ai.github.io/v1alpha1`
- `aggregation.RecordWriter` interface removed — `Loop.writer` is now `storage.RecordWriter`

---

## Release cadence

Pre-1.0: tagged when a meaningful set of features is complete.
Post-1.0: minor releases on a regular cadence, patch releases as needed.
