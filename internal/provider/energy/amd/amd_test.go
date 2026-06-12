//go:build linux && cgo

package amd

import (
	"context"
	"testing"
	"time"
)

// TestAMDProviderName verifies the provider name constant.
func TestAMDProviderName(t *testing.T) {
	p := &AMDProvider{windows: make(map[string]*amdWindow)}
	if p.Name() != "amd" {
		t.Errorf("Name() = %q, want %q", p.Name(), "amd")
	}
}

// TestAMDWindowNotFound verifies EndWindow returns an error for an unknown ID.
func TestAMDWindowNotFound(t *testing.T) {
	p := &AMDProvider{windows: make(map[string]*amdWindow)}
	_, err := p.EndWindow(context.Background(), "nonexistent")
	if err == nil {
		t.Error("EndWindow with unknown windowID: expected error, got nil")
	}
}

// TestAMDWindowRemovedAfterEnd verifies window state is cleaned up after
// EndWindow, regardless of whether the hardware read succeeds.
func TestAMDWindowRemovedAfterEnd(t *testing.T) {
	p := &AMDProvider{
		windows: map[string]*amdWindow{
			"w1": {startTime: time.Now(), startEnergy: 1000.0},
		},
	}
	// EndWindow will fail (no devices) but must still remove the window.
	p.EndWindow(context.Background(), "w1") //nolint:errcheck

	p.mu.Lock()
	_, present := p.windows["w1"]
	p.mu.Unlock()
	if present {
		t.Error("window still in map after EndWindow")
	}
}

// TestAMDDevicesEmptyOnNoInit verifies Devices returns empty slice without panic
// when device list is nil.
func TestAMDDevicesEmptyOnNoInit(t *testing.T) {
	p := &AMDProvider{windows: make(map[string]*amdWindow)}
	devs, err := p.Devices(context.Background())
	if err != nil {
		t.Fatalf("Devices: unexpected error: %v", err)
	}
	if len(devs) != 0 {
		t.Errorf("expected 0 devices, got %d", len(devs))
	}
}
