# Glossary

Key terms used throughout Aitra Meter documentation.

---

**Aggregation service**  
The central Kubernetes Deployment that receives measurements from all measurement agents, computes J/token, resolves attribution, applies calibration, and writes results to Prometheus and SQLite.

**Attribution method**  
How energy consumption is assigned to a namespace. `direct` means the namespace has its own vLLM instance and energy is measured directly. `proportional` means the namespace shares a vLLM instance and energy is allocated by token count fraction. Declared per namespace in `MeasurementPolicy`. Always surfaced in exports.

**Aitra Benchmark**  
The first-party calibration dataset produced by Aitra. Covers J/token across GPU tiers, model families, and workload types. The preferred calibration source when available. In development.

**Calibration baseline**  
A reference J/token value for a given model × hardware combination, used to contextualise live measurements. Three tiers: `aitra_benchmark`, `reference` (ML.ENERGY v3.0), `self_calibrated`.

**Calibration tier**  
One of `aitra_benchmark`, `reference`, `self_calibrated`, or `uncalibrated`. Stored on every measurement record. Surfaced as a badge in the dashboard.

**Cluster**  
One Kubernetes control plane. The deployment unit for Aitra Meter. One Helm install per cluster. See also: fleet, supercluster.

**Continuous batching**  
vLLM's request execution model where multiple requests from different tenants are processed in the same GPU forward pass. This makes per-request energy attribution approximate without request-level middleware. See proportional attribution.

**CV gate**  
Coefficient of variation check over a rolling window of 100 measurement windows. If CV exceeds 3%, measurements are flagged `unstable=true`. They are stored and included in reports, not dropped.

**DaemonSet**  
The Kubernetes workload kind used by the Aitra Meter measurement agent. One pod per GPU-bearing node. Requires `hostPID: true` and `privileged: true` for NVML access.

**Decode**  
The autoregressive token generation phase of inference. Memory-bandwidth-bound. Has a different energy profile than prefill for the same token count.

**EnergyProvider**  
The Go interface that energy measurement backends implement. Default: Zeus. Others: NVML direct, DCGM. See `internal/provider/provider.go`.

**Fleet**  
The totality of all GPU resources across all sites, clusters, and providers an organisation operates. Aitra Meter is scoped to a single cluster. Fleet views are on the roadmap.

**gCO₂/token**  
Grams of CO₂ equivalent per output token. Derived: `J/token × gCO₂/kWh ÷ 3,600,000`. The conversion factor comes from ElectricityMaps, WattTime, or a manual SiteConfig value.

**Grid intensity**  
Carbon intensity of the electricity grid in gCO₂/kWh for a given zone and time. Sourced from ElectricityMaps or WattTime. Used to derive gCO₂/token. Varies by time of day.

**Hardware**  
The GPU tier serving a workload. Read from the Kubernetes node label `gpu` (e.g. `h100`, `l40s`). One of the three dimensions in the measurement frame.

**Idle energy**  
GPU power draw when `vllm:num_requests_running = 0`. Measured by NVML at 10 Hz. Tracked separately from active inference energy. Not included in J/token (no tokens to divide by).

**InferenceMetricsProvider**  
The Go interface that inference server adapters implement. Default: vLLM. Others: generic-prometheus (works with TGI, SGLang, Ollama, Triton). See `internal/provider/provider.go`.

**J/response**  
Joules per response (complete output). Secondary metric. Less useful than J/token for comparison across workloads with different output lengths.

**J/token**  
Joules per output token. The primary measurement primitive. Computed as: total GPU energy for a window ÷ total output tokens for the same window. The only directly measured metric. All other metrics are derived from it.

**MeasurementPolicy**  
A Kubernetes CRD (`aitra-ai.github.io/v1alpha1`) that configures measurement scope, attribution method, calibration tier preference, CV gate thresholds, and budget alerts.

**Measurement agent**  
The Kubernetes DaemonSet that runs on every GPU-bearing node. Reads GPU energy via the configured EnergyProvider and token counts via the configured InferenceMetricsProvider. Emits measurements to the aggregation service.

**Measurement window**  
A bounded period during which energy is measured. Started by `EnergyProvider.BeginWindow()` and ended by `EnergyProvider.EndWindow()`, aligned to the inference request handler lifecycle.

**ML.ENERGY v3.0**  
NeurIPS 2025 Datasets and Benchmarks Track spotlight. 46 models, 7 tasks, 1,858 configurations on H100 and B200. Used as the interim calibration reference until Aitra Benchmark is published. [github.com/ml-energy/leaderboard](https://github.com/ml-energy/leaderboard)

**Model**  
The AI model being served. Read from the inference server's metric label. One of the three dimensions in the measurement frame.

**Namespace**  
A Kubernetes namespace. The attribution unit for chargeback. Maps to an org or team.

**NVML**  
NVIDIA Management Library. Provides per-GPU power readings, utilisation, VRAM, and temperature. Accessed via the Zeus library (default) or directly via go-nvml.

**PUE**  
Power Usage Effectiveness. Data centre overhead multiplier applied to measured energy in chargeback reports. Configured in `SiteConfig`, not measured. Typical range: 1.1–1.6.

**Prefill**  
The prompt processing phase of inference. Compute-bound. Has a different energy profile than decode for the same token count.

**Proportional attribution**  
The approximate attribution method for namespaces sharing a vLLM instance. Namespace energy = cluster energy × (namespace tokens ÷ cluster tokens). Labeled `proportional` in all reports.

**Provider registry**  
The in-process registry where EnergyProvider and InferenceMetricsProvider implementations register themselves at startup via `init()`. See `internal/provider/registry.go`.

**Self-calibrated**  
Calibration tier used when neither Aitra Benchmark nor ML.ENERGY v3.0 covers the model × hardware combination. Based on Aitra Meter's own production measurements. Baseline is available after 1,000 measurement windows.

**SiteConfig**  
A Kubernetes CRD (`aitra-ai.github.io/v1alpha1`) that configures per-cluster electricity cost, grid zone, PUE, and carbon intensity fallback.

**Supercluster**  
Multiple Kubernetes clusters on shared high-bandwidth fabric (NVLink, InfiniBand) where tensor parallelism spans cluster boundaries. Future scope.

**$/M tokens**  
US dollars per million output tokens. Derived: `J/token × $/kWh ÷ 3,600 × 1,000,000`. Energy cost only — does not include compute instance cost.

**Tensor parallelism (TP)**  
Multi-GPU model serving where the model is split across GPUs. TP=2 means 2 GPUs per model instance; TP=8 means 8. Aitra Meter sums all NVML readings across participating GPUs.

**Workload**  
The type of inference request: `chat`, `code`, `reasoning`, `batch`, or `unknown`. Set via the pod annotation `aitra-ai.github.io/workload`. One of the three dimensions in the measurement frame. `unknown` if annotation is absent.

**Zeus**  
Open-source GPU energy measurement library from ML.ENERGY / CMU. Apache 2.0. PyTorch ecosystem project. Mozilla Technology Fund 2024 awardee. Default EnergyProvider in Aitra Meter. [github.com/ml-energy/zeus](https://github.com/ml-energy/zeus)
