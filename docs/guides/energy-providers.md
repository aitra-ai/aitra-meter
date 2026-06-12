# Energy Providers

Aitra Meter reads GPU energy through a pluggable provider. The provider is
selected by the `energyProvider.type` Helm value or the `--energy-provider`
flag. Two providers ship with Aitra Meter. Both use the GPU vendor's
recommended API directly.

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
