# Runbook: AIEfficiencyRegression

**Alert:** `AIEfficiencyRegression` · **Severity:** warning

## What it means

A `workload × model × hardware` series is now burning **>20% more joules per output
token** than its own 1-hour rolling average. Energy efficiency for that series has
regressed.

## Why it matters

Higher J/token means higher energy cost and carbon per token for the same output —
it shows up directly in chargeback and `aitra_cost_per_million_tokens_usd`.

## Diagnose

```promql
# Current vs 1h baseline for the firing series
aitra_j_per_token{namespace="...", model="...", hardware="..."}
avg_over_time(aitra_j_per_token{...}[1h])
```

Common causes:
- **Smaller effective batch size** — traffic dropped, so fixed GPU overhead is
  amortized over fewer tokens. Cross-check `aitra_idle_time_ratio` and request rate.
- **Thermal throttling** — check `aitra_gpu_power_watts` and node temperature.
- **Model / vLLM config change** — precision, max-num-seqs, or tensor-parallel change.
- **Noisy neighbor** — another workload sharing the node/GPU.

## Remediate

- If caused by low traffic: consolidate replicas or scale down (see the GPU-idle runbook).
- If thermal: check cooling / power caps on the node.
- If a config regression: compare against `aitra_calibration_reference_j_per_token`
  and roll back the offending change.
- If transient (traffic spike settling): the alert clears once the series returns
  within 20% of its rolling average.

## Related

`aitra_j_per_token`, `aitra_calibration_reference_j_per_token`,
`aitra_gpu_power_watts`, `aitra_idle_time_ratio`.
