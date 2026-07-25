package main

import (
	"context"
	"flag"
	"os"
	"os/signal"
	"syscall"
	"time"

	"go.uber.org/zap"

	"github.com/aitra-ai/aitra-meter/internal/agent"
	"github.com/aitra-ai/aitra-meter/internal/provider"

	// Import providers to trigger their init() registration.
	_ "github.com/aitra-ai/aitra-meter/internal/provider/energy/amd"
	_ "github.com/aitra-ai/aitra-meter/internal/provider/energy/dcgm"
	_ "github.com/aitra-ai/aitra-meter/internal/provider/energy/zeus"
	_ "github.com/aitra-ai/aitra-meter/internal/provider/hostenergy/gracehwmon"
	_ "github.com/aitra-ai/aitra-meter/internal/provider/hostenergy/gracespark"
	_ "github.com/aitra-ai/aitra-meter/internal/provider/hostenergy/rapl"
	_ "github.com/aitra-ai/aitra-meter/internal/provider/inference/genericprometheus"
	_ "github.com/aitra-ai/aitra-meter/internal/provider/inference/vllm"
)

func main() {
	energyType := flag.String("energy-provider", "nvml", "Energy provider: nvml | amd | zeus | dcgm")
	hostEnergyType := flag.String("host-energy-provider", "none", "Host (non-accelerator) energy provider: none | rapl | grace-hwmon | grace-spark-hwmon (experimental)")
	inferenceType := flag.String("inference-provider", "vllm", "Inference provider: vllm | generic-prometheus")
	aggregatorAddr := flag.String("aggregator", "aitra-meter-aggregation:9091", "Aggregation service gRPC address")
	nodeName := flag.String("node", "", "Kubernetes node name (defaults to NODE_NAME env var)")
	windowSecs := flag.Int("window-seconds", 30, "Measurement window duration in seconds")
	logLevel := flag.String("log-level", "info", "Log level: debug | info | warn | error")
	inferenceEndpoint := flag.String("inference-endpoint", "", "Inference provider metrics URL (e.g. http://localhost:8000/metrics)")
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
		zap.String("host_energy_provider", *hostEnergyType),
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

	// Host energy is optional and best-effort: a failure to construct the host
	// provider must never take down the agent, and must never silently become a
	// zero reading. On any error we fall back to the Noop provider, which reports
	// unavailable so host metrics are omitted rather than zeroed.
	hostEnergyProvider, err := provider.NewHostEnergy(*hostEnergyType, nil)
	if err != nil {
		log.Warn("host energy provider init failed — host energy will be reported as unavailable",
			zap.String("provider", *hostEnergyType),
			zap.Error(err),
		)
		hostEnergyProvider = provider.NewNoopHostEnergy("init failed: " + err.Error())
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

	loop, err := agent.New(agent.Config{
		Node:               node,
		AggregatorAddr:     *aggregatorAddr,
		WindowDuration:     time.Duration(*windowSecs) * time.Second,
		EnergyProvider:     energyProvider,
		InferenceProvider:  inferenceProvider,
		HostEnergyProvider: hostEnergyProvider,
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
