# OpenCost integration

OpenCost and Aitra Meter are complementary tools that answer different questions about the same cluster:

- **OpenCost** — what did the cluster cost? Reports GPU-hours billed per namespace.
- **Aitra Meter** — what did the cluster produce? Reports tokens generated per joule per namespace.

Running both surfaces the inversion that neither tool sees alone: a namespace can appear cheaper by GPU-hour while being more expensive per token, depending on inference serving configuration.

## Prerequisites

- Aitra Meter installed and producing measurements
- OpenCost installed in the cluster:

```bash
helm repo add opencost https://opencost.github.io/opencost-helm-chart
helm repo update
helm install opencost opencost/opencost --namespace opencost --create-namespace
```

## Combined Grafana panel

Import the combined panel from [`examples/grafana/opencost-aitra-combined.json`](../../examples/grafana/opencost-aitra-combined.json) into an existing Grafana instance.

The panel shows two bar gauges side by side per namespace:
- Left: GPU-hour cost from OpenCost
- Right: `aitra_cost_per_million_tokens_usd` from Aitra Meter

A namespace with lower GPU-hour cost but higher $/M tokens means the hardware is cheaper but the model serving configuration is less efficient — a difference invisible without both tools.

## Reading the combined view

| Namespace | GPU-hour cost | $/M tokens | Interpretation |
|---|---|---|---|
| `inference-prod` | Higher | Lower | Expensive hardware, well-configured serving |
| `inference-staging` | Lower | Higher | Cheaper hardware, poor batching efficiency |

`inference-staging` looks cheaper by GPU-hour and more expensive per token. That inversion is the signal that the serving configuration needs attention — not the hardware choice.

## Verify

```bash
# Aitra Meter cost metric
kubectl port-forward -n monitoring svc/prometheus-operated 9090:9090
# Query: aitra_cost_per_million_tokens_usd

# OpenCost UI
kubectl port-forward -n opencost svc/opencost 9003:9003
# http://localhost:9003
```
