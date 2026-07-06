# Configuration reference

Complete reference for all Helm values, CRD fields, and pod annotations.

---

## Helm values

### `cluster`

| Key | Type | Default | Description |
|---|---|---|---|
| `cluster.name` | string | `""` | Name of this cluster. Used as a label on all measurements. Required. |

### `measurementAgent`

| Key | Type | Default | Description |
|---|---|---|---|
| `measurementAgent.image.repository` | string | `ghcr.io/aitra-ai/aitra-meter/measurement-agent` | Container image repository |
| `measurementAgent.image.tag` | string | chart `appVersion` | Container image tag |
| `measurementAgent.image.pullPolicy` | string | `IfNotPresent` | Image pull policy |
| `measurementAgent.nodeSelector` | map | `{"aitra-ai.github.io/gpu": "true"}` | Node selector for DaemonSet scheduling |
| `measurementAgent.resources.requests.cpu` | string | `100m` | CPU request |
| `measurementAgent.resources.requests.memory` | string | `128Mi` | Memory request |
| `measurementAgent.resources.limits.cpu` | string | `500m` | CPU limit |
| `measurementAgent.resources.limits.memory` | string | `256Mi` | Memory limit |
| `measurementAgent.energyProvider.type` | string | `nvml` | Energy provider name. Built-in: `nvml`, `amd`, `dcgm`. Community: `zeus` |
| `measurementAgent.energyProvider.config` | map | `{}` | Provider-specific config key-value pairs |
| `measurementAgent.inferenceProvider.type` | string | `vllm` | Inference provider name. Built-in: `vllm`, `sglang`, `triton`, `generic-prometheus`, `auto` (pod-annotation/label discovery) |
| `measurementAgent.inferenceProvider.config` | map | — | Provider-specific config. Every key is passed to the agent as an `INFERENCE_CONFIG_<KEY>` environment variable (keys must be lowercase snake_case), e.g. `output_tokens_metric`, `requests_running_metric`, `model_name_label` for `generic-prometheus` |
| `measurementAgent.inferenceProvider.config.endpoint` | string | `http://localhost:8000/metrics` | Inference server metrics endpoint. Ignored with `type: auto` — the endpoint discovered from the pod IP is authoritative |
| `measurementAgent.inferenceProvider.config.avg_output_tokens_per_request` | string | `"1"` | `triton` only — tokens-per-request multiplier for the token approximation (Triton has no direct token counter) |
| `measurementAgent.cvThreshold` | float | `0.03` | CV gate threshold. Measurements with rolling CV above this are flagged `unstable` |
| `measurementAgent.cvWindowSize` | int | `100` | Number of windows in the rolling CV calculation |
| `measurementAgent.logLevel` | string | `info` | Log level: `debug`, `info`, `warn`, `error` |

### `aggregationService`

| Key | Type | Default | Description |
|---|---|---|---|
| `aggregationService.image.repository` | string | `ghcr.io/aitra-ai/aitra-meter/aggregation-service` | Container image |
| `aggregationService.replicas` | int | `1` | Replica count. Single-cluster deployment supports 1 only |
| `aggregationService.port` | int | `8080` | Metrics and API port |
| `aggregationService.logLevel` | string | `info` | Log level |
| `aggregationService.resources.*` | — | see values.yaml | Resource requests and limits |

### `dashboard`

| Key | Type | Default | Description |
|---|---|---|---|
| `dashboard.enabled` | bool | `true` | Set `false` to skip dashboard deployment (use Grafana instead) |
| `dashboard.port` | int | `3000` | Dashboard HTTP port |
| `dashboard.service.type` | string | `ClusterIP` | Kubernetes Service type |

### `prometheus`

| Key | Type | Default | Description |
|---|---|---|---|
| `prometheus.serviceMonitor.enabled` | bool | `true` | Create a Prometheus ServiceMonitor |
| `prometheus.serviceMonitor.namespace` | string | `monitoring` | Namespace where Prometheus Operator is installed |
| `prometheus.serviceMonitor.interval` | string | `15s` | Scrape interval |
| `prometheus.serviceMonitor.scrapeTimeout` | string | `10s` | Scrape timeout |

### `storage`

| Key | Type | Default | Description |
|---|---|---|---|
| `storage.backend` | string | `sqlite` | Storage backend. `sqlite` is the default embedded backend. |
| `storage.config.path` | string | `/data/aitra.db` | Path to the SQLite database file. Mount a PersistentVolume at `/data` for durable storage. Pass `:memory:` for an ephemeral in-process store (tests only). |


### `siteConfig`

| Key | Type | Default | Description |
|---|---|---|---|
| `siteConfig.gridZone` | string | `""` | ElectricityMaps zone identifier (e.g. `SG`, `DE`, `US-CAL-CISO`) |
| `siteConfig.electricityCostPerKwh` | float | `0.12` | Electricity cost in USD per kWh |
| `siteConfig.pue` | float | `1.35` | Power Usage Effectiveness — multiplier applied in chargeback views |
| `siteConfig.carbonIntensityFallback` | int | `400` | Fallback grid carbon intensity in gCO₂/kWh when API unavailable |
| `siteConfig.carbonSource` | string | `electricitymaps` | Carbon intensity source: `electricitymaps`, `watttime`, `manual` |

### `externalApis`

| Key | Type | Default | Description |
|---|---|---|---|
| `externalApis.electricityMaps.enabled` | bool | `true` | Enable ElectricityMaps API integration |
| `externalApis.electricityMaps.secretName` | string | `aitra-electricitymaps-token` | Secret containing the API token |
| `externalApis.electricityMaps.secretKey` | string | `token` | Key within the secret |
| `externalApis.wattTime.enabled` | bool | `false` | Enable WattTime API integration |
| `externalApis.openEI.enabled` | bool | `false` | Enable OpenEI electricity cost API |

### `airGapped`

| Key | Type | Default | Description |
|---|---|---|---|
| `airGapped.enabled` | bool | `false` | Disable all external API calls. Use `siteConfig` manual values for all conversion factors |

---

## MeasurementPolicy CRD fields

**API version:** `aitra-ai.github.io/v1alpha1`  
**Kind:** `MeasurementPolicy`

| Field | Type | Default | Description |
|---|---|---|---|
| `spec.scope.namespaces` | []string | `[]` (all) | Namespaces to measure. Empty list = all namespaces |
| `spec.attribution.defaultMethod` | string | `direct` | Default attribution method: `direct` or `proportional` |
| `spec.attribution.namespaceOverrides` | []object | `[]` | Per-namespace attribution method overrides |
| `spec.attribution.namespaceOverrides[].namespace` | string | required | Namespace name |
| `spec.attribution.namespaceOverrides[].method` | string | required | `direct` or `proportional` |
| `spec.calibration.preferredTier` | string | `aitra_benchmark` | Preferred calibration tier: `aitra_benchmark`, `reference`, `self_calibrated` |
| `spec.cv.threshold` | float | `0.03` | CV gate threshold (3%) |
| `spec.cv.windowSize` | int | `100` | Rolling window size for CV calculation |
| `spec.budget[].namespace` | string | required | Namespace to apply budget to |
| `spec.budget[].monthlyLimitUSD` | float | required | Monthly spend limit in USD |
| `spec.budget[].alertThresholdPct` | int | `80` | Alert when burn rate reaches this percentage of the monthly limit |

---

## SiteConfig CRD fields

**API version:** `aitra-ai.github.io/v1alpha1`  
**Kind:** `SiteConfig`

| Field | Type | Default | Description |
|---|---|---|---|
| `spec.gridZone` | string | `""` | ElectricityMaps zone identifier |
| `spec.electricityCostPerKwh` | float | required | Electricity cost in USD per kWh |
| `spec.pue` | float | `1.0` | Power Usage Effectiveness multiplier |
| `spec.carbonIntensityFallback` | float | required | gCO₂/kWh used when carbon API is unavailable |
| `spec.carbonSource` | string | `manual` | `electricitymaps`, `watttime`, or `manual` |

---

## Pod annotations

Set on inference server pods to enable workload-level attribution.

| Annotation | Values | Required | Description |
|---|---|---|---|
| `aitra-ai.github.io/workload` | `chat`, `code`, `reasoning`, `batch` | No | Workload type. `unknown` if absent |
| `aitra-ai.github.io/precision` | `fp16`, `fp8`, `bf16` | No | Model precision. `unknown` if absent |
| `aitra-ai.github.io/team` | any string | No | Team name for attribution |
| `aitra-ai.github.io/cost-centre` | any string | No | Cost centre code for chargeback |
| `aitra-ai.github.io/inference-provider` | `vllm`, `sglang`, `triton`, `tgi` | No | Explicit engine selection for `inferenceProvider.type: auto`. Takes priority over the `app` label. Unknown values fall back to `generic-prometheus`. See the [inference providers guide](../guides/inference-providers.md) |

## Node labels

Set on GPU-bearing nodes by the cluster operator.

| Label | Value | Required | Description |
|---|---|---|---|
| `aitra-ai.github.io/gpu` | `"true"` | Yes | Schedules the measurement agent DaemonSet to this node |
| `gpu` | e.g. `h100`, `l40s`, `h200` | Yes | GPU tier label. Used as the `hardware` dimension in all measurements |
