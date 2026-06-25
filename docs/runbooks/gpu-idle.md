# Runbook: GPUIdleExcessive

**Alert:** `GPUIdleExcessive` · **Severity:** warning

## What it means

A node has spent **>40% of the last hour with no active inference requests**
(`aitra_idle_time_ratio > 0.40`) for 30 minutes. The GPU is powered and drawing
energy but producing few or no tokens.

## Why it matters

Idle GPU time is pure cost and carbon with zero output. It is the single biggest
lever for reducing $/token at the cluster level.

## Diagnose

```promql
aitra_idle_time_ratio{node="..."}
aitra_idle_power_watts{node="..."}          # watts wasted while idle
```

Common causes:
- **Over-provisioned replicas** for current traffic.
- **Uneven scheduling** — traffic concentrated on other nodes.
- **A drained/cordoned workload** that left the GPU allocated but unused.

## Remediate

- Scale the inference Deployment down, or enable KEDA scale-to-zero on
  `aitra_idle_time_ratio` (see `docs/guides/keda-integration.md`).
- Consolidate low-traffic models onto fewer nodes.
- If the node should be idle (batch/off-peak), silence the alert for that node or
  raise the threshold for its label set.

## Related

`aitra_idle_time_ratio`, `aitra_idle_power_watts`, `aitra_gpu_serving_utilization_ratio`.
