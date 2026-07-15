//go:build linux

package gracespark

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/aitra-ai/aitra-meter/internal/provider"
)

// writeChipName sets <base>/<hwmon>/name.
func writeChipName(t *testing.T, base, hwmon, name string) {
	t.Helper()
	d := filepath.Join(base, hwmon)
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d, "name"), []byte(name), 0o644); err != nil {
		t.Fatal(err)
	}
}

// writeEnergyRail creates <base>/<hwmon>/energy<N>_{label,input}.
func writeEnergyRail(t *testing.T, base, hwmon string, n int, label string, counter uint64) {
	t.Helper()
	d := filepath.Join(base, hwmon)
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	lbl := filepath.Join(d, "energy"+strconv.Itoa(n)+"_label")
	inp := filepath.Join(d, "energy"+strconv.Itoa(n)+"_input")
	if err := os.WriteFile(lbl, []byte(label), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(inp, []byte(strconv.FormatUint(counter, 10)), 0o644); err != nil {
		t.Fatal(err)
	}
}

// setCounter overwrites an existing energy<N>_input to simulate the counter
// advancing (or decreasing) between snapshots.
func setCounter(t *testing.T, base, hwmon string, n int, counter uint64) {
	t.Helper()
	inp := filepath.Join(base, hwmon, "energy"+strconv.Itoa(n)+"_input")
	if err := os.WriteFile(inp, []byte(strconv.FormatUint(counter, 10)), 0o644); err != nil {
		t.Fatal(err)
	}
}

func newTestSpark(base string) *SparkProvider {
	return newSpark(sparkConfig{
		base:       base,
		nameMatch:  defaultNameMatch,
		include:    splitTokens(defaultRailInclude),
		exclude:    splitTokens(defaultRailExclude),
		energyUnit: defaultEnergyUnit, // millijoules
	})
}

// TestWindowDeltaSumsRailsInJoules: the window delta equals the summed rail energy
// converted from millijoules to joules, and per-core subsets are excluded so the
// package rail is not double-counted.
func TestWindowDeltaSumsRailsInJoules(t *testing.T) {
	base := t.TempDir()
	writeChipName(t, base, "hwmon3", "spark")
	// package + dram are host energy; the perf/eff core rails are subsets of
	// package and must be excluded (they contain "core").
	writeEnergyRail(t, base, "hwmon3", 0, "package", 1_000_000)         // 1000 J start
	writeEnergyRail(t, base, "hwmon3", 1, "dram", 200_000)              // 200 J start
	writeEnergyRail(t, base, "hwmon3", 2, "CPU Performance Cores", 999) // excluded
	writeEnergyRail(t, base, "hwmon3", 3, "CPU Efficiency Cores", 999)  // excluded

	p := newTestSpark(base)
	if !p.Available(context.Background()) {
		t.Fatalf("expected available, got: %s", p.unavailReason)
	}
	domains, err := p.Domains(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(domains) != 2 {
		t.Fatalf("want 2 host rails (package, dram), got %d: %+v", len(domains), domains)
	}

	ctx := context.Background()
	if err := p.BeginWindow(ctx, "w1"); err != nil {
		t.Fatal(err)
	}
	// Advance: package +50_000 mJ, dram +10_000 mJ = 60_000 mJ = 60 J.
	setCounter(t, base, "hwmon3", 0, 1_050_000)
	setCounter(t, base, "hwmon3", 1, 210_000)
	// A core rail also advancing must not affect the sum.
	setCounter(t, base, "hwmon3", 2, 5_000)

	j, err := p.EndWindow(ctx, "w1")
	if err != nil {
		t.Fatal(err)
	}
	if j < 59.999 || j > 60.001 {
		t.Fatalf("EndWindow = %v J, want 60", j)
	}
}

// TestDriverAbsentIsUnavailable: no hwmon chip matching the spark name means the
// provider is unavailable and every read returns ErrHostEnergyUnavailable — never
// a zero. This is the common case on a box without the community driver.
func TestDriverAbsentIsUnavailable(t *testing.T) {
	base := t.TempDir()
	// A different chip present, but nothing named "spark".
	writeChipName(t, base, "hwmon0", "coretemp")
	writeEnergyRail(t, base, "hwmon0", 0, "package", 500_000)

	p := newTestSpark(base)
	if p.Available(context.Background()) {
		t.Fatal("expected unavailable when no spark chip present")
	}
	if err := p.BeginWindow(context.Background(), "w"); !errors.Is(err, provider.ErrHostEnergyUnavailable) {
		t.Fatalf("BeginWindow err = %v, want ErrHostEnergyUnavailable", err)
	}
	if j, err := p.EndWindow(context.Background(), "w"); !errors.Is(err, provider.ErrHostEnergyUnavailable) || j != 0 {
		t.Fatalf("EndWindow = (%v, %v), want (0, ErrHostEnergyUnavailable)", j, err)
	}
}

// TestMalformedReadNeverZero: a malformed (non-numeric / empty) counter must not
// be read as 0. At discovery it drops the rail; if it were the only rail the
// provider is unavailable rather than reporting a zero reading.
func TestMalformedReadNeverZero(t *testing.T) {
	base := t.TempDir()
	writeChipName(t, base, "hwmon3", "spark")
	// Non-numeric content — the exact hwmon "non-number is 0" hazard.
	d := filepath.Join(base, "hwmon3")
	if err := os.WriteFile(filepath.Join(d, "energy0_label"), []byte("package"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d, "energy0_input"), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}

	p := newTestSpark(base)
	if p.Available(context.Background()) {
		t.Fatal("expected unavailable when the only rail is unreadable — must not report a zero rail")
	}
	if _, err := p.EndWindow(context.Background(), "w"); !errors.Is(err, provider.ErrHostEnergyUnavailable) {
		t.Fatalf("EndWindow err = %v, want ErrHostEnergyUnavailable", err)
	}
}

// TestCounterDecreaseIsUnavailable: a counter that goes backwards within a window
// (possible wrap at an unconfirmed width) must yield ErrHostEnergyUnavailable for
// that window — never a negative and never a wrapped-large value.
func TestCounterDecreaseIsUnavailable(t *testing.T) {
	base := t.TempDir()
	writeChipName(t, base, "hwmon3", "spark")
	writeEnergyRail(t, base, "hwmon3", 0, "package", 1_000_000)

	p := newTestSpark(base)
	ctx := context.Background()
	if err := p.BeginWindow(ctx, "w"); err != nil {
		t.Fatal(err)
	}
	setCounter(t, base, "hwmon3", 0, 900_000) // decreased

	j, err := p.EndWindow(ctx, "w")
	if !errors.Is(err, provider.ErrHostEnergyUnavailable) {
		t.Fatalf("EndWindow err = %v, want ErrHostEnergyUnavailable on decrease", err)
	}
	if j != 0 {
		t.Fatalf("EndWindow joules = %v, want 0 (never negative, never wrapped-large)", j)
	}
}

// TestProviderLabel: the provider identifies as grace-spark-hwmon so experimental
// -path readings carry a distinct provider metric label.
func TestProviderLabel(t *testing.T) {
	p := newTestSpark(t.TempDir())
	if got := p.Name(); got != "grace-spark-hwmon" {
		t.Fatalf("Name() = %q, want grace-spark-hwmon", got)
	}
}

// TestMicrojouleUnitConfig: energy_unit=uj switches the divisor to 1e6.
func TestMicrojouleUnitConfig(t *testing.T) {
	base := t.TempDir()
	writeChipName(t, base, "hwmon3", "spark")
	writeEnergyRail(t, base, "hwmon3", 0, "package", 0)

	p := newSpark(sparkConfig{
		base:       base,
		nameMatch:  defaultNameMatch,
		include:    splitTokens(defaultRailInclude),
		exclude:    splitTokens(defaultRailExclude),
		energyUnit: "uj",
	})
	ctx := context.Background()
	if err := p.BeginWindow(ctx, "w"); err != nil {
		t.Fatal(err)
	}
	setCounter(t, base, "hwmon3", 0, 6_000_000) // +6e6 µJ = 6 J

	j, err := p.EndWindow(ctx, "w")
	if err != nil {
		t.Fatal(err)
	}
	if j < 5.999 || j > 6.001 {
		t.Fatalf("EndWindow = %v J, want 6 (µJ unit)", j)
	}
}
