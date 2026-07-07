# Energy Providers

Aitra Meter reads GPU energy through a pluggable provider. The provider is
selected by the `energyProvider.type` Helm value or the `--energy-provider`
flag. The vendor-native providers (`nvml`, `amd`) use the GPU vendor's
recommended API directly; `kepler` reads container-level energy from an
existing Kepler deployment instead.

## nvml — NVIDIA GPUs (default)

Uses `go-nvml` (`github.com/NVIDIA/go-nvml`), NVIDIA's official Go binding
for the NVIDIA Management Library (NVML).

**Hardware:** H100, H200, L40S, A100, and any NVML-capable NVIDIA GPU.

**Runtime requirement:** `libnvidia-ml.so.1` — shipped as part of the standard
NVIDIA driver. Present on every NVIDIA GPU node by definition. No additional
installation.

**What it reads:** `nvmlDeviceGetTotalEnergyConsumption()` — the hardware
energy accumulator counter in millijoules, available on Volta architecture and
newer (V100, T4, A100, H100, H200, L40S, B200). For each measurement window,
the provider records the counter value at window start and end; the delta is
the energy consumed during that window.

**MIG:** on GPUs partitioned with MIG (A100, A30, H100, H200, B200), the
provider detects MIG mode at startup and additionally attributes each window's
energy to individual MIG slices, exposed as the `aitra_mig_*` metrics on the
agent's `:9090/metrics` endpoint. Per-slice energy is attributed
proportionally (there is no per-slice energy sensor), and the feature has not
yet been validated on MIG hardware — see
[MIG support](../reference/mig-support.md) for the model and limitations.

**Helm:**

```yaml
measurementAgent:
  energyProvider:
    type: nvml
```

**CLI:**

```bash
--energy-provider=nvml
```

---

## amd — AMD GPUs

Uses `libamd_smi.so`, AMD's recommended C library for GPU telemetry on ROCm
platforms (AMD System Management Interface).

**Hardware:** MI300X, MI250X, MI210, RX 7900 series, and any AMD GPU supported
by ROCm 6.x+.

**Runtime requirement:** `libamd_smi.so` — shipped with the ROCm stack.
Present on any node running the AMD GPU operator or a manual ROCm
installation. No additional installation.

**What it reads:** `amdsmi_dev_get_energy_count()` — the energy accumulator
counter in microjoules, converted to millijoules. Falls back to
`amdsmi_get_power_measure()` (average socket power) when the energy counter
is unavailable on a given SKU.

**Helm:**

```yaml
measurementAgent:
  energyProvider:
    type: amd
```

**CLI:**

```bash
--energy-provider=amd
```

---

## kepler — Kepler pod/container energy (eBPF)

Reads pod and container energy from
[Kepler](https://github.com/sustainable-computing-io/kepler), a CNCF project
that attributes node energy (CPU, DRAM, GPU) to containers via eBPF. Useful
when Kepler is already deployed and NVML access is restricted, when CPU and
DRAM attribution is needed alongside GPU energy, or when the cluster mixes GPU
and CPU inference nodes. On NVIDIA GPU nodes where both are available, prefer
`nvml` — it reads the hardware energy counter directly, with no
scrape-interval lag. See the [Kepler integration guide](kepler-integration.md)
for a full walkthrough.

**Hardware:** any node where Kepler is deployed — GPU or CPU.

**Runtime requirement:** Kepler installed in the cluster and
`kepler_container_joules_total` present in Prometheus. No library is required
on the GPU node itself. The provider is pure Go — no CGO, no build tag.

**What it reads:** `kepler_container_joules_total` — Kepler's cumulative
per-container energy counter in joules — over HTTP. Two reads per measurement
window (at `BeginWindow` and `EndWindow`); the counter delta is the energy
consumed. When `endpoint` is a bare Prometheus base URL, the provider reads
through the Prometheus
[federation](https://prometheus.io/docs/prometheus/latest/federation/)
endpoint; when `endpoint` includes a path (for example a node-local Kepler
exporter `/metrics` URL), that URL is scraped as-is.
`kepler_node_package_joules_total` supplies the idle-power fallback.
Resolution is bounded by the Kepler/Prometheus scrape interval, so set
`scrape_interval` to match your deployment.

**Config keys** (`energyProvider.config`):

| Key | Default | Description |
|---|---|---|
| `endpoint` | required | Prometheus base URL, or a direct text-exposition URL |
| `container_label` | `container` | Label key used to filter container series (some Kepler releases emit `container_name`) |
| `container_name` | unset | When set, only series whose `container_label` equals this value are summed; when unset, all container series are summed |
| `scrape_interval` | `30s` | How often the underlying counters advance; also the minimum spacing between idle-power samples |

**Helm:**

```yaml
measurementAgent:
  energyProvider:
    type: kepler
    config:
      endpoint: "http://prometheus-operated.monitoring.svc.cluster.local:9090"
      container_label: "container"
```

**CLI:**

```bash
--energy-provider=kepler --energy-endpoint=http://prometheus-operated.monitoring.svc.cluster.local:9090
```

---

## Build tags

The `amd` provider requires CGO and is gated by `//go:build linux && cgo`.
The `nvml` provider also uses CGO via go-nvml. Both require `CGO_ENABLED=1`
in the build, which is the default in the Dockerfile. Neither requires the
vendor library to be present at compile time — only at runtime on the GPU node.

The Dockerfile sets `CGO_ENABLED=1` and links against `libdl` (for dlopen).
No vendor libraries are bundled in the image.

---

## Community extensions

The `zeus` provider name is reserved as a community extension point for
operators who run the Zeus ML.ENERGY daemon (`zeusd`) on their nodes. It is
not implemented in core. See
[`internal/provider/energy/zeus/zeus.go`](../../internal/provider/energy/zeus/zeus.go)
for the interface contract.

To implement a custom provider: implement `provider.EnergyProvider`, register
it with `provider.RegisterEnergy("yourname", factory)` in an `init()` function,
and add a blank import in `cmd/measurement-agent/main.go`.
