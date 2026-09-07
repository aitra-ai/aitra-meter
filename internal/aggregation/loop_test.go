package aggregation

import (
	"context"
	"math"
	"sync"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"

	measurementv1 "github.com/aitra-ai/aitra-meter/api/proto/measurement/v1"
	"github.com/aitra-ai/aitra-meter/internal/metrics"
	"github.com/aitra-ai/aitra-meter/internal/model"
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
	return newTestLoopWithSite(pods, policy, calEntries, SiteParams{})
}

func newTestLoopWithSite(pods map[string]PodMeta, policy PolicyConfig, calEntries map[CalibrationKey]CalibrationEntry, site SiteParams) (*Loop, *recordSink) {
	sink := &recordSink{}
	resolver := NewResolver(&stubLookup{pods: pods}, policy)
	cal := NewCalibrationTableFromMap(calEntries)
	hw := &staticHardware{label: "h100"}
	return NewLoop("test-cluster", resolver, cal, hw, sink, site), sink
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

// TestLoopModelTokensTotalAccumulates checks the model-level token counter is
// monotonic across windows. A unique model label isolates this counter child
// from other tests (Prometheus counters are process-global and never reset).
func TestLoopModelTokensTotalAccumulates(t *testing.T) {
	const model = "tokens-accum-model"
	loop, _ := newTestLoop(
		map[string]PodMeta{"node-1/" + model: {Namespace: "prod", Workload: "chat"}},
		PolicyConfig{DefaultMethod: AttributionDirect},
		nil,
	)
	child := metrics.ModelTokensTotal.WithLabelValues("prod", model, "h100", "chat")
	before := testutil.ToFloat64(child)
	for i := 0; i < 3; i++ {
		w := baseReport()
		w.ModelName = model
		if _, err := loop.ReportWindow(context.Background(), w); err != nil {
			t.Fatalf("ReportWindow: %v", err)
		}
	}
	if got, want := testutil.ToFloat64(child)-before, 3.0*1328.0; got != want {
		t.Errorf("model_tokens_total delta = %v, want %v", got, want)
	}
}

// TestLoopModelEnergyPer1MTokens checks the J/1M-tokens gauge reflects the
// current window's J/token scaled to one million tokens.
func TestLoopModelEnergyPer1MTokens(t *testing.T) {
	const model = "energy1m-model"
	loop, _ := newTestLoop(
		map[string]PodMeta{"node-1/" + model: {Namespace: "prod", Workload: "chat"}},
		PolicyConfig{DefaultMethod: AttributionDirect},
		nil,
	)
	w := baseReport()
	w.ModelName = model
	if _, err := loop.ReportWindow(context.Background(), w); err != nil {
		t.Fatalf("ReportWindow: %v", err)
	}
	got := testutil.ToFloat64(metrics.ModelEnergyPer1MTokens.WithLabelValues("prod", model, "h100", "chat"))
	want := (412.4 / 1328.0) * 1e6
	if math.Abs(got-want) > 1e-3 {
		t.Errorf("model_energy_per_1m_tokens = %v, want %v", got, want)
	}
}

// TestLoopCostCarbonDerivation checks the cost + carbon metrics derive from
// J/token and the configured site factors (issue #40 PR-2).
func TestLoopCostCarbonDerivation(t *testing.T) {
	const model = "cost-model"
	site := SiteParams{ElectricityCostPerKWh: 0.12, GridGCO2PerKWh: 400}
	loop, _ := newTestLoopWithSite(
		map[string]PodMeta{"node-1/" + model: {Namespace: "prod", Workload: "chat", Team: "platform", CostCentre: "cc-1"}},
		PolicyConfig{DefaultMethod: AttributionDirect}, nil, site,
	)
	tenant := metrics.TenantCostUSDTotal.WithLabelValues("prod", "platform", "cc-1")
	before := testutil.ToFloat64(tenant)
	w := baseReport()
	w.ModelName = model
	if _, err := loop.ReportWindow(context.Background(), w); err != nil {
		t.Fatalf("ReportWindow: %v", err)
	}

	jpt := 412.4 / 1328.0
	wantCostPerM := jpt / 3_600_000.0 * 0.12 * 1e6
	if got := testutil.ToFloat64(metrics.CostPerMillionTokensUSD.WithLabelValues("prod", "chat", model, "h100", "manual")); math.Abs(got-wantCostPerM) > 1e-9 {
		t.Errorf("cost_per_million = %v, want %v", got, wantCostPerM)
	}
	if got := testutil.ToFloat64(metrics.ModelCostPer1MTokensUSD.WithLabelValues("prod", model, "h100", "chat")); math.Abs(got-wantCostPerM) > 1e-9 {
		t.Errorf("model_cost_per_1m = %v, want %v", got, wantCostPerM)
	}
	wantCO2 := jpt / 3_600_000.0 * 400
	if got := testutil.ToFloat64(metrics.CO2PerTokenGrams.WithLabelValues("prod", "chat", model, "h100", "manual")); math.Abs(got-wantCO2) > 1e-12 {
		t.Errorf("co2_per_token = %v, want %v", got, wantCO2)
	}
	wantTenantDelta := 412.4 / 3_600_000.0 * 0.12
	if got := testutil.ToFloat64(tenant) - before; math.Abs(got-wantTenantDelta) > 1e-12 {
		t.Errorf("tenant_cost delta = %v, want %v", got, wantTenantDelta)
	}
}

// TestLoopCostDisabledWhenUnset checks cost metrics stay unset without site config.
func TestLoopCostDisabledWhenUnset(t *testing.T) {
	const model = "no-cost-model"
	loop, _ := newTestLoop(
		map[string]PodMeta{"node-1/" + model: {Namespace: "prod", Workload: "chat"}},
		PolicyConfig{DefaultMethod: AttributionDirect}, nil,
	)
	w := baseReport()
	w.ModelName = model
	if _, err := loop.ReportWindow(context.Background(), w); err != nil {
		t.Fatalf("ReportWindow: %v", err)
	}
	if got := testutil.ToFloat64(metrics.ModelCostPer1MTokensUSD.WithLabelValues("prod", model, "h100", "chat")); got != 0 {
		t.Errorf("model_cost set to %v with no site config, want 0 (unset)", got)
	}
}

// TestLoopIdleWindowTracking checks idle (zero-token) windows are not aggregated
// but do drive idle power and the serving/idle ratios (issue #40 PR-2).
func TestLoopIdleWindowTracking(t *testing.T) {
	const node = "idle-node"
	loop, sink := newTestLoop(map[string]PodMeta{}, PolicyConfig{}, nil)
	w := baseReport()
	w.Node = node
	w.ModelName = "" // whole-node idle: no model holds the GPUs
	w.OutputTokens = 0
	w.PowerWatts = 95.0
	ack, err := loop.ReportWindow(context.Background(), w)
	if err != nil {
		t.Fatalf("ReportWindow: %v", err)
	}
	if ack.Accepted {
		t.Error("idle window Accepted = true, want false")
	}
	if sink.len() != 0 {
		t.Errorf("idle window wrote %d records, want 0", sink.len())
	}
	if got := testutil.ToFloat64(metrics.IdlePowerWatts.WithLabelValues(node)); got != 95.0 {
		t.Errorf("idle_power_watts = %v, want 95", got)
	}
	if got := testutil.ToFloat64(metrics.GPUServingUtilizationRatio.WithLabelValues(node)); got != 0 {
		t.Errorf("serving_ratio = %v, want 0 after one idle window", got)
	}
	if got := testutil.ToFloat64(metrics.IdleTimeRatio.WithLabelValues(node)); got != 1 {
		t.Errorf("idle_time_ratio = %v, want 1 after one idle window", got)
	}
}

// TestLoopQuietModelWindow checks a zero-token window from a model pod keeps
// its power under the model's own series (the pod holds those GPUs, so this
// is not node idle power) and zeroes the model's efficiency gauges instead of
// freezing them at the last serving value.
func TestLoopQuietModelWindow(t *testing.T) {
	const node = "quiet-node"
	loop, sink := newTestLoop(
		map[string]PodMeta{node + "/llama-3-8b": {Namespace: "prod", Workload: "chat", Precision: "fp16"}},
		PolicyConfig{DefaultMethod: AttributionDirect},
		nil,
	)

	// Serving window first: J/token gets a real value.
	s := baseReport()
	s.Node = node
	if _, err := loop.ReportWindow(context.Background(), s); err != nil {
		t.Fatalf("ReportWindow(serving): %v", err)
	}
	jptLabels := []string{"prod", "chat", "llama-3-8b", "h100", "fp16", "uncalibrated", "direct"}
	if got := testutil.ToFloat64(metrics.JPerToken.WithLabelValues(jptLabels...)); got <= 0 {
		t.Fatalf("precondition: serving J/token = %v, want > 0", got)
	}

	w := baseReport()
	w.Node = node
	w.OutputTokens = 0 // loaded but quiet
	w.PowerWatts = 210.0
	ack, err := loop.ReportWindow(context.Background(), w)
	if err != nil {
		t.Fatalf("ReportWindow: %v", err)
	}
	if ack.Accepted {
		t.Error("quiet window Accepted = true, want false")
	}
	if sink.len() != 1 {
		t.Errorf("store has %d records, want 1 (serving only)", sink.len())
	}
	if got := testutil.ToFloat64(metrics.GPUPowerWatts.WithLabelValues(node, w.ModelName)); got != 210.0 {
		t.Errorf("gpu_power{model} = %v, want 210", got)
	}
	if got := testutil.ToFloat64(metrics.IdlePowerWatts.WithLabelValues(node)); got != 0 {
		t.Errorf("idle_power_watts = %v, want 0 (pod holds the GPUs)", got)
	}
	if got := testutil.ToFloat64(metrics.JPerToken.WithLabelValues(jptLabels...)); got != 0 {
		t.Errorf("quiet J/token = %v, want 0 (no freezing at last value)", got)
	}
	if got := testutil.ToFloat64(metrics.TokensPerJoule.WithLabelValues("prod", "chat", "llama-3-8b", "h100")); got != 0 {
		t.Errorf("quiet tokens/joule = %v, want 0", got)
	}
}

// TestLoopResidualWindow checks a residual report (energy of GPUs no pod
// holds, sent by per-model agents) is recorded as true idle power under the
// "idle" power series, is never stored as a measurement, and does not count
// toward the serving/idle time ratios (it is not a model window).
func TestLoopResidualWindow(t *testing.T) {
	const node = "residual-node"
	loop, sink := newTestLoop(map[string]PodMeta{}, PolicyConfig{}, nil)

	// A serving window first, so the ratios have defined values the residual
	// report must not disturb.
	w := baseReport()
	w.Node = node
	if _, err := loop.ReportWindow(context.Background(), w); err != nil {
		t.Fatalf("ReportWindow(serving): %v", err)
	}
	servingBefore := testutil.ToFloat64(metrics.GPUServingUtilizationRatio.WithLabelValues(node))

	r := baseReport()
	r.Node = node
	r.ModelName = model.ResidualModelName
	r.OutputTokens = 0
	r.PowerWatts = 88.0
	ack, err := loop.ReportWindow(context.Background(), r)
	if err != nil {
		t.Fatalf("ReportWindow(residual): %v", err)
	}
	if ack.Accepted {
		t.Error("residual window Accepted = true, want false")
	}
	if sink.len() != 1 {
		t.Errorf("store has %d records, want 1 (serving only — residual never stored)", sink.len())
	}
	if got := testutil.ToFloat64(metrics.IdlePowerWatts.WithLabelValues(node)); got != 88.0 {
		t.Errorf("idle_power_watts = %v, want 88", got)
	}
	if got := testutil.ToFloat64(metrics.GPUPowerWatts.WithLabelValues(node, "idle")); got != 88.0 {
		t.Errorf(`gpu_power{model="idle"} = %v, want 88`, got)
	}
	if got := testutil.ToFloat64(metrics.GPUServingUtilizationRatio.WithLabelValues(node)); got != servingBefore {
		t.Errorf("serving ratio changed by residual report: %v → %v", servingBefore, got)
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
