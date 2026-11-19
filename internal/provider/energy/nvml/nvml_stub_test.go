//go:build !(linux && cgo)

package nvml

import (
	"strings"
	"testing"

	"github.com/aitra-ai/aitra-meter/internal/provider"
)

// TestStubRegisteredWithRegistry verifies a build without cgo still
// recognises the "nvml" name and tells the operator how to get a working
// provider, instead of failing with "unknown energy provider".
func TestStubRegisteredWithRegistry(t *testing.T) {
	_, err := provider.NewEnergy("nvml", nil)
	if err == nil {
		t.Fatal("NewEnergy(nvml) on a build without cgo: expected error, got nil")
	}
	if !strings.Contains(err.Error(), "CGO_ENABLED=1") {
		t.Errorf("NewEnergy(nvml) error = %q, want it to mention CGO_ENABLED=1", err)
	}
}
