//go:build ignore

// seed.go inserts 30 days of synthetic measurement records into the local
// SQLite database used by the dev stack. Run with:
//
//	go run ./dev/seed.go [--path /data/aitra.db]
//
// The seed generates data for 3 namespaces x 2 models x 2 hardware tiers,
// at one record per 5 minutes, giving ~8,640 rows per combination and ~103k
// rows total — enough to exercise the 30-day chargeback query.
package main

import (
	"database/sql"
	"flag"
	"fmt"
	"log"
	"math/rand"
	"time"

	_ "modernc.org/sqlite"
)

var dbPath = flag.String("path", "/data/aitra.db", "SQLite database path")

const createSQL = `
CREATE TABLE IF NOT EXISTS aitra_measurements (
	timestamp          INTEGER NOT NULL,
	cluster            TEXT NOT NULL,
	node               TEXT NOT NULL,
	namespace          TEXT NOT NULL,
	workload           TEXT NOT NULL,
	model              TEXT NOT NULL,
	hardware           TEXT NOT NULL,
	precision          TEXT NOT NULL,
	team               TEXT NOT NULL,
	cost_centre        TEXT NOT NULL,
	energy_joules      REAL NOT NULL,
	output_tokens      INTEGER NOT NULL,
	j_per_token        REAL NOT NULL,
	calibration_tier   TEXT NOT NULL,
	ref_j_per_token    REAL NOT NULL,
	attribution_method TEXT NOT NULL,
	cv                 REAL NOT NULL,
	stable             INTEGER NOT NULL,
	energy_provider    TEXT NOT NULL,
	inference_provider TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_cluster_ns_ts
	ON aitra_measurements (cluster, namespace, timestamp);`

type combo struct {
	namespace  string
	workload   string
	model      string
	hardware   string
	precision  string
	team       string
	costCentre string
	baseJPT    float64
	tier       string
	refJPT     float64
	method     string
}

var combos = []combo{
	{
		namespace: "prod-nlp", workload: "chat", model: "llama-3-8b", hardware: "h100",
		precision: "fp16", team: "platform", costCentre: "cc-1100",
		baseJPT: 0.31, tier: "aitra_benchmark", refJPT: 0.31, method: "direct",
	},
	{
		namespace: "prod-nlp", workload: "chat", model: "llama-3-70b", hardware: "h100",
		precision: "fp16", team: "platform", costCentre: "cc-1100",
		baseJPT: 2.80, tier: "aitra_benchmark", refJPT: 2.80, method: "direct",
	},
	{
		namespace: "staging", workload: "eval", model: "llama-3-8b", hardware: "a100",
		precision: "bf16", team: "ml-eng", costCentre: "cc-2200",
		baseJPT: 0.44, tier: "reference", refJPT: 0.31, method: "direct",
	},
	{
		namespace: "staging", workload: "eval", model: "llama-3-70b", hardware: "a100",
		precision: "bf16", team: "ml-eng", costCentre: "cc-2200",
		baseJPT: 3.90, tier: "reference", refJPT: 2.80, method: "direct",
	},
	{
		namespace: "shared-inference", workload: "unknown", model: "llama-3-8b", hardware: "h100",
		precision: "fp16", team: "infra", costCentre: "cc-3300",
		baseJPT: 0.35, tier: "self_calibrated", refJPT: 0.0, method: "proportional",
	},
	{
		namespace: "shared-inference", workload: "unknown", model: "llama-3-70b", hardware: "h100",
		precision: "fp16", team: "infra", costCentre: "cc-3300",
		baseJPT: 3.10, tier: "self_calibrated", refJPT: 0.0, method: "proportional",
	},
}

func main() {
	flag.Parse()

	db, err := sql.Open("sqlite", *dbPath)
	if err != nil {
		log.Fatalf("open: %v", err)
	}
	defer db.Close()

	if _, err := db.Exec(createSQL); err != nil {
		log.Fatalf("create table: %v", err)
	}

	step := 5 * time.Minute
	end := time.Now().UTC().Truncate(step)
	start := end.Add(-30 * 24 * time.Hour)

	totalRows := 0
	rng := rand.New(rand.NewSource(42))

	for _, c := range combos {
		tx, err := db.Begin()
		if err != nil {
			log.Fatalf("begin: %v", err)
		}
		stmt, err := tx.Prepare(`INSERT INTO aitra_measurements VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`)
		if err != nil {
			log.Fatalf("prepare: %v", err)
		}

		for ts := start; ts.Before(end); ts = ts.Add(step) {
			noise := 1.0 + (rng.Float64()-0.5)*0.10
			jpt := c.baseJPT * noise
			tokens := int64(800 + rng.Intn(400))
			joules := jpt * float64(tokens)
			cv := rng.Float64() * 0.025
			stable := 1
			if cv >= 0.03 {
				stable = 0
			}
			if _, err := stmt.Exec(
				ts.UnixMilli(), "dev", "node-dev-0",
				c.namespace, c.workload, c.model, c.hardware, c.precision,
				c.team, c.costCentre,
				joules, tokens, jpt,
				c.tier, c.refJPT, c.method,
				cv, stable,
				"nvml", "vllm",
			); err != nil {
				log.Fatalf("insert: %v", err)
			}
			totalRows++
		}
		stmt.Close()
		if err := tx.Commit(); err != nil {
			log.Fatalf("commit: %v", err)
		}
		fmt.Printf("  %-20s %-14s seeded\n", c.namespace, c.model)
	}

	fmt.Printf("\nInserted %d rows across %d combinations.\n", totalRows, len(combos))
}
