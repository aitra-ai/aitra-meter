package carbon

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func TestElectricityMapsFetch(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		body       string
		want       float64
		wantErr    bool
	}{
		{"successful response", 200, `{"carbonIntensity":245.6}`, 245.6, false},
		{"HTTP unauthorized", 401, `{"error":"unauthorized"}`, 0, true},
		{"HTTP server error", 500, `{"error":"server error"}`, 0, true},
		{"invalid JSON", 200, `not-json`, 0, true},
		{"missing carbon intensity", 200, `{"zone":"IN"}`, 0, true},
		{"negative intensity", 200, `{"carbonIntensity":-10}`, 0, true},
		{"zero intensity", 200, `{"carbonIntensity":0}`, 0, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(
				func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Query().Get("zone") != "IN" {
						t.Errorf("expected zone IN, got %q", r.URL.Query().Get("zone"))
					}
					if r.Header.Get("auth-token") != "test-token" {
						t.Error("auth-token header was not set correctly")
					}
					w.WriteHeader(tt.statusCode)
					_, _ = fmt.Fprint(w, tt.body)
				},
			))
			defer server.Close()

			client := NewElectricityMapsClient(
				"IN",
				"test-token",
				&http.Client{
					Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
						req.URL.Scheme = "http"
						req.URL.Host = strings.TrimPrefix(server.URL, "http://")
						return http.DefaultTransport.RoundTrip(req)
					}),
				},
			)

			got, err := client.Fetch(context.Background())

			if (err != nil) != tt.wantErr {
				t.Fatalf("Fetch() error = %v, wantErr %v", err, tt.wantErr)
			}
			if err == nil && got != tt.want {
				t.Errorf("Fetch() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestElectricityMapsFetchMissingConfiguration(t *testing.T) {
	tests := []struct {
		name  string
		zone  string
		token string
	}{
		{"missing zone", "", "test-token"},
		{"missing token", "IN", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := NewElectricityMapsClient(tt.zone, tt.token, nil)
			_, err := client.Fetch(context.Background())
			if err == nil {
				t.Fatal("expected configuration error")
			}
		})
	}
}

func TestElectricityMapsSource(t *testing.T) {
	c := NewElectricityMapsClient("IN", "tok", nil)
	if got := c.Source(); got != "electricitymaps" {
		t.Errorf("Source() = %q, want %q", got, "electricitymaps")
	}
}

// ---------------------------------------------------------------------------
// WattTime tests
// ---------------------------------------------------------------------------

// wattTimeTestServer sets up a test server that handles /login and
// /v3/signal-index with the provided responses.
func wattTimeTestServer(
	t *testing.T,
	loginStatus int, loginBody string,
	indexStatus int, indexBody string,
) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/login":
			if r.Header.Get("Authorization") == "" {
				t.Error("expected Authorization header on /login")
			}
			w.WriteHeader(loginStatus)
			_, _ = fmt.Fprint(w, loginBody)
		case "/v3/signal-index":
			if r.Header.Get("Authorization") == "" {
				t.Error("expected Authorization header on /v3/signal-index")
			}
			if r.URL.Query().Get("region") != "CAISO_NORTH" {
				t.Errorf("expected region CAISO_NORTH, got %q", r.URL.Query().Get("region"))
			}
			w.WriteHeader(indexStatus)
			_, _ = fmt.Fprint(w, indexBody)
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

func newWattTimeClientForTest(t *testing.T, server *httptest.Server) *WattTimeClient {
	t.Helper()
	return NewWattTimeClient(
		"user", "pass", "CAISO_NORTH",
		&http.Client{
			Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
				req.URL.Scheme = "http"
				req.URL.Host = strings.TrimPrefix(server.URL, "http://")
				return http.DefaultTransport.RoundTrip(req)
			}),
		},
	)
}

func TestWattTimeFetch(t *testing.T) {
	// 850 lbs/MWh × 0.453592 ≈ 385.55 gCO2/kWh
	const moer = 850.0
	const wantGCO2 = moer * lbsMWhToGCO2KWh

	server := wattTimeTestServer(t,
		http.StatusOK, `{"token":"jwt-abc"}`,
		http.StatusOK, fmt.Sprintf(`{"moer":%v}`, moer),
	)
	defer server.Close()

	client := newWattTimeClientForTest(t, server)
	got, err := client.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch() unexpected error: %v", err)
	}
	if got != wantGCO2 {
		t.Errorf("Fetch() = %v, want %v", got, wantGCO2)
	}
}

func TestWattTimeFetchLoginFailure(t *testing.T) {
	server := wattTimeTestServer(t,
		http.StatusUnauthorized, `{"detail":"invalid credentials"}`,
		http.StatusOK, `{}`,
	)
	defer server.Close()

	client := newWattTimeClientForTest(t, server)
	_, err := client.Fetch(context.Background())
	if err == nil {
		t.Fatal("expected error on login failure")
	}
}

func TestWattTimeFetchIndexFailure(t *testing.T) {
	server := wattTimeTestServer(t,
		http.StatusOK, `{"token":"jwt-abc"}`,
		http.StatusInternalServerError, `{"detail":"server error"}`,
	)
	defer server.Close()

	client := newWattTimeClientForTest(t, server)
	_, err := client.Fetch(context.Background())
	if err == nil {
		t.Fatal("expected error on index fetch failure")
	}
}

func TestWattTimeFetchMissingMOER(t *testing.T) {
	server := wattTimeTestServer(t,
		http.StatusOK, `{"token":"jwt-abc"}`,
		http.StatusOK, `{"region":"CAISO_NORTH"}`,
	)
	defer server.Close()

	client := newWattTimeClientForTest(t, server)
	_, err := client.Fetch(context.Background())
	if err == nil {
		t.Fatal("expected error for missing moer field")
	}
}

func TestWattTimeFetchNegativeMOER(t *testing.T) {
	server := wattTimeTestServer(t,
		http.StatusOK, `{"token":"jwt-abc"}`,
		http.StatusOK, `{"moer":-5}`,
	)
	defer server.Close()

	client := newWattTimeClientForTest(t, server)
	_, err := client.Fetch(context.Background())
	if err == nil {
		t.Fatal("expected error for negative moer")
	}
}

func TestWattTimeFetchMissingConfiguration(t *testing.T) {
	tests := []struct {
		name     string
		username string
		password string
		region   string
	}{
		{"missing username", "", "pass", "CAISO_NORTH"},
		{"missing password", "user", "", "CAISO_NORTH"},
		{"missing region", "user", "pass", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := NewWattTimeClient(tt.username, tt.password, tt.region, nil)
			_, err := client.Fetch(context.Background())
			if err == nil {
				t.Fatal("expected configuration error")
			}
		})
	}
}

func TestWattTimeSource(t *testing.T) {
	c := NewWattTimeClient("u", "p", "CAISO_NORTH", nil)
	if got := c.Source(); got != "watttime" {
		t.Errorf("Source() = %q, want %q", got, "watttime")
	}
}

// TestWattTimeFetchTokenExpiry401Retry validates the auto-reauth-on-401 path.
// The signal endpoint returns 401 on the first call (simulating an expired
// token) and 200 on the second, after re-authentication.
func TestWattTimeFetchTokenExpiry401Retry(t *testing.T) {
	const moer = 400.0
	const wantGCO2 = moer * lbsMWhToGCO2KWh

	var signalCallCount int

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/login":
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprint(w, `{"token":"fresh-jwt"}`)
		case "/v3/signal-index":
			signalCallCount++
			if signalCallCount == 1 {
				// First attempt: simulate an expired/invalid token.
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = fmt.Fprint(w, `{"detail":"token expired"}`)
			} else {
				// Second attempt after re-auth: succeed.
				w.WriteHeader(http.StatusOK)
				_, _ = fmt.Fprintf(w, `{"moer":%v}`, moer)
			}
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	client := newWattTimeClientForTest(t, server)
	got, err := client.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch() unexpected error after 401 retry: %v", err)
	}
	if got != wantGCO2 {
		t.Errorf("Fetch() = %v, want %v", got, wantGCO2)
	}
	if signalCallCount != 2 {
		t.Errorf("expected 2 signal calls (first 401, second success), got %d", signalCallCount)
	}
}
