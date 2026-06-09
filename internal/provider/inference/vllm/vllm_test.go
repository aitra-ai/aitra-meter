package vllm

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// sampleMetrics is a representative vLLM /metrics payload.
const sampleMetrics = `# HELP vllm:generation_tokens_total Total generation tokens.
# TYPE vllm:generation_tokens_total counter
vllm:generation_tokens_total{model_name="Qwen3-27B"} 12450
# HELP vllm:num_requests_running Number of running requests.
# TYPE vllm:num_requests_running gauge
vllm:num_requests_running 3
`

func serve(body string) (*httptest.Server, *VLLMProvider) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, body)
	}))
	p := &VLLMProvider{endpoint: ts.URL, client: ts.Client()}
	return ts, p
}

func TestName(t *testing.T) {
	p := &VLLMProvider{}
	if p.Name() != "vllm" {
		t.Errorf("got %q, want \"vllm\"", p.Name())
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
			name: "reads counter value",
			body: sampleMetrics,
			want: 12450,
		},
		{
			name: "large counter",
			body: "vllm:generation_tokens_total{model_name=\"llama\"} 9999999\n",
			want: 9999999,
		},
		{
			name: "zero value",
			body: "vllm:generation_tokens_total 0\n",
			want: 0,
		},
		{
			name:    "metric absent returns error",
			body:    "# TYPE something_else gauge\nsomething_else 1\n",
			wantErr: true,
		},
		{
			name:    "empty body returns error",
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
		{"active requests", sampleMetrics, 3},
		{"zero requests", "vllm:num_requests_running 0\n", 0},
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
		{
			name: "different model name",
			body: `vllm:generation_tokens_total{model_name="meta-llama/Llama-3-8B"} 500` + "\n",
			want: "meta-llama/Llama-3-8B",
		},
		{
			// metric present but without label braces → no label → unknown
			name: "no label falls back to unknown",
			body: "vllm:generation_tokens_total 5\n",
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

func TestScrapeHTTPError(t *testing.T) {
	// 500 with empty body: no metrics parsed → OutputTokens returns "not found" error
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer ts.Close()
	p := &VLLMProvider{endpoint: ts.URL, client: ts.Client()}

	_, err := p.OutputTokens(context.Background())
	if err == nil {
		t.Fatal("expected error from missing metric after HTTP 500, got nil")
	}
}

func TestScrapeUnreachable(t *testing.T) {
	// Point at a closed server — rawLines should return a transport error
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	addr := ts.URL
	ts.Close() // close before any request

	p := &VLLMProvider{endpoint: addr, client: &http.Client{}}
	_, err := p.OutputTokens(context.Background())
	if err == nil {
		t.Fatal("expected connection error, got nil")
	}
}

func TestExtractLabel(t *testing.T) {
	tests := []struct {
		line  string
		label string
		want  string
	}{
		{`vllm:generation_tokens_total{model_name="Qwen3-27B"} 1`, "model_name", "Qwen3-27B"},
		{`metric{a="x",model_name="llama"} 1`, "model_name", "llama"},
		{`metric{other="y"} 1`, "model_name", ""},
		{`metric 1`, "model_name", ""},
	}
	for _, tc := range tests {
		got := extractLabel(tc.line, tc.label)
		if got != tc.want {
			t.Errorf("extractLabel(%q, %q) = %q, want %q", tc.line, tc.label, got, tc.want)
		}
	}
}
