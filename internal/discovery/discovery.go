// Package discovery detects which inference engine a pod runs from its
// labels and annotations, and derives measurement-agent provider
// configuration from the result.
//
// Detection priority (issue #41):
//  1. Explicit "aitra-ai.github.io/inference-provider" annotation.
//  2. "app" label matching a known engine name.
//  3. generic-prometheus fallback — only when the annotation names an engine
//     Aitra does not know; unannotated pods with unrecognised app labels are
//     NOT treated as inference servers.
//
// The package is pure detection plus a thin Kubernetes pod-listing helper;
// it does not watch for changes. The agent runs detection at startup and
// logs the chosen provider and token metric per discovered pod.
package discovery

import (
	"context"
	"fmt"
	"sort"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

const (
	// AnnotationInferenceProvider explicitly selects the inference provider
	// for a pod, overriding app-label detection.
	AnnotationInferenceProvider = "aitra-ai.github.io/inference-provider"

	// labelApp is the conventional Kubernetes app label used as the
	// second-priority detection signal.
	labelApp = "app"

	// Detection sources reported in Detection.Source.
	SourceAnnotation = "annotation"
	SourceAppLabel   = "app-label"
	SourceFallback   = "fallback"
)

// Engine describes a known inference engine: which registered provider reads
// it, which token metric that provider consumes, and where the metrics
// endpoint lives by default.
type Engine struct {
	// Provider is the registry name passed to provider.NewInference.
	Provider string
	// TokenMetric is the output-token counter the provider reads. Purely
	// informational — surfaced in startup logs so operators can verify what
	// is being measured.
	TokenMetric string
	// MetricsPort is the engine's default metrics port.
	MetricsPort int
	// Config holds extra provider config keys (used to pre-configure
	// generic-prometheus for engines without a dedicated provider).
	Config map[string]string
}

// knownEngines maps engine keys — valid values for both the
// aitra-ai.github.io/inference-provider annotation and the app label.
var knownEngines = map[string]Engine{
	"vllm": {
		Provider:    "vllm",
		TokenMetric: "vllm:generation_tokens_total",
		MetricsPort: 8000,
	},
	"sglang": {
		Provider:    "sglang",
		TokenMetric: "sglang_generation_tokens_total",
		MetricsPort: 30000,
	},
	"triton": { //nolint:gosec // G101 false positive: metric name, not a credential
		Provider:    "triton",
		TokenMetric: "nv_inference_request_success (token approximation)",
		MetricsPort: 8002,
	},
	// TGI is in maintenance mode (since 2025-12-11) and served through the
	// generic-prometheus provider with TGI metric names pre-configured.
	"tgi": {
		Provider:    "generic-prometheus",
		TokenMetric: "tgi_request_generated_tokens_total",
		MetricsPort: 3000,
		Config: map[string]string{
			"output_tokens_metric":    "tgi_request_generated_tokens_total",
			"requests_running_metric": "tgi_queue_size",
			"model_name_label":        "model_id",
		},
	},
}

// fallbackEngine is used when a pod carries the inference-provider annotation
// with a value Aitra does not recognise. The operator opted in explicitly, so
// we fall back to generic-prometheus; metric names must then be supplied via
// provider config.
var fallbackEngine = Engine{
	Provider:    "generic-prometheus",
	TokenMetric: "inference_output_tokens_total (generic default — configure output_tokens_metric)",
	MetricsPort: 8080,
}

// Detection is the result of engine detection for one pod.
type Detection struct {
	Engine Engine
	// Source is one of SourceAnnotation, SourceAppLabel, SourceFallback.
	Source string
	// Matched is the annotation or label value that triggered detection.
	Matched string
}

// Detect determines the inference engine for a pod from its labels and
// annotations. ok is false when the pod carries no recognisable signal —
// such pods are not inference servers as far as Aitra is concerned.
func Detect(labels, annotations map[string]string) (Detection, bool) {
	if v := annotations[AnnotationInferenceProvider]; v != "" {
		if eng, known := knownEngines[v]; known {
			return Detection{Engine: eng, Source: SourceAnnotation, Matched: v}, true
		}
		// Explicit opt-in with an unknown engine name → generic fallback.
		return Detection{Engine: fallbackEngine, Source: SourceFallback, Matched: v}, true
	}
	if v := labels[labelApp]; v != "" {
		if eng, known := knownEngines[v]; known {
			return Detection{Engine: eng, Source: SourceAppLabel, Matched: v}, true
		}
	}
	return Detection{}, false
}

// PodDetection couples a Detection with the pod it came from and the
// concrete metrics endpoint derived from the pod IP.
type PodDetection struct {
	Namespace string
	Pod       string
	Endpoint  string // http://<pod-ip>:<port>/metrics
	Detection Detection
}

// ProviderConfig builds the config map to pass to provider.NewInference:
// the engine's preset keys plus the discovered endpoint.
func (p PodDetection) ProviderConfig() map[string]string {
	cfg := map[string]string{}
	for k, v := range p.Detection.Engine.Config {
		cfg[k] = v
	}
	cfg["endpoint"] = p.Endpoint
	return cfg
}

// DiscoverNodeInference lists Running pods on the given node and returns a
// detection for every pod that identifies as an inference server (explicit
// annotation or recognised app label). Pods without an assigned IP are
// skipped — their endpoint cannot be built yet. Results are sorted by
// namespace/name so the first entry is stable across calls.
func DiscoverNodeInference(ctx context.Context, client kubernetes.Interface, node string) ([]PodDetection, error) {
	list, err := client.CoreV1().Pods(metav1.NamespaceAll).List(ctx, metav1.ListOptions{
		FieldSelector: "spec.nodeName=" + node + ",status.phase=Running",
	})
	if err != nil {
		return nil, fmt.Errorf("list pods on %s: %w", node, err)
	}
	var out []PodDetection
	for i := range list.Items {
		pod := &list.Items[i]
		// Always filter client-side — FieldSelector is a server hint, not a
		// guarantee (fake clients ignore it).
		if pod.Spec.NodeName != node || pod.Status.Phase != corev1.PodRunning {
			continue
		}
		det, ok := Detect(pod.Labels, pod.Annotations)
		if !ok {
			continue
		}
		if pod.Status.PodIP == "" {
			continue
		}
		out = append(out, PodDetection{
			Namespace: pod.Namespace,
			Pod:       pod.Name,
			Endpoint:  fmt.Sprintf("http://%s:%d/metrics", pod.Status.PodIP, det.Engine.MetricsPort),
			Detection: det,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Namespace != out[j].Namespace {
			return out[i].Namespace < out[j].Namespace
		}
		return out[i].Pod < out[j].Pod
	})
	return out, nil
}
