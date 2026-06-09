// Package genericprometheus provides an InferenceMetricsProvider for any
// inference server that exposes a Prometheus endpoint. Metric names and the
// model name label are configurable so the same provider works with TGI,
// SGLang, Ollama, Triton, and custom servers.
package genericprometheus

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

func init() {
	provider.RegisterInference("generic-prometheus", func(config map[string]string) (provider.InferenceMetricsProvider, error) {
		endpoint := config["endpoint"]
		if endpoint == "" {
			return nil, fmt.Errorf("generic-prometheus: endpoint is required")
		}
		return &GenericPrometheusProvider{
			endpoint:              endpoint,
			outputTokensMetric:    orDefault(config["output_tokens_metric"], "inference_output_tokens_total"),
			requestsRunningMetric: orDefault(config["requests_running_metric"], "inference_requests_running"),
			modelNameLabel:        orDefault(config["model_name_label"], "model_name"),
			client:                &http.Client{Timeout: 10 * time.Second},
		}, nil
	})
}

// GenericPrometheusProvider implements InferenceMetricsProvider for any
// inference server exposing a Prometheus /metrics endpoint.
type GenericPrometheusProvider struct {
	endpoint              string
	outputTokensMetric    string
	requestsRunningMetric string
	modelNameLabel        string
	client                *http.Client
}

func (g *GenericPrometheusProvider) Name() string { return "generic-prometheus" }

func (g *GenericPrometheusProvider) OutputTokens(ctx context.Context) (uint64, error) {
	m, err := g.scrape(ctx)
	if err != nil {
		return 0, err
	}
	val, ok := m[g.outputTokensMetric]
	if !ok {
		return 0, fmt.Errorf("generic-prometheus: metric %q not found at %s", g.outputTokensMetric, g.endpoint)
	}
	return uint64(val), nil
}

func (g *GenericPrometheusProvider) RequestsRunning(ctx context.Context) (int, error) {
	m, err := g.scrape(ctx)
	if err != nil {
		return 0, err
	}
	return int(m[g.requestsRunningMetric]), nil
}

func (g *GenericPrometheusProvider) ModelName(ctx context.Context) (string, error) {
	lines, err := g.rawLines(ctx)
	if err != nil {
		return "", err
	}
	prefix := g.outputTokensMetric + "{"
	for _, line := range lines {
		if strings.HasPrefix(line, prefix) {
			if name := extractLabel(line, g.modelNameLabel); name != "" {
				return name, nil
			}
		}
	}
	return "unknown", nil
}

func (g *GenericPrometheusProvider) scrape(ctx context.Context) (map[string]float64, error) {
	lines, err := g.rawLines(ctx)
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

func (g *GenericPrometheusProvider) rawLines(ctx context.Context) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, g.endpoint, nil)
	if err != nil {
		return nil, err
	}
	resp, err := g.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("generic-prometheus: scraping %s: %w", g.endpoint, err)
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

func orDefault(v, d string) string {
	if v == "" {
		return d
	}
	return v
}
