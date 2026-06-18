// Package dcgm provides an EnergyProvider that reads NVIDIA GPU energy from a
// node-local dcgm-exporter Prometheus endpoint. It suits enterprise clusters
// that already run dcgm-exporter (e.g. via the GPU Operator), alongside or
// instead of the in-process nvml provider.
//
// Energy comes from DCGM_FI_DEV_TOTAL_ENERGY_CONSUMPTION, a cumulative per-GPU
// counter in millijoules: BeginWindow snapshots it and EndWindow returns the
// joules consumed in between, summed across every GPU on the node. Idle power is
// the sum of DCGM_FI_DEV_POWER_USAGE (watts) across GPUs.
//
// Because it scrapes an HTTP endpoint rather than calling the driver directly,
// resolution is bounded by the dcgm-exporter collection interval; for the
// tightest window alignment prefer the nvml provider. This package is pure Go
// with no CGO or Python dependency and runs on any platform.
//
// Config keys (energyProvider.config in values.yaml):
//
//	endpoint       dcgm-exporter metrics URL (default http://localhost:9400/metrics)
//	energy-metric  override the energy counter name
//	power-metric   override the power gauge name
package dcgm

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aitra-ai/aitra-meter/internal/provider"
)

const (
	defaultEndpoint = "http://localhost:9400/metrics"
	// DCGM_FI_DEV_TOTAL_ENERGY_CONSUMPTION is a cumulative counter in millijoules.
	defaultEnergyMetric = "DCGM_FI_DEV_TOTAL_ENERGY_CONSUMPTION"
	// DCGM_FI_DEV_POWER_USAGE is instantaneous power in watts.
	defaultPowerMetric = "DCGM_FI_DEV_POWER_USAGE"

	gpuLabel   = "gpu"
	uuidLabel  = "UUID"
	modelLabel = "modelName"
)

func init() {
	provider.RegisterEnergy("dcgm", func(config map[string]string) (provider.EnergyProvider, error) {
		return New(config), nil
	})
}

// DCGMProvider implements provider.EnergyProvider by scraping dcgm-exporter.
type DCGMProvider struct {
	endpoint     string
	energyMetric string
	powerMetric  string
	client       *http.Client

	mu      sync.Mutex
	windows map[string]float64 // windowID -> start energy in joules
}

// New builds a DCGMProvider from a config map. Unset keys take their defaults.
func New(config map[string]string) *DCGMProvider {
	endpoint := config["endpoint"]
	if endpoint == "" {
		endpoint = defaultEndpoint
	}
	energyMetric := config["energy-metric"]
	if energyMetric == "" {
		energyMetric = defaultEnergyMetric
	}
	powerMetric := config["power-metric"]
	if powerMetric == "" {
		powerMetric = defaultPowerMetric
	}
	return &DCGMProvider{
		endpoint:     endpoint,
		energyMetric: energyMetric,
		powerMetric:  powerMetric,
		client:       &http.Client{Timeout: 10 * time.Second},
		windows:      make(map[string]float64),
	}
}

// Name returns the provider identifier used in metric labels and logs.
func (d *DCGMProvider) Name() string { return "dcgm" }

// BeginWindow snapshots cumulative GPU energy at the start of the window.
func (d *DCGMProvider) BeginWindow(ctx context.Context, windowID string) error {
	joules, err := d.totalEnergyJoules(ctx)
	if err != nil {
		return err
	}
	d.mu.Lock()
	d.windows[windowID] = joules
	d.mu.Unlock()
	return nil
}

// EndWindow returns the joules consumed since BeginWindow, summed across all
// GPUs on the node. A counter reset (e.g. driver reload) is clamped to 0 so the
// value is never negative, per the EnergyProvider contract.
func (d *DCGMProvider) EndWindow(ctx context.Context, windowID string) (float64, error) {
	d.mu.Lock()
	start, ok := d.windows[windowID]
	delete(d.windows, windowID)
	d.mu.Unlock()
	if !ok {
		return 0, fmt.Errorf("window %q not found", windowID)
	}
	end, err := d.totalEnergyJoules(ctx)
	if err != nil {
		return 0, err
	}
	joules := end - start
	if joules < 0 {
		joules = 0
	}
	return joules, nil
}

// IdlePower returns the current GPU power draw in watts, summed across GPUs.
func (d *DCGMProvider) IdlePower(ctx context.Context) (float64, error) {
	vals, err := d.sample(ctx, d.powerMetric)
	if err != nil {
		return 0, err
	}
	var watts float64
	for _, v := range vals {
		watts += v
	}
	return watts, nil
}

// Devices enumerates the GPUs reported by dcgm-exporter, one per energy series.
func (d *DCGMProvider) Devices(ctx context.Context) ([]provider.Device, error) {
	lines, err := d.rawLines(ctx)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var devices []provider.Device
	for _, line := range lines {
		if !metricLine(line, d.energyMetric) {
			continue
		}
		id := extractLabel(line, uuidLabel)
		if id == "" {
			id = extractLabel(line, gpuLabel)
		}
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		name := extractLabel(line, modelLabel)
		if name == "" {
			name = "GPU " + extractLabel(line, gpuLabel)
		}
		devices = append(devices, provider.Device{ID: id, Name: name, Type: "gpu"})
	}
	return devices, nil
}

// totalEnergyJoules sums the energy counter (millijoules) across GPUs and
// converts to joules. It errors when the metric is absent so a failed or empty
// scrape is not silently read as zero energy.
func (d *DCGMProvider) totalEnergyJoules(ctx context.Context) (float64, error) {
	vals, err := d.sample(ctx, d.energyMetric)
	if err != nil {
		return 0, err
	}
	if len(vals) == 0 {
		return 0, fmt.Errorf("metric %q not found at %s", d.energyMetric, d.endpoint)
	}
	var milliJoules float64
	for _, v := range vals {
		milliJoules += v
	}
	return milliJoules / 1000.0, nil
}

// sample returns the value of every series of the named metric (one per GPU).
func (d *DCGMProvider) sample(ctx context.Context, metric string) ([]float64, error) {
	lines, err := d.rawLines(ctx)
	if err != nil {
		return nil, err
	}
	var vals []float64
	for _, line := range lines {
		if !metricLine(line, metric) {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		v, err := strconv.ParseFloat(fields[len(fields)-1], 64)
		if err != nil {
			continue
		}
		vals = append(vals, v)
	}
	return vals, nil
}

func (d *DCGMProvider) rawLines(ctx context.Context) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, d.endpoint, nil)
	if err != nil {
		return nil, err
	}
	resp, err := d.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("scraping %s: %w", d.endpoint, err)
	}
	defer resp.Body.Close() //nolint:errcheck
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	return strings.Split(string(body), "\n"), nil
}

// metricLine reports whether a Prometheus text line is a sample of metric — the
// metric name followed by '{' labels or a space before the value. It excludes
// HELP/TYPE comments and metrics that merely share a name prefix.
func metricLine(line, metric string) bool {
	if !strings.HasPrefix(line, metric) {
		return false
	}
	rest := line[len(metric):]
	return strings.HasPrefix(rest, "{") || strings.HasPrefix(rest, " ")
}

// extractLabel returns the value of label in a Prometheus text line, or "".
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
