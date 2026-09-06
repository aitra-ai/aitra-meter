// Package agent implements the per-node measurement loop.
//
// The Loop ticks at a configurable interval (default 30 s). Each tick:
//  1. Reads the current cumulative output-token counter from the inference provider.
//  2. Computes the delta since the previous tick.
//  3. Begins an energy window at tick start, ends it at tick end.
//  4. Sends a WindowReport to the aggregation service via gRPC.
//
// Idle detection: when RequestsRunning == 0 for the full window, the window
// is still sent with the measured joules and zero token delta so the aggregation
// service can record idle power. The aggregation service rejects zero-token
// windows (accepted=false) without error; those are effectively idle samples.
//
// The Loop is designed for a single GPU node. For multi-GPU nodes, run one
// Loop per GPU, passing different windowID prefixes and Device IDs.
package agent

import (
	"context"
	"fmt"
	"time"

	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	measurementv1 "github.com/aitra-ai/aitra-meter/api/proto/measurement/v1"
	"github.com/aitra-ai/aitra-meter/internal/provider"
)

const (
	// DefaultWindowDuration is the default measurement window length.
	DefaultWindowDuration = 30 * time.Second

	// DefaultIdleTimeout is how long with RequestsRunning==0 before the loop
	// logs an idle notice. Does not suppress reporting.
	DefaultIdleTimeout = 5 * time.Minute
)

// Config holds parameters for the Loop.
type Config struct {
	// Node is the Kubernetes node name, added to every WindowReport.
	Node string

	// AggregatorAddr is the gRPC address of the aggregation service.
	AggregatorAddr string

	// WindowDuration controls how often a WindowReport is sent.
	// Defaults to DefaultWindowDuration.
	WindowDuration time.Duration

	// EnergyProvider and InferenceProvider are the measurement backends.
	EnergyProvider    provider.EnergyProvider
	InferenceProvider provider.InferenceMetricsProvider

	// HostEnergyProvider measures non-accelerator (CPU/DRAM/board) energy. It is
	// optional: when nil, or when it reports Available()==false, host energy is
	// omitted from every WindowReport — the field is left unset (not zero) so the
	// aggregation service can distinguish "not measured" from "measured zero".
	HostEnergyProvider provider.HostEnergyProvider
}

// Loop runs the measurement loop for a single node.
type Loop struct {
	cfg    Config
	log    *zap.Logger
	client measurementv1.MeasurementServiceClient
	conn   *grpc.ClientConn

	// prevTokens holds the last cumulative token count so we can compute deltas.
	prevTokens uint64
	// seenFirstToken is false until we get at least one successful OutputTokens read.
	seenFirstToken bool
	// windowSeq is a monotonic counter for window IDs.
	windowSeq uint64

	// hostAvailable is resolved once at construction from HostEnergyProvider
	// .Available(). When false, no host window is opened and no host field is set.
	hostAvailable bool
	// hostBegun records whether the current window's host BeginWindow succeeded,
	// so reportWindow only ends a window it actually started. The loop is a single
	// goroutine (begin → wait → report), so a plain field is safe here.
	hostBegun bool
}

// New creates a Loop and dials the aggregation service. Call Run to start.
func New(cfg Config, log *zap.Logger) (*Loop, error) {
	if cfg.WindowDuration <= 0 {
		cfg.WindowDuration = DefaultWindowDuration
	}
	if cfg.Node == "" {
		return nil, fmt.Errorf("agent.Config.Node is required")
	}
	if cfg.AggregatorAddr == "" {
		return nil, fmt.Errorf("agent.Config.AggregatorAddr is required")
	}

	conn, err := grpc.NewClient(
		cfg.AggregatorAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		return nil, fmt.Errorf("dial aggregation service %q: %w", cfg.AggregatorAddr, err)
	}

	l := &Loop{
		cfg:    cfg,
		log:    log,
		client: measurementv1.NewMeasurementServiceClient(conn),
		conn:   conn,
	}

	// Resolve host-energy capability exactly once, and log it once at INFO — not
	// per window (that is log spam that gets filtered, defeating the purpose).
	if cfg.HostEnergyProvider != nil {
		l.hostAvailable = cfg.HostEnergyProvider.Available(context.Background())
		if l.hostAvailable {
			log.Info("host energy measurement enabled",
				zap.String("host_provider", cfg.HostEnergyProvider.Name()))
		} else {
			reason := "provider reports no host telemetry on this hardware"
			if n, ok := cfg.HostEnergyProvider.(*provider.NoopHostEnergy); ok {
				reason = n.Reason
			}
			log.Info("host energy unavailable — system J/token will be omitted, never zeroed",
				zap.String("host_provider", cfg.HostEnergyProvider.Name()),
				zap.String("reason", reason))
		}
	}

	return l, nil
}

// Close releases the gRPC connection.
func (l *Loop) Close() error {
	return l.conn.Close()
}

// Run starts the measurement loop and blocks until ctx is cancelled.
// It sends one WindowReport per window duration.
func (l *Loop) Run(ctx context.Context) {
	ticker := time.NewTicker(l.cfg.WindowDuration)
	defer ticker.Stop()

	l.log.Info("measurement loop started",
		zap.String("node", l.cfg.Node),
		zap.String("energy_provider", l.cfg.EnergyProvider.Name()),
		zap.String("inference_provider", l.cfg.InferenceProvider.Name()),
		zap.Duration("window", l.cfg.WindowDuration),
	)

	for {
		// Begin energy window at the start of each tick period.
		windowID := l.nextWindowID()
		if err := l.cfg.EnergyProvider.BeginWindow(ctx, windowID); err != nil {
			l.log.Warn("BeginWindow failed — skipping tick",
				zap.String("window_id", windowID),
				zap.Error(err),
			)
			// Wait for next tick rather than hammering a failing provider.
			select {
			case <-ticker.C:
			case <-ctx.Done():
				return
			}
			continue
		}

		// Open the host-energy window alongside the GPU window. A host failure
		// never affects the GPU path: we simply omit host energy for this window.
		l.hostBegun = false
		if l.hostAvailable {
			if err := l.cfg.HostEnergyProvider.BeginWindow(ctx, windowID); err != nil {
				l.log.Debug("host BeginWindow failed — host energy omitted this window",
					zap.String("window_id", windowID), zap.Error(err))
			} else {
				l.hostBegun = true
			}
		}

		// Wait for the window to elapse.
		select {
		case <-ticker.C:
		case <-ctx.Done():
			// Drain the current window before exiting so energy isn't lost.
			// The main context is cancelled; use a short-lived background context
			// so the final EndWindow + gRPC send can complete cleanly.
			drainCtx, drainCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer drainCancel()
			l.reportWindow(drainCtx, windowID)
			return
		}

		l.reportWindow(ctx, windowID)
	}
}

// reportWindow ends the current energy window and sends the WindowReport.
func (l *Loop) reportWindow(ctx context.Context, windowID string) {
	joules, err := l.cfg.EnergyProvider.EndWindow(ctx, windowID)
	if err != nil {
		l.log.Warn("EndWindow failed — dropping window",
			zap.String("window_id", windowID),
			zap.Error(err),
		)
		return
	}

	// Read inference metrics.
	modelName, _ := l.cfg.InferenceProvider.ModelName(ctx)
	currTokens, tokErr := l.cfg.InferenceProvider.OutputTokens(ctx)
	running, _ := l.cfg.InferenceProvider.RequestsRunning(ctx)

	var tokenDelta uint64
	if tokErr == nil {
		if l.seenFirstToken && currTokens >= l.prevTokens {
			tokenDelta = currTokens - l.prevTokens
		}
		l.prevTokens = currTokens
		l.seenFirstToken = true
	} else {
		l.log.Warn("OutputTokens read failed", zap.Error(tokErr))
	}

	powerWatts := joules / l.cfg.WindowDuration.Seconds()

	report := &measurementv1.WindowReport{
		WindowId:          windowID,
		Node:              l.cfg.Node,
		ModelName:         modelName,
		EnergyJoules:      joules,
		OutputTokens:      tokenDelta,
		PowerWatts:        powerWatts,
		EnergyProvider:    l.cfg.EnergyProvider.Name(),
		InferenceProvider: l.cfg.InferenceProvider.Name(),
		TimestampUnixMs:   time.Now().UnixMilli(),
	}

	// Close the host-energy window, if one was opened. On success the fields are
	// set (presence = measured); on failure they are left unset (nil), which the
	// aggregation service reads as "not measured" — never as a zero reading.
	if l.hostBegun {
		hostJoules, herr := l.cfg.HostEnergyProvider.EndWindow(ctx, windowID)
		if herr != nil {
			l.log.Debug("host EndWindow failed — host energy omitted this window",
				zap.String("window_id", windowID), zap.Error(herr))
		} else {
			hostPower := hostJoules / l.cfg.WindowDuration.Seconds()
			report.HostEnergyJoules = &hostJoules
			report.HostPowerWatts = &hostPower
			report.HostProvider = l.cfg.HostEnergyProvider.Name()
		}
	}

	ack, err := l.client.ReportWindow(ctx, report)
	if err != nil {
		l.log.Error("ReportWindow RPC failed",
			zap.String("window_id", windowID),
			zap.Error(err),
		)
		return
	}

	l.log.Debug("window reported",
		zap.String("window_id", windowID),
		zap.Float64("joules", joules),
		zap.Uint64("token_delta", tokenDelta),
		zap.Int("requests_running", running),
		zap.Bool("accepted", ack.Accepted),
	)

	if !ack.Accepted {
		// Zero-token window — idle. The aggregation service correctly rejects
		// these; log at debug level so idle nodes aren't noisy.
		l.log.Debug("window not accepted by aggregation service (zero tokens — idle)",
			zap.String("window_id", windowID),
		)
	}
}

func (l *Loop) nextWindowID() string {
	l.windowSeq++
	return fmt.Sprintf("%s/%s/%d", l.cfg.Node, l.cfg.InferenceProvider.Name(), l.windowSeq)
}
