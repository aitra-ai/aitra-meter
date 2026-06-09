// Package duckdb provides a DuckDB storage Backend for Aitra Meter.
// DuckDB runs embedded inside the aggregation service — no separate server
// required. Suitable for single-site deployments and development.
//
// Build tag: this package uses CGO via the go-duckdb driver.
// It is excluded from builds where CGO is unavailable.
package duckdb

import (
	"context"
	"fmt"

	"github.com/aitra-ai/aitra-meter/internal/model"
	"github.com/aitra-ai/aitra-meter/internal/storage"
)

func init() {
	storage.Register("duckdb", func(config map[string]string) (storage.Backend, error) {
		path := config["path"]
		if path == "" {
			path = "/data/aitra.duckdb"
		}
		return New(path)
	})
}

// Backend implements storage.Backend using an embedded DuckDB instance.
type Backend struct {
	path string
	// db *sql.DB  — uncomment when go-duckdb is added to go.mod
}

// New creates a DuckDB backend at the given file path.
// Pass ":memory:" for an in-process ephemeral database.
func New(path string) (*Backend, error) {
	// TODO: open go-duckdb connection, apply DDL
	// import _ "github.com/marcboeker/go-duckdb"
	// db, err := sql.Open("duckdb", path)
	return &Backend{path: path}, nil
}

func (b *Backend) Name() string { return "duckdb" }

func (b *Backend) Write(ctx context.Context, r model.MeasurementRecord) error {
	return b.WriteBatch(ctx, []model.MeasurementRecord{r})
}

func (b *Backend) WriteBatch(_ context.Context, rs []model.MeasurementRecord) error {
	// TODO: INSERT INTO aitra_measurements using prepared statement
	return fmt.Errorf("duckdb: not yet implemented — %d records dropped", len(rs))
}

func (b *Backend) Close() error {
	// TODO: close db connection
	return nil
}

func (b *Backend) QueryChargeback(_ context.Context, q storage.ChargebackQuery) ([]storage.NamespaceCharge, error) {
	// TODO: SELECT namespace, sum(energy_joules), sum(output_tokens)
	// FROM aitra_measurements WHERE timestamp BETWEEN ? AND ? GROUP BY namespace
	return nil, fmt.Errorf("duckdb: QueryChargeback not yet implemented")
}
