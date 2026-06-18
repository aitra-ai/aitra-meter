package aggregation

import (
	"context"
	"sync"
	"time"

	"github.com/aitra-ai/aitra-meter/internal/export/otlp"

	measurementv1 "github.com/aitra-ai/aitra-meter/api/proto/measurement/v1"
	"github.com/aitra-ai/aitra-meter/internal/metrics"
	"github.com/aitra-ai/aitra-meter/internal/model"
	"github.com/aitra-ai/aitra-meter/internal/storage"
)

// MeasurementRecord is a type alias for model.MeasurementRecord.
// All code in this package (including tests) may use the unqualified name.
type MeasurementRecord = model.MeasurementRecord

// NodeHardware resolves the GPU tier label for a Kubernetes node.
// The real implementation reads the node label "gpu" via client-go;
// tests use a stub.
type NodeHardware interface {
	Hardware(ctx context.Context, node string) string
}

// Loop implements measurementv1.MeasurementServiceServer.
// It is the central computation hub: for each WindowReport it resolves
// attribution, computes J/token, updates the CV tracker, writes Prometheus
// metrics, and enqueues a storage record.
//
// Loop is safe for concurrent use.
type Loop struct {
	measurementv1.UnimplementedMeasurementServiceServer

	cluster     string
	resolver    *Resolver
	calibration *CalibrationTable
	hardware    NodeHardware
	writer      storage.RecordWriter

	mu      sync.Mutex
	cvByKey map[string]*CVTracker // key: node+"\x00"+modelName

	// OTLPExporter is optional. When non-nil, each window is also emitted
	// via OTLP to an OpenTelemetry Collector.
	OTLPExporter *otlp.Exporter
}

// NewLoop creates a Loop. All arguments must be non-nil.
func NewLoop(
	cluster string,
	resolver *Resolver,
	cal *CalibrationTable,
	hw NodeHardware,
	writer storage.RecordWriter,
) *Loop {
	return &Loop{
		cluster:     cluster,
		resolver:    resolver,
		calibration: cal,
		hardware:    hw,
		writer:      writer,
		cvByKey:     make(map[string]*CVTracker),
	}
}

// ReportWindow implements measurementv1.MeasurementServiceServer.
// It accepts a window report, computes J/token, and writes metrics + record.
// Windows with zero output tokens are rejected (accepted=false); all others
// are accepted even when flagged unstable — the unstable flag is recorded
// in the storage record and in the Prometheus CV gauge.
func (l *Loop) ReportWindow(
	ctx context.Context,
	w *measurementv1.WindowReport,
) (*measurementv1.WindowAck, error) {
	if w.OutputTokens == 0 {
		return &measurementv1.WindowAck{Accepted: false}, nil
	}

	// --- attribution -------------------------------------------------------
	attr := l.resolver.Resolve(ctx, w.Node, w.ModelName)
	hw := l.hardware.Hardware(ctx, w.Node)

	// --- J/token + calibration ---------------------------------------------
	jpt := w.EnergyJoules / float64(w.OutputTokens)
	cal := l.calibration.Lookup(w.ModelName, hw)

	// --- CV (per node × model) ---------------------------------------------
	key := w.Node + "\x00" + w.ModelName
	l.mu.Lock()
	cv, ok := l.cvByKey[key]
	if !ok {
		cv = NewCVTracker(DefaultWindowSize)
		l.cvByKey[key] = cv
	}
	cv.Add(jpt)
	cvVal := cv.CV()
	stable := cv.Stable()
	l.mu.Unlock()

	// --- Prometheus metrics ------------------------------------------------
	method := string(attr.Method)
	tier := string(cal.Tier)

	metrics.JPerToken.WithLabelValues(
		attr.Namespace, attr.Workload, w.ModelName, hw,
		attr.Precision, tier, method,
	).Set(jpt)

	if jpt > 0 {
		metrics.TokensPerJoule.WithLabelValues(
			attr.Namespace, attr.Workload, w.ModelName, hw,
		).Set(1.0 / jpt)
	}
	if w.PowerWatts > 0 && w.OutputTokens > 0 {
		// tokens/sec per watt: token delta / window duration / power
		// WindowDuration is not in WindowReport; use a 30s default approximation.
		// Exact value set when window duration is added to the proto.
		const windowSecs = 30.0
		tokensPerSecPerWatt := (float64(w.OutputTokens) / windowSecs) / w.PowerWatts
		metrics.GPUUtilizationEfficiency.WithLabelValues(
			attr.Namespace, attr.Workload, w.ModelName, hw,
		).Set(tokensPerSecPerWatt)
	}

	metrics.NamespaceEnergyJoulesTotal.WithLabelValues(attr.Namespace, method).
		Add(w.EnergyJoules)
	metrics.NamespaceTokensTotal.WithLabelValues(attr.Namespace).
		Add(float64(w.OutputTokens))

	// Model-level efficiency primitives (issue #40). The cost/tenant/serving
	// metrics in the same family are populated by the SiteConfig-cost and
	// idle-tracking follow-up.
	metrics.ModelTokensTotal.WithLabelValues(attr.Namespace, w.ModelName, hw, attr.Workload).
		Add(float64(w.OutputTokens))
	metrics.ModelEnergyPer1MTokens.WithLabelValues(attr.Namespace, w.ModelName, hw, attr.Workload).
		Set(jpt * 1e6)

	metrics.MeasurementCV.WithLabelValues(w.Node, w.ModelName).Set(cvVal)
	stableF := 0.0
	if stable {
		stableF = 1.0
	}
	metrics.MeasurementWindowStable.WithLabelValues(w.Node, w.ModelName).Set(stableF)

	if cal.RefJPerToken > 0 {
		metrics.CalibrationReferenceJPerToken.WithLabelValues(w.ModelName, hw, tier).
			Set(cal.RefJPerToken)
	}

	metrics.GPUPowerWatts.WithLabelValues(w.Node, "all").Set(w.PowerWatts)

	// --- storage record -------------------------------------------------
	ts := w.TimestampUnixMs
	if ts == 0 {
		ts = time.Now().UnixMilli()
	}
	rec := MeasurementRecord{
		TimestampUnixMs:   ts,
		Cluster:           l.cluster,
		Node:              w.Node,
		Namespace:         attr.Namespace,
		Workload:          attr.Workload,
		Model:             w.ModelName,
		Hardware:          hw,
		Precision:         attr.Precision,
		Team:              attr.Team,
		CostCentre:        attr.CostCentre,
		EnergyJoules:      w.EnergyJoules,
		OutputTokens:      w.OutputTokens,
		JPerToken:         jpt,
		CalibrationTier:   cal.Tier,
		RefJPerToken:      cal.RefJPerToken,
		AttributionMethod: attr.Method,
		CV:                cvVal,
		Stable:            stable,
		EnergyProvider:    w.EnergyProvider,
		InferenceProvider: w.InferenceProvider,
	}
	_ = l.writer.Write(ctx, rec) // async writers never block; errors are logged by the writer

	// --- OTLP export (optional) -------------------------------------------
	if l.OTLPExporter != nil {
		idleRatioVal := 0.0
		if w.OutputTokens == 0 {
			idleRatioVal = 1.0
		}
		l.OTLPExporter.RecordWindow(ctx, otlp.WindowAttrs{
			Model:             w.ModelName,
			InferenceProvider: w.InferenceProvider,
			Node:              w.Node,
			Namespace:         attr.Namespace,
		}, jpt, w.EnergyJoules, w.PowerWatts, idleRatioVal)
	}

	return &measurementv1.WindowAck{Accepted: true}, nil
}
