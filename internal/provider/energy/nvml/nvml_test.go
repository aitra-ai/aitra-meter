//go:build linux && cgo

package nvml

import (
	"context"
	"strings"
	"testing"

	"github.com/aitra-ai/aitra-meter/internal/provider"
)

// Interface compliance is mandatory (docs/guides/writing-a-provider.md).
var _ provider.EnergyProvider = (*NVMLProvider)(nil)

func TestImplementsInterface(t *testing.T) {
	var _ provider.EnergyProvider = (*NVMLProvider)(nil)
}

// TestRegisteredWithRegistry verifies init() wired the provider into the
// registry so the agent can select it by name (-energy-provider nvml). On a
// host without the NVIDIA driver the factory fails at NVML initialisation,
// which still proves the name resolved to the real provider.
func TestRegisteredWithRegistry(t *testing.T) {
	p, err := provider.NewEnergy("nvml", nil)
	if err != nil {
		if !strings.HasPrefix(err.Error(), "nvml init:") {
			t.Fatalf("NewEnergy(nvml): %v", err)
		}
		return
	}
	if p.Name() != "nvml" {
		t.Errorf("registered provider Name() = %q, want \"nvml\"", p.Name())
	}
}

// TestWindowNotFound verifies EndWindow returns an error for an unknown
// window ID without touching the hardware.
func TestWindowNotFound(t *testing.T) {
	p := &NVMLProvider{windows: make(map[string]*window)}
	if _, err := p.EndWindow(context.Background(), "nonexistent"); err == nil {
		t.Error("EndWindow with unknown windowID: expected error, got nil")
	}
}
