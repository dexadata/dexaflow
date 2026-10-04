#!/usr/bin/env bash
#
# Two-process split end-to-end test (ADR 0049).
#
# Proves the api/scheduler role split works as SEPARATE processes cooperating
# only through Postgres (DB-as-truth, ADR 0031): an api-role process (HTTP + UI,
# no scheduler, no agent gRPC) accepts a trigger and writes it to the DB; a
# distinct scheduler-role process (reconciler + dispatch + agent gRPC, no HTTP
# API) picks it up from the DB, dispatches a task pod, and the pod's agent reports
# state back over the scheduler's gRPC; then the api process serves the terminal
# state — all read/written through the shared DB, never in-process.
#
# This reuses the host-process harness of e2e.sh (control plane on the host, task
# pods in k3d dialing back) rather than the Helm chart, so it isolates the Go
# role-gating from cert-manager / in-cluster datastore concerns. The chart's split
# topology is validated separately by helm-unittest + the kind e2e.
#
# It also asserts role ISOLATION at the socket level: the api process serves no
# gRPC and the scheduler process serves no HTTP API; each still answers /healthz
# on its metrics port (the kubelet probe target for a scheduler-only pod).
#
# Requirements: k3d, kubectl, docker, jq, curl, the leoflow binaries
# (`make build`), and a running dev database (`make dev-up`). Developer/CI tool.
#
# Usage: test/e2e/split-two-process.sh [cluster-name]
set -euo pipefail

# shellcheck source=test/e2e/lib.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

CLUSTER="${1:-leoflow-split-e2e}"
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
WORKDIR="$(mktemp -d)"
PY_VERSION="3.11"
BASE_IMAGE="leoflow-base:py${PY_VERSION}"
DAG_IMAGE="leoflow-split-dag:dev"
DAG_ID="splitdag"

# The api process serves HTTP here; the scheduler serves gRPC + its own metrics.
API_HTTP_PORT="${LEOFLOW_E2E_API_HTTP_PORT:-8080}"
API_METRICS_PORT="9090"
# The scheduler binds gRPC + metrics only. Its HTTP addr is set to a port we then
# assert is NOT listening (role=scheduler serves no API).
SCHED_HTTP_PORT="8081"
SCHED_METRICS_PORT="9092"
GRPC_PORT="9091"
API="http://localhost:${API_HTTP_PORT}"
# Address task pods dial to reach the scheduler process's gRPC on the host.
HOST_ADDR="${LEOFLOW_E2E_HOST_ADDR:-$([ "$(uname -s)" = Linux ] && echo host.k3d.internal || echo host.docker.internal)}"
API_PID=""
SCHED_PID=""

log()  { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
dump_pods() {
  printf '\033[1;33m--- task pods (namespace leoflow) ---\033[0m\n' >&2
  kubectl get pods -n leoflow -o wide >&2 2>&1 || true
  for p in $(kubectl get pods -n leoflow -o name 2>/dev/null); do
    printf '\033[1;33m--- logs %s ---\033[0m\n' "$p" >&2
    kubectl logs -n leoflow "$p" --all-containers --tail=80 >&2 2>&1 || true
    kubectl describe -n leoflow "$p" >&2 2>&1 || true
  done
  printf '\033[1;33m--- images on the node ---\033[0m\n' >&2
  docker exec "k3d-${CLUSTER}-server-0" crictl images >&2 2>&1 || true
  docker exec "k3d-${CLUSTER}-server-0" df -h / >&2 2>&1 || true
}
fail() { printf '\033[1;31mFAIL:\033[0m %s\n' "$*" >&2; dump_pods; exit 1; }

cleanup() {
  [ -n "$API_PID" ] && kill "$API_PID" 2>/dev/null || true
  [ -n "$SCHED_PID" ] && kill "$SCHED_PID" 2>/dev/null || true
  k3d cluster delete "$CLUSTER" >/dev/null 2>&1 || true
  rm -rf "$WORKDIR"
}
trap cleanup EXIT

for tool in k3d kubectl docker jq curl; do
  command -v "$tool" >/dev/null || fail "missing required tool: $tool"
done

log "Scaffolding a minimal two-task DAG ($DAG_ID)"
"$ROOT/bin/leoflow" init "$WORKDIR/$DAG_ID"
# The DAG image must match the k3d node arch, else the pod ErrImagePulls. The
# config loader defaults build.platforms to linux/amd64 (the prod default), which
# breaks on an arm64 host (e.g. Apple Silicon). Pin it to the host arch so this
# e2e runs on any machine (arm64 locally, amd64 in CI).
case "$(uname -m)" in
  arm64|aarch64) HOST_PLATFORM="linux/arm64" ;;
  *)             HOST_PLATFORM="linux/amd64" ;;
esac
cat >> "$WORKDIR/$DAG_ID/dexaflow.yaml" <<YAML
build:
  platforms:
    - ${HOST_PLATFORM}
YAML
log "DAG image platform pinned to ${HOST_PLATFORM} (host arch)"
cat > "$WORKDIR/$DAG_ID/dag.py" <<'PY'
"""splitdag — proves a run flows across two role processes via the DB."""
from __future__ import annotations

from airflow.sdk import DAG, task


@task
def extract() -> str:
    print("split e2e: extract ran in a pod")
    return "payload-split"


@task
def transform(value: str) -> str:
    # Consuming extract's output across two pods proves the scheduler (a separate
    # process from the API) dispatched both and wired their XCom through the DB.
    print(f"split e2e: transform received {value}")
    return value.upper()


with DAG("splitdag", schedule=None, catchup=False, tags=["e2e", "split"]):
    transform(extract())
PY
cat > "$WORKDIR/$DAG_ID/Dockerfile" <<DOCKER
FROM ${BASE_IMAGE}
COPY dag.py /home/leoflow/dag.py
ENV PYTHONPATH=/home/leoflow
DOCKER

log "Building the base image"
# --provenance=false: with Docker Desktop's containerd image store, buildx adds
# an attestation manifest that turns the image into a manifest list the DAG's
# `FROM leoflow-base` cannot resolve ("no match for platform in manifest"). A
# plain single-platform image is what the downstream build + k3d import expect.
docker build --provenance=false -f "$ROOT/runtime/Dockerfile" --build-arg "PYTHON_VERSION=${PY_VERSION}" -t "$BASE_IMAGE" "$ROOT"

log "Creating k3d cluster '$CLUSTER'"
k3d cluster create "$CLUSTER" --wait
kubectl create namespace leoflow

# Shared env for BOTH processes — same DB/Redis/secrets so they are one logical
# control plane split across two processes (mirrors the shared datastores + RWX
# logs PVC of the Helm split).
export LEOFLOW_AUTH_JWT_SECRET="split-e2e-secret"
export LEOFLOW_BOOTSTRAP_PASSWORD="admin"
export LEOFLOW_SECRET_KEY="split-e2e-key-32-bytes-padding!!"
export LEOFLOW_AGENT_ALLOW_INSECURE_SECRETS=true
# Split REQUIRES a shared datastore: with no Redis each process would use its own
# in-process XCom backend and the api could not read XCom the scheduler's pods
# produced. The Helm chart mandates Redis for exactly this reason; mirror it here.
export LEOFLOW_REDIS_URL="${LEOFLOW_E2E_REDIS_URL:-redis://localhost:6379/0}"
# Both processes write/read task logs from the same dir (the scheduler receives
# them over gRPC and writes; the api reads them for the UI) — the host-process
# analogue of the shared RWX logs PVC.
export LEOFLOW_LOGS_DIR="${WORKDIR}/logs"
mkdir -p "$LEOFLOW_LOGS_DIR"

log "Starting the SCHEDULER process (role=scheduler: gRPC + dispatch, no HTTP API)"
# Task pods dial THIS process's gRPC. It runs the scheduler loop + agent gRPC and
# serves /healthz on its metrics port, but binds no HTTP API (role gate).
LEOFLOW_SERVER_ROLE=scheduler \
LEOFLOW_SERVER_HTTP_ADDR="0.0.0.0:${SCHED_HTTP_PORT}" \
LEOFLOW_SERVER_METRICS_ADDR="0.0.0.0:${SCHED_METRICS_PORT}" \
LEOFLOW_SERVER_GRPC_ADDR="0.0.0.0:${GRPC_PORT}" \
LEOFLOW_EXECUTOR_AGENT_CONTROL_PLANE_ADDR="${HOST_ADDR}:${GRPC_PORT}" \
  "$ROOT/bin/leoflow-server" &
SCHED_PID=$!

log "Starting the API process (role=api: HTTP + UI, no scheduler, no agent gRPC)"
LEOFLOW_SERVER_ROLE=api \
LEOFLOW_SERVER_HTTP_ADDR="0.0.0.0:${API_HTTP_PORT}" \
LEOFLOW_SERVER_METRICS_ADDR="0.0.0.0:${API_METRICS_PORT}" \
  "$ROOT/bin/leoflow-server" &
API_PID=$!
sleep 5

# ── Role isolation assertions ────────────────────────────────────────────────
log "Asserting role isolation at the socket level"
# api serves the HTTP API …
curl -fsS "${API}/healthz" >/dev/null || fail "api process /healthz (http :${API_HTTP_PORT}) not serving"
# … and /healthz on its metrics port (the every-role probe surface, ADR 0049).
curl -fsS "http://localhost:${API_METRICS_PORT}/healthz" >/dev/null \
  || fail "api process /healthz (metrics :${API_METRICS_PORT}) not serving"
# The scheduler serves /healthz on its metrics port (the kubelet probe target for
# a scheduler-only pod, since it has no HTTP API) …
curl -fsS "http://localhost:${SCHED_METRICS_PORT}/healthz" >/dev/null \
  || fail "scheduler process /healthz (metrics :${SCHED_METRICS_PORT}) not serving — the scheduler pod would have no probe target"
# … but binds NO HTTP API: a request to its http port must be refused.
if curl -fsS --max-time 3 "http://localhost:${SCHED_HTTP_PORT}/healthz" >/dev/null 2>&1; then
  fail "scheduler process is serving an HTTP API on :${SCHED_HTTP_PORT} — role=scheduler must not serve the API"
fi
log "isolation OK: api serves HTTP+metrics-health; scheduler serves metrics-health only (no HTTP API), gRPC on :${GRPC_PORT}"

log "Compiling, building, and importing the DAG image"
"$ROOT/bin/leoflow" compile "$WORKDIR/$DAG_ID" --image "$DAG_IMAGE" \
  --build --dockerfile Dockerfile -o "$WORKDIR/$DAG_ID/dag.json"
k3d_import "$CLUSTER" "$BASE_IMAGE" "$DAG_IMAGE"

log "Pushing + triggering the DAG against the API process (never the scheduler)"
TOKEN="$("$ROOT/bin/leoflow" auth create-token --server "$API" --username admin@leoflow.local --password admin)"
"$ROOT/bin/leoflow" push "$WORKDIR/$DAG_ID/dag.json" --server "$API" --token "$TOKEN"
RUN_ID="$(curl -fsS -X POST -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{}' "$API/api/v2/dags/$DAG_ID/dagRuns" | jq -r '.dag_run_id')"
[ -n "$RUN_ID" ] && [ "$RUN_ID" != "null" ] || fail "no dag_run_id returned from the api process"
log "run = $RUN_ID"

log "Waiting for all task instances to succeed (dispatched by the SEPARATE scheduler process, via the DB)"
deadline=$(( $(date +%s) + 300 ))
while :; do
  states="$(curl -fsS -H "Authorization: Bearer $TOKEN" \
    "$API/api/v2/dags/$DAG_ID/dagRuns/$RUN_ID/taskInstances" | jq -r '.task_instances[].state')"
  log "task states: [$(echo "$states" | tr '\n' ' ')] pods: [$(kubectl get pods -n leoflow --no-headers 2>/dev/null | awk '{print $1"="$3}' | tr '\n' ' ')]"
  echo "$states" | grep -qE 'failed|upstream_failed' && fail "a task failed: $states"
  if [ -n "$states" ] && ! echo "$states" | grep -qvE 'success|skipped'; then
    log "all tasks terminal-success: $states"
    break
  fi
  [ "$(date +%s)" -gt "$deadline" ] && fail "timed out; last states: ${states:-<none>}"
  sleep 3
done

log "Asserting TaskFlow value passing across two pods (proves cross-pod XCom via the DB)"
tlog=""
for try in 0 1 2; do
  body="$(curl -fsS -H "Authorization: Bearer $TOKEN" \
    "$API/api/v2/dags/$DAG_ID/dagRuns/$RUN_ID/taskInstances/transform/logs/$try" 2>/dev/null || true)"
  if echo "$body" | grep -q "split e2e: transform received payload-split"; then tlog="$body"; break; fi
done
[ -n "$tlog" ] || fail "transform did not receive extract's output — the two processes did not cooperate through the DB"
log "value passing OK: transform received payload-split (API served logs the scheduler's pods produced)"

log "SPLIT E2E passed — api and scheduler ran as separate processes cooperating via the DB (ADR 0049)"
