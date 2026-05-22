# Build stage
FROM golang:1.26-alpine AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-w -s" -o /measurement-agent ./cmd/measurement-agent

# Runtime stage
FROM alpine:3.20
RUN apk add --no-cache ca-certificates
COPY --from=builder /measurement-agent /measurement-agent
ENTRYPOINT ["/measurement-agent"]
