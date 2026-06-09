package k8s

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

const (
	// labelGPUTier is the node label that identifies the GPU hardware tier.
	// Values: "h100", "h200", "l40s", "b200", "a100", etc.
	labelGPUTier = "aitra-ai.github.io/gpu"
)

// NodeHardwareLookup implements aggregation.NodeHardware using direct
// Kubernetes API calls. One call per measurement window is acceptable
// at Phase 1 volumes; a cache layer can be added behind the same interface.
type NodeHardwareLookup struct {
	client kubernetes.Interface
}

// NewNodeHardwareLookup creates a NodeHardwareLookup backed by the given client.
func NewNodeHardwareLookup(client kubernetes.Interface) *NodeHardwareLookup {
	return &NodeHardwareLookup{client: client}
}

// Hardware returns the value of the aitra-ai.github.io/gpu label for the named
// node. Returns "unknown" if the node is not found or the label is absent.
func (n *NodeHardwareLookup) Hardware(ctx context.Context, nodeName string) string {
	node, err := n.client.CoreV1().Nodes().Get(ctx, nodeName, metav1.GetOptions{})
	if err != nil {
		return "unknown"
	}
	if hw, ok := node.Labels[labelGPUTier]; ok && hw != "" {
		return hw
	}
	return "unknown"
}

// NodesByHardware returns all node names labelled with the given hardware tier.
func (n *NodeHardwareLookup) NodesByHardware(ctx context.Context, hardware string) ([]string, error) {
	list, err := n.client.CoreV1().Nodes().List(ctx, metav1.ListOptions{
		LabelSelector: labelGPUTier + "=" + hardware,
	})
	if err != nil {
		return nil, fmt.Errorf("list nodes: %w", err)
	}
	names := make([]string, len(list.Items))
	for i, node := range list.Items {
		names[i] = node.Name
	}
	return names, nil
}

// StaticNodeHardware returns a fixed hardware label for all nodes.
type StaticNodeHardware struct{ label string }

func NewStaticNodeHardware(label string) *StaticNodeHardware {
	return &StaticNodeHardware{label: label}
}

func (s *StaticNodeHardware) Hardware(_ context.Context, _ string) string { return s.label }

// MapNodeHardware returns per-node hardware labels from a static map.
// Returns "unknown" for nodes not in the map.
type MapNodeHardware struct{ m map[string]string }

func NewMapNodeHardware(m map[string]string) *MapNodeHardware { return &MapNodeHardware{m: m} }

func (m *MapNodeHardware) Hardware(_ context.Context, node string) string {
	if hw, ok := m.m[node]; ok {
		return hw
	}
	return "unknown"
}
