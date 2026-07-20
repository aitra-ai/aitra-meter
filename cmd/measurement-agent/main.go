package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"go.uber.org/zap"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/aitra-ai/aitra-meter/internal/agent"
	"github.com/aitra-ai/aitra-meter/internal/provider"

	// Import providers to trigger their init() registration.
	// NOTE: deploy/aitra-meter-24 builds CGO-free (distroless). The amd and nvml
	// providers require cgo, so they are omitted here; this NVIDIA cluster uses
	// the pure-Go dcgm provider (scrapes node-local dcgm-exporter).
	_ "github.com/aitra-ai/aitra-meter/internal/provider/energy/dcgm"
	_ "github.com/aitra-ai/aitra-meter/internal/provider/energy/zeus"
	_ "github.com/aitra-ai/aitra-meter/internal/provider/inference/genericprometheus"
	_ "github.com/aitra-ai/aitra-meter/internal/provider/inference/vllm"
)

func main() {
	energyType     := flag.String("energy-provider",    "nvml", "Energy provider: nvml | amd | zeus | dcgm")
	inferenceType  := flag.String("inference-provider", "vllm", "Inference provider: vllm | generic-prometheus")
	aggregatorAddr := flag.String("aggregator",         "aitra-meter-aggregation:9091", "Aggregation service gRPC address")
	nodeName       := flag.String("node",               "", "Kubernetes node name (defaults to NODE_NAME env var)")
	windowSecs     := flag.Int(   "window-seconds",     30, "Measurement window duration in seconds")
	logLevel       := flag.String("log-level",          "info", "Log level: debug | info | warn | error")
	inferenceEndpoint := flag.String("inference-endpoint", "", "Inference provider metrics URL (e.g. http://localhost:8000/metrics)")
	energyEndpoint := flag.String("energy-endpoint", "", "Energy provider metrics URL (e.g. http://localhost:9400/metrics for dcgm)")
	perModel := flag.Bool("per-model", false, "Discover GPU pods on this node and report one window per model (requires a per-device energy provider such as dcgm)")
	checkpointPath := flag.String("checkpoint-path", "", "kubelet device-plugin checkpoint path (per-model mode; defaults to "+agent.DefaultCheckpointPath+")")
	kubeconfig := flag.String("kubeconfig", "", "Path to kubeconfig (per-model mode; defaults to in-cluster config)")
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

	energyConfig := map[string]string{}
	if *energyEndpoint != "" {
		energyConfig["endpoint"] = *energyEndpoint
	}
	energyProvider, err := provider.NewEnergy(*energyType, energyConfig)
	if err != nil {
		log.Fatal("energy provider init failed",
			zap.String("provider", *energyType),
			zap.Error(err),
		)
	}

	// Per-model attribution: discover GPU pods dynamically instead of reading
	// one fixed inference endpoint. Models launched by any platform appear on
	// the dashboard automatically and vanish when they scale to zero.
	if *perModel {
		perDevice, ok := energyProvider.(provider.PerDeviceEnergy)
		if !ok {
			log.Fatal("--per-model requires an energy provider with per-device data",
				zap.String("provider", *energyType),
			)
		}
		k8sClient, err := newK8sClient(*kubeconfig)
		if err != nil {
			log.Fatal("kubernetes client init failed", zap.Error(err))
		}
		loop, err := agent.NewMultiLoop(agent.MultiConfig{
			Node:           node,
			AggregatorAddr: *aggregatorAddr,
			WindowDuration: time.Duration(*windowSecs) * time.Second,
			Energy:         energyProvider,
			PerDevice:      perDevice,
			K8s:            k8sClient,
			CheckpointPath: *checkpointPath,
		}, log)
		if err != nil {
			log.Fatal("agent init failed", zap.Error(err))
		}
		defer loop.Close() //nolint:errcheck

		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer cancel()
		loop.Run(ctx)
		log.Info("measurement agent stopped")
		return
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
		Node:              node,
		AggregatorAddr:    *aggregatorAddr,
		WindowDuration:    time.Duration(*windowSecs) * time.Second,
		EnergyProvider:    energyProvider,
		InferenceProvider: inferenceProvider,
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

// newK8sClient builds a Kubernetes client from kubeconfigPath, or from the
// in-cluster service account when the path is empty.
func newK8sClient(kubeconfigPath string) (kubernetes.Interface, error) {
	var restCfg *rest.Config
	var err error
	if kubeconfigPath != "" {
		restCfg, err = clientcmd.BuildConfigFromFlags("", kubeconfigPath)
	} else {
		restCfg, err = rest.InClusterConfig()
	}
	if err != nil {
		return nil, fmt.Errorf("kubernetes config: %w", err)
	}
	return kubernetes.NewForConfig(restCfg)
}

func newLogger(level string) *zap.Logger {
	cfg := zap.NewProductionConfig()
	_ = cfg.Level.UnmarshalText([]byte(level))
	l, _ := cfg.Build()
	return l
}
