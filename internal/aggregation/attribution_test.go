package aggregation

import (
	"context"
	"errors"
	"testing"
)

// stubLookup implements PodLookup for testing.
type stubLookup struct {
	pods map[string]PodMeta // key: node+"/"+model
	err  error
}

func (s *stubLookup) ByNodeAndModel(_ context.Context, node, model string) (PodMeta, error) {
	if s.err != nil {
		return PodMeta{}, s.err
	}
	key := node + "/" + model
	if meta, ok := s.pods[key]; ok {
		return meta, nil
	}
	return PodMeta{}, errors.New("no pod found")
}

func TestResolverDirect(t *testing.T) {
	lookup := &stubLookup{pods: map[string]PodMeta{
		"node-1/llama": {
			Namespace: "inference-prod",
			Workload:  "chat",
			Precision: "fp16",
		},
	}}
	r := NewResolver(lookup, PolicyConfig{DefaultMethod: AttributionDirect})
	a := r.Resolve(context.Background(), "node-1", "llama")

	if a.Namespace != "inference-prod" {
		t.Errorf("Namespace = %q, want %q", a.Namespace, "inference-prod")
	}
	if a.Method != AttributionDirect {
		t.Errorf("Method = %q, want %q", a.Method, AttributionDirect)
	}
	if a.Workload != "chat" {
		t.Errorf("Workload = %q, want %q", a.Workload, "chat")
	}
}

func TestResolverRolePassthrough(t *testing.T) {
	// The llm-d.ai/role label ("prefill" | "decode") resolved by the pod
	// lookup must pass through Resolve unchanged (issue #49).
	tests := []struct {
		name string
		role string
	}{
		{name: "prefill", role: "prefill"},
		{name: "decode", role: "decode"},
		{name: "no role for conventional serving", role: ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			lookup := &stubLookup{pods: map[string]PodMeta{
				"node-1/qwen": {Namespace: "inference-prod", Role: tc.role},
			}}
			r := NewResolver(lookup, PolicyConfig{DefaultMethod: AttributionDirect})
			a := r.Resolve(context.Background(), "node-1", "qwen")
			if a.Role != tc.role {
				t.Errorf("Role = %q, want %q", a.Role, tc.role)
			}
		})
	}
}

func TestResolverRoleEmptyOnLookupError(t *testing.T) {
	// The best-effort fallback attribution must not invent a role.
	lookup := &stubLookup{err: errors.New("k8s unavailable")}
	r := NewResolver(lookup, PolicyConfig{})
	a := r.Resolve(context.Background(), "node-1", "qwen")
	if a.Role != "" {
		t.Errorf("Role = %q on lookup error, want empty", a.Role)
	}
}

func TestResolverNamespaceOverride(t *testing.T) {
	lookup := &stubLookup{pods: map[string]PodMeta{
		"node-1/llama": {Namespace: "inference-shared"},
	}}
	r := NewResolver(lookup, PolicyConfig{
		DefaultMethod: AttributionDirect,
		NamespaceOverrides: map[string]AttributionMethod{
			"inference-shared": AttributionProportional,
		},
	})
	a := r.Resolve(context.Background(), "node-1", "llama")

	if a.Method != AttributionProportional {
		t.Errorf("Method = %q, want proportional for overridden namespace", a.Method)
	}
}

func TestResolverLookupError(t *testing.T) {
	// When the pod lookup fails, Resolve must not propagate the error —
	// it returns a best-effort "unknown" attribution so measurements are never dropped.
	lookup := &stubLookup{err: errors.New("k8s unavailable")}
	r := NewResolver(lookup, PolicyConfig{})
	a := r.Resolve(context.Background(), "node-1", "llama")

	if a.Namespace != "unknown" {
		t.Errorf("Namespace = %q, want %q on lookup error", a.Namespace, "unknown")
	}
	if a.Method != AttributionDirect {
		t.Errorf("Method = %q, want default direct on lookup error", a.Method)
	}
}

func TestResolverDefaultMethodFallback(t *testing.T) {
	// Zero-value PolicyConfig should default to direct.
	lookup := &stubLookup{pods: map[string]PodMeta{
		"n/m": {Namespace: "ns"},
	}}
	r := NewResolver(lookup, PolicyConfig{})
	a := r.Resolve(context.Background(), "n", "m")
	if a.Method != AttributionDirect {
		t.Errorf("Method = %q, want direct as zero-value default", a.Method)
	}
}

func TestResolverNonOverriddenNamespace(t *testing.T) {
	// A namespace without an override uses DefaultMethod even when other
	// namespaces have overrides.
	lookup := &stubLookup{pods: map[string]PodMeta{
		"n/m": {Namespace: "inference-prod"},
	}}
	r := NewResolver(lookup, PolicyConfig{
		DefaultMethod: AttributionDirect,
		NamespaceOverrides: map[string]AttributionMethod{
			"inference-shared": AttributionProportional,
		},
	})
	a := r.Resolve(context.Background(), "n", "m")
	if a.Method != AttributionDirect {
		t.Errorf("Method = %q, want direct for non-overridden namespace", a.Method)
	}
}
