# Getting started

This guide walks you through installing Aitra Meter on a Kubernetes cluster and getting your first J/token measurement.

**Time required:** 15 minutes  
**Prerequisites:** A Kubernetes cluster with at least one GPU node, `kubectl` and `helm` installed.

---

## 1. Label your GPU nodes

Aitra Meter's measurement agent schedules only to nodes with the `aitra-ai.github.io/gpu=true` label.

```bash
# List your nodes to find GPU-bearing ones
kubectl get nodes -o wide

# Label each GPU node
kubectl label node <your-gpu-node> aitra-ai.github.io/gpu=true
```

---

## 2. Add the Helm repository

```bash
helm repo add aitra https://aitra-ai.github.io/helm-charts
helm repo update
```

---

## 3. Install Aitra Meter

```bash
helm install aitra-meter aitra/aitra-meter   --namespace aitra-system   --create-namespace   --set cluster.name=my-cluster   --set siteConfig.electricityCostPerKwh=0.12   --set siteConfig.carbonIntensityFallback=400
```

This installs:
- The measurement agent DaemonSet (one pod per labeled GPU node)
- The aggregation service Deployment
- The dashboard Deployment
- A Prometheus ServiceMonitor (auto-registers if kube-prometheus-stack is present)
- A ClickHouse instance (subchart, for historical storage)
- MeasurementPolicy and SiteConfig CRDs

---

## 4. Verify the installation

```bash
# All pods should be Running within 60 seconds
kubectl get pods -n aitra-system

# Expected output:
# NAME                                      READY   STATUS    RESTARTS   AGE
# aitra-meter-agent-<hash>                  1/1     Running   0          45s
# aitra-meter-aggregation-<hash>            1/1     Running   0          45s
# aitra-meter-dashboard-<hash>              1/1     Running   0          45s
# aitra-meter-clickhouse-0                  1/1     Running   0          45s
```

---

## 5. Label your inference workloads

Aitra Meter reads the workload type from a pod annotation. Without it, measurements appear as `workload=unknown`.

```yaml
# Add to your inference Deployment spec
spec:
  template:
    metadata:
      annotations:
        aitra-ai.github.io/workload: chat      # chat | code | reasoning | batch
        aitra-ai.github.io/team: platform
        aitra-ai.github.io/cost-centre: cc-1102
```

---

## 6. View your first measurement

```bash
# Port-forward the dashboard
kubectl port-forward -n aitra-system svc/aitra-meter-dashboard 3000:3000

# Open http://localhost:3000
```

You should see View 1 — J/token by workload × model × hardware — populated within one scrape interval (default 15 seconds) of inference traffic arriving.

---

## 7. Check metrics in Prometheus

```bash
# Port-forward Prometheus (if using kube-prometheus-stack)
kubectl port-forward -n monitoring svc/prometheus-operated 9090:9090

# Query current J/token
# http://localhost:9090/graph?g0.expr=aitra_j_per_token
```

---

## 8. Configure carbon intensity (optional)

To get live gCO₂/token readings, add your ElectricityMaps API key:

```bash
# Create the secret
kubectl create secret generic aitra-electricitymaps-token   --namespace aitra-system   --from-literal=token=<your-api-key>

# Update the Helm release
helm upgrade aitra-meter aitra/aitra-meter   --namespace aitra-system   --set siteConfig.carbonSource=electricitymaps   --set siteConfig.gridZone=SG   --set externalApis.electricityMaps.enabled=true
```

---

## 9. Configure namespace chargeback (optional)

To enable the namespace chargeback view, update your SiteConfig with the PUE for your data centre:

```bash
helm upgrade aitra-meter aitra/aitra-meter   --namespace aitra-system   --set siteConfig.pue=1.35
```

---

## Next steps

- [Metrics reference](../reference/metrics.md) — all Prometheus metrics exposed
- [Configuration reference](../reference/configuration.md) — all Helm values and CRD fields
- [Operations guide](operations.md) — upgrading, scaling, air-gapped install
- [Writing a provider](writing-a-provider.md) — add support for a new inference server or energy backend
