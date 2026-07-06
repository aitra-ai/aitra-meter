// Package vllm provides an InferenceMetricsProvider for vLLM inference servers.
package vllm

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
	metricOutputTokens    = "vllm:generation_tokens_total"
	metricRequestsRunning = "vllm:num_requests_running"
	metricModelLabel      = "model_name"

	// Optional latency histograms (issue #41). Read-only correlation
	// targets — Aitra does not re-expose them.
	metricTTFT = "vllm:time_to_first_token_seconds"
	metricTPOT = "vllm:time_per_output_token_seconds"
)

func init() {
	provider.RegisterInference("vllm", func(config map[string]string) (provider.InferenceMetricsProvider, error) {
		endpoint := config["endpoint"]
		if endpoint == "" {
			endpoint = "http://localhost:8000/metrics"
		}
		return &VLLMProvider{
			endpoint: endpoint,
			client:   &http.Client{Timeout: 10 * time.Second},
		}, nil
	})
}

// VLLMProvider implements provider.InferenceMetricsProvider using vLLM Prometheus /metrics.
type VLLMProvider struct {
	endpoint string
	client   *http.Client
}

func (v *VLLMProvider) Name() string { return "vllm" }

func (v *VLLMProvider) OutputTokens(ctx context.Context) (uint64, error) {
	m, err := v.scrape(ctx)
	if err != nil {
		return 0, err
	}
	val, ok := m[metricOutputTokens]
	if !ok {
		return 0, fmt.Errorf("metric %q not found", metricOutputTokens)
	}
	return uint64(val), nil
}

func (v *VLLMProvider) RequestsRunning(ctx context.Context) (int, error) {
	m, err := v.scrape(ctx)
	if err != nil {
		return 0, err
	}
	return int(m[metricRequestsRunning]), nil
}

func (v *VLLMProvider) ModelName(ctx context.Context) (string, error) {
	lines, err := v.rawLines(ctx)
	if err != nil {
		return "", err
	}
	for _, line := range lines {
		if strings.HasPrefix(line, metricOutputTokens+"{") {
			if name := extractLabel(line, metricModelLabel); name != "" {
				return name, nil
			}
		}
	}
	return "unknown", nil
}

// Latency reads vLLM's time-to-first-token and time-per-output-token
// histogram totals (_count and _sum series). ok is false when the histograms
// are absent from the payload — absence is not an error, since older vLLM
// versions do not expose them.
func (v *VLLMProvider) Latency(ctx context.Context) (provider.LatencySample, bool, error) {
	m, err := v.scrape(ctx)
	if err != nil {
		return provider.LatencySample{}, false, err
	}
	ttftCount, okTTFT := m[metricTTFT+"_count"]
	tpotCount, okTPOT := m[metricTPOT+"_count"]
	if !okTTFT && !okTPOT {
		return provider.LatencySample{}, false, nil
	}
	return provider.LatencySample{
		TTFTCount: ttftCount,
		TTFTSum:   m[metricTTFT+"_sum"],
		TPOTCount: tpotCount,
		TPOTSum:   m[metricTPOT+"_sum"],
	}, true, nil
}

func (v *VLLMProvider) scrape(ctx context.Context) (map[string]float64, error) {
	lines, err := v.rawLines(ctx)
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
			res[name] = val
		}
	}
	return res, nil
}

func (v *VLLMProvider) rawLines(ctx context.Context) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, v.endpoint, nil)
	if err != nil {
		return nil, err
	}
	resp, err := v.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("scraping %s: %w", v.endpoint, err)
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
