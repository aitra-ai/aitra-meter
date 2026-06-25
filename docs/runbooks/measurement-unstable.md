# Runbook: MeasurementUnstable

**Alert:** `MeasurementUnstable` · **Severity:** warning

## What it means

The rolling coefficient of variation of J/token for a `node × model` series has
been **above 3%** (`aitra_measurement_cv > 0.03`) for 5 minutes. Measurements are
flagged unstable — not dropped, but chargeback/efficiency figures for this series
may be unreliable.

## Why it matters

A high CV means the J/token signal is noisy, so derived cost and carbon for this
series carry more uncertainty. Decisions (scaling, chargeback) should wait for the
series to stabilize.

## Diagnose

```promql
aitra_measurement_cv{node="...", model_name="..."}
aitra_measurement_window_stable{node="...", model_name="..."}   # 0 = flagged
```

Common causes:
- **Bursty / low traffic** — few tokens per window amplifies variance.
- **Workload interference** — co-located pods competing for the GPU.
- **Thermal throttling** — variable clocks → variable power.
- **Very short windows** relative to request duration.

## Remediate

- For bursty traffic: increase the measurement window or CV window size
  (`measurementAgent.cvWindowSize`) so more samples smooth the signal.
- Reduce interference: isolate the model onto its own GPU, or check MIG slicing.
- Check node thermals / power caps if throttling is suspected.
- The alert clears automatically once CV falls back below the threshold.

## Related

`aitra_measurement_cv`, `aitra_measurement_window_stable`, `aitra_j_per_token`.
