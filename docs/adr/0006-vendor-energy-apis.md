# ADR-0006: Use Vendor APIs Directly for GPU Energy Measurement

**Status:** Accepted  
**Date:** June 2026

## Context

Aitra Meter's DaemonSet agent needs to read GPU energy consumption on NVIDIA
and AMD hardware. The original implementation used Zeus (ml-energy/zeus) as
the default energy provider. Zeus is a Python library that wraps the same
vendor APIs — NVML for NVIDIA and AMD SMI for AMD — and adds training-focused
features such as batch size optimisation, power limit tuning, and the
`begin_window`/`end_window` code-instrumentation API.

The question: should Aitra Meter continue to depend on Zeus as an intermediary,
or call vendor APIs directly?

## Decision

Use vendor APIs directly. No dependency on Zeus in the core measurement path.

**NVIDIA:** `go-nvml` (`github.com/NVIDIA/go-nvml`) — NVIDIA's official Go
binding for the NVIDIA Management Library. Already present in `go.mod`. The
`nvml` provider is production-ready and becomes the default.

**AMD:** Thin CGO binding against `libamd_smi.so` — AMD's recommended C
library for GPU telemetry. AMD does not publish an official Go binding; the C
library is the authoritative interface. The `amd` provider calls
`amdsmi_dev_get_energy_count()` and `amdsmi_get_power_measure()` directly.

**Zeus:** Retained as a named community extension point (`zeus` provider name
reserved) for operators who run `zeusd` for power limit management on training
nodes. Not implemented in core. Not the default.

## Rationale

**Vendor APIs are the recommended standard.** When a vendor provides an API,
use it. Intermediaries add complexity, Python runtime dependency, and a layer
of abstraction that Aitra Meter does not need.

**Zeus solves a different problem.** Zeus is designed for training loop
instrumentation — `begin_window`/`end_window` wraps discrete, named code
blocks inside a training script. Inference serving under continuous batching
has no equivalent code-block boundaries. Aitra Meter's window model (periodic
sampling against the hardware counter delta) is the right approach for the
inference serving use case, and it maps directly to the vendor counter APIs.

**No Python in the DaemonSet.** Zeus requires a Python runtime or a `zeusd`
sidecar process. A single Go binary with CGO is the correct deployment unit
for a Kubernetes DaemonSet. Adopters should not need to install Python or run
a separate daemon to measure GPU energy.

**AMD SMI complexity is already understood.** Zeus's AMD implementation
(585 lines) documents the edge cases: unreliable energy counter on some SKUs,
dual-die chiplet handling, `amdsmi` version differences. The `amd` provider
handles these same cases directly in Go CGO, using Zeus's implementation as
the specification for what to handle — not as a runtime dependency.

## Consequences

- Default `energyProvider.type` in `values.yaml` changes from `zeus` to `nvml`.
- `--energy-provider` default in `cmd/measurement-agent/main.go` changes from
  `zeus` to `nvml`.
- AMD GPU clusters set `energyProvider.type: amd`. `libamd_smi.so` must be
  present on GPU nodes (it is, on any ROCm node).
- The `zeus` provider returns a clear error message directing operators to
  `nvml` or `amd`, with a pointer to the community extension guide.
- Build requires `CGO_ENABLED=1` (already the case; go-nvml uses CGO).
- No change to the `EnergyProvider` interface.
- AC-1 and AC-2 (GPU hardware validation) are unaffected — they test the
  measurement result, not the provider implementation.
