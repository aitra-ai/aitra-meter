// Package k8s provides Kubernetes-backed implementations of the aggregation
// interfaces PodLookup and NodeHardware.
package k8s

import (
	"context"
	"errors"
	"fmt"

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
)

// ErrNoPod is returned when no matching pod is found for a (node, model) pair.
var ErrNoPod = errors.New("no matching pod found")

// PodMetaLookup implements aggregation.PodLookup using direct Kubernetes API
// calls. Each call issues a filtered List; this is safe for Phase 1 traffic
// volumes (one call per measurement window, typically every 30–60 seconds).
// A cache layer can be added transparently behind the same interface later.
type PodMetaLookup struct {
	client kubernetes.Interface
}

// NewPodMetaLookup creates a PodMetaLookup backed by the given Kubernetes client.
func NewPodMetaLookup(client kubernetes.Interface) *PodMetaLookup {
	return &PodMetaLookup{client: client}
}

// ByNodeAndModel finds the first Running pod on node serving modelName, then
// extracts namespace and Aitra annotations. Pods without the model label match
// any model (useful for single-model deployments).
func (p *PodMetaLookup) ByNodeAndModel(ctx context.Context, node, modelName string) (aggregation.PodMeta, error) {
	list, err := p.client.CoreV1().Pods(metav1.NamespaceAll).List(ctx, metav1.ListOptions{
		FieldSelector: "spec.nodeName=" + node + ",status.phase=Running",
	})
	if err != nil {
		return aggregation.PodMeta{}, fmt.Errorf("list pods on %s: %w", node, err)
	}
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
		if ml := pod.Labels[labelModelName]; ml != "" && ml != modelName {
			continue
		}
		return podToMeta(pod), nil
	}
	return aggregation.PodMeta{}, ErrNoPod
}

func podToMeta(pod *corev1.Pod) aggregation.PodMeta {
	ann := pod.Annotations
	return aggregation.PodMeta{
		Namespace:  pod.Namespace,
		Workload:   annotOr(ann, annotWorkload, "unknown"),
		Precision:  annotOr(ann, annotPrecision, "unknown"),
		Team:       ann[annotTeam],
		CostCentre: ann[annotCostCentre],
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
