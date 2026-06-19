# Multi-stage build for the aggregation-service. Compiles inside the image, so
# the build host needs only Docker (no Go toolchain). Pure-Go (CGO disabled):
# modernc.org/sqlite, client-go, grpc, prometheus all build without cgo.
FROM golang:1.25 AS build
WORKDIR /src
# -mod=mod lets the build fetch any go.sum entries missing for this toolchain.
ENV GOFLAGS=-mod=mod
COPY go.mod go.sum ./
RUN go mod download || true
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -o /out/aggregation-service ./cmd/aggregation-service

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/aggregation-service /aggregation-service
EXPOSE 8080 9091
ENTRYPOINT ["/aggregation-service"]
