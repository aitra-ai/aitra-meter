# Compatibility

## Energy providers

| Provider | `type` value | Hardware | Notes |
|---|---|---|---|
| NVML | `nvml` | NVIDIA GPUs — H100, H200, L40S, A100, B200 | Default. Pure Go. No sidecar. |
| AMD | `amd` | AMD GPUs — MI300X, MI250X, MI210, ROCm 6.x+ | Via libamd_smi.so. No sidecar. |
| Zeus | `zeus` (community) | NVIDIA + AMD + CPU/DRAM + Apple Silicon + Jetson | Requires zeusd sidecar. Use when NVML/AMD access is restricted or CPU+DRAM energy is needed alongside GPU. |
| DCGM | `dcgm` (community) | NVIDIA GPUs | NVIDIA-proprietary. Richer GPU telemetry. |

## Inference providers

| Provider | `type` value | Compatible servers | Notes |
|---|---|---|---|
| vLLM (default) | `vllm` | vLLM | Reads `vllm:generation_tokens_total` and `vllm:num_requests_running` |
| Generic Prometheus | `generic-prometheus` | TGI, SGLang, Ollama, Triton, any custom server | Configure metric names via `config` map. Works with any server exposing Prometheus metrics. |
| TGI | `tgi` (community) | HuggingFace Text Generation Inference | Dedicated adapter with TGI-specific metric names pre-configured |
| SGLang | `sglang` (community) | SGLang | Dedicated adapter |
| Ollama | `ollama` (community) | Ollama | Dedicated adapter |
| Triton | `triton` (community) | NVIDIA Triton Inference Server | Dedicated adapter |

## Using `generic-prometheus` for TGI

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

## Using `generic-prometheus` for SGLang

```yaml
measurementAgent:
  inferenceProvider:
    type: generic-prometheus
    config:
      endpoint: "http://localhost:30000/metrics"
      output_tokens_metric: "sglang:num_output_tokens_total"
      requests_running_metric: "sglang:num_running_reqs"
      model_name_label: "model_name"
```

## Using `generic-prometheus` for Ollama

```yaml
measurementAgent:
  inferenceProvider:
    type: generic-prometheus
    config:
      endpoint: "http://localhost:11434/metrics"
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
| NVIDIA H100 SXM5 | Yes | nvml, zeus |
| NVIDIA H200 SXM | Yes | nvml, zeus |
| NVIDIA L40S | Yes | nvml, zeus |
| NVIDIA B200 | Yes | nvml, zeus |
| NVIDIA A100 | Yes | nvml, zeus |
| AMD MI300X | Yes | amd, zeus |
| AMD MI250X | Yes | amd, zeus |
| Apple Silicon (M-series) | Community (zeus only) | zeus |
| Intel GPU | Planned | — |
