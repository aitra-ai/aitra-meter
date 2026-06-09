// Package sqlite provides an embedded SQLite storage Backend for Aitra Meter.
// It uses modernc.org/sqlite — a pure-Go port with no CGO dependency.
//
// Every measurement record produced by the aggregation loop is persisted here,
// not just records needed for chargeback. This gives a complete audit trail and
// allows any historical view to be rebuilt from storage if needed.
//
// The schema mirrors the MeasurementRecord struct. The chargeback query
// aggregates by namespace over a date range with PUE applied as a multiplier.
package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"time"

	// Pure-Go SQLite driver — no CGO required.
	_ "modernc.org/sqlite"

	"github.com/aitra-ai/aitra-meter/internal/model"
	"github.com/aitra-ai/aitra-meter/internal/storage"
)

func init() {
	storage.Register("sqlite", func(config map[string]string) (storage.Backend, error) {
		path := config["path"]
		if path == "" {
			path = "/data/aitra.db"
		}
		return New(path)
	})
}

const createTableSQL = `
CREATE TABLE IF NOT EXISTS aitra_measurements (
	timestamp_ms       INTEGER NOT NULL,
	cluster            TEXT    NOT NULL,
	node               TEXT    NOT NULL,
	namespace          TEXT    NOT NULL,
	workload           TEXT    NOT NULL,
	model              TEXT    NOT NULL,
	hardware           TEXT    NOT NULL,
	precision          TEXT    NOT NULL,
	team               TEXT    NOT NULL,
	cost_centre        TEXT    NOT NULL,
	energy_joules      REAL    NOT NULL,
	output_tokens      INTEGER NOT NULL,
	j_per_token        REAL    NOT NULL,
	calibration_tier   TEXT    NOT NULL,
	ref_j_per_token    REAL    NOT NULL,
	attribution_method TEXT    NOT NULL,
	cv                 REAL    NOT NULL,
	stable             INTEGER NOT NULL,
	energy_provider    TEXT    NOT NULL,
	inference_provider TEXT    NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_cluster_ns_ts
	ON aitra_measurements (cluster, namespace, timestamp_ms);`

const insertSQL = `
INSERT INTO aitra_measurements (
	timestamp_ms, cluster, node, namespace, workload, model, hardware, precision,
	team, cost_centre, energy_joules, output_tokens, j_per_token,
	calibration_tier, ref_j_per_token, attribution_method, cv, stable,
	energy_provider, inference_provider
) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`

const chargebackSQL = `
SELECT
	namespace,
	SUM(energy_joules)       AS energy_raw,
	SUM(output_tokens)       AS tokens,
	MAX(attribution_method)  AS method,
	MAX(team)                AS team,
	MAX(cost_centre)         AS cc
FROM aitra_measurements
WHERE cluster      = ?
  AND timestamp_ms >= ?
  AND timestamp_ms <= ?
GROUP BY namespace
ORDER BY energy_raw DESC`

// Backend implements storage.Backend using an embedded SQLite database.
type Backend struct {
	db   *sql.DB
	mu   sync.Mutex
	stmt *sql.Stmt
}

// New opens (or creates) a SQLite database at path and applies the DDL.
// Pass ":memory:" for an ephemeral in-process database (tests).
func New(path string) (*Backend, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("sqlite open %s: %w", path, err)
	}
	// Single writer connection — SQLite does not support concurrent writers.
	db.SetMaxOpenConns(1)

	if _, err := db.Exec(createTableSQL); err != nil {
		db.Close()
		return nil, fmt.Errorf("sqlite create table: %w", err)
	}

	stmt, err := db.Prepare(insertSQL)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("sqlite prepare insert: %w", err)
	}

	return &Backend{db: db, stmt: stmt}, nil
}

func (b *Backend) Name() string { return "sqlite" }

// Write persists a single measurement record.
func (b *Backend) Write(ctx context.Context, r model.MeasurementRecord) error {
	return b.WriteBatch(ctx, []model.MeasurementRecord{r})
}

// WriteBatch persists a slice of records inside a single transaction.
func (b *Backend) WriteBatch(ctx context.Context, rs []model.MeasurementRecord) error {
	if len(rs) == 0 {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()

	tx, err := b.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("sqlite begin: %w", err)
	}
	txStmt := tx.Stmt(b.stmt)
	for _, r := range rs {
		stable := 0
		if r.Stable {
			stable = 1
		}
		if _, err := txStmt.ExecContext(ctx,
			r.TimestampUnixMs, r.Cluster, r.Node, r.Namespace, r.Workload,
			r.Model, r.Hardware, r.Precision, r.Team, r.CostCentre,
			r.EnergyJoules, r.OutputTokens, r.JPerToken,
			string(r.CalibrationTier), r.RefJPerToken,
			string(r.AttributionMethod), r.CV, stable,
			r.EnergyProvider, r.InferenceProvider,
		); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("sqlite insert: %w", err)
		}
	}
	return tx.Commit()
}

// QueryChargeback returns per-namespace energy aggregates for the query period.
func (b *Backend) QueryChargeback(ctx context.Context, q storage.ChargebackQuery) ([]storage.NamespaceCharge, error) {
	fromMs := q.From.UnixMilli()
	toMs := q.To.UnixMilli()

	rows, err := b.db.QueryContext(ctx, chargebackSQL, q.Cluster, fromMs, toMs)
	if err != nil {
		return nil, fmt.Errorf("sqlite chargeback query: %w", err)
	}
	defer rows.Close()

	var result []storage.NamespaceCharge
	for rows.Next() {
		var c storage.NamespaceCharge
		if err := rows.Scan(
			&c.Namespace, &c.EnergyJoulesRaw, &c.OutputTokens,
			&c.AttributionMethod, &c.Team, &c.CostCentre,
		); err != nil {
			return nil, fmt.Errorf("sqlite chargeback scan: %w", err)
		}
		c.EnergyJoulesPUE = c.EnergyJoulesRaw * q.PUE
		if c.OutputTokens > 0 {
			// CostUSD derived client-side from electricity cost configured in SiteConfig
		}
		result = append(result, c)
	}
	return result, rows.Err()
}

// Close closes the prepared statement and the database connection.
func (b *Backend) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.stmt != nil {
		_ = b.stmt.Close()
	}
	return b.db.Close()
}

// RetentionPurge deletes records older than the given cutoff.
// Call periodically to bound database size.
func (b *Backend) RetentionPurge(ctx context.Context, before time.Time) (int64, error) {
	res, err := b.db.ExecContext(ctx,
		`DELETE FROM aitra_measurements WHERE timestamp_ms < ?`,
		before.UnixMilli(),
	)
	if err != nil {
		return 0, fmt.Errorf("sqlite retention purge: %w", err)
	}
	return res.RowsAffected()
}
