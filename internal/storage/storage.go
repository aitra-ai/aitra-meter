// Package storage defines the Backend interface for persisting all measurement
// records. Every WindowReport that passes the CV gate is written to the storage
// backend — not just records needed for chargeback. This gives a complete audit
// trail and allows historical views to be rebuilt from storage if needed.
//
// Implementations register themselves in their init() function using Register().
// The aggregation service selects a backend by name from configuration.
// SQLite (internal/storage/sqlite) is the default. The memory backend is used
// in tests. Additional backends can be contributed to internal/storage/community/.
package storage

import (
	"context"
	"time"

	"github.com/aitra-ai/aitra-meter/internal/model"
)

// RecordWriter persists measurement windows produced by the aggregation loop.
type RecordWriter interface {
	// Write enqueues a single record. Implementations may buffer writes.
	Write(ctx context.Context, r model.MeasurementRecord) error

	// WriteBatch enqueues a slice of records atomically where possible.
	WriteBatch(ctx context.Context, rs []model.MeasurementRecord) error

	// Close flushes any buffered records and releases resources.
	Close() error

	// Name returns the backend identifier used in logs and metrics.
	Name() string
}

// ChargebackQuery specifies the parameters for a billing-period aggregation.
type ChargebackQuery struct {
	Cluster string
	From    time.Time
	To      time.Time
	PUE     float64 // applied as a multiplier to raw energy
}

// NamespaceCharge is one row in a chargeback report.
type NamespaceCharge struct {
	Namespace         string
	EnergyJoulesRaw   float64 // measured energy, before PUE
	EnergyJoulesPUE   float64 // EnergyJoulesRaw * PUE
	OutputTokens      uint64
	CostUSD           float64 // EnergyJoulesPUE * $/kWh / 3,600,000
	AttributionMethod string  // "direct" or "proportional"
	Team              string
	CostCentre        string
}

// ChargebackQuerier answers billing-period aggregation queries.
type ChargebackQuerier interface {
	QueryChargeback(ctx context.Context, q ChargebackQuery) ([]NamespaceCharge, error)
}

// Backend is the full storage contract. Implementations provide both write
// and query capabilities.
type Backend interface {
	RecordWriter
	ChargebackQuerier
}
