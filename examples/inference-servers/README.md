# Inference server configs

Per-engine Helm values for the Aitra Meter measurement agent. Pass one to
`helm install`/`helm upgrade` with `-f`:

```bash
helm upgrade --install aitra-meter ./helm/aitra-meter \
  -n aitra-system --create-namespace \
  -f examples/inference-servers/sglang.yaml
```

| File | Engine | Provider used | Notes |
|---|---|---|---|
| `vllm.yaml` | vLLM | `vllm` | Default; no server-side flags needed |
| `sglang.yaml` | SGLang | `sglang` | Server must run with `--enable-metrics` |
| `triton.yaml` | Triton Inference Server | `triton` | Token counts are approximate — set `avg_output_tokens_per_request` |
| `tgi.yaml` | Text Generation Inference | `generic-prometheus` | TGI is in maintenance mode since 2025-12-11 |
| `ollama.yaml` | Ollama | `generic-prometheus` | Requires a sidecar exporter; no native `/metrics` |
| `autodiscovery.yaml` | any of the above | `auto` | Detects the engine from pod annotations/labels |

Full documentation, including discovery rules and per-engine limitations, is
in [docs/guides/inference-providers.md](../../docs/guides/inference-providers.md).
