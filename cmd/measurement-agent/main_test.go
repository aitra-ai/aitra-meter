package main

import (
	"reflect"
	"testing"
)

func TestInferenceConfigFromEnv(t *testing.T) {
	tests := []struct {
		name    string
		environ []string
		want    map[string]string
	}{
		{
			name:    "empty environment",
			environ: nil,
			want:    map[string]string{},
		},
		{
			name: "unrelated vars ignored",
			environ: []string{
				"PATH=/usr/bin",
				"NODE_NAME=gpu-node-1",
				"INFERENCE_ENDPOINT=http://localhost:8000/metrics", // legacy var, handled separately
			},
			want: map[string]string{},
		},
		{
			name: "keys lowercased and prefix stripped",
			environ: []string{
				"INFERENCE_CONFIG_ENDPOINT=http://localhost:8002/metrics",
				"INFERENCE_CONFIG_AVG_OUTPUT_TOKENS_PER_REQUEST=250",
				"INFERENCE_CONFIG_OUTPUT_TOKENS_METRIC=tgi_request_generated_tokens_total",
			},
			want: map[string]string{
				"endpoint":                      "http://localhost:8002/metrics",
				"avg_output_tokens_per_request": "250",
				"output_tokens_metric":          "tgi_request_generated_tokens_total",
			},
		},
		{
			name: "empty values and bare prefix skipped",
			environ: []string{
				"INFERENCE_CONFIG_ENDPOINT=",
				"INFERENCE_CONFIG_=oops",
				"INFERENCE_CONFIG_MODEL_NAME_LABEL=model_id",
			},
			want: map[string]string{
				"model_name_label": "model_id",
			},
		},
		{
			name: "value may contain equals signs",
			environ: []string{
				"INFERENCE_CONFIG_ENDPOINT=http://localhost:8080/metrics?a=b",
			},
			want: map[string]string{
				"endpoint": "http://localhost:8080/metrics?a=b",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := inferenceConfigFromEnv(tt.environ)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("inferenceConfigFromEnv(%v) = %v, want %v", tt.environ, got, tt.want)
			}
		})
	}
}
