# Multi-stage build for the measurement-agent. In DCGM mode the agent only
# scrapes a dcgm-exporter HTTP endpoint, so it needs no GPU driver libs and runs
# unprivileged. Pure-Go build (CGO disabled): nvml/amd providers are compiled in
# but use dlopen at runtime only when selected.
FROM golang:1.25 AS build
WORKDIR /src
ENV GOFLAGS=-mod=mod
COPY go.mod go.sum ./
RUN go mod download || true
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -o /out/measurement-agent ./cmd/measurement-agent

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/measurement-agent /measurement-agent
ENTRYPOINT ["/measurement-agent"]
