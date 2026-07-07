package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"go.uber.org/zap"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/aitra-ai/aitra-meter/internal/agent"
	"github.com/aitra-ai/aitra-meter/internal/discovery"
	"github.com/aitra-ai/aitra-meter/internal/provider"

	// Import providers to trigger their init() registration.
	_ "github.com/aitra-ai/aitra-meter/internal/provider/energy/amd"
	_ "github.com/aitra-ai/aitra-meter/internal/provider/energy/dcgm"
	_ "github.com/aitra-ai/aitra-meter/internal/provider/energy/kepler"
	_ "github.com/aitra-ai/aitra-meter/internal/provider/energy/zeus"
	_ "github.com/aitra-ai/aitra-meter/internal/provider/inference/genericprometheus"
	_ "github.com/aitra-ai/aitra-meter/internal/provider/inference/sglang"
	_ "github.com/aitra-ai/aitra-meter/internal/provider/inference/triton"
	_ "github.com/aitra-ai/aitra-meter/internal/provider/inference/vllm"
)

const (
	// discoveryRetryInterval and discoveryTimeout bound the startup wait for
	// an inference pod to appear on this node when --inference-provider=auto.
	discoveryRetryInterval = 5 * time.Second
	discoveryTimeout       = 60 * time.Second

	// inferenceConfigEnvPrefix is how the Helm chart passes
	// inferenceProvider.config keys to the agent: each key is exported as
	// INFERENCE_CONFIG_<KEY> (key uppercased, e.g. inferenceProvider.config.
	// avg_output_tokens_per_request becomes
	// INFERENCE_CONFIG_AVG_OUTPUT_TOKENS_PER_REQUEST).
	inferenceConfigEnvPrefix = "INFERENCE_CONFIG_"
)

func main() {
	energyType := flag.String("energy-provider", "nvml", "Energy provider: nvml | amd | zeus | dcgm | kepler")
	inferenceType := flag.String("inference-provider", "vllm", "Inference provider: vllm | sglang | triton | generic-prometheus | auto")
	aggregatorAddr := flag.String("aggregator", "aitra-meter-aggregation:9091", "Aggregation service gRPC address")
	nodeName := flag.String("node", "", "Kubernetes node name (defaults to NODE_NAME env var)")
	windowSecs := flag.Int("window-seconds", 30, "Measurement window duration in seconds")
	logLevel := flag.String("log-level", "info", "Log level: debug | info | warn | error")
	inferenceEndpoint := flag.String("inference-endpoint", "", "Inference provider metrics URL (e.g. http://localhost:8000/metrics; defaults to INFERENCE_ENDPOINT env var; ignored with --inference-provider=auto, where the endpoint is discovered from the pod IP)")
	energyEndpoint := flag.String("energy-endpoint", "", "Energy provider endpoint URL for scrape-based providers (dcgm exporter /metrics URL, or the Prometheus base URL for kepler)")
	kubeconfig := flag.String("kubeconfig", "", "Path to kubeconfig for --inference-provider=auto (defaults to in-cluster config)")
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

	// Provider config: INFERENCE_CONFIG_* env vars (set by the Helm chart
	// from inferenceProvider.config), then the --inference-endpoint flag or
	// the legacy INFERENCE_ENDPOINT env var for the endpoint key.
	inferenceConfig := inferenceConfigFromEnv(os.Environ())
	endpoint := *inferenceEndpoint
	if endpoint == "" {
		endpoint = os.Getenv("INFERENCE_ENDPOINT")
	}
	if endpoint != "" {
		inferenceConfig["endpoint"] = endpoint
	}

	inferenceName := *inferenceType
	if inferenceName == "auto" {
		detected, err := autoDiscoverInference(log, *kubeconfig, node)
		if err != nil {
			log.Fatal("inference auto-discovery failed", zap.Error(err))
		}
		inferenceName = detected.Detection.Engine.Provider
		// Merge discovery results into the static config. The discovered
		// endpoint always wins — it points at the detected pod's IP, and a
		// statically configured endpoint (which the chart defaults to
		// localhost) must not clobber it. For every other key an explicit
		// static value takes precedence over the engine preset, so operators
		// can still tune e.g. avg_output_tokens_per_request in auto mode.
		for k, v := range detected.ProviderConfig() {
			if _, explicit := inferenceConfig[k]; !explicit || k == "endpoint" {
				inferenceConfig[k] = v
			}
		}
	}
	inferenceProvider, err := provider.NewInference(inferenceName, inferenceConfig)
	if err != nil {
		log.Fatal("inference provider init failed",
			zap.String("provider", inferenceName),
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

// inferenceConfigFromEnv collects inference provider config from
// INFERENCE_CONFIG_* environment variables (see inferenceConfigEnvPrefix).
// Env var names are uppercased snake_case; the provider config keys they map
// to are the lowercased remainder, e.g. INFERENCE_CONFIG_OUTPUT_TOKENS_METRIC
// becomes config["output_tokens_metric"]. Empty values are skipped so an
// unset chart value cannot mask a provider default.
func inferenceConfigFromEnv(environ []string) map[string]string {
	cfg := map[string]string{}
	for _, kv := range environ {
		name, value, ok := strings.Cut(kv, "=")
		if !ok || value == "" || !strings.HasPrefix(name, inferenceConfigEnvPrefix) {
			continue
		}
		key := strings.ToLower(strings.TrimPrefix(name, inferenceConfigEnvPrefix))
		if key == "" {
			continue
		}
		cfg[key] = value
	}
	return cfg
}

// autoDiscoverInference detects the inference engine running on this node
// from pod annotations/labels (see internal/discovery). It retries for up to
// discoveryTimeout so an agent that starts before the inference pod does not
// crash-loop unnecessarily. When several inference pods run on one node the
// first (sorted by namespace/name) is measured and the rest are logged —
// the agent tracks one token source per node.
func autoDiscoverInference(log *zap.Logger, kubeconfigPath, node string) (discovery.PodDetection, error) {
	client, err := buildK8sClient(kubeconfigPath)
	if err != nil {
		return discovery.PodDetection{}, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), discoveryTimeout)
	defer cancel()

	for {
		detections, err := discovery.DiscoverNodeInference(ctx, client, node)
		if err != nil {
			return discovery.PodDetection{}, err
		}
		for _, d := range detections {
			log.Info("discovered inference pod",
				zap.String("namespace", d.Namespace),
				zap.String("pod", d.Pod),
				zap.String("provider", d.Detection.Engine.Provider),
				zap.String("token_metric", d.Detection.Engine.TokenMetric),
				zap.String("endpoint", d.Endpoint),
				zap.String("detected_by", d.Detection.Source),
				zap.String("matched", d.Detection.Matched),
			)
		}
		if len(detections) > 0 {
			if len(detections) > 1 {
				log.Warn("multiple inference pods on node — measuring the first only",
					zap.Int("count", len(detections)),
					zap.String("selected", detections[0].Namespace+"/"+detections[0].Pod),
				)
			}
			return detections[0], nil
		}

		log.Info("no inference pod detected on node yet — retrying",
			zap.String("node", node),
			zap.Duration("retry_interval", discoveryRetryInterval),
		)
		select {
		case <-ctx.Done():
			return discovery.PodDetection{}, fmt.Errorf(
				"no inference pod found on node %s within %s — annotate your inference pods with %s or set --inference-provider explicitly",
				node, discoveryTimeout, discovery.AnnotationInferenceProvider)
		case <-time.After(discoveryRetryInterval):
		}
	}
}

func buildK8sClient(kubeconfigPath string) (kubernetes.Interface, error) {
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
