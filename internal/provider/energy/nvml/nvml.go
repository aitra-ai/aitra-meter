//go:build linux

// Package nvml provides a pure-Go EnergyProvider using go-nvml.
// This is the recommended provider for NVIDIA GPUs when a Python
// sidecar is not desirable. Zeus remains the default for broader
// hardware support.
package nvml

import (
	"context"
	"fmt"
	"sync"
	"time"

	gonvml "github.com/NVIDIA/go-nvml/pkg/nvml"
	"github.com/aitra-ai/aitra-meter/internal/provider"
)

func init() {
	provider.RegisterEnergy("nvml", func(config map[string]string) (provider.EnergyProvider, error) {
		p := &NVMLProvider{}
		if err := p.init(); err != nil {
			return nil, fmt.Errorf("nvml init: %w", err)
		}
		return p, nil
	})
}

// NVMLProvider implements provider.EnergyProvider using direct NVML bindings.
// No Python dependency. NVIDIA GPUs only.
type NVMLProvider struct {
	mu      sync.Mutex
	windows map[string]*window
}

type window struct {
	startTime    time.Time
	startEnergy  float64
}

func (n *NVMLProvider) init() error {
	ret := gonvml.Init()
	if ret != gonvml.SUCCESS {
		return fmt.Errorf("nvml.Init: %s", gonvml.ErrorString(ret))
	}
	n.windows = make(map[string]*window)
	return nil
}

func (n *NVMLProvider) Name() string { return "nvml" }

func (n *NVMLProvider) BeginWindow(ctx context.Context, windowID string) error {
	energy, err := n.totalEnergyMillijoules()
	if err != nil {
		return err
	}
	n.mu.Lock()
	n.windows[windowID] = &window{startTime: time.Now(), startEnergy: energy}
	n.mu.Unlock()
	return nil
}

func (n *NVMLProvider) EndWindow(ctx context.Context, windowID string) (float64, error) {
	n.mu.Lock()
	w, ok := n.windows[windowID]
	delete(n.windows, windowID)
	n.mu.Unlock()
	if !ok {
		return 0, fmt.Errorf("window %q not found", windowID)
	}
	endEnergy, err := n.totalEnergyMillijoules()
	if err != nil {
		return 0, err
	}
	joules := (endEnergy - w.startEnergy) / 1000.0
	return joules, nil
}

func (n *NVMLProvider) IdlePower(ctx context.Context) (float64, error) {
	count, ret := gonvml.DeviceGetCount()
	if ret != gonvml.SUCCESS {
		return 0, fmt.Errorf("DeviceGetCount: %s", gonvml.ErrorString(ret))
	}
	var totalWatts float64
	for i := 0; i < count; i++ {
		dev, ret := gonvml.DeviceGetHandleByIndex(i)
		if ret != gonvml.SUCCESS {
			continue
		}
		powerMw, ret := gonvml.DeviceGetPowerUsage(dev)
		if ret == gonvml.SUCCESS {
			totalWatts += float64(powerMw) / 1000.0
		}
	}
	return totalWatts, nil
}

func (n *NVMLProvider) Devices(ctx context.Context) ([]provider.Device, error) {
	count, ret := gonvml.DeviceGetCount()
	if ret != gonvml.SUCCESS {
		return nil, fmt.Errorf("DeviceGetCount: %s", gonvml.ErrorString(ret))
	}
	devices := make([]provider.Device, 0, count)
	for i := 0; i < count; i++ {
		dev, ret := gonvml.DeviceGetHandleByIndex(i)
		if ret != gonvml.SUCCESS {
			continue
		}
		name, ret := gonvml.DeviceGetName(dev)
		if ret != gonvml.SUCCESS {
			name = fmt.Sprintf("GPU %d", i)
		}
		devices = append(devices, provider.Device{
			ID:   fmt.Sprintf("%d", i),
			Name: name,
			Type: "gpu",
		})
	}
	return devices, nil
}

// totalEnergyMillijoules returns summed energy across all NVML devices in mJ.
func (n *NVMLProvider) totalEnergyMillijoules() (float64, error) {
	count, ret := gonvml.DeviceGetCount()
	if ret != gonvml.SUCCESS {
		return 0, fmt.Errorf("DeviceGetCount: %s", gonvml.ErrorString(ret))
	}
	var total float64
	for i := 0; i < count; i++ {
		dev, ret := gonvml.DeviceGetHandleByIndex(i)
		if ret != gonvml.SUCCESS {
			continue
		}
		// DeviceGetTotalEnergyConsumption returns millijoules
		mj, ret := gonvml.DeviceGetTotalEnergyConsumption(dev)
		if ret == gonvml.SUCCESS {
			total += float64(mj)
		}
	}
	return total, nil
}
