package main

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	colmetricpb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	"go.uber.org/zap"
	"google.golang.org/grpc"

	measurementv1 "github.com/aitra-ai/aitra-meter/api/proto/measurement/v1"
	"github.com/aitra-ai/aitra-meter/internal/aggregation"
	"github.com/aitra-ai/aitra-meter/internal/export/otlp"
)

// --- stubs ------------------------------------------------------------------

// stubCollector is an in-process OTLP gRPC metrics receiver that records the
// name of every metric exported to it.
type stubCollector struct {
	colmetricpb.UnimplementedMetricsServiceServer

	mu    sync.Mutex
	names map[string]int
}

func (c *stubCollector) Export(_ context.Context, req *colmetricpb.ExportMetricsServiceRequest) (*colmetricpb.ExportMetricsServiceResponse, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, rm := range req.GetResourceMetrics() {
		for _, sm := range rm.GetScopeMetrics() {
			for _, m := range sm.GetMetrics() {
				c.names[m.GetName()]++
			}
		}
	}
	return &colmetricpb.ExportMetricsServiceResponse{}, nil
}

func (c *stubCollector) seen(name string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.names[name] > 0
}

// startStubCollector serves a stubCollector on a loopback port and returns it
// with its host:port address.
func startStubCollector(t *testing.T) (*stubCollector, string) {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	c := &stubCollector{names: make(map[string]int)}
	srv := grpc.NewServer()
	colmetricpb.RegisterMetricsServiceServer(srv, c)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	return c, lis.Addr().String()
}

type stubPods struct{}

func (stubPods) ByNodeAndModel(context.Context, string, string) (aggregation.PodMeta, error) {
	return aggregation.PodMeta{Namespace: "inference-prod"}, nil
}

type stubHardware struct{}

func (stubHardware) Hardware(context.Context, string) string { return "a100" }

type discardWriter struct{}

func (discardWriter) Write(context.Context, aggregation.MeasurementRecord) error { return nil }
func (discardWriter) WriteBatch(context.Context, []aggregation.MeasurementRecord) error {
	return nil
}
func (discardWriter) Close() error { return nil }
func (discardWriter) Name() string { return "discard" }

func newTestLoop() *aggregation.Loop {
	return aggregation.NewLoop(
		"test-cluster",
		aggregation.NewResolver(stubPods{}, aggregation.PolicyConfig{}),
		aggregation.NewCalibrationTableFromMap(nil),
		stubHardware{},
		discardWriter{},
		aggregation.SiteParams{},
	)
}

func servingWindow() *measurementv1.WindowReport {
	return &measurementv1.WindowReport{
		WindowId:          "w-otlp-001",
		Node:              "node-otlp",
		ModelName:         "llama-otlp",
		EnergyJoules:      412.4,
		OutputTokens:      1328,
		PowerWatts:        320.0,
		Stable:            true,
		EnergyProvider:    "nvml",
		InferenceProvider: "vllm",
		TimestampUnixMs:   1716000000000,
	}
}

// --- tests ------------------------------------------------------------------

func TestAttachOTLPDisabledWhenEndpointEmpty(t *testing.T) {
	loop := newTestLoop()

	err := attachOTLP(context.Background(), loop, otlp.Config{ClusterName: "test-cluster"}, zap.NewNop())

	if err != nil {
		t.Errorf("attachOTLP with an empty endpoint: %v", err)
	}
	if loop.OTLPExporter != nil {
		t.Errorf("loop.OTLPExporter set with an empty endpoint")
	}
	// The loop must keep working with export off.
	ack, err := loop.ReportWindow(context.Background(), servingWindow())
	if err != nil || !ack.GetAccepted() {
		t.Errorf("ReportWindow = (%v, %v), want accepted window", ack, err)
	}
}

func TestAttachOTLPExportsReportedWindows(t *testing.T) {
	collector, addr := startStubCollector(t)
	loop := newTestLoop()

	err := attachOTLP(context.Background(), loop, otlp.Config{
		Endpoint:    addr,
		ClusterName: "test-cluster",
		Insecure:    true,
	}, zap.NewNop())
	if err != nil {
		t.Fatalf("attachOTLP: %v", err)
	}
	if loop.OTLPExporter == nil {
		t.Fatal("loop.OTLPExporter not set with an endpoint")
	}

	// gRPC serves ReportWindow concurrently, so the exporter must tolerate it.
	const reporters = 8
	var wg sync.WaitGroup
	for range reporters {
		wg.Go(func() {
			ack, err := loop.ReportWindow(context.Background(), servingWindow())
			if err != nil || !ack.GetAccepted() {
				t.Errorf("ReportWindow = (%v, %v), want accepted window", ack, err)
			}
		})
	}
	wg.Wait()

	// Shutdown flushes the periodic reader, so the window reaches the collector
	// without waiting for the export interval.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := loop.OTLPExporter.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	for _, name := range []string{
		"gen_ai.infrastructure.energy.joules_per_token",
		"gen_ai.infrastructure.energy.joules_total",
		"gen_ai.infrastructure.power.watts",
		"gen_ai.infrastructure.idle_ratio",
	} {
		if !collector.seen(name) {
			t.Errorf("collector did not receive metric %q", name)
		}
	}
}

func TestAttachOTLPRejectsMalformedEndpoint(t *testing.T) {
	for _, endpoint := range []string{
		"http://otel-collector:4317",     // URL, as OTEL_EXPORTER_OTLP_ENDPOINT takes
		"http://otel-collector",          // URL without a port
		"otel-collector",                 // no port
		":4317",                          // no host
		"otel-collector:",                // empty port
		"otel-collector:grpc",            // port is not a number
		"otel-collector:0",               // port out of range
		"otel-collector:65536",           // port out of range
		"otel-collector:4317/v1/metrics", // path after the port
	} {
		t.Run(endpoint, func(t *testing.T) {
			loop := newTestLoop()

			err := attachOTLP(context.Background(), loop, otlp.Config{Endpoint: endpoint}, zap.NewNop())

			if err == nil {
				t.Errorf("attachOTLP(%q) = nil error, want an error", endpoint)
			}
			if loop.OTLPExporter != nil {
				t.Errorf("attachOTLP(%q) attached an exporter", endpoint)
			}
		})
	}
}

func TestEnvBool(t *testing.T) {
	const key = "AITRA_TEST_ENV_BOOL"
	cases := []struct {
		name string
		val  string
		def  bool
		want bool
	}{
		{"unset keeps default", "", true, true},
		{"true", "true", false, true},
		{"false overrides default", "false", true, false},
		{"invalid keeps default", "maybe", true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(key, tc.val)
			if got := envBool(key, tc.def); got != tc.want {
				t.Errorf("envBool(%q, %v) = %v, want %v", tc.val, tc.def, got, tc.want)
			}
		})
	}
}
