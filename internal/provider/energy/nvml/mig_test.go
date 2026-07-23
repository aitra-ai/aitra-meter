// Unit tests for MIG attribution accounting (issue #43). These run on any
// platform: the NVML calls are mocked behind the migReader interface, so no
// GPU, CGO, or linux build tag is needed. Behaviour against real MIG
// hardware is covered by the (pending) integration test documented in
// docs/reference/mig-support.md.

package nvml

import (
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/aitra-ai/aitra-meter/internal/provider"
)

// fakeMIGReader is a mock migReader. energyMJ is mutated between
// beginWindow and endWindow to simulate counter advancement.
type fakeMIGReader struct {
	slices    []provider.MIGSlice
	slicesErr error
	energyMJ  map[string]float64
	energyErr map[string]error
}

func (f *fakeMIGReader) Slices() ([]provider.MIGSlice, error) {
	return f.slices, f.slicesErr
}

func (f *fakeMIGReader) ParentEnergyMillijoules(uuid string) (float64, error) {
	if err := f.energyErr[uuid]; err != nil {
		return 0, err
	}
	v, ok := f.energyMJ[uuid]
	if !ok {
		return 0, fmt.Errorf("unknown parent %q", uuid)
	}
	return v, nil
}

// newTestTracker returns a tracker with a controllable clock.
func newTestTracker(r migReader) (*migTracker, *time.Time) {
	tr := newMIGTracker(r)
	now := time.Unix(1_700_000_000, 0)
	tr.now = func() time.Time { return now }
	return tr, &now
}

func slice(parent string, parentIdx, idx, computeSlices int, memMB uint64) provider.MIGSlice {
	profile := migProfileName(computeSlices, memMB)
	return provider.MIGSlice{
		ParentUUID:    parent,
		ParentIndex:   parentIdx,
		Index:         idx,
		UUID:          fmt.Sprintf("MIG-%s-%d", parent, idx),
		Profile:       profile,
		Instance:      migInstanceLabel(profile, idx),
		ComputeSlices: computeSlices,
		MemoryMB:      memMB,
	}
}

func almostEqual(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

// --- label helpers ------------------------------------------------------------

func TestMIGProfileName(t *testing.T) {
	tests := []struct {
		computeSlices int
		memoryMB      uint64
		want          string
	}{
		{1, 4864, "1g.5gb"},   // A100 40GB, usable 4.75 GiB
		{1, 9728, "1g.10gb"},  // A100 80GB, usable 9.5 GiB
		{2, 19968, "2g.20gb"}, // A100 80GB, usable 19.5 GiB
		{3, 40192, "3g.40gb"}, // A100 80GB, usable 39.25 GiB
		{1, 12032, "1g.12gb"}, // H100 94GB variant
		{7, 81152, "7g.80gb"}, // full A100 80GB
		{4, 40960, "4g.40gb"}, // exact GiB boundary stays put
		{0, 0, "0g.0gb"},      // attributes unavailable — degenerate but stable
	}
	for _, tc := range tests {
		if got := migProfileName(tc.computeSlices, tc.memoryMB); got != tc.want {
			t.Errorf("migProfileName(%d, %d) = %q, want %q", tc.computeSlices, tc.memoryMB, got, tc.want)
		}
	}
}

func TestMIGInstanceLabel(t *testing.T) {
	if got := migInstanceLabel("1g.10gb", 0); got != "mig-1g.10gb:0" {
		t.Errorf("migInstanceLabel = %q, want mig-1g.10gb:0", got)
	}
	if got := migInstanceLabel("2g.20gb", 3); got != "mig-2g.20gb:3" {
		t.Errorf("migInstanceLabel = %q, want mig-2g.20gb:3", got)
	}
}

// --- tracker ------------------------------------------------------------------

func TestMIGTrackerProportionalSplit(t *testing.T) {
	// One parent, two slices with 3 and 4 compute slices. Parent consumes
	// 7 J over a 10 s window → 3 J and 4 J; 0.3 W and 0.4 W.
	reader := &fakeMIGReader{
		slices: []provider.MIGSlice{
			slice("GPU-A", 0, 0, 3, 40192),
			slice("GPU-A", 0, 1, 4, 40192),
		},
		energyMJ: map[string]float64{"GPU-A": 1000},
	}
	tr, now := newTestTracker(reader)

	if err := tr.beginWindow("w1"); err != nil {
		t.Fatalf("beginWindow: %v", err)
	}
	*now = now.Add(10 * time.Second)
	reader.energyMJ["GPU-A"] = 8000 // +7000 mJ

	got, err := tr.endWindow("w1")
	if err != nil {
		t.Fatalf("endWindow: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d slice energies, want 2", len(got))
	}
	if !almostEqual(got[0].Joules, 3.0) || !almostEqual(got[1].Joules, 4.0) {
		t.Errorf("joules = %.3f, %.3f, want 3, 4", got[0].Joules, got[1].Joules)
	}
	if !almostEqual(got[0].PowerWatts, 0.3) || !almostEqual(got[1].PowerWatts, 0.4) {
		t.Errorf("watts = %.3f, %.3f, want 0.3, 0.4", got[0].PowerWatts, got[1].PowerWatts)
	}
	// Conservation: per-slice energies sum to the parent's measured delta.
	if sum := got[0].Joules + got[1].Joules; !almostEqual(sum, 7.0) {
		t.Errorf("energy not conserved: sum = %.3f, want 7", sum)
	}
}

func TestMIGTrackerTwoParents(t *testing.T) {
	reader := &fakeMIGReader{
		slices: []provider.MIGSlice{
			slice("GPU-A", 0, 0, 7, 81152),
			slice("GPU-B", 1, 0, 1, 9728),
			slice("GPU-B", 1, 1, 1, 9728),
		},
		energyMJ: map[string]float64{"GPU-A": 0, "GPU-B": 0},
	}
	tr, now := newTestTracker(reader)

	if err := tr.beginWindow("w"); err != nil {
		t.Fatalf("beginWindow: %v", err)
	}
	*now = now.Add(30 * time.Second)
	reader.energyMJ["GPU-A"] = 10_000 // 10 J
	reader.energyMJ["GPU-B"] = 6_000  // 6 J

	got, err := tr.endWindow("w")
	if err != nil {
		t.Fatalf("endWindow: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d slice energies, want 3", len(got))
	}
	// Sorted by (ParentIndex, Index): GPU-A gets all 10 J; GPU-B splits 6 J equally.
	if !almostEqual(got[0].Joules, 10.0) {
		t.Errorf("GPU-A slice joules = %.3f, want 10", got[0].Joules)
	}
	if !almostEqual(got[1].Joules, 3.0) || !almostEqual(got[2].Joules, 3.0) {
		t.Errorf("GPU-B slice joules = %.3f, %.3f, want 3, 3", got[1].Joules, got[2].Joules)
	}
}

func TestMIGTrackerEqualSplitWhenAttributesUnavailable(t *testing.T) {
	// All ComputeSlices zero (DeviceGetAttributes failed) → equal split so
	// energy is not dropped.
	reader := &fakeMIGReader{
		slices: []provider.MIGSlice{
			slice("GPU-A", 0, 0, 0, 0),
			slice("GPU-A", 0, 1, 0, 0),
		},
		energyMJ: map[string]float64{"GPU-A": 0},
	}
	tr, now := newTestTracker(reader)
	if err := tr.beginWindow("w"); err != nil {
		t.Fatalf("beginWindow: %v", err)
	}
	*now = now.Add(time.Second)
	reader.energyMJ["GPU-A"] = 4000

	got, err := tr.endWindow("w")
	if err != nil {
		t.Fatalf("endWindow: %v", err)
	}
	if len(got) != 2 || !almostEqual(got[0].Joules, 2.0) || !almostEqual(got[1].Joules, 2.0) {
		t.Fatalf("equal split failed: %+v", got)
	}
}

func TestMIGTrackerUnknownWindow(t *testing.T) {
	tr, _ := newTestTracker(&fakeMIGReader{})
	if _, err := tr.endWindow("nope"); err == nil {
		t.Error("endWindow for unknown window should return error, got nil")
	}
}

func TestMIGTrackerCounterResetClampsToZero(t *testing.T) {
	// Energy counter goes backwards (driver reload) → 0 J, not negative.
	reader := &fakeMIGReader{
		slices:   []provider.MIGSlice{slice("GPU-A", 0, 0, 1, 9728)},
		energyMJ: map[string]float64{"GPU-A": 9_000_000},
	}
	tr, now := newTestTracker(reader)
	if err := tr.beginWindow("w"); err != nil {
		t.Fatalf("beginWindow: %v", err)
	}
	*now = now.Add(time.Second)
	reader.energyMJ["GPU-A"] = 100

	got, err := tr.endWindow("w")
	if err != nil {
		t.Fatalf("endWindow: %v", err)
	}
	if len(got) != 1 || got[0].Joules != 0 || got[0].PowerWatts != 0 {
		t.Errorf("counter reset should clamp to zero, got %+v", got)
	}
}

func TestMIGTrackerParentReadFailureSkipsParent(t *testing.T) {
	// GPU-B's counter fails at window start; GPU-A must still be attributed.
	reader := &fakeMIGReader{
		slices: []provider.MIGSlice{
			slice("GPU-A", 0, 0, 1, 9728),
			slice("GPU-B", 1, 0, 1, 9728),
		},
		energyMJ:  map[string]float64{"GPU-A": 0},
		energyErr: map[string]error{"GPU-B": fmt.Errorf("simulated NVML failure")},
	}
	tr, now := newTestTracker(reader)
	if err := tr.beginWindow("w"); err != nil {
		t.Fatalf("beginWindow: %v", err)
	}
	*now = now.Add(time.Second)
	reader.energyMJ["GPU-A"] = 5000

	got, err := tr.endWindow("w")
	if err != nil {
		t.Fatalf("endWindow: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d slice energies, want 1 (GPU-B skipped)", len(got))
	}
	if got[0].Slice.ParentUUID != "GPU-A" || !almostEqual(got[0].Joules, 5.0) {
		t.Errorf("unexpected attribution: %+v", got[0])
	}
}

func TestMIGTrackerSlicesEnumerationErrorFailsBegin(t *testing.T) {
	reader := &fakeMIGReader{slicesErr: fmt.Errorf("simulated enumeration failure")}
	tr, _ := newTestTracker(reader)
	if err := tr.beginWindow("w"); err == nil {
		t.Error("beginWindow should fail when slice enumeration fails")
	}
}

func TestMIGTrackerNoSlices(t *testing.T) {
	// MIG mode on but no compute instances configured: no error, no output.
	reader := &fakeMIGReader{energyMJ: map[string]float64{}}
	tr, now := newTestTracker(reader)
	if err := tr.beginWindow("w"); err != nil {
		t.Fatalf("beginWindow: %v", err)
	}
	*now = now.Add(time.Second)
	got, err := tr.endWindow("w")
	if err != nil {
		t.Fatalf("endWindow: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d slice energies, want 0", len(got))
	}
}

func TestMIGTrackerDiscardWindow(t *testing.T) {
	reader := &fakeMIGReader{
		slices:   []provider.MIGSlice{slice("GPU-A", 0, 0, 1, 9728)},
		energyMJ: map[string]float64{"GPU-A": 0},
	}
	tr, _ := newTestTracker(reader)
	if err := tr.beginWindow("w"); err != nil {
		t.Fatalf("beginWindow: %v", err)
	}
	tr.discardWindow("w")
	if _, err := tr.endWindow("w"); err == nil {
		t.Error("endWindow after discardWindow should return error")
	}
}
