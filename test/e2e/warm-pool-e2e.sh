#!/usr/bin/env bash
#
# Warm pools (ADR 0058) on a real k3d cluster (#1611).
#
# Warm pools ship with unit tests of the reconciler, the dispatcher and the warm
# agent, and with a single e2e check in source-mode-e2e.sh that a warm pod is
# created. No CI leg ran a task on a warm worker, so the parts only a cluster
# shows (the token exchange, the warm agent registering and serving attempts in
# its own pod, the sweeps between attempts on a read-only root filesystem, the
# anchors going away with the pods) were checked by hand or not at all. This
# script runs them with the control plane on the host, as e2e.sh does, and the
# settings warm pools require: token exchange transport, secret liveness
# enforcement, one idle worker and a pool of one per version, and the read-only
# root filesystem with per-attempt HOME and the /tmp and /dev/shm sweeps.
#
# It asserts:
#
#   1. Admission. The namespace enforces Pod Security `restricted`, so the warm
#      pod exactly as BuildWarmPod makes it, and the dedicated task pods, are
#      admitted under that profile or the run never finishes.
#   2. Warm hit. At least one of the two probe tasks ran on a warm worker: its
#      task instance carries warm_worker_id, the name of a pod labelled
#      leoflow.io/warm-worker=true.
#   3. Placement (#1543). A task that declares its own pod labels runs on a
#      dedicated pod carrying them, never on a warm worker.
#   4. Isolation. Each probe first looks for marker files an earlier attempt
#      left anywhere outside /proc and /sys, then writes its own to $HOME,
#      $TMPDIR, /tmp, /dev/shm, its working directory and /var/tmp. When both
#      probes ran on the same warm worker, the second must find nothing. When
#      they did not, the check is reported as not judged (a warning, not a
#      pass). The task image is built from the base in this checkout, so the
#      agent doing the sweeps is the one in this commit.
#   5. No leaks. Once the run settles: no task instance outside a terminal
#      state, and the reconciler drains every warm pod and every
#      leoflow.io/warm-anchor ConfigMap of the version. The DAG declares no
#      min_idle_workers, so with no active run its current version is not
#      prewarmed (ADR 0082) and the operator floor alone keeps no pool.
#
# Not covered here: a leader restart resuming warm placements (#1602) and a
# warm worker killed mid-attempt (#1600, #1603), whose fixes have unit tests but
# no cluster run yet. Draining the superseded version on a deploy is the warm
# pools leg of deploy-e2e.sh (#1707).
#
# Requirements: k3d, kubectl, docker, jq, curl, psql, python3, the binaries
# (`make build`), and a migrated database (`make dev-up`). Developer/CI tool,
# not part of `go test`.
#
# Environment:
#   LEOFLOW_E2E_HTTP_PORT      API port (default 18083)
#   LEOFLOW_E2E_HOST_ADDR      address task pods dial for the control plane
#   DEXAFLOW_E2E_DATABASE_URL  the control plane's database, read for
#                              warm_worker_id (default: the server default);
#                              LEOFLOW_E2E_DATABASE_URL is the older name
#   LEOFLOW_E2E_K3D_ARGS       extra `k3d cluster create` arguments, split on
#                              spaces
#
# Usage: test/e2e/warm-pool-e2e.sh [cluster-name]
set -euo pipefail

# shellcheck source=test/e2e/lib.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

CLUSTER="${1:-dexaflow-warm-pool-e2e}"
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
WORKDIR="$(mktemp -d)"
PY_VERSION="3.11"
BASE_IMAGE="leoflow-base:py${PY_VERSION}"
NS="leoflow"
DAG_ID="warmprobe"
HTTP_PORT="${LEOFLOW_E2E_HTTP_PORT:-18083}"
API="http://localhost:${HTTP_PORT}"
GRPC_PORT="9091"
HOST_ADDR="${LEOFLOW_E2E_HOST_ADDR:-$([ "$(uname -s)" = Linux ] && echo host.k3d.internal || echo host.docker.internal)}"
DB_URL="${DEXAFLOW_E2E_DATABASE_URL:-${LEOFLOW_E2E_DATABASE_URL:-postgres://leoflow:leoflow@localhost:5432/leoflow?sslmode=disable}}"
STAMP="$(date +%s)"
SERVER_PID=""
TOKEN=""
SERVER_LOG="${WORKDIR}/server.log"

# compile spawns `python3 -m leoflow_parser`; the parser ships under parser/.
export PYTHONPATH="${ROOT}/parser:${PYTHONPATH:-}"
# An isolated kubeconfig: the throwaway cluster never becomes the machine's
# current context, and the server and kubectl below both read this one.
export KUBECONFIG="${WORKDIR}/kubeconfig"

case "$(uname -m)" in arm64|aarch64) HOST_PLATFORM="linux/arm64" ;; *) HOST_PLATFORM="linux/amd64" ;; esac

log() { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
dump() {
  printf '\033[1;33m--- pods and warm anchors (namespace %s) ---\033[0m\n' "$NS" >&2
  kubectl get pods -n "$NS" -o wide --show-labels >&2 2>&1 || true
  kubectl get configmaps -n "$NS" -l leoflow.io/warm-anchor=true >&2 2>&1 || true
  for p in $(kubectl get pods -n "$NS" -o name 2>/dev/null); do
    printf '\033[1;33m--- describe/logs %s ---\033[0m\n' "$p" >&2
    kubectl describe -n "$NS" "$p" 2>&1 | tail -15 >&2 || true
    kubectl logs -n "$NS" "$p" --all-containers --tail=60 >&2 2>&1 || true
  done
  if [ -f "$SERVER_LOG" ]; then
    printf '\033[1;33m--- control plane log ---\033[0m\n' >&2
    grep -i 'violates PodSecurity' "$SERVER_LOG" >&2 || true
    tail -80 "$SERVER_LOG" >&2 || true
  fi
}
fail() { printf '\033[1;31mFAIL:\033[0m %s\n' "$*" >&2; dump; exit 1; }

stop_server() {
  [ -n "$SERVER_PID" ] || return 0
  kill "$SERVER_PID" 2>/dev/null || true
  for _ in $(seq 1 30); do kill -0 "$SERVER_PID" 2>/dev/null || break; sleep 1; done
  kill -9 "$SERVER_PID" 2>/dev/null || true
  wait "$SERVER_PID" 2>/dev/null || true
  SERVER_PID=""
}

cleanup() {
  stop_server
  k3d cluster delete "$CLUSTER" >/dev/null 2>&1 || true
  rm -rf "$WORKDIR"
}
trap cleanup EXIT

for tool in k3d kubectl docker jq curl psql python3; do
  command -v "$tool" >/dev/null || fail "missing required tool: $tool"
done

sql() { psql "$DB_URL" -v ON_ERROR_STOP=1 -AtX -c "$1"; }

# ─────────────────────────────────────────────────────────────────────────────
log "Building the task base image ($BASE_IMAGE) from this checkout"
docker build --provenance=false -f "$ROOT/runtime/Dockerfile" --build-arg "PYTHON_VERSION=${PY_VERSION}" -t "$BASE_IMAGE" "$ROOT"

log "Creating k3d cluster '$CLUSTER'"
# shellcheck disable=SC2086 # LEOFLOW_E2E_K3D_ARGS is a space-separated list on purpose
k3d cluster create "$CLUSTER" --wait \
  --kubeconfig-update-default=false --kubeconfig-switch-context=false ${LEOFLOW_E2E_K3D_ARGS:-}
k3d kubeconfig get "$CLUSTER" > "$KUBECONFIG"
kubectl create namespace "$NS"
# Assertion 1: every pod of this script, warm or dedicated, is admitted under
# the restricted profile, or it is never created and the run never finishes.
kubectl label namespace "$NS" pod-security.kubernetes.io/enforce=restricted \
  pod-security.kubernetes.io/enforce-version=latest

# ─────────────────────────────────────────────────────────────────────────────
log "Compiling the probe DAG through the generated Dockerfile"
mkdir -p "$WORKDIR/$DAG_ID"
cat > "$WORKDIR/$DAG_ID/dexaflow.yaml" <<YAML
schema_version: "1.0"
dag_id: ${DAG_ID}
base_image: ${BASE_IMAGE}
build:
  platforms:
    - ${HOST_PLATFORM}
tasks:
  placed:
    execution:
      labels:
        e2e-placement: dedicated
YAML
cat > "$WORKDIR/$DAG_ID/dag.py" <<'PY'
"""warmprobe: warm pool hit, placement and isolation probes (#1611)."""
from __future__ import annotations

import os
import time

from airflow.sdk import DAG, task

MARKER = "dexaflow-e2e-carry-marker"


def scan_then_write(name: str) -> None:
    # The marker names are split so that a log echoing this source never
    # matches the lines the e2e script greps for.
    found = []
    for root, dirs, files in os.walk("/", onerror=lambda _err: None):
        if root == "/":
            dirs[:] = [d for d in dirs if d not in ("proc", "sys")]
        for f in files:
            if f.startswith(MARKER):
                found.append(os.path.join(root, f))
    for path in found:
        print("E2E_CARRY" "OVER " + path, flush=True)
    print("E2E_SCAN_DONE found=%d" % len(found), flush=True)
    dirs = {
        "home": os.environ.get("HOME", ""),
        "tmpdir": os.environ.get("TMPDIR", ""),
        "tmp": "/tmp",
        "shm": "/dev/shm",
        "cwd": os.getcwd(),
        "vartmp": "/var/tmp",
    }
    for label, d in dirs.items():
        if not d:
            continue
        try:
            with open(os.path.join(d, MARKER + "-" + name), "w") as fh:
                fh.write(name)
            print("E2E_WROTE %s %s" % (label, d), flush=True)
        except OSError as err:
            print("E2E_NOT_WRITABLE %s %s %s" % (label, d, err), flush=True)


@task
def hold() -> None:
    # Keeps the version active long enough for the reconciler to create its
    # warm worker before the probes are dispatched.
    time.sleep(60)


@task
def probe_a() -> None:
    scan_then_write("probe_a")


@task
def probe_b() -> None:
    scan_then_write("probe_b")


@task
def placed() -> None:
    print("E2E_PLACED", flush=True)


with DAG("warmprobe", schedule=None, tags=["e2e"]):
    h = hold()
    h >> probe_a() >> probe_b()
    h >> placed()
PY
DAG_IMAGE="dexaflow-e2e-warmprobe:${STAMP}"
"$ROOT/bin/leoflow" compile "$WORKDIR/$DAG_ID" --image "$DAG_IMAGE" --build \
  --dag-version "e2e-$STAMP" -o "$WORKDIR/$DAG_ID/dag.json" >/dev/null
jq -e '.tasks[] | select(.task_id=="placed") | .execution.labels["e2e-placement"] == "dedicated"' \
  "$WORKDIR/$DAG_ID/dag.json" >/dev/null || fail "placed: the compiled task does not carry its execution.labels"
k3d_import "$CLUSTER" "$DAG_IMAGE"

# k3d adds host.k3d.internal to CoreDNS NodeHosts a little after the cluster
# reports ready, and a task or warm pod that starts before that fails its
# token exchange ("produced zero addresses"). Wait until a pod resolves it, as
# deploy-e2e.sh does. The probe runs the DAG image, which also warms the node's
# image cache, in the default namespace to stay out of the task pod listings.
wait_host_dns() {
  local probe=e2e-host-dns-probe phase deadline
  kubectl run "$probe" -n default --image="$DAG_IMAGE" --restart=Never --command -- \
    python -c 'import socket, sys, time
for _ in range(90):
    try:
        print(socket.gethostbyname(sys.argv[1])); sys.exit(0)
    except OSError:
        time.sleep(2)
sys.exit(1)' "$HOST_ADDR" >/dev/null
  deadline=$(( $(date +%s) + 240 ))
  while :; do
    phase="$(kubectl get pod "$probe" -n default -o jsonpath='{.status.phase}' 2>/dev/null || true)"
    case "$phase" in
      Succeeded) break ;;
      Failed)
        kubectl logs -n default "$probe" >&2 2>&1 || true
        fail "pods never resolved $HOST_ADDR (CoreDNS NodeHosts)" ;;
    esac
    [ "$(date +%s)" -ge "$deadline" ] && fail "the $HOST_ADDR DNS probe did not finish (phase: ${phase:-none})"
    sleep 2
  done
  log "pods resolve $HOST_ADDR as $(kubectl logs -n default "$probe" 2>/dev/null)"
  kubectl delete pod "$probe" -n default --wait=false >/dev/null 2>&1 || true
}
wait_host_dns

# ─────────────────────────────────────────────────────────────────────────────
log "Starting the control plane with warm pools on"
env \
  LEOFLOW_AUTH_JWT_SECRET="e2e-secret" \
  LEOFLOW_BOOTSTRAP_PASSWORD="admin" \
  LEOFLOW_EXECUTOR_AGENT_CONTROL_PLANE_ADDR="${HOST_ADDR}:${GRPC_PORT}" \
  LEOFLOW_LOGS_DIR="${WORKDIR}/logs" \
  LEOFLOW_AGENT_ALLOW_INSECURE_SECRETS=true \
  LEOFLOW_SECRET_KEY="e2e-secret-key-32-bytes-padding!" \
  LEOFLOW_SERVER_HTTP_ADDR="0.0.0.0:${HTTP_PORT}" \
  DEXAFLOW_EXECUTOR_COLLECT_SETTLED_RUN_PODS=false \
  DEXAFLOW_EXECUTION_WARM_POOLS_ENABLED=true \
  DEXAFLOW_AUTH_AGENT_TOKEN_TRANSPORT=exchange \
  DEXAFLOW_AUTH_SECRET_LIVENESS_MODE=enforce \
  DEXAFLOW_EXECUTION_MIN_IDLE_WORKERS=1 \
  DEXAFLOW_EXECUTION_MAX_POOL_SIZE=1 \
  DEXAFLOW_EXECUTION_WARM_READ_ONLY_ROOT_FILESYSTEM=true \
  "$ROOT/bin/leoflow-server" >"$SERVER_LOG" 2>&1 &
SERVER_PID=$!
for _ in $(seq 1 90); do
  curl -fsS "$API/readyz" >/dev/null 2>&1 && break
  kill -0 "$SERVER_PID" 2>/dev/null || fail "the control plane exited during boot"
  sleep 1
done
curl -fsS "$API/readyz" >/dev/null 2>&1 || fail "the control plane did not become ready"
TOKEN="$(admin_token "$ROOT/bin/leoflow" "$API" admin)" || fail "no bootstrap admin logs in"

"$ROOT/bin/leoflow" push "$WORKDIR/$DAG_ID/dag.json" --server "$API" --token "$TOKEN"
# A DAG registered for the first time starts paused, and a run of a paused DAG
# stays queued until it is unpaused (#1764).
unpause_dag "$API" "$DAG_ID" "$TOKEN" || fail "unpausing $DAG_ID failed"
RUN="$(curl -fsS -X POST -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{}' "$API/api/v2/dags/$DAG_ID/dagRuns" | jq -r '.dag_run_id')"
[ -n "$RUN" ] && [ "$RUN" != "null" ] || fail "no dag_run_id returned"
log "$DAG_ID run = $RUN"

saw_warm_pod=0
sample_warm() {
  local n
  n="$(kubectl get pods -n "$NS" -l leoflow.io/warm-worker=true -o name 2>/dev/null | wc -l | tr -d ' ')"
  [ "$n" -gt 0 ] && saw_warm_pod=1
  return 0
}

deadline=$(( $(date +%s) + 600 ))
while :; do
  sample_warm
  states="$(curl -fsS -H "Authorization: Bearer $TOKEN" \
    "$API/api/v2/dags/$DAG_ID/dagRuns/$RUN/taskInstances" | jq -r '.task_instances[] | "\(.task_id)=\(.state)"')"
  if echo "$states" | grep -qE '=(failed|upstream_failed)$'; then
    fail "a task failed: $(echo "$states" | tr '\n' ' ')"
  fi
  if [ "$(echo "$states" | grep -c .)" -eq 4 ] && ! echo "$states" | grep -qvE '=success$'; then
    break
  fi
  [ "$(date +%s)" -gt "$deadline" ] && fail "the run timed out; last states: $(echo "${states:-<none>}" | tr '\n' ' ')"
  sleep 3
done
[ "$saw_warm_pod" = "1" ] || fail "no warm worker pod was ever created for $DAG_ID"
log "the run succeeded, and the restricted namespace admitted every pod (assertion 1)"

# task_log TASK_ID MARKER prints the first try's log holding MARKER.
task_log() {
  local body try
  for try in 1 0 2; do
    body="$(curl -fsS -H "Authorization: Bearer $TOKEN" \
      "$API/api/v2/dags/$DAG_ID/dagRuns/$RUN/taskInstances/$1/logs/$try" 2>/dev/null || true)"
    if echo "$body" | grep -q "$2"; then printf '%s' "$body"; return 0; fi
  done
  fail "$1: no task log carries '$2'"
}

# warm_worker TASK_ID prints the warm_worker_id of the task's latest try, empty
# for an attempt that ran on a dedicated pod.
warm_worker() {
  sql "SELECT coalesce(ti.warm_worker_id, '')
         FROM task_instances ti
         JOIN dag_runs r ON r.id = ti.dag_run_id
         JOIN dags d ON d.id = r.dag_id
        WHERE d.dag_id = '$DAG_ID' AND r.run_id = '$RUN' AND ti.task_id = '$1'
        ORDER BY ti.try_number DESC LIMIT 1"
}

# ─────────────────────────────────────────────────────────────────────────────
# Assertion 2: warm hit.
WA="$(warm_worker probe_a)"
WB="$(warm_worker probe_b)"
log "probe_a warm_worker_id='${WA}', probe_b warm_worker_id='${WB}'"
[ -n "$WA" ] || [ -n "$WB" ] || fail "neither probe ran on a warm worker: warm placement never hit"
for w in $WA $WB; do
  case "$w" in
    leoflow-warm-*) ;;
    *) fail "warm_worker_id '$w' does not name a warm worker pod" ;;
  esac
done
log "warm hit: at least one probe was served by a warm worker (assertion 2)"

# Assertion 3: placement.
WP="$(warm_worker placed)"
[ -z "$WP" ] || fail "placed declares its own pod labels but ran on warm worker '$WP' (#1543)"
pods="$(kubectl get pods -n "$NS" -l "e2e-placement=dedicated" -o json)"
echo "$pods" | jq -e '.items | length >= 1' >/dev/null \
  || fail "placed: no pod carries its label e2e-placement=dedicated"
echo "$pods" | jq -e '.items | all(.metadata.labels["leoflow.io/warm-worker"] == null)' >/dev/null \
  || fail "placed: its pod is a warm worker"
task_log placed "E2E_PLACED" >/dev/null
log "placement: the task with its own labels ran on a dedicated pod carrying them (assertion 3)"

# Assertion 4: isolation.
LA="$(task_log probe_a "E2E_SCAN_DONE")"
LB="$(task_log probe_b "E2E_SCAN_DONE")"
for d in tmp shm; do
  echo "$LA" | grep -q "E2E_WROTE $d " \
    || fail "probe_a could not write its marker to $d, so the carryover check proves nothing: $(echo "$LA" | grep E2E_ || true)"
done
echo "$LA" | grep -q "E2E_SCAN_DONE found=0" || fail "probe_a found markers no attempt of this run wrote: $(echo "$LA" | grep E2E_CARRYOVER || true)"
if [ -n "$WA" ] && [ "$WA" = "$WB" ]; then
  if ! echo "$LB" | grep -q "E2E_SCAN_DONE found=0"; then
    fail "probe_b found what probe_a left on warm worker $WA: $(echo "$LB" | grep E2E_CARRYOVER || true)"
  fi
  log "isolation: probe_b found nothing probe_a wrote on the same warm worker $WA (assertion 4)"
else
  echo "::warning::isolation not judged: probe_a ran on '${WA:-dedicated}' and probe_b on '${WB:-dedicated}', not one warm worker"
  log "isolation: NOT JUDGED, the probes did not share a warm worker"
fi

# Assertion 5: no leaks once the run settled. With no active run and no
# min_idle_workers in its spec, the version is no warm target (ADR 0082).
open_tis="$(sql "SELECT count(*) FROM task_instances WHERE state NOT IN ('success', 'failed', 'skipped', 'upstream_failed')")"
[ "$open_tis" = "0" ] || fail "$open_tis task instance(s) outside a terminal state after the run settled"
deadline=$(( $(date +%s) + 240 ))
while :; do
  warm="$(kubectl get pods -n "$NS" -l leoflow.io/warm-worker=true -o name | wc -l | tr -d ' ')"
  anchors="$(kubectl get configmaps -n "$NS" -l leoflow.io/warm-anchor=true -o name | wc -l | tr -d ' ')"
  [ "$warm" = "0" ] && [ "$anchors" = "0" ] && break
  [ "$(date +%s)" -gt "$deadline" ] \
    && fail "the version has no active run, yet still holds $warm warm pod(s) and $anchors anchor ConfigMap(s) after 240s"
  sleep 5
done
log "no leaks: no open task instance, and the warm pods and anchors of the version are gone (assertion 5)"

stop_server
log "Warm pool e2e passed"
