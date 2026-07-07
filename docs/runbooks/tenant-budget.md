# Runbook: TenantCostBudgetExceeded

**Alert:** `TenantCostBudgetExceeded` · **Severity:** warning

## What it means

A namespace's inference-energy spend over the **last 24 hours** has exceeded its
configured budget:

```promql
increase(aitra_tenant_cost_usd_total[24h])
  > on(namespace) group_left() aitra_tenant_cost_budget_usd_24h
```

The budget (`aitra_tenant_cost_budget_usd_24h`) comes from the chart `costBudgets`
value — see [cost-budgets guide](../guides/cost-budgets.md).

## Why it matters

This is a spend SLO. It tells you a tenant is on track to overrun its allocation,
before the monthly bill lands.

## Diagnose

```promql
increase(aitra_tenant_cost_usd_total{namespace="..."}[24h])   # actual 24h spend
aitra_tenant_cost_budget_usd_24h{namespace="..."}             # configured budget
# What is driving the spend?
topk(5, increase(aitra_model_tokens_total{namespace="..."}[24h]))
aitra_j_per_token{namespace="..."}
```

Common causes:
- **Traffic growth** — legitimately more tokens served.
- **Efficiency regression** — J/token up (see the efficiency-regression runbook).
- **Idle waste** — paying for idle GPUs (see the GPU-idle runbook).
- **Budget set too low** for the workload's real footprint.

## Remediate

- If demand is legitimate: raise the budget in `costBudgets` and `helm upgrade`.
- If efficiency/idle driven: act on the corresponding runbook to cut J/token or
  idle time rather than just raising the budget.
- If chronic: move the workload to more efficient hardware (compare
  `aitra_model_energy_per_1m_tokens` across `hardware`).

## Note on data dependency

This alert needs `aitra_tenant_cost_usd_total`, populated once the SiteConfig cost
wiring lands (issue #40 follow-up). Until then it cannot fire.

## Related

`aitra_tenant_cost_usd_total`, `aitra_tenant_cost_budget_usd_24h`,
`aitra_model_tokens_total`, `aitra_model_energy_per_1m_tokens`.
