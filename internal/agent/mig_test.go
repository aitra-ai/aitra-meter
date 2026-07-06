package agent

import (
	"context"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"go.uber.org/zap"

	"github.com/aitra-ai/aitra-meter/internal/metrics"
	"github.com/aitra-ai/aitra-meter/internal/provider"
)

// migSliceEnergy builds a MIGSliceEnergy for tests.
func migSliceEnergy(parent, instance, uuid string, joules, watts float64) provider.MIGSliceEnergy {
	return provider.MIGSliceEnergy{
		Slice: provider.MIGSlice{
			ParentUUID: parent,
			Instance:   instance,
			UUID:       uuid,
		},
		Joules:     joules,
		PowerWatts: watts,
	}
}

func TestResolvePinnedSlice(t *testing.T) {
	a := migSliceEnergy("GPU-A", "mig-1g.10gb:0", "MIG-aaa", 3, 0.3)
	b := migSliceEnergy("GPU-A", "mig-1g.10gb:1", "MIG-bbb", 4, 0.4)

	tests := []struct {
		name     string
		pin      string
		slices   []provider.MIGSliceEnergy
		wantOK   bool
		wantUUID string
	}{
		{"empty pin, single slice auto-pins", "", []provider.MIGSliceEnergy{a}, true, "MIG-aaa"},
		{"empty pin, multiple slices unresolved", "", []provider.MIGSliceEnergy{a, b}, false, ""},
		{"pin by instance label", "mig-1g.10gb:1", []provider.MIGSliceEnergy{a, b}, true, "MIG-bbb"},
		{"pin by MIG UUID", "MIG-aaa", []provider.MIGSliceEnergy{a, b}, true, "MIG-aaa"},
		{"pin with no match", "mig-2g.20gb:0", []provider.MIGSliceEnergy{a, b}, false, ""},
		{"no slices", "mig-1g.10gb:0", nil, false, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := resolvePinnedSlice(tc.pin, tc.slices)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if ok && got.Slice.UUID != tc.wantUUID {
				t.Errorf("resolved UUID = %q, want %q", got.Slice.UUID, tc.wantUUID)
			}
		})
	}
}

func TestObserveMIGWindowPinnedSlice(t *testing.T) {
	// Unique label values so parallel/global metric state cannot collide
	// with other tests.
	const (
		node    = "mig-obs-node-1"
		parent  = "GPU-obs-1"
		instA   = "mig-1g.10gb:0"
		instB   = "mig-1g.10gb:1"
		nsLabel = "tenant-a"
		model   = "llama-3-8b"
		team    = "ml-platform"
	)
	slices := []provider.MIGSliceEnergy{
		migSliceEnergy(parent, instA, "MIG-obs-aaa", 30, 1.0),
		migSliceEnergy(parent, instB, "MIG-obs-bbb", 45, 1.5),
	}
	att := MIGAttribution{
		PinnedInstance:        instA,
		Namespace:             nsLabel,
		Team:                  team,
		ElectricityCostPerKWh: 0.20,
	}

	observeMIGWindow(node, model, att, slices, 600)

	// Power is recorded for every slice.
	if got := testutil.ToFloat64(metrics.MIGPowerWatts.WithLabelValues(node, parent, instA)); got != 1.0 {
		t.Errorf("MIGPowerWatts[%s] = %v, want 1.0", instA, got)
	}
	if got := testutil.ToFloat64(metrics.MIGPowerWatts.WithLabelValues(node, parent, instB)); got != 1.5 {
		t.Errorf("MIGPowerWatts[%s] = %v, want 1.5", instB, got)
	}

	// Token-derived metrics only for the pinned slice.
	if got := testutil.ToFloat64(metrics.MIGTokensTotal.WithLabelValues(node, parent, instA, nsLabel, model)); got != 600 {
		t.Errorf("MIGTokensTotal = %v, want 600", got)
	}
	if got := testutil.ToFloat64(metrics.MIGJPerToken.WithLabelValues(node, parent, instA, nsLabel, model)); got != 30.0/600 {
		t.Errorf("MIGJPerToken = %v, want %v", got, 30.0/600)
	}
	joules, price := 30.0, 0.20
	wantCost := joules / 3_600_000.0 * price // runtime arithmetic, same rounding as the code under test
	if got := testutil.ToFloat64(metrics.MIGCostUSDTotal.WithLabelValues(node, parent, instA, nsLabel, team)); got != wantCost {
		t.Errorf("MIGCostUSDTotal = %v, want %v", got, wantCost)
	}

	// The unpinned slice must not have token metrics.
	if got := testutil.ToFloat64(metrics.MIGTokensTotal.WithLabelValues(node, parent, instB, nsLabel, model)); got != 0 {
		t.Errorf("MIGTokensTotal for unpinned slice = %v, want 0", got)
	}
}

func TestObserveMIGWindowIdleWindowRecordsPowerOnly(t *testing.T) {
	const (
		node   = "mig-obs-node-2"
		parent = "GPU-obs-2"
		inst   = "mig-2g.20gb:0"
	)
	slices := []provider.MIGSliceEnergy{migSliceEnergy(parent, inst, "MIG-obs-ccc", 12, 0.4)}

	observeMIGWindow(node, "m", MIGAttribution{}, slices, 0)

	if got := testutil.ToFloat64(metrics.MIGPowerWatts.WithLabelValues(node, parent, inst)); got != 0.4 {
		t.Errorf("MIGPowerWatts = %v, want 0.4", got)
	}
	// tokenDelta == 0 → no tokens, no J/token (labels default to "unknown").
	if got := testutil.ToFloat64(metrics.MIGTokensTotal.WithLabelValues(node, parent, inst, "unknown", "m")); got != 0 {
		t.Errorf("MIGTokensTotal = %v, want 0 for idle window", got)
	}
}

func TestObserveMIGWindowCostDisabledWithoutPrice(t *testing.T) {
	const (
		node   = "mig-obs-node-3"
		parent = "GPU-obs-3"
		inst   = "mig-1g.10gb:0"
	)
	slices := []provider.MIGSliceEnergy{migSliceEnergy(parent, inst, "MIG-obs-ddd", 10, 0.3)}

	observeMIGWindow(node, "m", MIGAttribution{}, slices, 100)

	if got := testutil.ToFloat64(metrics.MIGCostUSDTotal.WithLabelValues(node, parent, inst, "unknown", "unknown")); got != 0 {
		t.Errorf("MIGCostUSDTotal = %v, want 0 when ElectricityCostPerKWh is unset", got)
	}
	// Tokens and J/token still recorded with defaulted labels.
	if got := testutil.ToFloat64(metrics.MIGTokensTotal.WithLabelValues(node, parent, inst, "unknown", "m")); got != 100 {
		t.Errorf("MIGTokensTotal = %v, want 100", got)
	}
}

// --- loop integration ---------------------------------------------------------

// fakeMIGEnergy implements provider.MIGEnergyProvider for loop tests.
type fakeMIGEnergy struct {
	joules float64
	slices []provider.MIGSliceEnergy
}

func (f *fakeMIGEnergy) Name() string                                  { return "fake-mig-energy" }
func (f *fakeMIGEnergy) BeginWindow(_ context.Context, _ string) error { return nil }
func (f *fakeMIGEnergy) EndWindow(_ context.Context, _ string) (float64, error) {
	return f.joules, nil
}
func (f *fakeMIGEnergy) IdlePower(_ context.Context) (float64, error)         { return 0, nil }
func (f *fakeMIGEnergy) Devices(_ context.Context) ([]provider.Device, error) { return nil, nil }
func (f *fakeMIGEnergy) MIGEnabled() bool                                     { return true }
func (f *fakeMIGEnergy) MIGSlices(_ context.Context) ([]provider.MIGSlice, error) {
	out := make([]provider.MIGSlice, len(f.slices))
	for i, se := range f.slices {
		out[i] = se.Slice
	}
	return out, nil
}
func (f *fakeMIGEnergy) EndWindowMIG(_ context.Context, _ string) (float64, []provider.MIGSliceEnergy, error) {
	return f.joules, f.slices, nil
}

func TestLoopMIGWindowPopulatesSliceMetrics(t *testing.T) {
	addr, svc, stop := startFakeAggSvc(t)
	defer stop()

	const (
		node   = "mig-loop-node"
		parent = "GPU-loop-1"
		inst   = "mig-3g.40gb:0"
		nsL    = "tenant-loop"
	)
	energy := &fakeMIGEnergy{
		joules: 90,
		slices: []provider.MIGSliceEnergy{migSliceEnergy(parent, inst, "MIG-loop-aaa", 90, 3.0)},
	}
	loop, err := New(Config{
		Node:              node,
		AggregatorAddr:    addr,
		WindowDuration:    20 * time.Millisecond,
		EnergyProvider:    energy,
		InferenceProvider: &incrementingInference{step: 250, model: "m-loop"},
		MIG:               MIGAttribution{Namespace: nsL},
	}, zap.NewNop())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer loop.Close() //nolint:errcheck

	runFor(loop, 120*time.Millisecond)

	if len(svc.all()) < 2 {
		t.Fatalf("expected ≥2 WindowReports, got %d", len(svc.all()))
	}

	// Power gauge set for the slice.
	if got := testutil.ToFloat64(metrics.MIGPowerWatts.WithLabelValues(node, parent, inst)); got != 3.0 {
		t.Errorf("MIGPowerWatts = %v, want 3.0", got)
	}
	// Single slice + no explicit pin → auto-pinned; token deltas of 250
	// accrue from the second window onward.
	tokens := testutil.ToFloat64(metrics.MIGTokensTotal.WithLabelValues(node, parent, inst, nsL, "m-loop"))
	if tokens == 0 {
		t.Error("MIGTokensTotal = 0, want > 0 (auto-pinned single slice)")
	}
	if int(tokens)%250 != 0 {
		t.Errorf("MIGTokensTotal = %v, want a multiple of 250", tokens)
	}
	if got := testutil.ToFloat64(metrics.MIGJPerToken.WithLabelValues(node, parent, inst, nsL, "m-loop")); got != 90.0/250 {
		t.Errorf("MIGJPerToken = %v, want %v", got, 90.0/250)
	}
}
