// Package aggregation implements the Aitra Meter aggregation logic:
// CV gating, namespace attribution, calibration tier lookup, and the
// main measurement loop.
package aggregation

import "math"

const (
	// DefaultWindowSize is the number of J/token samples used for CV computation.
	DefaultWindowSize = 100

	// CVThreshold is the coefficient-of-variation threshold above which a
	// measurement window is flagged unstable (AC-2).
	CVThreshold = 0.03
)

// CVTracker maintains a rolling window of J/token samples and computes the
// coefficient of variation (σ/μ) over the last WindowSize samples.
// It is not safe for concurrent use; callers must synchronise externally.
type CVTracker struct {
	size    int
	buf     []float64
	head    int // next write position (ring buffer)
	count   int // number of samples added (≤ size)
	sum     float64
	sumSq   float64
}

// NewCVTracker creates a CVTracker with the given ring buffer size.
// Panics if size < 2.
func NewCVTracker(size int) *CVTracker {
	if size < 2 {
		panic("CVTracker: size must be ≥ 2")
	}
	return &CVTracker{
		size: size,
		buf:  make([]float64, size),
	}
}

// Add records a new J/token sample, evicting the oldest when the buffer is full.
func (c *CVTracker) Add(jpt float64) {
	if c.count == c.size {
		// Evict oldest sample from running sums.
		old := c.buf[c.head]
		c.sum -= old
		c.sumSq -= old * old
	} else {
		c.count++
	}
	c.buf[c.head] = jpt
	c.head = (c.head + 1) % c.size
	c.sum += jpt
	c.sumSq += jpt * jpt
}

// Len returns the number of samples currently held.
func (c *CVTracker) Len() int { return c.count }

// Mean returns the arithmetic mean of all current samples.
// Returns 0 if no samples have been added.
func (c *CVTracker) Mean() float64 {
	if c.count == 0 {
		return 0
	}
	return c.sum / float64(c.count)
}

// StdDev returns the population standard deviation of current samples.
// Returns 0 if fewer than 2 samples have been added.
func (c *CVTracker) StdDev() float64 {
	if c.count < 2 {
		return 0
	}
	mean := c.Mean()
	// Var(X) = E[X²] - (E[X])²
	variance := (c.sumSq/float64(c.count)) - (mean * mean)
	if variance < 0 {
		// Guard against floating-point rounding below zero.
		variance = 0
	}
	return math.Sqrt(variance)
}

// CV returns the coefficient of variation (σ/μ).
// Returns 0 if the mean is 0 or fewer than 2 samples are held.
func (c *CVTracker) CV() float64 {
	mean := c.Mean()
	if mean == 0 || c.count < 2 {
		return 0
	}
	return c.StdDev() / mean
}

// Stable reports whether the current CV is below CVThreshold.
// Returns true (stable) when fewer than 2 samples are held — we don't
// flag instability before we have enough data to judge.
func (c *CVTracker) Stable() bool {
	if c.count < 2 {
		return true
	}
	return c.CV() < CVThreshold
}
