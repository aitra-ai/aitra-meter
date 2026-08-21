//go:build !linux

// This stub stands in for the Grace hwmon host-energy provider on non-Linux
// builds. The real provider (gracehwmon.go) reads the Linux hwmon sysfs interface
// and is build-tagged linux. Without it the package still registers "grace-hwmon"
// so the name is recognised, but selecting it returns a clear error instead of
// failing the build on non-Linux hosts.
package gracehwmon

import (
	"fmt"

	"github.com/aitra-ai/aitra-meter/internal/provider"
)

func init() {
	provider.RegisterHostEnergy("grace-hwmon", func(map[string]string) (provider.HostEnergyProvider, error) {
		return nil, fmt.Errorf("grace-hwmon host energy provider is only available on linux")
	})
}
