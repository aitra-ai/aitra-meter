// MIG (Multi-Instance GPU) attribution accounting for the NVML energy
// provider (issue #43).
//
// This file is intentionally free of build tags and go-nvml imports so the
// attribution logic can be unit-tested on machines without GPUs or CGO. The
// NVML-backed migReader implementation and the provider.MIGEnergyProvider
// methods on NVMLProvider live in mig_nvml.go behind the linux build tag.

package nvml

import (
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/aitra-ai/aitra-meter/internal/provider"
)

// migReader abstracts the NVML queries migTracker needs, so the accounting
// can be tested against a fake without GPU hardware.
type migReader interface {
	// Slices enumerates the MIG compute instances on the node.
	Slices() ([]provider.MIGSlice, error)

	// ParentEnergyMillijoules returns the cumulative energy counter of the
	// physical GPU identified by uuid, in millijoules.
	ParentEnergyMillijoules(uuid string) (float64, error)
}

// migTracker attributes parent-GPU window energy to MIG slices.
//
// NVML does not expose a per-slice energy sensor: A100/H100 boards have
// board-level power rails only, so nvmlDeviceGetTotalEnergyConsumption is
// meaningful on the physical GPU handle, not per MIG instance. The tracker
// therefore measures each parent GPU's energy delta over the window and
// splits it across that GPU's slices proportionally to their compute slice
// count (GpuInstanceSliceCount). The split conserves energy: per-slice values
// on a parent always sum to the parent's measured delta. See
// docs/reference/mig-support.md for the full attribution model.
type migTracker struct {
	reader migReader

	// now is time.Now, injectable for tests.
	now func() time.Time

	mu      sync.Mutex
	windows map[string]*migWindow
}

type migWindow struct {
	start time.Time
	// slices is the MIG geometry captured at window start.
	slices []provider.MIGSlice
	// parentStartMJ holds the start energy counter per parent GPU UUID.
	// Parents whose counter could not be read at window start are absent
	// and are excluded from attribution for this window.
	parentStartMJ map[string]float64
}

func newMIGTracker(reader migReader) *migTracker {
	return &migTracker{
		reader:  reader,
		now:     time.Now,
		windows: make(map[string]*migWindow),
	}
}

// beginWindow snapshots the MIG geometry and per-parent energy counters.
// Geometry is re-read every window because MIG instances can be created or
// destroyed at runtime.
func (t *migTracker) beginWindow(windowID string) error {
	slices, err := t.reader.Slices()
	if err != nil {
		return fmt.Errorf("mig: enumerate slices: %w", err)
	}

	w := &migWindow{
		start:         t.now(),
		slices:        slices,
		parentStartMJ: make(map[string]float64),
	}
	for _, s := range slices {
		if _, ok := w.parentStartMJ[s.ParentUUID]; ok {
			continue
		}
		mj, err := t.reader.ParentEnergyMillijoules(s.ParentUUID)
		if err != nil {
			// Best effort: skip this parent for the window rather than
			// failing the whole measurement.
			continue
		}
		w.parentStartMJ[s.ParentUUID] = mj
	}

	t.mu.Lock()
	t.windows[windowID] = w
	t.mu.Unlock()
	return nil
}

// endWindow computes the per-slice energy breakdown for a window started with
// beginWindow. Parents whose energy counter cannot be read at window end are
// skipped. The result is ordered by (ParentIndex, Index).
func (t *migTracker) endWindow(windowID string) ([]provider.MIGSliceEnergy, error) {
	t.mu.Lock()
	w, ok := t.windows[windowID]
	delete(t.windows, windowID)
	t.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("mig: window %q not found", windowID)
	}

	elapsed := t.now().Sub(w.start).Seconds()

	// Group slices by parent, preserving discovery order.
	byParent := make(map[string][]provider.MIGSlice)
	for _, s := range w.slices {
		byParent[s.ParentUUID] = append(byParent[s.ParentUUID], s)
	}

	var out []provider.MIGSliceEnergy
	for parent, slices := range byParent {
		startMJ, ok := w.parentStartMJ[parent]
		if !ok {
			continue // counter unreadable at window start
		}
		endMJ, err := t.reader.ParentEnergyMillijoules(parent)
		if err != nil {
			continue // counter unreadable at window end
		}
		deltaJ := (endMJ - startMJ) / 1000.0
		if deltaJ < 0 {
			deltaJ = 0 // counter reset (driver reload)
		}
		out = append(out, attributeParent(slices, deltaJ, elapsed)...)
	}

	sort.Slice(out, func(i, j int) bool {
		a, b := out[i].Slice, out[j].Slice
		if a.ParentIndex != b.ParentIndex {
			return a.ParentIndex < b.ParentIndex
		}
		return a.Index < b.Index
	})
	return out, nil
}

// discardWindow drops window state without computing attribution. Called when
// the total energy measurement for the window failed.
func (t *migTracker) discardWindow(windowID string) {
	t.mu.Lock()
	delete(t.windows, windowID)
	t.mu.Unlock()
}

// attributeParent splits one parent GPU's window energy across its slices in
// proportion to ComputeSlices. When no slice reports a compute slice count
// (attribute query failed for all), the energy is split equally so it is
// never silently dropped.
func attributeParent(slices []provider.MIGSlice, parentJoules, elapsedSecs float64) []provider.MIGSliceEnergy {
	if len(slices) == 0 {
		return nil
	}
	totalCompute := 0
	for _, s := range slices {
		totalCompute += s.ComputeSlices
	}

	out := make([]provider.MIGSliceEnergy, 0, len(slices))
	for _, s := range slices {
		var share float64
		if totalCompute > 0 {
			share = float64(s.ComputeSlices) / float64(totalCompute)
		} else {
			share = 1.0 / float64(len(slices))
		}
		joules := parentJoules * share
		var watts float64
		if elapsedSecs > 0 {
			watts = joules / elapsedSecs
		}
		out = append(out, provider.MIGSliceEnergy{
			Slice:      s,
			Joules:     joules,
			PowerWatts: watts,
		})
	}
	return out
}

// migProfileName builds the DCGM-style profile name, e.g. "1g.10gb".
//
// NVML reports the usable framebuffer, which is slightly below the nominal
// profile size because the driver reserves memory (e.g. 9728 MiB for a
// 1g.10gb instance, 40192 MiB for 3g.40gb). Rounding up to the next GiB
// therefore recovers the nominal size for every A100/H100 profile we are
// aware of. This derivation has not been verified on MIG hardware yet; if a
// profile name comes out wrong the label is still stable and unique per
// slice — see docs/reference/mig-support.md.
func migProfileName(computeSlices int, memoryMB uint64) string {
	gb := (memoryMB + 1023) / 1024
	return fmt.Sprintf("%dg.%dgb", computeSlices, gb)
}

// migInstanceLabel builds the mig_instance metric label value,
// e.g. "mig-1g.10gb:0". index is the MIG device index on the parent GPU.
func migInstanceLabel(profile string, index int) string {
	return fmt.Sprintf("mig-%s:%d", profile, index)
}
