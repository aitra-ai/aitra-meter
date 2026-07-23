//go:build linux

// NVML-backed side of MIG attribution (issue #43). Everything in this file
// touches go-nvml and is therefore gated to linux, like nvml.go. The
// portable accounting it feeds lives in mig.go.

package nvml

import (
	"context"
	"fmt"

	gonvml "github.com/NVIDIA/go-nvml/pkg/nvml"

	"github.com/aitra-ai/aitra-meter/internal/provider"
)

// NVMLProvider implements provider.MIGEnergyProvider when MIG mode is
// detected at init (n.mig != nil).
var _ provider.MIGEnergyProvider = (*NVMLProvider)(nil)

// detectMIGMode reports whether any GPU on the node has MIG mode enabled.
// GPUs that do not support MIG return ERROR_NOT_SUPPORTED and are skipped.
func detectMIGMode() bool {
	count, ret := gonvml.DeviceGetCount()
	if ret != gonvml.SUCCESS {
		return false
	}
	for i := 0; i < count; i++ {
		dev, ret := gonvml.DeviceGetHandleByIndex(i)
		if ret != gonvml.SUCCESS {
			continue
		}
		current, _, ret := gonvml.DeviceGetMigMode(dev)
		if ret == gonvml.SUCCESS && current == gonvml.DEVICE_MIG_ENABLE {
			return true
		}
	}
	return false
}

// nvmlMIGReader implements migReader over go-nvml.
type nvmlMIGReader struct{}

// Slices enumerates MIG compute instances across all MIG-enabled GPUs.
// Unconfigured MIG device indexes (no instance behind them) are skipped, so
// Index is the dense enumeration order — the same order `nvidia-smi -L`
// lists MIG devices on the parent.
func (nvmlMIGReader) Slices() ([]provider.MIGSlice, error) {
	count, ret := gonvml.DeviceGetCount()
	if ret != gonvml.SUCCESS {
		return nil, fmt.Errorf("DeviceGetCount: %s", gonvml.ErrorString(ret))
	}

	var slices []provider.MIGSlice
	for i := 0; i < count; i++ {
		dev, ret := gonvml.DeviceGetHandleByIndex(i)
		if ret != gonvml.SUCCESS {
			continue
		}
		current, _, ret := gonvml.DeviceGetMigMode(dev)
		if ret != gonvml.SUCCESS || current != gonvml.DEVICE_MIG_ENABLE {
			continue // MIG unsupported (pre-Ampere) or disabled on this GPU
		}
		parentUUID, ret := gonvml.DeviceGetUUID(dev)
		if ret != gonvml.SUCCESS {
			continue
		}
		maxMig, ret := gonvml.DeviceGetMaxMigDeviceCount(dev)
		if ret != gonvml.SUCCESS {
			continue
		}

		idx := 0
		for j := 0; j < maxMig; j++ {
			mig, ret := gonvml.DeviceGetMigDeviceHandleByIndex(dev, j)
			if ret != gonvml.SUCCESS {
				continue // index not backed by a configured instance
			}
			s := provider.MIGSlice{
				ParentUUID:  parentUUID,
				ParentIndex: i,
				Index:       idx,
			}
			if uuid, ret := gonvml.DeviceGetUUID(mig); ret == gonvml.SUCCESS {
				s.UUID = uuid
			}
			if gi, ret := gonvml.DeviceGetGpuInstanceId(mig); ret == gonvml.SUCCESS {
				s.GPUInstanceID = gi
			}
			if attrs, ret := gonvml.DeviceGetAttributes(mig); ret == gonvml.SUCCESS {
				s.ComputeSlices = int(attrs.GpuInstanceSliceCount)
				s.MemoryMB = attrs.MemorySizeMB
			}
			s.Profile = migProfileName(s.ComputeSlices, s.MemoryMB)
			s.Instance = migInstanceLabel(s.Profile, s.Index)
			slices = append(slices, s)
			idx++
		}
	}
	return slices, nil
}

// ParentEnergyMillijoules reads the board-level energy accumulator of the
// physical GPU identified by uuid.
func (nvmlMIGReader) ParentEnergyMillijoules(uuid string) (float64, error) {
	dev, ret := gonvml.DeviceGetHandleByUUID(uuid)
	if ret != gonvml.SUCCESS {
		return 0, fmt.Errorf("DeviceGetHandleByUUID(%s): %s", uuid, gonvml.ErrorString(ret))
	}
	mj, ret := gonvml.DeviceGetTotalEnergyConsumption(dev)
	if ret != gonvml.SUCCESS {
		return 0, fmt.Errorf("DeviceGetTotalEnergyConsumption(%s): %s", uuid, gonvml.ErrorString(ret))
	}
	return float64(mj), nil
}

// --- provider.MIGEnergyProvider ---------------------------------------------

// MIGEnabled reports whether MIG mode was detected at provider init.
func (n *NVMLProvider) MIGEnabled() bool { return n.mig != nil }

// MIGSlices enumerates the MIG compute instances currently configured.
// Returns an empty list on nodes without MIG mode.
func (n *NVMLProvider) MIGSlices(ctx context.Context) ([]provider.MIGSlice, error) {
	if n.mig == nil {
		return nil, nil
	}
	return n.mig.reader.Slices()
}

// EndWindowMIG ends the window like EndWindow and additionally returns the
// per-slice energy breakdown. Per-slice attribution is best effort: if it
// fails, the total is still returned with an empty slice list.
func (n *NVMLProvider) EndWindowMIG(ctx context.Context, windowID string) (float64, []provider.MIGSliceEnergy, error) {
	joules, err := n.EndWindow(ctx, windowID)
	if err != nil {
		if n.mig != nil {
			n.mig.discardWindow(windowID)
		}
		return 0, nil, err
	}
	if n.mig == nil {
		return joules, nil, nil
	}
	slices, err := n.mig.endWindow(windowID)
	if err != nil {
		return joules, nil, nil // total is valid; per-slice breakdown unavailable
	}
	return joules, slices, nil
}
