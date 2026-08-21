//go:build !linux

// This stub stands in for the RAPL host-energy provider on non-Linux builds.
// RAPL is a Linux powercap sysfs interface (/sys/class/powercap), so the real
// provider (rapl.go) is build-tagged linux. Without it the package still
// registers "rapl" so the name is recognised, but selecting it returns a clear
// error instead of failing the build on non-Linux hosts.
package rapl

import (
	"fmt"

	"github.com/aitra-ai/aitra-meter/internal/provider"
)

func init() {
	provider.RegisterHostEnergy("rapl", func(map[string]string) (provider.HostEnergyProvider, error) {
		return nil, fmt.Errorf("rapl host energy provider is only available on linux")
	})
}
