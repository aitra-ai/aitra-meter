# Per-GPU energy attribution — sum only for TP>1

## Status

Proposed — tracking [#89](https://github.com/aitra-ai/aitra-meter/issues/89). Extends the node-sum behaviour in spec §3.1 and ADR-0003.

## Context

The measurement agent sums energy across every GPU on a node and emits one node-total measurement for **one model** per window:

- `internal/provider/energy/nvml/nvml.go:123` — `totalEnergyMillijoules()` reads `DeviceGetTotalEnergyConsumption(dev)` per device, then `total += mj` across all devices.
- `internal/agent/loop.go:13-15,164` — the Loop is documented "single GPU node"; `reportWindow` reads one `ModelName()` + one token counter and pairs it with node-total joules.
- `internal/k8s/podmeta.go:48` — the pod is resolved by the `aitra-ai.github.io/model-name` label; there is no GPU→pod linkage.

The stated rationale for summing (spec §3.1) is **tensor parallelism**: a TP model spans N GPUs, so its energy must be summed across them. This is correct **when a node runs one inference instance** (dedicated node, possibly TP). It is wrong when a node runs **multiple different models on different GPUs** — every co-resident model is then charged the full node energy, so their J/token values collapse together.

This blocks a legitimate topology (N distinct single-GPU models on one node) and also contaminates TP<node-size measurements, where idle GPUs' power is folded into an active model's energy.

Two facts make this tractable:

1. **NVML is not the limiter.** `DeviceGetTotalEnergyConsumption` is already per-device (just summed away); `nvmlDeviceGetComputeRunningProcesses` is available; the agent already runs `hostPID: true, privileged: true`. Per-GPU energy is exact for one-model-per-GPU (whole-GPU energy = that model's energy).
2. **The spec already intends per-GPU metrics** — §3.1 defines `aitra_gpu_energy_joules_total{node, gpu_id, model_name}` and `aitra_gpu_power_watts{node, gpu_id}`. This ADR finishes that intent; it is not a new measurement philosophy.

## Decision

Attribute energy **per GPU**, **group GPUs by the pod/model instance that owns them**, and **sum energy only within a group**. "Sum only for TP>1" then falls out:

- A TP model = one pod requesting N GPUs = one group of N → **summed** (the only place summing occurs).
- A dedicated single-GPU model = a group of 1 → per-GPU.
- Distinct models = distinct groups → never cross-summed.

Mechanism:

1. **Per-GPU energy windows.** The NVML `EnergyProvider` returns a `gpu_id → joules` map for the window (retain per-device start/end from `DeviceGetTotalEnergyConsumption`; also carry `DeviceGetUUID` for stable identity). `EndWindow` (summed) is kept as a convenience for the single-model-per-node path.
2. **GPU→pod ownership.** Resolve which pod owns each GPU via the **kubelet Pod Resources API** (`/var/lib/kubelet/pod-resources/kubelet.sock`, `List()` → pod → allocated device IDs) — the deterministic, MIG-aware mapping used by DCGM-exporter / the GPU operator. Fallback: `nvmlDeviceGetComputeRunningProcesses(dev)` → PID → cgroup → pod (via `hostPID`).
3. **Grouping.** Group GPUs by owning pod; `E_group = Σ gpu_energy[i]` over the group's GPUs. GPUs owned by no inference pod are reported as idle/`unattributed`, never folded into a model.
4. **Token join per instance.** Join each group's energy to that pod's own token counter (aggregation service, per spec §5.2 — "read `vllm:generation_tokens_total` from all vLLM endpoints"), not a node-wide counter.
5. **Metrics.** Emit the already-spec'd per-GPU series (`aitra_gpu_energy_joules_total{gpu_id, model_name}`, `aitra_gpu_power_watts{gpu_id}`, `aitra_gpu_idle_power_watts{gpu_id}`); per-workload `aitra_j_per_token{namespace, workload, model, …}` is now computed per model instance, so co-resident models yield distinct values.

Enumerated changes (design only):

- `provider.EnergyProvider` — add a per-device result (e.g. `EndWindowByDevice(windowID) → map[gpuID]joules`); keep `EndWindow` for back-compat.
- NVML provider — per-device windows + `DeviceGetUUID`.
- New `internal/k8s/podresources` client → `gpuID → PodRef`.
- `WindowReport` proto — add repeated `{gpu_id, gpu_uuid, joules, pod_uid}`; keep `EnergyJoules`.
- `internal/agent/loop.go` — one report per group instead of one per node.
- Aggregation service — group-aware J/token; token scrape keyed by pod.
- Helm — mount the `pod-resources` socket (read-only hostPath) on the DaemonSet.

## Consequences

- **Unblocks multi-model-per-node** (N distinct J/token, no cross-model leakage) and **removes idle-GPU contamination** from partial-node TP runs.
- **Additive and feature-flagged** (`measurementAgent.perGpuAttribution: false` by default = today's node-sum path). Single-model-per-node is unchanged; new per-GPU metrics are additive.
- **Shared GPU (MPS/MIG):** NVML gives whole-GPU energy only and cannot split per process — fall back to ADR-0003 proportional-by-token *within that GPU*, labelled `attribution_method: proportional`. (Not required for one-model-per-GPU.)
- **Degradation:** if the Pod Resources API is unavailable, fall back to NVML process enumeration, then to today's node-sum — strictly non-regressing.
- **Cost:** net-new code (Pod Resources client + grouping + proto/aggregation plumbing) and a multi-GPU test rig. Unvalidated at multi-GPU scale; current dev is a single-GPU GB10. Requires a representative multi-GPU (8×H100) validation before relying on it for a booth demo.

### Non-goals

- Per-request or per-phase (prefill/decode) energy — still deferred (ADR-0003). This ADR changes granularity **to the GPU**, not below it.
- Per-process energy on a shared GPU — NVML cannot; handled only by the proportional fallback.
- Cross-node / multi-cluster — unchanged (out of scope per spec §1).

## Acceptance

- K distinct single-GPU models on one node → K distinct `aitra_j_per_token` series, each within tolerance of a single-GPU-isolated ground-truth run; no cross-model leakage.
- TP=k model → one series; `E_group` equals the sum of the k participating GPUs (and the whole-node figure when k == node size).
- Idle GPUs report only idle power and are excluded from active models.
- Feature-flag off ⇒ byte-for-byte the current node-sum behaviour.
