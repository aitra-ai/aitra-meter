package taskmeter

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/aitra-ai/aitra-meter/internal/provider"
)

var (
	// ErrHardwareUnavailable is returned when host hardware energy telemetry is not available on this system.
	ErrHardwareUnavailable = errors.New("host hardware energy telemetry is unavailable")

	// ErrCounterDiscontinuity is returned when an energy counter decreases or rolls over unexpectedly.
	ErrCounterDiscontinuity = errors.New("host energy counter discontinuity or wrap detected")
)

// UserHZ is the standard Linux kernel USER_HZ constant (100 Hz = 10ms per tick on x86/ARM64).
const UserHZ = 100.0

// AttributionTarget specifies the process PID or container cgroup to attribute energy to.
type AttributionTarget struct {
	PID        int    `json:"pid,omitempty"`
	CgroupPath string `json:"cgroup_path,omitempty"`
}

// HostEnergyMeasurement is the structured CPU host-level physical and attributed billing for a task.
type HostEnergyMeasurement struct {
	TaskID      string    `json:"task_id"`
	PID         int       `json:"pid,omitempty"`
	CgroupPath  string    `json:"cgroup_path,omitempty"`
	StartedAt   time.Time `json:"started_at"`
	FinishedAt  time.Time `json:"finished_at"`
	DurationSec float64   `json:"duration_seconds"`

	// Physical baseline and package energy (Joules / Watts)
	IdlePowerWatts   float64 `json:"idle_power_watts"`      // Baseline resting power (P_idle)
	RawPackageJoules float64 `json:"raw_package_joules"`    // Physical total meter delta (E_end - E_start)
	BaselineJoules   float64 `json:"baseline_joules"`       // Baseline energy deducted (IdlePower * Duration)
	ActiveJoules     float64 `json:"active_dynamic_joules"` // Real dynamic energy: max(0, Raw - Baseline)

	// Workload activity and final attribution
	AgentCPURatio    float64 `json:"agent_cpu_ratio"`  // Workload active time / Total System Active Time
	AttributedJoules float64 `json:"agent_cpu_joules"` // Final attributed CPU joules: ActiveJoules * AgentCPURatio

	Status string `json:"status"` // "ok" | "warning_low_power" | "process_not_found" | "cgroup_error"
}

// taskSession tracks an in-flight measurement window for an agent task.
type taskSession struct {
	TaskID          string
	PID             int
	CgroupPath      string
	UseCgroup       bool
	StartedAt       time.Time
	IdlePowerWatts  float64
	StartSysTicks   uint64
	StartAgentTicks uint64
	StartCgroupUsec uint64
	ProcessFound    bool
}

// HostTracker coordinates host energy measurement, baseline deduction, and workload CPU attribution.
type HostTracker struct {
	provider            provider.HostEnergyProvider
	reader              *ProcReader
	cgroupReader        *CgroupReader
	procRoot            string
	cgroupRoot          string
	mu                  sync.Mutex
	calibratedIdleWatts float64
	sessions            map[string]*taskSession
	now                 func() time.Time
}

// TrackerOption configures a HostTracker.
type TrackerOption func(*HostTracker)

// WithIdleBaselineWatts sets a calibrated resting idle baseline power in Watts.
// When set > 0, StartTarget uses this constant resting baseline instead of dynamically
// sampling the node power (which could be perturbed if other tasks or containers are already running).
func WithIdleBaselineWatts(watts float64) TrackerOption {
	return func(ht *HostTracker) {
		ht.calibratedIdleWatts = watts
	}
}

// WithProcFS sets a custom procfs root path (primarily for hermetic testing).
func WithProcFS(root string) TrackerOption {
	return func(ht *HostTracker) {
		ht.procRoot = root
		ht.reader = NewProcReader(root)
	}
}

// WithCgroupRoot sets a custom cgroup root path (primarily for hermetic testing).
func WithCgroupRoot(root string) TrackerOption {
	return func(ht *HostTracker) {
		ht.cgroupRoot = root
		ht.cgroupReader = NewCgroupReader(root)
	}
}

// WithNowFunc sets a custom clock function (for deterministic testing).
func WithNowFunc(fn func() time.Time) TrackerOption {
	return func(ht *HostTracker) {
		ht.now = fn
	}
}

// NewHostTracker creates a new HostTracker using the provided HostEnergyProvider.
func NewHostTracker(p provider.HostEnergyProvider, opts ...TrackerOption) *HostTracker {
	ht := &HostTracker{
		provider:     p,
		procRoot:     "/proc",
		cgroupRoot:   "/sys/fs/cgroup",
		reader:       NewProcReader("/proc"),
		cgroupReader: NewCgroupReader("/sys/fs/cgroup"),
		sessions:     make(map[string]*taskSession),
		now:          time.Now,
	}
	for _, opt := range opts {
		opt(ht)
	}
	return ht
}

// Start begins a task measurement session for a process PID (backward-compatible).
func (t *HostTracker) Start(taskID string, pid int) error {
	return t.StartTarget(taskID, AttributionTarget{PID: pid})
}

// StartTarget begins a task measurement session: locks idle power baseline, starts provider window,
// and samples initial cgroup usage or process CPU ticks.
func (t *HostTracker) StartTarget(taskID string, target AttributionTarget) error {
	ctx := context.Background()

	// Zero-Mock Fail Fast: check if hardware telemetry is available
	if t.provider == nil || !t.provider.Available(ctx) {
		return fmt.Errorf("%w: driver or device not available", ErrHardwareUnavailable)
	}

	var idleWatts float64
	if t.calibratedIdleWatts > 0 {
		idleWatts = t.calibratedIdleWatts
	} else {
		var err error
		idleWatts, err = t.provider.IdlePower(ctx)
		if err != nil {
			if errors.Is(err, provider.ErrHostEnergyUnavailable) {
				return fmt.Errorf("%w: %v", ErrHardwareUnavailable, err)
			}
			return fmt.Errorf("sample idle power: %w", err)
		}
	}

	if err := t.provider.BeginWindow(ctx, taskID); err != nil {
		if errors.Is(err, provider.ErrHostEnergyUnavailable) {
			return fmt.Errorf("%w: %v", ErrHardwareUnavailable, err)
		}
		return fmt.Errorf("begin energy window: %w", err)
	}

	// Sample system ticks
	startSysTicks, _, err := t.reader.SystemActiveTicks()
	if err != nil {
		return fmt.Errorf("sample system active ticks: %w", err)
	}

	sess := &taskSession{
		TaskID:         taskID,
		PID:            target.PID,
		CgroupPath:     target.CgroupPath,
		StartedAt:      t.now(),
		IdlePowerWatts: idleWatts,
		StartSysTicks:  startSysTicks,
	}

	if target.CgroupPath != "" {
		// Cgroup-first path: sample initial container microseconds
		startCgroupUsec, err := t.cgroupReader.ReadUsageUsec(target.CgroupPath)
		if err != nil {
			return fmt.Errorf("sample container cgroup %s: %w", target.CgroupPath, err)
		}
		sess.UseCgroup = true
		sess.StartCgroupUsec = startCgroupUsec
	} else {
		// Fallback to process PID ticks
		startAgentTicks, found, err := t.reader.ProcessTreeTicks(target.PID)
		if err != nil {
			return fmt.Errorf("sample process tree ticks: %w", err)
		}
		sess.StartAgentTicks = startAgentTicks
		sess.ProcessFound = found
	}

	t.mu.Lock()
	t.sessions[taskID] = sess
	t.mu.Unlock()

	return nil
}

// Stop closes the measurement window, performs baseline deduction, calculates workload attribution,
// and produces a HostEnergyMeasurement.
func (t *HostTracker) Stop(taskID string) (HostEnergyMeasurement, error) {
	ctx := context.Background()
	finishedAt := t.now()

	t.mu.Lock()
	sess, ok := t.sessions[taskID]
	if ok {
		delete(t.sessions, taskID)
	}
	t.mu.Unlock()

	if !ok {
		return HostEnergyMeasurement{}, fmt.Errorf("task session %q not found or already stopped", taskID)
	}

	durationSec := finishedAt.Sub(sess.StartedAt).Seconds()
	if durationSec < 0 {
		durationSec = 0
	}

	rawJoules, err := t.provider.EndWindow(ctx, taskID)
	if err != nil {
		if errors.Is(err, ErrCounterDiscontinuity) {
			return HostEnergyMeasurement{}, ErrCounterDiscontinuity
		}
		return HostEnergyMeasurement{}, fmt.Errorf("end energy window: %w", err)
	}
	if rawJoules < 0 {
		return HostEnergyMeasurement{}, fmt.Errorf("%w: raw package joules negative (%f)", ErrCounterDiscontinuity, rawJoules)
	}

	// Sample terminating system ticks
	endSysTicks, _, err := t.reader.SystemActiveTicks()
	if err != nil {
		return HostEnergyMeasurement{}, fmt.Errorf("sample end system ticks: %w", err)
	}

	// Baseline energy deduction formula: Delta E_active = max(0, Delta E_pkg - P_idle * Delta t)
	baselineJoules := sess.IdlePowerWatts * durationSec
	activeJoules := rawJoules - baselineJoules

	status := "ok"
	if activeJoules < 0 {
		activeJoules = 0
		status = "warning_low_power"
	}

	var agentRatio float64

	if sess.UseCgroup {
		// Container cgroup mode
		endCgroupUsec, err := t.cgroupReader.ReadUsageUsec(sess.CgroupPath)
		if err != nil {
			status = "cgroup_error"
			agentRatio = 0.0
		} else {
			var deltaCgroupUsec uint64
			if endCgroupUsec > sess.StartCgroupUsec {
				deltaCgroupUsec = endCgroupUsec - sess.StartCgroupUsec
			}
			deltaCgroupSec := float64(deltaCgroupUsec) / 1e6

			var deltaSysTicks uint64
			if endSysTicks > sess.StartSysTicks {
				deltaSysTicks = endSysTicks - sess.StartSysTicks
			}
			deltaSysSec := float64(deltaSysTicks) / UserHZ

			if deltaSysSec <= 0 || deltaCgroupSec <= 0 {
				agentRatio = 0.0
			} else {
				agentRatio = deltaCgroupSec / deltaSysSec
				if agentRatio > 1.0 {
					agentRatio = 1.0
				} else if agentRatio < 0.0 {
					agentRatio = 0.0
				}
			}
		}
	} else {
		// Host process PID mode
		endAgentTicks, foundEnd, err := t.reader.ProcessTreeTicks(sess.PID)
		if err != nil {
			return HostEnergyMeasurement{}, fmt.Errorf("sample end process ticks: %w", err)
		}

		if !sess.ProcessFound && !foundEnd {
			status = "process_not_found"
			agentRatio = 0.0
		} else {
			var deltaSysTicks float64
			if endSysTicks > sess.StartSysTicks {
				deltaSysTicks = float64(endSysTicks - sess.StartSysTicks)
			}

			var deltaAgentTicks float64
			if endAgentTicks > sess.StartAgentTicks {
				deltaAgentTicks = float64(endAgentTicks - sess.StartAgentTicks)
			}

			if deltaSysTicks <= 0 || deltaAgentTicks <= 0 {
				agentRatio = 0.0
			} else {
				agentRatio = deltaAgentTicks / deltaSysTicks
				if agentRatio > 1.0 {
					agentRatio = 1.0
				} else if agentRatio < 0.0 {
					agentRatio = 0.0
				}
			}
		}
	}

	attributedJoules := activeJoules * agentRatio

	return HostEnergyMeasurement{
		TaskID:           taskID,
		PID:              sess.PID,
		CgroupPath:       sess.CgroupPath,
		StartedAt:        sess.StartedAt,
		FinishedAt:       finishedAt,
		DurationSec:      durationSec,
		IdlePowerWatts:   sess.IdlePowerWatts,
		RawPackageJoules: rawJoules,
		BaselineJoules:   baselineJoules,
		ActiveJoules:     activeJoules,
		AgentCPURatio:    agentRatio,
		AttributedJoules: attributedJoules,
		Status:           status,
	}, nil
}
