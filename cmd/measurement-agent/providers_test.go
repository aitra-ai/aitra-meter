package main

import (
	"strings"
	"testing"

	"github.com/aitra-ai/aitra-meter/internal/provider"
)

// TestEnergyProvidersRegistered verifies that every energy provider the
// --energy-provider flag advertises, including the default, is linked into
// the agent binary. A provider whose package is not imported never runs its
// init() and the agent exits at startup with "unknown energy provider".
func TestEnergyProvidersRegistered(t *testing.T) {
	for _, name := range []string{"nvml", "amd", "zeus", "dcgm", "kepler"} {
		t.Run(name, func(t *testing.T) {
			// The factory may fail for its own reasons (no GPU, no endpoint,
			// stub build); only an unregistered name is a failure here.
			_, err := provider.NewEnergy(name, map[string]string{})
			if err != nil && strings.Contains(err.Error(), "unknown energy provider") {
				t.Fatalf("energy provider %q is not registered: %v", name, err)
			}
		})
	}
}
