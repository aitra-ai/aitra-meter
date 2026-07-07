// Package k8s provides Kubernetes-backed implementations of the aggregation
// interfaces PodLookup and NodeHardware.
package k8s

import (
	"context"
	"errors"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/aitra-ai/aitra-meter/internal/aggregation"
)

const (
	// Annotation keys on inference pods.
	annotWorkload   = "aitra-ai.github.io/workload"
	annotPrecision  = "aitra-ai.github.io/precision"
	annotTeam       = "aitra-ai.github.io/team"
	annotCostCentre = "aitra-ai.github.io/cost-centre"

	// labelModelName is set on pods by the measurement agent (or cluster operator)
	// to match pods to the model they serve.
	labelModelName = "aitra-ai.github.io/model-name"

	// llm-d (https://llm-d.ai) labels its inference pods with the serving phase
	// and model. Aitra Meter reads both so disaggregated prefill/decode
	// deployments get per-phase attribution with no llm-d-side changes.
	labelLLMDRole  = "llm-d.ai/role"  // "prefill" | "decode"
	labelLLMDModel = "llm-d.ai/model" // ModelService name, a DNS-1123 label
)

// ErrNoPod is returned when no matching pod is found for a (node, model) pair.
var ErrNoPod = errors.New("no matching pod found")

// PodMetaLookup implements aggregation.PodLookup using direct Kubernetes API
// calls. Each call issues a filtered List; this is safe for current traffic
// volumes (one call per measurement window, typically every 30–60 seconds).
// A cache layer can be added transparently behind the same interface later.
type PodMetaLookup struct {
	client kubernetes.Interface
}

// NewPodMetaLookup creates a PodMetaLookup backed by the given Kubernetes client.
func NewPodMetaLookup(client kubernetes.Interface) *PodMetaLookup {
	return &PodMetaLookup{client: client}
}

// ByNodeAndModel finds a Running pod on node serving modelName, then extracts
// namespace, Aitra annotations, and llm-d labels. Selection order:
//
//  1. A pod whose aitra-ai.github.io/model-name label equals modelName, or
//     whose llm-d.ai/model label matches it (see llmdModelMatches).
//  2. Otherwise, the first candidate pod: one with no aitra-ai.github.io/model-name
//     label, matching any model (useful for single-model deployments), or with
//     a label equal to modelName.
//
// The llm-d.ai/model label is only ever a positive match hint — a pod is never
// excluded because of it, since llm-d ModelService names rarely equal the model
// name the inference server reports (label values cannot contain "/").
func (p *PodMetaLookup) ByNodeAndModel(ctx context.Context, node, modelName string) (aggregation.PodMeta, error) {
	list, err := p.client.CoreV1().Pods(metav1.NamespaceAll).List(ctx, metav1.ListOptions{
		FieldSelector: "spec.nodeName=" + node + ",status.phase=Running",
	})
	if err != nil {
		return aggregation.PodMeta{}, fmt.Errorf("list pods on %s: %w", node, err)
	}
	var fallback *corev1.Pod
	for i := range list.Items {
		pod := &list.Items[i]
		// Always filter client-side — FieldSelector is a server hint, not a guarantee
		// (fake clients ignore it; some k8s versions don't support all field selectors).
		if pod.Spec.NodeName != node {
			continue
		}
		if pod.Status.Phase != corev1.PodRunning {
			continue
		}
		ml := pod.Labels[labelModelName]
		if ml != "" && ml != modelName {
			continue
		}
		if ml == modelName || llmdModelMatches(pod.Labels[labelLLMDModel], modelName) {
			return podToMeta(pod), nil
		}
		if fallback == nil {
			fallback = pod
		}
	}
	if fallback != nil {
		return podToMeta(fallback), nil
	}
	return aggregation.PodMeta{}, ErrNoPod
}

// llmdModelMatches reports whether an llm-d.ai/model label value refers to
// modelName. Label values cannot contain "/", so llm-d deployments name the
// ModelService after the model in DNS-1123 form (e.g. "qwen3-32b" for
// "Qwen/Qwen3-32B"). The comparison therefore also tries the sanitized full
// model name and its path basename.
func llmdModelMatches(labelValue, modelName string) bool {
	if labelValue == "" || modelName == "" {
		return false
	}
	if labelValue == modelName {
		return true
	}
	lv := strings.ToLower(labelValue)
	if lv == sanitizeModelName(modelName) {
		return true
	}
	if idx := strings.LastIndex(modelName, "/"); idx >= 0 {
		return lv == sanitizeModelName(modelName[idx+1:])
	}
	return false
}

// sanitizeModelName lowercases s and replaces every character that is not
// valid in a Kubernetes label value ([a-z0-9-._]) with "-".
func sanitizeModelName(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '.', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	return b.String()
}

func podToMeta(pod *corev1.Pod) aggregation.PodMeta {
	ann := pod.Annotations
	return aggregation.PodMeta{
		Namespace:  pod.Namespace,
		Workload:   annotOr(ann, annotWorkload, "unknown"),
		Precision:  annotOr(ann, annotPrecision, "unknown"),
		Team:       ann[annotTeam],
		CostCentre: ann[annotCostCentre],
		Role:       pod.Labels[labelLLMDRole],
	}
}

func annotOr(ann map[string]string, key, fallback string) string {
	if v, ok := ann[key]; ok && v != "" {
		return v
	}
	return fallback
}

// StaticPodMetaLookup is a test-friendly implementation that resolves from
// a pre-built map keyed by "node/model". It does not require a cluster.
type StaticPodMetaLookup struct {
	pods map[string]aggregation.PodMeta
}

// NewStaticPodMetaLookup builds a StaticPodMetaLookup from the given pods.
func NewStaticPodMetaLookup(pods map[string]aggregation.PodMeta) *StaticPodMetaLookup {
	return &StaticPodMetaLookup{pods: pods}
}

func (s *StaticPodMetaLookup) ByNodeAndModel(_ context.Context, node, model string) (aggregation.PodMeta, error) {
	key := node + "/" + model
	if meta, ok := s.pods[key]; ok {
		return meta, nil
	}
	return aggregation.PodMeta{}, ErrNoPod
}
