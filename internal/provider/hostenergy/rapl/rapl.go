//go:build linux

// Package rapl provides a HostEnergyProvider that reads host (CPU package + DRAM)
// energy from the Linux powercap RAPL interface at /sys/class/powercap. It is
// pure Go — no cgo, no sidecar, no new dependency — and reads a monotonic
// microjoule counter that it differences across a window, exactly like the NVML
// accelerator provider differences its energy accumulator.
//
// Domain selection: it sums the "package-N" domains and their "dram" subdomains.
// It deliberately excludes "core"/"uncore" (which are subsets of package and
// would double-count) and "psys" (a platform-wide superset that overlaps
// package). Domains are identified by reading each directory's "name" file, never
// by assuming ordering.
//
// On hardware with no RAPL interface, or where energy_uj exists but is unreadable
// (it is 0400 root on many distributions following CVE-2020-8694), the provider
// reports Available()==false and every read returns provider.ErrHostEnergyUnavailable
// with a distinct reason. It never returns a zero reading and never crashes.
package rapl

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aitra-ai/aitra-meter/internal/provider"
)

const defaultBasePath = "/sys/class/powercap"

func init() {
	provider.RegisterHostEnergy("rapl", func(config map[string]string) (provider.HostEnergyProvider, error) {
		base := config["path"]
		if base == "" {
			base = defaultBasePath
		}
		return newRAPL(base), nil
	})
}

// raplDomain is a single measurable RAPL domain (a package or a dram subdomain).
type raplDomain struct {
	name       string // e.g. "package-0", "dram"
	energyPath string // <dir>/energy_uj
	maxRangeUJ uint64 // <dir>/max_energy_range_uj — the value the counter wraps at
}

// RAPLProvider implements provider.HostEnergyProvider over /sys/class/powercap.
type RAPLProvider struct {
	basePath string

	// domains and unavailReason are resolved once at construction. If
	// unavailReason != "" the node has no usable RAPL telemetry and every read
	// returns ErrHostEnergyUnavailable wrapped with that reason.
	domains       []raplDomain
	unavailReason string

	mu      sync.Mutex
	windows map[string]map[string]uint64 // windowID -> domainName -> start microjoules
}

func newRAPL(base string) *RAPLProvider {
	domains, reason := discover(base)
	return &RAPLProvider{
		basePath:      base,
		domains:       domains,
		unavailReason: reason,
		windows:       make(map[string]map[string]uint64),
	}
}

// discover walks the powercap tree and returns the package + dram domains whose
// energy_uj is readable, plus a reason string that is non-empty when no usable
// domain exists (absent interface, or present-but-unreadable).
func discover(base string) ([]raplDomain, string) {
	entries, err := os.ReadDir(base)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Sprintf("no RAPL interface at %s (powercap/intel-rapl not present on this node)", base)
		}
		return nil, fmt.Sprintf("cannot read %s: %v", base, err)
	}

	var domains []raplDomain
	permissionDenied := false
	for _, e := range entries {
		name := e.Name()
		// Match intel-rapl:0, intel-rapl:0:1, etc. Exclude intel-rapl-mmio:*.
		if !strings.HasPrefix(name, "intel-rapl:") {
			continue
		}
		dir := filepath.Join(base, name)
		domainName, err := readTrim(filepath.Join(dir, "name"))
		if err != nil {
			continue
		}
		// package-N (top-level socket) and dram (its subdomain) only. core/uncore
		// are subsets of package; psys overlaps it — including either double-counts.
		if !strings.HasPrefix(domainName, "package") && domainName != "dram" {
			continue
		}
		energyPath := filepath.Join(dir, "energy_uj")
		if _, err := readUint(energyPath); err != nil {
			if errors.Is(err, os.ErrPermission) {
				permissionDenied = true
			}
			continue
		}
		maxRange, _ := readUint(filepath.Join(dir, "max_energy_range_uj"))
		domains = append(domains, raplDomain{name: domainName, energyPath: energyPath, maxRangeUJ: maxRange})
	}

	if len(domains) == 0 {
		if permissionDenied {
			return nil, fmt.Sprintf("RAPL energy_uj present under %s but unreadable (permission denied — "+
				"energy_uj is 0400 root since CVE-2020-8694; run the agent as root or make the file group-readable)", base)
		}
		return nil, fmt.Sprintf("no readable package/dram RAPL domains under %s", base)
	}
	return domains, ""
}

func (p *RAPLProvider) unavail() error {
	return fmt.Errorf("%w: %s", provider.ErrHostEnergyUnavailable, p.unavailReason)
}

// BeginWindow snapshots every domain's raw microjoule counter.
func (p *RAPLProvider) BeginWindow(_ context.Context, windowID string) error {
	if p.unavailReason != "" {
		return p.unavail()
	}
	snap := make(map[string]uint64, len(p.domains))
	for _, d := range p.domains {
		v, err := readUint(d.energyPath)
		if err != nil {
			return fmt.Errorf("rapl BeginWindow read %s: %w", d.energyPath, err)
		}
		snap[d.name] = v
	}
	p.mu.Lock()
	p.windows[windowID] = snap
	p.mu.Unlock()
	return nil
}

// EndWindow differences each domain (handling counter wraparound) and returns the
// summed host energy in joules.
func (p *RAPLProvider) EndWindow(_ context.Context, windowID string) (float64, error) {
	if p.unavailReason != "" {
		return 0, p.unavail()
	}
	p.mu.Lock()
	start, ok := p.windows[windowID]
	delete(p.windows, windowID)
	p.mu.Unlock()
	if !ok {
		return 0, fmt.Errorf("rapl window %q not found", windowID)
	}

	var totalUJ uint64
	for _, d := range p.domains {
		s, ok := start[d.name]
		if !ok {
			continue
		}
		e, err := readUint(d.energyPath)
		if err != nil {
			return 0, fmt.Errorf("rapl EndWindow read %s: %w", d.energyPath, err)
		}
		totalUJ += windowDelta(s, e, d.maxRangeUJ)
	}
	// Convert microjoules to joules at the boundary, not before.
	return float64(totalUJ) / 1e6, nil
}

// windowDelta returns end-start, correcting for a single counter wrap. energy_uj
// is a uint64 that resets to 0 after reaching max_energy_range_uj; at high power
// the counter can wrap within a window. A naive end-start underflows to a huge
// value on wrap — the most likely silent correctness bug in this provider.
//
// It assumes at most one wrap per window, which holds for the 10–30 s windows the
// agent uses against the ~65 kJ–262 kJ ranges typical of package/dram counters.
func windowDelta(start, end, maxRange uint64) uint64 {
	if end >= start {
		return end - start
	}
	// Wrapped. Recover only if we know the range the counter wraps at.
	if maxRange > start {
		return (maxRange - start) + end
	}
	// No usable range — cannot recover this window's delta. Drop it (0) rather
	// than emit a garbage negative-turned-huge value.
	return 0
}

// IdlePower samples the counters over a short dwell and returns average watts.
// RAPL exposes an energy counter, not an instantaneous power reading, so power is
// derived by differencing over a brief interval.
func (p *RAPLProvider) IdlePower(ctx context.Context) (float64, error) {
	if p.unavailReason != "" {
		return 0, p.unavail()
	}
	const dwell = 200 * time.Millisecond
	first, err := p.sample()
	if err != nil {
		return 0, err
	}
	select {
	case <-time.After(dwell):
	case <-ctx.Done():
		return 0, ctx.Err()
	}
	second, err := p.sample()
	if err != nil {
		return 0, err
	}
	var totalUJ uint64
	for _, d := range p.domains {
		totalUJ += windowDelta(first[d.name], second[d.name], d.maxRangeUJ)
	}
	joules := float64(totalUJ) / 1e6
	return joules / dwell.Seconds(), nil
}

func (p *RAPLProvider) sample() (map[string]uint64, error) {
	m := make(map[string]uint64, len(p.domains))
	for _, d := range p.domains {
		v, err := readUint(d.energyPath)
		if err != nil {
			return nil, fmt.Errorf("rapl sample read %s: %w", d.energyPath, err)
		}
		m[d.name] = v
	}
	return m, nil
}

// Domains enumerates the measurable host energy domains.
func (p *RAPLProvider) Domains(_ context.Context) ([]provider.Device, error) {
	if p.unavailReason != "" {
		return nil, p.unavail()
	}
	devs := make([]provider.Device, 0, len(p.domains))
	for _, d := range p.domains {
		typ := "cpu"
		if d.name == "dram" {
			typ = "dram"
		}
		devs = append(devs, provider.Device{ID: d.name, Name: "RAPL " + d.name, Type: typ})
	}
	return devs, nil
}

// Available reports whether usable RAPL telemetry was found at construction.
func (p *RAPLProvider) Available(_ context.Context) bool { return p.unavailReason == "" }

// Name returns "rapl".
func (p *RAPLProvider) Name() string { return "rapl" }

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
