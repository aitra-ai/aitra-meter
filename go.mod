module github.com/aitra-ai/aitra-meter

go 1.22

require (
	github.com/NVIDIA/go-nvml v0.12.0-1
	github.com/ClickHouse/clickhouse-go/v2 v2.23.2
	github.com/prometheus/client_golang v1.19.1
	github.com/prometheus/common v0.53.0
	go.opentelemetry.io/otel v1.27.0
	go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc v1.27.0
	go.uber.org/zap v1.27.0
	k8s.io/api v0.30.1
	k8s.io/apimachinery v0.30.1
	k8s.io/client-go v0.30.1
	sigs.k8s.io/controller-runtime v0.18.3
)
