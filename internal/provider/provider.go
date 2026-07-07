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

// LatencySample holds cumulative latency-histogram totals read from an
// inference server. Values are running totals (the Prometheus _count and _sum
// series of the underlying histograms); callers compute deltas between windows.
type LatencySample struct {
	TTFTCount float64 // time-to-first-token histogram sample count
	TTFTSum   float64 // time-to-first-token histogram sum, seconds
	TPOTCount float64 // time-per-output-token histogram sample count
	TPOTSum   float64 // time-per-output-token histogram sum, seconds
}

// LatencyProvider is an optional interface for inference providers that also
// expose time-to-first-token / time-per-output-token histograms. The agent
// reads these for correlation with energy windows (debug logging only);
// Aitra does not re-expose them as its own metrics.
type LatencyProvider interface {
	// Latency returns the current latency totals. ok is false when the
	// server does not expose the latency metrics (absence is not an error).
	Latency(ctx context.Context) (sample LatencySample, ok bool, err error)
}

// MIGSlice identifies one MIG (Multi-Instance GPU) compute instance on an
// NVIDIA GPU with MIG mode enabled.
type MIGSlice struct {
	// ParentUUID is the UUID of the physical GPU the slice belongs to
	// (e.g. "GPU-5c8e1a2f-…"). Used as the gpu_uuid metric label.
	ParentUUID string

	// ParentIndex is the NVML index of the physical GPU.
	ParentIndex int

	// Index is the MIG device index within the parent GPU, in NVML
	// enumeration order (the order `nvidia-smi -L` lists MIG devices).
	Index int

	// UUID is the MIG device UUID (e.g. "MIG-8bf7c667-…"). This is the value
	// a pinned workload sets in CUDA_VISIBLE_DEVICES.
	UUID string

	// GPUInstanceID is the NVML GPU instance ID. Matches DCGM's GPU_I_ID label.
	GPUInstanceID int

	// Profile is the MIG profile name, e.g. "1g.10gb".
	Profile string

	// Instance is the Prometheus mig_instance label value for this slice,
	// e.g. "mig-1g.10gb:0". The suffix is Index.
	Instance string

	// ComputeSlices is the number of GPU compute slices the instance owns
	// (NVML GpuInstanceSliceCount). Drives proportional energy attribution.
	ComputeSlices int

	// MemoryMB is the framebuffer memory of the instance in MiB.
	MemoryMB uint64
}

// MIGSliceEnergy is the energy attributed to one MIG slice over one
// measurement window.
type MIGSliceEnergy struct {
	Slice      MIGSlice
	Joules     float64
	PowerWatts float64
}

// MIGEnergyProvider is an optional interface an EnergyProvider can implement
// when it can attribute node energy to individual MIG slices. The agent
// detects it with a type assertion at startup and switches to MIG-aware
// windows automatically when MIGEnabled reports true.
type MIGEnergyProvider interface {
	EnergyProvider

	// MIGEnabled reports whether at least one GPU on the node has MIG mode
	// enabled. Evaluated once at provider init.
	MIGEnabled() bool

	// MIGSlices enumerates the MIG compute instances currently configured
	// on the node.
	MIGSlices(ctx context.Context) ([]MIGSlice, error)

	// EndWindowMIG behaves like EndWindow but additionally returns the
	// per-slice energy breakdown for the window. The error refers to the
	// total measurement only: when per-slice attribution fails, the total is
	// still returned with an empty or partial slice list.
	EndWindowMIG(ctx context.Context, windowID string) (float64, []MIGSliceEnergy, error)
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
