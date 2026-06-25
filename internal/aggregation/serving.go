package aggregation

// DefaultServingWindowSize is the number of recent windows used for the
// serving-utilization and idle-time ratios. At the default 30s measurement
// window this spans ~1 hour, matching the aitra_idle_time_ratio definition.
const DefaultServingWindowSize = 120

// servingTracker is a fixed-size ring over the most recent windows for one node,
// recording whether each window was serving (output tokens > 0) or idle. It
// yields the serving fraction across the ring in O(1) per update.
type servingTracker struct {
	ring    []bool
	idx     int // next slot to write
	count   int // filled slots (<= len(ring))
	serving int // serving slots currently in the ring
}

func newServingTracker(size int) *servingTracker {
	if size <= 0 {
		size = DefaultServingWindowSize
	}
	return &servingTracker{ring: make([]bool, size)}
}

// add records one window and returns the serving fraction over the ring (0.0–1.0).
func (s *servingTracker) add(serving bool) float64 {
	if s.count == len(s.ring) {
		if s.ring[s.idx] { // evicting a serving slot
			s.serving--
		}
	} else {
		s.count++
	}
	s.ring[s.idx] = serving
	if serving {
		s.serving++
	}
	s.idx = (s.idx + 1) % len(s.ring)
	return float64(s.serving) / float64(s.count)
}
