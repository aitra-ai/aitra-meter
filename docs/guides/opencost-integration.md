# OpenCost integration

OpenCost and Aitra Meter are complementary. OpenCost measures $/GPU-hr —
what the infrastructure costs. Aitra Meter measures $/M tokens — what the
inference produces per million output tokens.

The combined Grafana panel makes the inversion visible: a namespace that
looks cheaper per GPU-hour can be more expensive per token due to poor
batching. Neither tool reveals this alone.

## Prerequisites

OpenCost installed with a shared Prometheus backend:

```bash
helm install opencost opencost/opencost --namespace opencost --create-namespace
```

Aitra Meter installed (shares the same Prometheus instance).

## Import the combined panel

1. Open Grafana → Dashboards → Import
2. Upload `examples/grafana/opencost-aitra-combined.json`
3. Select your Prometheus datasource

The left panel shows OpenCost GPU allocation cost per namespace.
The right panel shows Aitra `aitra_cost_per_million_tokens_usd` per namespace.

## Interpreting the inversion

If `inference-staging` shows lower $/GPU-hr than `inference-prod` but higher
$/M tokens, `staging` has worse model serving configuration despite cheaper
hardware. The right optimisation target is batching and concurrency, not
hardware tier.
