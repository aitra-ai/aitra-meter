// Package triton provides an InferenceMetricsProvider for NVIDIA Triton
// Inference Server.
//
// Triton exposes Prometheus metrics on port 8002 by default. Unlike vLLM and
// SGLang, Triton does not expose a direct output-token counter for LLM
// backends (it is an orchestration layer over backends such as TensorRT-LLM).
// This provider therefore APPROXIMATES the token count:
//
//	tokens ≈ nv_inference_request_success × avg_output_tokens_per_request
//
// The multiplier defaults to 1 (i.e. the counter reports requests, not
// tokens) and should be set from your own workload's average via the
// avg_output_tokens_per_request config key. Energy-per-token figures derived
// from this provider are approximate and less accurate than vLLM/SGLang —
// see docs/guides/inference-providers.md for the full limitation notes.
//
// Metrics are summed across all models and versions served by the Triton
// instance, because Triton reports one series per (model, version) pair.
package triton

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/aitra-ai/aitra-meter/internal/provider"
)

const (
	// metricRequestSuccess counts successful inference requests per model/version.
	metricRequestSuccess = "nv_inference_request_success"
	// metricPendingRequests is the number of pending requests per model.
	metricPendingRequests = "nv_inference_pending_request_count"
	// metricModelLabel is Triton's model-name label.
	metricModelLabel = "model"

	// DefaultEndpoint is Triton's default metrics port.
	DefaultEndpoint = "http://localhost:8002/metrics"

	// configAvgOutputTokens is the config key for the tokens-per-request
	// multiplier used in the token approximation.
	configAvgOutputTokens = "avg_output_tokens_per_request" //nolint:gosec // G101 false positive: config key name, not a credential
)

func init() {
	provider.RegisterInference("triton", func(config map[string]string) (provider.InferenceMetricsProvider, error) {
		endpoint := config["endpoint"]
		if endpoint == "" {
			endpoint = DefaultEndpoint
		}
		avgTokens := 1.0
		if raw := config[configAvgOutputTokens]; raw != "" {
			v, err := strconv.ParseFloat(raw, 64)
			if err != nil || v <= 0 {
				return nil, fmt.Errorf("triton: %s must be a positive number, got %q", configAvgOutputTokens, raw)
			}
			avgTokens = v
		}
		return &TritonProvider{
			endpoint:  endpoint,
			avgTokens: avgTokens,
			client:    &http.Client{Timeout: 10 * time.Second},
		}, nil
	})
}

// TritonProvider implements provider.InferenceMetricsProvider using Triton's
// Prometheus /metrics endpoint. Token counts are approximate — see the
// package documentation.
type TritonProvider struct {
	endpoint  string
	avgTokens float64
	client    *http.Client
}

func (t *TritonProvider) Name() string { return "triton" }

// OutputTokens returns an APPROXIMATE cumulative token count:
// successful requests summed across all models, multiplied by the configured
// avg_output_tokens_per_request (default 1). Triton does not expose a direct
// token counter for LLM backends.
func (t *TritonProvider) OutputTokens(ctx context.Context) (uint64, error) {
	m, err := t.scrapeSum(ctx)
	if err != nil {
		return 0, err
	}
	val, ok := m[metricRequestSuccess]
	if !ok {
		return 0, fmt.Errorf("triton: metric %q not found at %s", metricRequestSuccess, t.endpoint)
	}
	return uint64(val * t.avgTokens), nil
}

// RequestsRunning returns pending inference requests summed across all
// models. Absent metric is treated as 0 (idle assumption), matching the
// other providers.
func (t *TritonProvider) RequestsRunning(ctx context.Context) (int, error) {
	m, err := t.scrapeSum(ctx)
	if err != nil {
		return 0, err
	}
	return int(m[metricPendingRequests]), nil
}

// ModelName returns the first model label found on the request-success
// counter. When Triton serves multiple models, only the first is reported —
// the whole-node energy attribution model has one token source per node.
func (t *TritonProvider) ModelName(ctx context.Context) (string, error) {
	lines, err := t.rawLines(ctx)
	if err != nil {
		return "", err
	}
	for _, line := range lines {
		if strings.HasPrefix(line, metricRequestSuccess+"{") {
			if name := extractLabel(line, metricModelLabel); name != "" {
				return name, nil
			}
		}
	}
	return "unknown", nil
}

// scrapeSum parses the metrics payload and SUMS all samples that share a
// metric name. Triton emits one series per (model, version) pair, so a plain
// last-value map would silently drop all models but one.
func (t *TritonProvider) scrapeSum(ctx context.Context) (map[string]float64, error) {
	lines, err := t.rawLines(ctx)
	if err != nil {
		return nil, err
	}
	res := map[string]float64{}
	for _, line := range lines {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) < 2 {
			continue
		}
		name := parts[0]
		if i := strings.Index(name, "{"); i > 0 {
			name = name[:i]
		}
		if val, err := strconv.ParseFloat(parts[len(parts)-1], 64); err == nil {
			res[name] += val
		}
	}
	return res, nil
}

func (t *TritonProvider) rawLines(ctx context.Context) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, t.endpoint, nil)
	if err != nil {
		return nil, err
	}
	resp, err := t.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("triton: scraping %s: %w", t.endpoint, err)
	}
	defer resp.Body.Close() //nolint:errcheck
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	return strings.Split(string(body), "\n"), nil
}

func extractLabel(line, label string) string {
	key := label + `="`
	idx := strings.Index(line, key)
	if idx < 0 {
		return ""
	}
	start := idx + len(key)
	end := strings.Index(line[start:], `"`)
	if end < 0 {
		return ""
	}
	return line[start : start+end]
}
