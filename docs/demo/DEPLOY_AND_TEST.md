# Per-Model Attribution & Demo Kit — Deploy and Test Guide

How to build, deploy, verify, and tear down everything on this branch: the
`--per-model` measurement path and the KubeCon demo kit (model fleet, Grafana
dashboards, demo-traffic console). Written against a single 8-GPU worker
node; adjust node names and model lists for your cluster.

Companion documents:

- `docs/demo/kubecon-runbook.md` — how to *run the demos* (talk tracks,
  acceptance gates, fallback ladder). This file covers *deploying and
  testing the software*.
- `docs/adr/0007-per-gpu-energy-attribution.md` — why the attribution works
  the way it does.

---

## 0. What gets deployed

```
GPU node
├── vllm fleet ×8            one pod per GPU, models served from hostPath
├── dcgm-exporter            per-GPU cumulative energy counters   (DaemonSet)
├── aitra-meter-agent        --per-model attribution loop         (DaemonSet)
├── aitra-meter-aggregation  J/token computation + semantics      (Deployment)
├── prometheus               metric storage, 2-day retention      (Deployment)
├── grafana                  demo dashboards, anonymous read-only (Deployment)
├── aitra-meter-dashboard    tables / chargeback / carbon         (Deployment)
└── demo-control             traffic console, access-key gated    (Deployment)
```

Data flow: dcgm-exporter + each vLLM pod's own `/metrics` → agent (one
attribution round per window) → aggregation (gRPC) → Prometheus (scrape) →
Grafana / dashboard. The console only creates capped batch Jobs and toggles
the baseline load generator; it never touches the measurement path.

---

## 1. Prerequisites

### 1.1 Cluster

| Requirement | Detail | Verify with |
|---|---|---|
| Kubernetes ≥ 1.28 | validated on v1.32 (docker via cri-dockerd; containerd works, see §2.3) | `kubectl version` |
| NVIDIA device plugin | models request whole GPUs (`nvidia.com/gpu: 1`) | `kubectl describe node <n> \| grep nvidia.com/gpu` |
| Kubelet device-plugin checkpoint | the agent's source of GPU→pod ownership | `sudo head -c 200 /var/lib/kubelet/device-plugins/kubelet_internal_checkpoint` on the node — should print JSON containing `PodDeviceEntries` |
| GPU node label | fleet/agent manifests select `nvidia.com/gpu.present: "true"` (set automatically by NVIDIA GPU feature discovery; label manually otherwise) | `kubectl get node <n> --show-labels \| grep gpu.present` |

### 1.2 Images

| Image | Purpose | Notes |
|---|---|---|
| `aitra-meter/measurement-agent:dev` | agent | built locally in §2, `imagePullPolicy: Never` — no registry needed |
| `aitra-meter/aggregation-service:dev` | aggregation | same |
| `vllm/vllm-openai:v0.19.0` | model serving, bench jobs, console runtime | must be a **genuine vLLM image** (entrypoint `vllm serve`). If your cached copy has another tag: `docker tag <your-vllm-image> vllm/vllm-openai:v0.19.0`. Beware lookalikes: at least one downstream image reuses vLLM version tags but boots supervisord and rejects `--model`. Sanity check: `docker run --rm <img> --help \| head -3` should print vLLM's usage text |
| `nvcr.io/nvidia/k8s/dcgm-exporter:3.3.9-*` | energy counters | any recent dcgm-exporter works |
| `grafana/grafana:11.x`, `prom/prometheus:v2.5x`, `curlimages/curl` | surfaces & loadgen | pulled normally (`IfNotPresent`) |

### 1.3 Host directories (on the GPU node)

| Path | Content |
|---|---|
| `/root/aitra-models/<dir>` | model weights, one directory per model (see §3.3); hostPath-mounted read-only into vLLM pods |
| `/root/aitra-datasets/sharegpt_10k.json` | ShareGPT subset for real-conversation load (console "Real conversations" shape) |

### 1.4 Timing configuration (all knobs in one place)

These four must be consistent — each stage should be at most half of the next:

| Knob | Where | Demo value | Rule |
|---|---|---|---|
| dcgm-exporter collect interval | `deploy/dcgm-exporter.yaml` args `-c 2500` (ms) | 2.5 s | **≤ half the measurement window.** The 30 s default aliases short windows: consecutive windows read the same counter → zero/spike deltas |
| Measurement window | `deploy/measurement-agent.yaml` `--window-seconds=5` | 5 s | trade-off: shorter = snappier display, fewer tokens per window (see min-token gate below) |
| Prometheus scrape | `deploy/prometheus.yaml` `scrape_interval: 2s` | 2 s | ≤ half the window so no window is skipped |
| Grafana refresh | dashboard JSONs `"refresh": "2s"` + `GF_DASHBOARDS_MIN_REFRESH_INTERVAL=1s` | 2 s | end-to-end display latency ≈ window + scrape + refresh ≈ 6–9 s |

Related constant: the aggregation service treats windows with **< 30 output
tokens** as quiet (`minServingTokens` in `internal/aggregation/loop.go`) — at
a 5 s window that is a ~6 tok/s floor. If you lengthen windows you may raise
this; if you shorten below 5 s, lower it.

---

## 2. Build

### 2.1 On the GPU node (no Go toolchain needed)

```bash
git clone <repo> && cd aitra-meter && git checkout demo/kubecon-per-model-attribution-89
docker build -f build/measurement-agent/Dockerfile   -t aitra-meter/measurement-agent:dev .
docker build -f build/aggregation-service/Dockerfile -t aitra-meter/aggregation-service:dev .
docker images | grep aitra-meter   # expect both :dev tags, ~50–70 MB each
```

### 2.2 Cross-compile elsewhere (registry-less clusters, or when the node
cannot reach a Go module proxy)

```bash
# on any machine with Go ≥ 1.25 (both binaries are pure Go, CGO-free):
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o aggregation-service ./cmd/aggregation-service
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o measurement-agent  ./cmd/measurement-agent
scp aggregation-service measurement-agent <node>:/tmp/

# on the node — wrap in a minimal image (alpine or scratch both fine):
cat > /tmp/Dockerfile.agg <<'EOF'
FROM alpine:latest
COPY aggregation-service /aggregation-service
EXPOSE 8080 9091
ENTRYPOINT ["/aggregation-service"]
EOF
(cd /tmp && docker build -f Dockerfile.agg -t aitra-meter/aggregation-service:dev .)
# repeat analogously for the agent
```

### 2.3 containerd runtimes only

Docker-built images are invisible to containerd's kubelet; import them:

```bash
docker save aitra-meter/measurement-agent:dev   | ctr -n k8s.io images import -
docker save aitra-meter/aggregation-service:dev | ctr -n k8s.io images import -
```

(On cri-dockerd clusters this step is unnecessary.)

### 2.4 Multi-node clusters

`imagePullPolicy: Never` means every GPU node needs the images. Either build
per node, or transfer:

```bash
docker save aitra-meter/measurement-agent:dev | gzip | ssh <other-node> 'gunzip | docker load'
```

---

## 3. Deploy

All resources live in namespace `aitra-system`. **Order matters** — later
steps assume earlier Services exist. `scripts/deploy-a100-demo.sh` automates
3.1 + 3.2 + 3.4.

### 3.1 Core measurement stack

```bash
kubectl apply -f deploy/aitra-system.yaml        # namespace, RBAC (read-only pods/nodes), aggregation-service
kubectl apply -f deploy/prometheus.yaml          # scrapes aggregation :8080 every 2s, NodePort 30990
kubectl apply -f deploy/dcgm-exporter.yaml       # DaemonSet, -k pod attribution, -c 2500
kubectl apply -f deploy/measurement-agent.yaml   # DaemonSet, --per-model --energy-provider=dcgm --window-seconds=5
```

**Verify before continuing:**

```bash
kubectl -n aitra-system get pods
# expect: aggregation Running, prometheus Running, one dcgm-exporter + one agent per GPU node

kubectl -n aitra-system logs ds/aitra-meter-agent --tail=5
# expect: "per-model measurement loop started" with energy_provider=dcgm
# NOT expected: "requires an energy provider with per-device data" (wrong provider)
#               or repeated checkpoint errors (bad hostPath / not root)
```

Notes on the agent manifest:
- `runAsUser: 0` is required — the checkpoint directory is root-only (0750).
- The checkpoint hostPath mount is **read-only**; the agent never writes it.
- `--per-model` refuses to start if the energy provider lacks per-device
  readings (dcgm implements it; nvml does not yet).

### 3.2 Demo surfaces

```bash
kubectl -n aitra-system create secret generic grafana-admin \
  --from-literal=password="$(head -c 24 /dev/urandom | base64 | tr -dc A-Za-z0-9 | head -c 16)"
kubectl -n aitra-system create configmap aitra-demo-dashboards \
  --from-file=deploy/grafana-dashboards/ --dry-run=client -o yaml | kubectl apply -f -
kubectl apply -f deploy/grafana.yaml             # NodePort 30852, anonymous Viewer
kubectl apply -f deploy/dashboard.yaml           # Meter dashboard, NodePort 30851
```

**Verify:** `curl -s http://<node>:30852/api/health` → `"database": "ok"`;
open `http://<node>:30852` — three dashboards in the "KubeCon Demo" folder,
Demo A is the home dashboard. Panels are empty until models serve (§3.3).

To update dashboards later: edit the JSONs, re-run the `create configmap`
command above, then `kubectl -n aitra-system rollout restart deploy/grafana`.

### 3.3 Model weights, fleet, baseline traffic

Weights layout — one directory per model under `/root/aitra-models/`,
containing `config.json`, `tokenizer*.json` and the `*.safetensors` files
(plus `model.safetensors.index.json` for sharded models). Two ways to get
them:

1. **Let the initContainers fetch** — each fleet pod's initContainer
   downloads missing files (hf-mirror primary, resumable) and skips files
   already on disk. Zero-effort but serial per pod and subject to mirror
   throttling.
2. **Pre-download on the host** (recommended for the big models) — any
   downloader works; **validate every safetensors file** before first use:
   the expected size is `8 + header_len + max(data_offsets)` from the
   file's own header. A corrupt shard fails at 60–100 % of vLLM model load
   with `SafetensorError: incomplete metadata`. Never resume a partial
   download across different mirrors — mixed-source files are how shards
   get corrupted.

```bash
kubectl apply -f deploy/vllm-fleet.yaml          # 8 Deployments, one GPU each
kubectl apply -f deploy/vllm-fleet-loadgen.yaml  # baseline: ~1 req / 2s per model
kubectl -n aitra-system get pods -l tier=vllm-fleet -w
```

Model-specific serving notes (already encoded in the manifests):
- FP8 checkpoints run on Ampere via vLLM's Marlin weight-only path — no
  Hopper needed.
- The 122B-A10B-Int4 tile barely fits one 80 GB card: it needs
  `--enforce-eager` (skips ~2 GB of CUDA-graph transients),
  `--limit-mm-per-prompt '{"image":0,"video":0}'` (multimodal-capable arch;
  encoder-cache profiling otherwise OOMs) and a 4 k context.
- Qwen3.5+ repos ship no `generation_config.json` — the fetch script treats
  it as optional; don't mark it required in forks.

**Verify:** within ~60 s of a model turning Ready, its series appears:

```bash
curl -s "http://<node>:30990/api/v1/query?query=aitra_j_per_token" | jq \
  '[.data.result[] | {model:.metric.model, jpt:.value[1]}]'
```

### 3.4 Demo traffic console (optional, recommended)

```bash
kubectl -n aitra-system create secret generic demo-control-key \
  --from-literal=key='<choose an access key>'
kubectl apply -f deploy/demo-control.yaml        # NodePort 30853
```

Open `http://<node>:30853/?k=<key>` — the key sets a 7-day cookie; the page
is bilingual (zh/EN toggle, top-right). Security properties worth knowing:

- Every page and API route returns 401 without the key. Deleting the Secret
  (+ pod restart) switches the panel to open mode — acceptable on a trusted
  LAN only; never expose the open mode publicly.
- RBAC allows exactly: batch Jobs (create/get/list/delete) in the namespace,
  and `deployments/scale` on the single resourceName `vllm-fleet-loadgen`.
  The console cannot touch model deployments or anything else.
- Every job carries `activeDeadlineSeconds` (hard cap, 4 h even in
  "continuous" mode) and `ttlSecondsAfterFinished: 900`.
- Server-side whitelists: 8 models × 4 shapes × {1,4,8,16,32,64} concurrency
  × 3 durations; anything else → HTTP 400.

Key rotation: recreate the Secret with the new value and
`kubectl -n aitra-system rollout restart deploy/demo-control`. Old links
stop working immediately.

### 3.5 Public access (optional)

The demo pattern is outbound-only tunnels (zero inbound ports, credentials
stay inside):

```bash
# on the node (cloudflared installed):
cloudflared tunnel --url http://localhost:30852 &   # Grafana  (anonymous read-only)
cloudflared tunnel --url http://localhost:30851 &   # dashboard (read-only)
cloudflared tunnel --url http://localhost:30853 &   # console  (ONLY because it is key-gated)
```

Each prints a public `https://….trycloudflare.com` URL. Quick-tunnel URLs
rotate on restart — re-share after any restart. Never tunnel the raw
Prometheus NodePort; if an external system needs queries, front it with the
token-checking `data-gateway`.

---

## 4. Test

### 4.1 Unit and static (any machine, no cluster)

```bash
go build ./... && go vet ./... && go test ./...
```

All suites must pass. What the important ones cover:

| Suite | Coverage |
|---|---|
| `internal/agent` | checkpoint parsing (NUMA-map + flat layouts, terminated-pod filtering); `attributeEnergy` math incl. TP>1 groups, counter resets, vanished/new devices; **`TestMultiLoopEndToEnd`** — the full discover → per-pod scrape → attribute → report loop against a fake k8s clientset, fake per-device energy, real httptest vLLM endpoints and a real gRPC sink, asserting exact per-pod joules, token deltas and the residual report |
| `internal/aggregation` | J/token arithmetic; residual (`_idle`) handling — idle power recorded, never stored, serving ratio untouched; quiet-window semantics — power stays on the model's series while efficiency gauges read zero; min-token gate; CV tracker (incl. near-constant-sample stability on arm64); cost/carbon derivation from site params |
| `internal/provider/energy/dcgm` | scrape parsing, per-UUID energy map, error-on-empty behaviour |

### 4.2 Pipeline smoke (on the cluster, ~2 minutes)

```bash
NODE=<node-ip>

# 1. Everything Running?
kubectl -n aitra-system get pods

# 2. Agent discovered every GPU-holding pod? (one line each)
kubectl -n aitra-system logs ds/aitra-meter-agent | grep "discovered GPU pod"

# 3. One J/token series per model, values distinct (leakage check):
curl -s "http://$NODE:30990/api/v1/query?query=aitra_j_per_token" | jq \
  '[.data.result[] | {m:.metric.model, v:.value[1]}] | sort_by(.v)'
# FAIL if two models track identical values over several minutes.

# 4. Power reconciliation — the sum test that proves attribution is real:
curl -s "http://$NODE:30990/api/v1/query" --data-urlencode \
  'query=sum(aitra_gpu_power_watts{gpu_id!="idle"}) + sum(aitra_idle_power_watts)'
nvidia-smi --query-gpu=power.draw --format=csv,noheader,nounits | awk '{s+=$1} END {print s}'
# The two figures should agree within a few percent.

# 5. Cross-check one model by arithmetic (J/token ≈ watts ÷ tok/s):
curl -s "http://$NODE:30990/api/v1/query" --data-urlencode \
  'query=aitra_gpu_power_watts{gpu_id="qwen3.5-0.8b"} / on() sum(rate(aitra_model_tokens_total{model="qwen3.5-0.8b"}[1m]))'
# should match that model's aitra_j_per_token within noise.
```

### 4.3 Display-semantics checks (what correct behaviour looks like)

Drive these from the console (or kubectl) and watch Grafana:

| Action | Expected within ~10 s | Why |
|---|---|---|
| Baseline off (console idle toggle) | every model's J/token → **0**; power panels keep showing real resting draw; collection never pauses | quiet models must not freeze at stale values |
| Baseline back on | real values return on the next window | |
| Start a high-concurrency run on one model | its J/token drops sharply; **other models' tiles unaffected** | per-model isolation under load |
| Stop that run | brief 0 (boundary windows are gated), then baseline value | windows with < 30 tokens are never displayed as measurements |
| Scale one model deployment to 0 | its GPU's power moves into the `idle` series; the freed GPU is charged to nobody | residual accounting |
| Kill switch (`stop all`) | all demo jobs deleted; fleet returns to baseline within a window | |

### 4.4 Console API smoke

```bash
K=<access key>; H="http://$NODE:30853"
curl -s -o /dev/null -w '%{http_code}\n' $H/                    # 401 (login page)
curl -s -o /dev/null -w '%{http_code}\n' "$H/?k=$K"             # 200 (+ cookie)
curl -s -o /dev/null -w '%{http_code}\n' $H/api/status          # 401 without key
curl -s -H "X-Demo-Key: $K" $H/api/status | jq '.models|length' # 8

# start / observe / stop:
curl -s -X POST -H "X-Demo-Key: $K" -H 'Content-Type: application/json' \
  $H/api/start -d '{"models":["qwen3.5-0.8b"],"shape":"balanced","concurrency":8,"duration":"5m"}'
kubectl -n aitra-system get jobs -l app=demo-load               # job present, deadline set
curl -s -X POST -H "X-Demo-Key: $K" $H/api/stop                 # {"stopped":N}

# whitelist enforcement — all of these must return an error, not a job:
curl -s -X POST -H "X-Demo-Key: $K" -H 'Content-Type: application/json' \
  $H/api/start -d '{"models":["qwen3.5-0.8b"],"shape":"balanced","concurrency":999}'
curl -s -X POST -H "X-Demo-Key: $K" -H 'Content-Type: application/json' \
  $H/api/start -d '{"models":["../../etc"],"shape":"balanced","concurrency":8}'
```

### 4.5 Latency verification (the sub-10 s claim)

```bash
# same series, 6 s apart — the value must differ under live traffic:
Q='aitra_j_per_token{model="qwen3.5-0.8b"}'
curl -s "http://$NODE:30990/api/v1/query" --data-urlencode "query=$Q" | jq -r '.data.result[0].value[1]'
sleep 6
curl -s "http://$NODE:30990/api/v1/query" --data-urlencode "query=$Q" | jq -r '.data.result[0].value[1]'
```

If values repeat across ≥ 2 checks: verify the dcgm-exporter `-c` interval
first (§1.4) — it is the usual culprit — then agent window and scrape.

### 4.6 Acceptance gates (before calling a demo ready)

Run the §9-style gates in `docs/demo/kubecon-runbook.md` §1: K distinct
non-identical series; isolated vs co-resident ground truth within CV
tolerance (measured drift on this rig: ~1 %); TP=k sums exactly its k GPUs
and TP=1 must **not** read node-total; idle GPUs report idle-only.

---

## 5. Troubleshooting

| Symptom | Likely cause / fix |
|---|---|
| Every GPU shows as residual/idle, no model series | agent cannot read the checkpoint — check the hostPath mount, `runAsUser: 0`, and that the node actually uses the device plugin (not static CPU-manager-style allocation) |
| J/token alternates zero ↔ spike each window | dcgm-exporter collect interval ≥ window (§1.4). Set `-c` ≤ half the window |
| A model's series named after its pod instead of the served name | its vLLM `/metrics` is unreachable from the agent (pod IP/port), or no `model_name` label; add the `aitra-ai.github.io/model-name` pod label as a fallback |
| Agent log: "requires an energy provider with per-device data" | `--per-model` with a provider lacking `PerDeviceEnergy` — use `--energy-provider=dcgm` |
| vLLM pod CrashLoop: `option --model not recognized` / supervisord output | wrong image behind the vLLM tag (see §1.2 lookalike warning) |
| vLLM pod CrashLoop: `SafetensorError: incomplete metadata` | corrupt shard — delete that file and re-download from a single source; validate sizes per §3.3 |
| 122B (or other near-capacity model) OOM at startup | see the §3.3 memory notes: enforce-eager + text-only + shorter context; check `gpu-memory-utilization` |
| Tiles frozen while pods run | aggregation restarted and traffic is off — gauges rebuild on the next serving/quiet window; or Prometheus target down (`up == 0`) |
| Console job ends as "failed" | usually the `activeDeadlineSeconds` hard cap — by design; use a bigger duration or the sweep's shape-aware sizing |
| Console 401 with the right key | Secret rotated after the cookie was issued — reload with `/?k=<new key>` |
| Public tunnel URL dead | cloudflared restarted; re-run the tunnel script and re-share the new URLs |

---

## 6. Update, rollback, teardown

**Update measurement code:** rebuild the image (§2), then
`kubectl -n aitra-system rollout restart deploy/aitra-meter-aggregation ds/aitra-meter-agent`.
Gauges are process-local — expect ~1 window of empty metrics after an
aggregation restart.

**Update dashboards / console:** re-apply the ConfigMap (+ `rollout restart`
of grafana / demo-control).

**Rollback:** all behaviour lives in the two `:dev` images and the manifests
— `git checkout <old-ref>`, rebuild, re-apply. Model weights on the host are
unaffected by any of it.

**Teardown (keeps namespaces/models):**

```bash
kubectl -n aitra-system delete -f deploy/demo-control.yaml -f deploy/vllm-fleet-loadgen.yaml
kubectl -n aitra-system delete deploy,svc -l tier=vllm-fleet
# full removal:
kubectl delete ns aitra-system     # weights under /root/aitra-models survive
```
