# KEDA integration

Aitra Meter exposes `aitra_j_per_token` and `aitra_idle_time_ratio` as Prometheus
metrics. KEDA can trigger autoscaling on either.

## Prerequisites

KEDA installed in the cluster:

```bash
helm install keda kedacore/keda --namespace keda --create-namespace
```

Aitra Meter installed and producing measurements. Verify:

```bash
kubectl port-forward -n monitoring svc/prometheus-operated 9090:9090
# http://localhost:9090/graph?g0.expr=aitra_idle_time_ratio
```

## Scale to zero on idle

Apply the reference ScaledObject:

```bash
kubectl apply -f examples/keda/scale-on-idle.yaml
```

When `aitra_idle_time_ratio` exceeds 0.40 for 300 seconds, KEDA scales the
`inference-chat` Deployment to zero. Replicas scale back up when requests resume.

Watch it work:

```bash
kubectl get events -n inference-prod --sort-by='.lastTimestamp' | grep ScaledObject
```

## Scale on efficiency regression

```bash
kubectl apply -f examples/keda/scale-on-efficiency-regression.yaml
```

Triggers when J/token exceeds 120% of its 1-hour prior value. Useful for
detecting model version regressions or GPU thermal throttling.

## Customising

Edit `scaleTargetRef.name` to match your Deployment name. Edit the `query`
label selectors to match your namespace and model. The `threshold` is a string
representation of the numeric threshold.

Both ScaledObjects use the standard `prometheus` KEDA trigger — no additional
KEDA plugins required.
