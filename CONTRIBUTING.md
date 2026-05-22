# Contributing to Aitra Meter

Thank you for your interest in contributing.

## Before you start

Read the [Technical Specification](docs/spec/aitra-meter-spec-v1.0.md) and the [Architecture Decision Records](docs/adr/) to understand the design intent.

## How to contribute

1. Open an issue describing what you want to change and why.
2. For spec or documentation changes, open a PR against the `docs/*` branch.
3. For code changes, open a PR against `develop`.
4. All PRs require at least one maintainer approval before merge.

## Coding standards

- Go: standard `gofmt` formatting, `golangci-lint` clean.
- Python: `black` + `ruff`.
- YAML: validated against schemas where available.

## Commit messages

Follow [Conventional Commits](https://www.conventionalcommits.org/):

```
feat: add idle power metric to measurement agent
fix: correct proportional attribution for shared vLLM instances
docs: update calibration tier definitions in spec
```

## Code of conduct

See [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md).
