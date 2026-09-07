# PR #91 — Branch Restore & Merge Notes

*2026-07-25 · for the merging reviewer*

## Why this push

On July 25 the branch `demo/kubecon-per-model-attribution-89` on GitHub was
reset to its July-21 commit (`bc461a4`) by an accidental force-push (most
likely from a stale clone), discarding ~80 commits. This push restores the
complete branch as a **plain fast-forward** — no history is rewritten and
nothing from the interim work on the reset branch is lost (its only code
change, a gofmt lint fix on `internal/aggregation/cv.go`, is included here
as an equivalent commit).

## The "merge conflicts" were not real

The conflicts observed on July 24–25 were an artifact of the reset:

- Merging the **old July-21 state** into `main` conflicts in 11 files
  (`internal/aggregation/cv.go`, `loop.go`, `loop_test.go`,
  `internal/provider/energy/dcgm/dcgm.go`, both `build/*/Dockerfile`,
  `go.mod`, `go.sum`, `docs/adr/0007-per-gpu-energy-attribution.md`,
  `cmd/measurement-agent/main.go`, `generic_prometheus_test.go`).
- Every one of those was **already resolved** in this branch's merge of
  `origin/main` (merge commit `1b9d688`, July 23), with build and tests
  green afterwards.
- Verified with `git merge-tree origin/main HEAD`: this branch merges into
  current `main` with **zero conflicts**. PR #91 can be merged as-is —
  no conflict resolution is needed on anyone's side.

If you have a local checkout from the reset period, sync it with:

```bash
git fetch origin
git reset --hard origin/demo/kubecon-per-model-attribution-89
```

## What the restored commits add on top of the original attribution PR

| Area | Content |
|---|---|
| Model fleet | 8× Qwen3.5/3.6 (`deploy/vllm-fleet.yaml`): 0.8B/2B/4B/9B, 27B, 27B-FP8, 35B-A3B-FP8 (MoE), 122B-A10B-Int4; memory-fit fixes for 122B-Int4 (enforce-eager, text-only, 4k ctx); `vllm-tp/shape/xspark` manifests synced |
| Demo console | `deploy/demo-control.yaml`: model multi-select × 4 task shapes (incl. ShareGPT real conversations) × 6 concurrency steps × durations (incl. continuous), sweep/burst scenes, idle-baseline toggle, bilingual zh/EN UI, access-key gate, whitelisted Jobs + RBAC + kill switch |
| Display semantics | Quiet models read **0** instead of a frozen stale value (`internal/aggregation/loop.go`, min-30-token gate); CV panel dropped; throughput in token/s; sub-10s end-to-end display latency (5s window, dcgm `-c 2500`, 2s scrape, 2s refresh) |
| Public access | `meter.aitra.ai` one-domain-three-faces: Grafana `root_url`, dashboard under `/board` (Next.js basePath, client fetches prefixed, promguard allows `gpu_id`), console mount-agnostic base path |
| Docs | `docs/demo/DEPLOY_AND_TEST.md` (full deploy + test guide), `demo-guide.html` / `demo-guide-en.html` (bilingual field guide with conference FAQ), `kubecon-runbook.md` updates |
| Housekeeping | Merge of `origin/main` with all conflicts resolved (`1b9d688`); gofmt on `cv.go` |

## Ask

Please avoid force-pushing to this branch. If anything looks wrong with it,
ping @xiaoxixixingxing first — the demo (KubeCon Japan, July 28–30) is
running live off this branch's manifests.
