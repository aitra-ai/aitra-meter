package sqlite

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/aitra-ai/aitra-meter/internal/model"
	"github.com/aitra-ai/aitra-meter/internal/storage"
)

func newTestBackend(t *testing.T) *Backend {
	t.Helper()
	b, err := New(":memory:")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { b.Close() })
	return b
}

func TestWriteAndQueryChargeback(t *testing.T) {
	b := newTestBackend(t)
	ctx := context.Background()
	now := time.Now().UTC()

	records := []model.MeasurementRecord{
		{
			TimestampUnixMs:   now.Add(-time.Hour).UnixMilli(),
			Cluster:           "test",
			Node:              "node-0",
			Namespace:         "prod",
			Workload:          "chat",
			Model:             "llama-3-8b",
			Hardware:          "h100",
			Precision:         "fp16",
			Team:              "platform",
			CostCentre:        "cc-1",
			EnergyJoules:      300.0,
			OutputTokens:      1000,
			JPerToken:         0.30,
			CalibrationTier:   model.TierUncalibrated,
			AttributionMethod: model.AttributionDirect,
			CV:                0.01,
			Stable:            true,
			EnergyProvider:    "nvml",
			InferenceProvider: "vllm",
		},
		{
			TimestampUnixMs:   now.Add(-30 * time.Minute).UnixMilli(),
			Cluster:           "test",
			Node:              "node-0",
			Namespace:         "staging",
			Workload:          "eval",
			Model:             "llama-3-8b",
			Hardware:          "h100",
			Precision:         "fp16",
			Team:              "ml-eng",
			CostCentre:        "cc-2",
			EnergyJoules:      150.0,
			OutputTokens:      500,
			JPerToken:         0.30,
			CalibrationTier:   model.TierUncalibrated,
			AttributionMethod: model.AttributionDirect,
			CV:                0.015,
			Stable:            true,
			EnergyProvider:    "nvml",
			InferenceProvider: "vllm",
		},
	}

	if err := b.WriteBatch(ctx, records); err != nil {
		t.Fatalf("WriteBatch: %v", err)
	}

	charges, err := b.QueryChargeback(ctx, storage.ChargebackQuery{
		Cluster: "test",
		From:    now.Add(-2 * time.Hour),
		To:      now,
		PUE:     1.2,
	})
	if err != nil {
		t.Fatalf("QueryChargeback: %v", err)
	}
	if len(charges) != 2 {
		t.Fatalf("expected 2 namespace charges, got %d", len(charges))
	}

	// prod namespace: 300J raw, 360J PUE-adjusted
	var prod storage.NamespaceCharge
	for _, c := range charges {
		if c.Namespace == "prod" {
			prod = c
		}
	}
	if prod.Namespace == "" {
		t.Fatal("prod namespace not found in charges")
	}
	if prod.EnergyJoulesRaw != 300.0 {
		t.Errorf("prod energy raw: got %.1f, want 300.0", prod.EnergyJoulesRaw)
	}
	if prod.EnergyJoulesPUE != 360.0 {
		t.Errorf("prod energy PUE: got %.1f, want 360.0", prod.EnergyJoulesPUE)
	}
	if prod.OutputTokens != 1000 {
		t.Errorf("prod tokens: got %d, want 1000", prod.OutputTokens)
	}
}

// TestHostEnergyRoundTripAbsentIsNotZero protects the central contract of issue
// #82 at the storage boundary: a nil HostEnergyJoules must persist as SQL NULL
// (not measured), never as 0. A record that did measure host energy persists the
// value.
func TestHostEnergyRoundTrip(t *testing.T) {
	b := newTestBackend(t)
	ctx := context.Background()
	now := time.Now().UTC()

	hostJ := 42.5
	records := []model.MeasurementRecord{
		{TimestampUnixMs: now.UnixMilli(), Cluster: "c", Node: "measured", Namespace: "ns", HostEnergyJoules: &hostJ},
		{TimestampUnixMs: now.UnixMilli(), Cluster: "c", Node: "unmeasured", Namespace: "ns", HostEnergyJoules: nil},
	}
	if err := b.WriteBatch(ctx, records); err != nil {
		t.Fatalf("WriteBatch: %v", err)
	}

	read := func(node string) sql.NullFloat64 {
		var v sql.NullFloat64
		err := b.db.QueryRowContext(ctx,
			`SELECT host_energy_joules FROM aitra_measurements WHERE node = ?`, node).Scan(&v)
		if err != nil {
			t.Fatalf("scan %s: %v", node, err)
		}
		return v
	}

	if got := read("measured"); !got.Valid || got.Float64 != hostJ {
		t.Fatalf("measured node: got %+v, want valid %v", got, hostJ)
	}
	// The one that matters: unmeasured must be NULL, never 0.
	if got := read("unmeasured"); got.Valid {
		t.Fatalf("unmeasured node: got valid=%v value=%v, want SQL NULL (not measured)", got.Valid, got.Float64)
	}
}

func TestWriteBatchEmpty(t *testing.T) {
	b := newTestBackend(t)
	if err := b.WriteBatch(context.Background(), nil); err != nil {
		t.Fatalf("WriteBatch(nil): %v", err)
	}
}

func TestRetentionPurge(t *testing.T) {
	b := newTestBackend(t)
	ctx := context.Background()
	now := time.Now().UTC()

	records := []model.MeasurementRecord{
		{TimestampUnixMs: now.Add(-48 * time.Hour).UnixMilli(), Cluster: "test", Node: "n", Namespace: "old"},
		{TimestampUnixMs: now.Add(-time.Hour).UnixMilli(), Cluster: "test", Node: "n", Namespace: "new"},
	}
	if err := b.WriteBatch(ctx, records); err != nil {
		t.Fatalf("WriteBatch: %v", err)
	}

	deleted, err := b.RetentionPurge(ctx, now.Add(-24*time.Hour))
	if err != nil {
		t.Fatalf("RetentionPurge: %v", err)
	}
	if deleted != 1 {
		t.Errorf("expected 1 deleted, got %d", deleted)
	}

	charges, err := b.QueryChargeback(ctx, storage.ChargebackQuery{
		Cluster: "test",
		From:    now.Add(-72 * time.Hour),
		To:      now,
		PUE:     1.0,
	})
	if err != nil {
		t.Fatalf("QueryChargeback: %v", err)
	}
	if len(charges) != 1 || charges[0].Namespace != "new" {
		t.Errorf("expected only 'new' namespace after purge, got %v", charges)
	}
}
