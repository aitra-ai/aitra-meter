# Host energy measurement

`aitra_j_per_token` measures **accelerator** energy only. Everything else the
node does to serve a token — tokenization, detokenization, sampling, request
scheduling, KV-cache management, and the HTTP path — runs on the host and, by
default, is unmeasured. Host energy measurement (issue #82) closes that gap
**without** redefining `aitra_j_per_token`.

With a host-energy provider configured, the agent also measures non-accelerator
energy per window and the aggregation service emits
[`aitra_system_j_per_token`](../reference/metrics.md#aitra_system_j_per_token)
and [`aitra_host_energy_fraction`](../reference/metrics.md#aitra_host_energy_fraction).

## The one rule: absent is not zero

**A node with no host telemetry reports the host metrics as _absent_, never as
`0`.**

A zero would be silently wrong in the worst direction: it would understate
`aitra_system_j_per_token` and make an *unmeasured* node look **more efficient**
than a measured one. Two clusters would be incomparable, and the more honest one
would look worse. So:

- Providers return `ErrHostEnergyUnavailable` (with a reason) on hardware with no
  telemetry — never a zero reading.
- The aggregation service **omits** the host metrics for that node.
- The agent logs the unavailability **once at startup**, with the reason, at INFO.
- The storage record's `host_energy_joules` is SQL `NULL`, not `0`.

## `host`, not `cpu`

The metric family is named `host`, not `cpu`, on purpose. RAPL exposes *domains*
(`package`, `dram`, `core`, `uncore`, `psys`) — and DRAM is not the CPU at all,
yet it is a material share of inference host energy. More importantly, `host`
names a **boundary** (the accelerator versus everything else on the node) rather
than a component, so it survives non-x86 hardware and board-level paths
(BMC/Redfish) that also capture NICs, storage, fans, and PSU losses. RAPL is one
*provider* of host energy, exactly as NVML is one provider of accelerator energy.

## Provider matrix

| Platform | Provider | Source | Status |
|---|---|---|---|
| x86 (Intel, AMD) | `rapl` | `/sys/class/powercap/intel-rapl:*/energy_uj` | Supported |
| Grace Superchip (72-core) | `grace-hwmon` | hwmon `power1_average` / `power1_oem_info` | Supported |
| GB10 / DGX Spark | — | none | **Unavailable, by design** |
| Server-class, any arch | `redfish` | BMC | Future |
| Default | `none` | Noop (metrics omitted) | Supported |

Configure via Helm:

```yaml
hostEnergyProvider:
  type: rapl   # rapl | grace-hwmon | none
  config: {}
```

or directly on the agent: `--host-energy-provider=rapl`.

## `rapl` (x86)

Reads and differences `/sys/class/powercap/intel-rapl:*/energy_uj`. Pure Go, no
cgo, no sidecar. Notes:

- **Domains.** It sums each `package-N` domain and its `dram` subdomain,
  identified by reading each directory's `name` file. `core`/`uncore` are
  excluded (subsets of `package` — including them double-counts); `psys` is
  excluded (a platform superset that overlaps `package`).
- **Counter wraparound.** `energy_uj` wraps at `max_energy_range_uj`; at high
  power the counter can wrap within a window. The provider detects `end < start`
  and adds the range. This is the most likely source of a silent correctness bug
  in a RAPL reader — it is unit-tested explicitly.
- **Permissions.** On many distributions `energy_uj` is `0400 root` following
  CVE-2020-8694. The DaemonSet must read it as root or have the file
  group-readable. If the file exists but is unreadable, that is reported as
  unavailable (with a distinct reason), never as a crash and never as zero.
- **Container visibility.** `/sys/class/powercap` must be visible inside the pod;
  it may need an explicit `hostPath` mount. Verify in a real pod.
- **AMD** exposes the same interface under `intel-rapl` naming on recent kernels;
  the provider enumerates rather than hardcoding paths.

## `grace-hwmon` (Grace Superchip, 72-core)

Reads the CPU power rails documented in the NVIDIA Grace Performance Tuning
Guide. The correct rail is found by reading `powerN_oem_info` (e.g.
`"CPU Power Socket 0"`), never by index. Because `powerN_average` is a **power**
reading in microwatts (not an energy counter), this provider integrates power
over the window rather than differencing a counter. Requires
`CONFIG_SENSORS_ACPI_POWER`; if the hwmon rails are absent it reports
unavailable.

## GB10 / DGX Spark — unavailable, deliberately

NVIDIA has stated on the developer forum (2026-02-20) that Spark's power
management differs from the 72-core Grace CPU, that there is **no method to
monitor CPU power**, and that there are no plans to expose CPU rail information.
`nvidia-smi` reports GPU power only. Independently verified as absent on Spark:
the ACPI power meter, hwmon power attributes (`power1_input`, `power1_average`,
`power1_oem_info`), SCMI power domains, and a BMC (Spark has none, so
Redfish/IPMI is not an option either).

A community out-of-tree kernel driver (`antheas/spark_hwmon`) reaches the
MediaTek SSPM firmware's shared memory and does expose cumulative energy counters
in millijoules through standard hwmon. **Aitra Meter does not take a dependency on
it.** It is unsigned and out-of-tree, needs DKMS and MOK keys under Secure Boot,
is self-described by its author as "vibe coded", and its firmware ABI is
explicitly still in flux. Building a shipped provider on an unstable ABI, or
asking users to install such a driver, is not a reasonable ask.

On GB10, host energy is `unavailable`, and Aitra Meter says so. That is the
honest state — and it is a stronger position than silently reporting a wrong
number.

> **Sizing the error, separately.** For a one-off *benchmark* (not a shipped
> path), `antheas/spark_hwmon` can be installed on a GB10 to quantify how large
> the host share actually is. A measured example on a GB10 (one small–mid model,
> concurrency 8, eager mode): the GPU drew ≈30 % of total box power, i.e. GPU-only
> J/token undercounted whole-system energy by ≈3.3×, and — measured across the
> qwen2.5 0.5B→32B ladder — that ratio was roughly **constant** rather than
> shrinking with model size. Treat these as hardware- and load-specific: on a
> datacenter GPU drawing 400–700 W the host share is far smaller.
