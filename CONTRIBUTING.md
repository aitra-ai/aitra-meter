# Contributing to Aitra Meter

Contributions are welcome across code, documentation, examples, and issue triage.

## Before you start

Read the [Architecture Decision Records](docs/adr/) to understand decisions that have already been made and why. If you are planning a substantial change, check whether an ADR covers it — if so, open an issue first to discuss before writing code.

For new features or significant changes, open an issue before opening a PR. This avoids duplicate effort and surfaces design questions early. Claim an issue by commenting on it before starting work.

## Contribution paths

### Add a new inference server

The generic-prometheus inference provider accepts configurable metric names and endpoint URL. To add support for a new server:

1. Confirm the server exposes a Prometheus endpoint with an output token counter and a running request gauge.
2. Find the metric names (see `examples/inference-servers/` for TGI, SGLang, and Ollama patterns).
3. Add an example config to `examples/inference-servers/<server-name>.yaml`.
4. Add the server to the compatibility matrix in `docs/reference/compatibility.md`.
5. Open a PR. No changes to core code required.

For servers that need custom scraping logic beyond metric name configuration, implement `InferenceMetricsProvider` in `internal/provider/inference/<server>/`. Register via `init()`. The vLLM provider is the reference implementation.

### Add a new energy backend

Implement `EnergyProvider` in `internal/provider/energy/<backend>/`. Register via `init()`. The NVML provider is the simplest reference; Zeus shows the sidecar pattern.

The interface requires four methods: `BeginWindow`, `EndWindow`, `IdlePower`, `Devices`. All four must be implemented — stubs that return `fmt.Errorf("not implemented")` are not acceptable in a merged PR.

### Add a new measurement agent

The aggregation service accepts `WindowReport` messages over gRPC from any process. The proto is in `api/proto/measurement/v1/measurement.proto` and is documented in [docs/reference/proto.md](docs/reference/proto.md).

You can write a measurement agent in any language that supports gRPC. Point it at the aggregation service address, send `WindowReport` messages, and measurements appear in the dashboard.

### Add a new storage backend

Implement `storage.Backend` in `internal/storage/<backend>/`. Register via `init()`. The SQLite backend is the reference implementation.

Both `RecordWriter` and `ChargebackQuerier` must be fully implemented. Include integration tests using testcontainers or equivalent.

## Development setup

```bash
git clone https://github.com/aitra-ai/aitra-meter.git
cd aitra-meter

# Run all unit tests
go test ./...

# Run with race detector
go test -race ./...

# Build all binaries
go build ./...

# Lint
golangci-lint run

# Local dev stack (Prometheus, aggregation service, dashboard)
docker compose up -d
go run ./dev/seed.go       # seed synthetic measurement data
# Dashboard at http://localhost:3000
```

For Helm chart changes:

```bash
helm lint helm/aitra-meter
helm template aitra-meter helm/aitra-meter | kubectl apply --dry-run=client -f -
```

## Pull request checklist

Before opening a PR:

- `go build ./...` is clean
- `go test -race ./...` passes
- `golangci-lint run` is clean
- If the change touches the Helm chart: `helm lint` passes
- If the change touches measurement methodology: an ADR is included or referenced
- If the change adds a new dependency: the dependency is discussed in the PR description
- Tests cover the change

All PRs require at least one maintainer approval before merge. PRs should be kept under 500 lines where possible — larger changes are easier to review when split into a series.

## Commit messages

Follow [Conventional Commits](https://www.conventionalcommits.org/):

```
feat: add SGLang inference provider
fix: correct proportional attribution for shared vLLM instances
docs: add proto reference for custom agent implementations
chore: update go-nvml to v0.12.1
```

Types: `feat`, `fix`, `docs`, `chore`, `test`, `refactor`, `perf`.

Include the affected component in the scope where helpful: `feat(provider): ...`, `fix(dashboard): ...`, `docs(getting-started): ...`.

## DCO sign-off

All commits must be signed off per the [Developer Certificate of Origin](https://developercertificate.org/):

```bash
git commit -s -m "feat: add SGLang inference provider"
```

This adds `Signed-off-by: Your Name <your@email.com>` to the commit. PRs without sign-off will not be merged.

## Coding standards

- Go: `gofmt` formatting, `golangci-lint` clean, idiomatic error wrapping (`fmt.Errorf("context: %w", err)`)
- No TODO comments in merged code — either fix it or open an issue
- New packages need a package-level doc comment
- Provider implementations must include unit tests with a mock or stub backend

## Code of conduct

See [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md).
