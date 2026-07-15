// Package promtext parses the subset of the Prometheus text exposition format
// that inference providers need, preserving each series' label set.
//
// It exists because every inference provider was parsing /metrics by hand, and
// each hand-rolled parser made the same mistake: it stripped the label set and
// keyed by bare metric name, so a metric exposed with several label sets kept
// only whichever series appeared last in the response body. On a server that
// shards a model across GPUs — SGLang labels every metric with tp_rank, pp_rank
// and moe_ep_rank, so a tensor-parallel deployment exposes one series per rank —
// that silently returned a fraction of the true value and inflated J/token by
// the parallelism degree.
//
// Parsing once, correctly, in a shared place removes the opportunity to
// reintroduce the bug per provider.
package promtext

import (
	"strconv"
	"strings"
)

// Sample is one Prometheus series: its raw label set (the text between the
// braces, as written) and its value.
type Sample struct {
	Labels string
	Value  float64
}

// Parse reads Prometheus text exposition lines into metric name -> series.
// A metric exposed with several label sets yields one Sample per series rather
// than a single collapsed value. Comment and HELP/TYPE lines are ignored.
func Parse(lines []string) map[string][]Sample {
	out := map[string][]Sample{}
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) < 2 {
			continue
		}
		name, labels := parts[0], ""
		if i := strings.Index(name, "{"); i > 0 {
			if j := strings.LastIndex(name, "}"); j > i {
				labels = name[i+1 : j]
			}
			name = name[:i]
		}
		val, err := strconv.ParseFloat(parts[len(parts)-1], 64)
		if err != nil {
			continue
		}
		out[name] = append(out[name], Sample{Labels: labels, Value: val})
	}
	return out
}

// Sum totals the values of every series of a metric. This is the correct
// aggregation for a counter sharded across ranks: the tokens a model generated
// are the sum over its shards. Returns 0 and false if the metric is absent.
func Sum(series map[string][]Sample, metric string) (float64, bool) {
	samples, ok := series[metric]
	if !ok || len(samples) == 0 {
		return 0, false
	}
	var total float64
	for _, s := range samples {
		total += s.Value
	}
	return total, true
}

// SumFirst returns the summed value of the first metric name present, trying
// each in order. Inference servers sometimes expose the same counter under more
// than one name across versions (for example SGLang's "sglang_generation_tokens_total"
// and the legacy "sglang:generation_tokens_total"); this tries the current name
// first and falls back to the legacy one. Returns the matched name so callers
// can report which was used.
func SumFirst(series map[string][]Sample, metrics ...string) (value float64, matched string, ok bool) {
	for _, m := range metrics {
		if v, found := Sum(series, m); found {
			return v, m, true
		}
	}
	return 0, "", false
}

// LabelValue extracts one label's value from a raw label set, or "" if absent.
func LabelValue(labels, label string) string {
	key := label + `="`
	idx := strings.Index(labels, key)
	if idx < 0 {
		return ""
	}
	start := idx + len(key)
	end := strings.Index(labels[start:], `"`)
	if end < 0 {
		return ""
	}
	return labels[start : start+end]
}

// SingleLabelValue returns the value of a label across all series of a metric,
// or an error-signalling empty string is left to the caller. It returns the
// value and true if every series that carries the label agrees on it; it
// returns the conflicting pair and false if they disagree, so a caller can
// refuse to attribute one endpoint's energy to two models. If no series carries
// the label, it returns "", "", true (caller decides the default).
func SingleLabelValue(series map[string][]Sample, metric, label string) (value, conflict string, consistent bool) {
	found := ""
	for _, s := range series[metric] {
		v := LabelValue(s.Labels, label)
		if v == "" {
			continue
		}
		if found == "" {
			found = v
			continue
		}
		if v != found {
			return found, v, false
		}
	}
	return found, "", true
}
