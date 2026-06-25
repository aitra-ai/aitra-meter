//go:build !(linux && cgo && amd)

// This stub stands in for the AMD SMI energy provider on builds that do not
// include the cgo implementation. The real provider (amd.go) links against
// libamd_smi.so and needs the AMD SMI headers at compile time, so it is gated
// behind the `amd` build tag and only compiled on ROCm nodes:
//
//	go build -tags amd ./...
//
// Without that tag the package still registers "amd" so the provider name is
// recognised, but selecting it returns a clear error instead of failing the
// build everywhere AMD SMI is unavailable (CI, non-ROCm hosts).
package amd

import (
	"fmt"

	"github.com/aitra-ai/aitra-meter/internal/provider"
)

func init() {
	provider.RegisterEnergy("amd", func(map[string]string) (provider.EnergyProvider, error) {
		return nil, fmt.Errorf("amd energy provider not built: rebuild with -tags amd on a ROCm node with AMD SMI installed")
	})
}
