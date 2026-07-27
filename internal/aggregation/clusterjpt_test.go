package aggregation

import (
	"testing"
	"time"
)

func TestClusterJPTTrackerSumsBeforeDividing(t *testing.T) {
	tr := newClusterJPTTracker(time.Minute)
	now := time.Unix(1000, 0)

	if got := tr.add(now, 100, 200); got != 0.5 {
		t.Errorf("first window: got %f, want 0.5", got)
	}
	// Σ = 300 J / 300 tokens = 1.0 — not the 1.25 an average of per-window
	// ratios (0.5 and 2.0) would give.
	if got := tr.add(now.Add(time.Second), 200, 100); got != 1.0 {
		t.Errorf("second window: got %f, want Σenergy/Σtokens = 1.0", got)
	}
}

func TestClusterJPTTrackerEvictsExpiredSamples(t *testing.T) {
	tr := newClusterJPTTracker(time.Minute)
	now := time.Unix(1000, 0)

	tr.add(now, 1000, 10) // JPT 100 — should age out
	if got := tr.add(now.Add(2*time.Minute), 100, 100); got != 1.0 {
		t.Errorf("after eviction: got %f, want 1.0 from the surviving window only", got)
	}
}

func TestClusterJPTTrackerZeroTokens(t *testing.T) {
	tr := newClusterJPTTracker(time.Minute)
	if got := tr.add(time.Unix(1000, 0), 50, 0); got != 0 {
		t.Errorf("zero retained tokens: got %f, want 0", got)
	}
}
