---
title: Aitra Meter — Technical Specification
version: 1.0
status: draft
date: 2026-05-22
authors: [Aitra contributors]
soda-foundation: true
---

# Aitra Meter — Technical Specification

**Version 1.0 · May 2026 · SODA Foundation**
*Status: Draft for review*

---

## 1. Purpose and scope

This document specifies the technical design of Aitra Meter Phase 1. It covers the measurement methodology, system architecture, component specifications, data model, attribution model, Kubernetes deployment, CNCF integrations, dashboard views, and acceptance criteria.

Aitra Meter Phase 1 is scoped to a single Kubernetes cluster. Multi-cluster federation, supercluster topologies, and cross-site views are out of scope and are deferred to Phase 2.

---

## 2. Goals

**G1** — Continuously measure J/token (joules per output token) for every active workload × model × hardware combination in a Kubernetes cluster.

**G2** — Attribute energy consumption to Kubernetes namespaces using cluster metadata, without requiring changes to inference application code.

**G3** — Derive carbon (gCO₂/token) and cost ($/M tokens) from measured J/token and configurable or API-sourced conversion factors.

**G4** — Deploy entirely via a single Helm chart with no new infrastructure required beyond an existing Kubernetes cluster.

**G5** — Integrate natively with the CNCF observability stack (Prometheus, OpenTelemetry, Grafana) already present in the cluster.

**G6** — Surface measurements through six Phase 1 dashboard views, each answering a specific operational question.

---

## 3. Non-goals

**NG1** — Aitra Meter does not make routing decisions. Routing is delegated to Aitra Gateway, LiteLLM, Envoy, or Kong.

**NG2** — Aitra Meter does not compare GPU hardware tiers. Hardware comparison requires cross-cluster data and is a Phase 2 capability.

**NG3** — Aitra Meter does not enforce budget gates in real-time. Budget reporting and alerting are in scope; real-time request blocking requires Aitra Gateway.

**NG4** — Aitra Meter does not track individual user identity. User-level attribution requires Aitra Gateway to inject a user-ID header per request.

**NG5** — Aitra Meter does not own fleet lifecycle, RMA tracking, or hardware refresh decisions. It exposes a J/token drift signal that DCIM tools consume.

**NG6** — Cross-cluster views, Thanos federation, and supercluster topology are Phase 2.

---

## 4. System architecture

### 4.1 Deployment topology

Aitra Meter deploys as a set of Kubernetes workloads inside a single cluster. One Helm chart installs the full stack.

```
Kubernetes cluster
├── DaemonSet: measurement-agent        (one pod per GPU node)
├── Deployment: aggregation-service     (central computation and attribution)
├── Deployment: dashboard               (Next.js, reads Prometheus + ClickHouse)
├── CRD: MeasurementPolicy              (declarative measurement config)
└── CRD: SiteConfig                     (per-cluster electricity cost, grid zone, PUE)
```

### 4.2 Data flow

```
GPU hardware
  └─ NVML → Zeus ZeusMonitor → measurement-agent
                                        │
vLLM pods                               │
  └─ /metrics Prometheus endpoint ──────┤
                                        ▼
                              aggregation-service
                              (J/token computation,
                               attribution resolution,
                               calibration comparison)
                                        │
                          ┌─────────────┴─────────────┐
                          ▼                           ▼
                     Prometheus                  ClickHouse
                     (hot metrics)           (time-series history)
                          │                           │
                   ┌──────┴──────────────────────────┘
                   ▼
         Dashboard / Grafana / OTel / Lago
```

### 4.3 External dependencies

| Dependency | Role | Fallback |
|---|---|---|
| ElectricityMaps API | Live grid carbon intensity (gCO₂/kWh) | SiteConfig manual value |
| WattTime API | Alternative carbon intensity (marginal emissions) | SiteConfig manual value |
| OpenEI API | Electricity cost ($/kWh) by region | SiteConfig manual value |
| Aitra Benchmark dataset | Primary calibration baseline | ML.ENERGY v3.0 (interim) |
| ML.ENERGY Benchmark v3.0 | Interim calibration reference | Self-calibrated from production |

All external dependencies have a manual ConfigMap fallback. Air-gapped clusters operate entirely from ConfigMap values.

---

## 5. Component specifications

### 5.1 Measurement agent (DaemonSet)

**Kind:** DaemonSet  
**Image:** `ghcr.io/stevenphtan/aitra-meter/measurement-agent:v1`  
**Node selector:** `aitra.io/gpu=true`  
**Security context:** `hostPID: true`, `privileged: true`

**Responsibilities:**
- Initialize Zeus `ZeusMonitor` per GPU device on the node
- Call Zeus `begin_window()` at the start of each vLLM request handling cycle
- Call Zeus `end_window()` at completion, capturing joules for the window
- Read `vllm:num_requests_running` from the local vLLM Prometheus endpoint to detect idle state
- Emit per-window measurements to the aggregation service via gRPC

**Supported hardware (via Zeus):**
- NVIDIA GPUs: H100 SXM5, H200 SXM, L40S, B200 via NVML
- AMD GPUs via ROCm telemetry
- CPU and DRAM energy (secondary)

**Multi-GPU handling:**  
For tensor-parallel models (TP=2, TP=8), the agent reads all NVML device readings on the node and sums them. The aggregated node energy is emitted as a single measurement.

**CV gate:**  
Rolling coefficient of variation over last 100 windows. If CV > 3%, measurements are flagged `unstable=true` but not dropped.

**Metrics emitted (Prometheus):**
```
aitra_gpu_energy_joules_total{node, gpu_id, model_name}
aitra_gpu_power_watts{node, gpu_id}
aitra_gpu_idle_power_watts{node, gpu_id}
aitra_measurement_cv{node, model_name}
aitra_measurement_window_stable{node, model_name}
```

---

### 5.2 Aggregation service (Deployment)

**Kind:** Deployment  
**Replicas:** 1 (Phase 1)  
**Image:** `ghcr.io/stevenphtan/aitra-meter/aggregation-service:v1`

**Responsibilities:**
- Receive per-window energy measurements from all measurement agents
- Read `vllm:generation_tokens_total` from all vLLM Prometheus endpoints
- Compute J/token = total energy ÷ total output tokens for the window
- Resolve attribution dimensions from Kubernetes pod metadata
- Apply calibration tier lookup for each workload × model × hardware combination
- Derive gCO₂/token and $/M tokens from J/token × conversion factors
- Write metrics to Prometheus and time-series records to ClickHouse

**J/token computation — continuous batching:**

| Method | Condition | Formula |
|---|---|---|
| `direct` | One vLLM instance per namespace | J/token = namespace_energy ÷ namespace_tokens |
| `proportional` | Shared vLLM instance | J/token = cluster_J/token × (namespace_tokens ÷ cluster_tokens) |

**Attribution dimensions:**

| Dimension | Source | Required |
|---|---|---|
| `namespace` | Pod namespace | Yes |
| `workload` | Pod annotation `aitra.io/workload` | No — `unknown` if absent |
| `model` | vLLM metric label `model_name` | Yes |
| `hardware` | Node label `gpu` | Yes |
| `precision` | Pod annotation `aitra.io/precision` | No |
| `team` | Pod annotation `aitra.io/team` | No |
| `cost_centre` | Pod annotation `aitra.io/cost-centre` | No |

**Calibration tier lookup (priority order):**
1. `aitra_benchmark` — Aitra Benchmark published dataset
2. `reference` — ML.ENERGY v3.0 dataset
3. `self_calibrated` — Aitra Meter production measurements
4. `uncalibrated` — no reference available

**Metrics exposed:**
```
aitra_j_per_token{namespace, workload, model, hardware, precision, calibration_tier, attribution_method}
aitra_co2_per_token_grams{namespace, workload, model, hardware, carbon_source}
aitra_cost_per_million_tokens_usd{namespace, workload, model, hardware, cost_source}
aitra_namespace_energy_joules_total{namespace, period, attribution_method}
aitra_namespace_tokens_total{namespace, period}
aitra_cluster_j_per_token{calibration_tier}
aitra_cluster_power_watts_total
aitra_idle_power_watts{node}
aitra_idle_time_ratio{node}
```

---

### 5.3 CRD: MeasurementPolicy

```yaml
apiVersion: aitra.io/v1alpha1
kind: MeasurementPolicy
metadata:
  name: default
  namespace: aitra-system
spec:
  scope:
    namespaces: []
  attribution:
    defaultMethod: direct
    namespaceOverrides:
      - namespace: inference-shared
        method: proportional
  calibration:
    preferredTier: aitra_benchmark
  cv:
    threshold: 0.03
    windowSize: 100
  budget:
    - namespace: inference-fin
      monthlyLimitUSD: 5000
      alertThresholdPct: 80
```

---

### 5.4 CRD: SiteConfig

```yaml
apiVersion: aitra.io/v1alpha1
kind: SiteConfig
metadata:
  name: sgp-dc01
  namespace: aitra-system
spec:
  gridZone: SG
  electricityCostPerKwh: 0.12
  pue: 1.35
  carbonIntensityFallback: 412
  carbonSource: electricitymaps
```

---

## 6. Data model

### 6.1 Measurement record

```json
{
  "timestamp": "2026-05-22T14:30:00Z",
  "cluster": "sgp-dc01",
  "node": "gpu-node-01",
  "namespace": "inference-prod",
  "workload": "chat",
  "model": "Qwen3.6-27B",
  "hardware": "h100",
  "precision": "fp16",
  "team": "platform",
  "cost_centre": "cc-1102",
  "energy_joules": 412.4,
  "output_tokens": 1328,
  "j_per_token": 0.3105,
  "calibration_tier": "aitra_benchmark",
  "calibration_reference_j_per_token": 0.29,
  "attribution_method": "direct",
  "measurement_stable": true,
  "cv": 0.018,
  "grid_intensity_gco2_kwh": 412,
  "carbon_source": "electricitymaps",
  "co2_per_token_grams": 0.0000355,
  "electricity_cost_per_kwh": 0.12,
  "cost_source": "siteconfig",
  "cost_per_million_tokens_usd": 0.0414,
  "pue": 1.35,
  "energy_joules_pue_adjusted": 556.7
}
```

### 6.2 ClickHouse schema

```sql
CREATE TABLE aitra_measurements (
  timestamp        DateTime,
  cluster          LowCardinality(String),
  namespace        LowCardinality(String),
  workload         LowCardinality(String),
  model            LowCardinality(String),
  hardware         LowCardinality(String),
  precision        LowCardinality(String),
  j_per_token      Float32,
  co2_per_token    Float32,
  cost_per_m_tokens Float32,
  output_tokens    UInt32,
  energy_joules    Float32,
  attribution_method LowCardinality(String),
  calibration_tier LowCardinality(String),
  measurement_stable UInt8,
  cv               Float32
) ENGINE = MergeTree()
PARTITION BY toYYYYMM(timestamp)
ORDER BY (cluster, namespace, model, timestamp);
```

---

## 7. Measurement methodology

### 7.1 J/token computation

```
J/token = Σ GPU energy (joules) over measurement window
          ÷ Σ output tokens generated over measurement window
```

### 7.2 Derived metrics

```
gCO₂/token  = J/token × (gCO₂/kWh ÷ 3,600,000)
$/M tokens  = J/token × ($/kWh ÷ 3,600) × 1,000,000
cluster J/token = Σ all node energy / Σ all output tokens
```

Derivation formula is stored with every record and surfaced in the dashboard.

### 7.3 Idle energy

Idle energy: GPU power draw when `vllm:num_requests_running = 0`, sampled at 10 Hz via NVML. Tracked separately, not included in J/token computation.

### 7.4 PUE adjustment

```
energy_pue_adjusted = energy_measured × PUE
```

PUE configured in `SiteConfig`. Shown as a configured input alongside raw measured energy.

---

## 8. Calibration

### 8.1 Tiers

| Tier | Source | When used |
|---|---|---|
| `aitra_benchmark` | Aitra Benchmark (Singapore AI Lab) | When published and covering the combination |
| `reference` | ML.ENERGY v3.0 (NeurIPS 2025) | When Aitra Benchmark does not cover it |
| `self_calibrated` | Aitra Meter production measurements | When neither covers it |
| `uncalibrated` | No reference available | New models |

### 8.2 Known ML.ENERGY v3.0 gaps

| Gap | Handling |
|---|---|
| L40S not covered | Hardware scaling factor from H100 baseline |
| FP8-PQ not primary | `self_calibrated` tier |
| APAC models (DeepSeek, MiniMax, Qwen new variants) | `self_calibrated` |
| J/response not J/token | Divide by average output token count per workload type |
| vLLM version drift | Flag calibration as potentially stale on version change |

---

## 9. Attribution model

### 9.1 Annotation contract

**Node labels (operator responsibility):**
```yaml
labels:
  gpu: h100
  aitra.io/gpu: "true"
```

**Pod annotations (operator responsibility):**
```yaml
annotations:
  aitra.io/workload: chat
  aitra.io/precision: fp16
  aitra.io/team: platform
  aitra.io/cost-centre: cc-1102
```

Missing annotations result in `unknown` for the dimension — records are not dropped.

---

## 10. Dashboard views — Phase 1

### View 1 — J/token by workload × model × hardware
Live table of every active combination. Calibration tier badge per row. Warning when workload label is absent. Updated on each Prometheus scrape.

### View 2a — Consumption over time, cluster
Cluster-wide aggregate. Cluster J/token rolling trend. Total power and throughput on paired axes. Time window: 1h / 6h / 24h / 7d.

### View 2b — Consumption over time, by series
One line per active workload × model × hardware combination. Toggle between cluster and series view.

### View 3 — Energy consumed by namespace
Total joules, tokens, cost per namespace per billing period. PUE slider (live). Attribution method declared per row. Exportable.

### View 4 — Idle consumption
GPU power draw when `vllm:num_requests_running = 0`. Stacked area chart (serving vs idle). Per-node table with idle draw, idle energy per hour, serving/idle time split.

### View 5 — Carbon and cost per token
gCO₂/token and $/M tokens per combination. Data source labeled (live API / last-known / manual). Derivation formula shown inline. 24h grid intensity chart.

---

## 11. CNCF integration specifications

### Prometheus
Native ServiceMonitor. Auto-registers with kube-prometheus-stack. No new Prometheus instance required.

### OpenTelemetry
OTel Collector sidecar. Aggregation service emits via OTLP. Energy cost annotations attached to trace spans when W3C TraceContext is present.

### Envoy
Phase 1: Access log ingestion via Fluentbit for attribution enrichment on Istio/Envoy mesh clusters.
Aitra Gateway: ext_proc for attribution header injection, ext_authz for budget enforcement.

### OpenCost
Shared Prometheus backend. Complementary metrics. OpenCost MCP server integration is Phase 2.

### KEDA
ScaledObject using `aitra_j_per_token` or `aitra_idle_time_ratio` as Prometheus triggers. Configuration is operator responsibility.

---

## 12. Helm chart

Key values: see `helm/aitra-meter/values.yaml`.

RBAC requirements — measurement agent:
```yaml
rules:
- apiGroups: [""]
  resources: ["pods", "nodes"]
  verbs: ["get", "list", "watch"]
- apiGroups: ["aitra.io"]
  resources: ["measurementpolicies", "siteconfigs"]
  verbs: ["get", "list", "watch"]
```

No write permissions required on any Kubernetes resource.

---

## 13. Security and compliance

- No PII in measurement records by default. `aitra.io/user-id` is opt-in.
- Air-gapped mode: all external API calls disabled via `airGapped: true`.
- MAS-regulated deployments: air-gapped mode + offline Helm + pre-pulled images.
- External API keys stored in Kubernetes Secrets, never in ConfigMaps or logs.

---

## 14. Acceptance criteria — Phase 1

| ID | Criterion | Verification |
|---|---|---|
| AC-1 | Measurement agent produces NVML readings within 60s of pod start | DaemonSet rollout + metric presence |
| AC-2 | J/token CV < 3% over 100-request window for stable workloads | `aitra_measurement_cv` metric |
| AC-3 | Cluster J/token = Σ energy ÷ Σ tokens (not average of series) | Integration test |
| AC-4 | Attribution method stored in every measurement record | ClickHouse inspection |
| AC-5 | All six dashboard views render within 5s | Load time test |
| AC-6 | PUE slider updates all namespace cost figures within 200ms | UI interaction test |
| AC-7 | Derivation formula shown inline for every gCO₂/token and $/M tokens value | Visual inspection |
| AC-8 | `workload=unknown` rows appear for pods with no annotation | Deploy unlabeled pod |
| AC-9 | Helm install completes on air-gapped cluster | Offline install test |
| AC-10 | ServiceMonitor auto-registers with kube-prometheus-stack | Install + ServiceMonitor inspection |
| AC-11 | 30-day namespace chargeback query completes within 10s | ClickHouse load test |
| AC-12 | Carbon figures update when API switches to fallback | API failure simulation |

---

## 15. Phase 2 scope (out of scope for this specification)

- Multi-cluster federation via Thanos
- Supercluster topology (cross-cluster tensor parallelism)
- Cross-cluster TCO comparison view
- Workload routing recommendation view
- Prefill vs decode energy separation
- Aitra Benchmark publication
- OpenCost MCP server formal integration
- Per-request attribution within continuous batching (requires Aitra Gateway)
- Hardware efficiency drift alerting via DCIM API
- Langfuse quality + efficiency correlation views

---

*Aitra Meter · Technical Specification v1.0 · SODA Foundation · Apache 2.0 · May 2026*  
*github.com/stevenphtan/aitra-meter · Singapore AI Lab · Tsinghua University / LF Research*
