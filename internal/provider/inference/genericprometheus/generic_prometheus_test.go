package genericprometheus

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

const testMetrics = `# HELP tgi_request_generated_tokens_total Total generated tokens
# TYPE tgi_request_generated_tokens_total counter
tgi_request_generated_tokens_total{model_id="llama-3-8b"} 42000
# HELP tgi_queue_size Current queue size
# TYPE tgi_queue_size gauge
tgi_queue_size 3
`

func newTestProvider(t *testing.T, body string) (*GenericPrometheusProvider, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(body)) //nolint:errcheck
	}))
	t.Cleanup(srv.Close)
	return newTGIProvider(srv.URL), srv
}

// newTGIProvider builds a provider wired to TGI metric names against endpoint.
func newTGIProvider(endpoint string) *GenericPrometheusProvider {
	return &GenericPrometheusProvider{
		endpoint:              endpoint + "/metrics",
		outputTokensMetric:    "tgi_request_generated_tokens_total",
		requestsRunningMetric: "tgi_queue_size",
		modelNameLabel:        "model_id",
		client:                http.DefaultClient,
	}
}

func TestOutputTokens(t *testing.T) {
	p, srv := newTestProvider(t, testMetrics)
	_ = srv
	got, err := p.OutputTokens(context.Background())
	if err != nil {
		t.Fatalf("OutputTokens: %v", err)
	}
	if got != 42000 {
		t.Errorf("got %d, want 42000", got)
	}
}

func TestRequestsRunning(t *testing.T) {
	p, srv := newTestProvider(t, testMetrics)
	_ = srv
	got, err := p.RequestsRunning(context.Background())
	if err != nil {
		t.Fatalf("RequestsRunning: %v", err)
	}
	if got != 3 {
		t.Errorf("got %d, want 3", got)
	}
}

func TestModelName(t *testing.T) {
	p, srv := newTestProvider(t, testMetrics)
	_ = srv
	got, err := p.ModelName(context.Background())
	if err != nil {
		t.Fatalf("ModelName: %v", err)
	}
	if got != "llama-3-8b" {
		t.Errorf("got %q, want %q", got, "llama-3-8b")
	}
}

func TestMissingMetric(t *testing.T) {
	p, srv := newTestProvider(t, "# no metrics here\n")
	_ = srv
	_, err := p.OutputTokens(context.Background())
	if err == nil {
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
