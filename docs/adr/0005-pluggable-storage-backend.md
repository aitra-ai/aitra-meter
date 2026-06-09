# ADR 0005: Pluggable storage backend interface

## Status

Accepted

## Context

The initial implementation hard-coded ClickHouse as the only storage backend.
Three problems emerged:

1. **Broken pattern.** EnergyProvider and InferenceMetricsProvider are both behind
   pluggable interfaces (ADR 0004). ClickHouse being a concrete dependency in
   `internal/clickhouse/writer.go` is inconsistent and makes the same mistakes
   the provider pattern was designed to prevent.

2. **Dashboard layer violation.** `dashboard/lib/clickhouse.ts` opened a direct
   connection from the Next.js server to ClickHouse. If the backend changes, the
   dashboard breaks. The dashboard should never know what storage backend is in use.

3. **DuckDB was already planned.** `values.yaml` listed DuckDB as a lightweight
   alternative but there was no interface to support two implementations cleanly.
   Two backends without an interface means conditional logic; an interface makes it
   clean.

## Decision

Introduce `internal/storage` with a `Backend` interface. All storage interactions
in the aggregation service go through this interface. ClickHouse, DuckDB, and an
in-memory implementation register themselves via `init()` exactly as energy and
inference providers do.

The dashboard chargeback route calls the aggregation service HTTP API
(`GET /api/v1/namespaces`) instead of querying ClickHouse directly.

## Interface

```go
// RecordWriter persists measurement windows.
type RecordWriter interface {
    Write(ctx context.Context, r aggregation.MeasurementRecord) error
    WriteBatch(ctx context.Context, rs []aggregation.MeasurementRecord) error
    Close() error
    Name() string
}

// ChargebackQuerier answers billing-period aggregation queries.
type ChargebackQuerier interface {
    QueryChargeback(ctx context.Context, q ChargebackQuery) ([]NamespaceCharge, error)
}

// Backend is the full storage contract. Implementations register with
// Register() in their init() function.
type Backend interface {
    RecordWriter
    ChargebackQuerier
}
```

## Implementations

| Name | Package | When to use |
|---|---|---|
| `clickhouse` | `internal/storage/clickhouse` | Default. Best analytical query performance. |
| `duckdb` | `internal/storage/duckdb` | Embedded, no server. Single-site or dev. |
| `memory` | `internal/storage/memory` | Tests only. Never in production images. |
| `postgres` | `internal/storage/postgres` (community) | Operators already running TimescaleDB. |

## Configuration

```yaml
storage:
  backend: clickhouse   # clickhouse | duckdb | postgres
  config: {}
```

## Migration

`internal/clickhouse/` is renamed to `internal/storage/clickhouse/`. The local
`RecordWriter` interface in `internal/aggregation/loop.go` is replaced with an
import from `internal/storage`. All existing tests continue to pass unchanged
because `loop_test.go` already uses a stub that matches the new interface.

## Consequences

- ClickHouse remains the default and the right choice for AC-11 query performance.
  Nothing about the preference changes — it just stops being the only option.
- The `LowCardinality` types and `MergeTree` ordering stay in the ClickHouse
  implementation. The interface is designed around what Aitra Meter needs, not
  around the lowest common denominator of all databases.
- The dashboard has no direct dependency on any database. This is the correct
  architectural layering.
- An in-memory backend eliminates the need for a ClickHouse container in unit
  tests. `writer_integration_test.go` becomes the only test that needs a real
  ClickHouse instance.
