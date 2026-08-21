// task-gateway is the agent-task metering entry point: an OpenAI-compatible
// reverse proxy that scopes every completion to a task (an agent run — e.g.
// one xModeling sandbox instance) and joins its token usage with the meter's
// live per-model J/token to produce a per-task energy bill.
//
// Task identity: /t/{taskID}/v1/... in the base URL (zero client changes —
// the sandbox launcher bakes its instance ID into OPENAI_BASE_URL), or the
// X-Aitra-Task-Id header on plain /v1/... paths.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"go.uber.org/zap"

	"github.com/aitra-ai/aitra-meter/internal/taskmeter"
)

func main() {
	listen := flag.String("listen", ":8090", "Listen address")
	promURL := flag.String("prometheus", "http://aitra-meter-prometheus:9090", "Meter Prometheus base URL (source of per-model J/token)")
	backends := flag.String("backends", "", "Model routing: model=http://host:port pairs, comma or semicolon separated")
	costPerKWh := flag.Float64("cost-per-kwh", 0, "Electricity cost in USD/kWh; 0 omits cost lines")
	gco2PerKWh := flag.Float64("gco2-per-kwh", 0, "Grid intensity in gCO2/kWh; 0 omits carbon lines")
	logLevel := flag.String("log-level", "info", "Log level: debug | info | warn | error")
	flag.Parse()

	log := newLogger(*logLevel)
	defer log.Sync() //nolint:errcheck

	routes, err := parseBackends(*backends)
	if err != nil {
		log.Fatal("invalid --backends", zap.Error(err))
	}
	if len(routes) == 0 {
		log.Fatal("--backends is required (model=url pairs)")
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	src := taskmeter.NewPromSource(ctx, *promURL)
	meter := taskmeter.New(src)
	meter.CostPerKWh = *costPerKWh
	meter.GCO2PerKWh = *gco2PerKWh

	proxy := &taskmeter.Proxy{Meter: meter, Backends: routes, Log: log}

	mux := http.NewServeMux()
	mux.Handle("/t/", proxy)
	mux.Handle("/v1/", proxy)
	mux.HandleFunc("/tasks", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, meter.List())
	})
	mux.HandleFunc("/tasks/", func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/tasks/")
		t, ok := meter.Get(id)
		if !ok {
			http.Error(w, `{"error":"unknown task"}`, http.StatusNotFound)
			return
		}
		writeJSON(w, t)
	})
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	srv := &http.Server{Addr: *listen, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		shutCtx, shutCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutCancel()
		_ = srv.Shutdown(shutCtx)
	}()

	log.Info("task gateway listening",
		zap.String("addr", *listen),
		zap.Int("models", len(routes)),
		zap.String("prometheus", *promURL),
	)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal("serve", zap.Error(err))
	}
	log.Info("task gateway stopped")
}

func parseBackends(s string) (map[string]*url.URL, error) {
	out := make(map[string]*url.URL)
	for _, pair := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ';' }) {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		model, raw, ok := strings.Cut(pair, "=")
		if !ok {
			return nil, &url.Error{Op: "parse", URL: pair}
		}
		u, err := url.Parse(strings.TrimSpace(raw))
		if err != nil {
			return nil, err
		}
		out[strings.TrimSpace(model)] = u
	}
	return out, nil
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

func newLogger(level string) *zap.Logger {
	cfg := zap.NewProductionConfig()
	if err := cfg.Level.UnmarshalText([]byte(level)); err != nil {
		cfg.Level.SetLevel(zap.InfoLevel)
	}
	log, err := cfg.Build()
	if err != nil {
		panic(err)
	}
	return log
}
