package genericprometheus

import (
	"context"
	"fmt"

	"github.com/aitra-ai/aitra-meter/internal/provider"
)

func init() {
	provider.RegisterInference("generic-prometheus", func(config map[string]string) (provider.InferenceMetricsProvider, error) {
		return &GenericPrometheusProvider{
			endpoint:          config["endpoint"],
			outputTokensMetric: orDefault(config["output_tokens_metric"], "inference_output_tokens_total"),
			requestsRunningMetric: orDefault(config["requests_running_metric"], "inference_requests_running"),
			modelNameLabel:    orDefault(config["model_name_label"], "model_name"),
		}, nil
	})
}

// GenericPrometheusProvider implements InferenceMetricsProvider for any inference
// server that exposes a Prometheus endpoint with configurable metric names.
// Compatible with TGI, SGLang, Ollama, Triton, and custom servers.
type GenericPrometheusProvider struct {
	endpoint              string
	outputTokensMetric    string
	requestsRunningMetric string
	modelNameLabel        string
}

func (g *GenericPrometheusProvider) Name() string { return "generic-prometheus" }

func (g *GenericPrometheusProvider) OutputTokens(ctx context.Context) (uint64, error) {
	// TODO: scrape g.outputTokensMetric from g.endpoint
	return 0, fmt.Errorf("not implemented")
}

func (g *GenericPrometheusProvider) RequestsRunning(ctx context.Context) (int, error) {
	// TODO: scrape g.requestsRunningMetric from g.endpoint
	return 0, fmt.Errorf("not implemented")
}

func (g *GenericPrometheusProvider) ModelName(ctx context.Context) (string, error) {
	// TODO: read g.modelNameLabel from scraped metrics
	return "", fmt.Errorf("not implemented")
}

func orDefault(v, d string) string {
	if v == "" { return d }
	return v
}
