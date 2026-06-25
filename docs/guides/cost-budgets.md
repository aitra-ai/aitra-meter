# Cost budgets

Aitra Meter can page you when a namespace's inference-energy spend exceeds a
budget you set. This guide covers the `costBudgets` Helm value and the
`TenantCostBudgetExceeded` alert.

## How it works

1. You set a per-namespace 24-hour USD budget in the `costBudgets` Helm value.
2. The chart renders one `aitra_tenant_cost_budget_usd_24h` **recording rule** per
   entry (see `helm/aitra-meter/templates/prometheusrule.yaml`).
3. The `TenantCostBudgetExceeded` alert (in `examples/alerting-rules.yaml`) compares
   the namespace's actual 24h spend against that budget:

   ```promql
   increase(aitra_tenant_cost_usd_total[24h])
     > on(namespace) group_left() aitra_tenant_cost_budget_usd_24h
   ```

Budgets live as a recording rule (not in code) so you can change them with a
`helm upgrade` — no redeploy of the aggregation service.

## Configure

```yaml
# values.yaml
costBudgets:
  inference-prod: 500   # USD per 24h
  inference-fin: 250
```

```bash
helm upgrade aitra-meter aitra/aitra-meter -f values.yaml
kubectl get prometheusrule -n monitoring   # <release>-cost-budgets appears
```

Leave `costBudgets: {}` (default) to render no budget rules; the alert then stays
inactive.

## Dependency

`TenantCostBudgetExceeded` reads `aitra_tenant_cost_usd_total`, which is populated
once the SiteConfig cost wiring lands (the cost/idle follow-up to issue #40). Until
then the metric is empty and the alert never fires — the budget recording rule is
still rendered and harmless.

## Relationship to `MeasurementPolicy.budget`

These are two different mechanisms:

| | `costBudgets` (this guide) | `MeasurementPolicy` `spec.budget` |
|---|---|---|
| Path | Prometheus recording rule + alert | In-product budget tracking (CRD) |
| Window | 24 hours | Monthly |
| Use | Alertmanager paging on spend | Policy-level budget configuration |

Use `costBudgets` when you want Prometheus/Alertmanager to page on short-term
overspend; use `MeasurementPolicy.budget` for the platform's own budget policy.

## Verify the rules

```bash
promtool check rules examples/alerting-rules.yaml
kubectl get prometheusrule -n monitoring <release>-cost-budgets -o yaml
```
