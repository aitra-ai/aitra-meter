package aggregation

import (
	"context"

	"github.com/aitra-ai/aitra-meter/internal/model"
)

// AttributionMethod is a type alias for model.AttributionMethod.
// Code in this package (including tests) may use the unqualified name.
type AttributionMethod = model.AttributionMethod

const (
	AttributionDirect       AttributionMethod = model.AttributionDirect
	AttributionProportional AttributionMethod = model.AttributionProportional
)

// PodMeta holds the Kubernetes metadata resolved for a workload pod.
type PodMeta struct {
	Namespace  string
	Workload   string // annotation aitra-ai.github.io/workload, or "unknown"
	Precision  string // annotation aitra-ai.github.io/precision, or "unknown"
	Team       string // annotation aitra-ai.github.io/team, or ""
	CostCentre string // annotation aitra-ai.github.io/cost-centre, or ""
}

// PodLookup is the interface the Resolver uses to fetch pod metadata from
// the Kubernetes API. The real implementation uses client-go; tests use a stub.
type PodLookup interface {
	// ByNodeAndModel returns metadata for the pod serving modelName on node.
	// Returns ErrNoPod if no matching pod is found.
	ByNodeAndModel(ctx context.Context, node, modelName string) (PodMeta, error)
}

// PolicyConfig carries the attribution settings from MeasurementPolicy.
type PolicyConfig struct {
	// DefaultMethod is used when a namespace has no explicit override.
	DefaultMethod AttributionMethod

	// NamespaceOverrides maps namespace name → override method.
	NamespaceOverrides map[string]AttributionMethod
}

// Attribution is the resolved output of a single Resolve call.
type Attribution struct {
	PodMeta
	Method AttributionMethod
}

// Resolver resolves attribution dimensions and method for a (node, modelName) pair.
type Resolver struct {
	lookup PodLookup
	policy PolicyConfig
}

// NewResolver creates a Resolver. lookup must not be nil.
func NewResolver(lookup PodLookup, policy PolicyConfig) *Resolver {
	if policy.DefaultMethod == "" {
		policy.DefaultMethod = AttributionDirect
	}
	return &Resolver{lookup: lookup, policy: policy}
}

// Resolve returns the Attribution for a measurement window identified by
// (node, modelName). If the pod lookup fails, it returns a best-effort
// Attribution with namespace "unknown" rather than propagating the error,
// so a lookup outage never drops measurements.
func (r *Resolver) Resolve(ctx context.Context, node, modelName string) Attribution {
	meta, err := r.lookup.ByNodeAndModel(ctx, node, modelName)
	if err != nil {
		meta = PodMeta{
			Namespace: "unknown",
			Workload:  "unknown",
			Precision: "unknown",
		}
	}
	method := r.methodFor(meta.Namespace)
	return Attribution{PodMeta: meta, Method: method}
}

func (r *Resolver) methodFor(namespace string) AttributionMethod {
	if override, ok := r.policy.NamespaceOverrides[namespace]; ok {
		return override
	}
	return r.policy.DefaultMethod
}
