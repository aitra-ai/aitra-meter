// MIG attribution in the measurement loop (issue #43).
//
// When the energy provider implements provider.MIGEnergyProvider and reports
// MIG mode, the loop ends each window with EndWindowMIG and records the
// aitra_mig_* metrics here. Token attribution follows the v0.9.0 scope: all
// output tokens from the node's inference server are attributed to the one
// MIG slice that server is pinned to (via CUDA_VISIBLE_DEVICES on the
// inference pod, named to the agent with --mig-instance). Multi-tenant
// pod-to-slice mapping is out of scope; see docs/reference/mig-support.md.

package agent

import (
	"github.com/aitra-ai/aitra-meter/internal/metrics"
	"github.com/aitra-ai/aitra-meter/internal/provider"
)

// joulesPerKWh is the number of joules in one kilowatt-hour.
const joulesPerKWh = 3_600_000.0

// MIGAttribution configures how per-slice MIG metrics are labelled and
// whether the cost counter is derived. Only consulted on nodes where the
// energy provider detects MIG mode.
type MIGAttribution struct {
	// PinnedInstance names the slice the node's inference server is pinned
	// to, either as a mig_instance label ("mig-1g.10gb:0") or a MIG device
	// UUID ("MIG-…", the CUDA_VISIBLE_DEVICES value of the inference pod).
	// Empty: tokens are auto-attributed only when the node exposes exactly
	// one MIG slice; with several slices and no pin, token metrics stay
	// absent (power is still recorded per slice).
	PinnedInstance string

	// Namespace of the pinned inference pod. Label value only.
	// Defaults to "unknown".
	Namespace string

	// Team label for aitra_mig_cost_usd_total. Defaults to "unknown".
	Team string

	// ElectricityCostPerKWh is the electricity price in USD/kWh used for
	// aitra_mig_cost_usd_total. Zero disables the cost counter.
	ElectricityCostPerKWh float64
}

// observeMIGWindow records the aitra_mig_* metrics for one measurement window.
//
// Per-slice power is recorded for every slice. Token-derived metrics
// (tokens, J/token, cost) are recorded only for the pinned slice and only
// for serving windows (tokenDelta > 0).
func observeMIGWindow(node, model string, att MIGAttribution, slices []provider.MIGSliceEnergy, tokenDelta uint64) {
	if len(slices) == 0 {
		return
	}
	if att.Namespace == "" {
		att.Namespace = "unknown"
	}
	if att.Team == "" {
		att.Team = "unknown"
	}
	if model == "" {
		model = "unknown"
	}

	for _, se := range slices {
		metrics.MIGPowerWatts.
			WithLabelValues(node, se.Slice.ParentUUID, se.Slice.Instance).
			Set(se.PowerWatts)
	}

	pinned, ok := resolvePinnedSlice(att.PinnedInstance, slices)
	if !ok || tokenDelta == 0 {
		return
	}

	metrics.MIGTokensTotal.
		WithLabelValues(node, pinned.Slice.ParentUUID, pinned.Slice.Instance, att.Namespace, model).
		Add(float64(tokenDelta))
	metrics.MIGJPerToken.
		WithLabelValues(node, pinned.Slice.ParentUUID, pinned.Slice.Instance, att.Namespace, model).
		Set(pinned.Joules / float64(tokenDelta))
	if att.ElectricityCostPerKWh > 0 {
		metrics.MIGCostUSDTotal.
			WithLabelValues(node, pinned.Slice.ParentUUID, pinned.Slice.Instance, att.Namespace, att.Team).
			Add(pinned.Joules / joulesPerKWh * att.ElectricityCostPerKWh)
	}
}

// resolvePinnedSlice finds the slice tokens should be attributed to.
// pin matches either the mig_instance label or the MIG device UUID. An empty
// pin resolves only when there is exactly one slice on the node.
func resolvePinnedSlice(pin string, slices []provider.MIGSliceEnergy) (provider.MIGSliceEnergy, bool) {
	if pin == "" {
		if len(slices) == 1 {
			return slices[0], true
		}
		return provider.MIGSliceEnergy{}, false
	}
	for _, se := range slices {
		if se.Slice.Instance == pin || (se.Slice.UUID != "" && se.Slice.UUID == pin) {
			return se, true
		}
	}
	return provider.MIGSliceEnergy{}, false
}
