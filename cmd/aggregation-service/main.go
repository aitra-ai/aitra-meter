package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	measurementv1 "github.com/aitra-ai/aitra-meter/api/proto/measurement/v1"
	"github.com/aitra-ai/aitra-meter/internal/aggregation"
	k8slookup "github.com/aitra-ai/aitra-meter/internal/k8s"
	"github.com/aitra-ai/aitra-meter/internal/storage"

	// storage backends — blank imports trigger init() registration.
	_ "github.com/aitra-ai/aitra-meter/internal/storage/memory"
	_ "github.com/aitra-ai/aitra-meter/internal/storage/sqlite"
)

func main() {
	metricsAddr := flag.String("metrics-addr", ":8080", "Prometheus metrics and API listen address")
	grpcAddr    := flag.String("grpc-addr",    ":9091", "gRPC listen address for measurement agents")
	clusterName := flag.String("cluster",      "",      "Cluster name (required)")
	kubeconfig  := flag.String("kubeconfig",   "",      "Path to kubeconfig (empty = in-cluster)")
	logLevel    := flag.String("log-level",    "info",  "Log level: debug | info | warn | error")
	flag.Parse()

	if *clusterName == "" {
		*clusterName = os.Getenv("CLUSTER_NAME")
	}

	log := newLogger(*logLevel)
	defer log.Sync() //nolint:errcheck

	log.Info("starting aggregation service",
		zap.String("cluster", *clusterName),
		zap.String("metrics_addr", *metricsAddr),
		zap.String("grpc_addr", *grpcAddr),
	)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// --- storage backend ---------------------------------------------------
	backendName := os.Getenv("STORAGE_BACKEND")
	if backendName == "" {
		backendName = "sqlite"
	}
	backend, err := storage.New(backendName, map[string]string{
		"path": os.Getenv("SQLITE_PATH"),
	})
	if err != nil {
		log.Warn("storage backend init failed — falling back to memory (no persistence)",
			zap.String("backend", backendName),
			zap.Error(err),
		)
		backend, _ = storage.New("memory", nil)
	} else {
		log.Info("storage backend ready", zap.String("backend", backendName))
	}
	defer backend.Close() //nolint:errcheck

	// --- Kubernetes client -------------------------------------------------
	k8sClient, err := buildK8sClient(*kubeconfig)
	if err != nil {
		log.Fatal("kubernetes client init failed", zap.Error(err))
	}

	// --- aggregation loop --------------------------------------------------
	loop := aggregation.NewLoop(
		*clusterName,
		aggregation.NewResolver(k8slookup.NewPodMetaLookup(k8sClient), aggregation.PolicyConfig{}),
		aggregation.NewCalibrationTableFromMap(nil),
		k8slookup.NewNodeHardwareLookup(k8sClient),
		backend,
	)

	// --- gRPC server -------------------------------------------------------
	lis, err := net.Listen("tcp", *grpcAddr)
	if err != nil {
		log.Fatal("gRPC listen failed", zap.String("addr", *grpcAddr), zap.Error(err))
	}
	grpcSrv := grpc.NewServer()
	measurementv1.RegisterMeasurementServiceServer(grpcSrv, loop)
	go func() {
		log.Info("gRPC server listening", zap.String("addr", *grpcAddr))
		if err := grpcSrv.Serve(lis); err != nil {
			log.Error("gRPC server stopped", zap.Error(err))
		}
	}()

	// --- HTTP server (metrics + health + API) ------------------------------
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ready"}`))
	})

	// GET /api/v1/namespaces?from=<RFC3339>&to=<RFC3339>&pue=<float>
	mux.HandleFunc("/api/v1/namespaces", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		from, err := time.Parse(time.RFC3339, q.Get("from"))
		if err != nil {
			from = time.Now().AddDate(0, 0, -30)
		}
		to, err := time.Parse(time.RFC3339, q.Get("to"))
		if err != nil {
			to = time.Now()
		}
		pue, err := strconv.ParseFloat(q.Get("pue"), 64)
		if err != nil || pue <= 0 {
			pue = 1.0
		}

		charges, err := backend.QueryChargeback(r.Context(), storage.ChargebackQuery{
			Cluster: *clusterName,
			From:    from,
			To:      to,
			PUE:     pue,
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"namespaces": charges})
	})

	httpSrv := &http.Server{Addr: *metricsAddr, Handler: mux}
	go func() {
		log.Info("metrics server listening", zap.String("addr", *metricsAddr))
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatal("metrics server error", zap.Error(err))
		}
	}()

	<-ctx.Done()
	log.Info("shutting down")
	grpcSrv.GracefulStop()
	_ = httpSrv.Shutdown(context.Background())
}

func newLogger(level string) *zap.Logger {
	cfg := zap.NewProductionConfig()
	_ = cfg.Level.UnmarshalText([]byte(level))
	l, _ := cfg.Build()
	return l
}

func buildK8sClient(kubeconfigPath string) (kubernetes.Interface, error) {
	var restCfg *rest.Config
	var err error
	if kubeconfigPath != "" {
		restCfg, err = clientcmd.BuildConfigFromFlags("", kubeconfigPath)
	} else {
		restCfg, err = rest.InClusterConfig()
	}
	if err != nil {
		return nil, fmt.Errorf("kubernetes config: %w", err)
	}
	return kubernetes.NewForConfig(restCfg)
}
