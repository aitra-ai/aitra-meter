# Compatibility

## Energy providers

| Provider | `type` value | Hardware | Notes |
|---|---|---|---|
| NVML | `nvml` | NVIDIA GPUs — H100, H200, L40S, A100, B200 | Default. Pure Go. No sidecar. |
| AMD | `amd` | AMD GPUs — MI300X, MI250X, MI210, ROCm 6.x+ | Via libamd_smi.so. No sidecar. |
| DCGM | `dcgm` | NVIDIA GPUs | Scrapes a node-local dcgm-exporter Prometheus endpoint. Pure Go, no sidecar. Resolution bounded by the exporter scrape interval. |
| Zeus | `zeus` (community) | NVIDIA + AMD + CPU/DRAM + Apple Silicon + Jetson | Requires zeusd sidecar. Use when NVML/AMD access is restricted or CPU+DRAM energy is needed alongside GPU. |

## Inference providers

| Provider | `type` value | Compatible servers | Notes |
|---|---|---|---|
| vLLM (default) | `vllm` | vLLM | Reads `vllm:generation_tokens_total` and `vllm:num_requests_running`. Optional TTFT/TPOT histogram read at debug log level. |
| SGLang | `sglang` | SGLang (server must run with `--enable-metrics`) | Reads `sglang_generation_tokens_total` (v0.5.4+ naming) with fallback to the legacy `sglang:` prefix |
| Triton | `triton` | NVIDIA Triton Inference Server | Token counts are **approximate**: `nv_inference_request_success` × configurable `avg_output_tokens_per_request`. Lower accuracy than vLLM/SGLang. |
| Generic Prometheus | `generic-prometheus` | TGI, Ollama (via sidecar exporter), any custom server | Configure metric names via `config` map. Works with any server exposing Prometheus metrics. |
| Auto | `auto` | vLLM, SGLang, Triton, TGI | Detects the engine from the `aitra-ai.github.io/inference-provider` pod annotation or `app` label. See the [inference providers guide](../guides/inference-providers.md). |
| Ollama | — (community) | Ollama | No native `/metrics` endpoint; needs a sidecar exporter + `generic-prometheus`. A dedicated adapter is deferred to a community contribution. |

Per-provider details, required server flags, and limitations:
[inference providers guide](../guides/inference-providers.md). Ready-made
Helm values per engine: [`examples/inference-servers/`](../../examples/inference-servers/).

## Using `generic-prometheus` for TGI

> **Deprecation notice:** TGI entered maintenance mode on December 11, 2025.
> Existing deployments keep working with the config below; new deployments
> should use vLLM or SGLang.

TGI exposes Prometheus metrics. Configure `generic-prometheus` with TGI metric names:

```yaml
measurementAgent:
  inferenceProvider:
    type: generic-prometheus
    config:
      endpoint: "http://localhost:3000/metrics"
      output_tokens_metric: "tgi_request_generated_tokens_total"
      requests_running_metric: "tgi_queue_size"
      model_name_label: "model_id"
```

## Using `generic-prometheus` for Ollama

Ollama has no native Prometheus endpoint — run a sidecar exporter and point
the config at it. Metric names below are placeholders; use your exporter's
actual names.

```yaml
measurementAgent:
  inferenceProvider:
    type: generic-prometheus
    config:
      endpoint: "http://localhost:9877/metrics"
      output_tokens_metric: "ollama_completion_tokens_total"
      requests_running_metric: "ollama_requests_active"
      model_name_label: "model"
```

## Kubernetes version compatibility

| Kubernetes version | Supported |
|---|---|
| 1.29+ | Yes |
| 1.27–1.28 | Best effort |
| < 1.27 | Not supported |

## GPU hardware

| GPU | Supported | Energy provider |
|---|---|---|
| NVIDIA H100 SXM5 | Yes | nvml, zeus, dcgm |
| NVIDIA H200 SXM | Yes | nvml, zeus, dcgm |
| NVIDIA L40S | Yes | nvml, zeus, dcgm |
| NVIDIA B200 | Yes | nvml, zeus, dcgm |
| NVIDIA A100 | Yes | nvml, zeus, dcgm |
| AMD MI300X | Yes | amd, zeus |
| AMD MI250X | Yes | amd, zeus |
| Apple Silicon (M-series) | Community (zeus only) | zeus |
| Intel GPU | Planned | — |
