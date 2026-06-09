## Summary

<!-- What does this PR do and why? Link to the issue it closes: Closes #123 -->

## Type of change

- [ ] Bug fix
- [ ] New feature
- [ ] Provider addition (energy or inference)
- [ ] Storage backend addition
- [ ] Documentation
- [ ] Chore / dependency update

## Checklist

- [ ] `go build ./...` is clean
- [ ] `go test -race ./...` passes
- [ ] `golangci-lint run` is clean
- [ ] Helm chart changes: `helm lint helm/aitra-meter` passes
- [ ] New provider or storage backend: full interface implemented (no `not implemented` stubs)
- [ ] Measurement methodology change: ADR included or referenced
- [ ] New dependency: rationale in PR description
- [ ] Commits are signed off (`git commit -s`)

## Does this change the measurement methodology?

<!-- If yes, describe the impact on J/token computation, attribution, or calibration. An ADR is required for methodology changes. -->

## Testing

<!-- How was this tested? What hardware, inference server, and provider were used? -->
