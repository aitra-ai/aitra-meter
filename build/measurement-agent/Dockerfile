# Measurement agent — runs as a DaemonSet on GPU nodes.
#
# Built CGO_ENABLED=0. The default NVML provider is pure Go (go-nvml dlopens
# libnvidia-ml.so.1 at runtime from the node's driver installation), so no cgo
# toolchain is needed here. The AMD provider is gated behind the `amd` build
# tag and compiles to a stub in this image; ROCm nodes need an image built
# with `--build-arg BUILD_TAGS=amd` and CGO_ENABLED=1 against the AMD SMI
# headers.
FROM golang:1.25-bookworm AS builder
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG VERSION=dev
ARG BUILD_TAGS=""
RUN CGO_ENABLED=0 GOOS=linux go build \
      -tags "${BUILD_TAGS}" \
      -trimpath \
      -ldflags "-s -w -X main.version=${VERSION}" \
      -o /out/measurement-agent \
      ./cmd/measurement-agent

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=builder /out/measurement-agent /usr/local/bin/measurement-agent
USER nonroot:nonroot
ENTRYPOINT ["/usr/local/bin/measurement-agent"]
