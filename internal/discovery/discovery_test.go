package discovery

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// --- Detect ------------------------------------------------------------------

func TestDetect(t *testing.T) {
	tests := []struct {
		name         string
		labels       map[string]string
		annotations  map[string]string
		wantOK       bool
		wantProvider string
		wantSource   string
		wantMatched  string
		wantPort     int
	}{
		{
			name:         "annotation vllm",
			annotations:  map[string]string{AnnotationInferenceProvider: "vllm"},
			wantOK:       true,
			wantProvider: "vllm",
			wantSource:   SourceAnnotation,
			wantMatched:  "vllm",
			wantPort:     8000,
		},
		{
			name:         "annotation sglang",
			annotations:  map[string]string{AnnotationInferenceProvider: "sglang"},
			wantOK:       true,
			wantProvider: "sglang",
			wantSource:   SourceAnnotation,
			wantMatched:  "sglang",
			wantPort:     30000,
		},
		{
			name:         "annotation triton",
			annotations:  map[string]string{AnnotationInferenceProvider: "triton"},
			wantOK:       true,
			wantProvider: "triton",
			wantSource:   SourceAnnotation,
			wantMatched:  "triton",
			wantPort:     8002,
		},
		{
			name:         "annotation tgi maps to generic-prometheus",
			annotations:  map[string]string{AnnotationInferenceProvider: "tgi"},
			wantOK:       true,
			wantProvider: "generic-prometheus",
			wantSource:   SourceAnnotation,
			wantMatched:  "tgi",
			wantPort:     3000,
		},
		{
			name:         "app label sglang",
			labels:       map[string]string{"app": "sglang"},
			wantOK:       true,
			wantProvider: "sglang",
			wantSource:   SourceAppLabel,
			wantMatched:  "sglang",
			wantPort:     30000,
		},
		{
			name:         "annotation beats app label",
			labels:       map[string]string{"app": "vllm"},
			annotations:  map[string]string{AnnotationInferenceProvider: "sglang"},
			wantOK:       true,
			wantProvider: "sglang",
			wantSource:   SourceAnnotation,
			wantMatched:  "sglang",
			wantPort:     30000,
		},
		{
			name:         "unknown annotation value falls back to generic-prometheus",
			annotations:  map[string]string{AnnotationInferenceProvider: "my-custom-engine"},
			wantOK:       true,
			wantProvider: "generic-prometheus",
			wantSource:   SourceFallback,
			wantMatched:  "my-custom-engine",
			wantPort:     8080,
		},
		{
			name:   "unrecognised app label is not an inference pod",
			labels: map[string]string{"app": "postgres"},
			wantOK: false,
		},
		{
			name:   "no signals at all",
			wantOK: false,
		},
		{
			name:        "empty annotation value ignored, app label used",
			labels:      map[string]string{"app": "triton"},
			annotations: map[string]string{AnnotationInferenceProvider: ""},
			wantOK:      true, wantProvider: "triton", wantSource: SourceAppLabel, wantMatched: "triton", wantPort: 8002,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			det, ok := Detect(tc.labels, tc.annotations)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if !ok {
				return
			}
			if det.Engine.Provider != tc.wantProvider {
				t.Errorf("Provider = %q, want %q", det.Engine.Provider, tc.wantProvider)
			}
			if det.Source != tc.wantSource {
				t.Errorf("Source = %q, want %q", det.Source, tc.wantSource)
			}
			if det.Matched != tc.wantMatched {
				t.Errorf("Matched = %q, want %q", det.Matched, tc.wantMatched)
			}
			if det.Engine.MetricsPort != tc.wantPort {
				t.Errorf("MetricsPort = %d, want %d", det.Engine.MetricsPort, tc.wantPort)
			}
			if det.Engine.TokenMetric == "" {
				t.Error("TokenMetric is empty — startup logging needs it")
			}
		})
	}
}

// --- DiscoverNodeInference -----------------------------------------------------

func pod(name, ns, node, ip string, phase corev1.PodPhase, lbls, annots map[string]string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Namespace:   ns,
			Labels:      lbls,
			Annotations: annots,
		},
		Spec:   corev1.PodSpec{NodeName: node},
		Status: corev1.PodStatus{Phase: phase, PodIP: ip},
	}
}

func TestDiscoverNodeInference(t *testing.T) {
	client := fake.NewSimpleClientset(
		// Annotated vLLM pod on our node.
		pod("vllm-0", "prod", "node-1", "10.1.0.5", corev1.PodRunning,
			nil, map[string]string{AnnotationInferenceProvider: "vllm"}),
		// SGLang pod detected via app label.
		pod("sglang-0", "prod", "node-1", "10.1.0.6", corev1.PodRunning,
			map[string]string{"app": "sglang"}, nil),
		// Unrelated pod — must not be detected.
		pod("postgres-0", "db", "node-1", "10.1.0.7", corev1.PodRunning,
			map[string]string{"app": "postgres"}, nil),
		// Inference pod on a different node — must not be detected.
		pod("vllm-other", "prod", "node-2", "10.1.0.8", corev1.PodRunning,
			nil, map[string]string{AnnotationInferenceProvider: "vllm"}),
		// Pending pod — must not be detected.
		pod("sglang-pending", "prod", "node-1", "", corev1.PodPending,
			map[string]string{"app": "sglang"}, nil),
		// Running but no IP yet — must be skipped.
		pod("vllm-noip", "prod", "node-1", "", corev1.PodRunning,
			nil, map[string]string{AnnotationInferenceProvider: "vllm"}),
	)

	got, err := DiscoverNodeInference(context.Background(), client, "node-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d detections, want 2: %+v", len(got), got)
	}
	// Sorted by namespace/name: sglang-0 before vllm-0.
	if got[0].Pod != "sglang-0" || got[1].Pod != "vllm-0" {
		t.Errorf("order = [%s, %s], want [sglang-0, vllm-0]", got[0].Pod, got[1].Pod)
	}
	if got[0].Endpoint != "http://10.1.0.6:30000/metrics" {
		t.Errorf("sglang endpoint = %q, want http://10.1.0.6:30000/metrics", got[0].Endpoint)
	}
	if got[1].Endpoint != "http://10.1.0.5:8000/metrics" {
		t.Errorf("vllm endpoint = %q, want http://10.1.0.5:8000/metrics", got[1].Endpoint)
	}
	if got[0].Detection.Engine.Provider != "sglang" || got[1].Detection.Engine.Provider != "vllm" {
		t.Errorf("providers = [%s, %s], want [sglang, vllm]",
			got[0].Detection.Engine.Provider, got[1].Detection.Engine.Provider)
	}
}

func TestDiscoverNodeInferenceEmpty(t *testing.T) {
	client := fake.NewSimpleClientset(
		pod("postgres-0", "db", "node-1", "10.1.0.7", corev1.PodRunning,
			map[string]string{"app": "postgres"}, nil),
	)
	got, err := DiscoverNodeInference(context.Background(), client, "node-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("got %d detections, want 0", len(got))
	}
}

func TestProviderConfig(t *testing.T) {
	// TGI detection must carry its preset metric names plus the endpoint.
	det, ok := Detect(nil, map[string]string{AnnotationInferenceProvider: "tgi"})
	if !ok {
		t.Fatal("tgi not detected")
	}
	pd := PodDetection{
		Namespace: "prod", Pod: "tgi-0",
		Endpoint:  "http://10.0.0.9:3000/metrics",
		Detection: det,
	}
	cfg := pd.ProviderConfig()
	if cfg["endpoint"] != "http://10.0.0.9:3000/metrics" {
		t.Errorf("endpoint = %q", cfg["endpoint"])
	}
	if cfg["output_tokens_metric"] != "tgi_request_generated_tokens_total" {
		t.Errorf("output_tokens_metric = %q", cfg["output_tokens_metric"])
	}
	if cfg["requests_running_metric"] != "tgi_queue_size" {
		t.Errorf("requests_running_metric = %q", cfg["requests_running_metric"])
	}
	if cfg["model_name_label"] != "model_id" {
		t.Errorf("model_name_label = %q", cfg["model_name_label"])
	}

	// Engines without presets get only the endpoint.
	det, _ = Detect(nil, map[string]string{AnnotationInferenceProvider: "vllm"})
	cfg = PodDetection{Endpoint: "http://10.0.0.1:8000/metrics", Detection: det}.ProviderConfig()
	if len(cfg) != 1 || cfg["endpoint"] != "http://10.0.0.1:8000/metrics" {
		t.Errorf("vllm config = %v, want endpoint only", cfg)
	}
}
