# ADR 0003: Proportional attribution for shared vLLM instances

## Status

Accepted

## Context

vLLM uses continuous batching — multiple requests from different tenants execute in the same GPU forward pass within a single measurement window. Zeus measures total energy for the window. vLLM's Prometheus endpoint reports aggregate token counts, not per-request or per-namespace breakdowns. This creates an attribution problem: how do you allocate energy to individual namespaces when they share a GPU execution window?

## Decision

Support two attribution methods, declared per namespace in `MeasurementPolicy`:

1. **Direct** — one vLLM instance per namespace. Energy and tokens are separately measured per instance. J/token per namespace is exact.
2. **Proportional** — shared vLLM instance. Energy is allocated proportionally by token count: `namespace_energy = cluster_energy × (namespace_tokens / cluster_tokens)`.

The attribution method used is stored in every measurement record and surfaced in every chargeback export. It is never hidden.

## Rationale

- Direct attribution (separate vLLM instances per namespace) is the recommended deployment pattern for enterprise clusters where accurate chargeback is required. It is already common practice. It eliminates attribution approximation entirely.
- Proportional attribution by token count is the simplest auditable method for shared instances. It is reproducible, deterministic, and easy for operators to verify.
- More accurate methods (separate prefill and decode windows, per-request energy estimation) were considered for Phase 1 but rejected on complexity grounds. Prefill is compute-bound and decode is memory-bandwidth-bound — the same token count has a different energy profile depending on prompt length vs output length. Correcting for this requires per-request prompt and output token counts, which vLLM's aggregate Prometheus metrics do not provide without additional instrumentation.
- Aitra Gateway, when deployed, injects per-request token counts via Envoy access logs. When Gateway is present, the proportional approximation can be replaced with token-exact attribution. This is the upgrade path for operators who need higher accuracy.

## Consequences

- Operators running shared vLLM instances will see `attribution_method: proportional` in their chargeback reports. The approximation is documented and labeled — it is not silent.
- Proportional attribution undercharges namespaces with long prompts and short outputs (high prefill fraction) and overcharges namespaces with short prompts and long outputs (high decode fraction). For most mixed workloads, this error is small.
- Phase 2 will implement prefill/decode energy separation as a measurement improvement, which will allow more accurate proportional attribution without requiring Gateway.
