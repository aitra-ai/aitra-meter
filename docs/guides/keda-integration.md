# KEDA integration

Aitra Meter exposes two Prometheus metrics that KEDA can use as scaling triggers:

- `aitra_idle_time_ratio` — fraction of the last hour a GPU node spent idle (no active inference requests)
- `aitra_j_per_token` — joules per output token, used to detect efficiency regressions

Reference ScaledObjects are in [`examples/keda/`](../../examples/keda/).

## Prerequisites

- Aitra Meter installed and producing measurements
- KEDA installed in the cluster:

```bash
helm repo add kedacore https://kedacore.github.io/charts
helm repo update
helm install keda kedacore/keda --namespace keda --create-namespace
```

## Scale to zero on idle

When `aitra_idle_time_ratio` exceeds 0.4 (40% of the last hour was idle), KEDA scales the inference deployment to zero replicas after a 5-minute cooldown. It scales back up automatically when traffic resumes.

```bash
kubectl apply -f examples/keda/scale-on-idle.yaml
```

Verify it is active:

```bash
kubectl get scaledobject -n inference-prod
```

Watch the scale-down in the Aitra Meter dashboard — View 4 (idle consumption) shows the shift from serving power to idle power in real time.

## Alert on efficiency regression

When `aitra_j_per_token` rises more than 20% above the 1-hour rolling average, the ScaledObject fires. Use this as a signal alongside the reference alerting rules in [`examples/alerting-rules.yaml`](../../examples/alerting-rules.yaml).

```bash
kubectl apply -f examples/keda/scale-on-efficiency-regression.yaml
```

## Verify

```bash
# Check KEDA is reading the metrics
kubectl get scaledobject inference-idle-scaledown -n inference-prod -o jsonpath='{.status}'

# Watch events
kubectl get events -n inference-prod --sort-by=.lastTimestamp | grep ScaledObject
```
