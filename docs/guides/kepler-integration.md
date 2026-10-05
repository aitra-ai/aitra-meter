# Kepler integration

Kepler and Aitra Meter are complementary tools that answer different questions about the same watts:

- **Kepler** — where did the watts go? Attributes node energy (CPU, DRAM, GPU) to pods and containers via eBPF.
- **Aitra Meter** — what did the watts produce? Reports tokens generated per joule per model and namespace.

Running both gives energy attribution and energy productivity side by side. The `kepler` energy provider additionally lets Aitra Meter read its energy numbers *from* Kepler instead of NVML, so Aitra Meter works on clusters where Kepler is already the energy source of truth.

## When to use `kepler` vs `nvml`

Use `kepler` when:

- Kepler is already deployed in the cluster and NVML access is restricted (no privileged access to the GPU driver from the agent pod)
- CPU and DRAM energy attribution is needed alongside GPU energy
- The cluster mixes GPU and CPU inference nodes

Stay on `nvml` (the default) when the node is an NVIDIA GPU node and NVML is accessible. `nvml` reads the hardware energy counter directly at window boundaries; `kepler` readings lag by up to one Kepler/Prometheus scrape interval. `kepler` is the fallback, not the preferred path, on NVML-capable nodes.

## Prerequisites

- Aitra Meter installed and producing measurements
- Kepler installed in the cluster:

```bash
helm repo add kepler https://sustainable-computing-io.github.io/kepler-helm-chart
helm repo update
helm install kepler kepler/kepler --namespace kepler --create-namespace
```

- `kepler_container_joules_total` present in Prometheus. Verify before switching:

```bash
kubectl port-forward -n monitoring svc/prometheus-operated 9090:9090
# Query: kepler_container_joules_total
```

If the query returns no series, the provider cannot work — fix the Kepler scrape first.

The provider is written against the documented `kepler_container_joules_total` metric family (container-level, not `kepler_pod_joules_total` — container-level stays precise when vLLM pods carry sidecars). Label names vary between Kepler releases: some emit `container`, others `container_name`. Check your series and set `container_label` accordingly.

## Enable the provider

```yaml
# values.yaml
measurementAgent:
  energyProvider:
    type: kepler
    config:
      endpoint: "http://prometheus-operated.monitoring.svc.cluster.local:9090"
      container_label: "container"
```

```bash
helm upgrade aitra-meter ./helm/aitra-meter -f values.yaml
```

Config keys:

| Key | Default | Description |
|---|---|---|
| `endpoint` | required | Prometheus base URL (read via the federation endpoint), or a direct text-exposition URL such as a node-local Kepler exporter `/metrics` |
| `container_label` | `container` | Label key used to filter container series |
| `container_name` | unset | Restrict to one container (e.g. `vllm`) to keep sidecar energy out of the window |
| `scrape_interval` | `30s` | The Kepler/Prometheus scrape interval — the counters cannot advance faster than this |

## Verify

After the agent restarts, confirm Aitra Meter is producing measurements from the new provider:

```bash
kubectl port-forward -n monitoring svc/prometheus-operated 9090:9090
# Query: aitra_j_per_token
```

`aitra_j_per_token` should appear (or keep updating) with the agent logging `energy_provider: kepler` at startup. Idle windows and windows shorter than one Kepler scrape interval legitimately report zero joules — that is a resolution limit, not a fault.

## Cross-checking against `nvml`

On NVIDIA GPU nodes Kepler reads GPU energy through NVML under the hood, so `kepler` and `nvml` readings are expected to agree within the 3% CV gate (`cvThreshold: 0.03`). Verify this on your own hardware before relying on it — run one node on each provider for a few hundred windows and compare `aitra_j_per_token`. Disagreement beyond the gate usually means the container filter is catching sidecar energy (set `container_name`), the `scrape_interval` does not match the real Kepler scrape cadence, or the Kepler `mode` series split (dynamic/idle) is being summed differently than you expect — the provider sums all matching series.

## Latency and resolution

The provider makes two HTTP reads per measurement window (`BeginWindow` and `EndWindow`) instead of two syscalls. Against an in-cluster Prometheus endpoint the round trip is well under a second, which is negligible against the default 30-second window. If your Prometheus is reached over an external network path, measure the round trip first — a slow scrape at the window boundary smears the window edges and inflates CV.

Counter resolution is bounded by the Kepler/Prometheus scrape interval. With a 30s window and a 30s scrape interval, each window sees roughly one counter update; energy lands in the right neighbourhood over time but individual windows are coarse. A shorter Kepler scrape interval (e.g. 15s) tightens per-window attribution.

## Limitations

- **One synthetic device.** Kepler aggregates at container level, not device level, so `Devices()` reports a single aggregate entry. No per-GPU breakdown is available through this provider.
- **Idle power needs two samples.** Kepler exposes cumulative joule counters, not a power gauge, so `IdlePower` derives watts from the `kepler_node_package_joules_total` delta between successive calls. The first call after startup returns an error while the sample primes.
- **Counter resets clamp to zero.** A Kepler restart resets its counters; the affected window reports 0 J rather than a negative value.
