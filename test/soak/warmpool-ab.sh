#!/usr/bin/env bash
#
# warmpool-ab.sh: the ADR 0058 warm-pool comparison, on a local k3d cluster.
#
# Warm pools are Kubernetes-only (internal/executor/warmpool_k8s.go; Lite's
# subprocess executor has no pool at all), so the with/without question cannot be
# answered inside the Lite soak. This is its own bounded experiment: minutes, one
# throwaway cluster, no registry, no cloud.
#
# ── The comparison is NOT one variable, and this script does not pretend it is ─
#
# internal/config/server.go fails boot closed unless execution.warm_pools_enabled
# is accompanied by auth.agent_token_transport=exchange AND
# auth.secret_liveness_mode=enforce (ADR 0058 D2). Warm pools are a security
# decision before they are a performance one. So the arms are:
#
#   Arm A (off): the shipped default. warm_pools_enabled=false,
#                agent_token_transport=envvar, secret_liveness_mode=observe.
#   Arm B (on):  warm_pools_enabled=true plus the two settings its boot validator
#                requires.
#
# The honest reading of any number this prints is therefore "warm pools together
# with their mandatory security posture, against dedicated pods at the shipped
# default", not "pools against no pools".
#
# ── Fairness ────────────────────────────────────────────────────────────────
#
#  1. The same two DAG images, the same task bodies, the same data volume.
#  2. The same operator mix in both arms (one native `python` DAG, one
#     `airflow_operator` DAG), so the comparison measures the pools and not the
#     mix.
#  3. Interleaved A/B/A/B blocks, never A-then-B: a single crossover loads
#     cluster warm-up, image-cache state and laptop thermal drift onto arm B.
#  4. A discarded warm-up block at the head of each arm, so arm B is not credited
#     for a pool that does not exist yet and arm A is not charged for a cold node.
#  5. One cluster, created once, for the whole experiment.
#
# ── Status ──────────────────────────────────────────────────────────────────
#
# WRITTEN, NOT YET EXECUTED. It reuses the k3d patterns from
# test/e2e/chaos-runtime.sh and test/e2e/e2e.sh, but it has not been run to
# completion, so treat its output format as a proposal until the first green run.
# The banner below says so on every invocation, on purpose: a number from an
# unvalidated harness that nobody flagged is worse than no number.
#
# Usage: bash test/soak/warmpool-ab.sh [--blocks 4] [--runs-per-block 5] [--keep]
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
CLUSTER="${SOAK_AB_CLUSTER:-leoflow-soak-ab}"
NS="leoflow"
BLOCKS="${SOAK_AB_BLOCKS:-4}"          # total blocks, alternating A,B,A,B
RUNS_PER_BLOCK="${SOAK_AB_RUNS:-5}"
WARMUP_RUNS="${SOAK_AB_WARMUP:-2}"     # discarded at the head of every block
API_PORT="${SOAK_AB_API_PORT:-18710}"
GRPC_PORT="${SOAK_AB_GRPC_PORT:-19091}"
METRICS_PORT="${SOAK_AB_METRICS_PORT:-19090}"
PG_PORT="${SOAK_AB_PG_PORT:-55434}"
KEEP=0
OUT="${SOAK_AB_OUT:-$ROOT/.soak/warmpool-ab-$(date -u +%Y%m%dT%H%M%SZ)}"

while [ $# -gt 0 ]; do
  case "$1" in
    --blocks) BLOCKS="$2"; shift 2 ;;
    --runs-per-block) RUNS_PER_BLOCK="$2"; shift 2 ;;
    --keep) KEEP=1; shift ;;
    -h|--help) sed -n '2,50p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) echo "unknown flag: $1" >&2; exit 2 ;;
  esac
done

mkdir -p "$OUT"
SERVER_PID=""
log()  { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
ok()   { printf '\033[1;32m    ok\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m    !!\033[0m %s\n' "$*" >&2; }
die()  { printf '\033[1;31mFATAL:\033[0m %s\n' "$*" >&2; exit 2; }

cat <<'BANNER'
┌───────────────────────────────────────────────────────────────────────────┐
│ warmpool-ab.sh has NOT been executed end to end yet.                      │
│ Its output format is a proposal. Do not quote a number from this script   │
│ as a result until a run has completed green and this banner is removed.   │
└───────────────────────────────────────────────────────────────────────────┘
BANNER

cleanup() {
  [ -n "$SERVER_PID" ] && kill "$SERVER_PID" 2>/dev/null
  if [ "$KEEP" = "0" ]; then
    k3d cluster delete "$CLUSTER" >/dev/null 2>&1
    docker rm -f leoflow-soak-ab-postgres >/dev/null 2>&1
  fi
  echo; log "evidence: $OUT"
}
trap cleanup EXIT INT TERM

log "preflight"
for tool in k3d kubectl docker go jq curl python3; do
  command -v "$tool" >/dev/null || die "missing required tool: $tool"
done
# Cost guard: this experiment must never touch a remote cluster or a paid
# registry. Images go in with `k3d image import`, and the kubeconfig is the one
# k3d writes for the throwaway cluster, never an inherited context.
if [ -n "${KUBECONFIG:-}" ]; then
  warn "KUBECONFIG is set; this script ignores it and uses the throwaway cluster's own file"
fi
ok "preflight passed"

log "creating the throwaway k3d cluster $CLUSTER"
k3d cluster delete "$CLUSTER" >/dev/null 2>&1
# --kubeconfig-update-default=false is not decoration: k3d defaults to merging the
# new cluster into the operator's ~/.kube/config AND switching the current
# context to it. A throwaway experiment must not repoint the machine's kubectl at
# a cluster it is about to delete.
k3d cluster create "$CLUSTER" --agents 0 --wait \
  --kubeconfig-update-default=false --kubeconfig-switch-context=false \
  >/dev/null 2>&1 || die "k3d cluster create failed"
export KUBECONFIG="$OUT/kubeconfig"
k3d kubeconfig get "$CLUSTER" > "$KUBECONFIG" || die "could not fetch the kubeconfig"
kubectl create namespace "$NS" >/dev/null 2>&1
ok "cluster up, kubeconfig isolated at $KUBECONFIG"

log "starting a throwaway Postgres for the control plane"
docker rm -f leoflow-soak-ab-postgres >/dev/null 2>&1
docker run -d --name leoflow-soak-ab-postgres \
  -e POSTGRES_USER=leoflow -e POSTGRES_PASSWORD=leoflow -e POSTGRES_DB=leoflow_ab \
  -p "127.0.0.1:${PG_PORT}:5432" --cpus 1.0 --memory 1g postgres:16-alpine >/dev/null \
  || die "could not start the A/B Postgres"
for _ in $(seq 1 60); do
  docker exec leoflow-soak-ab-postgres pg_isready -U leoflow -d leoflow_ab >/dev/null 2>&1 && break
  sleep 1
done
DB_URL="postgres://leoflow:leoflow@127.0.0.1:${PG_PORT}/leoflow_ab?sslmode=disable"
ok "postgres on 127.0.0.1:${PG_PORT}"

log "building binaries and the two DAG images"
go build -o "$OUT/leoflow" ./cmd/dexaflow || die "building leoflow"
go build -o "$OUT/leoflow-server" ./cmd/dexaflow-server || die "building leoflow-server"
go build -o "$OUT/leoflow-agent" ./cmd/dexaflow-agent || die "building leoflow-agent"
# The two DAGs are the same shapes the Lite soak runs, trimmed to the pair that
# makes the operator-mix comparison meaningful: one native python DAG and one
# airflow_operator DAG. Building them is the expensive part of this script.
WS="$OUT/workspace"; mkdir -p "$WS"
cp -R "$ROOT/test/soak/dags/soak_ingest" "$WS/"
cp -R "$ROOT/test/soak/dags/soak_operators" "$WS/"
warn "image build for the two DAGs is the step this script has not yet been run through"
warn "it is expected to use: dexaflow compile --build, then k3d image import"

# ── The two arms. Every variable that differs is listed here and nowhere else,
#    so a reader can check the fairness claim by reading one block. ───────────
arm_env() { # $1 = off|on
  case "$1" in
    off)
      echo "LEOFLOW_EXECUTION_WARM_POOLS_ENABLED=false"
      echo "LEOFLOW_AUTH_AGENT_TOKEN_TRANSPORT=envvar"
      echo "LEOFLOW_AUTH_SECRET_LIVENESS_MODE=observe"
      ;;
    on)
      # All five are required: the boot validator refuses warm pools without the
      # first two, and rejects a non-positive value for each of the last three.
      echo "LEOFLOW_EXECUTION_WARM_POOLS_ENABLED=true"
      echo "LEOFLOW_AUTH_AGENT_TOKEN_TRANSPORT=exchange"
      echo "LEOFLOW_AUTH_SECRET_LIVENESS_MODE=enforce"
      echo "LEOFLOW_EXECUTION_MAX_ATTEMPTS_PER_WORKER=50"
      echo "LEOFLOW_EXECUTION_MAX_WORKER_LIFETIME=1h"
      echo "LEOFLOW_EXECUTION_WORKER_IDLE_TTL=5m"
      echo "LEOFLOW_EXECUTION_MAX_POOL_SIZE=4"
      echo "LEOFLOW_EXECUTION_MAX_WARM_PODS_PER_TENANT=8"
      echo "LEOFLOW_EXECUTION_DEFAULT_WARM_WORKERS=1"
      ;;
  esac
}

start_server() { # $1 = off|on
  local arm="$1" env_lines
  env_lines="$(arm_env "$arm")"
  # shellcheck disable=SC2046 # word splitting of KEY=VALUE lines is intended
  env $env_lines \
    LEOFLOW_DATABASE_URL="$DB_URL" \
    LEOFLOW_EXECUTOR_TYPE=kubernetes \
    LEOFLOW_EXECUTOR_TASK_NAMESPACE="$NS" \
    LEOFLOW_SERVER_HTTP_ADDR="127.0.0.1:${API_PORT}" \
    LEOFLOW_SERVER_GRPC_ADDR="0.0.0.0:${GRPC_PORT}" \
    LEOFLOW_SERVER_METRICS_ADDR="127.0.0.1:${METRICS_PORT}" \
    LEOFLOW_EXECUTOR_AGENT_CONTROL_PLANE_ADDR="host.k3d.internal:${GRPC_PORT}" \
    LEOFLOW_AUTH_DEV_NO_AUTH=true \
    KUBECONFIG="$KUBECONFIG" \
    "$OUT/leoflow-server" >> "$OUT/server-$arm.log" 2>&1 &
  SERVER_PID=$!
  for _ in $(seq 1 120); do
    curl -fsS "http://127.0.0.1:${API_PORT}/readyz" >/dev/null 2>&1 && return 0
    kill -0 "$SERVER_PID" 2>/dev/null || return 1
    sleep 1
  done
  return 1
}

stop_server() {
  [ -n "$SERVER_PID" ] && kill "$SERVER_PID" 2>/dev/null
  for _ in $(seq 1 30); do kill -0 "$SERVER_PID" 2>/dev/null || break; sleep 1; done
  kill -9 "$SERVER_PID" 2>/dev/null
  SERVER_PID=""
}

trigger_and_wait() { # $1 dag_id
  local rid
  rid="$(curl -fsS -X POST "http://127.0.0.1:${API_PORT}/api/v2/dags/$1/dagRuns" \
    -H 'content-type: application/json' -d '{}' | jq -r '.dag_run_id')"
  [ -n "$rid" ] && [ "$rid" != "null" ] || { warn "could not trigger $1"; return 1; }
  for _ in $(seq 1 600); do
    state="$(curl -fsS "http://127.0.0.1:${API_PORT}/api/v2/dags/$1/dagRuns/$rid" 2>/dev/null | jq -r '.state')"
    case "$state" in success|failed) return 0 ;; esac
    sleep 2
  done
  warn "run $rid of $1 did not reach a terminal state"
  return 1
}

# block_marker records where in the timeline each block started and ended, so the
# analysis can attribute every task instance to exactly one arm without guessing.
: > "$OUT/blocks.jsonl"
mark_block() { # arm phase
  python3 -c '
import json,sys,datetime
print(json.dumps({"arm":sys.argv[1],"phase":sys.argv[2],
  "at":datetime.datetime.now(datetime.timezone.utc).isoformat().replace("+00:00","Z")}))' \
  "$1" "$2" >> "$OUT/blocks.jsonl"
}

log "running $BLOCKS interleaved blocks of $RUNS_PER_BLOCK runs ($WARMUP_RUNS discarded per block)"
for i in $(seq 1 "$BLOCKS"); do
  if [ $((i % 2)) -eq 1 ]; then arm=off; else arm=on; fi
  log "block $i: warm pools $arm"
  start_server "$arm" || { warn "control plane did not boot for arm $arm (check $OUT/server-$arm.log)"; break; }
  mark_block "$arm" "warmup_start"
  for _ in $(seq 1 "$WARMUP_RUNS"); do
    trigger_and_wait soak_ingest; trigger_and_wait soak_operators
  done
  mark_block "$arm" "measure_start"
  for _ in $(seq 1 "$RUNS_PER_BLOCK"); do
    trigger_and_wait soak_ingest; trigger_and_wait soak_operators
  done
  mark_block "$arm" "measure_end"
  curl -fsS "http://127.0.0.1:${METRICS_PORT}/metrics" > "$OUT/metrics-block-$i-$arm.prom" 2>/dev/null
  stop_server
done

log "analysis"
# The analysis is pure SQL over the metadatabase, attributed by the block
# timeline: no metric on the control plane survives a restart, and both arms
# restart, so the database is the only continuous record.
docker exec -i leoflow-soak-ab-postgres psql -U leoflow -d leoflow_ab -At -F'|' <<'SQL' > "$OUT/task-latencies.psv" 2>/dev/null
SELECT d.dag_id, ti.task_id, ti.operator,
       extract(epoch from (ti.started_at - ti.queued_at)) * 1000 AS dispatch_ms,
       extract(epoch from (ti.ended_at - ti.started_at))   * 1000 AS run_ms,
       ti.queued_at, ti.warm_worker_id
FROM task_instances ti
JOIN dag_runs r ON r.id = ti.dag_run_id
JOIN dags d     ON d.id = r.dag_id
WHERE ti.started_at IS NOT NULL AND ti.queued_at IS NOT NULL
ORDER BY ti.queued_at
SQL

python3 - "$OUT" <<'PY'
import json, os, statistics, sys

out = sys.argv[1]
blocks = [json.loads(l) for l in open(os.path.join(out, "blocks.jsonl"))] \
    if os.path.exists(os.path.join(out, "blocks.jsonl")) else []
path = os.path.join(out, "task-latencies.psv")
if not os.path.exists(path) or os.path.getsize(path) == 0:
    print("no task latencies were recorded; the run did not get far enough to compare")
    sys.exit(0)

# Build the measurement windows: every (measure_start, measure_end) pair.
windows = []
start = None
for b in blocks:
    if b["phase"] == "measure_start":
        start = b
    elif b["phase"] == "measure_end" and start:
        windows.append((start["arm"], start["at"], b["at"]))
        start = None

rows = []
for line in open(path):
    parts = line.rstrip("\n").split("|")
    if len(parts) < 7:
        continue
    dag, task, op, disp, run, qat, worker = parts[:7]
    rows.append({"dag": dag, "task": task, "op": op,
                 "dispatch_ms": float(disp or 0), "run_ms": float(run or 0),
                 "queued_at": qat, "worker": worker})

def arm_of(queued_at):
    for arm, s, e in windows:
        if s <= queued_at.replace(" ", "T") <= e:
            return arm
    return None

by = {}
for r in rows:
    a = arm_of(r["queued_at"])
    if a is None:
        continue
    by.setdefault((a, r["op"]), []).append(r)

def pct(v, p):
    if not v:
        return 0.0
    v = sorted(v)
    return v[min(len(v) - 1, int(p * (len(v) - 1) + 0.5))]

print()
print("| arm | operator | n | dispatch p50 ms | dispatch p95 ms | run p50 ms | distinct warm workers |")
print("|---|---|---|---|---|---|---|")
for (arm, op), rs in sorted(by.items()):
    d = [r["dispatch_ms"] for r in rs]
    m = [r["run_ms"] for r in rs]
    workers = len({r["worker"] for r in rs if r["worker"]})
    print(f"| {arm} | {op} | {len(rs)} | {pct(d,0.5):.0f} | {pct(d,0.95):.0f} | {pct(m,0.5):.0f} | {workers} |")
print()
print("Read it this way: warm pools buy dispatch latency, not run time. If the")
print("dispatch p50 of arm `on` is not clearly below arm `off` for the SAME")
print("operator, the pools are not being hit, and `distinct warm workers` tells")
print("you whether any pod was reused at all.")
PY

log "done. Raw per-task latencies in $OUT/task-latencies.psv, block timeline in $OUT/blocks.jsonl"
