//go:build linux

// Package gracespark provides an EXPERIMENTAL HostEnergyProvider for the NVIDIA
// GB10 / DGX Spark, reading the cumulative host-energy accumulator exposed by the
// community out-of-tree kernel driver antheas/spark_hwmon.
//
// NVIDIA exposes no CPU/host power telemetry on Spark and has stated it has no
// plans to (nvidia-smi reports GPU power only). The spark_hwmon driver reads the
// System Power Budget Manager (SPBM) shared memory — updated by the MediaTek SSPM
// firmware — and presents it through STANDARD hwmon sysfs: cumulative energy
// counters, live power, and per-rail channels. Because it is ordinary hwmon, this
// provider reads it the same way any other hwmon energy source would be read;
// Aitra never touches SPBM, ACPI, or the MediaTek interface directly.
//
// This provider is deliberately a distinct provider from grace-hwmon (the 72-core
// Superchip): the sysfs surface and the operational caveats differ. It is OFF by
// default and must be selected explicitly, because the driver it reads is:
//   - out-of-tree, DKMS-built, requiring MOK signing under Secure Boot;
//   - self-described by its author as "vibe coded", with an SPBM ABI still in flux;
//   - dependent on current firmware — older BIOS reports wrong CPU-channel values,
//     which this provider cannot detect (the operator must update via fwupd).
//
// Aitra does not install, sign, version-check, or otherwise manage the driver.
// That is the operator's responsibility. Aitra reads it if present and correct and
// reports Available()==false otherwise. Readings taken through this path carry the
// metric label provider="grace-spark-hwmon" (from Name()) so an operator can
// segregate experimental-path data from the supported rapl / grace-hwmon paths.
//
// Energy source: the CUMULATIVE energy counter (energyN_input), differenced across
// a window — the same pattern as NVML and RAPL. The instantaneous power channel is
// deliberately NOT integrated: the firmware runs a ~100 ms PID control loop that
// makes instantaneous power oscillate, whereas the accumulator already integrates
// correctly in firmware and is the accurate source for a window average.
//
// The "never zero" contract of this subsystem is preserved exactly (see
// provider.ErrHostEnergyUnavailable). hwmon has a specific hazard — a non-numeric
// sysfs read is easily mistaken for 0 — so an unparseable read, a driver-absent
// tree, or a counter that goes backwards within a window all become "unavailable",
// never a zero, a negative, or a wrapped-large value.
package gracespark

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
	providerName    = "grace-spark-hwmon"
	defaultBasePath = "/sys/class/hwmon"

	// defaultNameMatch is matched (case-insensitive substring) against each
	// hwmon device's "name" file to locate the chip spark_hwmon registers. The
	// exact string is not yet confirmed on hardware; it is configurable via the
	// "name" config key precisely so it can be corrected without a code change.
	defaultNameMatch = "spark"

	// defaultRailInclude / defaultRailExclude select which energy rails constitute
	// HOST energy. We sum the top-level package rail plus a DRAM-equivalent rail if
	// one is exposed separately, and exclude per-core subsets (CPU performance /
	// efficiency cores) which are already contained in the package rail — including
	// them would double-count, exactly as core/uncore are excluded in RAPL.
	defaultRailInclude = "package,dram,mem"
	defaultRailExclude = "core"

	// defaultEnergyUnit governs the divisor from the raw counter to joules. The
	// spark_hwmon accumulator is documented in millijoules; standard hwmon energy
	// is microjoules. Because the ABI is in flux this is configurable ("mj" | "uj")
	// so an operator can correct the scale on hardware without a rebuild.
	defaultEnergyUnit = "mj"
)

func init() {
	provider.RegisterHostEnergy(providerName, func(config map[string]string) (provider.HostEnergyProvider, error) {
		return newSpark(sparkConfig{
			base:       orDefault(config["path"], defaultBasePath),
			nameMatch:  orDefault(config["name"], defaultNameMatch),
			include:    splitTokens(orDefault(config["rails"], defaultRailInclude)),
			exclude:    splitTokens(orDefault(config["exclude"], defaultRailExclude)),
			energyUnit: orDefault(config["energy_unit"], defaultEnergyUnit),
		}), nil
	})
}

type sparkConfig struct {
	base       string
	nameMatch  string
	include    []string
	exclude    []string
	energyUnit string
}

// sparkRail is one selected hwmon cumulative-energy rail.
type sparkRail struct {
	label      string // from energyN_label, e.g. "package"
	energyPath string // <hwmon>/energyN_input — the cumulative counter
}

// SparkProvider implements provider.HostEnergyProvider over the spark_hwmon
// energy accumulators.
type SparkProvider struct {
	rails []sparkRail
	// toJoules divides the raw counter unit to joules (1e3 for mJ, 1e6 for µJ).
	toJoules float64
	// unavailReason is non-empty when the node has no usable spark_hwmon telemetry;
	// every read then returns ErrHostEnergyUnavailable wrapped with this reason.
	unavailReason string

	mu      sync.Mutex
	windows map[string]map[string]uint64 // windowID -> label -> start counter
}

func newSpark(cfg sparkConfig) *SparkProvider {
	div := 1e3 // millijoules
	if strings.EqualFold(cfg.energyUnit, "uj") {
		div = 1e6 // microjoules
	}
	rails, reason := discover(cfg)
	return &SparkProvider{
		rails:         rails,
		toJoules:      div,
		unavailReason: reason,
		windows:       make(map[string]map[string]uint64),
	}
}

// discover walks the hwmon tree for a device whose "name" matches the spark_hwmon
// chip, then selects its cumulative-energy rails by energyN_label. It returns a
// non-empty reason when no usable rail exists — the common case being a box where
// the operator has not installed the driver.
func discover(cfg sparkConfig) ([]sparkRail, string) {
	hwmons, err := filepath.Glob(filepath.Join(cfg.base, "hwmon*"))
	if err != nil || len(hwmons) == 0 {
		return nil, fmt.Sprintf("no hwmon devices under %s (spark_hwmon driver not loaded)", cfg.base)
	}

	nameMatch := strings.ToLower(cfg.nameMatch)
	chipFound := false
	var rails []sparkRail
	for _, h := range hwmons {
		name, err := readTrim(filepath.Join(h, "name"))
		if err != nil || !strings.Contains(strings.ToLower(name), nameMatch) {
			continue
		}
		chipFound = true
		// Enumerate energyN_label; never assume channel ordering.
		labels, _ := filepath.Glob(filepath.Join(h, "energy*_label"))
		for _, lf := range labels {
			label, err := readTrim(lf)
			if err != nil || !selectRail(label, cfg.include, cfg.exclude) {
				continue
			}
			// energy<N>_label -> energy<N>_input (the cumulative counter).
			energyPath := strings.TrimSuffix(lf, "_label") + "_input"
			// Require a clean, parseable read now so a malformed rail is dropped at
			// discovery rather than surfacing as a bad reading later.
			if _, err := readUint(energyPath); err != nil {
				continue
			}
			rails = append(rails, sparkRail{label: label, energyPath: energyPath})
		}
	}

	if !chipFound {
		return nil, fmt.Sprintf("spark_hwmon driver not loaded (no hwmon chip name matching %q under %s)", cfg.nameMatch, cfg.base)
	}
	if len(rails) == 0 {
		return nil, fmt.Sprintf("spark_hwmon chip present but no cumulative-energy rail matching %v (excluding %v) under %s — firmware/driver flux?",
			cfg.include, cfg.exclude, cfg.base)
	}
	return rails, ""
}

// selectRail reports whether a rail label is a host-energy rail: it must match an
// include token and none of the exclude tokens (case-insensitive substrings).
func selectRail(label string, include, exclude []string) bool {
	l := strings.ToLower(label)
	for _, ex := range exclude {
		if ex != "" && strings.Contains(l, ex) {
			return false
		}
	}
	for _, in := range include {
		if in != "" && strings.Contains(l, in) {
			return true
		}
	}
	return false
}

func (p *SparkProvider) unavail() error {
	return fmt.Errorf("%w: %s", provider.ErrHostEnergyUnavailable, p.unavailReason)
}

// snapshot reads every selected rail's cumulative counter. A read that does not
// parse as a number is an error (never silently 0), guarding the hwmon
// "non-number reads as zero" hazard.
func (p *SparkProvider) snapshot() (map[string]uint64, error) {
	m := make(map[string]uint64, len(p.rails))
	for _, r := range p.rails {
		v, err := readUint(r.energyPath)
		if err != nil {
			return nil, fmt.Errorf("grace-spark-hwmon read %s: %w", r.energyPath, err)
		}
		m[r.label] = v
	}
	return m, nil
}

// BeginWindow snapshots every rail's cumulative energy counter.
func (p *SparkProvider) BeginWindow(_ context.Context, windowID string) error {
	if p.unavailReason != "" {
		return p.unavail()
	}
	snap, err := p.snapshot()
	if err != nil {
		return err
	}
	p.mu.Lock()
	p.windows[windowID] = snap
	p.mu.Unlock()
	return nil
}

// EndWindow differences each rail and returns the summed host energy in joules.
//
// hwmon exposes no documented maximum for an energy counter (unlike RAPL's
// max_energy_range_uj), so the counter width at which it wraps is unknown until
// confirmed on hardware. A counter that goes BACKWARDS within a window is
// therefore treated as ErrHostEnergyUnavailable for that window — never emitted as
// a negative or a wrapped-large value. Omitting one window is the safe failure;
// emitting a garbage number is exactly what this subsystem exists to prevent.
func (p *SparkProvider) EndWindow(_ context.Context, windowID string) (float64, error) {
	if p.unavailReason != "" {
		return 0, p.unavail()
	}
	p.mu.Lock()
	start, ok := p.windows[windowID]
	delete(p.windows, windowID)
	p.mu.Unlock()
	if !ok {
		return 0, fmt.Errorf("grace-spark-hwmon window %q not found", windowID)
	}

	end, err := p.snapshot()
	if err != nil {
		return 0, err
	}

	var totalRaw uint64
	for _, r := range p.rails {
		s, ok := start[r.label]
		if !ok {
			continue
		}
		e := end[r.label]
		if e < s {
			// Counter decreased: possible wrap at an unconfirmed width, or firmware
			// flux. Omit this window rather than guess.
			return 0, fmt.Errorf("%w: rail %q counter decreased within window (%d -> %d) — "+
				"possible wrap at unconfirmed width; window omitted", provider.ErrHostEnergyUnavailable, r.label, s, e)
		}
		totalRaw += e - s
	}
	// Convert the raw counter unit to joules at the boundary, not before.
	return float64(totalRaw) / p.toJoules, nil
}

// IdlePower samples the accumulator over a short dwell and returns average watts.
// It differences the same cumulative counter used for windows rather than reading
// the oscillating instantaneous power channel. A decrease during the dwell is
// reported as unavailable, never as a zero or negative power.
func (p *SparkProvider) IdlePower(ctx context.Context) (float64, error) {
	if p.unavailReason != "" {
		return 0, p.unavail()
	}
	const dwell = 200 * time.Millisecond
	first, err := p.snapshot()
	if err != nil {
		return 0, err
	}
	select {
	case <-time.After(dwell):
	case <-ctx.Done():
		return 0, ctx.Err()
	}
	second, err := p.snapshot()
	if err != nil {
		return 0, err
	}
	var totalRaw uint64
	for _, r := range p.rails {
		s, e := first[r.label], second[r.label]
		if e < s {
			return 0, fmt.Errorf("%w: rail %q counter decreased during idle sample", provider.ErrHostEnergyUnavailable, r.label)
		}
		totalRaw += e - s
	}
	joules := float64(totalRaw) / p.toJoules
	return joules / dwell.Seconds(), nil
}

// Domains enumerates the selected cumulative-energy rails.
func (p *SparkProvider) Domains(_ context.Context) ([]provider.Device, error) {
	if p.unavailReason != "" {
		return nil, p.unavail()
	}
	devs := make([]provider.Device, 0, len(p.rails))
	for _, r := range p.rails {
		typ := "cpu"
		ll := strings.ToLower(r.label)
		if strings.Contains(ll, "dram") || strings.Contains(ll, "mem") {
			typ = "dram"
		}
		devs = append(devs, provider.Device{ID: r.label, Name: "spark " + r.label, Type: typ})
	}
	return devs, nil
}

// Available reports whether usable spark_hwmon energy rails were found at
// construction. False on a box without the (opt-in) community driver installed.
func (p *SparkProvider) Available(_ context.Context) bool { return p.unavailReason == "" }

// Name returns "grace-spark-hwmon" — carried as the provider metric label so
// experimental-path readings are distinguishable from supported providers.
func (p *SparkProvider) Name() string { return providerName }

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

func splitTokens(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.ToLower(strings.TrimSpace(p)); t != "" {
			out = append(out, t)
		}
	}
	return out
}

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
