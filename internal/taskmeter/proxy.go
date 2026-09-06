package taskmeter

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"

	"go.uber.org/zap"
)

// TaskIDHeader carries the task identity when it is not in the path.
const TaskIDHeader = "X-Aitra-Task-Id"

// Proxy is an OpenAI-compatible reverse proxy that books every completion's
// token usage against a task. Task identity comes from the URL prefix
// /t/{taskID}/v1/... — so any unmodified OpenAI client is task-scoped purely
// by its configured base URL (this is how sandbox instances integrate: the
// launcher injects OPENAI_BASE_URL with its instance ID in the path) — or
// from the X-Aitra-Task-Id header on plain /v1/... paths.
type Proxy struct {
	Meter    *Meter
	Backends map[string]*url.URL // model name → backend base URL
	// DefaultBackend receives requests whose model has no Backends entry —
	// the platform-gateway integration (e.g. xModeling aigateway, which does
	// its own model routing): the meter only books usage in passing. Nil means
	// unknown models are rejected.
	DefaultBackend *url.URL
	Log            *zap.Logger
}

// ServeHTTP routes /t/{task}/v1/* and /v1/* to the model's backend.
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	taskID, rest := splitTaskPath(r.URL.Path)
	if taskID == "" {
		taskID = r.Header.Get(TaskIDHeader)
	}
	if taskID == "" {
		http.Error(w, `{"error":"no task identity: use /t/{taskID}/v1/... or the X-Aitra-Task-Id header"}`, http.StatusBadRequest)
		return
	}
	if !strings.HasPrefix(rest, "/v1/") {
		http.Error(w, `{"error":"unsupported path"}`, http.StatusNotFound)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 32<<20))
	if err != nil {
		http.Error(w, `{"error":"read body"}`, http.StatusBadRequest)
		return
	}
	_ = r.Body.Close()

	model, stream := modelAndStream(body)
	backend, ok := p.Backends[model]
	if !ok {
		if p.DefaultBackend == nil {
			http.Error(w, fmt.Sprintf(`{"error":"unknown model %q"}`, model), http.StatusNotFound)
			return
		}
		backend = p.DefaultBackend
	}

	// Streamed completions only carry usage when the client opts in; opt in on
	// their behalf so every call is meterable. The extra final chunk is part of
	// the OpenAI protocol, so compliant clients are unaffected.
	if stream {
		if b, changed := ensureIncludeUsage(body); changed {
			body = b
		}
	}

	rp := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL.Scheme = backend.Scheme
			pr.Out.URL.Host = backend.Host
			pr.Out.URL.Path = rest
			pr.Out.Host = backend.Host
			pr.Out.Body = io.NopCloser(bytes.NewReader(body))
			pr.Out.ContentLength = int64(len(body))
		},
		ModifyResponse: func(resp *http.Response) error {
			if resp.StatusCode != http.StatusOK {
				return nil
			}
			if stream {
				resp.Body = p.meterStream(resp.Body, taskID, model)
				return nil
			}
			return p.meterJSON(resp, taskID, model)
		},
		ErrorLog: nil,
	}
	rp.ServeHTTP(w, r)
}

// meterJSON books usage from a non-streamed completion body.
func (p *Proxy) meterJSON(resp *http.Response, taskID, model string) error {
	b, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	_ = resp.Body.Close()
	if err != nil {
		return err
	}
	if u, ok := parseUsage(b); ok {
		p.record(taskID, model, u)
	}
	resp.Body = io.NopCloser(bytes.NewReader(b))
	resp.ContentLength = int64(len(b))
	resp.Header.Del("Content-Length")
	return nil
}

// meterStream tees an SSE stream, booking the final usage chunk after the
// stream closes. Chunks pass through unmodified with no added latency.
func (p *Proxy) meterStream(body io.ReadCloser, taskID, model string) io.ReadCloser {
	pr, pw := io.Pipe()
	go func() {
		var last usage
		var seen bool
		sc := bufio.NewScanner(io.TeeReader(body, pw))
		sc.Buffer(make([]byte, 0, 64*1024), 4<<20)
		for sc.Scan() {
			line := sc.Bytes()
			if !bytes.HasPrefix(line, []byte("data: ")) {
				continue
			}
			if u, ok := parseUsage(line[len("data: "):]); ok {
				last, seen = u, true
			}
		}
		_ = body.Close()
		_ = pw.CloseWithError(sc.Err())
		if seen {
			p.record(taskID, model, last)
		}
	}()
	return pr
}

func (p *Proxy) record(taskID, model string, u usage) {
	c := p.Meter.Record(taskID, model, u.PromptTokens, u.CompletionTokens)
	if p.Log != nil {
		p.Log.Debug("task call metered",
			zap.String("task", taskID), zap.String("model", model),
			zap.Uint64("output_tokens", c.OutputTokens), zap.Float64("gpu_joules", c.GPUJoules))
	}
}

// splitTaskPath extracts /t/{taskID} and returns the remaining path.
func splitTaskPath(path string) (taskID, rest string) {
	if !strings.HasPrefix(path, "/t/") {
		return "", path
	}
	remainder := path[len("/t/"):]
	i := strings.IndexByte(remainder, '/')
	if i <= 0 {
		return "", path
	}
	return remainder[:i], remainder[i:]
}

type usage struct {
	PromptTokens     uint64 `json:"prompt_tokens"`
	CompletionTokens uint64 `json:"completion_tokens"`
}

// parseUsage extracts a non-null usage object from a completion (or chunk).
func parseUsage(b []byte) (usage, bool) {
	var body struct {
		Usage *usage `json:"usage"`
	}
	if err := json.Unmarshal(b, &body); err != nil || body.Usage == nil {
		return usage{}, false
	}
	if body.Usage.PromptTokens == 0 && body.Usage.CompletionTokens == 0 {
		return usage{}, false
	}
	return *body.Usage, true
}

// modelAndStream pulls the model name and stream flag from a request body.
func modelAndStream(b []byte) (string, bool) {
	var body struct {
		Model  string `json:"model"`
		Stream bool   `json:"stream"`
	}
	_ = json.Unmarshal(b, &body)
	return body.Model, body.Stream
}

// ensureIncludeUsage sets stream_options.include_usage=true on a streaming
// request body, reporting whether the body changed.
func ensureIncludeUsage(b []byte) ([]byte, bool) {
	var body map[string]any
	if err := json.Unmarshal(b, &body); err != nil {
		return b, false
	}
	so, _ := body["stream_options"].(map[string]any)
	if so == nil {
		so = map[string]any{}
	}
	if inc, _ := so["include_usage"].(bool); inc {
		return b, false
	}
	so["include_usage"] = true
	body["stream_options"] = so
	out, err := json.Marshal(body)
	if err != nil {
		return b, false
	}
	return out, true
}
