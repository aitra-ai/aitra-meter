package dcgm

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/aitra-ai/aitra-meter/internal/provider"
)

// Interface compliance is mandatory (docs/guides/writing-a-provider.md).
var _ provider.EnergyProvider = (*DCGMProvider)(nil)

func TestImplementsInterface(t *testing.T) {
	var _ provider.EnergyProvider = (*DCGMProvider)(nil)
}

// TestRegisteredWithRegistry verifies init() wired the provider into the
// registry so the agent can select it by name (-energy-provider dcgm).
func TestRegisteredWithRegistry(t *testing.T) {
	p, err := provider.NewEnergy("dcgm", nil)
	if err != nil {
		t.Fatalf("NewEnergy(dcgm): %v", err)
	}
	if p.Name() != "dcgm" {
		t.Errorf("registered provider Name() = %q, want \"dcgm\"", p.Name())
	}
}

// energyBody renders a dcgm-exporter payload for the energy counter (mJ), one
// series per supplied per-GPU value.
func energyBody(mJ ...float64) string {
	s := "# HELP DCGM_FI_DEV_TOTAL_ENERGY_CONSUMPTION Total energy since boot (mJ).\n" +
		"# TYPE DCGM_FI_DEV_TOTAL_ENERGY_CONSUMPTION counter\n"
	for i, v := range mJ {
		s += fmt.Sprintf("DCGM_FI_DEV_TOTAL_ENERGY_CONSUMPTION{gpu=\"%d\",UUID=\"GPU-%d\",modelName=\"NVIDIA H100\"} %g\n", i, i, v)
	}
	return s
}

// powerBody renders a dcgm-exporter payload for the power gauge (watts).
func powerBody(w ...float64) string {
	s := "# TYPE DCGM_FI_DEV_POWER_USAGE gauge\n"
	for i, v := range w {
		s += fmt.Sprintf("DCGM_FI_DEV_POWER_USAGE{gpu=\"%d\",UUID=\"GPU-%d\",modelName=\"NVIDIA H100\"} %g\n", i, i, v)
	}
	return s
}

// flipServer serves a body the test can change between scrapes, so a single
// endpoint can simulate the energy counter advancing across a window.
type flipServer struct {
	mu   sync.Mutex
	body string
}

func (f *flipServer) set(b string) {
	f.mu.Lock()
	f.body = b
	f.mu.Unlock()
}

func (f *flipServer) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	f.mu.Lock()
	b := f.body
	f.mu.Unlock()
	fmt.Fprint(w, b) //nolint:errcheck
}

func newProvider(endpoint string) *DCGMProvider {
	return &DCGMProvider{
		endpoint:     endpoint,
		energyMetric: defaultEnergyMetric,
		powerMetric:  defaultPowerMetric,
		client:       &http.Client{},
		windows:      map[string]float64{},
	}
}

func TestName(t *testing.T) {
	if got := newProvider("").Name(); got != "dcgm" {
		t.Errorf("Name() = %q, want \"dcgm\"", got)
	}
}

func TestBeginEndWindowReturnsJoules(t *testing.T) {
	// Two GPUs. start = 1000+2000 mJ, end = 4000+6000 mJ.
	// delta = (3000+4000) mJ = 7000 mJ = 7 J.
	fs := &flipServer{body: energyBody(1000, 2000)}
	ts := httptest.NewServer(fs)
	defer ts.Close()
	p := newProvider(ts.URL)

	if err := p.BeginWindow(context.Background(), "w1"); err != nil {
		t.Fatalf("BeginWindow: %v", err)
	}
	fs.set(energyBody(4000, 6000))
	got, err := p.EndWindow(context.Background(), "w1")
	if err != nil {
		t.Fatalf("EndWindow: %v", err)
	}
	if want := 7.0; got != want {
		t.Errorf("EndWindow joules = %v, want %v", got, want)
	}
}

func TestEndWindowUnknownWindow(t *testing.T) {
	ts := httptest.NewServer(&flipServer{body: energyBody(1000)})
	defer ts.Close()
	p := newProvider(ts.URL)
	if _, err := p.EndWindow(context.Background(), "missing"); err == nil {
		t.Fatal("expected error for unknown window, got nil")
	}
}

func TestEndWindowNeverNegative(t *testing.T) {
	// Counter reset (driver reload): end < start must clamp to 0, not go negative.
	fs := &flipServer{body: energyBody(10000)}
	ts := httptest.NewServer(fs)
	defer ts.Close()
	p := newProvider(ts.URL)
	if err := p.BeginWindow(context.Background(), "w"); err != nil {
		t.Fatalf("BeginWindow: %v", err)
	}
	fs.set(energyBody(500))
	got, err := p.EndWindow(context.Background(), "w")
	if err != nil {
		t.Fatalf("EndWindow: %v", err)
	}
	if got != 0 {
		t.Errorf("EndWindow after counter reset = %v, want 0", got)
	}
}

func TestIdlePowerSumsDevices(t *testing.T) {
	ts := httptest.NewServer(&flipServer{body: powerBody(70, 80, 90)})
	defer ts.Close()
	p := newProvider(ts.URL)
	got, err := p.IdlePower(context.Background())
	if err != nil {
		t.Fatalf("IdlePower: %v", err)
	}
	if want := 240.0; got != want {
		t.Errorf("IdlePower = %v, want %v", got, want)
	}
}

func TestDevices(t *testing.T) {
	ts := httptest.NewServer(&flipServer{body: energyBody(1, 2)})
	defer ts.Close()
	p := newProvider(ts.URL)
	devs, err := p.Devices(context.Background())
	if err != nil {
		t.Fatalf("Devices: %v", err)
	}
	if len(devs) != 2 {
		t.Fatalf("got %d devices, want 2", len(devs))
	}
	if devs[0].ID != "GPU-0" || devs[0].Name != "NVIDIA H100" || devs[0].Type != "gpu" {
		t.Errorf("devices[0] = %+v, want {GPU-0 NVIDIA H100 gpu}", devs[0])
	}
}

func TestBeginWindowMetricAbsent(t *testing.T) {
	ts := httptest.NewServer(&flipServer{body: "# nothing useful here\n"})
	defer ts.Close()
	p := newProvider(ts.URL)
	if err := p.BeginWindow(context.Background(), "w"); err == nil {
		t.Fatal("expected error when energy metric absent, got nil")
	}
}

func TestScrapeUnreachable(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	addr := ts.URL
	ts.Close() // close before any request so the scrape fails
	p := newProvider(addr)
	if err := p.BeginWindow(context.Background(), "w"); err == nil {
		t.Fatal("expected connection error, got nil")
	}
}

func TestNewDefaultsAndOverrides(t *testing.T) {
	p := New(nil)
	if p.endpoint != defaultEndpoint || p.energyMetric != defaultEnergyMetric || p.powerMetric != defaultPowerMetric {
		t.Errorf("New(nil) defaults wrong: %+v", p)
	}
	p = New(map[string]string{"endpoint": "http://x:9400/metrics", "energy-metric": "FOO", "power-metric": "BAR"})
	if p.endpoint != "http://x:9400/metrics" || p.energyMetric != "FOO" || p.powerMetric != "BAR" {
		t.Errorf("New(config) overrides wrong: %+v", p)
	}
}

func TestMetricLine(t *testing.T) {
	cases := []struct {
		line   string
		metric string
		want   bool
	}{
		{`DCGM_FI_DEV_POWER_USAGE{gpu="0"} 70`, "DCGM_FI_DEV_POWER_USAGE", true},
		{`DCGM_FI_DEV_POWER_USAGE 70`, "DCGM_FI_DEV_POWER_USAGE", true},
		{`# HELP DCGM_FI_DEV_POWER_USAGE help text`, "DCGM_FI_DEV_POWER_USAGE", false},
		{`DCGM_FI_DEV_POWER_USAGE_LIMIT{} 1`, "DCGM_FI_DEV_POWER_USAGE", false},
	}
	for _, c := range cases {
		if got := metricLine(c.line, c.metric); got != c.want {
			t.Errorf("metricLine(%q, %q) = %v, want %v", c.line, c.metric, got, c.want)
		}
	}
}
