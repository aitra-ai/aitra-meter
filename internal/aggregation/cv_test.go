package aggregation

import (
	"math"
	"testing"
)

func TestNewCVTrackerPanic(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic for size < 2")
		}
	}()
	NewCVTracker(1)
}

func TestCVTrackerEmpty(t *testing.T) {
	c := NewCVTracker(10)
	if c.Len() != 0 {
		t.Errorf("Len = %d, want 0", c.Len())
	}
	if c.Mean() != 0 {
		t.Errorf("Mean = %f, want 0", c.Mean())
	}
	if c.StdDev() != 0 {
		t.Errorf("StdDev = %f, want 0", c.StdDev())
	}
	if c.CV() != 0 {
		t.Errorf("CV = %f, want 0", c.CV())
	}
	if !c.Stable() {
		t.Error("Stable = false with no samples, want true")
	}
}

func TestCVTrackerSingleSample(t *testing.T) {
	c := NewCVTracker(10)
	c.Add(2.0)
	if c.Len() != 1 {
		t.Errorf("Len = %d, want 1", c.Len())
	}
	if c.Mean() != 2.0 {
		t.Errorf("Mean = %f, want 2.0", c.Mean())
	}
	if c.StdDev() != 0 {
		t.Errorf("StdDev = %f, want 0 (single sample)", c.StdDev())
	}
	if !c.Stable() {
		t.Error("Stable = false for single sample, want true")
	}
}

func TestCVTrackerMeanAndStdDev(t *testing.T) {
	// Samples: 2, 4, 4, 4, 5, 5, 7, 9
	// Mean = 5.0, population stddev = 2.0, CV = 0.4
	c := NewCVTracker(100)
	for _, v := range []float64{2, 4, 4, 4, 5, 5, 7, 9} {
		c.Add(v)
	}
	if c.Len() != 8 {
		t.Errorf("Len = %d, want 8", c.Len())
	}
	if math.Abs(c.Mean()-5.0) > 1e-9 {
		t.Errorf("Mean = %f, want 5.0", c.Mean())
	}
	if math.Abs(c.StdDev()-2.0) > 1e-9 {
		t.Errorf("StdDev = %f, want 2.0", c.StdDev())
	}
	if math.Abs(c.CV()-0.4) > 1e-9 {
		t.Errorf("CV = %f, want 0.4", c.CV())
	}
	if c.Stable() {
		t.Error("Stable = true for CV=0.4, want false (threshold 0.03)")
	}
}

func TestCVTrackerStable(t *testing.T) {
	// Samples very close to each other: CV well below 3%
	c := NewCVTracker(100)
	for i := 0; i < 20; i++ {
		c.Add(1.000 + float64(i)*0.0001) // ±0.01% variation
	}
	if !c.Stable() {
		t.Errorf("Stable = false for low-variance data, CV = %f", c.CV())
	}
	if c.CV() >= CVThreshold {
		t.Errorf("CV = %f, should be below threshold %f", c.CV(), CVThreshold)
	}
}

func TestCVTrackerRingEviction(t *testing.T) {
	// Window size = 3. Add samples [1, 1, 1, 100] — after eviction the
	// buffer holds [1, 1, 100], not the old low-variance set.
	c := NewCVTracker(3)
	c.Add(1)
	c.Add(1)
	c.Add(1)
	if c.Len() != 3 {
		t.Fatalf("Len = %d, want 3", c.Len())
	}
	c.Add(100) // evicts first 1; buffer = [1, 1, 100]
	if c.Len() != 3 {
		t.Fatalf("Len after overflow = %d, want 3", c.Len())
	}
	if math.Abs(c.Mean()-(102.0/3.0)) > 1e-9 {
		t.Errorf("Mean after eviction = %f, want %f", c.Mean(), 102.0/3.0)
	}
	if c.Stable() {
		t.Error("Stable = true after injecting outlier, want false")
	}
}

func TestCVTrackerRingFull(t *testing.T) {
	// Completely fill and then rotate the ring several times.
	// After many identical samples the CV must remain 0.
	c := NewCVTracker(10)
	for i := 0; i < 50; i++ {
		c.Add(3.14)
	}
	if c.Len() != 10 {
		t.Errorf("Len = %d, want 10 (capped at window size)", c.Len())
	}
	if c.CV() != 0 {
		t.Errorf("CV = %f for constant series, want 0", c.CV())
	}
	if !c.Stable() {
		t.Error("Stable = false for constant series, want true")
	}
}

func TestCVThresholdBoundary(t *testing.T) {
	// Samples 0.97 and 1.03: mathematical CV = 0.03/1.0 = CVThreshold.
	// Floating-point arithmetic means the computed value may land just above
	// or just below the threshold — so we only assert the magnitude, not
	// the Stable() outcome at the exact boundary.
	c := NewCVTracker(10)
	c.Add(0.97)
	c.Add(1.03)
	cv := c.CV()
	if math.Abs(cv-CVThreshold) > 1e-10 {
		t.Errorf("CV = %.15f, want ≈%.15f (CVThreshold)", cv, CVThreshold)
	}
}

func TestCVJustBelowThreshold(t *testing.T) {
	// Samples: 1.0 ± ε where ε gives CV just below 0.03
	c := NewCVTracker(10)
	d := CVThreshold*1.0 - 1e-12 // d/mean = 0.03 - ε
	c.Add(1.0 - d)
	c.Add(1.0 + d)
	if !c.Stable() {
		t.Errorf("Stable = false for CV just below threshold, CV = %f", c.CV())
	}
}
