# llm-d integration

[llm-d](https://llm-d.ai) serves large models with disaggregated prefill and
decode: prefill pods and decode pods run on separate GPU pools, labelled with
`llm-d.ai/role=prefill` or `llm-d.ai/role=decode`. Prefill is compute-bound,
decode is memory-bandwidth-bound — they have different energy signatures.

Aitra Meter reads the `llm-d.ai/role` label during attribution and propagates
it into the `role` label on `aitra_j_per_token`, so each phase gets its own
J/token series. No changes to llm-d are required — no sidecars, no CRD changes.

An example dashboard is in
[`examples/grafana/llmd-prefill-decode.json`](../../examples/grafana/llmd-prefill-decode.json).

## Prerequisites

- llm-d deployed with disaggregated serving (pods carry the `llm-d.ai/role` label)
- Aitra Meter installed on the GPU nodes:

```bash
helm repo add aitra https://aitra-ai.github.io/helm-charts
helm repo update
helm install aitra-meter aitra/aitra-meter \
  --namespace aitra-system \
  --create-namespace \
  --set cluster.name=my-cluster
```

The measurement agent scrapes the same vLLM `/metrics` endpoint
(`vllm:generation_tokens_total`) that llm-d's own monitoring uses, and reads
GPU energy from NVML (or DCGM/AMD SMI — see
[energy providers](energy-providers.md)).

## How attribution works

For each measurement window the aggregation service looks up the Running pod
on the reporting node and reads its labels:

| Label | Used for |
|---|---|
| `llm-d.ai/role` | `role` metric label: `prefill` or `decode` |
| `llm-d.ai/model` | Matching the pod to the model name reported by the inference server (multi-model nodes) |

The Aitra annotations (`aitra-ai.github.io/workload`, `precision`, `team`,
`cost-centre`) work on llm-d pods exactly as on any other pod — add them to the
pod template if you want those dimensions too.

### What appears in Prometheus

```
aitra_j_per_token{namespace="inference-prod", model="Qwen/Qwen3-32B",
                  role="prefill", hardware="h100", ...}
aitra_j_per_token{namespace="inference-prod", model="Qwen/Qwen3-32B",
                  role="decode",  hardware="h100", ...}
```

Pods without the `llm-d.ai/role` label produce an empty `role` value, which
Prometheus treats as an absent label — existing series and queries are
unaffected, and the dimension adds no cardinality outside llm-d deployments.
The label value is passed through verbatim; Aitra Meter does not restrict it
to `prefill`/`decode`, so if llm-d adds new roles they appear as new series
without a code change.

### Model matching on multi-model nodes

Kubernetes label values cannot contain `/`, so the `llm-d.ai/model` label
usually holds a DNS-form name (`qwen3-32b`) while the inference server reports
the full model ID (`Qwen/Qwen3-32B`). Aitra Meter matches the label against
the reported name, its sanitized form, and the sanitized basename after the
last `/` — `qwen3-32b` matches `Qwen/Qwen3-32B`.

The label is only ever a positive hint: when several inference pods share a
node, a pod whose `llm-d.ai/model` matches wins, but a pod is never excluded
because its label does not match. If your ModelService names do not resemble
the served model names, add the explicit
`aitra-ai.github.io/model-name: <name>` label to the pod template — it takes
precedence and matches exactly.

## Interpreting prefill vs decode J/token

Be careful with direct magnitude comparisons. J/token divides a node's GPU
energy by the output tokens reported by the vLLM instances on that node. In
disaggregated serving the decode pods report almost all generation tokens;
prefill pods report few (roughly the first token per request, depending on
llm-d version). So:

- `aitra_j_per_token{role="decode"}` — joules per generated token on the
  decode pool. This is the number comparable to conventional serving.
- `aitra_j_per_token{role="prefill"}` — joules per token *reported by the
  prefill instances*, which is closer to "joules of prefill per request" than
  to a per-generated-token cost. Expect it to be much larger than the decode
  value; that is an artifact of where tokens are counted, not a measurement
  error.

Both series are still useful as trends: decode J/token falling as KV-cache hit
rate improves, or prefill J/token rising with longer prompts, are real signals.
Track each role against itself over time rather than reading the prefill/decode
ratio as a hardware efficiency ratio.

If prefill nodes report zero output tokens in a window, that window counts as
idle and is not aggregated — the node's energy then shows up in
`aitra_idle_power_watts` instead. Check `aitra_gpu_serving_utilization_ratio`
for the prefill nodes if the prefill series is missing.

## Grafana panel: latency and energy side by side

Import [`examples/grafana/llmd-prefill-decode.json`](../../examples/grafana/llmd-prefill-decode.json).
It shows, per role:

- J/token from Aitra Meter (`aitra_j_per_token` split by `role`)
- Time per output token from vLLM's own histogram
  (`vllm:time_per_output_token_seconds`), which llm-d's monitoring stack
  already scrapes

The pairing connects inference efficiency (latency) to infrastructure
efficiency (energy) in one view.

Useful queries:

```promql
# Per-phase J/token by model
avg by (model, role) (aitra_j_per_token{role=~"prefill|decode"})

# Decode-pool efficiency trend (lower is better)
avg by (model) (aitra_j_per_token{role="decode"})

# vLLM decode latency for the same split (llm-d metrics, not Aitra Meter)
histogram_quantile(0.9,
  sum by (le) (rate(vllm:time_per_output_token_seconds_bucket[5m])))
```

## Verify

```bash
# 1. Pods carry the role label
kubectl get pods -A -L llm-d.ai/role,llm-d.ai/model | grep -E 'prefill|decode'

# 2. The role label reaches Prometheus
kubectl port-forward -n monitoring svc/prometheus-operated 9090:9090
# Query: aitra_j_per_token{role=~"prefill|decode"}
```

If the series appear without a `role` label, the pod lookup matched a
different pod on the node — see the multi-model section above, or check that
the llm-d pods are Running on the nodes where the measurement agent reports.
