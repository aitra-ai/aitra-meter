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

// sample is a single Prometheus series: its raw label set and its value.
type sample struct {
	labels string
	value  float64
}

// OutputTokens returns cumulative output tokens, summed across every series of
// the counter.
//
// A server that shards a model across GPUs reports one series per shard. SGLang,
// for example, labels each metric with tp_rank, pp_rank and moe_ep_rank, so a
// tensor-parallel deployment exposes one counter per rank. The tokens the model
// generated are the sum over those series; taking any single one undercounts by
// the parallelism degree and inflates J/token by the same factor.
func (g *GenericPrometheusProvider) OutputTokens(ctx context.Context) (uint64, error) {
	series, err := g.scrape(ctx)
	if err != nil {
		return 0, err
	}
	samples, ok := series[g.outputTokensMetric]
	if !ok || len(samples) == 0 {
		return 0, fmt.Errorf("generic-prometheus: metric %q not found at %s", g.outputTokensMetric, g.endpoint)
	}
	var total float64
	for _, s := range samples {
		total += s.value
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
	var total float64
	for _, s := range series[g.requestsRunningMetric] {
		total += s.value
	}
	return int(total), nil
}

// ModelName returns the model served at the endpoint, read from the model label
// on the output-token counter.
//
// Every series of that counter on a single-model server carries the same model
// name, so any series is representative. If series disagree, the endpoint is
// serving more than one model and its energy cannot be attributed to a single
// one; that is reported rather than silently resolved to whichever series came
// first.
func (g *GenericPrometheusProvider) ModelName(ctx context.Context) (string, error) {
	series, err := g.scrape(ctx)
	if err != nil {
		return "", err
	}
	found := ""
	for _, s := range series[g.outputTokensMetric] {
		name := extractLabel(s.labels, g.modelNameLabel)
		if name == "" {
			continue
		}
		if found == "" {
			found = name
			continue
		}
		if name != found {
			return "", fmt.Errorf("generic-prometheus: metric %q at %s reports multiple values for label %q (%q and %q); "+
				"energy cannot be attributed to a single model — point the provider at a single-model endpoint",
				g.outputTokensMetric, g.endpoint, g.modelNameLabel, found, name)
		}
	}
	if found == "" {
		return "unknown", nil
	}
	return found, nil
}

// scrape parses the endpoint into metric name -> series, preserving each
// series' label set. A metric exposed with several label sets yields one entry
// per series rather than a single collapsed value.
func (g *GenericPrometheusProvider) scrape(ctx context.Context) (map[string][]sample, error) {
	lines, err := g.rawLines(ctx)
	if err != nil {
		return nil, err
	}
	series := map[string][]sample{}
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) < 2 {
			continue
		}
		name, labels := parts[0], ""
		if i := strings.Index(name, "{"); i > 0 {
			if j := strings.LastIndex(name, "}"); j > i {
				labels = name[i+1 : j]
			}
			name = name[:i]
		}
		val, err := strconv.ParseFloat(parts[len(parts)-1], 64)
		if err != nil {
			continue
		}
		series[name] = append(series[name], sample{labels: labels, value: val})
	}
	return series, nil
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

// extractLabel pulls one label's value out of a raw label set.
func extractLabel(labels, label string) string {
	key := label + `="`
	idx := strings.Index(labels, key)
	if idx < 0 {
		return ""
	}
	start := idx + len(key)
	end := strings.Index(labels[start:], `"`)
	if end < 0 {
		return ""
	}
	return labels[start : start+end]
}

func orDefault(v, d string) string {
	if v == "" {
		return d
	}
	return v
}
