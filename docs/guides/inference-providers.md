# Inference providers

Aitra Meter reads token counts from the inference server through a pluggable
provider, selected by the `inferenceProvider.type` Helm value or the
`--inference-provider` flag. This guide covers each built-in provider, the
flags the inference server itself needs, auto-discovery, and known
limitations.

Per-engine Helm values files are in
[`examples/inference-servers/`](../../examples/inference-servers/).

| Provider | `type` | Default endpoint | Token counter | Accuracy |
|---|---|---|---|---|
| vLLM | `vllm` | `http://localhost:8000/metrics` | `vllm:generation_tokens_total` | Exact |
| SGLang | `sglang` | `http://localhost:30000/metrics` | `sglang_generation_tokens_total` | Exact |
| Triton | `triton` | `http://localhost:8002/metrics` | `nv_inference_request_success` × multiplier | **Approximate** |
| Generic | `generic-prometheus` | none (required) | configurable | depends on server |
| Auto | `auto` | discovered from pod IP | per detected engine | per detected engine |

---

## vllm — vLLM (default)

Scrapes vLLM's Prometheus `/metrics` endpoint on the serving port.

**Metrics read:**

- `vllm:generation_tokens_total` — cumulative output tokens (the v1 metric
  name; still exposed by vLLM v0.9.x)
- `vllm:num_requests_running` — in-flight requests, for idle detection
- `model_name` label — served model identification

**Optional latency correlation:** when the agent runs with
`--log-level=debug`, the provider additionally reads
`vllm:time_to_first_token_seconds` and `vllm:time_per_output_token_seconds`
histogram totals (`_count`/`_sum`) and logs them alongside each energy
window. These are read-only correlation data — Aitra does not re-expose them
as its own metrics. On vLLM versions that do not expose the histograms the
read is silently skipped.

**Server-side requirements:** none. vLLM exposes `/metrics` by default.

```yaml
measurementAgent:
  inferenceProvider:
    type: vllm
    config:
      endpoint: "http://localhost:8000/metrics"
```

---

## sglang — SGLang

Scrapes SGLang's Prometheus metrics on the serving port (default 30000).

**Server-side requirement:** SGLang only exposes `/metrics` when launched
with `--enable-metrics`:

```bash
python -m sglang.launch_server --model-path <model> --enable-metrics
```

Without the flag, the endpoint does not exist and the provider logs scrape
errors that name the missing flag.

**Metrics read:**

- `sglang_generation_tokens_total` — cumulative output tokens
- `sglang_num_running_reqs` — in-flight requests
- `model_name` label — served model identification

**Metric-prefix note:** SGLang v0.5.4 renamed its metric prefix from
`sglang:` to `sglang_`. The provider reads the current name first and falls
back to the legacy `sglang:` name, so both old and new SGLang versions work
without configuration.

```yaml
measurementAgent:
  inferenceProvider:
    type: sglang
    config:
      endpoint: "http://localhost:30000/metrics"
```

---

## triton — NVIDIA Triton Inference Server

Scrapes Triton's Prometheus metrics endpoint (default port 8002).

**Known limitation — approximate token counts.** Triton is an orchestration
layer over backends (TensorRT-LLM for LLM workloads) and does not expose a
direct output-token counter. The provider approximates:

```
tokens ≈ nv_inference_request_success × avg_output_tokens_per_request
```

`avg_output_tokens_per_request` defaults to **1**, which means the raw value
counts *requests*, not tokens. Set it from your own workload's measured
average (for example from client-side accounting or TensorRT-LLM backend
logs) to get a meaningful J/token figure. Either way, energy-per-token
accuracy is lower than with vLLM or SGLang — treat Triton-derived J/token as
an estimate, not a measurement.

Counters are summed across all models and versions served by the Triton
instance. `ModelName` reports the first `model` label found; when Triton
serves multiple models, per-model attribution is not available (the agent
tracks one token source per node).

**Metrics read:**

- `nv_inference_request_success` — successful requests, summed over models
- `nv_inference_pending_request_count` — pending requests, for idle detection
- `model` label — served model identification

```yaml
measurementAgent:
  inferenceProvider:
    type: triton
    config:
      endpoint: "http://localhost:8002/metrics"
      avg_output_tokens_per_request: "250"   # measure this for your workload
```

---

## generic-prometheus — any other server

Works with any inference server that exposes a token counter and a running
request gauge over Prometheus. Metric names are configurable:

```yaml
measurementAgent:
  inferenceProvider:
    type: generic-prometheus
    config:
      endpoint: "http://localhost:8080/metrics"     # required
      output_tokens_metric: "inference_output_tokens_total"
      requests_running_metric: "inference_requests_running"
      model_name_label: "model_name"
```

### TGI — deprecation notice

HuggingFace Text Generation Inference entered **maintenance mode on
December 11, 2025**. Existing TGI deployments keep working through the
generic-prometheus provider with the config below; new deployments should
use vLLM or SGLang instead.

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

### Ollama

Ollama does not expose a Prometheus `/metrics` endpoint, so there is no
dedicated Ollama provider. To measure an Ollama deployment, run a sidecar
exporter that translates Ollama's API into Prometheus metrics and point
`generic-prometheus` at the exporter with your exporter's metric names.
A dedicated adapter is deferred to a community contribution — see
[`examples/inference-servers/ollama.yaml`](../../examples/inference-servers/ollama.yaml).

---

## auto — engine auto-discovery

With `inferenceProvider.type: auto` (flag `--inference-provider=auto`) the
agent detects the inference engine from the pods running on its own node at
startup, instead of requiring per-engine configuration.

**Detection priority per pod:**

1. Explicit `aitra-ai.github.io/inference-provider` annotation — value is one
   of `vllm`, `sglang`, `triton`, `tgi`. An unrecognised value falls back to
   `generic-prometheus` (you must then supply metric names via config).
2. `app` label equal to a known engine name (`vllm`, `sglang`, `triton`,
   `tgi`).
3. Pods with neither signal are not treated as inference servers.

The metrics endpoint is built from the pod IP and the engine's default
metrics port (vLLM 8000, SGLang 30000, Triton 8002, TGI 3000). In auto mode
the discovered endpoint is authoritative — a statically configured
`config.endpoint` / `--inference-endpoint` / `INFERENCE_ENDPOINT` is ignored
(to pin a specific endpoint, set the provider explicitly instead of `auto`).
All other `inferenceProvider.config` keys still apply, so you can for
example set `avg_output_tokens_per_request` for a workload where a Triton
pod will be detected.

**Startup logging:** every discovered pod is logged with its provider, token
metric name, endpoint, and which signal matched:

```
INFO discovered inference pod {"namespace": "inference-prod", "pod": "vllm-0",
  "provider": "vllm", "token_metric": "vllm:generation_tokens_total",
  "endpoint": "http://10.1.0.5:8000/metrics", "detected_by": "annotation", "matched": "vllm"}
```

**Behaviour details:**

- Discovery runs once at startup and retries every 5 s for up to 60 s while
  no inference pod is present, then exits with an error (Kubernetes restarts
  the agent, so discovery effectively keeps retrying).
- Discovery is point-in-time: if the inference pod moves or its IP changes,
  the agent must restart to re-discover. There is no watch-based re-detection
  yet.
- Multiple inference pods on one node: the first (sorted by namespace/name)
  is measured, the rest are logged with a warning. The measurement model
  attributes whole-node energy to one token source.
- RBAC: the agent needs `list` on pods. The Helm chart runs the measurement
  agent under the chart's ServiceAccount, which already has this permission.

Annotate your inference pods (pod template, not the Deployment object):

```yaml
template:
  metadata:
    annotations:
      aitra-ai.github.io/inference-provider: sglang
```

---

## Verifying

After install, check the agent log for the provider line:

```bash
kubectl logs -n aitra-system daemonset/aitra-meter-measurement-agent | grep -E "inference|discovered"
```

Then confirm `aitra_j_per_token` appears in Prometheus within about 60
seconds (two 30-second measurement windows) while the server is generating
tokens. If it does not, see the
[troubleshooting guide](troubleshooting.md).
