package aggregation

import (
	"context"
	"math"
	"sync"
	"testing"

	measurementv1 "github.com/aitra-ai/aitra-meter/api/proto/measurement/v1"
)

// --- stubs ------------------------------------------------------------------

// recordSink collects MeasurementRecords written by the loop.
type recordSink struct {
	mu      sync.Mutex
	records []MeasurementRecord
}

func (s *recordSink) Write(_ context.Context, r MeasurementRecord) error {
	s.mu.Lock()
	s.records = append(s.records, r)
	s.mu.Unlock()
	return nil
}

func (s *recordSink) WriteBatch(ctx context.Context, rs []MeasurementRecord) error {
	for _, r := range rs {
		if err := s.Write(ctx, r); err != nil {
			return err
		}
	}
	return nil
}

func (s *recordSink) Close() error { return nil }
func (s *recordSink) Name() string { return "recordSink" }

func (s *recordSink) last() MeasurementRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.records[len(s.records)-1]
}

func (s *recordSink) len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.records)
}

func (s *recordSink) all() []MeasurementRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]MeasurementRecord, len(s.records))
	copy(out, s.records)
	return out
}

// staticHardware always returns the configured hardware label.
type staticHardware struct{ label string }

func (h *staticHardware) Hardware(_ context.Context, _ string) string { return h.label }

// --- helpers ----------------------------------------------------------------

func newTestLoop(pods map[string]PodMeta, policy PolicyConfig, calEntries map[CalibrationKey]CalibrationEntry) (*Loop, *recordSink) {
	sink := &recordSink{}
	resolver := NewResolver(&stubLookup{pods: pods}, policy)
	cal := NewCalibrationTableFromMap(calEntries)
	hw := &staticHardware{label: "h100"}
	return NewLoop("test-cluster", resolver, cal, hw, sink), sink
}

func baseReport() *measurementv1.WindowReport {
	return &measurementv1.WindowReport{
		WindowId:          "w-001",
		Node:              "node-1",
		ModelName:         "llama-3-8b",
		EnergyJoules:      412.4,
		OutputTokens:      1328,
		PowerWatts:        320.0,
		Stable:            true,
		Cv:                0.01,
		EnergyProvider:    "nvml",
		InferenceProvider: "vllm",
		TimestampUnixMs:   1716000000000,
	}
}

// --- tests ------------------------------------------------------------------

func TestLoopJPerTokenArithmetic(t *testing.T) {
	loop, sink := newTestLoop(
		map[string]PodMeta{"node-1/llama-3-8b": {Namespace: "prod", Workload: "chat", Precision: "fp16"}},
		PolicyConfig{DefaultMethod: AttributionDirect},
		nil,
	)
	w := baseReport()
	ack, err := loop.ReportWindow(context.Background(), w)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ack.Accepted {
		t.Error("Accepted = false, want true")
	}
	if sink.len() != 1 {
		t.Fatalf("expected 1 record, got %d", sink.len())
	}
	r := sink.last()
	wantJPT := 412.4 / 1328.0
	if math.Abs(r.JPerToken-wantJPT) > 1e-9 {
		t.Errorf("JPerToken = %f, want %f", r.JPerToken, wantJPT)
	}
}

func TestLoopZeroTokensRejected(t *testing.T) {
	loop, sink := newTestLoop(nil, PolicyConfig{}, nil)
	w := baseReport()
	w.OutputTokens = 0
	ack, err := loop.ReportWindow(context.Background(), w)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ack.Accepted {
		t.Error("Accepted = true for zero-token window, want false")
	}
	if sink.len() != 0 {
		t.Errorf("expected 0 records for rejected window, got %d", sink.len())
	}
}

func TestLoopAttributionMethod(t *testing.T) {
	tests := []struct {
		name   string
		ns     string
		policy PolicyConfig
		wantM  AttributionMethod
	}{
		{
			name:   "direct",
			ns:     "prod",
			policy: PolicyConfig{DefaultMethod: AttributionDirect},
			wantM:  AttributionDirect,
		},
		{
			name: "proportional override",
			ns:   "shared",
			policy: PolicyConfig{
				DefaultMethod: AttributionDirect,
				NamespaceOverrides: map[string]AttributionMethod{
					"shared": AttributionProportional,
				},
			},
			wantM: AttributionProportional,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			loop, sink := newTestLoop(
				map[string]PodMeta{"node-1/llama-3-8b": {Namespace: tc.ns, Workload: "chat", Precision: "fp16"}},
				tc.policy,
				nil,
			)
			_, _ = loop.ReportWindow(context.Background(), baseReport())
			if got := sink.last().AttributionMethod; got != tc.wantM {
				t.Errorf("AttributionMethod = %q, want %q", got, tc.wantM)
			}
		})
	}
}

func TestLoopCalibrationTier(t *testing.T) {
	loop, sink := newTestLoop(
		map[string]PodMeta{"node-1/llama-3-8b": {Namespace: "prod"}},
		PolicyConfig{},
		map[CalibrationKey]CalibrationEntry{
			{Model: "llama-3-8b", Hardware: "h100"}: {
				Tier:         TierAitraBenchmark,
				RefJPerToken: 0.31,
			},
		},
	)
	_, _ = loop.ReportWindow(context.Background(), baseReport())
	r := sink.last()
	if r.CalibrationTier != TierAitraBenchmark {
		t.Errorf("CalibrationTier = %q, want aitra_benchmark", r.CalibrationTier)
	}
	if r.RefJPerToken != 0.31 {
		t.Errorf("RefJPerToken = %f, want 0.31", r.RefJPerToken)
	}
}

func TestLoopUncalibratedModel(t *testing.T) {
	loop, sink := newTestLoop(
		map[string]PodMeta{"node-1/llama-3-8b": {Namespace: "prod"}},
		PolicyConfig{},
		nil, // no calibration data
	)
	_, _ = loop.ReportWindow(context.Background(), baseReport())
	if got := sink.last().CalibrationTier; got != TierUncalibrated {
		t.Errorf("CalibrationTier = %q, want uncalibrated", got)
	}
}

func TestLoopCVAccumulates(t *testing.T) {
	loop, sink := newTestLoop(
		map[string]PodMeta{"node-1/llama-3-8b": {Namespace: "prod"}},
		PolicyConfig{},
		nil,
	)
	// Send 5 windows with identical J/token — CV must be 0 (stable).
	for i := 0; i < 5; i++ {
		w := baseReport()
		w.WindowId = "w-" + string(rune('0'+i))
		_, _ = loop.ReportWindow(context.Background(), w)
	}
	r := sink.last()
	if r.CV != 0 {
		t.Errorf("CV = %f for constant J/token series, want 0", r.CV)
	}
	if !r.Stable {
		t.Error("Stable = false for zero-variance series, want true")
	}
}

func TestLoopRecordFields(t *testing.T) {
	loop, sink := newTestLoop(
		map[string]PodMeta{"node-1/llama-3-8b": {
			Namespace:  "prod",
			Workload:   "chat",
			Precision:  "fp16",
			Team:       "platform",
			CostCentre: "cc-1102",
		}},
		PolicyConfig{DefaultMethod: AttributionDirect},
		nil,
	)
	w := baseReport()
	_, _ = loop.ReportWindow(context.Background(), w)
	r := sink.last()

	checks := []struct {
		field string
		got   string
		want  string
	}{
		{"Cluster", r.Cluster, "test-cluster"},
		{"Node", r.Node, "node-1"},
		{"Namespace", r.Namespace, "prod"},
		{"Workload", r.Workload, "chat"},
		{"Model", r.Model, "llama-3-8b"},
		{"Hardware", r.Hardware, "h100"},
		{"Precision", r.Precision, "fp16"},
		{"Team", r.Team, "platform"},
		{"CostCentre", r.CostCentre, "cc-1102"},
		{"EnergyProvider", r.EnergyProvider, "nvml"},
		{"InferenceProvider", r.InferenceProvider, "vllm"},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %q, want %q", c.field, c.got, c.want)
		}
	}
	if r.EnergyJoules != 412.4 {
		t.Errorf("EnergyJoules = %f, want 412.4", r.EnergyJoules)
	}
	if r.OutputTokens != 1328 {
		t.Errorf("OutputTokens = %d, want 1328", r.OutputTokens)
	}
	if r.TimestampUnixMs != 1716000000000 {
		t.Errorf("TimestampUnixMs = %d, want 1716000000000", r.TimestampUnixMs)
	}
}

func TestLoopTimestampFallback(t *testing.T) {
	loop, sink := newTestLoop(
		map[string]PodMeta{"node-1/llama-3-8b": {Namespace: "prod"}},
		PolicyConfig{},
		nil,
	)
	w := baseReport()
	w.TimestampUnixMs = 0 // trigger fallback
	_, _ = loop.ReportWindow(context.Background(), w)
	if sink.last().TimestampUnixMs == 0 {
		t.Error("TimestampUnixMs = 0 with zero-value input — fallback to time.Now() not applied")
	}
}

func TestLoopConcurrentWindows(t *testing.T) {
	// Fire 50 concurrent reports; no panic, no data race (run with -race).
	loop, sink := newTestLoop(
		map[string]PodMeta{"node-1/llama-3-8b": {Namespace: "prod"}},
		PolicyConfig{},
		nil,
	)
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = loop.ReportWindow(context.Background(), baseReport())
		}()
	}
	wg.Wait()
	if sink.len() != 50 {
		t.Errorf("expected 50 records from concurrent writes, got %d", sink.len())
	}
}

// Cluster J/token must be computed as Σenergy ÷ Σtokens, not as the
// average of per-window ratios. These two formulas diverge whenever window
// token counts differ, so we inject two windows with unequal token counts and
// verify that the stored energy and token values allow the correct aggregate
// to be reconstructed — and that no record pre-computes an incorrect average.
func TestLoopClusterJPerTokenIsSumOfEnergyDividedBySumOfTokens(t *testing.T) {
	loop, sink := newTestLoop(
		map[string]PodMeta{"node-1/llama-3-8b": {Namespace: "prod"}},
		PolicyConfig{},
		nil,
	)

	type win struct {
		joules float64
		tokens uint64
	}
	windows := []win{
		{joules: 100, tokens: 200}, // per-window JPT = 0.5
		{joules: 200, tokens: 100}, // per-window JPT = 2.0
	}
	for _, ww := range windows {
		w := baseReport()
		w.EnergyJoules = ww.joules
		w.OutputTokens = ww.tokens
		if _, err := loop.ReportWindow(context.Background(), w); err != nil {
			t.Fatalf("ReportWindow: %v", err)
		}
	}

	records := sink.all()
	if len(records) != 2 {
		t.Fatalf("expected 2 records, got %d", len(records))
	}

	// Verify each record stores raw energy and tokens, not a pre-aggregated value.
	for _, r := range records {
		wantJPT := r.EnergyJoules / float64(r.OutputTokens)
		if math.Abs(r.JPerToken-wantJPT) > 1e-9 {
			t.Errorf("record JPerToken = %f, want energy/tokens = %f", r.JPerToken, wantJPT)
		}
	}

	// Cluster aggregate = Σenergy / Σtokens = 300/300 = 1.0.
	var sumEnergy float64
	var sumTokens uint64
	for _, r := range records {
		sumEnergy += r.EnergyJoules
		sumTokens += r.OutputTokens
	}
	wantCluster := sumEnergy / float64(sumTokens) // 1.0
	if math.Abs(wantCluster-1.0) > 1e-9 {
		t.Errorf("Σenergy/Σtokens = %f, want 1.0", wantCluster)
	}

	// Average of per-window ratios = (0.5 + 2.0) / 2 = 1.25 ≠ 1.0.
	// Confirm the two formulas actually differ so the test is non-trivial.
	avgOfRatios := (100.0/200.0 + 200.0/100.0) / 2
	if math.Abs(wantCluster-avgOfRatios) < 1e-9 {
		t.Fatal("test setup error: Σenergy/Σtokens and avg-of-ratios must differ")
	}
}

// Every storage record must have attribution_method set to a known
// value ("direct" or "proportional") — never empty. This is true regardless
// of whether pod lookup succeeds or falls back to "unknown" namespace.
func TestLoopAttributionMethodNeverEmpty(t *testing.T) {
	validMethods := map[AttributionMethod]bool{
		AttributionDirect:       true,
		AttributionProportional: true,
	}

	tests := []struct {
		name   string
		pods   map[string]PodMeta
		policy PolicyConfig
	}{
		{
			name:   "pod found direct",
			pods:   map[string]PodMeta{"node-1/llama-3-8b": {Namespace: "prod"}},
			policy: PolicyConfig{DefaultMethod: AttributionDirect},
		},
		{
			name:   "pod found proportional",
			pods:   map[string]PodMeta{"node-1/llama-3-8b": {Namespace: "shared"}},
			policy: PolicyConfig{DefaultMethod: AttributionProportional},
		},
		{
			name:   "pod not found fallback",
			pods:   nil, // lookup will fail → namespace=unknown, method=DefaultMethod
			policy: PolicyConfig{DefaultMethod: AttributionDirect},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			loop, sink := newTestLoop(tc.pods, tc.policy, nil)
			if _, err := loop.ReportWindow(context.Background(), baseReport()); err != nil {
				t.Fatalf("ReportWindow: %v", err)
			}
			r := sink.last()
			if r.AttributionMethod == "" {
				t.Error("AttributionMethod is empty, want direct or proportional")
			}
			if !validMethods[r.AttributionMethod] {
				t.Errorf("AttributionMethod = %q, want direct or proportional", r.AttributionMethod)
			}
		})
	}
}
