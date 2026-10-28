#!/usr/bin/env bash
#
# Pro source mode (ADR 0067 §3, #1475) on a real k3d cluster (#1539).
#
# Source mode runs a DAG version on the operator's runtime image from the dag.py
# it was registered with: the executor puts the source in a pod annotation and
# projects it as a read-only dag.py through a downward API volume. The unit tests
# of #1520 assert the pod shape BuildPod produces; only a kubelet proves the file
# reaches the container and the runtime imports it. This script runs the whole
# path, with the control plane on the host as in e2e.sh:
#
#   1. The runtime image (the task base image) is pushed to a k3d registry and
#      pinned by the digest the push returns, so the server's digest check and
#      the cluster's pull see the same reference.
#   2. Source mode on, read-only root filesystem off: a version registered with
#      `dexaflow compile --image <runtime image>` and POST /versions (no image
#      build) runs a python task and a bash task, each in its own pod carrying
#      the source annotation, from /opt/leoflow/source; a python task whose
#      source is exactly 128 KiB runs, the same source plus one byte is refused
#      with 400, and a failing task runs its on_failure_callback in the pod.
#   3. The same DAG with the read-only task root filesystem on.
#   4. Warm pools on (with the token exchange and liveness enforcement they
#      require): a version built into its own image gets a warm pod, which shows
#      the pools work in this phase, while the source-mode version gets none and
#      every one of its attempts runs on a dedicated pod.
#
# Requirements: k3d, kubectl, docker, jq, curl, python3, the binaries
# (`make build`), and a migrated database the server reaches with its default
# URL (`make dev-up`). Developer/CI tool, not part of `go test`.
#
# Environment:
#   LEOFLOW_E2E_HTTP_PORT      API port (default 18082)
#   LEOFLOW_E2E_REGISTRY_PORT  k3d registry port (default 5112)
#   LEOFLOW_E2E_HOST_ADDR      address task pods dial for the control plane
#   LEOFLOW_E2E_K3D_ARGS       extra `k3d cluster create` arguments, split on
#                              spaces (for example a CA bundle volume or kubelet
#                              eviction thresholds on a constrained host)
#
# Usage: test/e2e/source-mode-e2e.sh [cluster-name]
set -euo pipefail

# shellcheck source=test/e2e/lib.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

CLUSTER="${1:-dexaflow-source-mode-e2e}"
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
WORKDIR="$(mktemp -d)"
PY_VERSION="3.11"
BASE_IMAGE="leoflow-base:py${PY_VERSION}"
NS="leoflow"
# `.localhost` resolves to the loopback on the host, so `docker push` works over
# plain HTTP, and k3d's registry mirror resolves the same name in the cluster
# (the deploy-e2e.sh pattern). The reference the server is configured with is
# therefore byte for byte the one the pods pull.
REG_NAME="source-mode-registry.localhost"
REG_PORT="${LEOFLOW_E2E_REGISTRY_PORT:-5112}"
REG_FULLNAME="k3d-${REG_NAME}"
REG_HOST="${REG_FULLNAME}:${REG_PORT}"
HTTP_PORT="${LEOFLOW_E2E_HTTP_PORT:-18082}"
API="http://localhost:${HTTP_PORT}"
GRPC_PORT="9091"
HOST_ADDR="${LEOFLOW_E2E_HOST_ADDR:-$([ "$(uname -s)" = Linux ] && echo host.k3d.internal || echo host.docker.internal)}"
SOURCE_DIR="/opt/leoflow/source"
SOURCE_ANNOTATION="leoflow.io/dag-source"
MAX_SOURCE_BYTES=131072
STAMP="$(date +%s)"
SERVER_PID=""
PHASE=""
TOKEN=""
RUNTIME_IMAGE=""

# compile spawns `python3 -m leoflow_parser`; the parser ships under parser/.
export PYTHONPATH="${ROOT}/parser:${PYTHONPATH:-}"
# An isolated kubeconfig: the throwaway cluster never becomes the machine's
# current context, and the server and kubectl below both read this one.
export KUBECONFIG="${WORKDIR}/kubeconfig"

case "$(uname -m)" in arm64|aarch64) HOST_PLATFORM="linux/arm64" ;; *) HOST_PLATFORM="linux/amd64" ;; esac

log() { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
dump() {
  printf '\033[1;33m--- task pods (namespace %s) ---\033[0m\n' "$NS" >&2
  kubectl get pods -n "$NS" -o wide >&2 2>&1 || true
  for p in $(kubectl get pods -n "$NS" -o name 2>/dev/null); do
    printf '\033[1;33m--- describe/logs %s ---\033[0m\n' "$p" >&2
    kubectl describe -n "$NS" "$p" 2>&1 | tail -15 >&2 || true
    kubectl logs -n "$NS" "$p" --all-containers --tail=60 >&2 2>&1 || true
  done
  if [ -n "$PHASE" ] && [ -f "$WORKDIR/server-$PHASE.log" ]; then
    printf '\033[1;33m--- control plane log (%s) ---\033[0m\n' "$PHASE" >&2
    tail -60 "$WORKDIR/server-$PHASE.log" >&2 || true
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
  k3d registry delete "$REG_FULLNAME" >/dev/null 2>&1 || true
  rm -rf "$WORKDIR"
}
trap cleanup EXIT

for tool in k3d kubectl docker jq curl python3; do
  command -v "$tool" >/dev/null || fail "missing required tool: $tool"
done

# start_server PHASE [KEY=VALUE ...] boots the control plane with source mode on
# plus the phase's own settings, and waits for /readyz.
start_server() {
  PHASE="$1"; shift
  log "Starting the control plane for phase '$PHASE' ($*)"
  env \
    LEOFLOW_AUTH_JWT_SECRET="e2e-secret" \
    LEOFLOW_BOOTSTRAP_PASSWORD="admin" \
    LEOFLOW_EXECUTOR_AGENT_CONTROL_PLANE_ADDR="${HOST_ADDR}:${GRPC_PORT}" \
    LEOFLOW_LOGS_DIR="${WORKDIR}/logs" \
    LEOFLOW_AGENT_ALLOW_INSECURE_SECRETS=true \
    LEOFLOW_SECRET_KEY="e2e-secret-key-32-bytes-padding!" \
    LEOFLOW_SERVER_HTTP_ADDR="0.0.0.0:${HTTP_PORT}" \
    DEXAFLOW_EXECUTION_SOURCE_MODE_ENABLED=true \
    DEXAFLOW_EXECUTION_SOURCE_MODE_IMAGE="$RUNTIME_IMAGE" \
    DEXAFLOW_EXECUTOR_COLLECT_SETTLED_RUN_PODS=false \
    "$@" \
    "$ROOT/bin/leoflow-server" >"$WORKDIR/server-$PHASE.log" 2>&1 &
  SERVER_PID=$!
  for _ in $(seq 1 90); do
    curl -fsS "$API/readyz" >/dev/null 2>&1 && break
    kill -0 "$SERVER_PID" 2>/dev/null || fail "the control plane exited during boot in phase '$PHASE'"
    sleep 1
  done
  curl -fsS "$API/readyz" >/dev/null 2>&1 || fail "the control plane did not become ready in phase '$PHASE'"
  TOKEN="$(admin_token "$ROOT/bin/leoflow" "$API" admin)"
}

# write_project DAG_ID writes a project directory whose dag.py comes from stdin.
write_project() {
  mkdir -p "$WORKDIR/$1"
  cat > "$WORKDIR/$1/dexaflow.yaml" <<YAML
schema_version: "1.0"
dag_id: $1
build:
  platforms:
    - ${HOST_PLATFORM}
YAML
  cat > "$WORKDIR/$1/dag.py"
}

# compile_source DAG_ID compiles a project onto the runtime image, with no
# build: the source-mode registration path of the Source mode docs page.
compile_source() {
  "$ROOT/bin/leoflow" compile "$WORKDIR/$1" --image "$RUNTIME_IMAGE" \
    --dag-version "e2e-$STAMP" -o "$WORKDIR/$1/dag.json" >/dev/null
  jq -e --arg img "$RUNTIME_IMAGE" '.image == $img' "$WORKDIR/$1/dag.json" >/dev/null \
    || fail "$1: dag.json does not name the runtime image"
  jq -e '(.source // "") | length > 0' "$WORKDIR/$1/dag.json" >/dev/null \
    || fail "$1: dag.json carries no source"
}

# register FILE DAG_ID prints the HTTP status of POST /versions; the response
# body is left in $WORKDIR/register.body.
register() {
  curl -sS -o "$WORKDIR/register.body" -w '%{http_code}' -X POST \
    -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
    --data-binary @"$1" "$API/api/v2/dags/$2/versions"
}

register_ok() {
  local code
  code="$(register "$WORKDIR/$1/dag.json" "$1")"
  [ "$code" = "201" ] || fail "$1: POST /versions answered $code, want 201: $(cat "$WORKDIR/register.body")"
}

trigger() {
  local rid
  # New DAGs are paused at creation, and a manual run of a paused DAG waits
  # until the DAG is unpaused.
  unpause_dag "$API" "$1" "$TOKEN" || fail "$1: unpausing failed"
  rid="$(curl -fsS -X POST -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
    -d '{}' "$API/api/v2/dags/$1/dagRuns" | jq -r '.dag_run_id')"
  [ -n "$rid" ] && [ "$rid" != "null" ] || fail "$1: no dag_run_id returned"
  printf '%s' "$rid"
}

# wait_run DAG_ID RUN_ID WANT [SAMPLER] waits until every task instance is
# terminal and checks the run against WANT: "success" (all succeeded) or
# "failed" (one failed). SAMPLER, when given, runs on every poll.
wait_run() {
  local dag="$1" run="$2" want="$3" sampler="${4:-}" deadline states
  deadline=$(( $(date +%s) + 300 ))
  while :; do
    [ -n "$sampler" ] && "$sampler"
    states="$(curl -fsS -H "Authorization: Bearer $TOKEN" \
      "$API/api/v2/dags/$dag/dagRuns/$run/taskInstances" | jq -r '.task_instances[].state')"
    if echo "$states" | grep -qE '^(failed|upstream_failed)$'; then
      [ "$want" = "failed" ] && return 0
      fail "$dag: a task failed: $(echo "$states" | tr '\n' ' ')"
    fi
    if [ -n "$states" ] && ! echo "$states" | grep -qvE '^(success|skipped)$'; then
      [ "$want" = "success" ] && return 0
      fail "$dag: every task succeeded, want a failure"
    fi
    [ "$(date +%s)" -gt "$deadline" ] && fail "$dag: timed out; last states: $(echo "${states:-<none>}" | tr '\n' ' ')"
    sleep 3
  done
}

# task_log DAG_ID RUN_ID TASK_ID MARKER prints the first try's log holding MARKER.
task_log() {
  local body try
  for try in 1 0 2; do
    body="$(curl -fsS -H "Authorization: Bearer $TOKEN" \
      "$API/api/v2/dags/$1/dagRuns/$2/taskInstances/$3/logs/$try" 2>/dev/null || true)"
    if echo "$body" | grep -q "$4"; then printf '%s' "$body"; return 0; fi
  done
  fail "$1.$3: no task log carries '$4'"
}

# assert_source_pods DAG_ID COUNT RO checks that the DAG's task pods are COUNT
# dedicated source-mode pods: runtime image, source annotation, working directory
# on the projected source, no warm-worker label, and the read-only root
# filesystem as RO says.
assert_source_pods() {
  local f="$WORKDIR/pods-$1.json"
  kubectl get pods -n "$NS" -l "leoflow.io/dag-id=$1" -o json > "$f"
  jq -e --argjson n "$2" '.items | length == $n' "$f" >/dev/null \
    || fail "$1: want $2 task pods, got $(jq '.items | length' "$f")"
  jq -e --arg img "$RUNTIME_IMAGE" '.items | all(.spec.containers[0].image == $img)' "$f" >/dev/null \
    || fail "$1: a task pod does not run the runtime image"
  jq -e --arg a "$SOURCE_ANNOTATION" '.items | all((.metadata.annotations[$a] // "") | length > 0)' "$f" >/dev/null \
    || fail "$1: a task pod does not carry the $SOURCE_ANNOTATION annotation"
  jq -e --arg d "$SOURCE_DIR" '.items | all(.spec.containers[0].workingDir == $d)' "$f" >/dev/null \
    || fail "$1: a task pod does not work from $SOURCE_DIR"
  jq -e '.items | all(.metadata.labels["leoflow.io/warm-worker"] == null)' "$f" >/dev/null \
    || fail "$1: a source-mode attempt ran on a warm worker"
  jq -e --argjson ro "$3" '.items | all((.spec.containers[0].securityContext.readOnlyRootFilesystem // false) == $ro)' "$f" >/dev/null \
    || fail "$1: a task pod's readOnlyRootFilesystem is not $3"
}

# ─────────────────────────────────────────────────────────────────────────────
log "Building the runtime image ($BASE_IMAGE)"
docker build --provenance=false -f "$ROOT/runtime/Dockerfile" --build-arg "PYTHON_VERSION=${PY_VERSION}" -t "$BASE_IMAGE" "$ROOT"

log "Creating k3d registry ($REG_HOST) + cluster '$CLUSTER'"
k3d registry delete "$REG_FULLNAME" >/dev/null 2>&1 || true
k3d registry create "$REG_NAME" --port "$REG_PORT" >/dev/null
# shellcheck disable=SC2086 # LEOFLOW_E2E_K3D_ARGS is a space-separated list on purpose
k3d cluster create "$CLUSTER" --registry-use "$REG_HOST" --wait \
  --kubeconfig-update-default=false --kubeconfig-switch-context=false ${LEOFLOW_E2E_K3D_ARGS:-}
k3d kubeconfig get "$CLUSTER" > "$KUBECONFIG"
kubectl create namespace "$NS"

log "Pushing the runtime image and pinning it by digest"
docker tag "$BASE_IMAGE" "$REG_HOST/dexaflow-runtime:e2e"
push_out="$(docker push "$REG_HOST/dexaflow-runtime:e2e" 2>&1)" || { echo "$push_out" >&2; fail "docker push of the runtime image failed"; }
digest="$(printf '%s\n' "$push_out" | sed -nE 's/.*digest: (sha256:[a-f0-9]{64}).*/\1/p' | tail -1)"
[ -n "$digest" ] || { echo "$push_out" >&2; fail "docker push printed no sha256 digest"; }
RUNTIME_IMAGE="$REG_HOST/dexaflow-runtime@$digest"
log "runtime image = $RUNTIME_IMAGE"

# ─────────────────────────────────────────────────────────────────────────────
# Phase 1: source mode on, read-only root filesystem off.
start_server rofs-off DEXAFLOW_EXECUTOR_DEFAULTS_READ_ONLY_TASK_ROOT_FILESYSTEM=false

# The DAG of phases 1 and 2. The python task checks it runs from the projected
# source directory and can write to its temporary directory; the bash task
# renders a macro, so the runtime loaded the DAG from the source before it ran.
src_dag() {
  cat <<PY
"""$1: a Pro source-mode DAG, registered with no image build."""
from __future__ import annotations

import os

from airflow.providers.standard.operators.bash import BashOperator
from airflow.sdk import DAG, task


@task
def py_hello() -> str:
    here = os.getcwd()
    print("E2E_SRC_PY cwd=" + here, flush=True)
    assert os.path.isfile(os.path.join(here, "dag.py")), "dag.py is not in the working directory"
    tmp = os.environ.get("TMPDIR") or "/tmp"
    with open(os.path.join(tmp, "e2e-source-mode"), "w") as fh:
        fh.write("ok")
    print("E2E_SRC_TMP_WRITE ok", flush=True)
    return "src-42"


with DAG("$1", schedule=None, tags=["e2e"]):
    py_hello() >> BashOperator(task_id="bash_hello", bash_command='echo "E2E_SRC_BASH ds={{ ds }}"')
PY
}

run_src_dag() { # DAG_ID RO
  src_dag "$1" | write_project "$1"
  compile_source "$1"
  register_ok "$1"
  local run
  run="$(trigger "$1")"
  log "$1 run = $run"
  wait_run "$1" "$run" success
  task_log "$1" "$run" py_hello "E2E_SRC_PY cwd=${SOURCE_DIR}" >/dev/null
  task_log "$1" "$run" py_hello "E2E_SRC_TMP_WRITE ok" >/dev/null
  task_log "$1" "$run" bash_hello "E2E_SRC_BASH ds=[0-9]\{4\}-[0-9]\{2\}-[0-9]\{2\}" >/dev/null
  assert_source_pods "$1" 2 "$2"
  log "$1: python and bash tasks ran from the projected dag.py on dedicated pods (readOnlyRootFilesystem=$2)"
}

log "Phase 1: a python task and a bash task in source mode"
run_src_dag srcdag false

log "Phase 1: a source of exactly ${MAX_SOURCE_BYTES} bytes runs"
python3 - "$WORKDIR/srcbig.py" "$MAX_SOURCE_BYTES" <<'PY'
import sys

path, size = sys.argv[1], int(sys.argv[2])
head = '''"""srcbig: a source-mode DAG whose dag.py is exactly at the source cap."""
from __future__ import annotations

import os

from airflow.sdk import DAG, task


@task
def big() -> None:
    print("E2E_SRC_BIG size=%d" % os.path.getsize("dag.py"), flush=True)


with DAG("srcbig", schedule=None, tags=["e2e"]):
    big()

'''
body = head
line = "# " + "x" * 77 + "\n"
while len(body.encode()) + len(line) <= size:
    body += line
rest = size - len(body.encode())
if rest == 1:
    body += "\n"
elif rest > 1:
    body += "#" + "x" * (rest - 2) + "\n"
assert len(body.encode()) == size, len(body.encode())
open(path, "w").write(body)
PY
write_project srcbig < "$WORKDIR/srcbig.py"
compile_source srcbig
got="$(jq -j '.source' "$WORKDIR/srcbig/dag.json" | wc -c | tr -d ' ')"
[ "$got" = "$MAX_SOURCE_BYTES" ] || fail "srcbig: the compiled source is $got bytes, want $MAX_SOURCE_BYTES"
register_ok srcbig
run="$(trigger srcbig)"
wait_run srcbig "$run" success
task_log srcbig "$run" big "E2E_SRC_BIG size=${MAX_SOURCE_BYTES}" >/dev/null
assert_source_pods srcbig 1 false
log "srcbig: a ${MAX_SOURCE_BYTES} byte source ran"

log "Phase 1: the same source plus one byte is refused with 400"
jq --arg v "e2e-$STAMP-over" '.source += "\n" | .dag_version = $v' "$WORKDIR/srcbig/dag.json" > "$WORKDIR/srcbig-over.json"
code="$(register "$WORKDIR/srcbig-over.json" srcbig)"
[ "$code" = "400" ] || fail "srcbig: a $((MAX_SOURCE_BYTES + 1)) byte source got $code, want 400: $(cat "$WORKDIR/register.body")"
grep -q "over the ${MAX_SOURCE_BYTES} byte limit" "$WORKDIR/register.body" \
  || fail "srcbig: the 400 does not name the source cap: $(cat "$WORKDIR/register.body")"
log "srcbig: a $((MAX_SOURCE_BYTES + 1)) byte source was refused: $(jq -r '.detail // .' "$WORKDIR/register.body")"

log "Phase 1: a failing source-mode task runs its on_failure_callback in the pod"
write_project srccb <<'PY'
"""srccb: a failing source-mode task with an on_failure_callback."""
from airflow.sdk import DAG, task


def on_fail(context):
    print("E2E_SRC_CALLBACK_FIRED task=" + context["ti"].task_id, flush=True)


with DAG("srccb", schedule=None):

    @task(on_failure_callback=on_fail)
    def boom():
        raise RuntimeError("intentional e2e failure for the source-mode callback")

    boom()
PY
compile_source srccb
jq -e '.tasks[] | select(.task_id=="boom" and .on_failure_callback==true)' "$WORKDIR/srccb/dag.json" >/dev/null \
  || fail "srccb: boom did not compile with on_failure_callback=true"
register_ok srccb
run="$(trigger srccb)"
wait_run srccb "$run" failed
cblog="$(task_log srccb "$run" boom "E2E_SRC_CALLBACK_FIRED task=boom")"
echo "$cblog" | grep -q "running on_failure_callback" \
  || fail "srccb: the runtime did not log the on_failure_callback lifecycle"
log "srccb: the callback ran in the source-mode pod"
stop_server

# ─────────────────────────────────────────────────────────────────────────────
# Phase 2: the read-only task root filesystem on. The source volume is read only
# either way; this checks the rest of the pod still lets a source-mode task run.
start_server rofs-on DEXAFLOW_EXECUTOR_DEFAULTS_READ_ONLY_TASK_ROOT_FILESYSTEM=true
log "Phase 2: the same DAG with readOnlyRootFilesystem on"
run_src_dag srcro true
stop_server

# ─────────────────────────────────────────────────────────────────────────────
# Phase 3: warm pools on. A warm pod is created before its task is known, so it
# cannot carry a dag.py: a source-mode version must get no warm pool and every
# attempt a dedicated pod (ADR 0067 §3). A version built into its own image is
# the control: its warm pod shows the pools are live in this phase, so "no warm
# pod for the source-mode version" is not true merely because nothing is warm.
start_server warm \
  DEXAFLOW_EXECUTION_WARM_POOLS_ENABLED=true \
  DEXAFLOW_AUTH_AGENT_TOKEN_TRANSPORT=exchange \
  DEXAFLOW_AUTH_SECRET_LIVENESS_MODE=enforce \
  DEXAFLOW_EXECUTION_MIN_IDLE_WORKERS=1

# Both DAGs hold their run open for a while, so the warm-pool reconciler has
# ticks in which each version is active.
hold_dag() {
  cat <<PY
"""$1: holds its run open, then finishes."""
from __future__ import annotations

import time

from airflow.sdk import DAG, task


@task
def hold() -> None:
    time.sleep(45)
    print("E2E_HOLD_DONE", flush=True)


@task
def after() -> None:
    print("E2E_AFTER_DONE", flush=True)


with DAG("$1", schedule=None, tags=["e2e"]):
    hold() >> after()
PY
}

log "Phase 3: registering the control version on its own image"
hold_dag ctlwarm | write_project ctlwarm
cat > "$WORKDIR/ctlwarm/Dockerfile" <<DOCKER
FROM ${BASE_IMAGE}
COPY dag.py /home/leoflow/dag.py
ENV PYTHONPATH=/home/leoflow
DOCKER
CTL_IMAGE="dexaflow-e2e-ctlwarm:${STAMP}"
"$ROOT/bin/leoflow" compile "$WORKDIR/ctlwarm" --image "$CTL_IMAGE" --build --dockerfile Dockerfile \
  --dag-version "e2e-$STAMP" -o "$WORKDIR/ctlwarm/dag.json" >/dev/null
k3d_import "$CLUSTER" "$CTL_IMAGE"
register_ok ctlwarm

log "Phase 3: registering the source-mode version"
hold_dag srcwarm | write_project srcwarm
compile_source srcwarm
register_ok srcwarm

saw_ctl_warm=0
sample_warm() {
  local images
  images="$(kubectl get pods -n "$NS" -l leoflow.io/warm-worker=true \
    -o jsonpath='{range .items[*]}{range .spec.containers[*]}{.image}{"\n"}{end}{end}' 2>/dev/null || true)"
  if printf '%s\n' "$images" | grep -qxF "$RUNTIME_IMAGE"; then
    fail "srcwarm: a warm worker pod runs the source-mode runtime image"
  fi
  if printf '%s\n' "$images" | grep -qxF "$CTL_IMAGE"; then
    saw_ctl_warm=1
  fi
}

ctl_run="$(trigger ctlwarm)"
src_run="$(trigger srcwarm)"
log "ctlwarm run = $ctl_run, srcwarm run = $src_run"
wait_run srcwarm "$src_run" success sample_warm
wait_run ctlwarm "$ctl_run" success sample_warm
# The reconciler may still be acting on the last tick: one more look.
sleep 10
sample_warm
[ "$saw_ctl_warm" = "1" ] \
  || fail "ctlwarm: no warm worker pod ever ran the control image, so this phase did not prove warm pools were live"
task_log srcwarm "$src_run" after "E2E_AFTER_DONE" >/dev/null
assert_source_pods srcwarm 2 false
log "warm pools: the control version got a warm pod; the source-mode version got none and ran both attempts on dedicated pods"
stop_server

log "Source mode e2e passed"
