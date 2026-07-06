# MIG support

Aitra Meter can attribute GPU energy — and, when an inference server is
pinned to a slice, output tokens and J/token — to individual MIG
(Multi-Instance GPU) slices on NVIDIA GPUs. This page documents the
attribution model, its limitations, and how to configure it.

**Validation status: not yet validated on MIG hardware.** The attribution
logic is unit-tested against a mocked NVML interface. The integration test on
an A100/H100 in MIG mode (issue #43 acceptance criterion) is still pending
lab hardware. Until that has run, treat per-slice numbers as unverified.

---

## Requirements

| Requirement | Detail |
|---|---|
| GPU | MIG-capable NVIDIA GPU: A100, A30, H100, H200, B200 |
| Driver | R450.80.02 or newer (NVML 11.0 — first version with the MIG device APIs) |
| Energy provider | `nvml` (in-process NVML). The `dcgm` and `zeus` providers do not feed per-slice attribution |
| Metrics scrape | The `aitra_mig_*` metrics are exposed by the **measurement agent** (`:9090/metrics`, `--metrics-addr`), not the aggregation service |

## Detection

At startup the `nvml` energy provider calls `nvmlDeviceGetMigMode` on every
GPU. If at least one GPU has MIG mode enabled, the agent switches to
MIG-aware measurement windows automatically and logs:

```
MIG mode detected — per-slice energy attribution enabled
```

No configuration is required for detection. GPUs that predate MIG or have it
disabled are measured exactly as before; on mixed nodes only the MIG-enabled
GPUs contribute per-slice series.

MIG geometry is re-read at the start of every measurement window, so slices
created or destroyed at runtime are picked up within one window (default 30 s).

## Attribution model

### Energy

There is no per-slice energy sensor. A100/H100 boards have board-level power
rails, so `nvmlDeviceGetTotalEnergyConsumption` is meaningful on the physical
GPU handle, not per MIG instance. Aitra Meter therefore **attributes** energy
to slices; it does not measure it per slice:

1. Each measurement window reads the parent GPU's hardware energy counter at
   window start and end. The delta is the parent's measured energy.
2. The delta is split across the parent's slices in proportion to their
   compute slice count (NVML `GpuInstanceSliceCount`):

   ```
   slice_joules = parent_joules × slice_compute_slices / Σ compute_slices of all slices on that parent
   ```

3. `aitra_mig_power_watts` is `slice_joules / window_seconds`.

Properties and consequences of this model:

- **Energy is conserved.** Per-slice values on a parent always sum to the
  parent's measured delta. On a partially partitioned GPU (say 3 × 1g on an
  A100 with capacity for 7), the whole measured board energy — including the
  idle share of the unpartitioned capacity — is spread over the 3 existing
  slices. Nothing is left unattributed.
- **The split is static, not activity-based.** A busy 1g slice and an idle 1g
  slice on the same GPU are attributed the same energy. Activity-weighted
  attribution (e.g. by DCGM SM activity per instance) is a possible follow-up,
  not implemented.
- If slice attributes cannot be read for any slice on a parent, the parent's
  energy is split equally across its slices rather than dropped.
- If a parent's energy counter cannot be read for a window, that parent's
  slices are absent from that window's attribution (the node-total metrics
  are unaffected).

### Tokens and J/token

vLLM does not expose per-MIG token counts. The supported scope is **one
inference server pinned to one slice** via `CUDA_VISIBLE_DEVICES=MIG-<uuid>`
on the inference pod:

- All output tokens read from the node's inference provider are attributed to
  the pinned slice.
- The pinned slice is named to the agent with `--mig-instance`, either as the
  `mig_instance` label value (`mig-1g.10gb:0`) or the MIG device UUID
  (`MIG-…`, the same value as the pod's `CUDA_VISIBLE_DEVICES`).
- If `--mig-instance` is not set and the node exposes **exactly one** slice,
  tokens are attributed to it automatically. With several slices and no pin,
  token metrics stay absent (power attribution still works) — guessing would
  produce wrong numbers.

True multi-tenant MIG — several inference servers on one GPU, one per slice —
requires pod-to-MIG-device mapping (GPU Operator device-plugin allocations or
DCGM pod mapping) and is out of scope here; see issue #43 feasibility notes.

## Metrics

All four are exposed by the measurement agent. `gpu_uuid` is the UUID of the
**parent** physical GPU; `mig_instance` identifies the slice within it.

### `aitra_mig_j_per_token`

**Type:** Gauge  
**Description:** Joules per output token for the pinned MIG slice over the
last measurement window. `slice_joules / token_delta`.

| Label | Description |
|---|---|
| `node` | Kubernetes node name |
| `gpu_uuid` | Parent GPU UUID |
| `mig_instance` | Slice label, e.g. `mig-1g.10gb:0` |
| `namespace` | Namespace of the pinned inference pod (`--mig-namespace`, default `unknown`) |
| `model` | Model name from the inference provider |

### `aitra_mig_tokens_total`

**Type:** Counter  
**Description:** Cumulative output tokens attributed to a MIG slice.
Same labels as `aitra_mig_j_per_token`.

### `aitra_mig_cost_usd_total`

**Type:** Counter  
**Description:** Cumulative energy cost in USD attributed to a MIG slice:
`slice_joules / 3.6e6 × $/kWh` per window. Absent unless the agent is started
with `--electricity-cost-usd-per-kwh > 0`.

| Label | Description |
|---|---|
| `node` | Kubernetes node name |
| `gpu_uuid` | Parent GPU UUID |
| `mig_instance` | Slice label |
| `namespace` | Namespace label (default `unknown`) |
| `team` | Team label (`--mig-team`, default `unknown`) |

### `aitra_mig_power_watts`

**Type:** Gauge  
**Description:** Power attributed to a MIG slice: the parent GPU's window
energy split by compute-slice fraction, divided by the window duration.
Recorded for **every** slice, pinned or not.

| Label | Description |
|---|---|
| `node` | Kubernetes node name |
| `gpu_uuid` | Parent GPU UUID |
| `mig_instance` | Slice label |

## Label conventions

`mig_instance` is `mig-<profile>:<index>`, e.g. `mig-1g.10gb:0`,
`mig-2g.20gb:1`:

- `<profile>` is derived from NVML attributes as
  `<GpuInstanceSliceCount>g.<ceil(MemorySizeMB / 1024)>gb`. NVML reports the
  usable framebuffer, which sits slightly below the nominal profile size
  (e.g. 9728 MiB for `1g.10gb`), so rounding up recovers the nominal name for
  every A100/H100 profile we are aware of. This derivation has not been
  verified on hardware; if a name comes out wrong for some profile, the label
  is still stable and unique per slice — please file an issue.
- `<index>` is the MIG device index within the parent GPU in NVML enumeration
  order — the order `nvidia-smi -L` lists them. It is **not** the GPU
  instance ID that DCGM exposes as `GPU_I_ID` (those are not sequential:
  1g instances on an A100 get IDs 7–13). A slice is globally identified by
  the `(gpu_uuid, mig_instance)` pair.

## Configuration

Agent flags (all optional — detection itself needs none):

| Flag | Default | Purpose |
|---|---|---|
| `--mig-instance` | `""` | Slice the inference server is pinned to (`mig-1g.10gb:0` or `MIG-<uuid>`). Empty: auto when exactly one slice |
| `--mig-namespace` | `unknown` | `namespace` label on token/cost metrics |
| `--mig-team` | `unknown` | `team` label on the cost counter |
| `--electricity-cost-usd-per-kwh` | `0` | Enables `aitra_mig_cost_usd_total` |
| `--metrics-addr` | `:9090` | Where the agent serves `/metrics` (empty disables) |

Helm:

```yaml
measurementAgent:
  energyProvider:
    type: nvml
  mig:
    pinnedInstance: "mig-1g.10gb:0"
    namespace: "tenant-a"
    team: "ml-platform"
    electricityCostUSDPerKWh: 0.20
```

## Coexistence with DCGM Exporter

Aitra Meter only issues read-only NVML queries (`GetMigMode`, MIG device
enumeration, energy/power counters). It takes no exclusive locks and does not
reconfigure MIG, so DCGM Exporter keeps working unchanged alongside it — the
same guarantee as the non-MIG `nvml` provider.

Note when joining the two in PromQL: DCGM labels MIG series with `UUID`,
`GPU_I_ID`, and `GPU_I_PROFILE`, while Aitra Meter uses `gpu_uuid` and
`mig_instance`, and the `mig_instance` index is the enumeration order rather
than `GPU_I_ID`. Joins need label rewriting and a mapping between the two
index schemes; there is no automatic correlation today.

## Limitations

- **No sub-slice attribution.** Attribution stops at the MIG compute
  instance. Processes sharing one slice are not separated.
- **Proportional, not measured.** Per-slice energy is the parent's measured
  energy split by compute-slice fraction. It does not reflect per-slice
  activity differences.
- **One pinned inference server per node** for token metrics. Multi-tenant
  pod-to-slice mapping is not implemented.
- **Profile names are derived**, not read from NVML's profile-info API
  (see label conventions above).
- Geometry changes mid-window are attributed against the geometry captured at
  window start.
- The cost counter needs an electricity price flag; there is no SiteConfig
  integration on the agent side yet.
- AMD ROCm partitioning and time-sliced GPU sharing are out of scope
  (see issue #43).
