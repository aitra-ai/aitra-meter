package provider

import "context"

// Device represents a measurable accelerator device on a node.
type Device struct {
	ID   string
	Name string
	Type string // gpu | cpu | other
}

// EnergyProvider is the interface that energy measurement backends must implement.
// The default implementation uses Zeus. Others (DCGM, direct NVML, RAPL) can be
// swapped in by implementing this interface and registering with Register().
type EnergyProvider interface {
	// BeginWindow marks the start of an energy measurement window.
	BeginWindow(ctx context.Context, windowID string) error

	// EndWindow ends the window and returns joules consumed since BeginWindow.
	EndWindow(ctx context.Context, windowID string) (float64, error)

	// IdlePower returns current power draw in watts with no inference running.
	IdlePower(ctx context.Context) (float64, error)

	// Devices returns measurable devices on this node.
	Devices(ctx context.Context) ([]Device, error)

	// Name returns the provider identifier used in metric labels and logs.
	Name() string
}

// InferenceMetricsProvider is the interface that inference server adapters must implement.
// The default implementation reads vLLM's Prometheus /metrics endpoint.
// Any inference server exposing token counts and request state can implement this.
type InferenceMetricsProvider interface {
	// OutputTokens returns cumulative output tokens generated. The aggregation
	// service computes the delta between calls.
	OutputTokens(ctx context.Context) (uint64, error)

	// RequestsRunning returns in-flight inference requests. Used for idle detection.
	RequestsRunning(ctx context.Context) (int, error)

	// ModelName returns the name of the model currently being served.
	ModelName(ctx context.Context) (string, error)

	// Name returns the provider identifier used in metric labels and logs.
	Name() string
}
