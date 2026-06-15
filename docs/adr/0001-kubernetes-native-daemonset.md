# Kubernetes-native DaemonSet deployment

## Status

Accepted

## Context

Aitra Meter needs to measure GPU energy consumption at the node level. The measurement agent must run on every GPU-bearing node and have access to NVML (NVIDIA Management Library) for hardware-level power readings. Several deployment patterns were considered: sidecar injection into vLLM pods, a standalone node agent managed outside Kubernetes, and a DaemonSet with privileged access.

## Decision

Deploy the measurement agent as a Kubernetes DaemonSet with `hostPID: true` and `privileged: true`, scheduled only to nodes labeled `aitra.io/gpu=true`.

## Rationale

- DaemonSet is the standard Kubernetes pattern for per-node workloads. It is understood by platform teams and compatible with all cluster management tooling.
- NVML requires hardware-level access that is only available with `hostPID` and privileged security context. There is no unprivileged alternative for direct GPU power readings.
- Node affinity via `aitra.io/gpu=true` label prevents the agent from scheduling to non-GPU nodes, eliminating wasted pods.
- Sidecar injection into vLLM pods was rejected because: (1) it couples Aitra Meter's lifecycle to the inference server's lifecycle, (2) it cannot aggregate measurements across multiple GPU devices per node, and (3) it requires modifying inference pod specs.

## Consequences

- Operators must label GPU-bearing nodes with `aitra.io/gpu=true` at cluster setup. This is a one-time administrative step, documented in the Helm chart README.
- The DaemonSet requires privileged access, which some cluster policies restrict. For such clusters, operators must add a PodSecurityPolicy or equivalent exception for the `aitra-system` namespace.
- Cross-node tensor parallelism (TP jobs spanning multiple Kubernetes clusters) cannot be measured by a per-node DaemonSet. This is deferred to a future release (supercluster topology).
