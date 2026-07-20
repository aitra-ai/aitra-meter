# KubeCon Japan 2026 — Aitra Meter Demo Runbook

Two rigs, two roles:

| Rig | Hardware | Role | Demos |
|---|---|---|---|
| **Lab rig** (remote) | 8× A100-80G, one node | Main show, pulled remotely via Grafana | A (primary), B (TP curve), C Panel 1 (shapes) |
| **xspark** (local) | GB10 / DGX Spark, 1 GPU | Booth-local, zero network dependency | C Panel 2 (concurrency), model-swap mini-demo, live fallback |

Everything below is executed **rig-side**; the booth only pulls dashboards.
Scope: Meter only — no Policy, no per-phase energy claims.

## 0. One-time setup (do this week, not demo day)

Build & load the agent/aggregation images with the per-model attribution build
(branch `feat/per-model-attribution-89`), then:

```bash
NS=aitra-system
# Core (existing): namespace, RBAC, aggregation, prometheus, dcgm-exporter
kubectl apply -f deploy/aitra-system.yaml -f deploy/prometheus.yaml -f deploy/dcgm-exporter.yaml

# Per-model measurement agent (DaemonSet, --per-model, dcgm per-device)
kubectl apply -f deploy/measurement-agent.yaml

# Grafana demo surface
kubectl -n $NS create secret generic grafana-admin --from-literal=password='<pick>'
kubectl -n $NS create configmap aitra-demo-dashboards \
  --from-file=deploy/grafana-dashboards/ --dry-run=client -o yaml | kubectl apply -f -
kubectl apply -f deploy/grafana.yaml        # NodePort 30852

# Demo A fleet — START DOWNLOADS EARLY (72B-AWQ ≈ 41 GB via hf-mirror)
kubectl apply -f deploy/vllm-fleet.yaml
kubectl apply -f deploy/vllm-fleet-loadgen.yaml
kubectl -n $NS get pods -l tier=vllm-fleet -w
```

Verify the pipeline end-to-end before rehearsing:

```bash
# every model reporting its own series?
curl -s http://<node>:30852/api/health
curl -s 'http://aitra-meter-prometheus.aitra-system:9090/api/v1/query?query=aitra_j_per_token' | jq '.data.result[].metric.model'
```

## 1. Acceptance gates (§9 — run BEFORE calling any demo ready)

These re-state the demo-brief preconditions against the live rig. **Sushanth
owns sign-off.**

- [ ] **Distinct series:** 8 models → 8 distinct `aitra_j_per_token{model=...}`
      series, stable within 60 s of load start. Two identical lines = leakage → halt.
- [ ] **Ground truth:** for at least 2 models (one large, one small), stop the
      rest of the fleet, run the model alone, compare isolated J/token to its
      fleet reading. Must match within tolerance (CV gate < 3%).
- [ ] **TP sums:** `vllm-tp` at TP=8 → serving power ≈ whole-node power;
      residual (`_idle`) ≈ 0. At TP=1 → residual carries ~7 GPUs of idle draw.
- [ ] **Idle-only idle:** with loadgen stopped, model series keep their own
      power (pods still hold GPUs); with fleet scaled to 0, only the `idle`
      series and `aitra_idle_power_watts` remain.

## 2. Demo A — Heterogeneous Fleet (primary loop)

Dashboard: **Demo A: Heterogeneous Fleet** (`aitra-demo-a`, Grafana home).

1. Fleet + loadgen already running (steady state — leave it on all week).
2. Talk track: 8 tiles = 8 models, one GPU each, energy attributed per model
   by the exact GPUs it holds; roll-up reconciles: `sum(per-model power) +
   idle floor ≈ total node power`.
3. **Halt criterion:** any two tiles identical → say "let me show you the
   validation view instead", switch to recorded loop, debug later.

## 3. Demo B — TP scaling curve

Needs free GPUs — run outside fleet steady-state hours or scale the fleet:

```bash
kubectl -n aitra-system scale deploy -l tier=vllm-fleet --replicas=0
kubectl apply -f deploy/vllm-tp.yaml           # starts at TP=2
# step the curve: edit BOTH `--tensor-parallel-size` and `nvidia.com/gpu`
# in deploy/vllm-tp.yaml (1 → 2 → 4 → 8), re-apply, wait Ready between steps
```

Drive load per step (lab-side):

```bash
vllm bench serve --backend openai --base-url http://vllm-tp.aitra-system:8000 \
  --model qwen2.5-72b-awq-tp --num-prompts 400 --request-rate 4
```

Dashboard: **Demo B: Tensor-Parallel Scaling**. The J/token line steps as TP
changes; annotate live. **Halt criterion:** TP=1 J/token inflated toward
node-total (idle GPUs leaking into the numerator) → grouping broken.

## 4. Demo C — Workload contrast

**Panel 2 (concurrency sweep) is the safe-anywhere demo** — works on the
single-tenant fallback too.

```bash
# Panel 1 — shape contrast (3 free GPUs)
kubectl apply -f deploy/vllm-shape.yaml
# then three shaped loads (lab-side), one per endpoint:
vllm bench serve --base-url http://vllm-shape-summarize.aitra-system:8000 \
  --model qwen2.5-7b-summarize --random-input-len 8192 --random-output-len 64  --num-prompts 300
vllm bench serve --base-url http://vllm-shape-generate.aitra-system:8000 \
  --model qwen2.5-7b-generate  --random-input-len 64   --random-output-len 1024 --num-prompts 300
vllm bench serve --base-url http://vllm-shape-chat.aitra-system:8000 \
  --model qwen2.5-7b-chat      --random-input-len 512  --random-output-len 256  --num-prompts 300

# Panel 2 — concurrency sweep on any one model (e.g. fleet's qwen2.5-7b)
for C in 1 2 4 8 16 32 64; do
  vllm bench serve --base-url http://vllm-7b.aitra-system:8000 \
    --model qwen2.5-7b --max-concurrency $C --num-prompts $((C*40))
  sleep 90   # ≥2 full measurement windows per step
done
```

Dashboard: **Demo C: Workload Contrast**. Wording discipline: shape panels show
aggregate J/token differing *because* prefill/decode ratios differ — **never**
present as per-phase energy measurement.

## 5. xspark (GB10) — booth-local demo

The xspark cluster already runs the full stack (aggregation, prometheus,
dcgm-exporter, per-model agent, vllm-demo 0.5B). Add the demo surface and the
swap set:

```bash
# on the xspark cluster
kubectl -n aitra-system create secret generic grafana-admin --from-literal=password='<pick>'
kubectl -n aitra-system create configmap aitra-demo-dashboards \
  --from-file=deploy/grafana-dashboards/ --dry-run=client -o yaml | kubectl apply -f -
kubectl apply -f deploy/grafana.yaml       # NodePort 30852 (dashboard app stays on 30851)
kubectl apply -f deploy/xspark-demo.yaml   # 3B/7B/14B swap set, all replicas=0
# pre-download all three models once (start each briefly, or run the fetch
# initContainers by scaling each to 1 and back to 0 after "model ready")
```

Two booth-local segments, no remote link involved:

1. **Model-swap mini-demo** (the zero-config attribution story): scale
   `vllm-demo` down, `xspark-7b` up → its J/token tile appears by itself;
   swap 7B → 14B → tile changes; scale to 0 → only the idle series remains.
   One GPU, but the same discovery/attribution machinery as the fleet.
2. **Demo C Panel 2 — concurrency sweep** against the active swap model
   (`vllm bench serve` from inside the vllm pod or any lab host):

   ```bash
   for C in 1 2 4 8 16 32; do
     vllm bench serve --base-url http://xspark-7b.aitra-system:8000 \
       --model qwen2.5-7b --max-concurrency $C --num-prompts $((C*40))
     sleep 90
   done
   ```

3. Optional talking point: same `qwen2.5-7b` runs on both rigs — compare its
   J/token on A100 vs GB10 (two Grafana tabs). Different hardware, same
   measurement pipeline.

GB10 notes: 128GB unified memory — every swap size fits; single GPU means one
model at a time; energy path is the same dcgm-exporter feed already validated
on this box.

## 6. Fallback ladder (decide top-down on demo day)

1. **Full remote Demo A/B/C** — per-model attribution validated (§1 all
   green), remote link healthy.
2. **Single-tenant lab node** — per-model path not validated: run one model
   at a time on the full node (classic agent mode, no `--per-model`); Demo C
   Panel 2 works fully; Demo B works (whole-node energy = the TP group at
   every degree, curve still real).
3. **xspark live-local** — remote link down: run the §5 segments on the
   booth-local box. Still a real, live measurement demo — lead with the
   model-swap story and the concurrency sweep.
4. **Recorded loop** — everything else failed: play the recorded run-through
   (record it during the §1 rehearsal — screen-capture one full Demo A + one
   TP sweep at 1080p, loop it).

## 7. Remote-delivery checklist (before July 28)

- [ ] Grafana reachable from a venue-like network (phone hotspot test, not lab
      LAN). Read-only anonymous access confirmed; admin login NOT usable
      anonymously.
- [ ] Booth pulls dashboards only; all load generation stays lab-side.
- [ ] Recorded fallback downloaded onto the booth laptop (not streamed).
- [ ] Ops decision per briefs: `sol.aitra.ai` did not make the July 25 gate →
      presenter triggers runs directly lab-side (VPN/kubectl). Keep a tmux
      session with the §2–§4 commands pre-typed.
- [ ] Second rehearsal end-to-end over the representative network.

## 8. Known-open items (tracked, not demo-blocking after fallback decisions)

- **aigateway** — not in this repo, ownership unconfirmed; demos route load
  directly at per-model Services instead. If it lands, point `vllm bench
  serve` at the gateway URL; nothing else changes.
- **`meter.aitra.ai` routing** to lab Grafana (30852) + removal of the GB10
  dev Grafana from public routing — infra task outside this repo.
- **`sol.aitra.ai`** — post-KubeCon project (constrained lab-side API,
  Casdoor auth, kill switch), per the demo brief's timeline gate.
