#!/bin/bash
# One-shot demo deploy for the .25 8×A100 cluster.
# Run ON the host (k8s node with docker + kubectl), from the repo root,
# on branch feat/per-model-attribution-89.
#
#   ./scripts/deploy-a100-demo.sh            # build images + core + fleet
#   SKIP_BUILD=1 ./scripts/deploy-a100-demo.sh   # manifests only
#
# Containerd note: if the cluster runs containerd (not dockershim), the
# `docker build` images must also be imported:
#   docker save aitra-meter/measurement-agent:dev | ctr -n k8s.io images import -
set -euo pipefail
cd "$(dirname "$0")/.."
NS=aitra-system

echo "=== [1/5] images ==="
if [ -z "${SKIP_BUILD:-}" ]; then
  docker build -f build/measurement-agent/Dockerfile    -t aitra-meter/measurement-agent:dev .
  docker build -f build/aggregation-service/Dockerfile  -t aitra-meter/aggregation-service:dev .
  if command -v ctr >/dev/null 2>&1; then
    docker save aitra-meter/measurement-agent:dev   | ctr -n k8s.io images import - || true
    docker save aitra-meter/aggregation-service:dev | ctr -n k8s.io images import - || true
  fi
fi

echo "=== [2/5] core stack ==="
kubectl apply -f deploy/aitra-system.yaml
kubectl apply -f deploy/prometheus.yaml
kubectl apply -f deploy/dcgm-exporter.yaml
kubectl apply -f deploy/measurement-agent.yaml
kubectl -n $NS rollout restart deploy/aitra-meter-aggregation
kubectl -n $NS rollout restart daemonset/aitra-meter-agent 2>/dev/null || true

echo "=== [3/5] grafana demo surface (NodePort 30852) ==="
kubectl -n $NS get secret grafana-admin >/dev/null 2>&1 || {
  PW=$(head -c 24 /dev/urandom | base64 | tr -d '/+=' | head -c 16)
  kubectl -n $NS create secret generic grafana-admin --from-literal=password="$PW"
  echo ">>> grafana admin password (save it): $PW"
}
kubectl -n $NS create configmap aitra-demo-dashboards \
  --from-file=deploy/grafana-dashboards/ --dry-run=client -o yaml | kubectl apply -f -
kubectl apply -f deploy/grafana.yaml

echo "=== [4/5] Demo A fleet + loadgen (downloads start now; 72B ≈ 41 GB) ==="
kubectl apply -f deploy/vllm-fleet.yaml
kubectl apply -f deploy/vllm-fleet-loadgen.yaml

echo "=== [5/5] status ==="
kubectl -n $NS get pods -o wide
echo
echo "watch:      kubectl -n $NS get pods -l tier=vllm-fleet -w"
echo "grafana:    http://<node-ip>:30852  (anonymous read-only)"
echo "first data: expect aitra_j_per_token series within ~2 windows (60s) of a model turning Ready"
