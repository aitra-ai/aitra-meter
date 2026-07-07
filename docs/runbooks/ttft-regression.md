# Runbook: TTFTRegression

**Alert:** `TTFTRegression` · **Severity:** warning

## What it means

The **P95 time-to-first-token has exceeded 3 seconds** over the last 5 minutes for
a namespace. This is a latency SLO signal sourced directly from vLLM
(`vllm:time_to_first_token_seconds`) — Aitra Meter ships the rule but does not own
or re-export the metric.

## Why it matters

TTFT is the latency users feel first. A regression usually means the serving tier
is saturated or queueing, which also tends to drag efficiency (J/token) and may
precede dropped requests.

## Diagnose

```promql
histogram_quantile(0.95,
  sum by (le, namespace) (rate(vllm:time_to_first_token_seconds_bucket[5m]))) 
vllm:num_requests_running                 # queue depth / concurrency
vllm:num_requests_waiting
```

Common causes:
- **Saturation** — request rate above capacity; requests queueing.
- **Long prompts** — prefill cost up (large input tokens).
- **KV-cache pressure** — high `gpu_cache_usage_perc`, preemptions.
- **Co-located noisy neighbor** on the GPU/node.

## Remediate

- Scale the inference Deployment out, or enable autoscaling on queue depth.
- Tune vLLM (`max-num-seqs`, `max-num-batched-tokens`) for the prompt profile.
- Route long-prompt workloads to dedicated replicas.
- If 3s is too strict/loose for this namespace, adjust the alert threshold.

## Related

`vllm:time_to_first_token_seconds_bucket`, `vllm:num_requests_running`,
`aitra_j_per_token` (efficiency often regresses alongside latency).
