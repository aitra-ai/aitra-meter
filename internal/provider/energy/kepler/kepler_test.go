package kepler

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aitra-ai/aitra-meter/internal/provider"
)

// Interface compliance is mandatory (docs/guides/writing-a-provider.md).
var _ provider.EnergyProvider = (*KeplerProvider)(nil)

func TestImplementsInterface(t *testing.T) {
	var _ provider.EnergyProvider = (*KeplerProvider)(nil)
}

// TestRegisteredWithRegistry verifies init() wired the provider into the
// registry so the agent can select it by name (-energy-provider kepler).
func TestRegisteredWithRegistry(t *testing.T) {
	p, err := provider.NewEnergy("kepler", map[string]string{"endpoint": "http://prometheus.monitoring.svc:9090"})
	if err != nil {
		t.Fatalf("NewEnergy(kepler): %v", err)
	}
	if p.Name() != "kepler" {
		t.Errorf("registered provider Name() = %q, want \"kepler\"", p.Name())
	}
	if _, err := provider.NewEnergy("kepler", nil); err == nil {
		t.Error("NewEnergy(kepler) without endpoint: expected error, got nil")
	}
}

func TestNewConfig(t *testing.T) {
	cases := []struct {
		name    string
		config  map[string]string
		wantErr bool
		check   func(t *testing.T, p *KeplerProvider)
	}{
		{
			name:    "missing endpoint",
			config:  map[string]string{},
			wantErr: true,
		},
		{
			name:    "relative endpoint",
			config:  map[string]string{"endpoint": "prometheus:9090"},
			wantErr: true,
		},
		{
			name:    "invalid scrape_interval",
			config:  map[string]string{"endpoint": "http://p:9090", "scrape_interval": "banana"},
			wantErr: true,
		},
		{
			name:    "negative scrape_interval",
			config:  map[string]string{"endpoint": "http://p:9090", "scrape_interval": "-5s"},
			wantErr: true,
		},
		{
			name:   "defaults",
			config: map[string]string{"endpoint": "http://p:9090"},
			check: func(t *testing.T, p *KeplerProvider) {
				if p.containerLabel != "container" {
					t.Errorf("containerLabel = %q, want \"container\"", p.containerLabel)
				}
				if p.containerName != "" {
					t.Errorf("containerName = %q, want \"\"", p.containerName)
				}
				if p.scrapeInterval != 30*time.Second {
					t.Errorf("scrapeInterval = %v, want 30s", p.scrapeInterval)
				}
			},
		},
		{
			name: "overrides",
			config: map[string]string{
				"endpoint":        "http://p:9090",
				"container_label": "container_name",
				"container_name":  "vllm",
				"scrape_interval": "15s",
			},
			check: func(t *testing.T, p *KeplerProvider) {
				if p.containerLabel != "container_name" || p.containerName != "vllm" || p.scrapeInterval != 15*time.Second {
					t.Errorf("overrides not applied: %+v", p)
				}
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p, err := New(c.config)
			if c.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			if c.check != nil {
				c.check(t, p)
			}
		})
	}
}

func TestBuildScrapeURL(t *testing.T) {
	cases := []struct {
		name     string
		endpoint string
		want     string
		wantErr  bool
	}{
		{
			name:     "bare Prometheus base becomes federate",
			endpoint: "http://prometheus-operated.monitoring.svc.cluster.local:9090",
			want:     "http://prometheus-operated.monitoring.svc.cluster.local:9090/federate?match%5B%5D=kepler_container_joules_total&match%5B%5D=kepler_node_package_joules_total",
		},
		{
			name:     "trailing slash also becomes federate",
			endpoint: "http://p:9090/",
			want:     "http://p:9090/federate?match%5B%5D=kepler_container_joules_total&match%5B%5D=kepler_node_package_joules_total",
		},
		{
			name:     "explicit metrics path used as-is",
			endpoint: "http://kepler.kepler.svc.cluster.local:9102/metrics",
			want:     "http://kepler.kepler.svc.cluster.local:9102/metrics",
		},
		{
			name:     "not absolute",
			endpoint: "prometheus:9090",
			wantErr:  true,
		},
		{
			name:     "unparseable",
			endpoint: "http://bad host/",
			wantErr:  true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := buildScrapeURL(c.endpoint)
			if c.wantErr {
				if err == nil {
					t.Fatalf("expected error, got %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("buildScrapeURL: %v", err)
			}
			if got != c.want {
				t.Errorf("buildScrapeURL(%q) = %q, want %q", c.endpoint, got, c.want)
			}
		})
	}
}

// containerBody renders Kepler container counter series, one per name=joules pair.
func containerBody(pairs ...[2]string) string {
	s := "# HELP kepler_container_joules_total Aggregated RAPL value in container in joules\n" +
		"# TYPE kepler_container_joules_total counter\n"
	for _, p := range pairs {
		s += fmt.Sprintf("kepler_container_joules_total{container=%q,pod=\"pod-a\",namespace=\"inference\",mode=\"dynamic\"} %s\n", p[0], p[1])
	}
	return s
}

// nodeBody renders Kepler node package counter series, one per value.
func nodeBody(joules ...float64) string {
	s := "# TYPE kepler_node_package_joules_total counter\n"
	for i, v := range joules {
		s += fmt.Sprintf("kepler_node_package_joules_total{package=\"%d\",mode=\"dynamic\"} %g\n", i, v)
	}
	return s
}

// flipServer serves a body the test can change between scrapes, so a single
// endpoint can simulate the Kepler counters advancing across a window.
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

// newTestProvider builds a provider that scrapes endpoint as-is with a zero
// scrape interval so tests need no wall-clock waits.
func newTestProvider(t *testing.T, endpoint string, config map[string]string) *KeplerProvider {
	t.Helper()
	if config == nil {
		config = map[string]string{}
	}
	config["endpoint"] = endpoint
	p, err := New(config)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	p.scrapeInterval = 0
	return p
}

func TestName(t *testing.T) {
	p := newTestProvider(t, "http://p:9090", nil)
	if got := p.Name(); got != "kepler" {
		t.Errorf("Name() = %q, want \"kepler\"", got)
	}
}

// TestWindowDelta covers the BeginWindow/EndWindow counter-delta behaviour:
// normal deltas, idle zero-delta windows, counter resets and label filtering.
func TestWindowDelta(t *testing.T) {
	cases := []struct {
		name   string
		config map[string]string
		begin  string
		end    string
		want   float64
	}{
		{
			name:  "delta summed across containers",
			begin: containerBody([2]string{"vllm", "1000"}, [2]string{"sidecar", "2000"}),
			end:   containerBody([2]string{"vllm", "1500"}, [2]string{"sidecar", "2600"}),
			want:  1100, // (1500-1000) + (2600-2000)
		},
		{
			name:  "zero-delta idle window",
			begin: containerBody([2]string{"vllm", "1000"}),
			end:   containerBody([2]string{"vllm", "1000"}),
			want:  0,
		},
		{
			name:  "counter reset clamps to zero",
			begin: containerBody([2]string{"vllm", "10000"}),
			end:   containerBody([2]string{"vllm", "500"}),
			want:  0,
		},
		{
			name:   "container_name filter excludes non-matching containers",
			config: map[string]string{"container_name": "vllm"},
			begin:  containerBody([2]string{"vllm", "100"}, [2]string{"sidecar", "1000"}),
			end:    containerBody([2]string{"vllm", "250"}, [2]string{"sidecar", "9999"}),
			want:   150, // sidecar's +8999 must not leak in
		},
		{
			name:   "custom container_label",
			config: map[string]string{"container_label": "container_name", "container_name": "vllm"},
			begin: "kepler_container_joules_total{container_name=\"vllm\"} 10\n" +
				"kepler_container_joules_total{container_name=\"other\"} 100\n",
			end: "kepler_container_joules_total{container_name=\"vllm\"} 40\n" +
				"kepler_container_joules_total{container_name=\"other\"} 900\n",
			want: 30,
		},
		{
			name: "federate timestamps and spaced label values tolerated",
			begin: "kepler_container_joules_total{container=\"vllm\",node=\"gpu node 1\"} 100 1712345678901\n" +
				"kepler_node_package_joules_total{package=\"0\"} 5 1712345678901\n",
			end:  "kepler_container_joules_total{container=\"vllm\",node=\"gpu node 1\"} 350 1712345708901\n",
			want: 250,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fs := &flipServer{body: c.begin}
			ts := httptest.NewServer(fs)
			defer ts.Close()
			p := newTestProvider(t, ts.URL+"/metrics", c.config)

			if err := p.BeginWindow(context.Background(), "w1"); err != nil {
				t.Fatalf("BeginWindow: %v", err)
			}
			fs.set(c.end)
			got, err := p.EndWindow(context.Background(), "w1")
			if err != nil {
				t.Fatalf("EndWindow: %v", err)
			}
			if got != c.want {
				t.Errorf("EndWindow joules = %v, want %v", got, c.want)
			}
		})
	}
}

func TestEndWindowUnknownWindow(t *testing.T) {
	ts := httptest.NewServer(&flipServer{body: containerBody([2]string{"vllm", "1"})})
	defer ts.Close()
	p := newTestProvider(t, ts.URL+"/metrics", nil)
	if _, err := p.EndWindow(context.Background(), "missing"); err == nil {
		t.Fatal("expected error for unknown window, got nil")
	}
}

// TestBeginWindowErrors covers missing metrics, filters that match nothing and
// malformed responses — each must produce a clear error, never a silent zero.
func TestBeginWindowErrors(t *testing.T) {
	cases := []struct {
		name    string
		config  map[string]string
		body    string
		status  int
		wantSub string
	}{
		{
			name:    "metric absent",
			body:    "# nothing useful here\n",
			wantSub: "kepler_container_joules_total",
		},
		{
			name:    "only other kepler metrics present",
			body:    "kepler_pod_joules_total{pod=\"p\"} 42\n" + nodeBody(10),
			wantSub: "not found",
		},
		{
			name:    "container_name matches nothing",
			config:  map[string]string{"container_name": "vllm"},
			body:    containerBody([2]string{"sidecar", "100"}),
			wantSub: `container="vllm"`,
		},
		{
			name:    "container_label missing from all series",
			config:  map[string]string{"container_label": "container_name"},
			body:    containerBody([2]string{"vllm", "100"}),
			wantSub: "container_name",
		},
		{
			name:    "malformed sample value",
			body:    "kepler_container_joules_total{container=\"vllm\"} banana\n",
			wantSub: "kepler_container_joules_total",
		},
		{
			name:    "unclosed label block",
			body:    "kepler_container_joules_total{container=\"vllm\" 12\n",
			wantSub: "kepler_container_joules_total",
		},
		{
			name:    "HTML instead of exposition",
			body:    "<html><body>Prometheus Time Series Collection</body></html>\n",
			wantSub: "not found",
		},
		{
			name:    "HTTP 500",
			body:    "boom",
			status:  http.StatusInternalServerError,
			wantSub: "unexpected status",
		},
		{
			name:    "HTTP 404",
			body:    "not here",
			status:  http.StatusNotFound,
			wantSub: "unexpected status",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if c.status != 0 {
					w.WriteHeader(c.status)
				}
				fmt.Fprint(w, c.body) //nolint:errcheck
			}))
			defer ts.Close()
			p := newTestProvider(t, ts.URL+"/metrics", c.config)
			err := p.BeginWindow(context.Background(), "w")
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), c.wantSub) {
				t.Errorf("error %q does not mention %q", err.Error(), c.wantSub)
			}
		})
	}
}

func TestScrapeUnreachable(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	addr := ts.URL
	ts.Close() // close before any request so the scrape fails
	p := newTestProvider(t, addr+"/metrics", nil)
	if err := p.BeginWindow(context.Background(), "w"); err == nil {
		t.Fatal("expected connection error, got nil")
	}
}

// TestFederateRequest verifies that a bare Prometheus base endpoint is read
// through /federate with match[] selectors for both Kepler metric families.
func TestFederateRequest(t *testing.T) {
	var gotPath string
	var gotMatch []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotMatch = r.URL.Query()["match[]"]
		// Federation output carries timestamps after the value.
		fmt.Fprint(w, "kepler_container_joules_total{container=\"vllm\"} 123 1712345678901\n") //nolint:errcheck
	}))
	defer ts.Close()

	p := newTestProvider(t, ts.URL, nil) // no path — federate mode
	if err := p.BeginWindow(context.Background(), "w"); err != nil {
		t.Fatalf("BeginWindow via federate: %v", err)
	}
	if gotPath != "/federate" {
		t.Errorf("scrape path = %q, want \"/federate\"", gotPath)
	}
	want := []string{containerMetric, nodeMetric}
	if len(gotMatch) != 2 || gotMatch[0] != want[0] || gotMatch[1] != want[1] {
		t.Errorf("match[] = %v, want %v", gotMatch, want)
	}
}

func TestIdlePower(t *testing.T) {
	fs := &flipServer{body: nodeBody(1000, 2000)}
	ts := httptest.NewServer(fs)
	defer ts.Close()

	p := newTestProvider(t, ts.URL+"/metrics", map[string]string{"scrape_interval": "30s"})
	clock := time.Unix(1700000000, 0)
	p.now = func() time.Time { return clock }
	p.scrapeInterval = 30 * time.Second

	// First call primes the sample — no rate can be computed yet.
	if _, err := p.IdlePower(context.Background()); err == nil {
		t.Fatal("first IdlePower call: expected priming error, got nil")
	}

	// Too soon: within scrape_interval and no previous value to fall back on.
	clock = clock.Add(10 * time.Second)
	if _, err := p.IdlePower(context.Background()); err == nil {
		t.Fatal("IdlePower within scrape_interval with no cached value: expected error, got nil")
	}

	// After 30s (from the priming sample) with +9000 J across both packages: 300 W.
	clock = clock.Add(20 * time.Second)
	fs.set(nodeBody(4000, 8000))
	got, err := p.IdlePower(context.Background())
	if err != nil {
		t.Fatalf("IdlePower: %v", err)
	}
	if want := 300.0; got != want {
		t.Errorf("IdlePower = %v W, want %v W", got, want)
	}

	// A call inside the next scrape_interval returns the cached value.
	clock = clock.Add(5 * time.Second)
	fs.set(nodeBody(9999, 9999))
	got, err = p.IdlePower(context.Background())
	if err != nil {
		t.Fatalf("IdlePower (cached): %v", err)
	}
	if want := 300.0; got != want {
		t.Errorf("cached IdlePower = %v W, want %v W", got, want)
	}

	// Counter reset (Kepler restart) clamps to 0 W, not negative.
	clock = clock.Add(30 * time.Second)
	fs.set(nodeBody(1, 2))
	got, err = p.IdlePower(context.Background())
	if err != nil {
		t.Fatalf("IdlePower (after reset): %v", err)
	}
	if got != 0 {
		t.Errorf("IdlePower after counter reset = %v W, want 0", got)
	}
}

func TestIdlePowerMetricAbsent(t *testing.T) {
	ts := httptest.NewServer(&flipServer{body: containerBody([2]string{"vllm", "1"})})
	defer ts.Close()
	p := newTestProvider(t, ts.URL+"/metrics", nil)
	if _, err := p.IdlePower(context.Background()); err == nil {
		t.Fatal("expected error when node package metric absent, got nil")
	}
}

func TestDevicesSyntheticEntry(t *testing.T) {
	p := newTestProvider(t, "http://p:9090", nil)
	devs, err := p.Devices(context.Background())
	if err != nil {
		t.Fatalf("Devices: %v", err)
	}
	if len(devs) != 1 {
		t.Fatalf("got %d devices, want 1 synthetic entry", len(devs))
	}
	if devs[0].ID == "" || devs[0].Type != "other" {
		t.Errorf("devices[0] = %+v, want non-empty ID and Type \"other\"", devs[0])
	}
}

func TestMetricLine(t *testing.T) {
	cases := []struct {
		line   string
		metric string
		want   bool
	}{
		{`kepler_container_joules_total{container="a"} 1`, containerMetric, true},
		{`kepler_container_joules_total 1`, containerMetric, true},
		{`# HELP kepler_container_joules_total help`, containerMetric, false},
		{`kepler_container_joules_total_created{} 1`, containerMetric, false},
		{`kepler_node_package_joules_total{package="0"} 1`, containerMetric, false},
	}
	for _, c := range cases {
		if got := metricLine(c.line, c.metric); got != c.want {
			t.Errorf("metricLine(%q, %q) = %v, want %v", c.line, c.metric, got, c.want)
		}
	}
}

func TestSampleValue(t *testing.T) {
	cases := []struct {
		name string
		line string
		want float64
		ok   bool
	}{
		{"plain", `kepler_container_joules_total{container="a"} 12.5`, 12.5, true},
		{"no labels", `kepler_container_joules_total 7`, 7, true},
		{"federate timestamp", `kepler_container_joules_total{container="a"} 12.5 1712345678901`, 12.5, true},
		{"space in label value", `kepler_container_joules_total{container="a",node="gpu node 1"} 3`, 3, true},
		{"escaped quote in label value", `kepler_container_joules_total{container="a\"b"} 4`, 4, true},
		{"non-numeric value", `kepler_container_joules_total{container="a"} banana`, 0, false},
		{"missing value", `kepler_container_joules_total{container="a"}`, 0, false},
		{"unclosed labels", `kepler_container_joules_total{container="a" 12`, 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := sampleValue(c.line, containerMetric)
			if ok != c.ok || got != c.want {
				t.Errorf("sampleValue(%q) = (%v, %v), want (%v, %v)", c.line, got, ok, c.want, c.ok)
			}
		})
	}
}

func TestExtractLabel(t *testing.T) {
	cases := []struct {
		name  string
		line  string
		label string
		want  string
	}{
		{"first label", `m{container="vllm",pod="p"} 1`, "container", "vllm"},
		{"mid label", `m{pod="p",container="vllm"} 1`, "container", "vllm"},
		{"absent", `m{pod="p"} 1`, "container", ""},
		{"suffix key must not match", `m{pod_container="x"} 1`, "container", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := extractLabel(c.line, c.label); got != c.want {
				t.Errorf("extractLabel(%q, %q) = %q, want %q", c.line, c.label, got, c.want)
			}
		})
	}
}
