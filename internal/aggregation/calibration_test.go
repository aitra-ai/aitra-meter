package aggregation

import "testing"

func TestCalibrationLookupHit(t *testing.T) {
	table := NewCalibrationTableFromMap(map[CalibrationKey]CalibrationEntry{
		{Model: "llama-3-8b", Hardware: "h100"}: {
			Tier:         TierAitraBenchmark,
			RefJPerToken: 0.31,
		},
	})
	e := table.Lookup("llama-3-8b", "h100")
	if e.Tier != TierAitraBenchmark {
		t.Errorf("Tier = %q, want aitra_benchmark", e.Tier)
	}
	if e.RefJPerToken != 0.31 {
		t.Errorf("RefJPerToken = %f, want 0.31", e.RefJPerToken)
	}
}

func TestCalibrationLookupMiss(t *testing.T) {
	table := NewCalibrationTableFromMap(nil)
	e := table.Lookup("unknown-model", "h100")
	if e.Tier != TierUncalibrated {
		t.Errorf("Tier = %q, want uncalibrated for unknown model", e.Tier)
	}
	if e.RefJPerToken != 0 {
		t.Errorf("RefJPerToken = %f, want 0 for uncalibrated", e.RefJPerToken)
	}
}

func TestCalibrationTierPriority(t *testing.T) {
	// When two entries exist for the same key, the higher-priority tier wins.
	keys := []CalibrationKey{
		{Model: "m", Hardware: "h"},
		{Model: "m", Hardware: "h"},
	}
	entries := []CalibrationEntry{
		{Tier: TierReference, RefJPerToken: 0.5},
		{Tier: TierAitraBenchmark, RefJPerToken: 0.3},
	}
	table := NewCalibrationTable(entries, keys)
	e := table.Lookup("m", "h")
	if e.Tier != TierAitraBenchmark {
		t.Errorf("Tier = %q, want aitra_benchmark (higher priority wins)", e.Tier)
	}
	if e.RefJPerToken != 0.3 {
		t.Errorf("RefJPerToken = %f, want 0.3", e.RefJPerToken)
	}
}

func TestCalibrationTierPriorityReversedOrder(t *testing.T) {
	// Same as above but inserted in reverse order — result must be identical.
	keys := []CalibrationKey{
		{Model: "m", Hardware: "h"},
		{Model: "m", Hardware: "h"},
	}
	entries := []CalibrationEntry{
		{Tier: TierAitraBenchmark, RefJPerToken: 0.3},
		{Tier: TierReference, RefJPerToken: 0.5},
	}
	table := NewCalibrationTable(entries, keys)
	e := table.Lookup("m", "h")
	if e.Tier != TierAitraBenchmark {
		t.Errorf("Tier = %q, want aitra_benchmark regardless of insertion order", e.Tier)
	}
}

func TestCalibrationDifferentHardware(t *testing.T) {
	table := NewCalibrationTableFromMap(map[CalibrationKey]CalibrationEntry{
		{Model: "qwen-27b", Hardware: "h100"}:  {Tier: TierReference, RefJPerToken: 0.42},
		{Model: "qwen-27b", Hardware: "l40s"}:  {Tier: TierReference, RefJPerToken: 0.61},
	})
	h100 := table.Lookup("qwen-27b", "h100")
	l40s := table.Lookup("qwen-27b", "l40s")

	if h100.RefJPerToken == l40s.RefJPerToken {
		t.Error("same J/token for different hardware tiers — keys are not being differentiated")
	}
	if h100.RefJPerToken != 0.42 {
		t.Errorf("h100 RefJPerToken = %f, want 0.42", h100.RefJPerToken)
	}
	if l40s.RefJPerToken != 0.61 {
		t.Errorf("l40s RefJPerToken = %f, want 0.61", l40s.RefJPerToken)
	}
}

func TestTierPriorityOrder(t *testing.T) {
	want := []CalibrationTier{
		TierAitraBenchmark,
		TierReference,
		TierSelfCalibrated,
		TierUncalibrated,
	}
	for i := 0; i < len(want)-1; i++ {
		if tierPriority(want[i]) >= tierPriority(want[i+1]) {
			t.Errorf("tier %q should have lower priority value than %q", want[i], want[i+1])
		}
	}
}
