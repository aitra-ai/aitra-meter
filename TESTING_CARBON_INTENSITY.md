# Testing Guide: Real-Time Grid Carbon Intensity

This guide documents all environment variables, CLI flags, Kubernetes/Helm settings, and commands required to test the live grid carbon intensity feature (`aitra_grid_intensity_gco2_per_kwh`) across **WattTime**, **Electricity Maps**, and **Static Fallback** modes.

---

## 1. Environment Variables & CLI Flags

### Core Aggregation Service Settings

| Environment Variable | CLI Flag | Default | Description |
|---|---|---|---|
| `CLUSTER_NAME` | `--cluster` | `""` | Cluster name (required when starting the service). |
| `CARBON_SOURCE` | `--carbon-source` | `"manual"` | Provider selector: `watttime`, `electricitymaps`, `static`, or `manual`. |
| `GRID_GCO2_PER_KWH` | `--grid-gco2-per-kwh` | `0` | Static fallback grid carbon intensity in `gCO2/kWh` (used on startup before the first async poll finishes, or when live APIs fail/are disabled). |
| `GRID_ZONE` | — | `""` | Default grid zone / region fallback used if provider-specific zone/region is unset. |
| `ELECTRICITY_COST_PER_KWH` | `--electricity-cost-per-kwh` | `0` | Optional electricity price (`USD/kWh`) for cost derivation. |

### Provider A: WattTime (`--carbon-source=watttime`)

| Environment Variable | Required | Example | Description |
|---|---|---|---|
| `WATTTIME_USERNAME` | Yes | `"my_watttime_user"` | WattTime account username (used for Basic Auth to `/login`). |
| `WATTTIME_PASSWORD` | Yes | `"my_watttime_pass"` | WattTime account password. |
| `WATTTIME_REGION` | Yes (or `GRID_ZONE`) | `"CAISO_NORTH"` | WattTime balancing-authority region (e.g., `CAISO_NORTH`, `PJM_DC`, `ERCOT_EASTTX`). |

> **How it works:** WattTime JWT tokens expire after ~30 minutes. Since the poller runs hourly, the client re-authenticates at the start of every fetch cycle, retries once on HTTP `401`, and queries `/v3/forecast?horizon_hours=0` (`lbs_co2_per_mwh × 0.453592 = gCO2/kWh`).

### Provider B: Electricity Maps (`--carbon-source=electricitymaps`)

| Environment Variable | CLI Flag | Required | Example | Description |
|---|---|---|---|---|
| `ELECTRICITY_MAPS_API_TOKEN` | — | Yes | `"em_live_..."` | Electricity Maps v4 API token (`auth-token` header). |
| `ELECTRICITY_MAPS_ZONE` | `--electricity-maps-zone` | Yes (or `GRID_ZONE`) | `"US-CAL-CISO"` | Electricity Maps grid zone identifier (e.g., `US-CAL-CISO`, `DE`, `IN-KA`). |

---

## 2. Running Automated Unit & Concurrency Tests

No external API credentials or Kubernetes cluster are required for unit tests (HTTP responses are mocked):

```bash
# Run all aggregation + carbon unit tests with the race detector
go test -race -v ./internal/aggregation/...
```

What this verifies:
- `Start()` is non-blocking (initializes cache immediately with static fallback so `/readyz` does not block).
- WattTime token refresh and HTTP `401` retry (`TestWattTimeFetchTokenExpiry401Retry`).
- Electricity Maps zone validation and response parsing.
- Metric emission (`aitra_grid_intensity_gco2_per_kwh`) with labels `zone` and `carbon_source`.

---

## 3. Testing with Live WattTime Credentials (No Cluster Needed)

A dedicated live integration test verifies real authentication and `/v3/forecast` data retrieval directly against `api.watttime.org`.

To keep **both** your username and password hidden (blank when typed and excluded from shell history), use `read -r -s`:

```bash
read -r -s -p "WattTime Username: " WATTTIME_USERNAME; echo
read -r -s -p "WattTime Password: " WATTTIME_PASSWORD; echo
read -r -p "WattTime Region [CAISO_NORTH]: " WATTTIME_REGION
WATTTIME_REGION=${WATTTIME_REGION:-CAISO_NORTH}
export WATTTIME_USERNAME WATTTIME_PASSWORD WATTTIME_REGION

go test -v -count=1 -run TestWattTimeLiveIntegration ./internal/aggregation/carbon/
```

**Expected Output:**
```text
=== RUN   TestWattTimeLiveIntegration
    integration_test.go:28: WattTime client ready: region=CAISO_NORTH
    integration_test.go:38: ✅  Live MOER for CAISO_NORTH = 427.92 gCO2/kWh
--- PASS: TestWattTimeLiveIntegration (2.81s)
PASS
```

*(Note: WattTime updates CAISO_NORTH MOER values every 5 minutes, so running again within the same 5-minute window returns the same reading until the next window.)*

---

## 4. Testing the Full Aggregation Service (With Kubernetes / `kubeconfig`)

The `aggregation-service` binary requires a Kubernetes config (either in-cluster or via `--kubeconfig`) at startup to resolve pod metadata and node hardware.

### Option A: Run with WattTime
```bash
read -r -s -p "WattTime Username: " WATTTIME_USERNAME; echo
read -r -s -p "WattTime Password: " WATTTIME_PASSWORD; echo
export WATTTIME_USERNAME WATTTIME_PASSWORD WATTTIME_REGION="CAISO_NORTH"

go run ./cmd/aggregation-service \
  --cluster=test-cluster \
  --kubeconfig="$HOME/.kube/config" \
  --metrics-addr=:8080 \
  --carbon-source=watttime \
  --grid-gco2-per-kwh=400
```

### Option B: Run with Electricity Maps
```bash
read -r -s -p "Electricity Maps API Token: " ELECTRICITY_MAPS_API_TOKEN; echo
export ELECTRICITY_MAPS_API_TOKEN ELECTRICITY_MAPS_ZONE="US-CAL-CISO"

go run ./cmd/aggregation-service \
  --cluster=test-cluster \
  --kubeconfig="$HOME/.kube/config" \
  --metrics-addr=:8080 \
  --carbon-source=electricitymaps \
  --electricity-maps-zone=US-CAL-CISO \
  --grid-gco2-per-kwh=400
```

### Option C: Run with Static Fallback (Air-gapped / No API Keys)
```bash
go run ./cmd/aggregation-service \
  --cluster=test-cluster \
  --kubeconfig="$HOME/.kube/config" \
  --metrics-addr=:8080 \
  --carbon-source=static \
  --grid-gco2-per-kwh=400
```

### Verify Prometheus Metrics Output
Once the service is running and receives a measurement window from an agent (or test client), scrape `/metrics`:

```bash
curl -s http://localhost:8080/metrics | grep -E 'aitra_grid_intensity_gco2_per_kwh|aitra_co2_per_token_grams'
```

**Expected Metric Format:**
```prometheus
aitra_grid_intensity_gco2_per_kwh{carbon_source="watttime",zone="CAISO_NORTH"} 427.92
```
- `carbon_source` will be `"watttime"`, `"electricitymaps"`, or `"static"` / `"manual"`.
- If the live API fails at startup, the service boots immediately using `--grid-gco2-per-kwh` (`carbon_source="static"`), ensuring `/readyz` always succeeds.

---

## 5. Testing via Helm / Kubernetes Deployment

### Step 1: Create the Kubernetes Secret

**For WattTime** (default secret name: `aitra-watttime-credentials`):
```bash
kubectl create secret generic aitra-watttime-credentials \
  --from-literal=username="<your-username>" \
  --from-literal=password="<your-password>" \
  --from-literal=region="CAISO_NORTH"
```

**For Electricity Maps** (default secret name: `aitra-electricitymaps-token`):
```bash
kubectl create secret generic aitra-electricitymaps-token \
  --from-literal=token="<your-api-token>"
```

### Step 2: Configure `values.yaml` (or `--set` flags)

**For WattTime:**
```yaml
siteConfig:
  gridZone: "CAISO_NORTH"
  carbonIntensityFallback: 400
  carbonSource: watttime

externalApis:
  electricityMaps:
    enabled: false
  wattTime:
    enabled: true
    secretName: aitra-watttime-credentials
```

**For Electricity Maps:**
```yaml
siteConfig:
  gridZone: "US-CAL-CISO"
  carbonIntensityFallback: 400
  carbonSource: electricitymaps

externalApis:
  electricityMaps:
    enabled: true
    secretName: aitra-electricitymaps-token
    secretKey: token
  wattTime:
    enabled: false
```

### Step 3: Lint & Deploy
```bash
helm lint helm/aitra-meter
helm upgrade --install aitra-meter helm/aitra-meter -f values.yaml
```
