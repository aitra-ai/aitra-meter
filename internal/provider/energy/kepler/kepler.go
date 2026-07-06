// Package kepler provides an EnergyProvider that reads container energy from
// Kepler (https://github.com/sustainable-computing-io/kepler), a CNCF project
// that attributes node energy (CPU, DRAM, GPU) to pods and containers via eBPF.
//
// The provider is additive to Kepler rather than a replacement: Kepler reports
// where the watts went; Aitra Meter reports what AI output those watts
// produced. It suits clusters where Kepler is already deployed and NVML access
// is restricted, where CPU and DRAM attribution is needed alongside GPU
// energy, or where GPU and CPU inference nodes are mixed. On NVIDIA GPU nodes
// where both are available, prefer the nvml provider — it reads the hardware
// energy counter directly with no scrape-interval lag.
//
// Energy comes from kepler_container_joules_total, a cumulative per-container
// counter in joules: BeginWindow snapshots the sum of the matching series and
// EndWindow returns the joules consumed in between. Idle power falls back to
// the rate of kepler_node_package_joules_total between successive IdlePower
// calls. Resolution is bounded by the Kepler/Prometheus scrape interval, so
// set scrape_interval to match the deployment.
//
// The endpoint may be either a Prometheus server base URL (the provider then
// reads through the /federate endpoint, which returns text exposition) or any
// URL that serves Prometheus text exposition directly, such as a node-local
// Kepler exporter /metrics URL. This package is pure Go with no CGO and no
// build tag.
//
// Config keys (energyProvider.config in values.yaml):
//
//	endpoint         required — Prometheus base URL (e.g. http://prometheus-operated.monitoring.svc.cluster.local:9090)
//	                 or a direct text-exposition URL (e.g. http://kepler.kepler.svc.cluster.local:9102/metrics)
//	container_label  label key used to filter container series (default "container";
//	                 some Kepler releases emit "container_name" instead)
//	container_name   optional label value — when set, only series whose
//	                 container_label equals it are summed; when empty, all
//	                 container series are summed
//	scrape_interval  how often the underlying counters advance (default "30s");
//	                 also the minimum spacing between idle-power samples
package kepler

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aitra-ai/aitra-meter/internal/provider"
)

const (
	// containerMetric is Kepler's cumulative per-container energy counter in joules.
	containerMetric = "kepler_container_joules_total"
	// nodeMetric is Kepler's cumulative per-node CPU package energy counter in joules.
	nodeMetric = "kepler_node_package_joules_total"

	defaultContainerLabel = "container"
	defaultScrapeInterval = 30 * time.Second
)

func init() {
	provider.RegisterEnergy("kepler", func(config map[string]string) (provider.EnergyProvider, error) {
		return New(config)
	})
}

// KeplerProvider implements provider.EnergyProvider by reading Kepler counters
// over HTTP in Prometheus text exposition format.
type KeplerProvider struct {
	scrapeURL      string
	containerLabel string
	containerName  string
	scrapeInterval time.Duration
	client         *http.Client
	now            func() time.Time // injectable for tests

	mu        sync.Mutex
	windows   map[string]float64 // windowID -> start energy in joules
	idlePrev  *idleSample        // last node-package sample used as rate base
	idleWatts float64            // last computed idle power
	idleValid bool
}

// idleSample is one reading of the node package counter.
type idleSample struct {
	joules float64
	at     time.Time
}

// New builds a KeplerProvider from a config map. endpoint is required; the
// remaining keys take their defaults.
func New(config map[string]string) (*KeplerProvider, error) {
	endpoint := config["endpoint"]
	if endpoint == "" {
		return nil, fmt.Errorf("kepler: endpoint is required (Prometheus base URL or a text-exposition metrics URL)")
	}
	scrapeURL, err := buildScrapeURL(endpoint)
	if err != nil {
		return nil, err
	}
	containerLabel := config["container_label"]
	if containerLabel == "" {
		containerLabel = defaultContainerLabel
	}
	interval := defaultScrapeInterval
	if v := config["scrape_interval"]; v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d <= 0 {
			return nil, fmt.Errorf("kepler: invalid scrape_interval %q (want a positive Go duration like \"30s\")", v)
		}
		interval = d
	}
	return &KeplerProvider{
		scrapeURL:      scrapeURL,
		containerLabel: containerLabel,
		containerName:  config["container_name"],
		scrapeInterval: interval,
		client:         &http.Client{Timeout: 10 * time.Second},
		now:            time.Now,
		windows:        make(map[string]float64),
	}, nil
}

// buildScrapeURL turns the configured endpoint into the URL that is scraped.
// A bare Prometheus base URL (no path) becomes a /federate query for the two
// Kepler metric families; a URL with a path is used as-is.
func buildScrapeURL(endpoint string) (string, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return "", fmt.Errorf("kepler: invalid endpoint %q: %w", endpoint, err)
	}
	if u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("kepler: endpoint %q must be an absolute URL (e.g. http://prometheus-operated.monitoring.svc.cluster.local:9090)", endpoint)
	}
	if u.Path == "" || u.Path == "/" {
		u.Path = "/federate"
		q := u.Query()
		q["match[]"] = []string{containerMetric, nodeMetric}
		u.RawQuery = q.Encode()
	}
	return u.String(), nil
}

// Name returns the provider identifier used in metric labels and logs.
func (k *KeplerProvider) Name() string { return "kepler" }

// BeginWindow snapshots the cumulative container energy at the window start.
func (k *KeplerProvider) BeginWindow(ctx context.Context, windowID string) error {
	joules, err := k.containerJoules(ctx)
	if err != nil {
		return err
	}
	k.mu.Lock()
	k.windows[windowID] = joules
	k.mu.Unlock()
	return nil
}

// EndWindow returns the joules consumed since BeginWindow, summed across the
// matching container series. A counter reset (e.g. Kepler restart) is clamped
// to 0 so the value is never negative, per the EnergyProvider contract. A
// zero delta — an idle window, or two reads inside one Kepler scrape interval
// — is a valid result, not an error.
func (k *KeplerProvider) EndWindow(ctx context.Context, windowID string) (float64, error) {
	k.mu.Lock()
	start, ok := k.windows[windowID]
	delete(k.windows, windowID)
	k.mu.Unlock()
	if !ok {
		return 0, fmt.Errorf("kepler: window %q not found", windowID)
	}
	end, err := k.containerJoules(ctx)
	if err != nil {
		return 0, err
	}
	joules := end - start
	if joules < 0 {
		joules = 0
	}
	return joules, nil
}

// IdlePower returns the node CPU package power in watts, derived from the
// rate of kepler_node_package_joules_total between successive calls. Kepler
// exposes cumulative joule counters rather than a power gauge, so at least
// two samples scrape_interval apart are needed; the first call primes the
// sample and returns an error, and calls closer together than scrape_interval
// return the last computed value. Devices reports a single synthetic
// aggregate device, so no per-device division applies.
func (k *KeplerProvider) IdlePower(ctx context.Context) (float64, error) {
	joules, err := k.nodePackageJoules(ctx)
	if err != nil {
		return 0, err
	}
	cur := idleSample{joules: joules, at: k.now()}

	k.mu.Lock()
	defer k.mu.Unlock()
	if k.idlePrev == nil {
		k.idlePrev = &cur
		return 0, fmt.Errorf("kepler: idle power needs two samples of %s at least %s apart — retry after the next scrape", nodeMetric, k.scrapeInterval)
	}
	elapsed := cur.at.Sub(k.idlePrev.at)
	if elapsed <= 0 || elapsed < k.scrapeInterval {
		if k.idleValid {
			return k.idleWatts, nil
		}
		return 0, fmt.Errorf("kepler: idle power needs two samples of %s at least %s apart — retry after the next scrape", nodeMetric, k.scrapeInterval)
	}
	watts := (cur.joules - k.idlePrev.joules) / elapsed.Seconds()
	if watts < 0 {
		watts = 0 // counter reset
	}
	k.idlePrev = &cur
	k.idleWatts = watts
	k.idleValid = true
	return watts, nil
}

// Devices returns a single synthetic device: Kepler attributes energy at the
// container level (CPU, DRAM and GPU combined), not per physical device.
func (k *KeplerProvider) Devices(_ context.Context) ([]provider.Device, error) {
	return []provider.Device{{
		ID:   "kepler-aggregate",
		Name: "Kepler container energy (aggregate)",
		Type: "other",
	}}, nil
}

// containerJoules sums kepler_container_joules_total across the series that
// match the container filter. It errors when nothing usable matches so a
// failed, empty or misconfigured scrape is not silently read as zero energy.
func (k *KeplerProvider) containerJoules(ctx context.Context) (float64, error) {
	lines, err := k.rawLines(ctx)
	if err != nil {
		return 0, err
	}
	var sum float64
	series, matched := 0, 0
	for _, line := range lines {
		if !metricLine(line, containerMetric) {
			continue
		}
		series++
		name := extractLabel(line, k.containerLabel)
		if name == "" {
			continue // series without the filter label cannot be attributed
		}
		if k.containerName != "" && name != k.containerName {
			continue
		}
		v, ok := sampleValue(line, containerMetric)
		if !ok {
			continue // malformed sample value
		}
		sum += v
		matched++
	}
	if matched == 0 {
		if series == 0 {
			return 0, fmt.Errorf("kepler: metric %q not found at %s — is Kepler deployed and scraped?", containerMetric, k.scrapeURL)
		}
		if k.containerName != "" {
			return 0, fmt.Errorf("kepler: no %s series with %s=%q and a numeric value at %s", containerMetric, k.containerLabel, k.containerName, k.scrapeURL)
		}
		return 0, fmt.Errorf("kepler: found %d %s series at %s but none had a usable %q label and numeric value — set container_label to match your Kepler version", series, containerMetric, k.scrapeURL, k.containerLabel)
	}
	return sum, nil
}

// nodePackageJoules sums kepler_node_package_joules_total across all series.
func (k *KeplerProvider) nodePackageJoules(ctx context.Context) (float64, error) {
	lines, err := k.rawLines(ctx)
	if err != nil {
		return 0, err
	}
	var sum float64
	count := 0
	for _, line := range lines {
		if !metricLine(line, nodeMetric) {
			continue
		}
		v, ok := sampleValue(line, nodeMetric)
		if !ok {
			continue
		}
		sum += v
		count++
	}
	if count == 0 {
		return 0, fmt.Errorf("kepler: metric %q not found at %s", nodeMetric, k.scrapeURL)
	}
	return sum, nil
}

func (k *KeplerProvider) rawLines(ctx context.Context) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, k.scrapeURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := k.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("kepler: scraping %s: %w", k.scrapeURL, err)
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("kepler: scraping %s: unexpected status %s", k.scrapeURL, resp.Status)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	return strings.Split(string(body), "\n"), nil
}

// metricLine reports whether a Prometheus text line is a sample of metric —
// the metric name followed by '{' labels or a space before the value. It
// excludes HELP/TYPE comments and metrics that merely share a name prefix.
func metricLine(line, metric string) bool {
	if !strings.HasPrefix(line, metric) {
		return false
	}
	rest := line[len(metric):]
	return strings.HasPrefix(rest, "{") || strings.HasPrefix(rest, " ")
}

// sampleValue parses the sample value of a text-exposition line whose metric
// name has already been matched with metricLine. It tolerates label values
// containing spaces and the trailing timestamp that Prometheus federation
// appends after the value.
func sampleValue(line, metric string) (float64, bool) {
	rest := line[len(metric):]
	if strings.HasPrefix(rest, "{") {
		end := labelsEnd(rest)
		if end < 0 {
			return 0, false
		}
		rest = rest[end:]
	}
	fields := strings.Fields(rest)
	if len(fields) == 0 {
		return 0, false
	}
	v, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

// labelsEnd returns the index just past the '}' that closes the label block
// starting at s[0] == '{', respecting quoted label values, or -1 when the
// block never closes.
func labelsEnd(s string) int {
	inQuotes := false
	for i := 1; i < len(s); i++ {
		switch s[i] {
		case '\\':
			if inQuotes {
				i++ // skip the escaped character inside a quoted value
			}
		case '"':
			inQuotes = !inQuotes
		case '}':
			if !inQuotes {
				return i + 1
			}
		}
	}
	return -1
}

// extractLabel returns the value of a label key in a text-exposition line, or
// "". The key must open the label block or follow a comma, so that e.g.
// container does not match a pod_container label.
func extractLabel(line, label string) string {
	for _, prefix := range []string{"{" + label + `="`, "," + label + `="`} {
		idx := strings.Index(line, prefix)
		if idx < 0 {
			continue
		}
		start := idx + len(prefix)
		end := strings.Index(line[start:], `"`)
		if end < 0 {
			return ""
		}
		return line[start : start+end]
	}
	return ""
}
