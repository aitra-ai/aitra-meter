package taskmeter

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"
)

type staticSource map[string]Efficiency

func (s staticSource) Efficiency(model string) Efficiency { return s[model] }

func TestMeterAccumulatesAcrossModels(t *testing.T) {
	m := New(staticSource{
		"small": {JPT: 2, SystemJPT: 3, At: time.Now()},
		"large": {JPT: 10, At: time.Now()},
	})
	m.CostPerKWh = 0.36 // makes 3.6e6 J cost exactly $0.36

	m.Record("task-1", "small", 100, 50)  // gpu 100 J, system 150 J
	m.Record("task-1", "large", 200, 30)  // gpu 300 J, system unmeasured
	m.Record("task-2", "small", 10, 10)   // separate task

	got, ok := m.Get("task-1")
	if !ok {
		t.Fatal("task-1 missing")
	}
	if got.Calls != 2 || got.OutputTokens != 80 {
		t.Errorf("calls/tokens = %d/%d, want 2/80", got.Calls, got.OutputTokens)
	}
	if got.GPUJoules != 400 {
		t.Errorf("gpu joules = %v, want 400", got.GPUJoules)
	}
	// System total only includes measured calls — never zero-padded.
	if got.SystemJoules != 150 {
		t.Errorf("system joules = %v, want 150", got.SystemJoules)
	}
	if len(got.ByModel) != 2 || got.ByModel[0].Model != "large" {
		t.Errorf("by_model = %+v, want large first (most energy)", got.ByModel)
	}
	if got.Attribution != "token-share" {
		t.Errorf("attribution = %q", got.Attribution)
	}
	if len(m.List()) != 2 {
		t.Errorf("List() = %d tasks, want 2", len(m.List()))
	}
}

func TestMeterUnknownModelBooksTokensNotEnergy(t *testing.T) {
	m := New(staticSource{})
	c := m.Record("t", "mystery", 5, 7)
	if c.GPUJoules != 0 || c.JPTUsed != 0 {
		t.Errorf("unknown model must book zero joules with JPTUsed=0, got %+v", c)
	}
	got, _ := m.Get("t")
	if got.OutputTokens != 7 {
		t.Errorf("tokens must still be booked: %d", got.OutputTokens)
	}
}

func TestSplitTaskPath(t *testing.T) {
	for _, tc := range []struct{ in, id, rest string }{
		{"/t/sandbox-42/v1/chat/completions", "sandbox-42", "/v1/chat/completions"},
		{"/v1/completions", "", "/v1/completions"},
		{"/t//v1/x", "", "/t//v1/x"},
		{"/t/abc", "", "/t/abc"},
	} {
		id, rest := splitTaskPath(tc.in)
		if id != tc.id || rest != tc.rest {
			t.Errorf("splitTaskPath(%q) = %q,%q want %q,%q", tc.in, id, rest, tc.id, tc.rest)
		}
	}
}

func TestEnsureIncludeUsage(t *testing.T) {
	out, changed := ensureIncludeUsage([]byte(`{"model":"m","stream":true}`))
	if !changed || !strings.Contains(string(out), `"include_usage":true`) {
		t.Errorf("include_usage not injected: %s", out)
	}
	_, changed = ensureIncludeUsage([]byte(`{"stream_options":{"include_usage":true}}`))
	if changed {
		t.Error("already-opted-in body must not change")
	}
}

// TestProxyEndToEnd drives a JSON and a streamed completion through the proxy
// against a fake vLLM backend and checks the task bill.
func TestProxyEndToEnd(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), `"stream":true`) {
			if !strings.Contains(string(body), `"include_usage":true`) {
				t.Errorf("stream request missing injected include_usage: %s", body)
			}
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n")
			fmt.Fprint(w, "data: {\"usage\":{\"prompt_tokens\":11,\"completion_tokens\":22}}\n\n")
			fmt.Fprint(w, "data: [DONE]\n\n")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[],"usage":{"prompt_tokens":40,"completion_tokens":60}}`)
	}))
	defer backend.Close()

	bu, _ := url.Parse(backend.URL)
	meter := New(staticSource{"m1": {JPT: 2, At: time.Now()}})
	proxy := &Proxy{Meter: meter, Backends: map[string]*url.URL{"m1": bu}, Log: zap.NewNop()}
	srv := httptest.NewServer(proxy)
	defer srv.Close()

	// JSON call via path-scoped task identity.
	resp, err := http.Post(srv.URL+"/t/sandbox-7/v1/chat/completions", "application/json",
		strings.NewReader(`{"model":"m1"}`))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close() //nolint:errcheck
	if !strings.Contains(string(b), "usage") {
		t.Errorf("response body not passed through: %s", b)
	}

	// Streamed call via header-scoped identity.
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/chat/completions",
		strings.NewReader(`{"model":"m1","stream":true}`))
	req.Header.Set(TaskIDHeader, "sandbox-7")
	resp2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	stream, _ := io.ReadAll(resp2.Body)
	resp2.Body.Close() //nolint:errcheck
	if !strings.Contains(string(stream), "[DONE]") {
		t.Errorf("stream not passed through: %s", stream)
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		got, ok := meter.Get("sandbox-7")
		if ok && got.Calls == 2 {
			if got.OutputTokens != 82 || got.GPUJoules != 164 {
				t.Errorf("bill = %d tokens / %v J, want 82 / 164", got.OutputTokens, got.GPUJoules)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("stream call never metered; got %+v (ok=%v)", got, ok)
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Missing identity is a 400, not an unmetered pass-through.
	resp3, _ := http.Post(srv.URL+"/v1/chat/completions", "application/json", strings.NewReader(`{"model":"m1"}`))
	if resp3.StatusCode != http.StatusBadRequest {
		t.Errorf("no-identity status = %d, want 400", resp3.StatusCode)
	}
	resp3.Body.Close() //nolint:errcheck
}
