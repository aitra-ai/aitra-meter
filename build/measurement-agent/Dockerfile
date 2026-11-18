# Measurement agent — runs as a DaemonSet on GPU nodes.
#
# Built CGO_ENABLED=1 (ADR-0006). The default NVML provider uses go-nvml, which
# needs cgo to compile: it dlopens libnvidia-ml.so.1 at runtime from the node's
# driver installation, and that dlopen goes through cgo. The resulting binary
# is dynamically linked against glibc, so the runtime image is distroless
# "base" (glibc) rather than "static". No vendor libraries are bundled; the
# NVIDIA container runtime injects them (see the ENV below).
#
# The AMD provider is gated behind the `amd` build tag and compiles to a stub
# in this image; ROCm nodes need an image built with
# `--build-arg BUILD_TAGS=amd` against the AMD SMI headers.
FROM golang:1.26-bookworm AS builder
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG VERSION=dev
ARG BUILD_TAGS=""
RUN CGO_ENABLED=1 GOOS=linux go build \
      -tags "${BUILD_TAGS}" \
      -trimpath \
      -ldflags "-s -w -X main.version=${VERSION}" \
      -o /out/measurement-agent \
      ./cmd/measurement-agent

FROM gcr.io/distroless/base-debian12:nonroot
# The NVIDIA container runtime mounts the driver libraries, libnvidia-ml.so.1
# among them, only into containers that ask for them. The agent reads
# telemetry for every GPU on the node and needs the `utility` capability only.
ENV NVIDIA_VISIBLE_DEVICES=all
ENV NVIDIA_DRIVER_CAPABILITIES=utility
COPY --from=builder /out/measurement-agent /usr/local/bin/measurement-agent
USER nonroot:nonroot
ENTRYPOINT ["/usr/local/bin/measurement-agent"]
