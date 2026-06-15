# Zeus as the measurement engine

## Status

Accepted

## Context

Aitra Meter needs a reliable, production-tested library for GPU energy measurement. Options considered: build a custom NVML wrapper, use DCGM (NVIDIA Data Center GPU Manager), or use Zeus (ML.ENERGY / CMU).

## Decision

Use Zeus as the measurement engine for all GPU energy readings.

## Rationale

- Zeus is Apache 2.0 licensed and is a PyTorch ecosystem project (official PyTorch blog, May 2024). It has sustained academic and industry investment.
- Zeus provides exactly the abstraction Aitra Meter needs: `begin_window()` / `end_window()` around a code block, returning joules consumed. This maps cleanly to vLLM request handler instrumentation.
- Zeus expanded hardware support in May 2025 to include AMD GPUs (ROCm), CPU/DRAM, Apple Silicon, and NVIDIA Jetson — making Aitra Meter's measurement layer hardware-agnostic without additional code.
- Zeus was selected as a 2024 Mozilla Technology Fund awardee, indicating sustained maintenance commitment.
- Zeus includes `CarbonEmissionMonitor` and `EnergyCostMonitor` as first-class features, providing carbon and cost attribution without additional libraries.
- DCGM was rejected because it is NVIDIA-proprietary and does not support AMD GPUs or non-NVIDIA hardware.
- A custom NVML wrapper was rejected because Zeus already provides the exact functionality needed and maintaining a custom NVML layer is significant ongoing work.

## Consequences

- Aitra Meter takes a dependency on the Zeus Python package. Zeus is stable and actively maintained; version pinning in the Helm chart mitigates upgrade risk.
- Zeus does not perform continuous batching attribution (it measures total energy for a window, not per-request). Aitra Meter must implement proportional attribution on top of Zeus for shared vLLM instances. See proportional attribution decision.
- As vLLM versions evolve, Zeus-measured J/token benchmarks may go stale. Aitra Meter must detect vLLM version changes in metric labels and flag calibration baselines as potentially stale when a version change is detected.
