# HTTP API reference

The aggregation service exposes an HTTP API on port `8080` for the dashboard, export functions, and direct queries.

**Base URL:** `http://aitra-meter-aggregation.<namespace>.svc.cluster.local:8080`

---

## Endpoints

### `GET /metrics`

Prometheus metrics exposition endpoint. Returns all metrics in Prometheus text format.

```
HTTP 200 OK
Content-Type: text/plain; version=0.0.4

# HELP aitra_j_per_token Joules per output token
# TYPE aitra_j_per_token gauge
aitra_j_per_token{namespace="inference-prod",workload="chat",...} 0.31
...
```

---

### `GET /healthz`

Liveness probe endpoint.

```
HTTP 200 OK
{"status": "ok"}
```

---

### `GET /readyz`

Readiness probe. Returns `503` until the aggregation service has received at least one measurement from all expected measurement agents.

```
HTTP 200 OK
{"status": "ready", "agents_connected": 6, "agents_expected": 6}
```

---

### `GET /api/v1/measurements`

Returns recent J/token measurements.

**Query parameters:**

| Parameter | Type | Default | Description |
|---|---|---|---|
| `namespace` | string | all | Filter by namespace |
| `model` | string | all | Filter by model name |
| `hardware` | string | all | Filter by GPU tier |
| `workload` | string | all | Filter by workload type |
| `from` | RFC3339 | 1h ago | Start of time range |
| `to` | RFC3339 | now | End of time range |
| `limit` | int | 100 | Maximum records to return |

**Response:**

```json
{
  "measurements": [
    {
      "timestamp": "2026-05-22T14:30:00Z",
      "namespace": "inference-prod",
      "workload": "chat",
      "model": "Qwen3.6-27B",
      "hardware": "h100",
      "precision": "fp16",
      "j_per_token": 0.3105,
      "calibration_tier": "aitra_benchmark",
      "attribution_method": "direct",
      "co2_per_token_grams": 0.0000355,
      "cost_per_million_tokens_usd": 0.0414
    }
  ],
  "total": 1,
  "from": "2026-05-22T13:30:00Z",
  "to": "2026-05-22T14:30:00Z"
}
```

---

### `GET /api/v1/namespaces`

Returns namespace-level energy aggregates for a billing period.

**Query parameters:**

| Parameter | Type | Default | Description |
|---|---|---|---|
| `from` | RFC3339 | start of current month | Billing period start |
| `to` | RFC3339 | now | Billing period end |
| `pue` | float | SiteConfig value | Override PUE for this request |

**Response:**

```json
{
  "period": {"from": "2026-05-01T00:00:00Z", "to": "2026-05-22T14:30:00Z"},
  "pue": 1.35,
  "namespaces": [
    {
      "namespace": "inference-prod",
      "energy_joules_raw": 810000,
      "energy_joules_pue": 1093500,
      "output_tokens": 2600000000,
      "cost_usd": 131.22,
      "attribution_method": "direct",
      "team": "platform",
      "cost_centre": "cc-1102"
    }
  ]
}
```

---

### `GET /api/v1/export/chargeback`

Returns a chargeback report as CSV.

**Query parameters:** same as `/api/v1/namespaces`.

**Response:** `text/csv` with columns:

```
namespace,team,cost_centre,energy_joules_raw,energy_joules_pue,output_tokens,cost_usd,attribution_method,period_from,period_to,pue,carbon_source,co2_kg
```

---

### `GET /api/v1/cluster`

Returns cluster-level aggregate metrics.

**Response:**

```json
{
  "cluster": "sgp-dc01",
  "j_per_token": 0.34,
  "power_watts_total": 2410,
  "idle_power_watts": 312,
  "idle_ratio": 0.22,
  "throughput_tokens_per_sec": 7104,
  "grid_intensity_gco2_kwh": 412,
  "carbon_source": "electricitymaps",
  "calibration_tier": "aitra_benchmark"
}
```

---

### `GET /api/v1/providers`

Returns the currently configured energy and inference providers.

**Response:**

```json
{
  "energy_provider": {
    "name": "zeus",
    "status": "healthy",
    "devices": [
      {"id": "0", "name": "NVIDIA H100 SXM5 80GB", "type": "gpu"},
      {"id": "1", "name": "NVIDIA H100 SXM5 80GB", "type": "gpu"}
    ]
  },
  "inference_provider": {
    "name": "vllm",
    "status": "healthy",
    "endpoint": "http://localhost:8000/metrics",
    "model": "Qwen3.6-27B"
  }
}
```
