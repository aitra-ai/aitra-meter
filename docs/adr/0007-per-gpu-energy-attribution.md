# Per-GPU energy attribution — sum only for TP>1

## Status

Accepted — implemented on the per-model measurement path (issue
[#89](https://github.com/aitra-ai/aitra-meter/issues/89)). Extends the
node-sum behaviour in spec §3.1 and ADR-0003. Supersedes the earlier
proposal draft in mechanism (see "Deltas from the proposal" below).

## Context

The classic measurement agent sums energy across every GPU on a node and
emits one node-total measurement for **one model** per window. The stated
rationale (spec §3.1) is tensor parallelism: a TP model spans N GPUs, so its
energy must be summed. That is correct when a node runs one inference
instance; it is wrong when a node runs **multiple different models on
different GPUs** — every co-resident model gets charged the full node
energy, and their J/token values collapse together. It also contaminates
TP<node-size runs, where idle GPUs' power folds into the active model.

## Decision

Attribute energy **per GPU**, **group GPUs by the pod that holds them**, and
**sum energy only within a group**. "Sum only for TP>1" falls out naturally:

- A TP model = one pod holding N GPUs = one group of N → **summed** (the
  only place summing occurs).
- A single-GPU model = a group of 1 → per-GPU.
- Distinct models = distinct groups → never cross-summed.
- GPUs held by no pod → a **residual** report (`_idle`), recorded as true
  node idle power, never folded into any model.

## Mechanism (as implemented)

One `MultiLoop` per node (`internal/agent/multiloop.go`, enabled by the
agent flag `--per-model`):

1. **GPU→pod ownership** — parse the kubelet device-plugin checkpoint
   (`/var/lib/kubelet/device-plugins/kubelet_internal_checkpoint`) into
   pod UID → GPU UUIDs, intersected with Running pods on the node (the
   checkpoint retains terminated pods). Both the NUMA-map and flat-array
   checkpoint layouts are accepted.
2. **Per-device energy** — the energy provider exposes an optional
   capability `provider.PerDeviceEnergy` (`DeviceEnergyJoules() →
   map[uuid]joules`, cumulative). The **dcgm** provider implements it by
   scraping the node-local dcgm-exporter per-device series. Each window the
   loop differences the per-device counters; counter resets clamp to zero.
3. **Grouping** — `E_pod = Σ ΔE[uuid]` over exactly the pod's GPUs.
   Unallocated GPUs' energy sums into the residual.
4. **Token join per pod** — each discovered pod's own `/metrics` endpoint
   (pod IP + declared port) is scraped with the vLLM inference provider;
   tokens and the served model name come from the pod itself, never from a
   node-wide counter. Unreachable endpoints (model loading, non-vLLM pods)
   report zero tokens while still receiving their energy.
5. **Reporting** — one `WindowReport` per model pod plus one residual
   report (`model.ResidualModelName` = `_idle`) per window, over the
   existing proto — no schema change. The aggregation service records
   residual power as `aitra_idle_power_watts` / the `idle` power series,
   keeps quiet (zero-token) model windows under the model's own series, and
   splits `aitra_gpu_power_watts` into one series per model.

Discovery is zero-config: whatever pod holds a GPU next window gets
measured; models launched by any platform (csghub, KServe, plain
Deployments) appear on the dashboard automatically and vanish when they
scale to zero.

## Deltas from the proposal

The accepted implementation differs from the earlier draft in four ways:

- **DCGM, not NVML, provides per-device energy.** The per-device capability
  is an optional interface; the NVML provider can adopt it later without
  design change. `--per-model` refuses to start if the configured energy
  provider lacks the capability.
- **Kubelet checkpoint, not the Pod Resources API.** The checkpoint file is
  a read-only hostPath mount, needs no gRPC client, and carries the same
  pod→device mapping. The Pod Resources socket remains a candidate
  alternative if a kubelet version breaks the checkpoint layout.
- **Agent flag, not Helm value.** Enablement is `--per-model` on the agent
  (wired through the DaemonSet args), not `measurementAgent.perGpuAttribution`.
  Default off; without the flag the classic single-endpoint loop runs
  byte-for-byte unchanged.
- **`gpu_id` label reuse.** In per-model mode the `aitra_gpu_power_watts`
  `gpu_id` label carries the model name (one power series per model, plus
  `idle`), rather than a physical GPU index. Dashboards treat it as the
  attribution key. A dedicated label may replace this in a future minor.

## Consequences

- Unblocks multi-model-per-node (N distinct J/token series, no cross-model
  leakage) and removes idle-GPU contamination from partial-node TP runs.
- Validated end-to-end on an 8×A100-80G node: co-resident models produce
  distinct, size-ordered J/token series; a TP=2 group sums exactly its two
  GPUs (unit-tested); residual degrades to the classic whole-node idle
  report when no model pods exist.
- **Shared GPU (MPS/MIG):** per-device energy cannot split one GPU across
  processes — out of scope here; ADR-0003 proportional attribution applies
  within a GPU (see also the MIG work in #71).
- **Degradation:** checkpoint unreadable → all GPUs treated as unallocated
  (residual-only, visible as idle); per-device scrape failure → the tick is
  skipped and the next successful tick spans the gap, so no energy is lost.

### Non-goals

- Per-request or per-phase (prefill/decode) energy — still deferred.
- Per-process energy on a shared GPU.
- Cross-node / multi-cluster — unchanged.

## Acceptance

- K distinct single-GPU models on one node → K distinct
  `aitra_j_per_token` series, each within tolerance of a single-GPU-isolated
  ground-truth run; no cross-model leakage.
- TP=k model → one series; `E_group` equals the sum of the k participating
  GPUs (and the whole-node figure when k == node size).
- Idle GPUs report only idle power and are excluded from active models.
- Flag off ⇒ byte-for-byte the current node-sum behaviour.
