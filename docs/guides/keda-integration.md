# KEDA integration

> This guide is in progress. Reference YAML is available in [`examples/keda/`](../../examples/keda/).

Aitra Meter exposes two Prometheus metrics that KEDA can use as scaling triggers:

- `aitra_idle_time_ratio` — fraction of the last hour a GPU node spent idle. Use this to scale inference deployments to zero when demand drops.
- `aitra_j_per_token` — joules per output token. Use this to trigger alerts or scaling actions when efficiency degrades.

Reference ScaledObjects are in [`examples/keda/scale-on-idle.yaml`](../../examples/keda/scale-on-idle.yaml) and [`examples/keda/scale-on-efficiency-regression.yaml`](../../examples/keda/scale-on-efficiency-regression.yaml).

A full walkthrough will be added here before the v0.3.0 release.
