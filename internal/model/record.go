// Package model defines shared data types used by the aggregation, storage,
// and API layers. It has no external dependencies so any package can import it
// without creating cycles.
package model

// CalibrationTier indicates the quality of the reference baseline available
// for a model × hardware combination (spec §5.2, priority order).
type CalibrationTier string

const (
	TierAitraBenchmark CalibrationTier = "aitra_benchmark"
	TierReference      CalibrationTier = "reference"
	TierSelfCalibrated CalibrationTier = "self_calibrated"
	TierUncalibrated   CalibrationTier = "uncalibrated"
)

// AttributionMethod describes how J/token is attributed to a namespace.
type AttributionMethod string

const (
	AttributionDirect       AttributionMethod = "direct"
	AttributionProportional AttributionMethod = "proportional"
)

// MeasurementRecord is the canonical schema for a single measurement window.
// It is written to the storage backend and used in Prometheus metrics.
type MeasurementRecord struct {
	TimestampUnixMs   int64
	Cluster           string
	Node              string
	Namespace         string
	Workload          string
	Model             string
	Hardware          string
	Precision         string
	Team              string
	CostCentre        string
	EnergyJoules      float64
	OutputTokens      uint64
	JPerToken         float64
	CalibrationTier   CalibrationTier
	RefJPerToken      float64
	AttributionMethod AttributionMethod
	CV                float64
	Stable            bool
	EnergyProvider    string
	InferenceProvider string
}
