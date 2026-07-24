package k8s

import (
	"context"
	"fmt"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

const (
	// labelNFDGPUProduct is published by Node Feature Discovery, which the
	// NVIDIA GPU Operator runs on every GPU node. Values are product strings
	// such as "NVIDIA-H100-PCIe-80GB" or "NVIDIA-A100-SXM4-40GB". This is read
	// first: any cluster running the GPU Operator already has it, so hardware is
	// identified without asking the operator to label anything.
	labelNFDGPUProduct = "nvidia.com/gpu.product"

	// labelHardwareOverride lets an operator name the hardware tier explicitly.
	// It takes precedence over the NFD label, for clusters that do not run NFD
	// or that want a tier name ("h100") rather than a product string.
	//
	// This is deliberately NOT the same key as the DaemonSet's nodeSelector.
	// Scheduling ("run the agent here") and identification ("this is an H100")
	// are different questions; one label cannot answer both.
	labelHardwareOverride = "aitra-ai.github.io/gpu-tier"

	hardwareUnknown = "unknown"
)

// NodeHardwareLookup implements aggregation.NodeHardware using direct
// Kubernetes API calls. One call per measurement window is acceptable
// at current volumes; a cache layer can be added behind the same interface.
type NodeHardwareLookup struct {
	client kubernetes.Interface
}

// NewNodeHardwareLookup creates a NodeHardwareLookup backed by the given client.
func NewNodeHardwareLookup(client kubernetes.Interface) *NodeHardwareLookup {
	return &NodeHardwareLookup{client: client}
}

// Hardware returns the GPU tier for the named node.
//
// Resolution order:
//
//  1. aitra-ai.github.io/gpu-tier, if set — the operator's explicit override.
//  2. nvidia.com/gpu.product, normalized — published by Node Feature Discovery.
//  3. "unknown".
//
// It never returns the empty string, and never returns a scheduling flag: a
// value of "true" or "false" is rejected, because that indicates the node was
// labelled with a selector value rather than a hardware tier.
func (n *NodeHardwareLookup) Hardware(ctx context.Context, nodeName string) string {
	node, err := n.client.CoreV1().Nodes().Get(ctx, nodeName, metav1.GetOptions{})
	if err != nil {
		return hardwareUnknown
	}
	if tier := sanitizeTier(node.Labels[labelHardwareOverride]); tier != "" {
		return tier
	}
	if tier := NormalizeGPUProduct(node.Labels[labelNFDGPUProduct]); tier != "" {
		return tier
	}
	return hardwareUnknown
}

// NodesByHardware returns all node names whose resolved hardware tier matches.
// Because a tier may come from either the override label or the NFD label, this
// resolves each node rather than issuing a single label selector.
func (n *NodeHardwareLookup) NodesByHardware(ctx context.Context, hardware string) ([]string, error) {
	list, err := n.client.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list nodes: %w", err)
	}
	var names []string
	for _, node := range list.Items {
		tier := sanitizeTier(node.Labels[labelHardwareOverride])
		if tier == "" {
			tier = NormalizeGPUProduct(node.Labels[labelNFDGPUProduct])
		}
		if tier == hardware {
			names = append(names, node.Name)
		}
	}
	return names, nil
}

// NormalizeGPUProduct maps a Node Feature Discovery product string to a tier.
//
//	"NVIDIA-H100-PCIe-80GB"  -> "h100"
//	"NVIDIA-A100-SXM4-40GB"  -> "a100"
//	"NVIDIA-L40S"            -> "l40s"
//	"Tesla-V100-SXM2-16GB"   -> "v100"
//
// The tier is the first token that is not a vendor or form-factor word. Where no
// such token is found the normalized full string is returned rather than
// discarding the information: an unrecognized GPU is still worth labelling
// distinctly, and comparing measurements across two unknown GPUs is worse than
// comparing them across two named ones.
//
// Returns "" for the empty string, so callers can distinguish "absent" from
// "present but unrecognized".
func NormalizeGPUProduct(product string) string {
	if product == "" {
		return ""
	}
	parts := strings.Split(strings.ToLower(product), "-")
	for _, p := range parts {
		if p == "" || vendorOrFormFactor[p] {
			continue
		}
		return p
	}
	return strings.ToLower(product)
}

// vendorOrFormFactor are tokens that identify the manufacturer or the physical
// packaging rather than the GPU model, and are skipped when deriving a tier.
var vendorOrFormFactor = map[string]bool{
	"nvidia": true,
	"tesla":  true,
	"amd":    true,
	"intel":  true,
	"pcie":   true,
	"sxm":    true,
	"sxm2":   true,
	"sxm4":   true,
	"sxm5":   true,
	"nvl":    true,
}

// sanitizeTier rejects boolean-looking values. The DaemonSet's nodeSelector is a
// presence flag ("true"); a hardware tier is a model name. If the two are ever
// pointed at the same key again, this makes the mistake visible as "unknown"
// rather than silently stamping every measurement hardware="true".
func sanitizeTier(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "", "true", "false", "1", "0", "yes", "no":
		return ""
	}
	return strings.ToLower(strings.TrimSpace(v))
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
	return hardwareUnknown
}
