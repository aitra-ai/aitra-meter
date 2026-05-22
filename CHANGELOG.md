# Changelog

All notable changes to Aitra Meter will be documented here.

Format follows [Keep a Changelog](https://keepachangelog.com/en/1.0.0/).
Versioning follows [Semantic Versioning](https://semver.org/).

---

## [Unreleased]

### Fixed
- Typo  →  in  (build error)
- NVML provider uses  which is Linux-only — added  tag to  and moved side-effect import to  (macOS build error)
- Literal newline inside string literal in  — replaced with  escape (build error)

### Added
- EnergyProvider and InferenceMetricsProvider pluggable interfaces (`internal/provider/`)
- Zeus energy provider stub
- vLLM inference provider stub
- generic-prometheus inference provider (compatible with TGI, SGLang, Ollama, Triton)
- Provider registry with `RegisterEnergy` / `RegisterInference` / `NewEnergy` / `NewInference`
- MeasurementPolicy CRD schema
- SiteConfig CRD schema
- Helm chart scaffold (`helm/aitra-meter/`)
- Technical specification v1.0 (`docs/spec/aitra-meter-spec-v1.0.md`)
- Architecture Decision Records 0001–0004
- SECURITY.md, ROADMAP.md, GOVERNANCE.md, CONTRIBUTING.md

### Changed
- Annotation prefix migrated from `aitra.io` to `aitra-ai.github.io`
- API group migrated from `aitra.io/v1alpha1` to `aitra-ai.github.io/v1alpha1`

---

## Release cadence

Pre-v1.0: releases are tagged when a meaningful set of features is complete.  
Post-v1.0: minor releases monthly, patch releases as needed.
