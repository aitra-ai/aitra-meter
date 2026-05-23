# Changelog

All notable changes to Aitra Meter will be documented here.

Format follows [Keep a Changelog](https://keepachangelog.com/en/1.0.0/).
Versioning follows [Semantic Versioning](https://semver.org/).

---

## [Unreleased] — v0.1.0-alpha pending GPU node verification

### Added

**Measurement agent**
- `internal/agent/loop.go`: core per-node measurement loop — `BeginWindow → wait → EndWindow → compute token delta → ReportWindow` over gRPC
- First-window zero-delta handled correctly: aggregation service returns `accepted=false`; loop logs at debug and continues (not an error)
- Idle windows (zero tokens) sent so idle consumption dashboard has data
- Graceful shutdown: final window drained with a 5 s `context.Background()` timeout before exit
- `cmd/measurement-agent/main.go`: fully wired — `--node`/`NODE_NAME`, `--energy-provider`, `--inference-provider`, `--aggregator`, `--window-seconds`, `--inference-endpoint`

**Storage**
- `internal/model/record.go`: zero-dependency package for `MeasurementRecord`, `CalibrationTier`, `AttributionMethod` — breaks the `storage ↔ aggregation` import cycle
- `internal/storage/`: pluggable `Backend` interface with registry (`Register` / `New`)
- `internal/storage/clickhouse/`: full ClickHouse backend with `QueryChargeback`, `WriteBatch`, `init()` auto-registration
- `internal/storage/memory/`: in-memory backend for tests
- `internal/storage/duckdb/`: stub backend (compiles, errors gracefully, TODOs marked)
- `cmd/aggregation-service/main.go`: backend selected by `STORAGE_BACKEND` env var; falls back to memory if init fails; `GET /api/v1/namespaces` chargeback handler

**Dashboard (Next.js 16.2.6, Tailwind v4)**
- View 1: live J/token table — tier badges, stability dots, delta vs baseline
- View 2a: cluster average J/token trend line chart with time window toggle (1h/6h/24h/7d)
- View 2b: per-workload × model × hardware multi-line chart
- View 3: 30-day namespace chargeback (proxies to aggregation service); PUE slider, cost/kWh input, CSV export — all derived client-side, zero re-fetch on slider change
- View 4: GPU power stacked area chart — serving (blue) vs idle (gray)
- View 5: carbon and cost per token with derivation formula shown inline under each value (AC-7); grid intensity source badge; fallback to user-adjustable input when Prometheus metric absent (AC-11 fallback)
- `data-testid="view-N-ready"` on all views for Playwright AC-5 assertions
- `dashboard/app/api/chargeback/route.ts` proxies to aggregation service `/api/v1/namespaces`

**Infrastructure**
- `docker-compose.yml`: local dev stack (prometheus, clickhouse, aggregation-service, dashboard)
- `dev/seed.go`: inserts 103k synthetic rows across 30 days for AC-5 and AC-11 testing
- `dashboard/Dockerfile`: three-stage build with Next.js standalone output
- `dashboard/e2e/views.spec.ts`: Playwright tests for AC-5 (5 s render), AC-6 (PUE slider <200 ms), AC-7 (formula inline), AC-8 (unknown workload styling)
- `internal/storage/clickhouse/writer_integration_test.go`: `TestChargebackQuery30Day` — AC-11 (30-day chargeback query <10 s with 52k rows, via testcontainers)
- ADR 0005: pluggable storage backend

**Aggregation service**
- `internal/aggregation/loop.go`: CV tracker (ring buffer, O(1)), attribution resolver, calibration table
- `internal/aggregation/loop_test.go`: AC-3 (`TestLoopClusterJPerTokenIsSumOfEnergyDividedBySumOfTokens`), AC-4 (`TestLoopAttributionMethodNeverEmpty`)
- `internal/clickhouse/` deleted; superseded by `internal/storage/clickhouse/`

**Providers**
- vLLM inference provider with 17 table-driven tests
- NVML energy provider with integration tests (`//go:build linux && integration`)
- CI `nvml-integration` job on `[self-hosted, gpu]` runner

### Fixed
- Typo `eneregyProviders` → `energyProviders` in `internal/provider/registry.go:38` caused `NewEnergy` to always return "unknown provider"
- Literal newline inside string literal in `internal/provider/inference/vllm/vllm.go:95` prevented compilation
- `github.com/NVIDIA/go-nvml` uses `RTLD_DEEPBIND` (Linux-only CGO constant); added `//go:build linux` to the nvml provider and split its side-effect import into `cmd/measurement-agent/providers_linux.go`
- `internal/storage/storage.go` imported `internal/aggregation` for `MeasurementRecord`, creating a cycle when `aggregation` imported `storage.RecordWriter` — resolved by extracting shared types to `internal/model`
- Graceful shutdown drain in `internal/agent/loop.go` used the cancelled `ctx`, causing the final gRPC `ReportWindow` to fail — fixed by using a fresh `context.WithTimeout(context.Background(), 5s)`

### Changed
- Annotation prefix migrated from `aitra.io` to `aitra-ai.github.io`
- API group migrated from `aitra.io/v1alpha1` to `aitra-ai.github.io/v1alpha1`
- `aggregation.CalibrationTier` and `aggregation.AttributionMethod` are now type aliases for `model.CalibrationTier` / `model.AttributionMethod` — all existing code unchanged
- `aggregation.RecordWriter` interface removed; `Loop.writer` field is now `storage.RecordWriter`

---

## Release cadence

Pre-v1.0: releases are tagged when a meaningful set of features is complete.  
Post-v1.0: minor releases monthly, patch releases as needed.
