//go:build !(linux && cgo)

// This stub stands in for the NVML energy provider on builds that do not
// include the cgo implementation. The real provider (nvml.go) uses go-nvml,
// which needs cgo to compile, so it is only built on linux with:
//
//	CGO_ENABLED=1 go build ./...
//
// Without cgo the package still registers "nvml" so the provider name is
// recognised, but selecting it returns a clear error instead of the
// registry's "unknown energy provider".
package nvml

import (
	"fmt"

	"github.com/aitra-ai/aitra-meter/internal/provider"
)

func init() {
	provider.RegisterEnergy("nvml", func(map[string]string) (provider.EnergyProvider, error) {
		return nil, fmt.Errorf("nvml energy provider not built: rebuild with CGO_ENABLED=1 on linux")
	})
}
