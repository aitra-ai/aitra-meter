package carbon

import (
	"context"
	"errors"
	"math"
	"sync"
	"testing"
	"time"
)

type mockProvider struct {
	value  float64
	err    error
	source string
}

// Source returns p.source verbatim, including "".
// Use source:"" in a test to exercise the manager's own "live" safety fallback.
func (p *mockProvider) Source() string { return p.source }

func (p *mockProvider) Fetch(ctx context.Context) (float64, error) {
	if p.err != nil {
		return 0, p.err
	}
	return p.value, nil
}

func TestManagerInitialFallback(t *testing.T) {
	manager := NewManager(
		&mockProvider{value: 200},
		400,
		time.Hour,
		0,
	)

	reading := manager.Current()

	if reading.GCO2PerKWh != 400 {
		t.Errorf("expected fallback 400, got %v", reading.GCO2PerKWh)
	}
	if reading.Source != "static" {
		t.Errorf("expected source static, got %q", reading.Source)
	}
	if reading.UpdatedAt.IsZero() {
		t.Error("expected initial update timestamp")
	}
}

func TestManagerSuccessfulRefresh(t *testing.T) {
	manager := NewManager(
		&mockProvider{value: 245.6, source: "electricitymaps"},
		400,
		time.Hour,
		0,
	)

	err := manager.Refresh(context.Background())
	if err != nil {
		t.Fatalf("Refresh() returned error: %v", err)
	}

	reading := manager.Current()

	if reading.GCO2PerKWh != 245.6 {
		t.Errorf("expected intensity 245.6, got %v", reading.GCO2PerKWh)
	}
	if reading.Source != "electricitymaps" {
		t.Errorf("expected source electricitymaps, got %q", reading.Source)
	}
}

func TestManagerFallbackOnProviderError(t *testing.T) {
	manager := NewManager(
		&mockProvider{err: errors.New("API unavailable")},
		400,
		time.Hour,
		0,
	)

	err := manager.Refresh(context.Background())
	if err == nil {
		t.Fatal("expected provider error")
	}

	reading := manager.Current()

	if reading.GCO2PerKWh != 400 {
		t.Errorf("expected fallback 400, got %v", reading.GCO2PerKWh)
	}
	if reading.Source != "static" {
		t.Errorf("expected source static, got %q", reading.Source)
	}
}

func TestManagerFallbackOnInvalidValue(t *testing.T) {
	values := []float64{-1, math.NaN(), math.Inf(1)}

	for _, value := range values {
		manager := NewManager(
			&mockProvider{value: value},
			400,
			time.Hour,
			0,
		)

		if err := manager.Refresh(context.Background()); err == nil {
			t.Errorf("expected error for invalid value %v", value)
		}

		reading := manager.Current()
		if reading.Source != "static" || reading.GCO2PerKWh != 400 {
			t.Errorf("expected static fallback, got %+v", reading)
		}
	}
}

func TestManagerNilProvider(t *testing.T) {
	manager := NewManager(nil, 400, time.Hour, 0)

	if err := manager.Refresh(context.Background()); err == nil {
		t.Fatal("expected error for nil provider")
	}

	reading := manager.Current()
	if reading.Source != "static" || reading.GCO2PerKWh != 400 {
		t.Errorf("expected static fallback, got %+v", reading)
	}
}

func TestManagerSourceLabelFromProvider(t *testing.T) {
	tests := []struct {
		name       string
		source     string
		wantSource string
	}{
		{"electricitymaps provider", "electricitymaps", "electricitymaps"},
		{"watttime provider", "watttime", "watttime"},
		{"empty source falls back to live", "", "live"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manager := NewManager(
				&mockProvider{value: 300, source: tt.source},
				400,
				time.Hour,
				0,
			)

			if err := manager.Refresh(context.Background()); err != nil {
				t.Fatalf("Refresh() unexpected error: %v", err)
			}

			reading := manager.Current()
			if reading.Source != tt.wantSource {
				t.Errorf("Source = %q, want %q", reading.Source, tt.wantSource)
			}
		})
	}
}

func TestManagerConcurrentAccess(t *testing.T) {
	manager := NewManager(
		&mockProvider{value: 250, source: "electricitymaps"},
		400,
		time.Hour,
		0,
	)

	var wg sync.WaitGroup

	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			for j := 0; j < 100; j++ {
				_ = manager.Current()
				_ = manager.Refresh(context.Background())
			}
		}()
	}

	wg.Wait()
}

// TestManagerStartDoesNotBlock verifies that Start() returns immediately without
// waiting for the first live API call to complete.
// This prevents container startup from hanging when the carbon API is slow.
func TestManagerStartDoesNotBlock(t *testing.T) {
	// Use a provider that takes 200ms to respond.
	slow := &slowProvider{delay: 200 * time.Millisecond, value: 300, source: "electricitymaps"}

	manager := NewManager(slow, 400, time.Hour, 0)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	started := time.Now()
	go manager.Start(ctx)
	elapsed := time.Since(started)

	// Start() must return in well under 100ms (much less than the provider delay).
	if elapsed > 100*time.Millisecond {
		t.Errorf("Start() blocked for %v, expected near-instant return", elapsed)
	}

	// Immediately after Start(), the cache must still be readable (static fallback).
	reading := manager.Current()
	if reading.GCO2PerKWh != 400 || reading.Source != "static" {
		t.Errorf("expected static fallback immediately after Start(), got %+v", reading)
	}
}

type slowProvider struct {
	delay  time.Duration
	value  float64
	source string
}

func (p *slowProvider) Source() string { return p.source }
func (p *slowProvider) Fetch(ctx context.Context) (float64, error) {
	select {
	case <-time.After(p.delay):
		return p.value, nil
	case <-ctx.Done():
		return 0, ctx.Err()
	}
}
