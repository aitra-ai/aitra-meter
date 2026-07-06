# Proposal: `gen_ai.infrastructure.energy.*` — energy semantic conventions for GenAI serving

Status: **draft for internal review** — once approved, this becomes a draft PR against
[open-telemetry/semantic-conventions-genai](https://github.com/open-telemetry/semantic-conventions-genai).
Tracked by [#38](https://github.com/aitra-ai/aitra-meter/issues/38).

> **Note on target repository.** Issue #38 says `open-telemetry/semantic-conventions`,
> but the GenAI conventions have since moved to the dedicated
> `open-telemetry/semantic-conventions-genai` repository (the main repo's
> `docs/gen-ai/` pages now redirect there). The draft PR should target the new repo;
> the registry snippet below follows its `file_format: definition/2` schema.

## Motivation

The `gen_ai.*` semantic conventions describe what GenAI systems *do* — tokens used,
operation duration, time to first chunk — but say nothing about what they *consume*.
Energy is the missing dimension:

- GenAI inference runs on accelerators drawing hundreds of watts each; clusters draw
  megawatts. Operators are increasingly asked to report energy and carbon per
  workload, and no OTel convention exists to express it.
- Without a convention, every tool invents its own names, and energy data cannot be
  joined with the `gen_ai.*` signals that already carry model and provider attributes.
- Normalizing by output tokens (J/token) is what makes energy *actionable*: it turns
  a hardware counter into an efficiency signal comparable across models, engines,
  and hardware generations.

## Proposed metrics

Four metrics under a new `gen_ai.infrastructure.*` sub-namespace:

| Name | Instrument | Unit | Description |
|---|---|---|---|
| `gen_ai.infrastructure.energy` | Counter | `J` | Cumulative accelerator energy consumed serving GenAI workloads |
| `gen_ai.infrastructure.energy.per_token` | Gauge | `J` | Energy per generated output token over the most recent measurement window |
| `gen_ai.infrastructure.power` | Gauge | `W` | Instantaneous accelerator power draw |
| `gen_ai.infrastructure.idle_ratio` | Gauge | `1` | Fraction of the measurement window during which the accelerator served no GenAI requests |

Registry snippet: [`otel-semconv-energy-metrics.yaml`](otel-semconv-energy-metrics.yaml).

### Attributes

Reuses existing registry attributes — no new attributes are proposed:

| Attribute | Level | Why |
|---|---|---|
| `gen_ai.request.model` | recommended | Joins energy to the model being served |
| `gen_ai.provider.name` | recommended | Inference engine (`vllm`, `generic-prometheus`, …) |
| `server.address` | recommended | The node the accelerator is attached to |
| `k8s.namespace.name` | opt-in | Chargeback boundary on Kubernetes |
| `k8s.cluster.name` | opt-in | Multi-cluster roll-ups |

### Semantics

- **Measurement window.** `energy.per_token` and `idle_ratio` are window-scoped:
  the emitter samples an interval (Aitra Meter uses 30 s), computes
  ΔJoules ÷ Δoutput_tokens, and reports one value per window. Window length is an
  implementation choice and deliberately not part of the convention.
- **Attribution.** When one accelerator serves several workloads, emitters SHOULD
  document their attribution method. Values are best-effort; the convention does
  not mandate an attribution algorithm.
- **Honesty.** Windows with zero output tokens MUST NOT emit `energy.per_token`
  (division by zero) but SHOULD still emit `energy`, `power`, and `idle_ratio` —
  idle consumption is signal, not noise.

## Naming note (open question for reviewers)

OTel naming guidance discourages embedding units in metric names, so this proposal
uses `…energy` (unit `J`) and `…power` (unit `W`). Aitra Meter's shipped exporter
currently emits `…energy.joules_total`, `…energy.joules_per_token`, and
`…power.watts`; if the unit-free spelling is accepted upstream, the exporter will
be renamed to match before the convention stabilizes.

## Prior art

- **Kepler** (CNCF) — eBPF-based pod energy attribution. Reports joules per pod but
  is not GenAI-aware: no token normalization, no `gen_ai.*` attributes.
- **DCGM / NVML** — hardware counters (mJ, mW) with no workload semantics.
- **ML.ENERGY** — offline benchmark measurements of J/token; a calibration
  reference, not a telemetry convention.

None of these define how energy telemetry should be *named and attributed* in the
GenAI context — that is the gap this proposal fills.

## Working implementation

[Aitra Meter](https://github.com/aitra-ai/aitra-meter) (Apache 2.0, SODA Foundation)
ships an opt-in OTLP exporter emitting exactly these four metrics with the attribute
set above (`internal/export/otlp/`). The pipeline is validated on production
hardware (8×A100 cluster; H100 validation in progress) with NVML, DCGM, AMD, and
Zeus energy providers behind a common interface.

## Submission checklist (when opening the upstream draft PR)

- [ ] Registry entry in `model/gen-ai/metrics.yaml` (adapt the snippet in this directory)
- [ ] Docs table in `docs/gen-ai/gen-ai-metrics.md` (generated via the repo's `make generate`)
- [ ] `changelog.d/` entry per upstream `CONTRIBUTING.md`
- [ ] PR marked **draft**; body links Aitra Meter as the working implementation
- [ ] Link the PR from Aitra Meter's README (required for Demo 2, KubeCon Japan 2026)
