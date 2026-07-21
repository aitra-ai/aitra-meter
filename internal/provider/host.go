package provider

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

// ErrHostEnergyUnavailable is returned by a HostEnergyProvider on hardware that
// exposes no host power telemetry. It is an error, never a zero reading.
//
// A zero would be silently wrong in the worst possible direction: it would
// understate aitra_system_j_per_token and make an *unmeasured* node look more
// efficient than a measured one. Preventing that is the single most important
// invariant of this subsystem — every provider returns this error (wrapped with
// a human-readable reason) rather than a zero when telemetry is absent.
var ErrHostEnergyUnavailable = errors.New("host energy telemetry not available on this hardware")

// HostEnergyProvider measures energy consumed by everything on the node that is
// not the accelerator: the CPU package, DRAM, and — depending on platform and
// provider — the wider board (NICs, storage, fans, PSU losses).
//
// The contract is deliberately identical in shape to EnergyProvider — a
// monotonic energy counter, snapshotted at window boundaries and differenced —
// so both sides of the node are measured the same way. RAPL is one provider of
// host energy exactly as NVML is one provider of accelerator energy.
//
// "host" names a boundary (the accelerator versus everything else on the node),
// not a component. That boundary survives non-x86 hardware and board-level
// measurement paths (BMC/Redfish); "cpu" would not.
type HostEnergyProvider interface {
	// BeginWindow snapshots the host energy counters.
	BeginWindow(ctx context.Context, windowID string) error

	// EndWindow returns joules consumed by the host since BeginWindow.
	// Returns ErrHostEnergyUnavailable on hardware with no host telemetry.
	EndWindow(ctx context.Context, windowID string) (float64, error)

	// IdlePower returns current host power draw in watts.
	// Returns ErrHostEnergyUnavailable on hardware with no host telemetry.
	IdlePower(ctx context.Context) (float64, error)

	// Domains enumerates measurable host energy domains ("package-0", "dram-0").
	Domains(ctx context.Context) ([]Device, error)

	// Available reports whether this node exposes host energy telemetry. Called
	// once at startup so the agent can log the capability and omit host metrics
	// cleanly rather than emitting zeros.
	Available(ctx context.Context) bool

	// Name returns the provider identifier used in metric labels and logs.
	Name() string
}

// HostEnergyProviderFactory creates a HostEnergyProvider from a config map.
type HostEnergyProviderFactory func(config map[string]string) (HostEnergyProvider, error)

var (
	hostEnergyMu        sync.RWMutex
	hostEnergyProviders = map[string]HostEnergyProviderFactory{}
)

// RegisterHostEnergy registers a HostEnergyProvider factory under a given name.
// Call this from an init() function in your provider package.
func RegisterHostEnergy(name string, factory HostEnergyProviderFactory) {
	hostEnergyMu.Lock()
	defer hostEnergyMu.Unlock()
	hostEnergyProviders[name] = factory
}

// NewHostEnergy creates a HostEnergyProvider by name.
func NewHostEnergy(name string, config map[string]string) (HostEnergyProvider, error) {
	hostEnergyMu.RLock()
	factory, ok := hostEnergyProviders[name]
	hostEnergyMu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("unknown host energy provider %q — registered: %v", name, hostEnergyProviderNames())
	}
	return factory(config)
}

func hostEnergyProviderNames() []string {
	hostEnergyMu.RLock()
	defer hostEnergyMu.RUnlock()
	names := make([]string, 0, len(hostEnergyProviders))
	for n := range hostEnergyProviders {
		names = append(names, n)
	}
	return names
}

// NoopHostEnergy is the HostEnergyProvider used when host telemetry is absent or
// host measurement is switched off. Every read returns ErrHostEnergyUnavailable
// wrapped with a human-readable reason. Nothing it returns is ever zero-as-data:
// a zero reading is the one output this subsystem must never produce.
type NoopHostEnergy struct {
	// Reason explains why host energy is unavailable (e.g. "no host provider
	// configured", "GB10/Spark exposes no host power telemetry"). It is included
	// in every returned error and in the agent's one-time startup log line.
	Reason string
}

// NewNoopHostEnergy returns a NoopHostEnergy carrying the given reason.
func NewNoopHostEnergy(reason string) *NoopHostEnergy {
	if reason == "" {
		reason = "no host energy provider configured"
	}
	return &NoopHostEnergy{Reason: reason}
}

func (n *NoopHostEnergy) err() error {
	return fmt.Errorf("%w: %s", ErrHostEnergyUnavailable, n.Reason)
}

// BeginWindow always fails: there is nothing to measure.
func (n *NoopHostEnergy) BeginWindow(_ context.Context, _ string) error { return n.err() }

// EndWindow always returns ErrHostEnergyUnavailable and never a zero reading.
func (n *NoopHostEnergy) EndWindow(_ context.Context, _ string) (float64, error) {
	return 0, n.err()
}

// IdlePower always returns ErrHostEnergyUnavailable and never a zero reading.
func (n *NoopHostEnergy) IdlePower(_ context.Context) (float64, error) { return 0, n.err() }

// Domains returns no domains.
func (n *NoopHostEnergy) Domains(_ context.Context) ([]Device, error) { return nil, n.err() }

// Available always reports false.
func (n *NoopHostEnergy) Available(_ context.Context) bool { return false }

// Name returns "none".
func (n *NoopHostEnergy) Name() string { return "none" }

func init() {
	// "none" is the default provider: host metrics are omitted, never zeroed.
	RegisterHostEnergy("none", func(config map[string]string) (HostEnergyProvider, error) {
		return NewNoopHostEnergy(config["reason"]), nil
	})
}
