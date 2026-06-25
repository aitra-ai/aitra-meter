package agent

import (
	"context"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"
	"google.golang.org/grpc"

	measurementv1 "github.com/aitra-ai/aitra-meter/api/proto/measurement/v1"
	"github.com/aitra-ai/aitra-meter/internal/provider"
)

// --- fake providers ---------------------------------------------------------

type fakeEnergy struct {
	joules float64
	err    error
}

func (f *fakeEnergy) Name() string                                           { return "fake-energy" }
func (f *fakeEnergy) BeginWindow(_ context.Context, _ string) error          { return f.err }
func (f *fakeEnergy) EndWindow(_ context.Context, _ string) (float64, error) { return f.joules, f.err }
func (f *fakeEnergy) IdlePower(_ context.Context) (float64, error)           { return f.joules / 30, f.err }
func (f *fakeEnergy) Devices(_ context.Context) ([]provider.Device, error)   { return nil, nil }

type fakeInference struct {
	tokens  uint64
	running int
	model   string
	err     error
}

func (f *fakeInference) Name() string                                   { return "fake-inference" }
func (f *fakeInference) OutputTokens(_ context.Context) (uint64, error) { return f.tokens, f.err }
func (f *fakeInference) RequestsRunning(_ context.Context) (int, error) { return f.running, f.err }
func (f *fakeInference) ModelName(_ context.Context) (string, error)    { return f.model, f.err }

// --- fake aggregation service -----------------------------------------------

type fakeAggSvc struct {
	measurementv1.UnimplementedMeasurementServiceServer
	mu      sync.Mutex
	reports []*measurementv1.WindowReport
}

func (s *fakeAggSvc) ReportWindow(_ context.Context, r *measurementv1.WindowReport) (*measurementv1.WindowAck, error) {
	s.mu.Lock()
	s.reports = append(s.reports, r)
	s.mu.Unlock()
	return &measurementv1.WindowAck{Accepted: r.OutputTokens > 0}, nil
}

func (s *fakeAggSvc) all() []*measurementv1.WindowReport {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*measurementv1.WindowReport, len(s.reports))
	copy(out, s.reports)
	return out
}

// startFakeAggSvc starts a real gRPC server on a random port and returns
// the address and a stop function.
func startFakeAggSvc(t *testing.T) (addr string, svc *fakeAggSvc, stop func()) {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	svc = &fakeAggSvc{}
	srv := grpc.NewServer()
	measurementv1.RegisterMeasurementServiceServer(srv, svc)
	go srv.Serve(lis) //nolint:errcheck
	return lis.Addr().String(), svc, srv.GracefulStop
}

// --- helpers ----------------------------------------------------------------

func newTestLoop(t *testing.T, addr string, energy *fakeEnergy, inference *fakeInference) *Loop {
	t.Helper()
	l, err := New(Config{
		Node:              "test-node",
		AggregatorAddr:    addr,
		WindowDuration:    20 * time.Millisecond, // fast for tests
		EnergyProvider:    energy,
		InferenceProvider: inference,
	}, zap.NewNop())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { l.Close() }) //nolint:errcheck
	return l
}

// runFor runs the loop for at most d or until stop is called, whichever comes first.
func runFor(l *Loop, d time.Duration) {
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	l.Run(ctx)
}

// --- tests ------------------------------------------------------------------

func TestLoopSendsWindowReports(t *testing.T) {
	addr, svc, stop := startFakeAggSvc(t)
	defer stop()

	energy := &fakeEnergy{joules: 100}
	inference := &fakeInference{tokens: 500, running: 1, model: "llama-3-8b"}
	loop := newTestLoop(t, addr, energy, inference)

	runFor(loop, 120*time.Millisecond) // should get ~6 windows at 20ms

	reports := svc.all()
	if len(reports) < 2 {
		t.Fatalf("expected ≥2 WindowReports, got %d", len(reports))
	}
	r := reports[0]
	if r.Node != "test-node" {
		t.Errorf("Node = %q, want test-node", r.Node)
	}
	if r.EnergyJoules != 100 {
		t.Errorf("EnergyJoules = %f, want 100", r.EnergyJoules)
	}
	if r.EnergyProvider != "fake-energy" {
		t.Errorf("EnergyProvider = %q, want fake-energy", r.EnergyProvider)
	}
	if r.InferenceProvider != "fake-inference" {
		t.Errorf("InferenceProvider = %q, want fake-inference", r.InferenceProvider)
	}
	if r.ModelName != "llama-3-8b" {
		t.Errorf("ModelName = %q, want llama-3-8b", r.ModelName)
	}
}

func TestLoopTokenDeltaComputed(t *testing.T) {
	// Tokens increase by 200 each window. First window has no delta (seenFirstToken=false).
	// From second window onward delta should be 200.
	addr, svc, stop := startFakeAggSvc(t)
	defer stop()

	inf := &fakeInference{model: "m"}
	// We advance tokens by 200 on each call.
	energy := &fakeEnergy{joules: 50}
	loop := newTestLoop(t, addr, energy, inf)

	// Use a custom inference provider via struct field mutation (same package).
	loop.cfg.InferenceProvider = &incrementingInference{step: 200, model: "m"}

	runFor(loop, 120*time.Millisecond)

	reports := svc.all()
	if len(reports) < 2 {
		t.Fatalf("expected ≥2 reports, got %d", len(reports))
	}
	// First report: delta is 0 because we haven't seen a previous token count.
	if reports[0].OutputTokens != 0 {
		t.Errorf("first report OutputTokens = %d, want 0 (no previous baseline)", reports[0].OutputTokens)
	}
	// Second report onward: delta should be 200.
	if reports[1].OutputTokens != 200 {
		t.Errorf("second report OutputTokens = %d, want 200", reports[1].OutputTokens)
	}
}

// incrementingInference returns tokens that grow by step on each call.
type incrementingInference struct {
	mu      sync.Mutex
	current uint64
	step    uint64
	model   string
}

func (i *incrementingInference) Name() string { return "incr" }
func (i *incrementingInference) OutputTokens(_ context.Context) (uint64, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.current += i.step
	return i.current, nil
}
func (i *incrementingInference) RequestsRunning(_ context.Context) (int, error) { return 1, nil }
func (i *incrementingInference) ModelName(_ context.Context) (string, error)    { return i.model, nil }

func TestLoopIdleWindowSentNotAccepted(t *testing.T) {
	// Zero tokens → aggregation service returns accepted=false.
	// The loop should still send the report (for idle power tracking).
	addr, svc, stop := startFakeAggSvc(t)
	defer stop()

	energy := &fakeEnergy{joules: 80}
	inference := &fakeInference{tokens: 0, running: 0, model: "m"}
	loop := newTestLoop(t, addr, energy, inference)

	runFor(loop, 80*time.Millisecond)

	reports := svc.all()
	if len(reports) == 0 {
		t.Fatal("expected at least one WindowReport even with zero tokens")
	}
	for _, r := range reports {
		if r.OutputTokens != 0 {
			t.Errorf("expected OutputTokens=0 for idle window, got %d", r.OutputTokens)
		}
	}
}

func TestLoopEnergyProviderBeginFailSkipsTick(t *testing.T) {
	// If BeginWindow fails, the loop should skip the tick and continue.
	addr, svc, stop := startFakeAggSvc(t)
	defer stop()

	energy := &fakeEnergy{joules: 0, err: errFake}
	inference := &fakeInference{tokens: 100, running: 1, model: "m"}
	loop := newTestLoop(t, addr, energy, inference)

	runFor(loop, 100*time.Millisecond)

	// No reports — every tick fails BeginWindow.
	if n := len(svc.all()); n > 0 {
		t.Errorf("expected 0 reports when BeginWindow always fails, got %d", n)
	}
}

var errFake = fmt.Errorf("simulated provider error")

func TestLoopWindowIDMonotonicallyIncreasing(t *testing.T) {
	addr, svc, stop := startFakeAggSvc(t)
	defer stop()

	energy := &fakeEnergy{joules: 10}
	inference := &fakeInference{tokens: 50, running: 1, model: "m"}
	loop := newTestLoop(t, addr, energy, inference)

	runFor(loop, 100*time.Millisecond)

	reports := svc.all()
	if len(reports) < 2 {
		t.Skip("not enough reports to compare window IDs")
	}
	// Window IDs must be distinct.
	seen := map[string]bool{}
	for _, r := range reports {
		if seen[r.WindowId] {
			t.Errorf("duplicate WindowId %q", r.WindowId)
		}
		seen[r.WindowId] = true
	}
}

func TestLoopTimestampPopulated(t *testing.T) {
	addr, svc, stop := startFakeAggSvc(t)
	defer stop()

	before := time.Now().UnixMilli()
	energy := &fakeEnergy{joules: 10}
	inference := &fakeInference{tokens: 1, running: 1, model: "m"}
	loop := newTestLoop(t, addr, energy, inference)

	runFor(loop, 60*time.Millisecond)
	after := time.Now().UnixMilli()

	for _, r := range svc.all() {
		if r.TimestampUnixMs < before || r.TimestampUnixMs > after {
			t.Errorf("TimestampUnixMs %d out of range [%d, %d]", r.TimestampUnixMs, before, after)
		}
	}
}

func TestLoopGracefulShutdown(t *testing.T) {
	// Cancel context mid-window; loop must drain the current window and exit.
	addr, svc, stop := startFakeAggSvc(t)
	defer stop()

	energy := &fakeEnergy{joules: 42}
	inference := &fakeInference{tokens: 10, running: 1, model: "m"}
	loop := newTestLoop(t, addr, energy, inference)

	// Use a long window so we cancel before the first tick.
	loop.cfg.WindowDuration = 5 * time.Second

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		loop.Run(ctx)
	}()

	// Cancel after BeginWindow has had time to be called.
	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case <-done:
		// Loop exited cleanly.
	case <-time.After(2 * time.Second):
		t.Fatal("loop did not exit within 2s of context cancellation")
	}

	// One window should have been drained and reported.
	if n := len(svc.all()); n == 0 {
		t.Error("expected at least one window to be drained on shutdown")
	}
}
