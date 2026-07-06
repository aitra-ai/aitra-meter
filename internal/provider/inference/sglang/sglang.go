// Package sglang provides an InferenceMetricsProvider for SGLang inference
// servers.
//
// SGLang exposes Prometheus metrics on its serving port (default 30000) when
// launched with --enable-metrics. Without that flag the /metrics endpoint does
// not exist and this provider returns scrape errors — document the flag in any
// deployment that uses this provider.
//
// Metric naming: SGLang v0.5.4 renamed its metric prefix from "sglang:" to
// "sglang_". This provider reads the current name first and falls back to the
// legacy name so both old and new servers work without configuration.
package sglang

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
	// Current metric names (SGLang v0.5.4+, "sglang_" prefix).
	metricOutputTokens    = "sglang_generation_tokens_total"
	metricRequestsRunning = "sglang_num_running_reqs"

	// Legacy metric names (pre-v0.5.4, "sglang:" prefix).
	legacyOutputTokens    = "sglang:generation_tokens_total"
	legacyRequestsRunning = "sglang:num_running_reqs"

	metricModelLabel = "model_name"

	// DefaultEndpoint is the SGLang serving port; metrics share it.
	DefaultEndpoint = "http://localhost:30000/metrics"
)

func init() {
	provider.RegisterInference("sglang", func(config map[string]string) (provider.InferenceMetricsProvider, error) {
		endpoint := config["endpoint"]
		if endpoint == "" {
			endpoint = DefaultEndpoint
		}
		return &SGLangProvider{
			endpoint: endpoint,
			client:   &http.Client{Timeout: 10 * time.Second},
		}, nil
	})
}

// SGLangProvider implements provider.InferenceMetricsProvider using SGLang's
// Prometheus /metrics endpoint (requires --enable-metrics on the server).
type SGLangProvider struct {
	endpoint string
	client   *http.Client
}

func (s *SGLangProvider) Name() string { return "sglang" }

func (s *SGLangProvider) OutputTokens(ctx context.Context) (uint64, error) {
	m, err := s.scrape(ctx)
	if err != nil {
		return 0, err
	}
	if val, ok := m[metricOutputTokens]; ok {
		return uint64(val), nil
	}
	if val, ok := m[legacyOutputTokens]; ok {
		return uint64(val), nil
	}
	return 0, fmt.Errorf("sglang: metric %q (or legacy %q) not found at %s — is the server running with --enable-metrics?",
		metricOutputTokens, legacyOutputTokens, s.endpoint)
}

func (s *SGLangProvider) RequestsRunning(ctx context.Context) (int, error) {
	m, err := s.scrape(ctx)
	if err != nil {
		return 0, err
	}
	if val, ok := m[metricRequestsRunning]; ok {
		return int(val), nil
	}
	// Absent metric is treated as 0 (idle assumption), matching the vLLM provider.
	return int(m[legacyRequestsRunning]), nil
}

func (s *SGLangProvider) ModelName(ctx context.Context) (string, error) {
	lines, err := s.rawLines(ctx)
	if err != nil {
		return "", err
	}
	for _, line := range lines {
		if strings.HasPrefix(line, metricOutputTokens+"{") || strings.HasPrefix(line, legacyOutputTokens+"{") {
			if name := extractLabel(line, metricModelLabel); name != "" {
				return name, nil
			}
		}
	}
	return "unknown", nil
}

func (s *SGLangProvider) scrape(ctx context.Context) (map[string]float64, error) {
	lines, err := s.rawLines(ctx)
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

func (s *SGLangProvider) rawLines(ctx context.Context) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.endpoint, nil)
	if err != nil {
		return nil, err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("sglang: scraping %s: %w", s.endpoint, err)
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
