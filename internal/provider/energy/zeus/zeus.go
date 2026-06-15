// Package zeus provides an EnergyProvider backed by the Zeus ML energy
// measurement library. Zeus runs as a Python sidecar container in the same
// DaemonSet pod and communicates over a Unix domain socket.
//
// The sidecar exposes a minimal JSON-RPC interface over the socket:
//   {"method":"begin_window","id":"<windowID>"}
//   {"method":"end_window","id":"<windowID>"}  -> {"joules": <float>}
//   {"method":"idle_power"}                    -> {"watts": <float>}
//   {"method":"devices"}                       -> {"devices": [...]}
//
// The socket path defaults to /tmp/zeus.sock and is shared between the
// measurement-agent container and the zeus-sidecar container via an emptyDir
// volume mount.
package zeus

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"time"

	"github.com/aitra-ai/aitra-meter/internal/provider"
)

const (
	defaultSocketPath    = "/tmp/zeus.sock"
	defaultDialTimeout   = 5 * time.Second
	defaultRPCTimeout    = 10 * time.Second
)

func init() {
	provider.RegisterEnergy("zeus", func(config map[string]string) (provider.EnergyProvider, error) {
		socketPath := config["socket_path"]
		if socketPath == "" {
			socketPath = defaultSocketPath
		}
		return &ZeusProvider{socketPath: socketPath}, nil
	})
}

// ZeusProvider implements provider.EnergyProvider via Unix socket IPC
// to a Zeus Python sidecar container.
type ZeusProvider struct {
	socketPath string
}

func (z *ZeusProvider) Name() string { return "zeus" }

// BeginWindow tells the Zeus sidecar to begin an energy measurement window.
func (z *ZeusProvider) BeginWindow(ctx context.Context, windowID string) error {
	_, err := z.call(ctx, map[string]any{
		"method": "begin_window",
		"id":     windowID,
	})
	return err
}

// EndWindow ends the measurement window and returns joules consumed since BeginWindow.
func (z *ZeusProvider) EndWindow(ctx context.Context, windowID string) (float64, error) {
	resp, err := z.call(ctx, map[string]any{
		"method": "end_window",
		"id":     windowID,
	})
	if err != nil {
		return 0, err
	}
	joules, ok := resp["joules"].(float64)
	if !ok {
		return 0, fmt.Errorf("zeus: end_window response missing joules field")
	}
	return joules, nil
}

// IdlePower returns current GPU power draw in watts with no active requests.
func (z *ZeusProvider) IdlePower(ctx context.Context) (float64, error) {
	resp, err := z.call(ctx, map[string]any{"method": "idle_power"})
	if err != nil {
		return 0, err
	}
	watts, ok := resp["watts"].(float64)
	if !ok {
		return 0, fmt.Errorf("zeus: idle_power response missing watts field")
	}
	return watts, nil
}

// Devices returns the list of measurable GPU devices on this node.
func (z *ZeusProvider) Devices(ctx context.Context) ([]provider.Device, error) {
	resp, err := z.call(ctx, map[string]any{"method": "devices"})
	if err != nil {
		return nil, err
	}
	raw, ok := resp["devices"].([]any)
	if !ok {
		return nil, fmt.Errorf("zeus: devices response missing devices field")
	}
	devices := make([]provider.Device, 0, len(raw))
	for _, item := range raw {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		devices = append(devices, provider.Device{
			ID:   fmt.Sprintf("%v", m["id"]),
			Name: fmt.Sprintf("%v", m["name"]),
			Type: "gpu",
		})
	}
	return devices, nil
}

// call sends a JSON-RPC request to the Zeus sidecar over the Unix socket
// and returns the parsed response.
func (z *ZeusProvider) call(ctx context.Context, req map[string]any) (map[string]any, error) {
	dialCtx, cancel := context.WithTimeout(ctx, defaultDialTimeout)
	defer cancel()

	var d net.Dialer
	conn, err := d.DialContext(dialCtx, "unix", z.socketPath)
	if err != nil {
		return nil, fmt.Errorf("zeus: dial %s: %w", z.socketPath, err)
	}
	defer conn.Close() //nolint:errcheck

	deadline := time.Now().Add(defaultRPCTimeout)
	if dl, ok := ctx.Deadline(); ok && dl.Before(deadline) {
		deadline = dl
	}
	if err := conn.SetDeadline(deadline); err != nil {
		return nil, fmt.Errorf("zeus: set deadline: %w", err)
	}

	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return nil, fmt.Errorf("zeus: encode request: %w", err)
	}

	var resp map[string]any
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		return nil, fmt.Errorf("zeus: decode response: %w", err)
	}
	if errMsg, ok := resp["error"].(string); ok && errMsg != "" {
		return nil, fmt.Errorf("zeus: sidecar error: %s", errMsg)
	}
	return resp, nil
}
