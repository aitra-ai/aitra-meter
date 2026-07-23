package k8s

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/aitra-ai/aitra-meter/internal/aggregation"
)

// --- helpers ----------------------------------------------------------------

func makePod(name, ns, node string, phase corev1.PodPhase, lbls, annots map[string]string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Namespace:   ns,
			Labels:      lbls,
			Annotations: annots,
		},
		Spec:   corev1.PodSpec{NodeName: node},
		Status: corev1.PodStatus{Phase: phase},
	}
}

func makeNode(name string, lbls map[string]string) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: lbls},
	}
}

// --- StaticPodMetaLookup ----------------------------------------------------

func TestStaticPodMetaLookupHit(t *testing.T) {
	l := NewStaticPodMetaLookup(map[string]aggregation.PodMeta{
		"node-1/llama": {Namespace: "prod", Workload: "chat", Precision: "fp16"},
	})
	meta, err := l.ByNodeAndModel(context.Background(), "node-1", "llama")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if meta.Namespace != "prod" {
		t.Errorf("Namespace = %q, want prod", meta.Namespace)
	}
}

func TestStaticPodMetaLookupMiss(t *testing.T) {
	l := NewStaticPodMetaLookup(nil)
	if _, err := l.ByNodeAndModel(context.Background(), "n", "m"); err == nil {
		t.Fatal("expected error for missing pod, got nil")
	}
}

// --- PodMetaLookup with fake client -----------------------------------------

func TestPodMetaLookupAllAnnotations(t *testing.T) {
	client := fake.NewSimpleClientset(
		makePod("vllm-0", "prod", "node-1", corev1.PodRunning,
			map[string]string{labelModelName: "llama-3-8b"},
			map[string]string{
				annotWorkload:   "chat",
				annotPrecision:  "fp16",
				annotTeam:       "platform",
				annotCostCentre: "cc-101",
			},
		),
	)
	lookup := NewPodMetaLookup(client)
	meta, err := lookup.ByNodeAndModel(context.Background(), "node-1", "llama-3-8b")
	if err != nil {
		t.Fatalf("ByNodeAndModel: %v", err)
	}
	checks := []struct{ field, got, want string }{
		{"Namespace", meta.Namespace, "prod"},
		{"Workload", meta.Workload, "chat"},
		{"Precision", meta.Precision, "fp16"},
		{"Team", meta.Team, "platform"},
		{"CostCentre", meta.CostCentre, "cc-101"},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %q, want %q", c.field, c.got, c.want)
		}
	}
}

func TestPodMetaLookupSkipsWrongNode(t *testing.T) {
	client := fake.NewSimpleClientset(
		makePod("vllm-0", "prod", "node-2", corev1.PodRunning,
			map[string]string{labelModelName: "llama"}, nil),
	)
	_, err := NewPodMetaLookup(client).ByNodeAndModel(context.Background(), "node-1", "llama")
	if err == nil {
		t.Fatal("expected ErrNoPod for wrong node, got nil")
	}
}

func TestPodMetaLookupSkipsNonRunning(t *testing.T) {
	client := fake.NewSimpleClientset(
		makePod("vllm-0", "prod", "node-1", corev1.PodPending,
			map[string]string{labelModelName: "llama"}, nil),
	)
	_, err := NewPodMetaLookup(client).ByNodeAndModel(context.Background(), "node-1", "llama")
	if err == nil {
		t.Fatal("expected ErrNoPod for non-Running pod, got nil")
	}
}

func TestPodMetaLookupSkipsWrongModel(t *testing.T) {
	client := fake.NewSimpleClientset(
		makePod("vllm-0", "prod", "node-1", corev1.PodRunning,
			map[string]string{labelModelName: "qwen"}, nil),
	)
	_, err := NewPodMetaLookup(client).ByNodeAndModel(context.Background(), "node-1", "llama")
	if err == nil {
		t.Fatal("expected ErrNoPod for mismatched model label, got nil")
	}
}

func TestPodMetaLookupNoLabelMatchesAnyModel(t *testing.T) {
	// Pod without model label matches any modelName.
	client := fake.NewSimpleClientset(
		makePod("vllm-0", "prod", "node-1", corev1.PodRunning,
			nil, map[string]string{annotWorkload: "batch"}),
	)
	meta, err := NewPodMetaLookup(client).ByNodeAndModel(context.Background(), "node-1", "any-model")
	if err != nil {
		t.Fatalf("ByNodeAndModel: %v", err)
	}
	if meta.Workload != "batch" {
		t.Errorf("Workload = %q, want batch", meta.Workload)
	}
}

func TestPodMetaLookupMissingAnnotationsFallback(t *testing.T) {
	client := fake.NewSimpleClientset(
		makePod("vllm-0", "ns-a", "node-1", corev1.PodRunning, nil, nil),
	)
	meta, err := NewPodMetaLookup(client).ByNodeAndModel(context.Background(), "node-1", "m")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if meta.Workload != "unknown" {
		t.Errorf("Workload = %q, want unknown for missing annotation", meta.Workload)
	}
	if meta.Precision != "unknown" {
		t.Errorf("Precision = %q, want unknown for missing annotation", meta.Precision)
	}
}

// --- llm-d labels (issue #49) -------------------------------------------------

func TestPodMetaLookupLLMDRole(t *testing.T) {
	// The llm-d.ai/role label is read into PodMeta.Role.
	for _, role := range []string{"prefill", "decode"} {
		t.Run(role, func(t *testing.T) {
			client := fake.NewSimpleClientset(
				makePod("llm-d-0", "inference-prod", "node-1", corev1.PodRunning,
					map[string]string{labelLLMDRole: role, labelLLMDModel: "qwen3-32b"},
					nil),
			)
			meta, err := NewPodMetaLookup(client).ByNodeAndModel(context.Background(), "node-1", "Qwen/Qwen3-32B")
			if err != nil {
				t.Fatalf("ByNodeAndModel: %v", err)
			}
			if meta.Role != role {
				t.Errorf("Role = %q, want %q", meta.Role, role)
			}
		})
	}
}

func TestPodMetaLookupRoleEmptyWithoutLabel(t *testing.T) {
	client := fake.NewSimpleClientset(
		makePod("vllm-0", "prod", "node-1", corev1.PodRunning, nil, nil),
	)
	meta, err := NewPodMetaLookup(client).ByNodeAndModel(context.Background(), "node-1", "m")
	if err != nil {
		t.Fatalf("ByNodeAndModel: %v", err)
	}
	if meta.Role != "" {
		t.Errorf("Role = %q for pod without llm-d.ai/role, want empty", meta.Role)
	}
}

func TestPodMetaLookupPrefersLLMDModelMatch(t *testing.T) {
	// With two llm-d pods on the same node serving different models, the pod
	// whose llm-d.ai/model matches the reported model name wins — even when a
	// non-matching candidate is listed first.
	client := fake.NewSimpleClientset(
		makePod("a-other", "prod", "node-1", corev1.PodRunning,
			map[string]string{labelLLMDModel: "llama-3-8b", labelLLMDRole: "decode"}, nil),
		makePod("b-match", "prod", "node-1", corev1.PodRunning,
			map[string]string{labelLLMDModel: "qwen3-32b", labelLLMDRole: "prefill"}, nil),
	)
	meta, err := NewPodMetaLookup(client).ByNodeAndModel(context.Background(), "node-1", "Qwen/Qwen3-32B")
	if err != nil {
		t.Fatalf("ByNodeAndModel: %v", err)
	}
	if meta.Role != "prefill" {
		t.Errorf("Role = %q, want prefill from the llm-d.ai/model-matching pod", meta.Role)
	}
}

func TestPodMetaLookupLLMDModelNeverExcludes(t *testing.T) {
	// A non-matching llm-d.ai/model must not exclude the pod: llm-d
	// ModelService names rarely equal the served model name, and dropping the
	// pod would regress attribution to namespace="unknown".
	client := fake.NewSimpleClientset(
		makePod("llm-d-0", "inference-prod", "node-1", corev1.PodRunning,
			map[string]string{labelLLMDModel: "my-service", labelLLMDRole: "decode"}, nil),
	)
	meta, err := NewPodMetaLookup(client).ByNodeAndModel(context.Background(), "node-1", "Qwen/Qwen3-32B")
	if err != nil {
		t.Fatalf("ByNodeAndModel: %v", err)
	}
	if meta.Namespace != "inference-prod" {
		t.Errorf("Namespace = %q, want inference-prod (fallback candidate)", meta.Namespace)
	}
	if meta.Role != "decode" {
		t.Errorf("Role = %q, want decode", meta.Role)
	}
}

func TestPodMetaLookupAitraLabelBeatsLLMDHint(t *testing.T) {
	// The explicit aitra-ai.github.io/model-name label stays authoritative:
	// a pod with a matching aitra label is selected over an earlier pod whose
	// llm-d.ai/model does not match.
	client := fake.NewSimpleClientset(
		makePod("a-llmd", "ns-a", "node-1", corev1.PodRunning,
			map[string]string{labelLLMDModel: "other-model"}, nil),
		makePod("b-aitra", "ns-b", "node-1", corev1.PodRunning,
			map[string]string{labelModelName: "llama"}, nil),
	)
	meta, err := NewPodMetaLookup(client).ByNodeAndModel(context.Background(), "node-1", "llama")
	if err != nil {
		t.Fatalf("ByNodeAndModel: %v", err)
	}
	if meta.Namespace != "ns-b" {
		t.Errorf("Namespace = %q, want ns-b (exact aitra model-name match)", meta.Namespace)
	}
}

func TestLLMDModelMatches(t *testing.T) {
	tests := []struct {
		name       string
		labelValue string
		modelName  string
		want       bool
	}{
		{"exact", "qwen3-32b", "qwen3-32b", true},
		{"sanitized full name", "qwen-qwen3-32b", "Qwen/Qwen3-32B", true},
		{"sanitized basename", "qwen3-32b", "Qwen/Qwen3-32B", true},
		{"case-insensitive label", "Qwen3-32B", "qwen3-32b", true},
		{"dots preserved", "llama-3.1-8b-instruct", "meta-llama/Llama-3.1-8B-Instruct", true},
		{"different model", "llama-3-8b", "Qwen/Qwen3-32B", false},
		{"empty label", "", "Qwen/Qwen3-32B", false},
		{"empty model", "qwen3-32b", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := llmdModelMatches(tc.labelValue, tc.modelName); got != tc.want {
				t.Errorf("llmdModelMatches(%q, %q) = %v, want %v", tc.labelValue, tc.modelName, got, tc.want)
			}
		})
	}
}

func TestSanitizeModelName(t *testing.T) {
	tests := []struct{ in, want string }{
		{"Qwen/Qwen3-32B", "qwen-qwen3-32b"},
		{"meta-llama/Llama-3.1-8B-Instruct", "meta-llama-llama-3.1-8b-instruct"},
		{"already-valid_name.v2", "already-valid_name.v2"},
		{"", ""},
	}
	for _, tc := range tests {
		if got := sanitizeModelName(tc.in); got != tc.want {
			t.Errorf("sanitizeModelName(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// --- annotOr ----------------------------------------------------------------

func TestAnnotOrFallback(t *testing.T) {
	if got := annotOr(nil, "k", "fb"); got != "fb" {
		t.Errorf("nil map: got %q, want fb", got)
	}
	if got := annotOr(map[string]string{"k": ""}, "k", "fb"); got != "fb" {
		t.Errorf("empty string: got %q, want fb", got)
	}
	if got := annotOr(map[string]string{"k": "v"}, "k", "fb"); got != "v" {
		t.Errorf("present: got %q, want v", got)
	}
}

// --- NodeHardwareLookup -----------------------------------------------------

func TestNodeHardwareLookupHit(t *testing.T) {
	client := fake.NewSimpleClientset(
		makeNode("node-1", map[string]string{labelHardwareOverride: "h100"}),
	)
	hw := NewNodeHardwareLookup(client)
	if got := hw.Hardware(context.Background(), "node-1"); got != "h100" {
		t.Errorf("Hardware = %q, want h100", got)
	}
}

func TestNodeHardwareLookupMissingLabel(t *testing.T) {
	client := fake.NewSimpleClientset(makeNode("node-1", nil))
	hw := NewNodeHardwareLookup(client)
	if got := hw.Hardware(context.Background(), "node-1"); got != "unknown" {
		t.Errorf("Hardware = %q, want unknown", got)
	}
}

func TestNodeHardwareLookupUnknownNode(t *testing.T) {
	client := fake.NewSimpleClientset()
	hw := NewNodeHardwareLookup(client)
	if got := hw.Hardware(context.Background(), "node-99"); got != "unknown" {
		t.Errorf("Hardware = %q, want unknown for missing node", got)
	}
}

func TestNodesByHardware(t *testing.T) {
	client := fake.NewSimpleClientset(
		makeNode("node-1", map[string]string{labelHardwareOverride: "h100"}),
		makeNode("node-2", map[string]string{labelHardwareOverride: "h100"}),
		makeNode("node-3", map[string]string{labelHardwareOverride: "l40s"}),
	)
	hw := NewNodeHardwareLookup(client)
	nodes, err := hw.NodesByHardware(context.Background(), "h100")
	if err != nil {
		t.Fatalf("NodesByHardware: %v", err)
	}
	if len(nodes) != 2 {
		t.Errorf("got %d h100 nodes, want 2: %v", len(nodes), nodes)
	}
}

// --- Static / Map helpers ---------------------------------------------------

func TestStaticNodeHardware(t *testing.T) {
	hw := NewStaticNodeHardware("l40s")
	if got := hw.Hardware(context.Background(), "any"); got != "l40s" {
		t.Errorf("Hardware = %q, want l40s", got)
	}
}

func TestMapNodeHardware(t *testing.T) {
	hw := NewMapNodeHardware(map[string]string{"node-1": "h100"})
	if got := hw.Hardware(context.Background(), "node-1"); got != "h100" {
		t.Errorf("node-1: got %q, want h100", got)
	}
	if got := hw.Hardware(context.Background(), "node-99"); got != "unknown" {
		t.Errorf("node-99: got %q, want unknown", got)
	}
}
