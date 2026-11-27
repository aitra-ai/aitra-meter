// integration_test.go — real API smoke test, only runs when
// WATTTIME_USERNAME / WATTTIME_PASSWORD / WATTTIME_REGION are all set.
//
// Run with:
//
//	WATTTIME_USERNAME=... WATTTIME_PASSWORD=... WATTTIME_REGION=... \
//	    go test -v -run TestWattTimeLiveIntegration ./internal/aggregation/carbon/
package carbon

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestWattTimeLiveIntegration(t *testing.T) {
	user := os.Getenv("WATTTIME_USERNAME")
	pass := os.Getenv("WATTTIME_PASSWORD")
	region := os.Getenv("WATTTIME_REGION")

	if user == "" || pass == "" || region == "" {
		t.Skip("skipping live integration test: set WATTTIME_USERNAME, WATTTIME_PASSWORD, WATTTIME_REGION to run")
	}

	client := NewWattTimeClient(user, pass, region, nil)

	t.Logf("WattTime client ready: region=%s", region)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	gco2, err := client.Fetch(ctx)
	if err != nil {
		t.Fatalf("Fetch() error: %v", err)
	}

	t.Logf("✅  Live MOER for %s = %.2f gCO2/kWh", region, gco2)

	if gco2 <= 0 {
		t.Errorf("expected positive gCO2/kWh value, got %v", gco2)
	}
	if gco2 > 2000 {
		t.Errorf("suspiciously high gCO2/kWh value: %v (unconverted lbs?)", gco2)
	}
	if client.Source() != "watttime" {
		t.Errorf("Source() = %q, want \"watttime\"", client.Source())
	}
}
