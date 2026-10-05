package aggregation

import "time"

// DefaultClusterWindow bounds how far back serving windows contribute to the
// cluster-wide J/token aggregate. Long enough to smooth per-model report
// jitter, short enough that the gauge tracks load changes within a minute.
const DefaultClusterWindow = 60 * time.Second

type clusterSample struct {
	at     time.Time
	joules float64
	tokens float64
}

// clusterJPTTracker derives the cluster-wide J/token as Σenergy ÷ Σtokens
// over the serving windows retained in its time span (issue: the
// aitra_cluster_j_per_token gauge was declared but never set). Summing before
// dividing weights each window by its token count; averaging per-window
// ratios would not (see TestLoopClusterJPerTokenIsSumOfEnergyDividedBySumOfTokens).
//
// Not safe for concurrent use; callers hold the Loop mutex.
type clusterJPTTracker struct {
	window  time.Duration
	samples []clusterSample // arrival order; evicted from the front
	joules  float64
	tokens  float64
}

func newClusterJPTTracker(window time.Duration) *clusterJPTTracker {
	if window <= 0 {
		window = DefaultClusterWindow
	}
	return &clusterJPTTracker{window: window}
}

// add records one serving window and returns Σenergy ÷ Σtokens over the
// retained span, or 0 when no tokens are retained.
func (c *clusterJPTTracker) add(now time.Time, joules, tokens float64) float64 {
	c.samples = append(c.samples, clusterSample{at: now, joules: joules, tokens: tokens})
	c.joules += joules
	c.tokens += tokens

	cutoff := now.Add(-c.window)
	evict := 0
	for evict < len(c.samples) && c.samples[evict].at.Before(cutoff) {
		c.joules -= c.samples[evict].joules
		c.tokens -= c.samples[evict].tokens
		evict++
	}
	if evict > 0 {
		c.samples = append(c.samples[:0], c.samples[evict:]...)
	}

	if c.tokens <= 0 {
		return 0
	}
	return c.joules / c.tokens
}
