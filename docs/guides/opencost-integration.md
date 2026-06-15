# OpenCost integration

> This guide is in progress. A combined Grafana panel is available in [`examples/grafana/opencost-aitra-combined.json`](../../examples/grafana/opencost-aitra-combined.json).

OpenCost and Aitra Meter are complementary:

- **OpenCost** reports what the cluster costs — GPU-hours billed per namespace.
- **Aitra Meter** reports what the cluster produced — tokens generated per joule per namespace.

Running both in the same cluster surfaces the inversion that neither tool sees alone: a namespace can appear cheaper by GPU-hour while being more expensive per token, or vice versa, depending on inference serving configuration.

The combined Grafana panel in `examples/grafana/` plots both metrics side by side per namespace. Import it into an existing Grafana instance alongside the Aitra Meter dashboard.

A full walkthrough will be added here before the v0.3.0 release.
