//go:build linux

// Package gracehwmon provides a HostEnergyProvider for the NVIDIA Grace Superchip
// (72-core), reading CPU power rails from the hwmon interface documented in the
// NVIDIA Grace Performance Tuning Guide.
//
// Unlike RAPL and NVML, the Grace hwmon interface exposes powerN_average — a
// POWER reading in microwatts averaged over an interval — not a monotonic energy
// counter. This provider therefore integrates power over the window (trapezoidal:
// mean of the start and end average-power samples times the elapsed time) rather
// than differencing a counter.
//
// The correct rail is identified by reading powerN_oem_info, which names the rail
// (e.g. "CPU Power Socket 0"); rails are never selected by index. Requires
// CONFIG_SENSORS_ACPI_POWER in the kernel. When the hwmon nodes are absent the
// provider reports Available()==false and every read returns
// provider.ErrHostEnergyUnavailable — never a zero reading.
//
// Note: this is the 72-core Grace Superchip, NOT the GB10 / DGX Spark. NVIDIA has
// stated Spark exposes no host power telemetry, so on GB10 the "none" provider is
// used and host energy is reported unavailable. See docs/guides/host-energy.md.
package gracehwmon

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aitra-ai/aitra-meter/internal/provider"
)

const (
	defaultBasePath  = "/sys/class/hwmon"
	defaultRailMatch = "CPU Power" // matched against powerN_oem_info (case-insensitive)
)

func init() {
	provider.RegisterHostEnergy("grace-hwmon", func(config map[string]string) (provider.HostEnergyProvider, error) {
		base := config["path"]
		if base == "" {
			base = defaultBasePath
		}
		match := config["rail_match"]
		if match == "" {
			match = defaultRailMatch
		}
		return newGrace(base, match), nil
	})
}

// rail is one selected hwmon power rail.
type rail struct {
	label       string // from powerN_oem_info, e.g. "CPU Power Socket 0"
	averagePath string // <hwmon>/powerN_average, microwatts
}

// GraceProvider implements provider.HostEnergyProvider over Grace hwmon rails.
type GraceProvider struct {
	rails         []rail
	unavailReason string

	mu      sync.Mutex
	windows map[string]graceWindow
}

type graceWindow struct {
	start      time.Time
	startWatts float64
}

func newGrace(base, railMatch string) *GraceProvider {
	rails, reason := discover(base, railMatch)
	return &GraceProvider{rails: rails, unavailReason: reason, windows: make(map[string]graceWindow)}
}

// discover scans hwmon devices for powerN_oem_info files whose label contains
// railMatch, and records the matching powerN_average paths.
func discover(base, railMatch string) ([]rail, string) {
	hwmons, err := filepath.Glob(filepath.Join(base, "hwmon*"))
	if err != nil || len(hwmons) == 0 {
		return nil, fmt.Sprintf("no hwmon devices under %s (CONFIG_SENSORS_ACPI_POWER / Grace power rails not present)", base)
	}
	match := strings.ToLower(railMatch)
	var rails []rail
	for _, h := range hwmons {
		infos, _ := filepath.Glob(filepath.Join(h, "power*_oem_info"))
		for _, info := range infos {
			label, err := readTrim(info)
			if err != nil {
				continue
			}
			if !strings.Contains(strings.ToLower(label), match) {
				continue
			}
			// power<N>_oem_info -> power<N>_average
			avg := strings.TrimSuffix(info, "_oem_info") + "_average"
			if _, err := readUint(avg); err != nil {
				continue
			}
			rails = append(rails, rail{label: label, averagePath: avg})
		}
	}
	if len(rails) == 0 {
		return nil, fmt.Sprintf("no hwmon power rail matching %q under %s", railMatch, base)
	}
	return rails, ""
}

func (p *GraceProvider) unavail() error {
	return fmt.Errorf("%w: %s", provider.ErrHostEnergyUnavailable, p.unavailReason)
}

// sumWatts returns the summed instantaneous average power across all rails, in watts.
func (p *GraceProvider) sumWatts() (float64, error) {
	var totalUW uint64
	for _, r := range p.rails {
		uw, err := readUint(r.averagePath)
		if err != nil {
			return 0, fmt.Errorf("grace-hwmon read %s: %w", r.averagePath, err)
		}
		totalUW += uw
	}
	return float64(totalUW) / 1e6, nil
}

// BeginWindow records the start time and start power for later integration.
func (p *GraceProvider) BeginWindow(_ context.Context, windowID string) error {
	if p.unavailReason != "" {
		return p.unavail()
	}
	w, err := p.sumWatts()
	if err != nil {
		return err
	}
	p.mu.Lock()
	p.windows[windowID] = graceWindow{start: time.Now(), startWatts: w}
	p.mu.Unlock()
	return nil
}

// EndWindow integrates power over the elapsed window (trapezoidal) and returns joules.
func (p *GraceProvider) EndWindow(_ context.Context, windowID string) (float64, error) {
	if p.unavailReason != "" {
		return 0, p.unavail()
	}
	p.mu.Lock()
	begin, ok := p.windows[windowID]
	delete(p.windows, windowID)
	p.mu.Unlock()
	if !ok {
		return 0, fmt.Errorf("grace-hwmon window %q not found", windowID)
	}
	endWatts, err := p.sumWatts()
	if err != nil {
		return 0, err
	}
	elapsed := time.Since(begin.start).Seconds()
	meanWatts := (begin.startWatts + endWatts) / 2
	return meanWatts * elapsed, nil
}

// IdlePower returns the current summed rail power in watts.
func (p *GraceProvider) IdlePower(_ context.Context) (float64, error) {
	if p.unavailReason != "" {
		return 0, p.unavail()
	}
	return p.sumWatts()
}

// Domains enumerates the selected power rails.
func (p *GraceProvider) Domains(_ context.Context) ([]provider.Device, error) {
	if p.unavailReason != "" {
		return nil, p.unavail()
	}
	devs := make([]provider.Device, 0, len(p.rails))
	for _, r := range p.rails {
		devs = append(devs, provider.Device{ID: r.label, Name: r.label, Type: "cpu"})
	}
	return devs, nil
}

// Available reports whether matching Grace power rails were found at construction.
func (p *GraceProvider) Available(_ context.Context) bool { return p.unavailReason == "" }

// Name returns "grace-hwmon".
func (p *GraceProvider) Name() string { return "grace-hwmon" }

func readTrim(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

func readUint(path string) (uint64, error) {
	s, err := readTrim(path)
	if err != nil {
		return 0, err
	}
	return strconv.ParseUint(s, 10, 64)
}
