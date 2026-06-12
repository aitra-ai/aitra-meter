// Package zeus is a community extension point for operators who run the Zeus
// ML.ENERGY daemon (zeusd) and want to route Aitra Meter's energy reads
// through it — for example, to share a zeusd instance that is already
// managing GPU power limits for training workloads on the same node.
//
// Zeus is not required for inference energy measurement. For NVIDIA GPUs use
// the nvml provider (default). For AMD GPUs use the amd provider.
//
// To implement this provider, connect to the zeusd Unix socket or HTTP
// endpoint and call ZeusMonitor.begin_window / end_window via the Zeus
// power-streaming SSE API. See https://ml.energy/zeus for protocol details.
//
// Register as "zeus" — the factory below must be replaced with a real
// implementation before this provider is usable.
package zeus

import (
	"context"
	"fmt"

	"github.com/aitra-ai/aitra-meter/internal/provider"
)

func init() {
	provider.RegisterEnergy("zeus", func(config map[string]string) (provider.EnergyProvider, error) {
		return nil, fmt.Errorf(
			"zeus provider is not implemented: use --energy-provider=nvml (NVIDIA) " +
				"or --energy-provider=amd (AMD); see docs/guides/energy-providers.md " +
				"for community extension instructions",
		)
	})
}

// ZeusProvider is a placeholder. Replace with a real implementation that
// connects to zeusd. See package doc above.
type ZeusProvider struct{}

func (z *ZeusProvider) Name() string { return "zeus" }

func (z *ZeusProvider) BeginWindow(_ context.Context, _ string) error {
	return fmt.Errorf("zeus provider not implemented")
}

func (z *ZeusProvider) EndWindow(_ context.Context, _ string) (float64, error) {
	return 0, fmt.Errorf("zeus provider not implemented")
}

func (z *ZeusProvider) IdlePower(_ context.Context) (float64, error) {
	return 0, fmt.Errorf("zeus provider not implemented")
}

func (z *ZeusProvider) Devices(_ context.Context) ([]provider.Device, error) {
	return nil, fmt.Errorf("zeus provider not implemented")
}
