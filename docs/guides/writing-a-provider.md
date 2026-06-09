# Writing a provider

Aitra Meter uses two pluggable interfaces: `EnergyProvider` for energy measurement backends and `InferenceMetricsProvider` for inference server adapters. This guide shows how to implement either.

---

## When to write a provider

Write an **EnergyProvider** if you want to use a different energy measurement backend:
- NVIDIA DCGM instead of Zeus
- Direct NVML bindings (go-nvml) with no Python dependency
- Intel RAPL for CPU energy
- Custom hardware telemetry

Write an **InferenceMetricsProvider** if your inference server is not yet supported:
- Any server not covered by `generic-prometheus`
- A server with a non-Prometheus metrics format
- A custom inference framework

Before writing a new InferenceMetricsProvider, check whether `generic-prometheus` already works. It supports any server that exposes token count and request count as Prometheus metrics, with configurable metric names. See the [configuration reference](../reference/configuration.md).

---

## EnergyProvider interface

```go
type EnergyProvider interface {
    BeginWindow(ctx context.Context, windowID string) error
    EndWindow(ctx context.Context, windowID string) (float64, error)
    IdlePower(ctx context.Context) (float64, error)
    Devices(ctx context.Context) ([]Device, error)
    Name() string
}
```

**Implementing BeginWindow / EndWindow**

The measurement agent calls `BeginWindow` at the start of a request handling cycle and `EndWindow` at completion. `EndWindow` must return the joules consumed between the two calls, for all GPU devices on the node combined.

If your backend measures energy as power × time, sample power at high frequency (≥10 Hz) and integrate. Do not use instantaneous power at the end of the window — it will produce noisy readings.

**Implementing IdlePower**

`IdlePower` is called continuously when no inference requests are running (when the InferenceMetricsProvider returns `RequestsRunning() == 0`). Return the current power draw in watts. This populates the idle consumption dashboard view.

---

## InferenceMetricsProvider interface

```go
type InferenceMetricsProvider interface {
    OutputTokens(ctx context.Context) (uint64, error)
    RequestsRunning(ctx context.Context) (int, error)
    ModelName(ctx context.Context) (string, error)
    Name() string
}
```

**Implementing OutputTokens**

Return a cumulative counter of output tokens generated since the provider started. The aggregation service computes the delta between successive calls. Do not return per-window counts — return the running total.

**Implementing RequestsRunning**

Return the number of in-flight inference requests. This is used to determine idle state. If your server does not expose this directly, return 0 when the output token rate is zero.

---

## Registering your provider

Use `init()` to register with the provider registry. Your provider is then selectable by name in `values.yaml`.

```go
package myprovider

import (
    "context"
    "github.com/aitra-ai/aitra-meter/internal/provider"
)

func init() {
    provider.RegisterEnergy("my-provider", func(config map[string]string) (provider.EnergyProvider, error) {
        return &MyProvider{endpoint: config["endpoint"]}, nil
    })
}

type MyProvider struct {
    endpoint string
}

func (m *MyProvider) Name() string { return "my-provider" }
// ... implement remaining methods
```

---

## File location

**Built-in providers** (maintained by the Aitra Meter team):
```
internal/provider/energy/<name>/<name>.go
internal/provider/inference/<name>/<name>.go
```

**Community providers** (contributed, best-effort support):
```
internal/provider/community/<name>/<name>.go
```

Community providers follow the same interface. The only difference is the support commitment.

---

## Testing your provider

Every provider must include a test fixture:

```go
// internal/provider/energy/myprovider/myprovider_test.go
func TestMyProviderImplementsInterface(t *testing.T) {
    var _ provider.EnergyProvider = &MyProvider{}
}

func TestBeginEndWindowReturnsPositiveJoules(t *testing.T) {
    // ...
}
```

The interface compliance test is mandatory. Hardware-dependent tests should use build tags:

```go
//go:build integration
```

---

## Submitting a provider

1. Implement the interface in `internal/provider/community/<name>/`.
2. Register via `init()`.
3. Add a `README.md` in the package directory describing: what backend it targets, required config keys, known limitations.
4. Include the interface compliance test.
5. Open a PR. The maintainers will review interface correctness; hardware testing is the contributor's responsibility.

---

## Config map keys

Config keys are passed as `map[string]string` from the `inferenceProvider.config` or `energyProvider.config` block in `values.yaml`. Use lowercase, hyphenated keys. Document every key in your package `README.md`.

```yaml
inferenceProvider:
  type: my-provider
  config:
    endpoint: "http://localhost:8080/metrics"
    timeout: "5s"
```
