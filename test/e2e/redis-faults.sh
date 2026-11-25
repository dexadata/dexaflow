#!/usr/bin/env bash
#
# Redis fault injection on a running DAG (#2307), on a real k3d cluster.
#
# Redis holds the XCom values of a Cluster install (operate/redis.md). This
# script runs a DAG that passes a value from `produce` to `consume`, with a
# `hold` task in between, and breaks Redis while `hold` runs, after the value
# was written and before it is read:
#
#   R1) restart with persistence (AOF, appendfsync everysec): the value
#       survives, the control plane reconnects on its own, the run succeeds.
#   R2) managed failover: a replica is promoted and the endpoint the control
#       plane uses is moved to it (a TCP proxy stands in for the provider's
#       primary endpoint). The control plane is not restarted, the run
#       succeeds with the replicated value, and the error window is measured.
#   R3) flush (the same as an instance replaced without its data): the value
#       is gone. The run must FAIL, with a reason on `consume`, never succeed
#       with a wrong or empty value; a run started afterwards succeeds.
#
# Each scenario prints a RESULT line, and the summary is written to
# $REDIS_FAULTS_RESULTS (markdown) when set: operate/redis.md cites those
# measured outcomes (#2378).
#
# The control plane runs on the host as one process (both roles) against its
# default Postgres (LEOFLOW_DATABASE_URL overrides it; the database must be
# migrated) and a Redis this script owns: a primary
# with AOF, a replica, and the proxy, all on one Docker network. Images come
# from Docker Hub's official library (redis, haproxy), through mirror.gcr.io
# on CI.
#
# NOT a `go test`; destructive; runs weekly in .github/workflows/e2e-redis-faults.yaml
# and on pull requests that change it. Select scenarios with REDIS_FAULTS_ONLY
# (for example "R3" or "R1R2").
#
# Usage: test/e2e/redis-faults.sh [cluster-name]
set -uo pipefail

# shellcheck source=test/e2e/lib.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

CLUSTER="${1:-dexaflow-redis-faults}"
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
WORKDIR="$(mktemp -d)"
PY_VERSION="3.11"
BASE_IMAGE="leoflow-base:py${PY_VERSION}"
DAG_IMAGE="dexaflow-redis-faults-dag:dev"
DAG_ID="redisfaultdag"
REDIS_IMAGE="${REDIS_FAULTS_REDIS_IMAGE:-redis:7}"
PROXY_IMAGE="${REDIS_FAULTS_PROXY_IMAGE:-haproxy:3.0-alpine}"
NET="dexaflow-redis-faults"
PRIMARY="dexaflow-redis-a"
REPLICA="dexaflow-redis-b"
PROXY="dexaflow-redis-proxy"
REDIS_PORT="${REDIS_FAULTS_PORT:-6390}"
HTTP_PORT="${LEOFLOW_E2E_HTTP_PORT:-18090}"
METRICS_PORT="19090"
GRPC_PORT="9091"
API="http://localhost:${HTTP_PORT}"
HOST_ADDR="${LEOFLOW_E2E_HOST_ADDR:-$([ "$(uname -s)" = Linux ] && echo host.k3d.internal || echo host.docker.internal)}"
# HOLD_SECONDS is how long `hold` runs: the window each fault lands in.
HOLD_SECONDS="${REDIS_FAULTS_HOLD_SECONDS:-45}"
RUN_TIMEOUT=300
SERVER_PID=""; TOKEN=""; FAILED=0
RESULTS=()

log()  { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
ok()   { printf '\033[1;32m    PASS\033[0m %s\n' "$*"; }
bad()  { printf '\033[1;31m    FAIL\033[0m %s\n' "$*" >&2; FAILED=1; }
fatal() { printf '\033[1;31mFATAL:\033[0m %s\n' "$*" >&2; exit 1; }
result() { printf '\033[1;35m  RESULT\033[0m %s\n' "$*"; RESULTS+=("$*"); }

cleanup() {
  [ -n "$SERVER_PID" ] && kill "$SERVER_PID" 2>/dev/null || true
  if [ -n "${REDIS_FAULTS_LOG_DIR:-}" ]; then
    mkdir -p "$REDIS_FAULTS_LOG_DIR"
    cp "$WORKDIR"/*.log "$REDIS_FAULTS_LOG_DIR"/ 2>/dev/null || true
    kubectl get pods -n leoflow -o wide >"$REDIS_FAULTS_LOG_DIR/pods.txt" 2>&1 || true
  fi
  docker rm -f "$PROXY" "$PRIMARY" "$REPLICA" >/dev/null 2>&1 || true
  docker network rm "$NET" >/dev/null 2>&1 || true
  k3d cluster delete "$CLUSTER" >/dev/null 2>&1 || true
  rm -rf "$WORKDIR"
}
trap cleanup EXIT

for tool in k3d kubectl docker jq curl; do
  command -v "$tool" >/dev/null || fatal "missing required tool: $tool"
done

want() { [ -z "${REDIS_FAULTS_ONLY:-}" ] || [[ "$REDIS_FAULTS_ONLY" == *"$1"* ]]; }

# ── Redis: primary (AOF), replica, and the proxy that is "the endpoint" ──────
rcli() {  # <container> <args...>: redis-cli inside a Redis container
  local c="$1"; shift
  docker exec "$c" redis-cli "$@"
}

# start_proxy points the endpoint (localhost:$REDIS_PORT) at <container>. The
# old proxy goes first, so every open connection breaks, as it does when a
# provider moves its primary endpoint.
start_proxy() {
  local backend="$1"
  docker rm -f "$PROXY" >/dev/null 2>&1 || true
  cat >"$WORKDIR/haproxy.cfg" <<CFG
defaults
  mode tcp
  timeout connect 2s
  timeout client 1h
  timeout server 1h
frontend redis
  bind :6379
  default_backend primary
backend primary
  server primary ${backend}:6379
CFG
  docker run -d --name "$PROXY" --network "$NET" -p "127.0.0.1:${REDIS_PORT}:6379" \
    -v "$WORKDIR/haproxy.cfg:/usr/local/etc/haproxy/haproxy.cfg:ro" "$PROXY_IMAGE" >/dev/null \
    || fatal "could not start the Redis proxy"
}

# endpoint_ping answers PONG when the endpoint reaches a Redis.
endpoint_ping() {
  docker run --rm --network host "$REDIS_IMAGE" redis-cli -h 127.0.0.1 -p "$REDIS_PORT" ping 2>/dev/null
}

wait_endpoint() {
  local deadline; deadline=$(( $(date +%s) + ${1:-30} ))
  while [ "$(date +%s)" -lt "$deadline" ]; do
    [ "$(endpoint_ping)" = PONG ] && return 0
    sleep 1
  done
  return 1
}

start_redis() {
  docker network create "$NET" >/dev/null 2>&1 || true
  docker rm -f "$PROXY" "$PRIMARY" "$REPLICA" >/dev/null 2>&1 || true
  # The settings operate/redis.md requires: noeviction, AOF every second.
  docker run -d --name "$PRIMARY" --network "$NET" "$REDIS_IMAGE" \
    redis-server --appendonly yes --appendfsync everysec --maxmemory-policy noeviction >/dev/null \
    || fatal "could not start the Redis primary"
  docker run -d --name "$REPLICA" --network "$NET" "$REDIS_IMAGE" \
    redis-server --appendonly yes --appendfsync everysec --maxmemory-policy noeviction \
    --replicaof "$PRIMARY" 6379 >/dev/null \
    || fatal "could not start the Redis replica"
  start_proxy "$PRIMARY"
  wait_endpoint 60 || fatal "the Redis endpoint never answered PONG on localhost:${REDIS_PORT}"
}

# ── DAG ─────────────────────────────────────────────────────────────────────
log "Scaffolding the DAG (produce -> hold -> consume, the value crosses the fault)"
"$ROOT/bin/leoflow" init "$WORKDIR/$DAG_ID" >/dev/null
case "$(uname -m)" in arm64|aarch64) HOST_PLATFORM="linux/arm64" ;; *) HOST_PLATFORM="linux/amd64" ;; esac
cat >>"$WORKDIR/$DAG_ID/dexaflow.yaml" <<YAML
build:
  platforms:
    - ${HOST_PLATFORM}
YAML
cat >"$WORKDIR/$DAG_ID/dag.py" <<PY
"""redisfaultdag: an XCom value written before a Redis fault, read after it."""
from __future__ import annotations
import time
import uuid
from airflow.sdk import DAG, task


@task(retries=0)
def produce() -> str:
    value = "redis-faults-" + uuid.uuid4().hex
    print(f"redis-faults: produced {value}", flush=True)
    return value


@task(retries=0)
def hold() -> None:
    # The fault lands here: after produce stored its value, before consume reads it.
    print("redis-faults: hold START", flush=True)
    time.sleep(${HOLD_SECONDS})
    print("redis-faults: hold DONE", flush=True)


@task(retries=0)
def consume(value: str) -> str:
    if not isinstance(value, str) or not value.startswith("redis-faults-"):
        raise ValueError(f"consume received a wrong value: {value!r}")
    print(f"redis-faults: consumed {value}", flush=True)
    return value


with DAG("${DAG_ID}", schedule=None, catchup=False, tags=["e2e"]):
    v = produce()
    h = hold()
    c = consume(v)
    v >> h >> c
PY
cat >"$WORKDIR/$DAG_ID/Dockerfile" <<DOCKER
FROM ${BASE_IMAGE}
COPY dag.py /home/leoflow/dag.py
ENV PYTHONPATH=/home/leoflow
DOCKER

log "Building the runtime image, the cluster and the Redis endpoint"
docker build --provenance=false -f "$ROOT/runtime/Dockerfile" --build-arg "PYTHON_VERSION=${PY_VERSION}" -t "$BASE_IMAGE" "$ROOT" \
  || fatal "runtime image build failed"
start_redis
k3d cluster create "$CLUSTER" --wait || fatal "k3d cluster create failed"
kubectl create namespace leoflow >/dev/null
k3d_wait_host_entry 60 || fatal "the cluster cannot resolve host.k3d.internal"

log "Starting the control plane (one process, both roles) on redis://localhost:${REDIS_PORT}"
export LEOFLOW_AUTH_JWT_SECRET="redis-faults-secret"
export LEOFLOW_BOOTSTRAP_PASSWORD="admin"
export LEOFLOW_SECRET_KEY="redis-faults-secret-key-32bytes!"
export LEOFLOW_AGENT_ALLOW_INSECURE_SECRETS=true
export LEOFLOW_REDIS_URL="redis://localhost:${REDIS_PORT}/0"
export LEOFLOW_LOGS_DIR="${WORKDIR}/logs"; mkdir -p "$LEOFLOW_LOGS_DIR"
LEOFLOW_SERVER_HTTP_ADDR="0.0.0.0:${HTTP_PORT}" \
LEOFLOW_SERVER_METRICS_ADDR="0.0.0.0:${METRICS_PORT}" \
LEOFLOW_SERVER_GRPC_ADDR="0.0.0.0:${GRPC_PORT}" \
LEOFLOW_EXECUTOR_AGENT_CONTROL_PLANE_ADDR="${HOST_ADDR}:${GRPC_PORT}" \
  "$ROOT/bin/leoflow-server" >"${WORKDIR}/server.log" 2>&1 &
SERVER_PID=$!

deadline=$(( $(date +%s) + 60 ))
until curl -fsS --max-time 5 "${API}/healthz" >/dev/null 2>&1; do
  kill -0 "$SERVER_PID" 2>/dev/null || { tail -30 "${WORKDIR}/server.log" >&2; fatal "the control plane exited at boot"; }
  [ "$(date +%s)" -lt "$deadline" ] || { tail -30 "${WORKDIR}/server.log" >&2; fatal "the control plane never served /healthz"; }
  sleep 1
done

TOKEN="$(admin_token "$ROOT/bin/leoflow" "$API" admin)" || fatal "could not mint an admin token"
[ -n "$TOKEN" ] || fatal "empty admin token"

log "Compiling, importing and registering the DAG"
"$ROOT/bin/leoflow" compile "$WORKDIR/$DAG_ID" --image "$DAG_IMAGE" --build --dockerfile Dockerfile \
  -o "$WORKDIR/$DAG_ID/dag.json" >/dev/null || fatal "compile failed"
k3d_import "$CLUSTER" "$BASE_IMAGE" "$DAG_IMAGE" || fatal "k3d import failed"
"$ROOT/bin/leoflow" dags delete "$DAG_ID" --deregister --server "$API" --token "$TOKEN" >/dev/null 2>&1 || true
"$ROOT/bin/leoflow" push "$WORKDIR/$DAG_ID/dag.json" --server "$API" --token "$TOKEN" >/dev/null \
  || fatal "pushing $DAG_ID failed"
unpause_dag "$API" "$DAG_ID" "$TOKEN" || fatal "unpausing $DAG_ID failed"

# ── API helpers ─────────────────────────────────────────────────────────────
api_get() { curl -fsS --max-time 10 -H "Authorization: Bearer $TOKEN" "$API$1" 2>/dev/null; }

trigger_run() {
  curl -fsS --max-time 10 -X POST -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
    -d '{}' "$API/api/v2/dags/$DAG_ID/dagRuns" | jq -r '.dag_run_id'
}

ti_state() {  # <run> <task>
  api_get "/api/v2/dags/$DAG_ID/dagRuns/$1/taskInstances" |
    jq -r --arg t "$2" '.task_instances[] | select(.task_id == $t) | .state // "none"'
}

ti_reason() {  # <run> <task>
  api_get "/api/v2/dags/$DAG_ID/dagRuns/$1/taskInstances" |
    jq -r --arg t "$2" '.task_instances[] | select(.task_id == $t) | .failure_reason // ""'
}

run_state() { api_get "/api/v2/dags/$DAG_ID/dagRuns/$1" | jq -r '.state // "unknown"'; }

xcom_value() {  # <run> <task>: the return value as the API serves it
  api_get "/api/v2/dags/$DAG_ID/dagRuns/$1/taskInstances/$2/xcomEntries/return_value" | jq -c '.value'
}

# wait_hold_running returns once produce succeeded and hold is running, which
# is when the value sits in Redis and nothing has read it yet.
wait_hold_running() {  # <run>
  local deadline; deadline=$(( $(date +%s) + 180 ))
  while [ "$(date +%s)" -lt "$deadline" ]; do
    if [ "$(ti_state "$1" produce)" = success ] && [ "$(ti_state "$1" hold)" = running ]; then
      return 0
    fi
    case "$(ti_state "$1" produce)" in failed|upstream_failed) return 1 ;; esac
    sleep 2
  done
  return 1
}

# wait_run_done prints the terminal run state, or "timeout".
wait_run_done() {  # <run>
  local deadline st; deadline=$(( $(date +%s) + RUN_TIMEOUT ))
  while [ "$(date +%s)" -lt "$deadline" ]; do
    st="$(run_state "$1")"
    case "$st" in success|failed) printf '%s\n' "$st"; return 0 ;; esac
    sleep 3
  done
  printf 'timeout\n'
}

# redis_failures prints the control plane's Redis command failure count.
redis_failures() {
  curl -fsS --max-time 5 "http://localhost:${METRICS_PORT}/metrics" 2>/dev/null |
    awk '/^dexaflow_redis_command_failures_total/{s+=$NF} END{printf "%d\n", s}'
}

# expect_value_carried asserts consume returned exactly what produce returned.
expect_value_carried() {  # <scenario> <run>
  local produced consumed
  produced="$(xcom_value "$2" produce)"; consumed="$(xcom_value "$2" consume)"
  if [ -n "$produced" ] && [ "$produced" != null ] && [ "$produced" = "$consumed" ]; then
    ok "$1: consume read the value produce wrote before the fault ($produced)"
  else
    bad "$1: the value did not cross the fault intact (produce=$produced consume=$consumed)"
  fi
}

server_alive() { kill -0 "$SERVER_PID" 2>/dev/null; }

# ── R1: restart with persistence ────────────────────────────────────────────
scenario_restart() {
  log "R1: restart the Redis primary (AOF on) while a value waits to be read"
  local run; run="$(trigger_run)"
  [ -n "$run" ] && [ "$run" != null ] || { bad "R1: no run id"; return; }
  wait_hold_running "$run" || { bad "R1: produce never succeeded with hold running"; dump_task_instances "$API" "$DAG_ID" "$run" "$TOKEN"; return; }
  sleep 2 # one appendfsync everysec period, so the value is on disk
  local f0 t0 t1; f0="$(redis_failures)"; t0=$(date +%s)
  docker restart "$PRIMARY" >/dev/null || { bad "R1: docker restart failed"; return; }
  wait_endpoint 60 || { bad "R1: Redis never answered after the restart"; return; }
  t1=$(date +%s)
  local st; st="$(wait_run_done "$run")"
  if [ "$st" = success ]; then
    ok "R1: the run succeeded across the restart"
    expect_value_carried R1 "$run"
  else
    bad "R1: run ended $st after a restart with persistence on"
    dump_task_instances "$API" "$DAG_ID" "$run" "$TOKEN"
  fi
  if server_alive; then ok "R1: the control plane was not restarted"; else bad "R1: the control plane exited"; fi
  result "R1 restart with AOF: run $st, Redis back in $((t1 - t0))s, control plane Redis command failures during the fault: $(( $(redis_failures) - f0 ))"
}

# ── R2: managed failover behind the same endpoint ───────────────────────────
scenario_failover() {
  log "R2: promote the replica and move the endpoint to it, control plane untouched"
  local run; run="$(trigger_run)"
  [ -n "$run" ] && [ "$run" != null ] || { bad "R2: no run id"; return; }
  wait_hold_running "$run" || { bad "R2: produce never succeeded with hold running"; dump_task_instances "$API" "$DAG_ID" "$run" "$TOKEN"; return; }
  # Asynchronous replication: wait until the replica holds what the primary
  # has, as a provider's planned failover does before it switches.
  local deadline po ro; deadline=$(( $(date +%s) + 30 ))
  while [ "$(date +%s)" -lt "$deadline" ]; do
    po="$(rcli "$PRIMARY" info replication | tr -d '\r' | awk -F: '/^master_repl_offset/{print $2}')"
    ro="$(rcli "$REPLICA" info replication | tr -d '\r' | awk -F: '/^master_repl_offset/{print $2}')"
    [ -n "$po" ] && [ "$po" = "$ro" ] && break
    sleep 1
  done
  [ -n "$po" ] && [ "$po" = "$ro" ] || { bad "R2: the replica never caught up (primary $po, replica $ro)"; return; }
  local f0 t0 t1; f0="$(redis_failures)"; t0=$(date +%s)
  docker stop "$PRIMARY" >/dev/null
  rcli "$REPLICA" replicaof no one >/dev/null || { bad "R2: could not promote the replica"; return; }
  start_proxy "$REPLICA"
  wait_endpoint 60 || { bad "R2: the moved endpoint never answered"; return; }
  t1=$(date +%s)
  local st; st="$(wait_run_done "$run")"
  if [ "$st" = success ]; then
    ok "R2: the run succeeded across the failover"
    expect_value_carried R2 "$run"
  else
    bad "R2: run ended $st after a failover that kept the replicated value"
    dump_task_instances "$API" "$DAG_ID" "$run" "$TOKEN"
  fi
  if server_alive; then ok "R2: the control plane reconnected without a restart"; else bad "R2: the control plane exited"; fi
  result "R2 failover behind the same endpoint: run $st, endpoint down $((t1 - t0))s, control plane Redis command failures: $(( $(redis_failures) - f0 ))"
}

# ── R3: flush (an instance replaced without its data) ───────────────────────
scenario_flush() {
  log "R3: flush Redis while a value waits to be read; the run must fail, not guess"
  local primary="$PRIMARY"
  docker inspect -f '{{.State.Running}}' "$PRIMARY" 2>/dev/null | grep -q true || primary="$REPLICA"
  local run; run="$(trigger_run)"
  [ -n "$run" ] && [ "$run" != null ] || { bad "R3: no run id"; return; }
  wait_hold_running "$run" || { bad "R3: produce never succeeded with hold running"; dump_task_instances "$API" "$DAG_ID" "$run" "$TOKEN"; return; }
  local t0; t0=$(date +%s)
  rcli "$primary" flushall >/dev/null || { bad "R3: FLUSHALL failed"; return; }
  local st; st="$(wait_run_done "$run")"
  local cst reason pst
  cst="$(ti_state "$run" consume)"; reason="$(ti_reason "$run" consume)"; pst="$(ti_state "$run" produce)"
  if [ "$st" = failed ] && [ "$cst" = failed ]; then
    ok "R3: the run failed on consume (reason: ${reason:-none})"
  else
    bad "R3: run $st, consume $cst after the flush: a lost value must fail the reader"
    dump_task_instances "$API" "$DAG_ID" "$run" "$TOKEN"
  fi
  if [ "$pst" = success ]; then ok "R3: produce kept its success"; else bad "R3: produce is $pst after the flush"; fi
  local served; served="$(xcom_value "$run" produce)"
  log "R3: produce's value as the API serves it after the flush: ${served:-<no entry>}"
  # Recovery: the next run writes and reads its own value.
  local t1 run2 st2; t1=$(date +%s); run2="$(trigger_run)"
  st2="$(wait_run_done "$run2")"
  if [ "$st2" = success ]; then
    ok "R3: a run started after the flush succeeded"
  else
    bad "R3: the run after the flush ended $st2"
    dump_task_instances "$API" "$DAG_ID" "$run2" "$TOKEN"
  fi
  server_alive || bad "R3: the control plane exited"
  result "R3 flush: run $st, consume $cst with reason \"${reason}\", produce $pst, API value after flush ${served:-<none>}, failed run settled $((t1 - t0))s after the flush, next run $st2 in $(( $(date +%s) - t1 ))s"
}

want R1 && scenario_restart
want R2 && scenario_failover
want R3 && scenario_flush

printf '\n\033[1m===== redis-faults summary =====\033[0m\n'
for r in "${RESULTS[@]}"; do printf -- '- %s\n' "$r"; done
if [ -n "${REDIS_FAULTS_RESULTS:-}" ]; then
  {
    printf '# Redis fault injection results\n\n'
    printf 'Commit %s, %s, Redis image %s.\n\n' "$(git -C "$ROOT" rev-parse --short HEAD 2>/dev/null || echo unknown)" "$(date -u +%FT%TZ)" "$REDIS_IMAGE"
    for r in "${RESULTS[@]}"; do printf -- '- %s\n' "$r"; done
  } >"$REDIS_FAULTS_RESULTS"
fi
if [ "$FAILED" -ne 0 ]; then
  printf '\033[1;31mredis-faults: FAILED\033[0m\n' >&2
  exit 1
fi
printf '\033[1;32mredis-faults: all scenarios passed\033[0m\n'
