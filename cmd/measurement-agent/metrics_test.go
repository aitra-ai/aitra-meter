package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestMetricsMuxEndpoints verifies the agent answers the paths the Helm chart
// targets: the liveness and readiness probes and the ServiceMonitor scrape.
func TestMetricsMuxEndpoints(t *testing.T) {
	mux := newMetricsMux()
	for _, path := range []string{"/healthz", "/readyz", "/metrics"} {
		t.Run(path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
			if rec.Code != http.StatusOK {
				t.Errorf("GET %s = %d, want %d", path, rec.Code, http.StatusOK)
			}
		})
	}
}
