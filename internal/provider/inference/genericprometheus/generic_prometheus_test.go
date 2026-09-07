package genericprometheus

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TGI exposes one series per metric.
const tgiMetrics = `# HELP tgi_request_generated_tokens_total Total generated tokens
# TYPE tgi_request_generated_tokens_total counter
tgi_request_generated_tokens_total{model_id="llama-3-8b"} 42000
# HELP tgi_queue_size Current queue size
# TYPE tgi_queue_size gauge
tgi_queue_size 3
`

// SGLang labels every metric with the shard identity — model_name, engine_type,
// tp_rank, pp_rank, moe_ep_rank — so a tensor-parallel deployment exposes one
// series per rank. Four ranks reporting 1000 tokens each means the model
// generated 4000.
const sglangTensorParallelMetrics = `# HELP sglang:generation_tokens_total Number of generation tokens processed.
# TYPE sglang:generation_tokens_total counter
sglang:generation_tokens_total{model_name="qwen2.5-32b",engine_type="unified",tp_rank="0",pp_rank="0",moe_ep_rank="0"} 1000
sglang:generation_tokens_total{model_name="qwen2.5-32b",engine_type="unified",tp_rank="1",pp_rank="0",moe_ep_rank="0"} 1000
sglang:generation_tokens_total{model_name="qwen2.5-32b",engine_type="unified",tp_rank="2",pp_rank="0",moe_ep_rank="0"} 1000
sglang:generation_tokens_total{model_name="qwen2.5-32b",engine_type="unified",tp_rank="3",pp_rank="0",moe_ep_rank="0"} 1000
# HELP sglang:num_running_reqs The number of running requests.
# TYPE sglang:num_running_reqs gauge
sglang:num_running_reqs{model_name="qwen2.5-32b",engine_type="unified",tp_rank="0",pp_rank="0",moe_ep_rank="0"} 2
sglang:num_running_reqs{model_name="qwen2.5-32b",engine_type="unified",tp_rank="1",pp_rank="0",moe_ep_rank="0"} 2
sglang:num_running_reqs{model_name="qwen2.5-32b",engine_type="unified",tp_rank="2",pp_rank="0",moe_ep_rank="0"} 2
sglang:num_running_reqs{model_name="qwen2.5-32b",engine_type="unified",tp_rank="3",pp_rank="0",moe_ep_rank="0"} 2
`

func serveMetrics(t *testing.T, body string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(body)) //nolint:errcheck
	}))
	t.Cleanup(srv.Close)
	return srv.URL + "/metrics"
}

// newTGIProvider builds a provider wired to TGI metric names against endpoint.
func newTGIProvider(endpoint string) *GenericPrometheusProvider {
	return &GenericPrometheusProvider{
		endpoint:              endpoint,
		outputTokensMetric:    "tgi_request_generated_tokens_total",
		requestsRunningMetric: "tgi_queue_size",
		modelNameLabel:        "model_id",
		client:                http.DefaultClient,
	}
}

// newSGLangProvider builds a provider wired to SGLang metric names against endpoint.
func newSGLangProvider(endpoint string) *GenericPrometheusProvider {
	return &GenericPrometheusProvider{
		endpoint:              endpoint,
		outputTokensMetric:    "sglang:generation_tokens_total",
		requestsRunningMetric: "sglang:num_running_reqs",
		modelNameLabel:        "model_name",
		client:                http.DefaultClient,
	}
}

func TestOutputTokens(t *testing.T) {
	p := newTGIProvider(serveMetrics(t, tgiMetrics))
	got, err := p.OutputTokens(context.Background())
	if err != nil {
		t.Fatalf("OutputTokens: %v", err)
	}
	if got != 42000 {
		t.Errorf("got %d, want 42000", got)
	}
}

func TestRequestsRunning(t *testing.T) {
	p := newTGIProvider(serveMetrics(t, tgiMetrics))
	got, err := p.RequestsRunning(context.Background())
	if err != nil {
		t.Fatalf("RequestsRunning: %v", err)
	}
	if got != 3 {
		t.Errorf("got %d, want 3", got)
	}
}

func TestModelName(t *testing.T) {
	p := newTGIProvider(serveMetrics(t, tgiMetrics))
	got, err := p.ModelName(context.Background())
	if err != nil {
		t.Fatalf("ModelName: %v", err)
	}
	if got != "llama-3-8b" {
		t.Errorf("got %q, want %q", got, "llama-3-8b")
	}
}

// A sharded model reports its tokens across one series per rank. Keying the
// scrape map by bare metric name kept only the last series and returned 1000,
// a denominator four times too small.
func TestOutputTokensSumsTensorParallelRanks(t *testing.T) {
	p := newSGLangProvider(serveMetrics(t, sglangTensorParallelMetrics))
	got, err := p.OutputTokens(context.Background())
	if err != nil {
		t.Fatalf("OutputTokens: %v", err)
	}
	if got != 4000 {
		t.Errorf("got %d, want 4000 (sum across four tp_rank series)", got)
	}
}

func TestRequestsRunningSumsTensorParallelRanks(t *testing.T) {
	p := newSGLangProvider(serveMetrics(t, sglangTensorParallelMetrics))
	got, err := p.RequestsRunning(context.Background())
	if err != nil {
		t.Fatalf("RequestsRunning: %v", err)
	}
	if got != 8 {
		t.Errorf("got %d, want 8 (sum across four tp_rank series)", got)
	}
}

func TestModelNameAcrossTensorParallelRanks(t *testing.T) {
	p := newSGLangProvider(serveMetrics(t, sglangTensorParallelMetrics))
	got, err := p.ModelName(context.Background())
	if err != nil {
		t.Fatalf("ModelName: %v", err)
	}
	if got != "qwen2.5-32b" {
		t.Errorf("got %q, want %q", got, "qwen2.5-32b")
	}
}

// One endpoint serving two models cannot be attributed to a single model.
func TestModelNameRejectsMultiModelEndpoint(t *testing.T) {
	const body = `sglang:generation_tokens_total{model_name="qwen2.5-7b",tp_rank="0"} 500
sglang:generation_tokens_total{model_name="llama-3-8b",tp_rank="0"} 700
`
	p := newSGLangProvider(serveMetrics(t, body))
	if _, err := p.ModelName(context.Background()); err == nil {
		t.Fatal("expected an error when one endpoint reports two model names")
	}
}

func TestMissingMetric(t *testing.T) {
	p := newTGIProvider(serveMetrics(t, "# no metrics here\n"))
	if _, err := p.OutputTokens(context.Background()); err == nil {
		t.Fatal("expected error for missing metric")
	}
}

func TestOrDefault(t *testing.T) {
	if orDefault("", "default") != "default" {
		t.Error("empty string should return default")
	}
	if orDefault("value", "default") != "value" {
		t.Error("non-empty string should return itself")
	}
}
