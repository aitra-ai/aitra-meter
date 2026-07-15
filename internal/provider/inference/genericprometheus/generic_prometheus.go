// Package genericprometheus provides an InferenceMetricsProvider for any
// inference server that exposes a Prometheus endpoint. Metric names and the
// model name label are configurable so the same provider works with TGI,
// SGLang, Ollama, Triton, and custom servers.
//
// Parsing is delegated to the shared promtext package, which preserves each
// series' label set and sums across series. A server that shards a model across
// GPUs exposes one series per shard; the tokens the model generated are the sum
// over them.
package genericprometheus

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/aitra-ai/aitra-meter/internal/provider"
	"github.com/aitra-ai/aitra-meter/internal/provider/inference/promtext"
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

// OutputTokens returns cumulative output tokens, summed across every series of
// the configured counter.
func (g *GenericPrometheusProvider) OutputTokens(ctx context.Context) (uint64, error) {
	series, err := g.scrape(ctx)
	if err != nil {
		return 0, err
	}
	total, ok := promtext.Sum(series, g.outputTokensMetric)
	if !ok {
		return 0, fmt.Errorf("generic-prometheus: metric %q not found at %s", g.outputTokensMetric, g.endpoint)
	}
	if total < 0 {
		return 0, fmt.Errorf("generic-prometheus: metric %q summed to a negative value (%v) at %s",
			g.outputTokensMetric, total, g.endpoint)
	}
	return uint64(total), nil
}

// RequestsRunning returns in-flight requests, summed across series. It feeds
// idle detection: the server is busy if any shard is busy.
func (g *GenericPrometheusProvider) RequestsRunning(ctx context.Context) (int, error) {
	series, err := g.scrape(ctx)
	if err != nil {
		return 0, err
	}
	total, _ := promtext.Sum(series, g.requestsRunningMetric)
	return int(total), nil
}

// ModelName returns the model served at the endpoint, read from the model label
// on the output-token counter. If series disagree, the endpoint is serving more
// than one model and its energy cannot be attributed to a single one; that is
// reported rather than silently resolved.
func (g *GenericPrometheusProvider) ModelName(ctx context.Context) (string, error) {
	series, err := g.scrape(ctx)
	if err != nil {
		return "", err
	}
	value, conflict, consistent := promtext.SingleLabelValue(series, g.outputTokensMetric, g.modelNameLabel)
	if !consistent {
		return "", fmt.Errorf("generic-prometheus: metric %q at %s reports multiple values for label %q (%q and %q); "+
			"energy cannot be attributed to a single model — point the provider at a single-model endpoint",
			g.outputTokensMetric, g.endpoint, g.modelNameLabel, value, conflict)
	}
	if value == "" {
		return "unknown", nil
	}
	return value, nil
}

func (g *GenericPrometheusProvider) scrape(ctx context.Context) (map[string][]promtext.Sample, error) {
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
	return promtext.Parse(strings.Split(string(body), "\n")), nil
}

func orDefault(v, d string) string {
	if v == "" {
		return d
	}
	return v
}
