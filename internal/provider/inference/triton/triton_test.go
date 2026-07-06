package triton

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aitra-ai/aitra-meter/internal/provider"
)

// sampleMetrics is a representative Triton /metrics payload with two models.
const sampleMetrics = `# HELP nv_inference_request_success Number of successful inference requests, all batch sizes
# TYPE nv_inference_request_success counter
nv_inference_request_success{model="llama-trtllm",version="1"} 300
nv_inference_request_success{model="reranker",version="2"} 700
# HELP nv_inference_pending_request_count Instantaneous number of pending requests awaiting execution per-model.
# TYPE nv_inference_pending_request_count gauge
nv_inference_pending_request_count{model="llama-trtllm",version="1"} 4
nv_inference_pending_request_count{model="reranker",version="2"} 1
`

func serve(body string) (*httptest.Server, *TritonProvider) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, body)
	}))
	p := &TritonProvider{endpoint: ts.URL, avgTokens: 1, client: ts.Client()}
	return ts, p
}

func TestImplementsInterface(t *testing.T) {
	var _ provider.InferenceMetricsProvider = &TritonProvider{}
}

func TestName(t *testing.T) {
	p := &TritonProvider{}
	if p.Name() != "triton" {
		t.Errorf("got %q, want \"triton\"", p.Name())
	}
}

func TestFactoryRegistered(t *testing.T) {
	p, err := provider.NewInference("triton", nil)
	if err != nil {
		t.Fatalf("factory not registered: %v", err)
	}
	if p.Name() != "triton" {
		t.Errorf("factory returned provider %q, want \"triton\"", p.Name())
	}
}

func TestFactoryConfig(t *testing.T) {
	tests := []struct {
		name    string
		config  map[string]string
		wantErr bool
		wantAvg float64
		wantURL string
	}{
		{
			name:    "defaults",
			config:  nil,
			wantAvg: 1,
			wantURL: DefaultEndpoint,
		},
		{
			name:    "custom endpoint and multiplier",
			config:  map[string]string{"endpoint": "http://10.0.0.2:8002/metrics", "avg_output_tokens_per_request": "250"},
			wantAvg: 250,
			wantURL: "http://10.0.0.2:8002/metrics",
		},
		{
			name:    "non-numeric multiplier rejected",
			config:  map[string]string{"avg_output_tokens_per_request": "many"},
			wantErr: true,
		},
		{
			name:    "zero multiplier rejected",
			config:  map[string]string{"avg_output_tokens_per_request": "0"},
			wantErr: true,
		},
		{
			name:    "negative multiplier rejected",
			config:  map[string]string{"avg_output_tokens_per_request": "-3"},
			wantErr: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p, err := provider.NewInference("triton", tc.config)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			tp, ok := p.(*TritonProvider)
			if !ok {
				t.Fatalf("factory returned %T, want *TritonProvider", p)
			}
			if tp.avgTokens != tc.wantAvg {
				t.Errorf("avgTokens = %v, want %v", tp.avgTokens, tc.wantAvg)
			}
			if tp.endpoint != tc.wantURL {
				t.Errorf("endpoint = %q, want %q", tp.endpoint, tc.wantURL)
			}
		})
	}
}

func TestOutputTokens(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		avgTokens float64
		want      uint64
		wantErr   bool
	}{
		{
			name:      "sums requests across models, multiplier 1",
			body:      sampleMetrics,
			avgTokens: 1,
			want:      1000, // 300 + 700
		},
		{
			name:      "applies avg tokens multiplier",
			body:      sampleMetrics,
			avgTokens: 250,
			want:      250000, // (300 + 700) × 250
		},
		{
			name:      "single model",
			body:      "nv_inference_request_success{model=\"m\",version=\"1\"} 42\n",
			avgTokens: 10,
			want:      420,
		},
		{
			name:      "zero requests",
			body:      "nv_inference_request_success{model=\"m\",version=\"1\"} 0\n",
			avgTokens: 100,
			want:      0,
		},
		{
			name:      "metric absent returns error",
			body:      "# TYPE something_else gauge\nsomething_else 1\n",
			avgTokens: 1,
			wantErr:   true,
		},
		{
			name:      "empty body returns error",
			body:      "",
			avgTokens: 1,
			wantErr:   true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ts, p := serve(tc.body)
			defer ts.Close()
			p.avgTokens = tc.avgTokens
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
		{"sums pending across models", sampleMetrics, 5}, // 4 + 1
		{"zero pending", "nv_inference_pending_request_count{model=\"m\"} 0\n", 0},
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
		{"first model label wins", sampleMetrics, "llama-trtllm"},
		{
			name: "single model",
			body: `nv_inference_request_success{model="ensemble-llm",version="1"} 9` + "\n",
			want: "ensemble-llm",
		},
		{
			name: "no label falls back to unknown",
			body: "nv_inference_request_success 5\n",
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

	p := &TritonProvider{endpoint: addr, avgTokens: 1, client: &http.Client{}}
	_, err := p.OutputTokens(context.Background())
	if err == nil {
		t.Fatal("expected connection error, got nil")
	}
}
