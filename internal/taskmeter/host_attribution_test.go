package taskmeter

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/aitra-ai/aitra-meter/internal/provider"
)

// fakeHostEnergyProvider provides controlled energy counters for deterministic testing.
type fakeHostEnergyProvider struct {
	mu          sync.Mutex
	available   bool
	idlePower   float64
	idleErr     error
	windows     map[string]float64 // windowID -> start raw joules
	endJoules   map[string]float64 // windowID -> target delta raw joules
	endErr      map[string]error
	discontinue bool
}

func newFakeHostEnergy(idlePower float64) *fakeHostEnergyProvider {
	return &fakeHostEnergyProvider{
		available: true,
		idlePower: idlePower,
		windows:   make(map[string]float64),
		endJoules: make(map[string]float64),
		endErr:    make(map[string]error),
	}
}

func (f *fakeHostEnergyProvider) BeginWindow(_ context.Context, windowID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.available {
		return provider.ErrHostEnergyUnavailable
	}
	f.windows[windowID] = 1000.0 // arbitrary counter baseline
	return nil
}

func (f *fakeHostEnergyProvider) EndWindow(_ context.Context, windowID string) (float64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.available {
		return 0, provider.ErrHostEnergyUnavailable
	}
	if err, ok := f.endErr[windowID]; ok {
		return 0, err
	}
	if f.discontinue {
		return -10.0, ErrCounterDiscontinuity
	}
	delta, ok := f.endJoules[windowID]
	if !ok {
		return 0, fmt.Errorf("window %q not found", windowID)
	}
	return delta, nil
}

func (f *fakeHostEnergyProvider) IdlePower(_ context.Context) (float64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.available {
		return 0, provider.ErrHostEnergyUnavailable
	}
	if f.idleErr != nil {
		return 0, f.idleErr
	}
	return f.idlePower, nil
}

func (f *fakeHostEnergyProvider) Domains(_ context.Context) ([]provider.Device, error) {
	return nil, nil
}

func (f *fakeHostEnergyProvider) Available(_ context.Context) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.available
}

func (f *fakeHostEnergyProvider) Name() string {
	return "fake-host-energy"
}

// helper to write synthetic /proc/stat
func writeProcStat(t *testing.T, procDir string, user, nice, system, idle, iowait, irq, softirq, steal uint64) {
	t.Helper()
	line := fmt.Sprintf("cpu  %d %d %d %d %d %d %d %d 0 0\n",
		user, nice, system, idle, iowait, irq, softirq, steal)
	err := os.WriteFile(filepath.Join(procDir, "stat"), []byte(line), 0o644)
	if err != nil {
		t.Fatalf("failed to write proc stat: %v", err)
	}
}

// helper to write synthetic /proc/<pid>/stat
func writeProcPidStat(t *testing.T, procDir string, pid int, comm string, ppid int, utime, stime, cutime, cstime uint64) {
	t.Helper()
	pidDir := filepath.Join(procDir, strconv.Itoa(pid))
	if err := os.MkdirAll(pidDir, 0o755); err != nil {
		t.Fatalf("failed to mkdir pid dir: %v", err)
	}
	// /proc/<pid>/stat has at least 17 fields:
	// pid (1), comm (2), state (3), ppid (4), pgrp (5), session (6), tty_nr (7), tpgid (8), flags (9), minflt (10), cminflt (11), majflt (12), cmajflt (13), utime (14), stime (15), cutime (16), cstime (17)
	content := fmt.Sprintf("%d (%s) S %d 0 0 0 0 0 0 0 0 0 %d %d %d %d 0 0 0 0\n",
		pid, comm, ppid, utime, stime, cutime, cstime)
	err := os.WriteFile(filepath.Join(pidDir, "stat"), []byte(content), 0o644)
	if err != nil {
		t.Fatalf("failed to write pid stat: %v", err)
	}
}

func TestHostAttributionBaselineSubtraction(t *testing.T) {
	procDir := t.TempDir()
	pid := 1234

	// Initial proc state
	writeProcStat(t, procDir, 100, 0, 100, 800, 0, 0, 0, 0)        // active = 200
	writeProcPidStat(t, procDir, pid, "pi-agent", 1, 10, 10, 0, 0) // agent active = 20

	p := newFakeHostEnergy(40.0) // 40W idle
	tracker := NewHostTracker(p, WithProcFS(procDir))

	taskID := "task-001"
	if err := tracker.Start(taskID, pid); err != nil {
		t.Fatalf("tracker.Start failed: %v", err)
	}

	// Advance time and ticks
	time.Sleep(50 * time.Millisecond)

	// Updated proc state
	// System active: 200 -> 400 (delta = 200)
	writeProcStat(t, procDir, 200, 0, 200, 800, 0, 0, 0, 0)
	// Agent active: 20 -> 120 (delta = 100) -> ratio = 100 / 200 = 0.5
	writeProcPidStat(t, procDir, pid, "pi-agent", 1, 60, 60, 0, 0)

	// Set raw package delta: 350.0 J
	p.mu.Lock()
	p.endJoules[taskID] = 350.0
	p.mu.Unlock()

	res, err := tracker.Stop(taskID)
	if err != nil {
		t.Fatalf("tracker.Stop failed: %v", err)
	}

	if res.TaskID != taskID {
		t.Errorf("expected TaskID %q, got %q", taskID, res.TaskID)
	}
	if res.PID != pid {
		t.Errorf("expected PID %d, got %d", pid, res.PID)
	}
	if res.IdlePowerWatts != 40.0 {
		t.Errorf("expected IdlePowerWatts 40.0, got %f", res.IdlePowerWatts)
	}
	if res.RawPackageJoules != 350.0 {
		t.Errorf("expected RawPackageJoules 350.0, got %f", res.RawPackageJoules)
	}

	// Baseline energy = IdlePowerWatts * DurationSec
	expectedBaseline := res.IdlePowerWatts * res.DurationSec
	if res.BaselineJoules <= 0 || res.BaselineJoules != expectedBaseline {
		t.Errorf("expected BaselineJoules %f, got %f", expectedBaseline, res.BaselineJoules)
	}

	expectedActive := 350.0 - res.BaselineJoules
	if expectedActive < 0 {
		expectedActive = 0
	}
	if res.ActiveJoules != expectedActive {
		t.Errorf("expected ActiveJoules %f, got %f", expectedActive, res.ActiveJoules)
	}

	// Agent ratio = 100 / 200 = 0.5
	if res.AgentCPURatio < 0.49 || res.AgentCPURatio > 0.51 {
		t.Errorf("expected AgentCPURatio ~0.5, got %f", res.AgentCPURatio)
	}

	expectedAttributed := expectedActive * res.AgentCPURatio
	if res.AttributedJoules != expectedAttributed {
		t.Errorf("expected AttributedJoules %f, got %f", expectedAttributed, res.AttributedJoules)
	}

	// Invariant: agent_cpu_joules must never equal unreduced RawPackageJoules
	if res.AttributedJoules == res.RawPackageJoules {
		t.Errorf("invariant violated: AttributedJoules (%f) equals RawPackageJoules (%f)",
			res.AttributedJoules, res.RawPackageJoules)
	}

	if res.Status != "ok" {
		t.Errorf("expected Status 'ok', got %q", res.Status)
	}
}

func TestHostAttributionLowPowerClamp(t *testing.T) {
	procDir := t.TempDir()
	pid := 2001

	writeProcStat(t, procDir, 100, 0, 100, 800, 0, 0, 0, 0)
	writeProcPidStat(t, procDir, pid, "app", 1, 10, 10, 0, 0)

	p := newFakeHostEnergy(50.0) // 50W idle
	tracker := NewHostTracker(p, WithProcFS(procDir))

	taskID := "task-low-power"
	if err := tracker.Start(taskID, pid); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	time.Sleep(30 * time.Millisecond)

	writeProcStat(t, procDir, 200, 0, 200, 800, 0, 0, 0, 0)
	writeProcPidStat(t, procDir, pid, "app", 1, 20, 20, 0, 0)

	// Simulate jitter where Raw < BaselineJoules (e.g. raw is only 0.1J while baseline is ~1.5J)
	p.mu.Lock()
	p.endJoules[taskID] = 0.1
	p.mu.Unlock()

	res, err := tracker.Stop(taskID)
	if err != nil {
		t.Fatalf("Stop failed: %v", err)
	}

	// Energy must clamp to 0 and status must flag warning_low_power
	if res.ActiveJoules != 0 {
		t.Errorf("expected ActiveJoules 0, got %f", res.ActiveJoules)
	}
	if res.AttributedJoules != 0 {
		t.Errorf("expected AttributedJoules 0, got %f", res.AttributedJoules)
	}
	if res.Status != "warning_low_power" {
		t.Errorf("expected status 'warning_low_power', got %q", res.Status)
	}
}

func TestHostAttributionProcessNotFound(t *testing.T) {
	procDir := t.TempDir()
	pid := 999999 // does not exist

	writeProcStat(t, procDir, 100, 0, 100, 800, 0, 0, 0, 0)

	p := newFakeHostEnergy(30.0)
	tracker := NewHostTracker(p, WithProcFS(procDir))

	taskID := "task-vanished"
	if err := tracker.Start(taskID, pid); err != nil {
		t.Fatalf("Start should not fail fatally on absent PID: %v", err)
	}

	time.Sleep(20 * time.Millisecond)
	writeProcStat(t, procDir, 200, 0, 200, 800, 0, 0, 0, 0)
	p.mu.Lock()
	p.endJoules[taskID] = 50.0
	p.mu.Unlock()

	res, err := tracker.Stop(taskID)
	if err != nil {
		t.Fatalf("Stop failed: %v", err)
	}

	if res.Status != "process_not_found" {
		t.Errorf("expected status 'process_not_found', got %q", res.Status)
	}
	if res.AgentCPURatio != 0 {
		t.Errorf("expected AgentCPURatio 0, got %f", res.AgentCPURatio)
	}
	if res.AttributedJoules != 0 {
		t.Errorf("expected AttributedJoules 0, got %f", res.AttributedJoules)
	}
}

func TestProcessTreeAggregation(t *testing.T) {
	procDir := t.TempDir()
	parentPid := 3000
	childPid := 3001
	grandchildPid := 3002

	// Setup process tree: parent(3000) -> child(3001) -> grandchild(3002)
	writeProcStat(t, procDir, 1000, 0, 1000, 8000, 0, 0, 0, 0)
	writeProcPidStat(t, procDir, parentPid, "pi-agent", 1, 10, 10, 0, 0)
	writeProcPidStat(t, procDir, childPid, "bash", parentPid, 5, 5, 0, 0)
	writeProcPidStat(t, procDir, grandchildPid, "python", childPid, 1, 1, 0, 0)

	p := newFakeHostEnergy(35.0)
	tracker := NewHostTracker(p, WithProcFS(procDir))

	taskID := "task-tree"
	if err := tracker.Start(taskID, parentPid); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	// Update process tree:
	// Parent active increased by 20 (utime: 20, stime: 20)
	// Child active increased by 10 (utime: 10, stime: 10)
	// Grandchild exited and waited by child, contributing cutime/cstime to child
	// System active increased by 100 (from 2000 to 2100)
	writeProcStat(t, procDir, 1050, 0, 1050, 8000, 0, 0, 0, 0)                // sys delta = 100
	writeProcPidStat(t, procDir, parentPid, "pi-agent", 1, 20, 20, 0, 0)      // delta = 20
	writeProcPidStat(t, procDir, childPid, "bash", parentPid, 10, 10, 15, 15) // delta self = 10, cutime+cstime = 30 -> total child delta = 40
	// remove grandchild (exited)
	_ = os.RemoveAll(filepath.Join(procDir, strconv.Itoa(grandchildPid)))

	// Total agent tree delta = 20 + 40 = 60
	// AgentCPURatio = 60 / 100 = 0.6
	p.mu.Lock()
	p.endJoules[taskID] = 200.0
	p.mu.Unlock()

	res, err := tracker.Stop(taskID)
	if err != nil {
		t.Fatalf("Stop failed: %v", err)
	}

	if res.AgentCPURatio < 0.57 || res.AgentCPURatio > 0.59 {
		t.Errorf("expected tree AgentCPURatio ~0.58, got %f", res.AgentCPURatio)
	}
}

func TestZeroSystemTicksDivisionGuard(t *testing.T) {
	procDir := t.TempDir()
	pid := 4000

	// System ticks do not change between Start and Stop
	writeProcStat(t, procDir, 500, 0, 500, 500, 0, 0, 0, 0)
	writeProcPidStat(t, procDir, pid, "idle-app", 1, 10, 10, 0, 0)

	p := newFakeHostEnergy(25.0)
	tracker := NewHostTracker(p, WithProcFS(procDir))

	taskID := "task-zero-ticks"
	if err := tracker.Start(taskID, pid); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	// Same ticks
	p.mu.Lock()
	p.endJoules[taskID] = 50.0
	p.mu.Unlock()

	res, err := tracker.Stop(taskID)
	if err != nil {
		t.Fatalf("Stop failed: %v", err)
	}

	if res.AgentCPURatio != 0.0 {
		t.Errorf("expected AgentCPURatio 0.0 when system delta ticks is 0, got %f", res.AgentCPURatio)
	}
	if res.AttributedJoules != 0.0 {
		t.Errorf("expected AttributedJoules 0.0, got %f", res.AttributedJoules)
	}
}

func TestZeroMockHardwareUnavailable(t *testing.T) {
	procDir := t.TempDir()
	p := newFakeHostEnergy(0)
	p.available = false // SPBM driver not present

	tracker := NewHostTracker(p, WithProcFS(procDir))
	err := tracker.Start("task-mock-fail", 1)
	if !errors.Is(err, ErrHardwareUnavailable) && !errors.Is(err, provider.ErrHostEnergyUnavailable) {
		t.Fatalf("expected ErrHardwareUnavailable or ErrHostEnergyUnavailable, got: %v", err)
	}
}

func TestCounterDiscontinuity(t *testing.T) {
	procDir := t.TempDir()
	pid := 5000

	writeProcStat(t, procDir, 100, 0, 100, 800, 0, 0, 0, 0)
	writeProcPidStat(t, procDir, pid, "app", 1, 10, 10, 0, 0)

	p := newFakeHostEnergy(40.0)
	p.discontinue = true

	tracker := NewHostTracker(p, WithProcFS(procDir))
	if err := tracker.Start("task-disc", pid); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	_, err := tracker.Stop("task-disc")
	if !errors.Is(err, ErrCounterDiscontinuity) {
		t.Fatalf("expected ErrCounterDiscontinuity, got: %v", err)
	}
}

func TestConcurrentTaskAttribution_NoRace(t *testing.T) {
	procDir := t.TempDir()
	writeProcStat(t, procDir, 1000, 0, 1000, 5000, 0, 0, 0, 0)

	for i := 1; i <= 10; i++ {
		writeProcPidStat(t, procDir, i, "worker", 1, 10, 10, 0, 0)
	}

	p := newFakeHostEnergy(30.0)
	tracker := NewHostTracker(p, WithProcFS(procDir))

	var wg sync.WaitGroup
	for i := 1; i <= 10; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			taskID := fmt.Sprintf("concurrent-task-%d", id)
			if err := tracker.Start(taskID, id); err != nil {
				t.Errorf("Start %s failed: %v", taskID, err)
				return
			}
			p.mu.Lock()
			p.endJoules[taskID] = float64(100 + id*10)
			p.mu.Unlock()

			res, err := tracker.Stop(taskID)
			if err != nil {
				t.Errorf("Stop %s failed: %v", taskID, err)
				return
			}
			if res.TaskID != taskID {
				t.Errorf("TaskID mismatch: %s != %s", res.TaskID, taskID)
			}
		}(i)
	}
	wg.Wait()
}

func TestSimulationSleepVsBusyWorkload(t *testing.T) {
	procDir := t.TempDir()
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	mockTime := now

	clock := func() time.Time {
		return mockTime
	}

	sleepPID := 8001
	busyPID := 8002

	// Setup initial system state: Active = 1000
	writeProcStat(t, procDir, 500, 0, 500, 5000, 0, 0, 0, 0)
	writeProcPidStat(t, procDir, sleepPID, "sleep-3s", 1, 10, 10, 0, 0)
	writeProcPidStat(t, procDir, busyPID, "busy-3s", 1, 10, 10, 0, 0)

	// Both workloads share the same baseline idle power: 40.0 Watts
	pSleep := newFakeHostEnergy(40.0)
	trackerSleep := NewHostTracker(pSleep, WithProcFS(procDir), WithNowFunc(clock))

	pBusy := newFakeHostEnergy(40.0)
	trackerBusy := NewHostTracker(pBusy, WithProcFS(procDir), WithNowFunc(clock))

	// Start both tasks
	if err := trackerSleep.Start("task-sleep-3s", sleepPID); err != nil {
		t.Fatalf("trackerSleep.Start failed: %v", err)
	}
	if err := trackerBusy.Start("task-busy-3s", busyPID); err != nil {
		t.Fatalf("trackerBusy.Start failed: %v", err)
	}

	// Advance clock by exactly 3.00 seconds
	mockTime = mockTime.Add(3 * time.Second)

	// 1. SLEEP WORKLOAD:
	// System active ticks delta = 300
	// Sleep process ticks delta = 0 (only sleeping, 0 CPU activity)
	// Raw package energy = 120.5 J (only baseline 40W * 3s = 120.0 J + 0.5 J idle jitter)
	writeProcStat(t, procDir, 650, 0, 650, 5000, 0, 0, 0, 0)            // sys delta = 300
	writeProcPidStat(t, procDir, sleepPID, "sleep-3s", 1, 10, 10, 0, 0) // ticks delta = 0
	pSleep.mu.Lock()
	pSleep.endJoules["task-sleep-3s"] = 120.5
	pSleep.mu.Unlock()

	sleepRes, err := trackerSleep.Stop("task-sleep-3s")
	if err != nil {
		t.Fatalf("trackerSleep.Stop failed: %v", err)
	}

	// 2. BUSY CPU WORKLOAD:
	// Busy process active ticks delta = 285 (dominant CPU load out of 300 system ticks -> ratio = 0.95)
	// Raw package energy = 300.0 J (CPU power surged to 100W * 3s = 300.0 J)
	writeProcPidStat(t, procDir, busyPID, "busy-3s", 1, 150, 155, 0, 0) // ticks delta = 140+145 = 285
	pBusy.mu.Lock()
	pBusy.endJoules["task-busy-3s"] = 300.0
	pBusy.mu.Unlock()

	busyRes, err := trackerBusy.Stop("task-busy-3s")
	if err != nil {
		t.Fatalf("trackerBusy.Stop failed: %v", err)
	}

	// Format and print comparison table
	table := fmt.Sprintf("\n"+
		"========================================================================================================\n"+
		"| %-12s | %-10s | %-12s | %-12s | %-12s | %-10s | %-14s |\n"+
		"|--------------|------------|--------------|--------------|--------------|------------|----------------|\n"+
		"| %-12s | %-9.2fs | %-10.2f W | %-10.2f J | %-10.2f J | %-10.4f | %-12.2f J |\n"+
		"| %-12s | %-9.2fs | %-10.2f W | %-10.2f J | %-10.2f J | %-10.4f | %-12.2f J |\n"+
		"========================================================================================================\n",
		"Workload", "Duration", "Idle Power", "Raw SPBM", "Baseline", "CPU Ratio", "Attributed J",
		"Sleep 3s", sleepRes.DurationSec, sleepRes.IdlePowerWatts, sleepRes.RawPackageJoules, sleepRes.BaselineJoules, sleepRes.AgentCPURatio, sleepRes.AttributedJoules,
		"Busy CPU 3s", busyRes.DurationSec, busyRes.IdlePowerWatts, busyRes.RawPackageJoules, busyRes.BaselineJoules, busyRes.AgentCPURatio, busyRes.AttributedJoules,
	)
	t.Log(table)

	// Assertions verifying acceptance proof requirements:
	// Sleep process attribution must be zero / negligible (< 0.01 J)
	if sleepRes.AttributedJoules > 0.01 {
		t.Errorf("Sleep process should attribute ~0 J, got %f", sleepRes.AttributedJoules)
	}
	// Busy process attribution must record true incremental dynamic energy
	if busyRes.AttributedJoules < 170.0 || busyRes.AttributedJoules > 172.0 {
		t.Errorf("Busy process expected ~171.0 J, got %f", busyRes.AttributedJoules)
	}
}

func writeCgroupV2CpuStat(t *testing.T, cgroupRoot, relPath string, usageUsec, userUsec, sysUsec uint64) {
	t.Helper()
	dir := filepath.Join(cgroupRoot, relPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("failed to mkdir cgroup dir: %v", err)
	}
	content := fmt.Sprintf("usage_usec %d\nuser_usec %d\nsystem_usec %d\n", usageUsec, userUsec, sysUsec)
	if err := os.WriteFile(filepath.Join(dir, "cpu.stat"), []byte(content), 0o644); err != nil {
		t.Fatalf("failed to write cgroup cpu.stat: %v", err)
	}
}

func writeCgroupV1Cpuacct(t *testing.T, cgroupRoot, relPath string, usageNs uint64) {
	t.Helper()
	dir := filepath.Join(cgroupRoot, relPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("failed to mkdir cgroup dir: %v", err)
	}
	content := fmt.Sprintf("%d\n", usageNs)
	if err := os.WriteFile(filepath.Join(dir, "cpuacct.usage"), []byte(content), 0o644); err != nil {
		t.Fatalf("failed to write cgroup cpuacct.usage: %v", err)
	}
}

func TestHostTracker_CgroupAttribution(t *testing.T) {
	procDir := t.TempDir()
	cgroupDir := t.TempDir()
	cgroupRel := "docker/agent-container-123"

	now := time.Date(2026, 10, 16, 10, 0, 0, 0, time.UTC)
	mockTime := now
	clock := func() time.Time { return mockTime }

	// Initial system ticks: active = 1000 + 1000 = 2000
	writeProcStat(t, procDir, 1000, 0, 1000, 8000, 0, 0, 0, 0)
	// Initial container cgroup: 1s total CPU used (1,000,000 usec)
	writeCgroupV2CpuStat(t, cgroupDir, cgroupRel, 1000000, 800000, 200000)

	p := newFakeHostEnergy(40.0) // 40W baseline
	tracker := NewHostTracker(p, WithProcFS(procDir), WithCgroupRoot(cgroupDir), WithNowFunc(clock))

	taskID := "task-cgroup-001"
	err := tracker.StartTarget(taskID, AttributionTarget{CgroupPath: cgroupRel})
	if err != nil {
		t.Fatalf("tracker.StartTarget failed: %v", err)
	}

	// Advance time by 2.0s
	mockTime = mockTime.Add(2 * time.Second)

	// Updated system ticks: active = 1100 + 1100 = 2200 (delta = 200 ticks = 2.0s at UserHZ 100)
	writeProcStat(t, procDir, 1100, 0, 1100, 8000, 0, 0, 0, 0)
	// Container used an extra 1,000,000 usec (1.0s active CPU)
	writeCgroupV2CpuStat(t, cgroupDir, cgroupRel, 2000000, 1600000, 400000)

	// Set raw package energy delta: 200.0 J
	p.mu.Lock()
	p.endJoules[taskID] = 200.0
	p.mu.Unlock()

	res, err := tracker.Stop(taskID)
	if err != nil {
		t.Fatalf("tracker.Stop failed: %v", err)
	}

	if res.TaskID != taskID {
		t.Errorf("expected TaskID %q, got %q", taskID, res.TaskID)
	}
	if res.CgroupPath != cgroupRel {
		t.Errorf("expected CgroupPath %q, got %q", cgroupRel, res.CgroupPath)
	}
	if res.PID != 0 {
		t.Errorf("expected PID 0, got %d", res.PID)
	}
	if res.DurationSec != 2.0 {
		t.Errorf("expected DurationSec 2.0, got %f", res.DurationSec)
	}
	if res.IdlePowerWatts != 40.0 {
		t.Errorf("expected IdlePowerWatts 40.0, got %f", res.IdlePowerWatts)
	}
	if res.RawPackageJoules != 200.0 {
		t.Errorf("expected RawPackageJoules 200.0, got %f", res.RawPackageJoules)
	}

	// Baseline = 40W * 2s = 80J
	expectedBaseline := 80.0
	if res.BaselineJoules != expectedBaseline {
		t.Errorf("expected BaselineJoules %f, got %f", expectedBaseline, res.BaselineJoules)
	}

	// Active = 200J - 80J = 120J
	expectedActive := 120.0
	if res.ActiveJoules != expectedActive {
		t.Errorf("expected ActiveJoules %f, got %f", expectedActive, res.ActiveJoules)
	}

	// Ratio = 1.0s / 2.0s = 0.5
	if res.AgentCPURatio < 0.499 || res.AgentCPURatio > 0.501 {
		t.Errorf("expected AgentCPURatio ~0.5, got %f", res.AgentCPURatio)
	}

	// Attributed = 120J * 0.5 = 60J
	expectedAttributed := 60.0
	if res.AttributedJoules < expectedAttributed-0.1 || res.AttributedJoules > expectedAttributed+0.1 {
		t.Errorf("expected AttributedJoules ~%f, got %f", expectedAttributed, res.AttributedJoules)
	}

	if res.Status != "ok" {
		t.Errorf("expected status 'ok', got %q", res.Status)
	}
}

func TestHostTracker_CgroupV1(t *testing.T) {
	procDir := t.TempDir()
	cgroupDir := t.TempDir()
	cgroupRel := "docker/v1-container-456"

	now := time.Date(2026, 10, 16, 10, 0, 0, 0, time.UTC)
	mockTime := now
	clock := func() time.Time { return mockTime }

	// Initial system ticks: active = 2000
	writeProcStat(t, procDir, 1000, 0, 1000, 8000, 0, 0, 0, 0)
	// Initial cgroup v1: 1,000,000,000 ns = 1.0s
	writeCgroupV1Cpuacct(t, cgroupDir, cgroupRel, 1000000000)

	p := newFakeHostEnergy(30.0)
	tracker := NewHostTracker(p, WithProcFS(procDir), WithCgroupRoot(cgroupDir), WithNowFunc(clock))

	taskID := "task-cgroup-v1"
	if err := tracker.StartTarget(taskID, AttributionTarget{CgroupPath: cgroupRel}); err != nil {
		t.Fatalf("StartTarget failed: %v", err)
	}

	mockTime = mockTime.Add(1 * time.Second)

	// System delta = 100 ticks = 1.0s
	writeProcStat(t, procDir, 1050, 0, 1050, 8000, 0, 0, 0, 0)
	// Cgroup v1 delta = 500,000,000 ns = 0.5s -> ratio = 0.5 / 1.0 = 0.5
	writeCgroupV1Cpuacct(t, cgroupDir, cgroupRel, 1500000000)

	p.mu.Lock()
	p.endJoules[taskID] = 100.0 // Raw = 100.0 J. Baseline = 30W * 1s = 30J. Active = 70J. Attributed = 35J.
	p.mu.Unlock()

	res, err := tracker.Stop(taskID)
	if err != nil {
		t.Fatalf("Stop failed: %v", err)
	}

	if res.AgentCPURatio < 0.49 || res.AgentCPURatio > 0.51 {
		t.Errorf("expected ratio ~0.5, got %f", res.AgentCPURatio)
	}
	if res.AttributedJoules < 34.9 || res.AttributedJoules > 35.1 {
		t.Errorf("expected AttributedJoules ~35.0, got %f", res.AttributedJoules)
	}
}

func TestHostTracker_CgroupErrors(t *testing.T) {
	procDir := t.TempDir()
	cgroupDir := t.TempDir()
	writeProcStat(t, procDir, 1000, 0, 1000, 8000, 0, 0, 0, 0)

	p := newFakeHostEnergy(30.0)
	tracker := NewHostTracker(p, WithProcFS(procDir), WithCgroupRoot(cgroupDir))

	// 1. Missing cgroup at StartTarget should fail fast
	err := tracker.StartTarget("missing-cg", AttributionTarget{CgroupPath: "nonexistent-container"})
	if err == nil {
		t.Fatal("expected error on nonexistent cgroup, got nil")
	}

	// 2. Cgroup removed before Stop should report cgroup_error status
	cgroupRel := "docker/transient-container"
	writeCgroupV2CpuStat(t, cgroupDir, cgroupRel, 500000, 400000, 100000)

	if err := tracker.StartTarget("transient-cg", AttributionTarget{CgroupPath: cgroupRel}); err != nil {
		t.Fatalf("StartTarget failed: %v", err)
	}

	// Remove cgroup directory simulating container termination/cleanup before stop
	_ = os.RemoveAll(filepath.Join(cgroupDir, cgroupRel))

	p.mu.Lock()
	p.endJoules["transient-cg"] = 80.0
	p.mu.Unlock()

	res, err := tracker.Stop("transient-cg")
	if err != nil {
		t.Fatalf("Stop should not fail fatally, but got err: %v", err)
	}

	if res.Status != "cgroup_error" {
		t.Errorf("expected status 'cgroup_error', got %q", res.Status)
	}
	if res.AgentCPURatio != 0.0 {
		t.Errorf("expected AgentCPURatio 0.0 on cgroup error, got %f", res.AgentCPURatio)
	}
	if res.AttributedJoules != 0.0 {
		t.Errorf("expected AttributedJoules 0.0 on cgroup error, got %f", res.AttributedJoules)
	}
}
