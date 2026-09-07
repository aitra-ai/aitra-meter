package promtext

import "testing"

// SGLang tensor-parallel: one series per rank, distinguished only by tp_rank.
// Four ranks at 1000 tokens each -> the model generated 4000.
const sglangTP4 = `# HELP sglang:generation_tokens_total Number of generation tokens processed.
# TYPE sglang:generation_tokens_total counter
sglang:generation_tokens_total{model_name="qwen2.5-32b",tp_rank="0",pp_rank="0",moe_ep_rank="0"} 1000
sglang:generation_tokens_total{model_name="qwen2.5-32b",tp_rank="1",pp_rank="0",moe_ep_rank="0"} 1000
sglang:generation_tokens_total{model_name="qwen2.5-32b",tp_rank="2",pp_rank="0",moe_ep_rank="0"} 1000
sglang:generation_tokens_total{model_name="qwen2.5-32b",tp_rank="3",pp_rank="0",moe_ep_rank="0"} 1000`

func lines(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	out = append(out, s[start:])
	return out
}

func TestSumAcrossTensorParallelRanks(t *testing.T) {
	got, ok := Sum(Parse(lines(sglangTP4)), "sglang:generation_tokens_total")
	if !ok {
		t.Fatal("metric not found")
	}
	if got != 4000 {
		t.Errorf("got %v, want 4000 (a value of 1000 is the collapse bug)", got)
	}
}

func TestSumMissingMetric(t *testing.T) {
	if _, ok := Sum(Parse(lines(sglangTP4)), "nonexistent"); ok {
		t.Error("expected not-found for absent metric")
	}
}

func TestSumFirstPrefersCurrentName(t *testing.T) {
	const body = `sglang_generation_tokens_total{tp_rank="0"} 500
sglang_generation_tokens_total{tp_rank="1"} 500`
	v, matched, ok := SumFirst(Parse(lines(body)),
		"sglang_generation_tokens_total", "sglang:generation_tokens_total")
	if !ok || v != 1000 || matched != "sglang_generation_tokens_total" {
		t.Errorf("got v=%v matched=%q ok=%v", v, matched, ok)
	}
}

func TestSumFirstFallsBackToLegacy(t *testing.T) {
	const body = `sglang:generation_tokens_total{tp_rank="0"} 700`
	v, matched, ok := SumFirst(Parse(lines(body)),
		"sglang_generation_tokens_total", "sglang:generation_tokens_total")
	if !ok || v != 700 || matched != "sglang:generation_tokens_total" {
		t.Errorf("got v=%v matched=%q ok=%v", v, matched, ok)
	}
}

func TestSingleLabelValueAgrees(t *testing.T) {
	v, _, ok := SingleLabelValue(Parse(lines(sglangTP4)),
		"sglang:generation_tokens_total", "model_name")
	if !ok || v != "qwen2.5-32b" {
		t.Errorf("got v=%q ok=%v, want qwen2.5-32b true", v, ok)
	}
}

func TestSingleLabelValueConflict(t *testing.T) {
	const body = `m{model_name="qwen2.5-7b",tp_rank="0"} 1
m{model_name="llama-3-8b",tp_rank="0"} 1`
	a, b, ok := SingleLabelValue(Parse(lines(body)), "m", "model_name")
	if ok {
		t.Fatal("expected conflict to be reported")
	}
	if (a != "qwen2.5-7b" && a != "llama-3-8b") || (b != "qwen2.5-7b" && b != "llama-3-8b") || a == b {
		t.Errorf("expected the two distinct model names, got %q and %q", a, b)
	}
}

func TestLabelValue(t *testing.T) {
	if got := LabelValue(`model_name="x",tp_rank="0"`, "model_name"); got != "x" {
		t.Errorf("got %q, want x", got)
	}
	if got := LabelValue(`tp_rank="0"`, "model_name"); got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

func TestParseSkipsCommentsAndHelp(t *testing.T) {
	m := Parse(lines(sglangTP4))
	if len(m) != 1 {
		t.Errorf("got %d metrics, want 1 (HELP/TYPE must be skipped)", len(m))
	}
}
