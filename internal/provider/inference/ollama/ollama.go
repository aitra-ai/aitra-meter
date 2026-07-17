// Package ollama provides an InferenceMetricsProvider for Ollama servers.
//
// Token and request metrics are read from Ollama's Prometheus /metrics endpoint
// (available on Ollama builds that expose Prometheus metrics, or via a metrics
// exporter sidecar). The currently-served model name is read from Ollama's
// native /api/ps endpoint, which is available on all Ollama builds and is what
// distinguishes this adapter from the generic-prometheus provider.
package ollama

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/aitra-ai/aitra-meter/internal/provider"
)

const (
	defaultMetricsEndpoint = "http://localhost:11434/metrics"
	defaultAPIBase         = "http://localhost:11434"
	defaultOutputMetric    = "ollama_completion_tokens_total"
	defaultRunningMetric   = "ollama_requests_active"
)

func init() {
	provider.RegisterInference("ollama", func(config map[string]string) (provider.InferenceMetricsProvider, error) {
		return build(config)
	})
}

// build constructs an OllamaProvider from a config map, applying defaults for
// any unset keys. Returns the concrete type so tests can inspect it.
func build(config map[string]string) (*OllamaProvider, error) {
	endpoint := config["endpoint"]
	if endpoint == "" {
		endpoint = defaultMetricsEndpoint
	}
	apiBase := config["api_base"]
	if apiBase == "" {
		apiBase = defaultAPIBase
	}
	outputMetric := config["output_tokens_metric"]
	if outputMetric == "" {
		outputMetric = defaultOutputMetric
	}
	runningMetric := config["requests_running_metric"]
	if runningMetric == "" {
		runningMetric = defaultRunningMetric
	}
	return &OllamaProvider{
		endpoint:      endpoint,
		apiBase:       strings.TrimRight(apiBase, "/"),
		outputMetric:  outputMetric,
		runningMetric: runningMetric,
		client:        &http.Client{Timeout: 10 * time.Second},
	}, nil
}

// OllamaProvider implements provider.InferenceMetricsProvider for Ollama.
type OllamaProvider struct {
	endpoint      string
	apiBase       string
	outputMetric  string
	runningMetric string
	client        *http.Client
}

func (o *OllamaProvider) Name() string { return "ollama" }

// OutputTokens returns the cumulative completion-token counter from Ollama's
// Prometheus /metrics endpoint.
func (o *OllamaProvider) OutputTokens(ctx context.Context) (uint64, error) {
	m, err := o.scrape(ctx)
	if err != nil {
		return 0, err
	}
	val, ok := m[o.outputMetric]
	if !ok {
		return 0, fmt.Errorf("metric %q not found at %s", o.outputMetric, o.endpoint)
	}
	return uint64(val), nil
}

// RequestsRunning returns the number of in-flight requests. A missing gauge is
// treated as zero (idle), matching the vLLM adapter's behaviour.
func (o *OllamaProvider) RequestsRunning(ctx context.Context) (int, error) {
	m, err := o.scrape(ctx)
	if err != nil {
		return 0, err
	}
	return int(m[o.runningMetric]), nil
}

// ModelName reports the currently-loaded model via Ollama's native /api/ps,
// which works on every Ollama build regardless of Prometheus support. Returns
// "unknown" when no model is resident.
func (o *OllamaProvider) ModelName(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, o.apiBase+"/api/ps", nil)
	if err != nil {
		return "", err
	}
	resp, err := o.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("querying %s/api/ps: %w", o.apiBase, err)
	}
	defer resp.Body.Close() //nolint:errcheck
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	var ps struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.Unmarshal(body, &ps); err != nil {
		return "", fmt.Errorf("decoding %s/api/ps: %w", o.apiBase, err)
	}
	if len(ps.Models) == 0 {
		return "unknown", nil
	}
	return ps.Models[0].Name, nil
}

// scrape parses the Prometheus text exposition at the metrics endpoint into a
// metric-name -> value map. Samples with labels are keyed by their bare metric
// name; the last matching series wins, which is sufficient for single-model
// Ollama deployments.
func (o *OllamaProvider) scrape(ctx context.Context) (map[string]float64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, o.endpoint, nil)
	if err != nil {
		return nil, err
	}
	resp, err := o.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("scraping %s: %w", o.endpoint, err)
	}
	defer resp.Body.Close() //nolint:errcheck
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	res := map[string]float64{}
	for _, line := range strings.Split(string(body), "\n") {
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
