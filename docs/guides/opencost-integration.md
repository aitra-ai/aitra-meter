# OpenCost integration

OpenCost and Aitra Meter are complementary tools that answer different questions about the same cluster:

- **OpenCost** — what did the cluster cost? Reports GPU-hours billed per namespace.
- **Aitra Meter** — what did the cluster produce? Reports tokens generated per joule per namespace.

Running both surfaces the inversion that neither tool sees alone: a namespace can appear cheaper by GPU-hour while being more expensive per token, depending on inference serving configuration. Surfacing that inversion is the primary use case of this integration.

This is a read-only integration: no changes to OpenCost are required.

## Prerequisites

- Aitra Meter installed and producing measurements
- OpenCost installed in the cluster, with its Prometheus metrics scraped. Any release that exports `node_gpu_hourly_cost` and `container_gpu_allocation` works (all current 1.x releases do):

```bash
helm repo add opencost https://opencost.github.io/opencost-helm-chart
helm repo update
helm install opencost opencost/opencost --namespace opencost --create-namespace
```

- **GPU node pricing configured in OpenCost.** On-prem clusters must set GPU prices via OpenCost's custom pricing (`customPricing.gpuHourlyPrice` or a pricing CSV); without it `node_gpu_hourly_cost` is absent or zero and every join below is meaningless.

## Prometheus metric names

| Metric | Source | Meaning |
|---|---|---|
| `node_gpu_hourly_cost` | OpenCost | $/hour price of each GPU node |
| `container_gpu_allocation` | OpenCost | GPUs allocated per container (joined to nodes via `on(node)`) |
| `aitra_cost_per_million_tokens_usd` | Aitra Meter | Electricity cost per million output tokens |
| `aitra_model_tokens_total` | Aitra Meter | Cumulative output tokens per namespace/model/hardware |
| `aitra:opencost_cost_per_token_ratio` | Recording rule (below) | Hourly GPU spend per token generated |

Both projects label these series with `namespace`, so the join is direct — no relabeling needed.

## Recording rule: GPU spend per token

The derived ratio — GPU spend per token generated — requires a Prometheus recording rule. It ships in [`examples/alerting-rules.yaml`](../../examples/alerting-rules.yaml) (group `aitra-meter-opencost`):

```yaml
- record: aitra:opencost_cost_per_token_ratio
  expr: |
    sum by (namespace) (container_gpu_allocation * on(node) group_left() node_gpu_hourly_cost)
    /
    sum by (namespace) (rate(aitra_model_tokens_total[1h]) * 3600)
```

Numerator: hourly GPU spend per namespace (OpenCost). Denominator: tokens generated per hour per namespace (Aitra Meter). The result is dollars of GPU capacity consumed per output token.

This is a best-effort join. Its assumptions:

- OpenCost has GPU node pricing configured (see Prerequisites)
- The namespace runs its inference pods on the GPU nodes OpenCost is pricing
- A 1-hour rate window; namespaces with under an hour of traffic show noisy ratios

## Combined Grafana panel

Import the combined panel from [`examples/grafana/opencost-aitra-combined.json`](../../examples/grafana/opencost-aitra-combined.json) into an existing Grafana instance (validated against Grafana 10.x: Dashboards → New → Import → paste JSON).

The dashboard shows three bar gauges per namespace:

- **Left** — GPU-hour cost from OpenCost
- **Middle** — `aitra_cost_per_million_tokens_usd` from Aitra Meter
- **Right** — `aitra:opencost_cost_per_token_ratio`, the derived GPU spend per token (requires the recording rule above)

A namespace with lower GPU-hour cost but higher $/M tokens means the hardware is cheaper but the model serving configuration is less efficient — a difference invisible without both tools. The right-hand column turns that comparison into a single sortable number.

## Reading the combined view

| Namespace | GPU-hour cost | $/M tokens | GPU spend per token | Interpretation |
|---|---|---|---|---|
| `inference-prod` | Higher | Lower | Lower | Expensive hardware, well-configured serving |
| `inference-staging` | Lower | Higher | Higher | Cheaper hardware, poor batching efficiency |

`inference-staging` looks cheaper by GPU-hour and more expensive per token. That inversion is the signal that the serving configuration needs attention — not the hardware choice.

## Known limitations

- **OpenCost does not distinguish GPU-hours by inference engine.** A namespace mixing vLLM with training or batch jobs attributes all of its GPU spend to the ratio's numerator, inflating the apparent cost per token.
- **Idle time is counted.** GPU-hours accrue while the namespace serves zero tokens; a mostly-idle namespace shows a very high (but honest) spend per token. Cross-check `aitra_idle_time_ratio`.
- **Prices are static between OpenCost pricing refreshes.** Spot or time-of-day pricing changes appear only after OpenCost reloads its pricing data.
- The join is namespace-granular. Per-model or per-pod cost splits within one namespace are out of scope until per-request attribution lands (see ROADMAP).

## Verify

```bash
# Aitra Meter cost metric
kubectl port-forward -n monitoring svc/prometheus-operated 9090:9090
# Query: aitra_cost_per_million_tokens_usd
# Query: aitra:opencost_cost_per_token_ratio   (non-empty once the recording rule is loaded)

# OpenCost UI
kubectl port-forward -n opencost svc/opencost 9003:9003
# http://localhost:9003
```
