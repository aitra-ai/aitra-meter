# Operations guide

## Upgrading

```bash
helm repo update
helm upgrade aitra-meter aitra/aitra-meter --namespace aitra-system
```

The measurement agent DaemonSet rolling-updates one node at a time. In-progress measurements on a node are abandoned (not corrupted) during a pod restart. The SQLite schema is backward-compatible across minor versions.

For major version upgrades, read the migration notes in [CHANGELOG.md](../../CHANGELOG.md) before upgrading.

---

## Resource sizing

### Measurement agent (per GPU node)

| Resource | Minimum | Recommended |
|---|---|---|
| CPU request | 100m | 200m |
| CPU limit | 500m | 500m |
| Memory request | 128Mi | 256Mi |
| Memory limit | 256Mi | 256Mi |

The agent's CPU usage scales with GPU count per node and sampling frequency. Default 10 Hz NVML sampling is within the minimum sizing above.

### Aggregation service

| Resource | Minimum | Recommended (10 nodes) |
|---|---|---|
| CPU request | 200m | 500m |
| CPU limit | 1000m | 2000m |
| Memory request | 256Mi | 512Mi |
| Memory limit | 512Mi | 1Gi |

Scales with number of nodes × models × namespaces (metric cardinality). For clusters with >50 GPU nodes, increase memory limit to 2Gi.

### Storage

Aitra Meter uses SQLite by default, stored at `/data/aitra.db` inside the aggregation service pod.

**Persistent storage:** mount a PersistentVolume at `/data` to survive pod restarts:

```yaml
storage:
  config:
    path: /data/aitra.db
```

**Backup:** copy the SQLite file from the pod:

```bash
kubectl cp aitra-system/$(kubectl get pod -n aitra-system -l app.kubernetes.io/component=aggregation-service -o jsonpath='{.items[0].metadata.name}'):/data/aitra.db ./aitra-backup-$(date +%Y%m%d).db
```

