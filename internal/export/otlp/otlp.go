// Package otlp provides an OpenTelemetry OTLP metric exporter for Aitra Meter.
// It emits four metrics under the gen_ai.infrastructure.energy.* namespace,
// which Aitra Meter is proposing as an addition to the OTel semantic conventions.
//
// Metrics emitted:
//
//	gen_ai.infrastructure.energy.joules_per_token  Gauge   J
//	gen_ai.infrastructure.energy.joules_total      Counter J
//	gen_ai.infrastructure.power.watts              Gauge   W
//	gen_ai.infrastructure.idle_ratio               Gauge   ratio (0.0–1.0)
//
// Attribute set follows existing gen_ai.* conventions:
//
//	gen_ai.request.model      — model name
//	gen_ai.provider.name      — inference provider (vllm, generic-prometheus)
//	server.address            — node hostname
//	k8s.namespace.name        — Kubernetes namespace
//	k8s.cluster.name          — cluster name from SiteConfig
package otlp

import (
	"context"
	"fmt"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

const instrumentationScope = "github.com/aitra-ai/aitra-meter"

// Exporter holds the OTel meter and instruments for Aitra energy metrics.
type Exporter struct {
	provider *sdkmetric.MeterProvider
	meter    metric.Meter

	joulesPerToken metric.Float64Gauge
	joulesTotal    metric.Float64ObservableCounter
	powerWatts     metric.Float64Gauge
	idleRatio      metric.Float64Gauge

	// joulesAccumulator holds the cumulative joules value for the observable counter.
	joulesTotalVal float64
}

// Config holds options for the OTLP exporter.
type Config struct {
	// Endpoint is the OTLP gRPC receiver address.
	// Example: "otel-collector.monitoring.svc.cluster.local:4317"
	Endpoint string

	// ClusterName is added as k8s.cluster.name to every metric.
	ClusterName string

	// Insecure disables TLS. Set true for in-cluster collectors.
	Insecure bool
}

// New creates an Exporter, connects to the OTLP endpoint, and registers instruments.
func New(ctx context.Context, cfg Config) (*Exporter, error) {
	opts := []otlpmetricgrpc.Option{
		otlpmetricgrpc.WithEndpoint(cfg.Endpoint),
	}
	if cfg.Insecure {
		opts = append(opts, otlpmetricgrpc.WithInsecure())
	}

	exp, err := otlpmetricgrpc.New(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("otlp: create exporter: %w", err)
	}

	res, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceName("aitra-meter"),
			attribute.String("k8s.cluster.name", cfg.ClusterName),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("otlp: create resource: %w", err)
	}

	provider := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(exp)),
		sdkmetric.WithResource(res),
	)
	otel.SetMeterProvider(provider)

	meter := provider.Meter(instrumentationScope)

	e := &Exporter{provider: provider, meter: meter}

	e.joulesPerToken, err = meter.Float64Gauge(
		"gen_ai.infrastructure.energy.joules_per_token",
		metric.WithDescription("Joules per output token for the current measurement window."),
		metric.WithUnit("J"),
	)
	if err != nil {
		return nil, fmt.Errorf("otlp: register joules_per_token: %w", err)
	}

	e.powerWatts, err = meter.Float64Gauge(
		"gen_ai.infrastructure.power.watts",
		metric.WithDescription("Current GPU power draw in watts."),
		metric.WithUnit("W"),
	)
	if err != nil {
		return nil, fmt.Errorf("otlp: register power.watts: %w", err)
	}

	e.idleRatio, err = meter.Float64Gauge(
		"gen_ai.infrastructure.idle_ratio",
		metric.WithDescription("Fraction of the last measurement window with no active inference requests."),
		metric.WithUnit("1"),
	)
	if err != nil {
		return nil, fmt.Errorf("otlp: register idle_ratio: %w", err)
	}

	// Observable counter for cumulative joules — value updated via RecordWindow.
	e.joulesTotal, err = meter.Float64ObservableCounter(
		"gen_ai.infrastructure.energy.joules_total",
		metric.WithDescription("Cumulative GPU energy consumed in joules."),
		metric.WithUnit("J"),
	)
	if err != nil {
		return nil, fmt.Errorf("otlp: register joules_total: %w", err)
	}

	return e, nil
}

// WindowAttrs holds the per-window dimension values used as OTel attributes.
type WindowAttrs struct {
	Model             string
	InferenceProvider string
	Node              string
	Namespace         string
}

// RecordWindow emits all four metrics for a single measurement window.
func (e *Exporter) RecordWindow(ctx context.Context, attrs WindowAttrs, jPerToken, joules, powerWatts, idleRatio float64) {
	a := []attribute.KeyValue{
		attribute.String("gen_ai.request.model", attrs.Model),
		attribute.String("gen_ai.provider.name", attrs.InferenceProvider),
		attribute.String("server.address", attrs.Node),
		attribute.String("k8s.namespace.name", attrs.Namespace),
	}

	e.joulesPerToken.Record(ctx, jPerToken, metric.WithAttributes(a...))
	e.powerWatts.Record(ctx, powerWatts, metric.WithAttributes(a...))
	e.idleRatio.Record(ctx, idleRatio, metric.WithAttributes(a...))
	// joulesTotal is an observable counter — its value is accumulated externally.
	e.joulesTotalVal += joules
}

// Shutdown flushes pending metrics and shuts down the provider.
func (e *Exporter) Shutdown(ctx context.Context) error {
	return e.provider.Shutdown(ctx)
}
