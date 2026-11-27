package carbon

import (
	"context"
	"fmt"
	"log"
	"math"
	"math/rand"
	"sync"
	"time"
)

// Reading represents the currently available grid carbon intensity.
type Reading struct {
	GCO2PerKWh float64
	Source     string
	UpdatedAt  time.Time
}

// Manager maintains a thread-safe carbon intensity cache.
type Manager struct {
	provider Provider
	fallback float64
	interval time.Duration
	jitter   time.Duration

	mu      sync.RWMutex
	reading Reading
}

// NewManager creates a carbon intensity manager.
// The initial cached value uses the static fallback.
func NewManager(
	provider Provider,
	fallback float64,
	interval time.Duration,
	jitter time.Duration,
) *Manager {
	if math.IsNaN(fallback) || math.IsInf(fallback, 0) || fallback < 0 {
		fallback = 0
	}

	if interval <= 0 {
		interval = time.Hour
	}

	if jitter < 0 {
		jitter = 0
	}

	return &Manager{
		provider: provider,
		fallback: fallback,
		interval: interval,
		jitter:   jitter,
		reading: Reading{
			GCO2PerKWh: fallback,
			Source:     "static",
			UpdatedAt:  time.Now(),
		},
	}
}

// Current returns a copy of the latest cached reading.
func (m *Manager) Current() Reading {
	m.mu.RLock()
	defer m.mu.RUnlock()

	return m.reading
}

// Refresh fetches a new reading and updates the cache.
// On failure, it switches to the configured static fallback.
func (m *Manager) Refresh(ctx context.Context) error {
	if m.provider == nil {
		err := fmt.Errorf("carbon intensity provider is not configured")
		m.setFallback()
		return err
	}

	value, err := m.provider.Fetch(ctx)
	if err != nil {
		m.setFallback()
		return err
	}

	if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
		m.setFallback()
		return fmt.Errorf("provider returned invalid carbon intensity: %v", value)
	}

	source := m.provider.Source()
	if source == "" {
		source = "live"
	}

	m.mu.Lock()
	m.reading = Reading{
		GCO2PerKWh: value,
		Source:     source,
		UpdatedAt:  time.Now(),
	}
	m.mu.Unlock()

	return nil
}

// setFallback updates the cache to use the static fallback.
func (m *Manager) setFallback() {
	m.mu.Lock()
	m.reading = Reading{
		GCO2PerKWh: m.fallback,
		Source:     "static",
		UpdatedAt:  time.Now(),
	}
	m.mu.Unlock()
}

// Start runs the polling process until the context is cancelled.
// The cache is already pre-populated with the static fallback value from
// NewManager, so the service answers Kubernetes health checks immediately.
// The first live API request is made asynchronously in the background — it
// does NOT block Start's caller.
func (m *Manager) Start(ctx context.Context) {
	// Kick off the first poll asynchronously so the service starts up and
	// passes /readyz before the first external API round-trip completes.
	go func() {
		if err := m.Refresh(ctx); err != nil && ctx.Err() == nil {
			log.Printf("carbon intensity initial refresh failed; using static fallback: %v", err)
		}
	}()

	for {
		delay := m.interval
		if m.jitter > 0 {
			delay += time.Duration(rand.Int63n(int64(m.jitter) + 1))
		}

		timer := time.NewTimer(delay)

		select {
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return

		case <-timer.C:
			if err := m.Refresh(ctx); err != nil && ctx.Err() == nil {
				log.Printf("carbon intensity refresh failed; using static fallback: %v", err)
			}
		}
	}
}
