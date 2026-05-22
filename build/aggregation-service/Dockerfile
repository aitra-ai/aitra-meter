FROM golang:1.26-alpine AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-w -s" -o /aggregation-service ./cmd/aggregation-service

FROM alpine:3.20
RUN apk add --no-cache ca-certificates
COPY --from=builder /aggregation-service /aggregation-service
ENTRYPOINT ["/aggregation-service"]
