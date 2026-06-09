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
		makeNode("node-1", map[string]string{labelGPUTier: "h100"}),
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
		makeNode("node-1", map[string]string{labelGPUTier: "h100"}),
		makeNode("node-2", map[string]string{labelGPUTier: "h100"}),
		makeNode("node-3", map[string]string{labelGPUTier: "l40s"}),
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
