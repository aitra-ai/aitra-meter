# Aitra Meter — Booth Demo Suite

The interactive surfaces used at conference booths to demonstrate per-model,
per-node and **per-agent-task** energy metering on a live GPU cluster. Every
number shown is measured (DCGM per-GPU energy counters + RAPL host energy),
not estimated; agent-task bills come from the task-gateway (PR #102).

| Surface | Dir | Runs as | Purpose |
|---|---|---|---|
| Demo hub | `landing/` | static file server (any) | one bilingual page linking every surface |
| Demo console | `console/` | Deployment (`deploy/demo-control.yaml`) | health banner · GPU rack drag-and-drop (drop a model onto a GPU to start it) · traffic combos/sweeps · agent-scenario launcher |
| Visitor agent chat | `booth-agent/` | docker container (host level) | the "try it yourself" entry: chat with a tool-using agent (CCB headless engine, one process + private workdir per session) with the session's energy bill pinned to the page |

## Prerequisites

- A running aitra-meter stack (agent + aggregation + prometheus) and the
  task-gateway (`cmd/task-gateway`, PR #102) with routes for the demo models.
- vLLM models that will serve agent traffic need tool calling enabled:
  `--enable-auto-tool-choice --tool-call-parser qwen3_xml` and a context
  window of at least 32k (agent system prompts alone exceed 4k tokens).
- `booth-agent` additionally needs a build of CCB (claude-code-best) —
  `node dist/cli.js` — mounted at `/ccb`.

## Configuration (no secrets in code)

Everything environment-specific is an env var; nothing in this tree contains
keys or site addresses.

| Var | Component | Meaning |
|---|---|---|
| `ACCESS_KEY` | console | gate key; served 401 without it (store in a Secret) |
| `PROM_URL` | console | meter Prometheus base URL |
| `TASK_GW` | console, booth-agent | task-gateway base URL (in-cluster DNS by default; NodePort when running as a host container) |
| `AGENT_MODELS` | booth-agent | comma list of selectable models (first = default) |
| `CCB_ENTRY` | booth-agent | path to CCB `cli.js` (default `/ccb/cli.js`) |

## Launch sketches

```sh
# console: ConfigMap-mounted app (see deploy/demo-control.yaml)
kubectl -n aitra-system create configmap demo-control-app \
  --from-file=demos/console/app.py --from-file=demos/console/index.html
kubectl apply -f deploy/demo-control.yaml

# visitor agent chat (host docker; CCB dist mounted read-only)
docker build -t aitra-demo/booth-agent demos/booth-agent
docker run -d --restart=always --cpus=2 --memory=1500m -p 32046:80 \
  -v /path/to/ccb/dist:/ccb:ro \
  -e TASK_GW=http://<node-ip>:30854 \
  -e AGENT_MODELS=<model-a>,<model-b> \
  aitra-demo/booth-agent

# landing hub (any static server)
docker run -d --restart=always -p 80:80 -v $PWD/demos/landing:/www:ro \
  node:22-alpine node /www/httpd.js
```

## Notes

- The console needs RBAC for the rack view: `deployments` get +
  `deployments/scale` get/update/patch, restricted by `resourceNames` to the
  fleet deployments (see the Role in `deploy/demo-control.yaml`).
- Sessions in booth-agent are in-memory by design (booth
  ephemerality); bills persist in the task-gateway.
- Attribution semantics for every bill shown: `token-share` — proportional
  attribution over batched serving, same contract as the host-energy split.
