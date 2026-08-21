//go:build linux

package rapl

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/aitra-ai/aitra-meter/internal/provider"
)

// writeDomain creates <base>/<dir>/{name,energy_uj,max_energy_range_uj}.
func writeDomain(t *testing.T, base, dir, name string, energyUJ, maxRange uint64) string {
	t.Helper()
	d := filepath.Join(base, dir)
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(f, v string) {
		if err := os.WriteFile(filepath.Join(d, f), []byte(v), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("name", name)
	write("energy_uj", itoa(energyUJ))
	write("max_energy_range_uj", itoa(maxRange))
	return filepath.Join(d, "energy_uj")
}

func itoa(v uint64) string { return strconv.FormatUint(v, 10) }

// fixture builds a two-domain tree: package-0 and its dram subdomain, plus a
// core subdomain that MUST be ignored (it is a subset of package).
func fixture(t *testing.T) (base, pkgEnergy, dramEnergy string) {
	t.Helper()
	base = t.TempDir()
	pkgEnergy = writeDomain(t, base, "intel-rapl:0", "package-0", 1_000_000, 262_143_328_850)
	dramEnergy = writeDomain(t, base, "intel-rapl:0:0", "dram", 500_000, 65_712_999_613)
	// core is a subset of package — enumerating it would double-count.
	writeDomain(t, base, "intel-rapl:0:1", "core", 400_000, 262_143_328_850)
	return
}

func setEnergy(t *testing.T, path string, v uint64) {
	t.Helper()
	if err := os.WriteFile(path, []byte(itoa(v)), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDiscoverSelectsPackageAndDramOnly(t *testing.T) {
	base, _, _ := fixture(t)
	p := newRAPL(base)
	if !p.Available(context.Background()) {
		t.Fatalf("expected available, got unavailable: %s", p.unavailReason)
	}
	domains, err := p.Domains(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(domains) != 2 {
		t.Fatalf("want 2 domains (package-0, dram), got %d: %+v", len(domains), domains)
	}
	names := map[string]bool{}
	for _, d := range domains {
		names[d.ID] = true
	}
	if !names["package-0"] || !names["dram"] {
		t.Fatalf("missing expected domains: %+v", names)
	}
	if names["core"] {
		t.Fatal("core domain must be excluded (subset of package)")
	}
}

func TestEndWindowSumsPackagePlusDram(t *testing.T) {
	base, pkg, dram := fixture(t)
	p := newRAPL(base)
	ctx := context.Background()
	if err := p.BeginWindow(ctx, "w1"); err != nil {
		t.Fatal(err)
	}
	// package advances 2_000_000 uJ, dram advances 300_000 uJ.
	setEnergy(t, pkg, 3_000_000)
	setEnergy(t, dram, 800_000)
	j, err := p.EndWindow(ctx, "w1")
	if err != nil {
		t.Fatal(err)
	}
	// (2_000_000 + 300_000) uJ = 2.3 J
	if got, want := j, 2.3; !approx(got, want) {
		t.Fatalf("host joules = %v, want %v", got, want)
	}
}

func TestWraparound(t *testing.T) {
	base := t.TempDir()
	const maxRange = 262_143_328_850
	pkg := writeDomain(t, base, "intel-rapl:0", "package-0", maxRange-100_000, maxRange)
	p := newRAPL(base)
	ctx := context.Background()
	if err := p.BeginWindow(ctx, "w"); err != nil {
		t.Fatal(err)
	}
	// Counter wraps: it was 100_000 below max, advances 250_000 uJ, so it lands at
	// 150_000 after wrapping through 0. A naive end-start would be a huge negative.
	setEnergy(t, pkg, 150_000)
	j, err := p.EndWindow(ctx, "w")
	if err != nil {
		t.Fatal(err)
	}
	// True delta = 100_000 + 150_000 = 250_000 uJ = 0.25 J
	if got, want := j, 0.25; !approx(got, want) {
		t.Fatalf("wraparound host joules = %v, want %v", got, want)
	}
}

func TestWindowDeltaUnit(t *testing.T) {
	cases := []struct {
		start, end, max, want uint64
	}{
		{100, 250, 1000, 150}, // no wrap
		{900, 150, 1000, 250}, // wrap: (1000-900)+150
		{900, 150, 0, 0},      // wrap with no range info -> drop
		{0, 0, 1000, 0},       // idle
		{500, 500, 1000, 0},   // no change
	}
	for _, c := range cases {
		if got := windowDelta(c.start, c.end, c.max); got != c.want {
			t.Errorf("windowDelta(%d,%d,%d)=%d want %d", c.start, c.end, c.max, got, c.want)
		}
	}
}

func TestUnavailableWhenPathAbsent(t *testing.T) {
	p := newRAPL(filepath.Join(t.TempDir(), "does-not-exist"))
	if p.Available(context.Background()) {
		t.Fatal("expected unavailable for absent powercap path")
	}
	// Every read returns ErrHostEnergyUnavailable, never a zero reading.
	if err := p.BeginWindow(context.Background(), "w"); !errors.Is(err, provider.ErrHostEnergyUnavailable) {
		t.Fatalf("BeginWindow err = %v, want ErrHostEnergyUnavailable", err)
	}
	if _, err := p.EndWindow(context.Background(), "w"); !errors.Is(err, provider.ErrHostEnergyUnavailable) {
		t.Fatalf("EndWindow err = %v, want ErrHostEnergyUnavailable", err)
	}
	if _, err := p.IdlePower(context.Background()); !errors.Is(err, provider.ErrHostEnergyUnavailable) {
		t.Fatalf("IdlePower err = %v, want ErrHostEnergyUnavailable", err)
	}
}

func TestUnavailableWhenEnergyUnreadable(t *testing.T) {
	base := t.TempDir()
	// A domain whose energy_uj is a directory, not a file — read fails, so the
	// domain is unusable and the provider must report unavailable, not crash and
	// not zero.
	d := filepath.Join(base, "intel-rapl:0")
	if err := os.MkdirAll(filepath.Join(d, "energy_uj"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d, "name"), []byte("package-0"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := newRAPL(base)
	if p.Available(context.Background()) {
		t.Fatal("expected unavailable when energy_uj is unreadable")
	}
}

func approx(a, b float64) bool {
	d := a - b
	if d < 0 {
		d = -d
	}
	return d < 1e-9
}

// TestEndWindowMultiSocketDramCollision is the dual-socket regression: both
// sockets' DRAM subdomains carry the identical label "dram", so any snapshot
// keyed by label collides and EndWindow differences one socket's counter
// against the other's snapshot — on real hardware that produced a ~45×
// cross-counter garbage delta. Domains must be keyed by their sysfs path.
func TestEndWindowMultiSocketDramCollision(t *testing.T) {
	base := t.TempDir()
	pkg0 := writeDomain(t, base, "intel-rapl:0", "package-0", 1_000_000, 262_143_328_850)
	dram0 := writeDomain(t, base, "intel-rapl:0:0", "dram", 200_000_000_000, 262_143_328_850)
	pkg1 := writeDomain(t, base, "intel-rapl:1", "package-1", 5_000_000, 262_143_328_850)
	dram1 := writeDomain(t, base, "intel-rapl:1:0", "dram", 30_000_000, 262_143_328_850)

	p := newRAPL(base)
	if err := p.BeginWindow(context.Background(), "w"); err != nil {
		t.Fatalf("BeginWindow: %v", err)
	}
	// Advance every counter by a known amount.
	setEnergy(t, pkg0, 1_000_000+1_000)
	setEnergy(t, dram0, 200_000_000_000+2_000)
	setEnergy(t, pkg1, 5_000_000+3_000)
	setEnergy(t, dram1, 30_000_000+4_000)

	got, err := p.EndWindow(context.Background(), "w")
	if err != nil {
		t.Fatalf("EndWindow: %v", err)
	}
	want := float64(1_000+2_000+3_000+4_000) / 1e6
	if got != want {
		t.Errorf("EndWindow = %v J, want %v J (label-keyed snapshots collide across sockets)", got, want)
	}
}
