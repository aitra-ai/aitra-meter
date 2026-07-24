# Per-Model Attribution & Demo Kit — Deploy and Test Guide

How to build, deploy, and verify everything on this branch: the `--per-model`
measurement path and the KubeCon demo kit (fleet, dashboards, traffic
console). Written against a single 8-GPU node; adjust names for your cluster.

## 1. Prerequisites

| Requirement | Notes |
|---|---|
| Kubernetes ≥ 1.28 | validated on v1.32; worker node with NVIDIA GPUs |
| NVIDIA device plugin | pods request `nvidia.com/gpu`; the kubelet checkpoint at `/var/lib/kubelet/device-plugins/kubelet_internal_checkpoint` is how GPU→pod ownership is read |
| dcgm-exporter | per-GPU cumulative energy source. **Set the collect interval ≤ half your window** (`-c 2500` for 5 s windows — the 30 s default aliases short windows into zero/spike deltas) |
| Docker (or any builder) on a node | measurement images are built locally and used with `imagePullPolicy: Never`; no registry needed |
| vLLM serving image on the node | manifests reference `vllm/vllm-openai:v0.19.0`; retag whatever genuine vLLM image you have |
| Model weights on the host | `/root/aitra-models/<dir>`, hostPath-mounted; see §5 of the runbook for download tooling |

## 2. Build

```bash
# Option A — build on the GPU node (no Go toolchain needed there):
docker build -f build/measurement-agent/Dockerfile   -t aitra-meter/measurement-agent:dev .
docker build -f build/aggregation-service/Dockerfile -t aitra-meter/aggregation-service:dev .

# Option B — cross-compile elsewhere (e.g. a laptop) and wrap on the node:
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o aggregation-service ./cmd/aggregation-service
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o measurement-agent  ./cmd/measurement-agent
# then on the node: FROM alpine + COPY binary (see scripts/deploy-a100-demo.sh)

# containerd runtimes additionally need:
docker save aitra-meter/measurement-agent:dev | ctr -n k8s.io images import -
```

## 3. Deploy (order matters)

```bash
NS=aitra-system

# 3.1 Core measurement stack
kubectl apply -f deploy/aitra-system.yaml        # namespace, RBAC, aggregation-service
kubectl apply -f deploy/prometheus.yaml          # scrapes aggregation :8080
kubectl apply -f deploy/dcgm-exporter.yaml       # per-GPU energy counters (-c 2500)
kubectl apply -f deploy/measurement-agent.yaml   # DaemonSet, --per-model --window-seconds=5

# 3.2 Demo surfaces
kubectl -n $NS create secret generic grafana-admin --from-literal=password='<pick>'
kubectl -n $NS create configmap aitra-demo-dashboards \
  --from-file=deploy/grafana-dashboards/ --dry-run=client -o yaml | kubectl apply -f -
kubectl apply -f deploy/grafana.yaml             # NodePort 30852, anonymous read-only
kubectl apply -f deploy/dashboard.yaml           # Meter dashboard, NodePort 30851

# 3.3 Model fleet + baseline traffic (weights must exist on the host first)
kubectl apply -f deploy/vllm-fleet.yaml
kubectl apply -f deploy/vllm-fleet-loadgen.yaml

# 3.4 Demo traffic console (optional but recommended)
kubectl -n $NS create secret generic demo-control-key --from-literal=key='<access key>'
kubectl apply -f deploy/demo-control.yaml        # NodePort 30853; open http://<node>:30853/?k=<key>
```

One-shot equivalent: `scripts/deploy-a100-demo.sh` (build + 3.1–3.3).

Notes:
- The agent needs `runAsUser: 0` and a read-only hostPath of the kubelet
  checkpoint dir — both are already in the manifest.
- `--per-model` refuses to start unless the energy provider implements
  per-device readings (dcgm does; nvml does not yet).
- The console's Secret is optional: without it the panel runs open — LAN only
  in that case, never tunnel it unauthenticated.

## 4. Test

### 4.1 Unit / static (any machine, no cluster)

```bash
go build ./...
go vet ./...
go test ./...
```

Key suites: `internal/agent` (checkpoint parsing, energy attribution incl.
TP>1 and counter resets, full discover→attribute→report loop against fake
k8s + fake vLLM + fake gRPC), `internal/aggregation` (J/token arithmetic,
residual/idle/quiet-window semantics, min-token gate, CV tracker,
cost/carbon derivation).

### 4.2 Pipeline smoke (on the cluster)

```bash
NODE=<node-ip>

# every component up?
kubectl -n aitra-system get pods

# agent discovered the model pods? (one log line per pod)
kubectl -n aitra-system logs ds/aitra-meter-agent | grep "discovered GPU pod"

# J/token series present — one per model, none identical (leakage check):
curl -s "http://$NODE:30990/api/v1/query?query=aitra_j_per_token" | jq \
  '[.data.result[] | {m:.metric.model, v:.value[1]}]'

# power reconciliation: serving + idle ≈ node total from nvidia-smi
curl -s "http://$NODE:30990/api/v1/query" \
  --data-urlencode 'query=sum(aitra_gpu_power_watts{gpu_id!="idle"})'
curl -s "http://$NODE:30990/api/v1/query" \
  --data-urlencode 'query=sum(aitra_idle_power_watts)'
```

### 4.3 Semantics checks (what correct behaviour looks like)

| Action | Expected within ~10 s |
|---|---|
| Stop all traffic to one model | its `aitra_j_per_token` reads **0** (not the last value); its power series keeps showing the true resting draw |
| Resume traffic | real J/token value returns on the next window |
| Scale a model deployment to 0 | its GPU's energy moves into the `idle` residual; the model's series stops updating |
| Windows with < 30 output tokens | treated as quiet (zero efficiency) — boundary windows never show inflated values |
| Sanity math | `aitra_gpu_power_watts{gpu_id="<model>"}` ÷ its `rate(aitra_model_tokens_total[1m])` ≈ its J/token |

### 4.4 Console smoke

```bash
K=<access key>; H="http://$NODE:30853"
curl -s -o /dev/null -w '%{http_code}\n' $H/                      # 401 without key
curl -s -o /dev/null -w '%{http_code}\n' "$H/?k=$K"               # 200
curl -s -X POST -H "X-Demo-Key: $K" -H 'Content-Type: application/json' \
  $H/api/start -d '{"models":["qwen3.5-0.8b"],"shape":"balanced","concurrency":8,"duration":"5m"}'
curl -s -X POST -H "X-Demo-Key: $K" $H/api/stop                   # kill switch
curl -s -X POST -H "X-Demo-Key: $K" -H 'Content-Type: application/json' \
  $H/api/loadgen -d '{"on":false}'                                # idle-state toggle
# out-of-whitelist input must be rejected:
curl -s -X POST -H "X-Demo-Key: $K" -H 'Content-Type: application/json' \
  $H/api/start -d '{"models":["qwen3.5-0.8b"],"shape":"balanced","concurrency":999}'
```

### 4.5 Acceptance (before calling a demo ready)

The §9-style gates, in `docs/demo/kubecon-runbook.md` §1: distinct
non-identical series for all co-resident models; isolated vs co-resident
ground truth within CV tolerance; TP=k sums exactly its k GPUs (TP=1 must
not read node-total); idle GPUs report idle-only.

## 5. Troubleshooting

| Symptom | Likely cause |
|---|---|
| All models read as residual/idle | checkpoint unreadable — verify the hostPath mount and `runAsUser: 0` |
| Zero/spike alternating deltas | dcgm-exporter collect interval longer than the measurement window (`-c` flag) |
| Model name shows the pod name | pod's vLLM `/metrics` unreachable from the agent (check pod IP/port) or no `model_name` label; add the `aitra-ai.github.io/model-name` pod label as fallback |
| A model's tile frozen | that pod stopped reporting entirely — check the pod, not the meter |
| Console job "failed" | usually the job hit its `activeDeadlineSeconds` hard cap — by design for runaway protection |
