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

	"github.com/aitra-ai/aitra-meter/internal/provider"
	_ "github.com/aitra-ai/aitra-meter/internal/provider/hostenergy/gracehwmon"
	_ "github.com/aitra-ai/aitra-meter/internal/provider/hostenergy/gracespark"
	_ "github.com/aitra-ai/aitra-meter/internal/provider/hostenergy/rapl"
	"github.com/aitra-ai/aitra-meter/internal/taskmeter"
)

func main() {
	listen := flag.String("listen", ":8090", "Listen address")
	promURL := flag.String("prometheus", "http://aitra-meter-prometheus:9090", "Meter Prometheus base URL (source of per-model J/token)")
	backends := flag.String("backends", "", "Model routing: model=http://host:port pairs, comma or semicolon separated")
	defaultBackend := flag.String("default-backend", "", "Upstream for models not in --backends (e.g. a platform gateway that does its own routing); empty rejects unknown models")
	costPerKWh := flag.Float64("cost-per-kwh", 0, "Electricity cost in USD/kWh; 0 omits cost lines")
	gco2PerKWh := flag.Float64("gco2-per-kwh", 0, "Grid intensity in gCO2/kWh; 0 omits carbon lines")
	hostProvider := flag.String("host-provider", "", "Host energy provider: grace-spark-hwmon | grace-hwmon | rapl | auto (empty disables host CPU attribution)")
	hwmonPath := flag.String("hwmon-path", "/sys/class/hwmon", "Path for hwmon devices")
	logLevel := flag.String("log-level", "info", "Log level: debug | info | warn | error")
	flag.Parse()

	log := newLogger(*logLevel)
	defer log.Sync() //nolint:errcheck

	routes, err := parseBackends(*backends)
	if err != nil {
		log.Fatal("invalid --backends", zap.Error(err))
	}
	var defURL *url.URL
	if *defaultBackend != "" {
		if defURL, err = url.Parse(*defaultBackend); err != nil {
			log.Fatal("invalid --default-backend", zap.Error(err))
		}
	}
	if len(routes) == 0 && defURL == nil {
		log.Fatal("provide --backends (model=url pairs) and/or --default-backend")
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	src := taskmeter.NewPromSource(ctx, *promURL)
	meter := taskmeter.New(src)
	meter.CostPerKWh = *costPerKWh
	meter.GCO2PerKWh = *gco2PerKWh

	var tracker *taskmeter.HostTracker
	if *hostProvider != "" {
		providerName := *hostProvider
		if providerName == "auto" {
			providerName = "grace-spark-hwmon"
		}
		p, err := provider.NewHostEnergy(providerName, map[string]string{
			"path": *hwmonPath,
		})
		if err != nil {
			log.Warn("host energy provider initialization failed",
				zap.String("provider", providerName),
				zap.Error(err),
			)
		} else if !p.Available(ctx) {
			log.Warn("host energy provider not available on this hardware",
				zap.String("provider", providerName),
			)
		} else {
			tracker = taskmeter.NewHostTracker(p)
			log.Info("host CPU energy attribution enabled", zap.String("provider", p.Name()))
		}
	}
	cpuHandler := taskmeter.NewCPUHandler(tracker, meter, log)

	proxy := &taskmeter.Proxy{Meter: meter, Backends: routes, DefaultBackend: defURL, Log: log}

	mux := http.NewServeMux()
	mux.Handle("/t/", proxy)
	mux.Handle("/v1/", proxy)
	mux.HandleFunc("/tasks", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, meter.List())
	})
	mux.HandleFunc("/tasks/", func(w http.ResponseWriter, r *http.Request) {
		sub := strings.TrimPrefix(r.URL.Path, "/tasks/")
		if strings.HasSuffix(sub, "/cpu/start") {
			taskID := strings.TrimSuffix(sub, "/cpu/start")
			cpuHandler.HandleStart(w, r, taskID)
			return
		}
		if strings.HasSuffix(sub, "/cpu/stop") {
			taskID := strings.TrimSuffix(sub, "/cpu/stop")
			cpuHandler.HandleStop(w, r, taskID)
			return
		}
		t, ok := meter.Get(sub)
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
