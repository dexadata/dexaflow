#!/usr/bin/env bash
#
# End-to-end test for `execution_timeout` on a real Kubernetes cluster (k3d),
# #925 + #930. This is the seam no unit test can reach: it needs a real kubelet,
# because the kubelet counts activeDeadlineSeconds from pod.Status.StartTime —
# a field only a kubelet stamps.
#
# Why a real cluster is the only proof. Two clocks bound a task that declares
# execution_timeout_seconds: the AGENT's (started inside its execute step) and
# the KUBELET's (activeDeadlineSeconds from StartTime, stamped before the image
# pull). The agent owns the SEMANTIC timeout — it is the only one that can say
# "execution_timeout: task exceeded Xs limit". The kubelet's is a backstop for
# an agent that can no longer enforce anything, so it must never fire first;
# when it does, the pod is gone and the reason degrades to what Kubernetes saw
# from outside, which cannot name a timeout. A unit test can pin each half, but
# only a kubelet can run the race.
#
# "Visible" throughout means SERVED on the task-instance API (`failure_reason`),
# which is where this script reads it. The UI does not render the field, so no
# assertion here says anything about what an operator sees on a screen.
#
# The scenario declares `execution_timeout_seconds: 10` on a task that sleeps
# far past it AND leaves a grandchild holding the agent's stdout pipe (#943),
# with the image PRE-LOADED into the cluster so the startup delta is a few
# seconds and the run stays fast.
#
# What is red before #943. Assertions 1 and 2 are red UNCONDITIONALLY, on every
# host: the agent killed its direct child alone, the grandchild kept the stdout
# pipe open, and the agent's Wait would have blocked for the 600s that
# grandchild lives — so the kubelet's 220s deadline always fired first, stamping
# `status.reason=DeadlineExceeded` and settling the pod with a generic reason.
# The agent never reached the branch that names the timeout or writes the
# durable outcome record, so both halves of assertion 1 and the whole of
# assertion 2 fail. These are the scenario's only unconditional OUTCOME reds;
# see the #925 note below for why the earlier, in-process shape had none.
#
# What is red before #925, precisely. Assertion 3 is red UNCONDITIONALLY: the
# pod deadline was the declared timeout itself, so 10 rather than 220, on any
# host. Assertion 1 — the API-visible consequence assertion 3 exists to
# produce — is red wherever the startup the agent's clock does not cover
# exceeds the kubelet's deadline-check granularity, which is every real
# cluster (the image pull and volume mount dominate there, and the kubelet then
# killed at 10s from StartTime while the agent would only fire at 13-15s, so
# the task instance's served reason was the generic container-terminated one).
# On a warm local k3d with the image already imported that startup is
# sub-second, so assertion 1 alone is a coin flip HERE — which is exactly why
# assertion 3 asserts the arithmetic directly rather than inferring it from the
# outcome.
#
# Five assertions:
#   0. the sleeper's shipped log carries a line written by its GRANDCHILD, which
#      is the premise of assertion 1 rather than an assertion in its own right:
#      it proves the task really did hand the agent's stdout pipe to a process
#      the agent does not directly own, so a silently failed spawn cannot let
#      this scenario pass while covering only the easy shape;
#   1. the timed-out task instance's `failure_reason` names `execution_timeout:`
#      — the agent won the race — AND the sleeper pod's own `status.reason` is
#      NOT `DeadlineExceeded`, which names the race winner directly rather than
#      inferring it from the message. The pod-level check is strictly tighter:
#      it also catches the agent's report landing and the kubelet then killing
#      the pod anyway;
#   2. the pod's DURABLE OUTCOME RECORD carries the same diagnosis (#930). This
#      asserts the BYTES are on the pod, not that the reconciler rendered them:
#      the report does land in this scenario, so the record is the channel a
#      LOST report would be settled from, and the settling itself is covered by
#      internal/executor's unit tests;
#   3. a pod created through the REAL DISPATCH PATH carries
#      activeDeadlineSeconds == declared timeout + startup headroom + effective
#      termination grace — locking the seam through dispatch, not just the pod
#      builder, which a unit test already covers;
#   4. an agent frozen after RUNNING has its attempt settled by the AGENT-LOST
#      REAPER inside a window bounded by the ladder itself (75-135s after the
#      freeze; see REAP_MIN/REAP_MAX), instead of lingering to the pod deadline
#      (which for a task declaring no timeout is the 24h credential ceiling).
#
# Requirements: k3d, kubectl, docker, jq, curl, the golang-migrate CLI
# (`migrate`), `make build`, and a running dev Postgres/Redis (`make dev-up`).
# Run from the repository root. On Linux/CI set
# LEOFLOW_E2E_HOST_ADDR=host.k3d.internal.
#
# Usage: test/e2e/execution-timeout-e2e.sh
set -euo pipefail

# shellcheck source=test/e2e/lib.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
CLUSTER="${LEOFLOW_E2E_CLUSTER:-leoflow-timeout-e2e}"
HOST_ADDR="${LEOFLOW_E2E_HOST_ADDR:-$([ "$(uname -s)" = Linux ] && echo host.k3d.internal || echo host.docker.internal)}"
HTTP_PORT="${LEOFLOW_E2E_HTTP_PORT:-8080}"
GRPC_PORT="${LEOFLOW_E2E_GRPC_PORT:-9091}"
METRICS_PORT="${LEOFLOW_E2E_METRICS_PORT:-9090}"
PY_VERSION="${LEOFLOW_E2E_PY_VERSION:-3.11}"
BASE_IMAGE="leoflow-base:py${PY_VERSION}"
DAG_IMAGE="leoflow-timeout-dag:dev"
# A UNIQUE dag_id per invocation, under a fixed prefix.
#
# The dags/dag_versions rows live in the SHARED dev database and outlive this
# script, so a fixed id makes each invocation collide with the last: re-pushing
# the same leoflow version is a 409 ("inserting version: resource already
# exists"), and until that push lands the scheduler is still acting on the
# PREVIOUS version — dispatching ITS runs at control-plane boot, before the image
# import, into pods that carry this run's task-id labels. A fresh id makes both
# impossible by construction. DAG_PREFIX is what purge_stale_dags sweeps, so a
# crashed invocation cannot leave a live DAG behind either, and cleanup
# deregisters this one.
DAG_PREFIX="timeoutdag"
DAG_ID="${DAG_PREFIX}$(date +%s)"
API="http://localhost:${HTTP_PORT}"
NS=leoflow
# The server's OWN database, not the Lite dev database `leoflow db migrate`
# targets. This is the default `database.url` the server boots with, migrated
# here with the golang-migrate CLI exactly as the k3d CI jobs do.
DATABASE_URL="${DATABASE_URL:-postgres://leoflow:leoflow@localhost:5432/leoflow?sslmode=disable}"
WORKDIR="$(mktemp -d)"
# Every curl is bounded: an unreachable control plane must surface as a named
# failure here, not as a hang that a later assertion misattributes.
CURL_MAX_TIME=15

# ── The activeDeadlineSeconds arithmetic under assertion (assertion 3) ────────
# TASK_TIMEOUT is the DAG's declaration. STARTUP_HEADROOM is podStartupHeadroom
# (internal/executor/kubernetes.go), which equals defaultDispatchLostThreshold =
# 3m. GRACE_TERM is the effective termination grace the deadline budgets: the
# task declares none, so podDeadlineGraceTerm falls back to the Kubernetes
# default the kubelet itself applies (corev1.DefaultTerminationGracePeriodSeconds
# = 30). Spelled out rather than folded into one number so a change to any rung
# of the arithmetic fails here with the arithmetic visible.
TASK_TIMEOUT=10
STARTUP_HEADROOM=180
GRACE_TERM=30
WANT_DEADLINE=$((TASK_TIMEOUT + STARTUP_HEADROOM + GRACE_TERM))

# ── The agent-lost window under assertion (assertion 4) ──────────────────────
# The rungs: defaultAgentLostThreshold = 90s (reaper.go), the leader's
# maintenance loop sweeps every reconcileInterval = 30s (leoflow-server), and
# the agent heartbeats every DefaultHeartbeatInterval = 15s (ttl.go).
#
# FLOOR. The reaper measures silence from the LAST heartbeat, which is at most
# one interval before the freeze, so the earliest LEGITIMATE reap is
# threshold - heartbeat = 75s after the freeze. A floor below that tolerates a
# reaper firing before its own threshold — which in production is a false reap
# of a live task — so it is set just under 75 for clock slack, not far under.
#
# CEILING. Worst case the freeze lands just after a sweep, so the reap is
# threshold + sweep + heartbeat = 135s, plus slack. The ceiling separates "the
# reaper settled it" from "it lingered to the pod deadline" (24h for a task
# declaring no timeout), and a tight one also makes this scenario go RED rather
# than silently drift if a rung ever moves.
REAP_MIN=70
REAP_MAX=150
# defaultSettlingGrace: NO reaper fires until this long after the process takes
# leadership, so the freeze must not happen inside that window or assertion 4
# would measure the gate rather than the reaper.
SETTLING_GRACE=180

# RECORD_GRACE bounds the wait for the kubelet to PUBLISH the termination
# message after the task instance has already gone failed (see the sleeper loop).
RECORD_GRACE=60

log() { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
dump_pods() {
  printf '\033[1;33m--- task pods (namespace %s) ---\033[0m\n' "$NS" >&2
  kubectl get pods -n "$NS" -o wide >&2 2>&1 || true
  for p in $(kubectl get pods -n "$NS" -o name 2>/dev/null); do
    printf '\033[1;33m--- describe %s ---\033[0m\n' "$p" >&2
    kubectl describe -n "$NS" "$p" >&2 2>&1 | tail -30 || true
    printf '\033[1;33m--- logs %s ---\033[0m\n' "$p" >&2
    kubectl logs -n "$NS" "$p" --all-containers --tail=60 >&2 2>&1 || true
  done
  printf '\033[1;33m--- server.log tail ---\033[0m\n' >&2
  tail -40 "$WORKDIR/server.log" >&2 2>&1 || true
}
fail() { printf '\033[1;31mFAIL:\033[0m %s\n' "$*" >&2; dump_pods; exit 1; }

SERVER_PID=""
cleanup() {
  set +e
  # Deregister this run's DAG while the control plane is still up. Its rows are
  # `running` at this point (the keeper's agent is frozen on purpose), so leaving
  # them behind poisons the next invocation and leaves live-looking rows in the
  # maintainer's dev database.
  [ -n "${TOKEN:-}" ] && deregister_dag "$TOKEN" "$DAG_ID"
  [ -n "$SERVER_PID" ] && kill "$SERVER_PID" 2>/dev/null
  k3d cluster delete "$CLUSTER" >/dev/null 2>&1
  rm -rf "$WORKDIR"
}
trap cleanup EXIT

# api GETs a path with the admin bearer, bounded.
api() { curl -fsS --max-time "$CURL_MAX_TIME" -H "Authorization: Bearer $TOKEN" "$API$1"; }

# task_field prints one field of one task instance in the run, or empty.
#
# A transient curl failure prints EMPTY instead of failing. Every caller is an
# assignment from a command substitution, and under `set -euo pipefail` such an
# assignment takes the substitution's exit status — so one network blip would
# kill the run with a bare exit code and no pod dump, at whichever line happened
# to be executing. Empty flows into the polling loops instead, whose own
# deadlines fail by name and dump the pods, and into the two reason captures,
# which assert on content and fail by name too.
task_field() {
  api "/api/v2/dags/$DAG_ID/dagRuns/$RUN_ID/taskInstances" 2>/dev/null \
    | jq -r --arg t "$1" --arg f "$2" '.task_instances[] | select(.task_id==$t) | .[$f] // ""' \
    || true
}

# task_pod prints the name of the task pod dispatched for one task_id, or empty.
#
# It REFUSES to guess when more than one pod matches. The selector pairs the
# task id with the DAG id, which the dispatcher stamps on every task pod and
# which is unique per invocation here, so the lookup is unique BY CONSTRUCTION
# rather than by exclusion. That matters because the sweep can only remove DAGs
# under this scenario's own prefix: a task instance left `running` in the SHARED
# dev database by any OTHER dag with a task named the same — a chaos scenario, a
# hand-made dev dag — is re-dispatched once this run's control plane takes
# leadership, and its pod would carry the same task-id label. Picking items[0]
# then silently read and froze the wrong run's pod while the assertions polled
# this run's task instance. The refuse-to-guess check stays as belt and braces.
task_pod() {
  local names count
  names="$(kubectl get pods -n "$NS" -l "leoflow.io/dag-id=$DAG_ID,leoflow.io/task-id=$1" \
    -o jsonpath='{.items[*].metadata.name}' 2>/dev/null || true)"
  count="$(printf '%s' "$names" | wc -w | tr -d ' ')"
  [ "$count" -le 1 ] \
    || fail "$count pods carry leoflow.io/task-id=$1 ($names) — another run of '$DAG_ID' is live in this cluster, so no assertion here can say which pod it read; refusing to guess"
  printf '%s' "$names"
}

# deregister_dag removes one DAG artifact and cascades its runs, task instances
# and XCom. Best-effort and never fails the caller: it is a cleanup primitive.
deregister_dag() {
  local token="$1" dag="$2"
  if curl -fsS --max-time "$CURL_MAX_TIME" -X DELETE -H "Authorization: Bearer $token" \
    "$API/api/v2/dags/$dag?deregister=true" >/dev/null 2>&1; then
    log "deregistered dag $dag"
  else
    log "could not deregister dag $dag (continuing)"
  fi
}

# purge_stale_dags deregisters every DAG this scenario left in the shared dev
# database on an earlier invocation.
#
# It has to exist because the script kills its control plane while its own run is
# still `running` — the keeper's agent is frozen by design — so a crashed or
# interrupted invocation leaves `running` task instances behind. This run's
# control plane then re-dispatches them into the fresh cluster, where their pods
# carry the same leoflow.io/task-id labels as the run under test and shadow every
# pod the assertions read. Called as early as the API allows, so the window
# before a stale run can be dispatched is as small as possible; task_pod refuses
# to guess if anything still slips through.
purge_stale_dags() {
  local token="$1" dag
  for dag in $(curl -fsS --max-time "$CURL_MAX_TIME" -H "Authorization: Bearer $token" \
    "$API/api/v2/dags?limit=200" 2>/dev/null \
    | jq -r --arg p "$DAG_PREFIX" --arg self "$DAG_ID" \
      '.dags[]?.dag_id | select(startswith($p)) | select(. != $self)' 2>/dev/null || true); do
    deregister_dag "$token" "$dag"
  done
}

for tool in k3d kubectl docker jq curl migrate; do
  command -v "$tool" >/dev/null || fail "missing required tool: $tool"
done
[ -x "$ROOT/bin/leoflow" ] && [ -x "$ROOT/bin/leoflow-server" ] || fail "build first: make build"

log "Migrating the SERVER's database ($DATABASE_URL)"
# `leoflow db migrate` is hardcoded to the Lite dev database and is NOT the tool
# for this one; the server's schema is owned by migrations/ + golang-migrate,
# exactly as the k3d CI jobs apply it.
migrate -path "$ROOT/migrations" -database "$DATABASE_URL" up 2>&1 | tail -5 \
  || fail "migrating the server database failed — is the dev Postgres up (make dev-up)?"

log "Scaffolding the timeout DAG project"
PROJ="$WORKDIR/$DAG_ID"
mkdir -p "$PROJ"
# Pin the DAG image to the host arch (see e2e.sh): the loader defaults to
# linux/amd64, which fails FROM an arm64 base on a Lima/dev host → ErrImagePull.
case "$(uname -m)" in arm64|aarch64) HOST_PLATFORM="linux/arm64" ;; *) HOST_PLATFORM="linux/amd64" ;; esac
cat >"$PROJ/dexaflow.yaml" <<YAML
schema_version: "1.0"
dag_id: ${DAG_ID}
build:
  platforms:
    - ${HOST_PLATFORM}
defaults:
  # No retries: a retried timeout would re-dispatch and make the assertions race
  # a second attempt's row.
  retries: 0
tasks:
  sleeper:
    execution_timeout_seconds: ${TASK_TIMEOUT}
YAML
# The body LEAVES A GRANDCHILD holding the agent's stdout, on purpose (#943).
# This scenario used to sleep strictly in-process to avoid that shape, because
# the agent SIGKILLed its direct child only and then waited for the stdout pipe
# to close, so a surviving grandchild blocked the wait past the agent's own
# deadline and handed the race to the kubelet for a reason unrelated to #925.
# That is the shape almost every real task has — the bash operator runs its
# entrypoint under `bash -c`, so any `&&`, pipe or background job makes the
# user's programs grandchildren — so avoiding it meant the green here did not
# cover the common case. The agent now puts the child in its own process group
# and kills the GROUP, so the scenario exercises the path instead of dodging it,
# and assertion 1 is what proves the agent still wins the race.
cat >"$PROJ/dag.py" <<'PY'
"""Locks the execution_timeout race against the kubelet (#925 / #930 / #943)."""
from __future__ import annotations

import subprocess
import time

from airflow.sdk import DAG, task


@task
def sleeper() -> None:
    # Declares execution_timeout_seconds: 10 (dexaflow.yaml) and runs far past it.
    # The agent's own clock must interrupt this, and the timeout must be named
    # on the task instance the API serves.
    #
    # The subprocess is a GRANDCHILD of the agent and inherits this process's
    # stdout, which is the agent's log pipe (#943). Its greeting is asserted in
    # the shipped log, because it is the premise of everything below: if the
    # spawn silently failed, this task would be an ordinary in-process sleep and
    # the scenario would be back to proving the easy case.
    print("sleeper: running past the declared execution_timeout", flush=True)
    subprocess.Popen(  # noqa: S603,S607 - inheriting stdout is the point
        ["sh", "-c", "echo 'sleeper: grandchild holding the agent stdout pipe'; sleep 600"],
    )
    time.sleep(600)


@task
def keeper() -> None:
    # Declares NO timeout, so its pod deadline is the attempt-credential ceiling
    # (24h). Once its agent is frozen mid-run, the agent-lost reaper is the only
    # thing that can settle the attempt.
    print("keeper: running until its agent is frozen", flush=True)
    time.sleep(3600)


PY
# The tail is written with expansion (the body above stays literal) because the
# dag_id is unique per invocation.
#
# schedule=None, like the chaos scenarios: the script triggers exactly ONE run
# and every assertion reads a specific task instance of it. Under a real schedule
# the scheduler adds a run of its own, whose pods carry the same
# leoflow.io/task-id label and shadow the run under test.
cat >>"$PROJ/dag.py" <<PY

with DAG("${DAG_ID}", schedule=None, catchup=False, tags=["e2e"]):
    sleeper()
    keeper()
PY
cat >"$PROJ/Dockerfile" <<DOCKER
FROM ${BASE_IMAGE}
COPY dag.py /home/leoflow/dag.py
ENV PYTHONPATH=/home/leoflow
DOCKER

log "Building the leoflow base image"
docker build --provenance=false -q -f "$ROOT/runtime/Dockerfile" \
  --build-arg "PYTHON_VERSION=${PY_VERSION}" -t "$BASE_IMAGE" "$ROOT" >/dev/null \
  || fail "building the base image failed"

log "Creating k3d cluster '$CLUSTER' + the $NS namespace"
k3d cluster delete "$CLUSTER" >/dev/null 2>&1 || true
k3d cluster create "$CLUSTER" --wait >/dev/null || fail "k3d cluster create failed"
kubectl create namespace "$NS" >/dev/null || fail "creating the $NS namespace failed"

log "Starting the control plane (agents dial ${HOST_ADDR}:${GRPC_PORT})"
export LEOFLOW_AUTH_JWT_SECRET="e2e-secret"
export LEOFLOW_BOOTSTRAP_PASSWORD="admin"
export LEOFLOW_EXECUTOR_AGENT_CONTROL_PLANE_ADDR="${HOST_ADDR}:${GRPC_PORT}"
export LEOFLOW_LOGS_DIR="${WORKDIR}/logs"
export LEOFLOW_SERVER_HTTP_ADDR="0.0.0.0:${HTTP_PORT}"
export LEOFLOW_SERVER_METRICS_ADDR="0.0.0.0:${METRICS_PORT}"
export LEOFLOW_DATABASE_URL="$DATABASE_URL"
# Assertion 4's premise, pinned rather than inherited. The keeper declares no
# execution_timeout, so its pod's activeDeadlineSeconds IS this ceiling — the
# floor branch of podActiveDeadline. Viper binds every LEOFLOW_* automatically,
# so a developer with a low ceiling exported (the state #937 was filed about)
# would have the kubelet preempt the agent-lost reaper, and assertion 4 would go
# red naming the reaper for a shell variable.
export LEOFLOW_AUTH_MAX_ATTEMPT_CREDENTIAL_LIFETIME=24h
"$ROOT/bin/leoflow-server" >"$WORKDIR/server.log" 2>&1 &
SERVER_PID=$!
# Wait on /readyz with a NAMED cause. Without this a control plane that never
# boots (a busy port, an unmigrated database) surfaces minutes later as "the
# task never ran", which points at the executor instead of at the boot.
LEADER_AT=0
for _ in $(seq 1 60); do
  if curl -fsS --max-time 5 "$API/readyz" >/dev/null 2>&1; then LEADER_AT="$(date +%s)"; break; fi
  kill -0 "$SERVER_PID" 2>/dev/null \
    || fail "the control plane exited during boot; server.log tail:
$(tail -30 "$WORKDIR/server.log")"
  sleep 1
done
[ "$LEADER_AT" != 0 ] || fail "the control plane never became ready on $API/readyz after 60s; server.log tail:
$(tail -30 "$WORKDIR/server.log")"

# Mint the admin token and sweep earlier invocations' DAGs IMMEDIATELY, before
# the ~30s of compile and image import: a stale `running` task instance is
# re-dispatched by this control plane, and the sooner its DAG is gone the smaller
# that window is.
TOKEN="$("$ROOT/bin/leoflow" auth create-token --server "$API" \
  --username admin@leoflow.local --password admin)" || fail "minting an admin token failed"
log "Sweeping DAGs left by earlier invocations (prefix '$DAG_PREFIX')"
purge_stale_dags "$TOKEN"

log "Compiling + building the DAG image"
"$ROOT/bin/leoflow" compile "$PROJ" --image "$DAG_IMAGE" --build --dockerfile Dockerfile \
  -o "$PROJ/dag.json" || fail "leoflow compile failed"
jq -e --argjson want "$TASK_TIMEOUT" \
  '.tasks[] | select(.task_id=="sleeper") | select(.execution_timeout_seconds==$want)' \
  "$PROJ/dag.json" >/dev/null \
  || fail "the compiled sleeper task does not carry execution_timeout_seconds=${TASK_TIMEOUT}"

# PRE-LOAD the image. The startup delta the agent's clock does not cover is
# dominated by the image pull; importing keeps it to a few seconds, which is
# what makes a 10s declared timeout a fair race rather than a coin flip.
log "Importing the images into the cluster (pre-loaded: no pull at dispatch)"
k3d_import "$CLUSTER" "$BASE_IMAGE" "$DAG_IMAGE"

log "Pushing + triggering"
"$ROOT/bin/leoflow" push "$PROJ/dag.json" --server "$API" --token "$TOKEN" || fail "leoflow push failed"
RUN_ID="$(curl -fsS --max-time "$CURL_MAX_TIME" -X POST -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" -d '{}' \
  "$API/api/v2/dags/$DAG_ID/dagRuns" | jq -r '.dag_run_id')" || fail "triggering the run failed"
[ -n "$RUN_ID" ] && [ "$RUN_ID" != "null" ] || fail "no dag_run_id returned"

# ── Assertions 1-3: the sleeper races the kubelet and loses nothing ──────────
log "Waiting for 'sleeper' to fail on its execution_timeout"
POD_DEADLINE=""
POD_RECORD=""
POD_STATUS_REASON=""
FAILED_AT=0
deadline=$(( $(date +%s) + 300 ))
while :; do
  # Capture the pod's spec, status reason and durable record WHILE the pod
  # exists: the reconciler garbage-collects a finished pod after its grace
  # period, and these are the only place assertions 1 (pod half), 2 and 3 can be
  # read from. Each is captured once and then kept.
  pod="$(task_pod sleeper)"
  if [ -n "$pod" ]; then
    [ -n "$POD_DEADLINE" ] || POD_DEADLINE="$(kubectl get pod -n "$NS" "$pod" \
      -o jsonpath='{.spec.activeDeadlineSeconds}' 2>/dev/null || true)"
    [ -n "$POD_RECORD" ] || POD_RECORD="$(kubectl get pod -n "$NS" "$pod" \
      -o jsonpath='{range .status.containerStatuses[?(@.name=="task")]}{.state.terminated.message}{end}' 2>/dev/null || true)"
    [ -n "$POD_STATUS_REASON" ] || POD_STATUS_REASON="$(kubectl get pod -n "$NS" "$pod" \
      -o jsonpath='{.status.reason}' 2>/dev/null || true)"
  fi
  state="$(task_field sleeper state)"
  echo "  sleeper=$state pod=${pod:-none} deadline=${POD_DEADLINE:-?} record=${POD_RECORD:+present}"
  case "$state" in
    success) fail "'sleeper' succeeded — it must be interrupted by its execution_timeout" ;;
    failed)
      # Break on the RECORD, not on the state. The task instance flips to failed
      # the moment the agent's report lands, which is a second or two BEFORE the
      # kubelet reads the termination-message file and publishes it on pod
      # status — so breaking on the state alone races assertion 2 into reading an
      # empty string it was simply too early for. Waiting costs nothing: the
      # finished-pod collection grace is ~10 minutes.
      [ -n "$POD_RECORD" ] && break
      [ "$FAILED_AT" != 0 ] || FAILED_AT="$(date +%s)"
      [ "$(( $(date +%s) - FAILED_AT ))" -lt "$RECORD_GRACE" ] \
        || fail "'sleeper' has been failed for ${RECORD_GRACE}s and its pod still publishes no termination message — the kubelet never surfaced the agent's durable record (pod ${pod:-gone})"
      ;;
  esac
  [ "$(date +%s)" -lt "$deadline" ] || fail "timeout waiting for 'sleeper' to fail"
  sleep 2
done

log "Asserting the sleeper really did leave a grandchild on the agent's stdout pipe"
# The PREMISE of assertion 1, checked rather than assumed. The grandchild writes
# its greeting to the stdout it inherited from the task process, so seeing that
# line in the shipped log is proof that the pipe was inherited by a process the
# agent does not directly own — the shape #943 was about. Without this check a
# silently failed spawn (no `sh`, no `sleep`, an image change) would quietly turn
# this scenario back into the in-process sleep it used to be, and it would go on
# passing while covering nothing.
GRANDCHILD_LOG=""
for try in 0 1 2; do
  body="$(curl -fsS --max-time "$CURL_MAX_TIME" -H "Authorization: Bearer $TOKEN" \
    "$API/api/v2/dags/$DAG_ID/dagRuns/$RUN_ID/taskInstances/sleeper/logs/$try" 2>/dev/null || true)"
  case "$body" in *"grandchild holding the agent stdout pipe"*) GRANDCHILD_LOG="$body"; break ;; esac
done
[ -n "$GRANDCHILD_LOG" ] \
  || fail "the sleeper's shipped log never carried the grandchild's line — the task did not spawn a process holding the agent's stdout pipe, so this scenario is not exercising the #943 shape it claims to"

REASON="$(task_field sleeper failure_reason)"
log "sleeper error_message: ${REASON:-<empty>}"
case "$REASON" in
  *execution_timeout:*) : ;;
  *) fail "sleeper error_message = '${REASON}', want it to contain 'execution_timeout:' — the kubelet won the race, so the agent's diagnosis was lost (#925)" ;;
esac

log "Asserting the kubelet did NOT deadline-kill the sleeper pod"
# The tightest statement of "the agent won the race", and tighter than the
# message assertion above: the kubelet stamps status.reason=DeadlineExceeded when
# its own activeDeadlineSeconds fires, so this also catches the agent reporting
# first and the kubelet killing the pod anyway a moment later.
case "$POD_STATUS_REASON" in
  DeadlineExceeded) fail "the sleeper pod's status.reason is 'DeadlineExceeded' — the kubelet's activeDeadlineSeconds fired, so the kubelet was the one that ended the pod (#925)" ;;
  *) log "sleeper pod status.reason: ${POD_STATUS_REASON:-<none, as expected>}" ;;
esac

log "Asserting the pod's DURABLE outcome record carries the same diagnosis (#930)"
# This asserts the reason BYTES are on the pod. It does not assert that the
# reconciler rendered them: the report does land here, so the record is the
# channel a LOST report would be settled from, and that settling is unit-covered
# in internal/executor.
[ -n "$POD_RECORD" ] || fail "the sleeper pod left no termination-message record; a lost report would settle as a bare exit code"
case "$POD_RECORD" in
  *execution_timeout*) log "durable record: $POD_RECORD" ;;
  *) fail "durable outcome record = '${POD_RECORD}', want it to carry the execution_timeout reason (#930)" ;;
esac

log "Asserting the dispatched pod's activeDeadlineSeconds == ${TASK_TIMEOUT}+${STARTUP_HEADROOM}+${GRACE_TERM}"
[ -n "$POD_DEADLINE" ] || fail "never observed the sleeper pod's activeDeadlineSeconds (pod GC'd before it was read?)"
[ "$POD_DEADLINE" = "$WANT_DEADLINE" ] \
  || fail "activeDeadlineSeconds = ${POD_DEADLINE}, want ${WANT_DEADLINE} (declared ${TASK_TIMEOUT} + headroom ${STARTUP_HEADROOM} + grace ${GRACE_TERM}) — dispatch is not applying the #925 arithmetic"

# ── Assertion 4: a frozen agent is settled by the agent-lost reaper ──────────
log "Waiting for 'keeper' to reach running"
deadline=$(( $(date +%s) + 300 ))
while [ "$(task_field keeper state)" != running ]; do
  st="$(task_field keeper state)"
  case "$st" in failed|success) fail "'keeper' reached $st before it could be frozen" ;; esac
  [ "$(date +%s)" -lt "$deadline" ] || fail "timeout waiting for 'keeper' to reach running"
  sleep 3
done
KEEPER_POD="$(task_pod keeper)"
[ -n "$KEEPER_POD" ] || fail "could not find the keeper pod"

# No reaper fires inside the leader-settling grace, so wait it out first —
# otherwise this measures the gate opening, not the reaper's threshold.
elapsed=$(( $(date +%s) - LEADER_AT ))
if [ "$elapsed" -lt "$((SETTLING_GRACE + 15))" ]; then
  waitfor=$(( SETTLING_GRACE + 15 - elapsed ))
  log "Waiting ${waitfor}s for the leader-settling grace to elapse before freezing the agent"
  sleep "$waitfor"
fi

# FREEZE the agent rather than killing it. SIGKILL would end the container, the
# pod would go terminal, and the RECONCILER would settle it in one 30s sweep —
# a different mechanism entirely. SIGSTOP leaves the pod Running with an agent
# that heartbeats no more, which is exactly the shape the agent-lost reaper
# exists for (a wedged or partitioned agent). It has to come from the k3d NODE's
# PID namespace: the agent is the pod container's PID 1, and the kernel ignores
# SIGSTOP sent to a namespace's init from inside that namespace, so a
# `kubectl exec ... kill -STOP 1` would be silently dropped.
log "Freezing the keeper pod's agent (SIGSTOP from the k3d node)"
NODE="k3d-${CLUSTER}-server-0"
CID="$(kubectl get pod -n "$NS" "$KEEPER_POD" \
  -o jsonpath='{range .status.containerStatuses[?(@.name=="task")]}{.containerID}{end}')"
CID="${CID#*://}"
[ -n "$CID" ] || fail "could not read the keeper pod's container id"
AGENT_PID="$(docker exec "$NODE" crictl inspect -o json "$CID" 2>/dev/null | jq -r '.info.pid // empty')"
[ -n "$AGENT_PID" ] || fail "could not resolve the agent's host pid for container $CID via crictl on $NODE"
# Verify the pid IS the agent before signalling it. A stale or wrong pid would
# otherwise be signalled harmlessly and assertion 4 would fail four minutes
# later blaming the reaper for a freeze that never happened.
AGENT_CMD="$(docker exec "$NODE" /bin/sh -c "tr '\0' ' ' < /proc/$AGENT_PID/cmdline" 2>/dev/null || true)"
case "$AGENT_CMD" in
  *leoflow-agent*) : ;;
  *) fail "host pid $AGENT_PID is not the agent (cmdline: '${AGENT_CMD:-<unreadable>}') — refusing to signal it" ;;
esac
docker exec "$NODE" /bin/sh -c "kill -STOP $AGENT_PID" || fail "SIGSTOP of the agent (pid $AGENT_PID) failed"
FROZE_AT="$(date +%s)"
# And verify the signal TOOK. A dropped SIGSTOP leaves a heartbeating agent, and
# assertion 4 would then time out pointing at the reaper.
AGENT_STATE="$(docker exec "$NODE" /bin/sh -c "grep '^State:' /proc/$AGENT_PID/status" 2>/dev/null || true)"
case "$AGENT_STATE" in
  *T*stopped*) log "agent pid $AGENT_PID is ${AGENT_STATE#State:}" ;;
  *) fail "SIGSTOP did not stop the agent (pid $AGENT_PID): /proc status says '${AGENT_STATE:-<unreadable>}', want the stopped state 'T (stopped)'" ;;
esac
log "Agent frozen (host pid $AGENT_PID); expecting the agent-lost reaper in ${REAP_MIN}-${REAP_MAX}s"

deadline=$(( FROZE_AT + REAP_MAX ))
while :; do
  state="$(task_field keeper state)"
  now="$(date +%s)"
  echo "  keeper=$state  t+$(( now - FROZE_AT ))s"
  [ "$state" = failed ] && break
  [ "$now" -lt "$deadline" ] \
    || fail "'keeper' was still '$state' ${REAP_MAX}s after its agent stopped heartbeating — the agent-lost reaper did not settle it, so the attempt lingers to the pod deadline (the 24h credential ceiling)"
  sleep 2
done
REAP_SECS=$(( $(date +%s) - FROZE_AT ))
KEEPER_REASON="$(task_field keeper failure_reason)"
log "keeper settled at t+${REAP_SECS}s with error_message: ${KEEPER_REASON:-<empty>}"
[ "$REAP_SECS" -ge "$REAP_MIN" ] \
  || fail "'keeper' was settled ${REAP_SECS}s after the freeze, under the ${REAP_MIN}s floor — the earliest legitimate reap is the 90s agent-lost threshold minus one 15s heartbeat interval, so anything below this is a reaper firing before its own threshold, which in production false-reaps live tasks"
case "$KEEPER_REASON" in
  *agent_lost*) : ;;
  *) fail "keeper error_message = '${KEEPER_REASON}', want it to name agent_lost — something other than the agent-lost reaper settled the attempt" ;;
esac

log "execution_timeout e2e passed (agent won the race, the diagnosis is durable, dispatch applies the deadline arithmetic, and a frozen agent is reaped in ${REAP_SECS}s)"
