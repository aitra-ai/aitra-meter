//go:build linux

package gracehwmon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/aitra-ai/aitra-meter/internal/provider"
)

// writeRail creates <base>/<hwmon>/power<N>_{oem_info,average}.
func writeRail(t *testing.T, base, hwmon string, n int, label string, avgUW uint64) {
	t.Helper()
	d := filepath.Join(base, hwmon)
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	oem := filepath.Join(d, "power"+strconv.Itoa(n)+"_oem_info")
	avg := filepath.Join(d, "power"+strconv.Itoa(n)+"_average")
	if err := os.WriteFile(oem, []byte(label), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(avg, []byte(strconv.FormatUint(avgUW, 10)), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDiscoverSelectsMatchingRails(t *testing.T) {
	base := t.TempDir()
	// Two CPU rails (both sockets) plus a non-matching module rail.
	writeRail(t, base, "hwmon0", 1, "CPU Power Socket 0", 40_000_000) // 40 W
	writeRail(t, base, "hwmon0", 2, "CPU Power Socket 1", 35_000_000) // 35 W
	writeRail(t, base, "hwmon0", 3, "Module Power Socket 0", 90_000_000)

	p := newGrace(base, defaultRailMatch)
	if !p.Available(context.Background()) {
		t.Fatalf("expected available, got: %s", p.unavailReason)
	}
	domains, err := p.Domains(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(domains) != 2 {
		t.Fatalf("want 2 CPU rails, got %d: %+v", len(domains), domains)
	}
	// IdlePower sums the two CPU rails: 40 + 35 = 75 W. Module rail excluded.
	w, err := p.IdlePower(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if w < 74.999 || w > 75.001 {
		t.Fatalf("IdlePower = %v W, want 75", w)
	}
}

func TestUnavailableWhenNoRails(t *testing.T) {
	p := newGrace(t.TempDir(), defaultRailMatch)
	if p.Available(context.Background()) {
		t.Fatal("expected unavailable with no matching rails")
	}
	if err := p.BeginWindow(context.Background(), "w"); !errors.Is(err, provider.ErrHostEnergyUnavailable) {
		t.Fatalf("BeginWindow err = %v, want ErrHostEnergyUnavailable", err)
	}
	if _, err := p.EndWindow(context.Background(), "w"); !errors.Is(err, provider.ErrHostEnergyUnavailable) {
		t.Fatalf("EndWindow err = %v, want ErrHostEnergyUnavailable", err)
	}
}
