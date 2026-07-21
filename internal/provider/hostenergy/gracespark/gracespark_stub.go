//go:build !linux

// This stub stands in for the experimental grace-spark-hwmon host-energy provider
// on non-Linux builds. The real provider (gracespark.go) reads the Linux hwmon
// sysfs interface exposed by the community antheas/spark_hwmon driver and is
// build-tagged linux. Without it the package still registers "grace-spark-hwmon"
// so the name is recognised, but selecting it returns a clear error instead of
// failing the build on non-Linux hosts.
package gracespark

import (
	"fmt"

	"github.com/aitra-ai/aitra-meter/internal/provider"
)

func init() {
	provider.RegisterHostEnergy("grace-spark-hwmon", func(map[string]string) (provider.HostEnergyProvider, error) {
		return nil, fmt.Errorf("grace-spark-hwmon host energy provider is only available on linux")
	})
}
