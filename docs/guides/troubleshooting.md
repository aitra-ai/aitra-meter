# Troubleshooting

## Measurement agent pod not starting

**Symptom:** DaemonSet pod stays in `Pending` state.

**Check node labels:**
```bash
kubectl get nodes --show-labels | grep aitra-ai.github.io/gpu
```
If no nodes are labeled, the DaemonSet has no valid target. Label at least one GPU node:
```bash
kubectl label node <node-name> aitra-ai.github.io/gpu=true
```

**Check PodSecurityPolicy / PodSecurity:**  
The measurement agent requires `privileged: true`. If your cluster enforces a restrictive PodSecurity standard on the `aitra-system` namespace, add a label to allow privileged pods:
```bash
kubectl label namespace aitra-system pod-security.kubernetes.io/enforce=privileged
```

---

## NVML access denied

**Symptom:** Measurement agent logs show `NVML error: insufficient permissions` or `failed to initialize NVML`.

**Cause:** The pod is running but cannot access NVML. This usually means `hostPID: true` or `privileged: true` is being overridden by a security policy.

**Check:**
```bash
kubectl describe pod -n aitra-system -l app=aitra-meter-agent | grep -A5 "Security Context"
```

Both `privileged: true` and `hostPID: true` must be present. If your cluster's PodSecurity policy prevents this, create a policy exception for the `aitra-system` namespace.

---

## No metrics appearing in Prometheus

**Symptom:** Prometheus shows no `aitra_*` metrics.

**Check ServiceMonitor:**
```bash
kubectl get servicemonitor -n aitra-system
kubectl describe servicemonitor -n aitra-system aitra-meter
```

The ServiceMonitor's `namespaceSelector` must match where Prometheus Operator is watching. Set `prometheus.serviceMonitor.namespace` in `values.yaml` to the namespace where your Prometheus Operator is installed (commonly `monitoring`).

**Check Prometheus targets:**  
Open Prometheus UI → Status → Targets. Look for `aitra-meter` in the list. If it shows `down`, check the error message — usually a network policy blocking scrape traffic.

---

## J/token shows as `workload=unknown`

**Symptom:** All measurements in View 1 show `workload: unknown`.

**Cause:** Your inference pods do not have the `aitra-ai.github.io/workload` annotation.

**Fix:** Add the annotation to your Deployment:
```yaml
spec:
  template:
    metadata:
      annotations:
        aitra-ai.github.io/workload: chat
```

`unknown` is not an error — measurements are collected regardless. Add labels when you need workload-level attribution.

---

## Namespace chargeback shows `proportional` attribution unexpectedly

**Symptom:** View 3 shows `proportional` for a namespace you expected to have `direct` attribution.

**Cause:** Multiple namespaces are sharing a single vLLM instance. Direct attribution requires one vLLM pod per namespace.

**Check:**
```bash
# Count vLLM pods per namespace
kubectl get pods --all-namespaces -l app=vllm -o custom-columns=NS:.metadata.namespace,NAME:.metadata.name
```

If multiple namespaces share one pod, either deploy separate vLLM instances per namespace (recommended for accurate chargeback), or accept proportional attribution and configure it explicitly in `MeasurementPolicy`.

---

## Storage errors

**Symptom:** Aggregation service logs show `sqlite: unable to open database file` or `no such table: aitra_measurements`.

**Check the storage path:**

```bash
kubectl exec -n aitra-system deploy/aitra-meter-aggregation-service -- ls -lh /data/
```

If the file is missing, the PersistentVolume may not be mounted. Check:

```bash
kubectl describe pvc -n aitra-system
```

If running without a PV (ephemeral), data is lost on pod restart — this is expected. Mount a PV at `/data` for persistent chargeback history.


## Carbon figures show `last_known` source

**Symptom:** View 5 shows `carbon_source: last_known` instead of `electricitymaps`.

**Cause:** The ElectricityMaps API is unreachable from the cluster, or the API key is missing/expired.

**Check:**
```bash
# Verify secret exists
kubectl get secret -n aitra-system aitra-electricitymaps-token

# Check aggregation service logs for API errors
kubectl logs -n aitra-system -l app=aitra-meter-aggregation | grep electricitymaps
```

For air-gapped clusters, set `airGapped.enabled: true` — this suppresses the `last_known` warning and uses the manual ConfigMap value instead.

---

## High CV gate instability

**Symptom:** `aitra_measurement_window_stable` is frequently `0`. Prometheus alert fires for `aitra_measurement_cv > 0.03`.

**Common causes:**
- Batch size is too small — insufficient requests per window to stabilise energy readings
- Thermal throttling — GPU is throttling under load, causing power fluctuations
- Mixed precision — FP16 and FP8 requests are being batched together on the same instance

**Investigate:**
```bash
# Check CV over time for the unstable node
kubectl exec -n aitra-system -l app=aitra-meter-agent --   cat /var/log/aitra/cv.log | tail -50
```

CV instability does not drop measurements — it flags them `unstable=true`. Unstable measurements are included in chargeback reports with a warning label.

---

## Getting help

- Open an issue: [github.com/aitra-ai/aitra-meter/issues](https://github.com/aitra-ai/aitra-meter/issues)
- Review ADRs in `docs/adr/` for design decisions
- Check the [glossary](../reference/glossary.md) for term definitions
