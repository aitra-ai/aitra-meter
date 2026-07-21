package sglang

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aitra-ai/aitra-meter/internal/provider"
)

// sampleMetrics is a representative SGLang /metrics payload (v0.5.4+ naming).
const sampleMetrics = `# HELP sglang_generation_tokens_total Number of generation tokens processed.
# TYPE sglang_generation_tokens_total counter
sglang_generation_tokens_total{model_name="Qwen3-27B"} 88200
# HELP sglang_num_running_reqs The number of running requests.
# TYPE sglang_num_running_reqs gauge
sglang_num_running_reqs{model_name="Qwen3-27B"} 5
`

// legacyMetrics uses the pre-v0.5.4 "sglang:" prefix.
const legacyMetrics = `# HELP sglang:generation_tokens_total Number of generation tokens processed.
# TYPE sglang:generation_tokens_total counter
sglang:generation_tokens_total{model_name="old-model"} 4100
sglang:num_running_reqs{model_name="old-model"} 2
`

func serve(body string) (*httptest.Server, *SGLangProvider) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, body)
	}))
	p := &SGLangProvider{endpoint: ts.URL, client: ts.Client()}
	return ts, p
}

func TestImplementsInterface(t *testing.T) {
	var _ provider.InferenceMetricsProvider = &SGLangProvider{}
}

func TestName(t *testing.T) {
	p := &SGLangProvider{}
	if p.Name() != "sglang" {
		t.Errorf("got %q, want \"sglang\"", p.Name())
	}
}

func TestFactoryRegistered(t *testing.T) {
	p, err := provider.NewInference("sglang", nil)
	if err != nil {
		t.Fatalf("factory not registered: %v", err)
	}
	if p.Name() != "sglang" {
		t.Errorf("factory returned provider %q, want \"sglang\"", p.Name())
	}
}

func TestFactoryEndpointConfig(t *testing.T) {
	p, err := provider.NewInference("sglang", map[string]string{"endpoint": "http://10.0.0.1:30000/metrics"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	sp, ok := p.(*SGLangProvider)
	if !ok {
		t.Fatalf("factory returned %T, want *SGLangProvider", p)
	}
	if sp.endpoint != "http://10.0.0.1:30000/metrics" {
		t.Errorf("endpoint = %q, want config value", sp.endpoint)
	}
}

func TestOutputTokens(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		want    uint64
		wantErr bool
	}{
		{
			name: "reads current counter",
			body: sampleMetrics,
			want: 88200,
		},
		{
			name: "reads legacy prefix counter",
			body: legacyMetrics,
			want: 4100,
		},
		{
			name: "prefers current name over legacy",
			body: "sglang_generation_tokens_total 10\nsglang:generation_tokens_total 99\n",
			want: 10,
		},
		{
			name: "zero value",
			body: "sglang_generation_tokens_total 0\n",
			want: 0,
		},
		{
			name:    "metric absent returns error",
			body:    "# TYPE something_else gauge\nsomething_else 1\n",
			wantErr: true,
		},
		{
			name:    "empty body returns error (metrics not enabled)",
			body:    "",
			wantErr: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ts, p := serve(tc.body)
			defer ts.Close()
			got, err := p.OutputTokens(context.Background())
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
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
	tests := []struct {
		name string
		body string
		want int
	}{
		{"active requests", sampleMetrics, 5},
		{"legacy prefix", legacyMetrics, 2},
		{"zero requests", "sglang_num_running_reqs 0\n", 0},
		// absent metric is treated as 0 (idle assumption)
		{"metric absent returns zero", "# nothing here\n", 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ts, p := serve(tc.body)
			defer ts.Close()
			got, err := p.RequestsRunning(context.Background())
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %d, want %d", got, tc.want)
			}
		})
	}
}

func TestModelName(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{"extracts model_name label", sampleMetrics, "Qwen3-27B"},
		{"extracts from legacy prefix", legacyMetrics, "old-model"},
		{
			name: "different model name",
			body: `sglang_generation_tokens_total{model_name="meta-llama/Llama-3-8B"} 500` + "\n",
			want: "meta-llama/Llama-3-8B",
		},
		{
			// metric present but without label braces → no label → unknown
			name: "no label falls back to unknown",
			body: "sglang_generation_tokens_total 5\n",
			want: "unknown",
		},
		{"empty body falls back to unknown", "", "unknown"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ts, p := serve(tc.body)
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

func TestScrapeUnreachable(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	addr := ts.URL
	ts.Close() // close before any request

	p := &SGLangProvider{endpoint: addr, client: &http.Client{}}
	_, err := p.OutputTokens(context.Background())
	if err == nil {
		t.Fatal("expected connection error, got nil")
	}
}
