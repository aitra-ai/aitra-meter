# Prometheus alerting rules

Reference Prometheus alerting rules for Aitra Meter metrics.

Copy these into your Prometheus `PrometheusRule` resource and adjust thresholds to your environment.

```yaml
apiVersion: monitoring.coreos.com/v1
kind: PrometheusRule
metadata:
  name: aitra-meter-alerts
  namespace: monitoring
  labels:
    prometheus: kube-prometheus
    role: alert-rules
spec:
  groups:
    - name: aitra-meter.efficiency
      interval: 60s
      rules:

        - alert: AitraHighJTokenDelta
          expr: |
            (aitra_j_per_token - on(model, hardware) aitra_calibration_reference_j_per_token)
              / on(model, hardware) aitra_calibration_reference_j_per_token > 0.20
          for: 10m
          labels:
            severity: warning
          annotations:
            summary: "J/token is >20% above calibration baseline"
            description: "{{ $labels.model }} on {{ $labels.hardware }} in namespace {{ $labels.namespace }} is running at {{ $value | humanizePercentage }} above the calibration baseline. Check for thermal throttling, reduced batch size, or hardware degradation."

        - alert: AitraHighIdleRatio
          expr: aitra_idle_time_ratio > 0.5
          for: 30m
          labels:
            severity: warning
          annotations:
            summary: "GPU node idle >50% of the time"
            description: "Node {{ $labels.node }} has been idle for more than 50% of the last hour. Consider consolidating workloads or scaling down."

        - alert: AitraMeasurementUnstable
          expr: aitra_measurement_window_stable == 0
          for: 5m
          labels:
            severity: warning
          annotations:
            summary: "Aitra Meter measurement CV above threshold"
            description: "Measurements on node {{ $labels.node }} for model {{ $labels.model_name }} have CV above 3% for 5 minutes. Chargeback figures for this combination may be unreliable."

        - alert: AitraAgentDown
          expr: up{job="aitra-meter-agent"} == 0
          for: 2m
          labels:
            severity: critical
          annotations:
            summary: "Aitra Meter measurement agent is down"
            description: "The measurement agent on node {{ $labels.node }} has been unreachable for 2 minutes. J/token measurements for this node are not being collected."

    - name: aitra-meter.cost
      interval: 300s
      rules:

        - alert: AitraNamespaceBudgetWarning
          expr: |
            (
              increase(aitra_namespace_energy_joules_total[30d])
              * on(namespace) aitra_cost_per_million_tokens_usd / 1e6
            ) / on(namespace) group_left() (
              aitra_measurement_policy_budget_usd
            ) > 0.80
          for: 0m
          labels:
            severity: warning
          annotations:
            summary: "Namespace AI spend at 80% of monthly budget"
            description: "Namespace {{ $labels.namespace }} has consumed 80% of its monthly AI inference budget."

        - alert: AitraHighClusterIdleCost
          expr: |
            sum(aitra_idle_power_watts) * 0.001
            * on() aitra_cost_per_million_tokens_usd > 10
          for: 60m
          labels:
            severity: info
          annotations:
            summary: "Cluster idle energy cost exceeding $10/hr"
            description: "The cluster is consuming significant energy with no inference output. Current idle cost rate: ${{ $value | humanize }}/hr."
```

## Recording rules

Recording rules improve query performance for dashboard views.

```yaml
    - name: aitra-meter.recording
      interval: 60s
      rules:

        - record: aitra:j_per_token:rate5m
          expr: rate(aitra_gpu_energy_joules_total[5m]) / rate(aitra_namespace_tokens_total[5m])

        - record: aitra:cluster_idle_power_watts
          expr: sum(aitra_idle_power_watts)

        - record: aitra:namespace_monthly_cost_usd
          expr: |
            increase(aitra_namespace_energy_joules_total[30d])
            * on(namespace) aitra_cost_per_million_tokens_usd / 1e6
```
