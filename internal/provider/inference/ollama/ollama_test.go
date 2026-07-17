package ollama

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// sampleMetrics is a representative Ollama /metrics payload.
const sampleMetrics = `# HELP ollama_completion_tokens_total Total completion tokens generated.
# TYPE ollama_completion_tokens_total counter
ollama_completion_tokens_total{model="qwen2.5-coder:32b"} 84213
# HELP ollama_requests_active Number of in-flight requests.
# TYPE ollama_requests_active gauge
ollama_requests_active 2
`

// newProvider wires an OllamaProvider to a test server whose handler serves
// the given /metrics body and /api/ps JSON.
func newProvider(metricsBody, psBody string) (*httptest.Server, *OllamaProvider) {
	mux := http.NewServeMux()
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, metricsBody)
	})
	mux.HandleFunc("/api/ps", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, psBody)
	})
	ts := httptest.NewServer(mux)
	p := &OllamaProvider{
		endpoint:      ts.URL + "/metrics",
		apiBase:       ts.URL,
		outputMetric:  defaultOutputMetric,
		runningMetric: defaultRunningMetric,
		client:        ts.Client(),
	}
	return ts, p
}

func TestName(t *testing.T) {
	p := &OllamaProvider{}
	if p.Name() != "ollama" {
		t.Errorf("got %q, want \"ollama\"", p.Name())
	}
}

func TestOutputTokens(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		want    uint64
		wantErr bool
	}{
		{name: "reads counter value", body: sampleMetrics, want: 84213},
		{name: "large counter", body: "ollama_completion_tokens_total{model=\"llama\"} 9999999\n", want: 9999999},
		{name: "zero value", body: "ollama_completion_tokens_total 0\n", want: 0},
		{name: "metric absent returns error", body: "# TYPE other gauge\nother 1\n", wantErr: true},
		{name: "empty body returns error", body: "", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ts, p := newProvider(tc.body, `{"models":[]}`)
			defer ts.Close()
			got, err := p.OutputTokens(context.Background())
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil (value %d)", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %d, want %d", got, tc.want)
			}
		})
	}
}

func TestRequestsRunning(t *testing.T) {
	ts, p := newProvider(sampleMetrics, `{"models":[]}`)
	defer ts.Close()
	got, err := p.RequestsRunning(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != 2 {
		t.Errorf("got %d, want 2", got)
	}

	// Missing gauge is treated as idle (0), not an error.
	ts2, p2 := newProvider("ollama_completion_tokens_total 1\n", `{"models":[]}`)
	defer ts2.Close()
	got, err = p2.RequestsRunning(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != 0 {
		t.Errorf("missing gauge: got %d, want 0", got)
	}
}

func TestModelName(t *testing.T) {
	tests := []struct {
		name   string
		psBody string
		want   string
	}{
		{name: "loaded model", psBody: `{"models":[{"name":"qwen2.5-coder:32b"}]}`, want: "qwen2.5-coder:32b"},
		{name: "no model loaded", psBody: `{"models":[]}`, want: "unknown"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ts, p := newProvider(sampleMetrics, tc.psBody)
			defer ts.Close()
			got, err := p.ModelName(context.Background())
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestFactoryDefaults(t *testing.T) {
	p, err := build(map[string]string{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.endpoint != defaultMetricsEndpoint {
		t.Errorf("endpoint: got %q, want %q", p.endpoint, defaultMetricsEndpoint)
	}
	if p.outputMetric != defaultOutputMetric {
		t.Errorf("outputMetric: got %q, want %q", p.outputMetric, defaultOutputMetric)
	}

	// Overrides and api_base trailing-slash trimming.
	p, err = build(map[string]string{"api_base": "http://ollama:11434/", "output_tokens_metric": "custom_tokens"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.apiBase != "http://ollama:11434" {
		t.Errorf("api_base trim: got %q, want %q", p.apiBase, "http://ollama:11434")
	}
	if p.outputMetric != "custom_tokens" {
		t.Errorf("outputMetric override: got %q, want %q", p.outputMetric, "custom_tokens")
	}
}
