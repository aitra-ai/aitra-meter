package zeus

import (
	"context"
	"fmt"

	"github.com/aitra-ai/aitra-meter/internal/provider"
)

func init() {
	provider.RegisterEnergy("zeus", func(config map[string]string) (provider.EnergyProvider, error) {
		return &ZeusProvider{}, nil
	})
}

// ZeusProvider implements provider.EnergyProvider using the Zeus ML.ENERGY library.
// Zeus is invoked via a sidecar process; communication is over a Unix socket.
type ZeusProvider struct{}

func (z *ZeusProvider) Name() string { return "zeus" }

func (z *ZeusProvider) BeginWindow(ctx context.Context, windowID string) error {
	// TODO: call Zeus ZeusMonitor.begin_window via sidecar
	return fmt.Errorf("not implemented")
}

func (z *ZeusProvider) EndWindow(ctx context.Context, windowID string) (float64, error) {
	// TODO: call Zeus ZeusMonitor.end_window via sidecar, return joules
	return 0, fmt.Errorf("not implemented")
}

func (z *ZeusProvider) IdlePower(ctx context.Context) (float64, error) {
	// TODO: read NVML power via Zeus
	return 0, fmt.Errorf("not implemented")
}

func (z *ZeusProvider) Devices(ctx context.Context) ([]provider.Device, error) {
	// TODO: enumerate NVML devices via Zeus
	return nil, fmt.Errorf("not implemented")
}
