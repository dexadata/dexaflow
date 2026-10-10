#!/usr/bin/env bash
#
# Runtime chaos / fault-injection e2e (#231 Phase 2) — RUN ON THE LIMA LINUX VM.
#
# Regression suites confirm known behaviour with fakes; this injects real faults
# on a real k3d cluster to surface the UNEXPECTED, targeting the two newest,
# riskiest surfaces:
#   A) scheduler-crash recovery in the api/scheduler SPLIT (ADR 0049) — kill the
#      scheduler process mid-run; assert the api's /monitor/health flips the
#      scheduler to unhealthy (the LeaderHealthReader, advisory-lock liveness),
#      then restart it and assert the in-flight run RESUMES to success and each
#      task ran exactly once (at-most-once).
#   B) task-pod kill (#474/#518) — delete a running task's pod; assert the reaper
#      moves the TI off `running` to a terminal state (not stuck forever) and
#      leaves no orphaned pod for that attempt.
#   C) durable outcome recovery (ADR 0052) — a task succeeds but the agent is told
#      to exit mid-report AFTER writing the durable outcome record; assert the
#      reconciler recovers the SUCCESS from the record (the pod is Failed and no
#      report ever landed), settling the run success instead of failing/retrying.
#   D) control-plane outage across termination (the field drill) — C's fault on
#      THREE parallel tasks with the scheduler SIGKILLed before they finish, so
#      the restarted leader wakes to task instances that are `running` with stale
#      heartbeats and pods that are terminal, exactly the signals an outage
#      manufactures. Assert the reconciler settles all three from their records
#      INSIDE the leader-settling grace, that the reap pass ran and was held
#      while the gate was shut, that the liveness valve never opened, and that
#      no reaper fired in that window. NOTE what it does NOT prove: the gate
#      refusing a REAL candidate (the same cycle's reconcile has already settled
#      them by the first tick, so cycle ordering alone explains the result), the
#      window after the grace opens, and #915's presence rule.
#
# Select a subset with LEOFLOW_CHAOS_ONLY (e.g. LEOFLOW_CHAOS_ONLY=D, or "BD");
# the shared setup always runs, so any scenario works on its own.
#
# NOT a `go test`; destructive; not gated in CI (slow). It mirrors the proven
# host-process split harness (split-two-process.sh) and reuses the k3d_import
# work-around (lib.sh). Best run inside Lima (Linux) — the CGO services and k3d
# behave as in prod there, unlike the macOS host.
#
# Usage: test/e2e/chaos-runtime.sh [cluster-name]
set -uo pipefail

# shellcheck source=test/e2e/lib.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

CLUSTER="${1:-leoflow-chaos}"
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
WORKDIR="$(mktemp -d)"
PY_VERSION="3.11"
BASE_IMAGE="leoflow-base:py${PY_VERSION}"
DAG_IMAGE="leoflow-chaos-dag:dev"
DAG_ID="chaosdag"
RECOVER_DAG_IMAGE="leoflow-recover-dag:dev"
RECOVER_DAG_ID="recoverdag"
OUTAGE_DAG_IMAGE="leoflow-outage-dag:dev"
OUTAGE_DAG_ID="outagedag"

API_HTTP_PORT="${LEOFLOW_E2E_API_HTTP_PORT:-8080}"
API_METRICS_PORT="9090"
SCHED_METRICS_PORT="9092"
GRPC_PORT="9091"
API="http://localhost:${API_HTTP_PORT}"
HOST_ADDR="${LEOFLOW_E2E_HOST_ADDR:-$([ "$(uname -s)" = Linux ] && echo host.k3d.internal || echo host.docker.internal)}"
# The dev Postgres container to restart in scenario C (future); its name differs
# between `make dev-up` (compose) and a standalone run. Overridable.
API_PID=""; SCHED_PID=""; TOKEN=""; FAILED=0

log()  { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
ok()   { printf '\033[1;32m    PASS\033[0m %s\n' "$*"; }
bad()  { printf '\033[1;31m    FAIL\033[0m %s\n' "$*" >&2; FAILED=1; }
dump_pods() {
  kubectl get pods -n leoflow -o wide >&2 2>&1 || true
}
fatal() { printf '\033[1;31mFATAL:\033[0m %s\n' "$*" >&2; dump_pods; exit 1; }

cleanup() {
  [ -n "$API_PID" ] && kill "$API_PID" 2>/dev/null || true
  [ -n "$SCHED_PID" ] && kill "$SCHED_PID" 2>/dev/null || true
  k3d cluster delete "$CLUSTER" >/dev/null 2>&1 || true
  rm -rf "$WORKDIR"
}
trap cleanup EXIT

for tool in k3d kubectl docker jq curl; do
  command -v "$tool" >/dev/null || fatal "missing required tool: $tool"
done

# ── shared control-plane env (one logical control plane, two role processes) ──
export LEOFLOW_AUTH_JWT_SECRET="chaos-secret"
export LEOFLOW_BOOTSTRAP_PASSWORD="admin"
export LEOFLOW_SECRET_KEY="chaos-secret-key-32-bytes-padd!!"
export LEOFLOW_AGENT_ALLOW_INSECURE_SECRETS=true
export LEOFLOW_REDIS_URL="${LEOFLOW_E2E_REDIS_URL:-redis://localhost:6379/0}"
export LEOFLOW_LOGS_DIR="${WORKDIR}/logs"; mkdir -p "$LEOFLOW_LOGS_DIR"

start_scheduler() {
  LEOFLOW_SERVER_ROLE=scheduler \
  LEOFLOW_SERVER_HTTP_ADDR="0.0.0.0:8085" \
  LEOFLOW_SERVER_METRICS_ADDR="0.0.0.0:${SCHED_METRICS_PORT}" \
  LEOFLOW_SERVER_GRPC_ADDR="0.0.0.0:${GRPC_PORT}" \
  LEOFLOW_EXECUTOR_AGENT_CONTROL_PLANE_ADDR="${HOST_ADDR}:${GRPC_PORT}" \
    "$ROOT/bin/leoflow-server" >"${WORKDIR}/scheduler.log" 2>&1 &
  SCHED_PID=$!
}

# scheduler_health returns the status string the api reports for the scheduler.
scheduler_health() {
  curl -fsS --max-time 10 -H "Authorization: Bearer $TOKEN" "$API/api/v2/monitor/health" 2>/dev/null \
    | jq -r '.scheduler.status // "unknown"'
}

# wait_scheduler_status polls /monitor/health until the scheduler status matches
# $1 (healthy|unhealthy) or the timeout elapses. Returns 0 on match.
wait_scheduler_status() {
  local want="$1" deadline; deadline=$(( $(date +%s) + ${2:-30} ))
  while [ "$(date +%s)" -lt "$deadline" ]; do
    [ "$(scheduler_health)" = "$want" ] && return 0
    sleep 2
  done
  return 1
}

task_states() {
  curl -fsS --max-time 10 -H "Authorization: Bearer $TOKEN" \
    "$API/api/v2/dags/$DAG_ID/dagRuns/$1/taskInstances" 2>/dev/null | jq -r '.task_instances[].state'
}

# ── build + cluster + two role processes ─────────────────────────────────────
log "Scaffolding the chaos DAG (a sleeper so faults land mid-run)"
"$ROOT/bin/leoflow" init "$WORKDIR/$DAG_ID" >/dev/null
case "$(uname -m)" in arm64|aarch64) HOST_PLATFORM="linux/arm64" ;; *) HOST_PLATFORM="linux/amd64" ;; esac
cat >> "$WORKDIR/$DAG_ID/dexaflow.yaml" <<YAML
build:
  platforms:
    - ${HOST_PLATFORM}
YAML
cat > "$WORKDIR/$DAG_ID/dag.py" <<'PY'
"""chaosdag — a sleeper DAG so a fault can be injected while a task runs."""
from __future__ import annotations
import time
from airflow.sdk import DAG, task


@task
def work() -> str:
    # Long enough to (a) kill the scheduler mid-run and (b) let the agent emit at
    # least one heartbeat (15s interval) before scenario B kills the pod, so the
    # agent-lost backstop is armed. Comfortably outlives both scenarios' setup.
    print("chaos: work START", flush=True)
    time.sleep(90)
    print("chaos: work DONE", flush=True)
    return "done"


@task
def finish(value: str) -> None:
    print(f"chaos: finish saw {value}", flush=True)


with DAG("chaosdag", schedule=None, catchup=False, tags=["chaos"]):
    finish(work())
PY
cat > "$WORKDIR/$DAG_ID/Dockerfile" <<DOCKER
FROM ${BASE_IMAGE}
COPY dag.py /home/leoflow/dag.py
ENV PYTHONPATH=/home/leoflow
DOCKER

log "Building base image + cluster"
docker build --provenance=false -f "$ROOT/runtime/Dockerfile" --build-arg "PYTHON_VERSION=${PY_VERSION}" -t "$BASE_IMAGE" "$ROOT" \
  || fatal "base image build failed — refusing to continue against a possibly stale $BASE_IMAGE"

# A build that fails (e.g. a transient registry timeout pulling layers) used to
# leave any pre-existing $BASE_IMAGE tag in place, so the run silently proceeded on
# a stale agent binary. When that agent predates the fault-injection seam, the
# durable-outcome scenario runs the ordinary report path and passes for the wrong
# reason. Assert the freshly-built agent carries the seam the scenario depends on,
# so a stale image can never masquerade as a green run.
docker run --rm --entrypoint sh "$BASE_IMAGE" \
  -c 'grep -aq LEOFLOW_CHAOS_DIE_BEFORE_REPORT /usr/local/bin/leoflow-agent' \
  || fatal "$BASE_IMAGE has a stale leoflow-agent (missing the LEOFLOW_CHAOS_DIE_BEFORE_REPORT seam); run 'docker rmi -f $BASE_IMAGE' and re-run to force a fresh build"

k3d cluster create "$CLUSTER" --wait
kubectl create namespace leoflow

log "Starting scheduler + api role processes"
start_scheduler
LEOFLOW_SERVER_ROLE=api \
LEOFLOW_SERVER_HTTP_ADDR="0.0.0.0:${API_HTTP_PORT}" \
LEOFLOW_SERVER_METRICS_ADDR="0.0.0.0:${API_METRICS_PORT}" \
  "$ROOT/bin/leoflow-server" >"${WORKDIR}/api.log" 2>&1 &
API_PID=$!

# Wait for the api to actually serve, and fail HERE with both role logs if it
# does not. A fixed sleep let a control plane that died at boot (an unmigrated
# database, a taken port) sail past this line: the next few steps ignore their
# own errors, so the run surfaced minutes later as the unrelated-looking "no run
# id" from whichever scenario happened to run first.
wait_api() {
  local deadline; deadline=$(( $(date +%s) + 45 ))
  while [ "$(date +%s)" -lt "$deadline" ]; do
    curl -fsS --max-time 10 "${API}/healthz" >/dev/null 2>&1 && return 0
    kill -0 "$API_PID" 2>/dev/null || return 1
    sleep 1
  done
  return 1
}
if ! wait_api; then
  printf '\n---- api.log ----\n' >&2; tail -20 "${WORKDIR}/api.log" >&2 2>/dev/null || true
  printf '\n---- scheduler.log ----\n' >&2; tail -20 "${WORKDIR}/scheduler.log" >&2 2>/dev/null || true
  fatal "the api role never served ${API}/healthz — see the role logs above (a local run needs the server's database migrated: DATABASE_URL=<dsn> bin/dexaflow db migrate)"
fi

log "Compiling + importing the DAG image"
"$ROOT/bin/leoflow" compile "$WORKDIR/$DAG_ID" --image "$DAG_IMAGE" --build --dockerfile Dockerfile -o "$WORKDIR/$DAG_ID/dag.json" >/dev/null
k3d_import "$CLUSTER" "$BASE_IMAGE" "$DAG_IMAGE" || fatal "k3d import failed"
# deregister_dag drops a DAG left behind by an earlier run on the same database.
# A DAG version is immutable and keyed by the repository's describe output, so
# re-running after editing a scenario's DAG body — without a new commit — is
# rejected with 409 "resource already exists" and, worse, would otherwise risk
# running the PREVIOUS body under the same version. The cluster is recreated per
# run but the control-plane database is not, so this is what makes the harness
# re-runnable. A DAG that was never registered is not an error.
deregister_dag() {
  "$ROOT/bin/leoflow" dags delete "$1" --deregister --server "$API" --token "$TOKEN" >/dev/null 2>&1 || true
}

TOKEN="$("$ROOT/bin/leoflow" auth create-token --server "$API" --username admin@leoflow.local --password admin)" \
  || fatal "could not mint an admin token against $API"
[ -n "$TOKEN" ] || fatal "empty admin token from $API"
deregister_dag "$DAG_ID"
"$ROOT/bin/leoflow" push "$WORKDIR/$DAG_ID/dag.json" --server "$API" --token "$TOKEN" >/dev/null \
  || fatal "pushing $DAG_ID failed — the scenarios below would all report a missing run id"

trigger_run() {
  curl -fsS --max-time 10 -X POST -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
    -d '{}' "$API/api/v2/dags/$DAG_ID/dagRuns" | jq -r '.dag_run_id'
}

wait_task_running() {  # $1=run
  local deadline; deadline=$(( $(date +%s) + 90 ))
  while [ "$(date +%s)" -lt "$deadline" ]; do
    kubectl get pods -n leoflow --no-headers 2>/dev/null | grep -q 'work.*Running' && return 0
    task_states "$1" | grep -q 'failed\|upstream_failed' && return 1
    sleep 3
  done
  return 1
}

# ─────────────────────────────────────────────────────────────────────────────
# Scenario A — scheduler-crash recovery in the split
# ─────────────────────────────────────────────────────────────────────────────
scenario_scheduler_crash() {
  log "SCENARIO A — kill the scheduler mid-run; assert health flips, run resumes, at-most-once"
  local run; run="$(trigger_run)"
  [ -n "$run" ] && [ "$run" != "null" ] || { bad "A: no run id"; return; }
  wait_task_running "$run" || { bad "A: 'work' pod never reached Running"; return; }

  # api must currently see the scheduler healthy.
  wait_scheduler_status healthy 20 || { bad "A: scheduler not healthy before the kill"; return; }

  log "A: killing the scheduler process ($SCHED_PID)"
  kill "$SCHED_PID" 2>/dev/null; wait "$SCHED_PID" 2>/dev/null; SCHED_PID=""
  # The api reads scheduler liveness from the leadership advisory lock; when the
  # scheduler's session drops, the lock releases → unhealthy. (F1 fix, live.)
  if wait_scheduler_status unhealthy 40; then
    ok "A: /monitor/health flipped scheduler → unhealthy after the crash"
  else
    bad "A: scheduler still reported healthy after the process was killed (LeaderHealthReader?)"
  fi

  log "A: restarting the scheduler; run must resume"
  start_scheduler
  wait_scheduler_status healthy 40 || bad "A: scheduler did not report healthy after restart"

  local deadline states; deadline=$(( $(date +%s) + 180 ))
  while [ "$(date +%s)" -lt "$deadline" ]; do
    states="$(task_states "$run")"
    echo "$states" | grep -qE 'failed|upstream_failed' && { bad "A: a task failed after restart: $states"; return; }
    if [ -n "$states" ] && ! echo "$states" | grep -qvE 'success|skipped'; then
      ok "A: in-flight run RESUMED to success after the scheduler restart"
      break
    fi
    sleep 4
  done
  [ "$(date +%s)" -lt "$deadline" ] || { bad "A: run did not resume to success within 180s"; return; }

  # At-most-once: exactly one pod per attempt for 'work' (a resumed scheduler must
  # NOT re-dispatch a task that already ran — no duplicate work pod).
  local workpods
  workpods="$(kubectl get pods -n leoflow --no-headers 2>/dev/null | grep -c 'chaosdag-work-')"
  if [ "$workpods" -le 1 ]; then
    ok "A: at-most-once held — a single 'work' pod ($workpods), no duplicate dispatch on resume"
  else
    bad "A: $workpods 'work' pods — the resumed scheduler double-dispatched (at-most-once violated)"
  fi
}

# ─────────────────────────────────────────────────────────────────────────────
# Scenario B — task-pod kill → reaper moves the TI off running, no orphan
# ─────────────────────────────────────────────────────────────────────────────
scenario_task_pod_kill() {
  log "SCENARIO B — delete a running task pod; assert the TI leaves 'running' and no orphan lingers"
  local run; run="$(trigger_run)"
  [ -n "$run" ] && [ "$run" != "null" ] || { bad "B: no run id"; return; }
  wait_task_running "$run" || { bad "B: 'work' pod never reached Running"; return; }

  local pod
  pod="$(kubectl get pods -n leoflow --no-headers 2>/dev/null | awk '/chaosdag-work-.*Running/{print $1; exit}')"
  [ -n "$pod" ] || { bad "B: could not find the running work pod"; return; }

  # Arm the agent-lost backstop before the kill. That reaper only fires once a TI
  # has heartbeated at least once (the zero-heartbeat "do no harm" guard, ADR
  # 0031); the agent heartbeats every 15s (cmd/dexaflow-agent HeartbeatInterval).
  # Killing the pod inside that first interval hits a separate, un-backstopped
  # window (the reconciler is blind to a *deleted* pod, and agent-lost skips a
  # null heartbeat) tracked in #527 — NOT what this scenario asserts. Wait one
  # interval + margin so we exercise the documented mid-run-kill → agent-lost path.
  log "B: waiting one heartbeat interval (15s) so the agent-lost backstop is armed (#527)"
  sleep 20

  log "B: force-deleting $pod mid-execution"
  kubectl delete pod -n leoflow "$pod" --grace-period=0 --force >/dev/null 2>&1 || true

  # The agent-lost reaper must move the TI off `running` (to failed / retry),
  # within the agent-lost window + margin — it must NOT stay `running` forever.
  local deadline st; deadline=$(( $(date +%s) + 180 ))
  while [ "$(date +%s)" -lt "$deadline" ]; do
    st="$(task_states "$run" | head -1)"
    if [ -n "$st" ] && [ "$st" != "running" ] && [ "$st" != "queued" ] && [ "$st" != "null" ]; then
      ok "B: TI left 'running' after the pod was killed (state=$st) — no stuck task"
      break
    fi
    sleep 5
  done
  [ "$(date +%s)" -lt "$deadline" ] || { bad "B: TI stuck (never left running/queued) 180s after the pod was killed"; return; }

  # No orphaned pod for that killed attempt should linger (reaper teardown, #518).
  sleep 5
  if kubectl get pod -n leoflow "$pod" >/dev/null 2>&1; then
    bad "B: the killed pod's name still exists — reaper did not tear it down"
  else
    ok "B: no orphaned pod lingering after reap"
  fi
}

# ─────────────────────────────────────────────────────────────────────────────
# Scenario C — durable outcome recovery (ADR 0052)
# ─────────────────────────────────────────────────────────────────────────────
setup_recoverdag() {
  log "C-setup: scaffolding recoverdag (succeeds, but the agent dies mid-report)"
  "$ROOT/bin/leoflow" init "$WORKDIR/$RECOVER_DAG_ID" >/dev/null
  cat >> "$WORKDIR/$RECOVER_DAG_ID/dexaflow.yaml" <<YAML
build:
  platforms:
    - ${HOST_PLATFORM}
YAML
  cat > "$WORKDIR/$RECOVER_DAG_ID/dag.py" <<'PY'
"""recoverdag — the task succeeds; the agent is told to die mid-report, so the
reconciler must recover the success from the durable outcome record (ADR 0052)."""
from __future__ import annotations
from airflow.sdk import DAG, task


@task
def quick() -> str:
    print("recover: task body ran to success", flush=True)
    return "ok"


with DAG("recoverdag", schedule=None, catchup=False, tags=["chaos"]):
    quick()
PY
  # The fault seam rides in the IMAGE, not in dexaflow.yaml's task env: dispatch
  # strips every LEOFLOW_-prefixed key an author declares (#828/#829, so a DAG
  # cannot redirect the agent's credential exchange), and the agent reads this
  # one with os.Getenv at startup. A task-env seam is silently dropped — which is
  # how this scenario quietly lost its fault injection when that fix landed. The
  # image's own ENV survives, because nothing in the pod spec overrides it.
  cat > "$WORKDIR/$RECOVER_DAG_ID/Dockerfile" <<DOCKER
FROM ${BASE_IMAGE}
COPY dag.py /home/leoflow/dag.py
ENV PYTHONPATH=/home/leoflow
ENV LEOFLOW_CHAOS_DIE_BEFORE_REPORT=TASK_STATE_SUCCESS
DOCKER
  "$ROOT/bin/leoflow" compile "$WORKDIR/$RECOVER_DAG_ID" --image "$RECOVER_DAG_IMAGE" --build --dockerfile Dockerfile -o "$WORKDIR/$RECOVER_DAG_ID/dag.json" >/dev/null
  k3d_import "$CLUSTER" "$RECOVER_DAG_IMAGE" || fatal "C: k3d import of recoverdag failed"
  deregister_dag "$RECOVER_DAG_ID"
  "$ROOT/bin/leoflow" push "$WORKDIR/$RECOVER_DAG_ID/dag.json" --server "$API" --token "$TOKEN" >/dev/null \
    || fatal "C: pushing $RECOVER_DAG_ID failed"
}

recover_run_state() {  # $1=run
  curl -fsS --max-time 10 -H "Authorization: Bearer $TOKEN" \
    "$API/api/v2/dags/$RECOVER_DAG_ID/dagRuns/$1" 2>/dev/null | jq -r '.state // "unknown"'
}

scenario_durable_outcome_recovery() {
  log "SCENARIO C — pod killed mid-report after writing the outcome record; reconciler recovers the SUCCESS (ADR 0052)"
  setup_recoverdag

  local run
  run="$(curl -fsS --max-time 10 -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
    -d '{}' "$API/api/v2/dags/$RECOVER_DAG_ID/dagRuns" | jq -r '.dag_run_id')"
  [ -n "$run" ] && [ "$run" != "null" ] || { bad "C: no run id"; return; }

  # The task pod MUST end non-successfully at the k8s level (agent exit 137) — the
  # whole point is that pod phase says failure while the record says success. This
  # is fail-closed: if the pod ends `Completed` (exit 0) the fault-injection seam
  # did NOT fire (e.g. a stale agent image without the seam), so the recovery path
  # was never exercised — a false green — and the scenario must fail loudly rather
  # than let the run reach success via the ordinary report path.
  local deadline status=""; deadline=$(( $(date +%s) + 120 ))
  while [ "$(date +%s)" -lt "$deadline" ]; do
    status="$(kubectl get pods -n leoflow --no-headers 2>/dev/null | awk '/recoverdag-quick-/{print $3; exit}')"
    case "$status" in
      Error|Failed) break ;;
      Completed|Succeeded)
        bad "C: recoverdag pod ended '$status' (exit 0) — the fault-injection seam did NOT fire, so recovery was never exercised (stale agent image?). Refusing to pass on the ordinary report path."
        return ;;
    esac
    sleep 3
  done
  case "$status" in
    Error|Failed) ok "C: fault injected — the pod ended '$status' (agent exited mid-report, record already written)" ;;
    *) bad "C: recoverdag pod never reached a terminal failed state (status='$status') — cannot confirm the fault fired"; return ;;
  esac

  # The reconciler (30s sweep) must settle the TI SUCCEEDED from the durable record,
  # winning the race against the agent-lost reaper (90s, and disarmed here: a sub-15s
  # task never heartbeats, so the zero-heartbeat guard skips it). Assert the run
  # reaches success and does NOT fail.
  deadline=$(( $(date +%s) + 150 )); local state=""
  while [ "$(date +%s)" -lt "$deadline" ]; do
    state="$(recover_run_state "$run")"
    [ "$state" = success ] && break
    [ "$state" = failed ] && { bad "C: run FAILED — the reconciler did not recover the success from the durable record"; return; }
    sleep 5
  done
  if [ "$state" = success ]; then
    ok "C: run recovered to SUCCESS from the durable outcome record, despite the Failed pod and the lost report"
  else
    bad "C: run did not reach success within the recovery window (state=$state)"
    return
  fi

  # Recovery is a SETTLE, not a retry: the durable record settles the current
  # attempt succeeded (try_number unchanged), so there is exactly one attempt.
  local tries
  tries="$(curl -fsS --max-time 10 -H "Authorization: Bearer $TOKEN" \
    "$API/api/v2/dags/$RECOVER_DAG_ID/dagRuns/$run/taskInstances" 2>/dev/null \
    | jq -r '[.task_instances[].try_number] | max')"
  if [ "$tries" = "1" ]; then
    ok "C: recovery settled the current attempt (try_number=1) — no wasteful retry of already-done work"
  else
    bad "C: expected a single attempt (recovery is a settle, not a retry), got max try_number=$tries"
  fi
}

# ─────────────────────────────────────────────────────────────────────────────
# Scenario D — a control-plane outage that spans a task's termination
#
# The field shape (EKS Auto Mode, leoflow 0.4.4): the control plane is evicted
# while tasks run; their pods finish DURING the outage and no report can land;
# the restarted leader then sees task instances that are `running` with stale
# heartbeats and pods that are gone-or-terminal — signals the outage itself
# manufactured — and a reaper that judges them before the reconciler has read
# their durable outcome records turns a SUCCEEDED task into a failed one and the
# run unrecoverable.
#
# Scenario C proves the recovery path with the control plane UP. This proves the
# ORDERING under an outage, which is what actually broke: the leader-settling
# gate must hold every reaper while the reconciler settles all three attempts
# from their records, and the run must reach success WITHOUT any reap and
# WITHOUT a retry. Three parallel tasks, not one, because the field failure was
# a fleet-wide mass-reap, not a single unlucky attempt.
# ─────────────────────────────────────────────────────────────────────────────

# decision_count reports how many times the RESTARTED scheduler recorded a given
# scheduler decision, or 0 when the counter has no sample yet (a Prometheus
# counter with labels is absent until first incremented, which is itself the
# assertion "this never happened"). Reading the fresh process is deliberate: the
# restart resets the counters, so every sample below belongs to the recovery
# window and nothing from scenarios A-C can leak into it.
# decision_from reads one decision's count out of an already-scraped metrics
# body, so the four assertions below share one snapshot and cannot skew between
# scrapes. A counter with labels is absent until first incremented, which is
# itself the assertion "this never happened".
decision_from() {
  printf '%s\n' "$1" \
    | awk -v d="$2" '$0 ~ "^leoflow_scheduler_decisions_total\\{decision_type=\"" d "\"\\}" {print $2; exit}' \
    | awk 'NF{print; found=1} END{if(!found) print 0}'
}

decision_count() {
  local want="$1" line
  line="$(curl -fsS --max-time 10 "http://localhost:${SCHED_METRICS_PORT}/metrics" 2>/dev/null \
    | awk -v d="$want" '$0 ~ "^leoflow_scheduler_decisions_total\\{decision_type=\"" d "\"\\}" {print $2; exit}')"
  printf '%s' "${line:-0}"
}

# outage_states prints one state per task instance of the outage run.
outage_states() {
  curl -fsS --max-time 10 -H "Authorization: Bearer $TOKEN" \
    "$API/api/v2/dags/$OUTAGE_DAG_ID/dagRuns/$1/taskInstances" 2>/dev/null \
    | jq -r '.task_instances[].state'
}

outage_run_state() {
  curl -fsS --max-time 10 -H "Authorization: Bearer $TOKEN" \
    "$API/api/v2/dags/$OUTAGE_DAG_ID/dagRuns/$1" 2>/dev/null | jq -r '.state // "unknown"'
}

setup_outagedag() {
  log "D-setup: scaffolding outagedag (three parallel tasks; each agent dies after writing its record)"
  "$ROOT/bin/leoflow" init "$WORKDIR/$OUTAGE_DAG_ID" >/dev/null
  cat >> "$WORKDIR/$OUTAGE_DAG_ID/dexaflow.yaml" <<YAML
build:
  platforms:
    - ${HOST_PLATFORM}
YAML
  cat > "$WORKDIR/$OUTAGE_DAG_ID/dag.py" <<'PY'
"""outagedag — three independent tasks, each of which succeeds and then loses its
report because the control plane is down.

The sleep is sized so every agent emits at least one heartbeat before it
terminates: a task instance that never heartbeat is skipped by the agent-lost
reaper's zero-heartbeat guard, and this scenario is worthless unless that reaper
is armed against all three.

NOTHING IS RETURNED, deliberately. The agent pushes a task's return value to the
control plane BEFORE it writes the durable outcome record, and that push has no
deadline of its own, so with the control plane down an agent with a return value
blocks in the gRPC reconnect and its pod stays Running through the whole outage —
which is correct behaviour (the value is not lost) but the opposite of what this
scenario needs: a pod that TERMINATES inside the outage, leaving a record and no
report. Give a task a return value here and the scenario times out waiting for a
termination that cannot happen."""
from __future__ import annotations
import time
from airflow.sdk import DAG, task


def _work(name: str) -> None:
    print(f"outage: {name} START", flush=True)
    time.sleep(50)
    print(f"outage: {name} DONE", flush=True)


@task
def alpha() -> None:
    _work("alpha")


@task
def bravo() -> None:
    _work("bravo")


@task
def charlie() -> None:
    _work("charlie")


with DAG("outagedag", schedule=None, catchup=False, tags=["chaos"]):
    alpha()
    bravo()
    charlie()
PY
  # The seam rides in the image; see setup_recoverdag for why a task-env seam is
  # stripped before it reaches the pod.
  cat > "$WORKDIR/$OUTAGE_DAG_ID/Dockerfile" <<DOCKER
FROM ${BASE_IMAGE}
COPY dag.py /home/leoflow/dag.py
ENV PYTHONPATH=/home/leoflow
ENV LEOFLOW_CHAOS_DIE_BEFORE_REPORT=TASK_STATE_SUCCESS
DOCKER
  "$ROOT/bin/leoflow" compile "$WORKDIR/$OUTAGE_DAG_ID" --image "$OUTAGE_DAG_IMAGE" --build --dockerfile Dockerfile -o "$WORKDIR/$OUTAGE_DAG_ID/dag.json" >/dev/null
  k3d_import "$CLUSTER" "$OUTAGE_DAG_IMAGE" || fatal "D: k3d import of outagedag failed"
  deregister_dag "$OUTAGE_DAG_ID"
  "$ROOT/bin/leoflow" push "$WORKDIR/$OUTAGE_DAG_ID/dag.json" --server "$API" --token "$TOKEN" >/dev/null \
    || fatal "D: pushing $OUTAGE_DAG_ID failed"
}

scenario_outage_recovery() {
  log "SCENARIO D — control plane down across three tasks' termination; the restarted leader recovers all three without reaping"
  setup_outagedag

  # Nothing else may be in flight: the counter assertions below say "no reaper
  # fired at all", so a leftover running attempt from an earlier scenario could
  # be reaped inside the recovery window and read as this scenario's failure.
  local leftover
  leftover="$(kubectl get pods -n leoflow --no-headers 2>/dev/null | grep -c 'Running' || true)"
  [ "${leftover:-0}" -eq 0 ] || log "D: note — $leftover task pod(s) still Running from an earlier scenario; a reaper decision below may not be ours"

  local run
  run="$(curl -fsS --max-time 10 -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
    -d '{}' "$API/api/v2/dags/$OUTAGE_DAG_ID/dagRuns" | jq -r '.dag_run_id')"
  [ -n "$run" ] && [ "$run" != "null" ] || { bad "D: no run id"; return; }

  # All three must be Running before the kill, or the outage lands on dispatch
  # (a different, already-covered failure) instead of on the report.
  # Count DISTINCT tasks, not lines: three pods of one task would otherwise
  # satisfy a line count and the scenario would proceed on a single attempt.
  local deadline running=0
  deadline=$(( $(date +%s) + 120 ))
  while [ "$(date +%s)" -lt "$deadline" ]; do
    running="$(kubectl get pods -n leoflow --no-headers 2>/dev/null \
      | awk '/outagedag-(alpha|bravo|charlie)-.*Running/ {
              match($1, /outagedag-(alpha|bravo|charlie)/); print substr($1, RSTART, RLENGTH) }' \
      | sort -u | wc -l | tr -d ' ')"
    [ "${running:-0}" -ge 3 ] && break
    sleep 3
  done
  [ "${running:-0}" -ge 3 ] || { bad "D: only ${running:-0}/3 outagedag tasks reached Running before the kill window"; dump_pods; return; }
  ok "D: all three task pods are Running"

  # One heartbeat interval (15s) plus margin, so every attempt has a non-null
  # last_heartbeat and the agent-lost reaper is armed against all three. Without
  # this the scenario would pass on the zero-heartbeat guard, not on the gate.
  log "D: letting every agent heartbeat once before the outage"
  sleep 20

  log "D: killing the scheduler (SIGKILL — an eviction gives no graceful stop)"
  kill -9 "$SCHED_PID" 2>/dev/null; wait "$SCHED_PID" 2>/dev/null; SCHED_PID=""

  # The tasks must now finish INSIDE the outage. Each agent writes its durable
  # outcome record and exits without delivering the report, so the pods end in a
  # failed phase while their records say success. A pod that ends Completed
  # (exit 0) means the fault-injection seam did not fire, so the recovery path
  # was never exercised — fail loudly rather than pass on the ordinary path.
  local terminal=0
  deadline=$(( $(date +%s) + 150 ))
  while [ "$(date +%s)" -lt "$deadline" ]; do
    if kubectl get pods -n leoflow --no-headers 2>/dev/null | grep -qE 'outagedag-.*(Completed|Succeeded)'; then
      bad "D: an outagedag pod ended Completed/Succeeded (exit 0) — the fault-injection seam did NOT fire. Either the base image predates the seam, or the ENV is not reaching the agent (it must come from the DAG image: dispatch strips LEOFLOW_-prefixed task env). Refusing to pass on the ordinary report path."
      dump_pods; return
    fi
    terminal="$(kubectl get pods -n leoflow --no-headers 2>/dev/null | grep -cE 'outagedag-(alpha|bravo|charlie)-.*(Error|Failed)' || true)"
    [ "${terminal:-0}" -ge 3 ] && break
    sleep 3
  done
  if [ "${terminal:-0}" -lt 3 ]; then
    bad "D: only ${terminal:-0}/3 pods terminated during the outage. A pod still Running means its agent is blocked on a control-plane call BEFORE the durable outcome record — the agent log below names the last thing it tried"
    dump_pods
    # The agent's own last lines are the diagnosis: everything between the user
    # process exiting and BeforeReport is a control-plane round trip (the log
    # sink's Close, the return-value push, extra links, custom XComs), and any
    # one of them blocking keeps the pod alive through the outage.
    for pod in $(kubectl get pods -n leoflow --no-headers -o custom-columns=:metadata.name 2>/dev/null | grep 'outagedag-'); do
      printf '\n---- agent log: %s ----\n' "$pod" >&2
      kubectl logs -n leoflow "$pod" --tail=25 >&2 2>&1 || true
    done
    return
  fi
  ok "D: all three pods terminated during the outage, records written, no report delivered"

  # Fail-closed: with the scheduler dead nothing may have settled these attempts.
  # A task instance already `success` here means the control plane was reachable
  # when it mattered, so the scenario proves nothing about the recovery ordering.
  # Fail CLOSED on the read itself: an api hiccup used to print nothing here, so
  # the "no success" test was vacuously true and the precondition unverified
  # inside an otherwise green run. Require all three attempts, each in a
  # not-yet-settled state.
  local states_raw states_before unsettled
  states_raw="$(outage_states "$run")"
  states_before="$(printf '%s\n' "$states_raw" | sort | uniq -c | tr '\n' ' ')"
  unsettled="$(printf '%s\n' "$states_raw" | grep -cE '^(running|queued|scheduled)$' || true)"
  if [ "$(printf '%s\n' "$states_raw" | grep -c .)" -ne 3 ] || [ "${unsettled:-0}" -ne 3 ]; then
    bad "D: could not confirm all three attempts are unsettled during the outage (states: ${states_before:-<read failed>}) — the precondition must be READ, not assumed"
    return
  fi
  ok "D: all three attempts are still unsettled during the outage (states: $states_before)"

  log "D: restarting the scheduler; the new leader must recover all three before any reaper may act"
  local restart_epoch; restart_epoch=$(date +%s)
  start_scheduler

  # The reconciler runs in the maintenance loop's reconcile phase (30s cadence),
  # while every reaper stays shut for the settling grace (180s from leadership).
  # So the recovery must land in the FIRST cycles, well inside the grace.
  #
  # The wait MUST outlast the grace it is compared against. It used to be 150s,
  # shorter than the 180s grace, so a success could only ever be measured below
  # it and the comparison below could not fail — a recovery that truly took 200s
  # was reported as "did not reach success", never as "past the grace".
  local settling_grace=180 state="" elapsed=0
  deadline=$(( restart_epoch + 2 * settling_grace ))
  while [ "$(date +%s)" -lt "$deadline" ]; do
    state="$(outage_run_state "$run")"
    [ "$state" = success ] && break
    if [ "$state" = failed ]; then
      bad "D: run FAILED after the outage — something settled an attempt as failed instead of recovering its record"
      outage_states "$run" | sort | uniq -c >&2
      return
    fi
    sleep 5
  done
  elapsed=$(( $(date +%s) - restart_epoch ))
  if [ "$state" != success ]; then
    bad "D: run did not reach success within $(( 2 * settling_grace ))s of the restart (state=$state, states: $(outage_states "$run" | sort | uniq -c | tr '\n' ' '))"
    return
  fi
  ok "D: run recovered to SUCCESS ${elapsed}s after the restart, all three attempts settled from their durable records"

  if [ "$elapsed" -lt "$settling_grace" ]; then
    ok "D: recovery (${elapsed}s) beat the settling grace (${settling_grace}s) — the reconciler settled the attempts before any reaper was even allowed to judge them"
  else
    bad "D: recovery took ${elapsed}s, at or past the ${settling_grace}s settling grace — the gate, not the ordering, is what saved the run; the margin the design relies on is gone"
  fi

  # Recovery is a settle, not a retry: three attempts, one try each.
  local tries
  tries="$(curl -fsS --max-time 10 -H "Authorization: Bearer $TOKEN" \
    "$API/api/v2/dags/$OUTAGE_DAG_ID/dagRuns/$run/taskInstances" 2>/dev/null \
    | jq -r '[.task_instances[].try_number] | max')"
  if [ "$tries" = "1" ]; then
    ok "D: every attempt settled in place (max try_number=1) — no task re-ran work it had already finished"
  else
    bad "D: expected one attempt per task (recovery is a settle, not a retry), got max try_number=$tries"
  fi

  # What the skip proves, stated honestly. It is recorded once per maintenance
  # tick BEFORE any candidate is listed, and the first tick lands one
  # reconcileInterval (30s) after boot — by which time that same cycle's
  # reconcile has already settled all three from their records. So the reap pass
  # was held while the gate was shut, but the gate had nothing left to hold: what
  # this scenario demonstrates is the CYCLE ORDERING (reconcile before reap),
  # not the gate refusing a real candidate. Build the same tree with
  # SettlingGrace 0 and every behavioural assertion here still passes; only this
  # instrument goes red. Proving the gate itself needs a scenario that costs the
  # reconciler a cycle, so a candidate still exists at reap time.
  #
  # It is still worth asserting: zero would mean the maintenance loop never ran
  # inside the grace at all, which would make the ordering claim unverified.
  local metrics skips valve lost_pod lost_agent
  metrics="$(curl -fsS --max-time 10 "http://localhost:${SCHED_METRICS_PORT}/metrics" 2>/dev/null)"
  case "$metrics" in
    *leoflow_scheduler_decisions_total*) : ;;
    *) bad "D: could not scrape the restarted scheduler's decisions — every counter assertion below would pass vacuously"; return ;;
  esac
  skips="$(decision_from "$metrics" reap_settling_skip)"
  valve="$(decision_from "$metrics" reap_settling_valve_open)"
  lost_pod="$(decision_from "$metrics" pod_lost)"
  lost_agent="$(decision_from "$metrics" agent_lost)"
  if [ "${skips%.*}" -ge 1 ] 2>/dev/null; then
    ok "D: the reap pass ran inside the grace and was held (reap_settling_skip=$skips) — the cycle ordering is what settled the attempts"
  else
    bad "D: no reap_settling_skip recorded — the maintenance loop never ran inside the grace, so the ordering claim is unverified (false green)"
  fi
  if [ "${valve%.*}" = "0" ]; then
    ok "D: the liveness valve never opened (reap_settling_valve_open=0)"
  else
    bad "D: the liveness valve opened ($valve) — the reconciler sweep or the informer was broken during recovery"
  fi
  if [ "${lost_pod%.*}" = "0" ] && [ "${lost_agent%.*}" = "0" ]; then
    ok "D: no reaper fired at all during recovery (pod_lost=0, agent_lost=0) — the field failure cannot reproduce"
  else
    bad "D: a reaper fired during recovery (pod_lost=$lost_pod, agent_lost=$lost_agent) — this is the field failure; check whether the decision belongs to this run"
    kubectl get pods -n leoflow -o wide >&2 2>&1 || true
  fi
}

# run_scenario runs one scenario unless LEOFLOW_CHAOS_ONLY names a subset that
# excludes its letter. The shared setup above is unconditional, so a single
# scenario can be iterated on without paying for the others' sleeps.
ONLY="${LEOFLOW_CHAOS_ONLY:-}"
run_scenario() {
  local letter="$1"; shift
  case "$ONLY" in
    "") "$@" ;;
    *"$letter"*) "$@" ;;
    *) log "skipping scenario $letter (LEOFLOW_CHAOS_ONLY=$ONLY)" ;;
  esac
}

run_scenario A scenario_scheduler_crash
run_scenario B scenario_task_pod_kill
run_scenario C scenario_durable_outcome_recovery
run_scenario D scenario_outage_recovery

printf '\n\033[1m===== chaos-runtime summary =====\033[0m\n'
if [ "$FAILED" -ne 0 ]; then
  printf '\033[1;31mchaos-runtime: FAILED — a fault-injection invariant broke (see above).\033[0m\n'; exit 1
fi
printf '\033[1;32mchaos-runtime: all fault-injection invariants held.\033[0m\n'
