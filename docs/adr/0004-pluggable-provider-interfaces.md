# ADR 0004: Pluggable energy and inference provider interfaces

## Status

Accepted

## Context

The initial implementation described Zeus as the energy measurement engine and vLLM as the inference server. Both are strong defaults, but locking the architecture to specific implementations creates problems:

- Operators running TGI, SGLang, Ollama, TensorRT-LLM, or a custom inference server cannot use Aitra Meter without forking it.
- Operators on AMD clusters, Intel platforms, or future hardware may need a different energy measurement backend.
- The project would accumulate conditional logic (`if zeus ... elif dcgm ...`) rather than clean extension points.
- Community contributors wanting to add support for a new inference server or measurement backend would need to modify core code.

## Decision

Define two provider interfaces in Go. All measurement and inference server interactions go through these interfaces. The core aggregation logic never imports Zeus, vLLM, or any specific implementation directly.

### EnergyProvider interface

```go
// EnergyProvider is the interface that energy measurement backends must implement.
// The default implementation uses Zeus. Others (DCGM, direct NVML, RAPL) can be
// swapped in by implementing this interface and registering with the provider registry.
type EnergyProvider interface {
    // BeginWindow marks the start of an energy measurement window for a given ID.
    BeginWindow(ctx context.Context, windowID string) error

    // EndWindow marks the end of a window and returns joules consumed since BeginWindow.
    EndWindow(ctx context.Context, windowID string) (float64, error)

    // IdlePower returns current GPU power draw in watts.
    // Called continuously when RequestsRunning() == 0 to measure idle energy.
    IdlePower(ctx context.Context) (float64, error)

    // Devices returns the list of measurable GPU/accelerator devices on this node.
    Devices(ctx context.Context) ([]Device, error)

    // Name returns the provider identifier used in metric labels and logs.
    Name() string
}
```

### InferenceMetricsProvider interface

```go
// InferenceMetricsProvider is the interface that inference server adapters must implement.
// The default implementation reads from vLLM's Prometheus /metrics endpoint.
// Any inference server that can expose token counts and request state can implement this.
type InferenceMetricsProvider interface {
    // OutputTokens returns total output tokens generated since the last call.
    // Implementations may return a cumulative counter; the aggregation service
    // computes the delta.
    OutputTokens(ctx context.Context) (uint64, error)

    // RequestsRunning returns the number of in-flight inference requests.
    // Used to determine idle state.
    RequestsRunning(ctx context.Context) (int, error)

    // ModelName returns the name of the model currently being served.
    ModelName(ctx context.Context) (string, error)

    // Name returns the provider identifier used in metric labels and logs.
    Name() string
}
```

### Provider registry

Providers register themselves at startup. The measurement agent selects providers by name from the Helm values configuration:

```yaml
measurementAgent:
  energyProvider:
    type: zeus        # zeus | nvml | dcgm | (any registered provider)
    config: {}
  inferenceProvider:
    type: vllm        # vllm | tgi | sglang | ollama | generic-prometheus
    config:
      endpoint: "http://localhost:8000/metrics"
```

### Built-in providers (Phase 1)

| Interface | Provider name | Backend |
|---|---|---|
| EnergyProvider | `zeus` | Zeus ML.ENERGY library via subprocess |
| EnergyProvider | `nvml` | Direct NVML bindings (go-nvml) — no Python dependency |
| InferenceMetricsProvider | `vllm` | vLLM `/metrics` Prometheus endpoint |
| InferenceMetricsProvider | `generic-prometheus` | Any Prometheus endpoint exposing token count and request count metrics (configurable label names) |

### Community providers (contributed, not bundled)

| Interface | Provider name | Backend |
|---|---|---|
| EnergyProvider | `dcgm` | NVIDIA DCGM |
| InferenceMetricsProvider | `tgi` | HuggingFace Text Generation Inference |
| InferenceMetricsProvider | `sglang` | SGLang |
| InferenceMetricsProvider | `ollama` | Ollama |
| InferenceMetricsProvider | `triton` | NVIDIA Triton Inference Server |

Community providers live in `internal/provider/community/` and are contributed via PR. They must implement the interface and include a test fixture.

## Rationale

- Interfaces keep the aggregation service ignorant of the measurement backend. It only knows `EnergyProvider` and `InferenceMetricsProvider` — never Zeus, never vLLM.
- The `generic-prometheus` inference provider makes Aitra Meter compatible with any inference server that exposes a Prometheus endpoint, without writing a custom adapter. Operators configure the metric names; the provider handles the scraping.
- The `nvml` energy provider provides a path to remove the Python/Zeus dependency entirely for operators who want a pure Go binary.
- Provider registration at startup (not compile-time) means new providers can be contributed without modifying the core registry — they register themselves via `init()`.

## Consequences

- The initial implementation must define these interfaces before writing any measurement or inference code. The interfaces are the contract; implementations come after.
- Zeus and vLLM remain the defaults and the best-tested implementations. They do not go away — they become `type: zeus` and `type: vllm` in the config.
- The `generic-prometheus` inference provider means Aitra Meter works out of the box with TGI, SGLang, and Ollama without writing dedicated adapters, as long as those servers expose token count and request count metrics (which they all do).
- Documentation must clearly distinguish between built-in providers (supported by maintainers) and community providers (best-effort).
