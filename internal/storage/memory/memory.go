// Package memory provides an in-memory storage Backend for use in tests.
// It is never included in production binaries — import it only in test files
// or with a build tag.
//
// Usage in tests:
//
//	import _ "github.com/aitra-ai/aitra-meter/internal/storage/memory"
//	// then:
//	b, _ := storage.New("memory", nil)
package memory

import (
	"context"
	"sync"
	"time"

	"github.com/aitra-ai/aitra-meter/internal/model"
	"github.com/aitra-ai/aitra-meter/internal/storage"
)

func init() {
	storage.Register("memory", func(_ map[string]string) (storage.Backend, error) {
		return &Backend{}, nil
	})
}

// Backend is a thread-safe in-memory storage backend.
// It stores all written records and supports basic chargeback aggregation.
type Backend struct {
	mu      sync.RWMutex
	records []model.MeasurementRecord
}

func (b *Backend) Name() string { return "memory" }

func (b *Backend) Write(_ context.Context, r model.MeasurementRecord) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.records = append(b.records, r)
	return nil
}

func (b *Backend) WriteBatch(_ context.Context, rs []model.MeasurementRecord) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.records = append(b.records, rs...)
	return nil
}

func (b *Backend) Close() error { return nil }

// Records returns a copy of all written records. Used in tests to assert state.
func (b *Backend) Records() []model.MeasurementRecord {
	b.mu.RLock()
	defer b.mu.RUnlock()
	out := make([]model.MeasurementRecord, len(b.records))
	copy(out, b.records)
	return out
}

// Reset clears all records. Useful between sub-tests.
func (b *Backend) Reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.records = b.records[:0]
}

func (b *Backend) QueryChargeback(_ context.Context, q storage.ChargebackQuery) ([]storage.NamespaceCharge, error) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	type acc struct {
		energy float64
		tokens uint64
		method string
		team   string
		cc     string
	}
	agg := map[string]*acc{}

	for _, r := range b.records {
		ts := time.UnixMilli(r.TimestampUnixMs)
		if ts.Before(q.From) || ts.After(q.To) {
			continue
		}
		if q.Cluster != "" && r.Cluster != q.Cluster {
			continue
		}
		a, ok := agg[r.Namespace]
		if !ok {
			agg[r.Namespace] = &acc{method: string(r.AttributionMethod), team: r.Team, cc: r.CostCentre}
			a = agg[r.Namespace]
		}
		a.energy += r.EnergyJoules
		a.tokens += r.OutputTokens
	}

	result := make([]storage.NamespaceCharge, 0, len(agg))
	for ns, a := range agg {
		pueEnergy := a.energy * q.PUE
		result = append(result, storage.NamespaceCharge{
			Namespace:         ns,
			EnergyJoulesRaw:   a.energy,
			EnergyJoulesPUE:   pueEnergy,
			OutputTokens:      a.tokens,
			AttributionMethod: a.method,
			Team:              a.team,
			CostCentre:        a.cc,
		})
	}
	return result, nil
}
