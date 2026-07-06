package main

import (
	"context"
	"errors"
	"flag"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.uber.org/zap"

	"github.com/aitra-ai/aitra-meter/internal/agent"
	"github.com/aitra-ai/aitra-meter/internal/provider"

	// Import providers to trigger their init() registration.
	_ "github.com/aitra-ai/aitra-meter/internal/provider/energy/amd"
	_ "github.com/aitra-ai/aitra-meter/internal/provider/energy/dcgm"
	_ "github.com/aitra-ai/aitra-meter/internal/provider/energy/zeus"
	_ "github.com/aitra-ai/aitra-meter/internal/provider/inference/genericprometheus"
	_ "github.com/aitra-ai/aitra-meter/internal/provider/inference/vllm"
)

func main() {
	energyType := flag.String("energy-provider", "nvml", "Energy provider: nvml | amd | zeus | dcgm")
	inferenceType := flag.String("inference-provider", "vllm", "Inference provider: vllm | generic-prometheus")
	aggregatorAddr := flag.String("aggregator", "aitra-meter-aggregation:9091", "Aggregation service gRPC address")
	nodeName := flag.String("node", "", "Kubernetes node name (defaults to NODE_NAME env var)")
	windowSecs := flag.Int("window-seconds", 30, "Measurement window duration in seconds")
	logLevel := flag.String("log-level", "info", "Log level: debug | info | warn | error")
	inferenceEndpoint := flag.String("inference-endpoint", "", "Inference provider metrics URL (e.g. http://localhost:8000/metrics)")
	metricsAddr := flag.String("metrics-addr", ":9090", "Address for the agent's Prometheus /metrics and health endpoints (empty to disable)")
	migInstance := flag.String("mig-instance", "", "MIG slice the node's inference server is pinned to, as a mig_instance label (mig-1g.10gb:0) or MIG device UUID (MIG-…). Empty: auto when the node exposes exactly one slice")
	migNamespace := flag.String("mig-namespace", "", "Namespace label for aitra_mig_* metrics (default \"unknown\")")
	migTeam := flag.String("mig-team", "", "Team label for aitra_mig_cost_usd_total (default \"unknown\")")
	electricityCost := flag.Float64("electricity-cost-usd-per-kwh", 0, "Electricity price in USD/kWh for aitra_mig_cost_usd_total; 0 disables the cost counter")
	flag.Parse()

	log := newLogger(*logLevel)
	defer log.Sync() //nolint:errcheck

	node := *nodeName
	if node == "" {
		node = os.Getenv("NODE_NAME")
	}
	if node == "" {
		log.Fatal("--node or NODE_NAME environment variable is required")
	}

	log.Info("starting measurement agent",
		zap.String("node", node),
		zap.String("energy_provider", *energyType),
		zap.String("inference_provider", *inferenceType),
		zap.String("aggregator", *aggregatorAddr),
		zap.Int("window_seconds", *windowSecs),
	)

	energyProvider, err := provider.NewEnergy(*energyType, nil)
	if err != nil {
		log.Fatal("energy provider init failed",
			zap.String("provider", *energyType),
			zap.Error(err),
		)
	}

	// MIG detection (issue #43): when the energy provider reports MIG mode,
	// the loop switches to MIG-aware windows automatically. This log makes
	// the switch visible at startup.
	if migp, ok := energyProvider.(provider.MIGEnergyProvider); ok && migp.MIGEnabled() {
		slices, serr := migp.MIGSlices(context.Background())
		if serr != nil {
			log.Warn("MIG mode detected but slice enumeration failed", zap.Error(serr))
		} else {
			instances := make([]string, 0, len(slices))
			for _, s := range slices {
				instances = append(instances, s.Instance)
			}
			log.Info("MIG mode detected — per-slice energy attribution enabled",
				zap.Int("slice_count", len(slices)),
				zap.Strings("mig_instances", instances),
				zap.String("pinned_instance", *migInstance),
			)
		}
	}

	inferenceConfig := map[string]string{}
	if *inferenceEndpoint != "" {
		inferenceConfig["endpoint"] = *inferenceEndpoint
	}
	inferenceProvider, err := provider.NewInference(*inferenceType, inferenceConfig)
	if err != nil {
		log.Fatal("inference provider init failed",
			zap.String("provider", *inferenceType),
			zap.Error(err),
		)
	}

	// Agent-local Prometheus metrics (aitra_mig_* on MIG nodes) + health probes.
	if *metricsAddr != "" {
		mux := http.NewServeMux()
		mux.Handle("/metrics", promhttp.Handler())
		mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		})
		mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"status":"ready"}`))
		})
		srv := &http.Server{Addr: *metricsAddr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
		go func() {
			log.Info("metrics server listening", zap.String("addr", *metricsAddr))
			if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Error("metrics server stopped", zap.Error(err))
			}
		}()
	}

	loop, err := agent.New(agent.Config{
		Node:              node,
		AggregatorAddr:    *aggregatorAddr,
		WindowDuration:    time.Duration(*windowSecs) * time.Second,
		EnergyProvider:    energyProvider,
		InferenceProvider: inferenceProvider,
		MIG: agent.MIGAttribution{
			PinnedInstance:        *migInstance,
			Namespace:             *migNamespace,
			Team:                  *migTeam,
			ElectricityCostPerKWh: *electricityCost,
		},
	}, log)
	if err != nil {
		log.Fatal("agent init failed", zap.Error(err))
	}
	defer loop.Close() //nolint:errcheck

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	loop.Run(ctx)
	log.Info("measurement agent stopped")
}

func newLogger(level string) *zap.Logger {
	cfg := zap.NewProductionConfig()
	_ = cfg.Level.UnmarshalText([]byte(level))
	l, _ := cfg.Build()
	return l
}
