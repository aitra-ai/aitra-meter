# Aggregation service — cluster-scoped Deployment. Joins GPU energy with
# inference token counts and exposes the resulting J/token series to
# Prometheus.
FROM golang:1.26-bookworm AS builder
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=linux go build \
      -trimpath \
      -ldflags "-s -w -X main.version=${VERSION}" \
      -o /out/aggregation-service \
      ./cmd/aggregation-service

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=builder /out/aggregation-service /usr/local/bin/aggregation-service
USER nonroot:nonroot
EXPOSE 9090
ENTRYPOINT ["/usr/local/bin/aggregation-service"]
