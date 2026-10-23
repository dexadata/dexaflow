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
# internal/config/server_execution.go fails boot closed unless execution.warm_pools_enabled
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
# NOT YET RUN TO A GREEN RESULT. It reuses the k3d patterns of test/e2e (the
# base image of e2e.sh, `dexaflow compile --build` from dexaflow.yaml, the
# k3d_import, admin_token and unpause_dag helpers of test/e2e/lib.sh), and its
# decision logic has a self-test, but no run has completed on a cluster yet, so
# treat its output format as a proposal until the first green run. The banner
# below says so on every invocation, on purpose: a number from an unvalidated
# harness that nobody flagged is worse than no number.
#
# Until #1920 it booted no control plane (no JWT secret), never built, imported
# or registered its two DAG images, and exited 0 having measured nothing, so
# `make soak-warmpool-ab` read as a pass. It now exits 2 whenever it has no
# latency for both arms.
#
# Exit codes: 0 both arms recorded latencies and the table is printed; 2 no
# comparison (the harness could not run, or nothing was measured).
#
# Usage: bash test/soak/warmpool-ab.sh [--blocks 4] [--runs-per-block 5] [--keep]
#        bash test/soak/warmpool-ab.sh --self-test
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
# shellcheck source=test/e2e/lib.sh
source "$ROOT/test/e2e/lib.sh"
CLUSTER="${SOAK_AB_CLUSTER:-leoflow-soak-ab}"
NS="leoflow"
BLOCKS="${SOAK_AB_BLOCKS:-4}"          # total blocks, alternating A,B,A,B
RUNS_PER_BLOCK="${SOAK_AB_RUNS:-5}"
WARMUP_RUNS="${SOAK_AB_WARMUP:-2}"     # discarded at the head of every block
API_PORT="${SOAK_AB_API_PORT:-18710}"
GRPC_PORT="${SOAK_AB_GRPC_PORT:-19091}"
METRICS_PORT="${SOAK_AB_METRICS_PORT:-19090}"
PG_PORT="${SOAK_AB_PG_PORT:-55434}"
API="http://127.0.0.1:${API_PORT}"
# Address task pods dial to reach the host control plane's gRPC, as in e2e.sh:
# k3d injects host.k3d.internal on Linux, Docker Desktop resolves
# host.docker.internal elsewhere.
HOST_ADDR="${SOAK_AB_HOST_ADDR:-$([ "$(uname -s)" = Linux ] && echo host.k3d.internal || echo host.docker.internal)}"
PY_VERSION="3.11"
BASE_IMAGE="leoflow-base:py${PY_VERSION}"
# The control plane refuses to boot without a JWT secret, and the bootstrap
# admin's password is what admin_token logs in with. Both are throwaway values
# for a throwaway cluster, like the ones the e2e scripts set.
JWT_SECRET="soak-ab-secret"
ADMIN_PASSWORD="admin"
DAGS="soak_ingest soak_operators"
KEEP=0
OUT="${SOAK_AB_OUT:-$ROOT/.soak/warmpool-ab-$(date -u +%Y%m%dT%H%M%SZ)}"
SERVER_PID=""
TOKEN=""

log()  { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
ok()   { printf '\033[1;32m    ok\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m    !!\033[0m %s\n' "$*" >&2; }
die()  { printf '\033[1;31mFATAL:\033[0m %s\n' "$*" >&2; exit 2; }

# dag_image names the local image a soak DAG is built into.
dag_image() { echo "leoflow-soak-ab-${1//_/-}:local"; }

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
  # The logs dir is the e2e scripts' fix for #36: the default /var/log/leoflow is
  # not writable by a normal user, and the agent's log stream then ends in EOF.
  # shellcheck disable=SC2086 # word splitting of KEY=VALUE lines is intended
  env $env_lines \
    LEOFLOW_DATABASE_URL="$DB_URL" \
    LEOFLOW_AUTH_JWT_SECRET="$JWT_SECRET" \
    LEOFLOW_BOOTSTRAP_PASSWORD="$ADMIN_PASSWORD" \
    LEOFLOW_LOGS_DIR="$OUT/logs" \
    LEOFLOW_EXECUTOR_TYPE=kubernetes \
    LEOFLOW_EXECUTOR_TASK_NAMESPACE="$NS" \
    LEOFLOW_SERVER_HTTP_ADDR="127.0.0.1:${API_PORT}" \
    LEOFLOW_SERVER_GRPC_ADDR="0.0.0.0:${GRPC_PORT}" \
    LEOFLOW_SERVER_METRICS_ADDR="127.0.0.1:${METRICS_PORT}" \
    LEOFLOW_EXECUTOR_AGENT_CONTROL_PLANE_ADDR="${HOST_ADDR}:${GRPC_PORT}" \
    KUBECONFIG="$KUBECONFIG" \
    "$OUT/leoflow-server" >> "$OUT/server-$arm.log" 2>&1 &
  SERVER_PID=$!
  for _ in $(seq 1 120); do
    curl -fsS --max-time 5 "$API/readyz" >/dev/null 2>&1 && return 0
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

# register_dags pushes the two compiled DAGs and unpauses them: a DAG starts
# paused, and a run triggered while it is paused stays queued. It needs a
# running control plane, so the first block calls it once.
register_dags() {
  local dag
  TOKEN="$(admin_token "$OUT/leoflow" "$API" "$ADMIN_PASSWORD")" || return 1
  [ -n "$TOKEN" ] || return 1
  for dag in $DAGS; do
    "$OUT/leoflow" push "$WS/$dag/dag.json" --server "$API" --token "$TOKEN" >> "$OUT/register.log" 2>&1 \
      || { warn "pushing $dag failed (see $OUT/register.log)"; return 1; }
    unpause_dag "$API" "$dag" "$TOKEN" || return 1
  done
}

trigger_and_wait() { # $1 dag_id
  local rid state
  rid="$(curl -fsS --max-time 15 -X POST -H "Authorization: Bearer $TOKEN" \
    -H 'content-type: application/json' -d '{}' "$API/api/v2/dags/$1/dagRuns" | jq -r '.dag_run_id')"
  [ -n "$rid" ] && [ "$rid" != "null" ] || { warn "could not trigger $1"; return 1; }
  for _ in $(seq 1 600); do
    state="$(curl -fsS --max-time 15 -H "Authorization: Bearer $TOKEN" \
      "$API/api/v2/dags/$1/dagRuns/$rid" 2>/dev/null | jq -r '.state')"
    case "$state" in success|failed) return 0 ;; esac
    sleep 2
  done
  warn "run $rid of $1 did not reach a terminal state"
  return 1
}

# mark_block records where in the timeline each block started and ended, so the
# analysis can attribute every task instance to exactly one arm without guessing.
mark_block() { # arm phase
  python3 -c '
import json,sys,datetime
print(json.dumps({"arm":sys.argv[1],"phase":sys.argv[2],
  "at":datetime.datetime.now(datetime.timezone.utc).isoformat().replace("+00:00","Z")}))' \
  "$1" "$2" >> "$OUT/blocks.jsonl"
}

# analyze prints the comparison from <out>/blocks.jsonl and
# <out>/task-latencies.psv, one row per task instance:
# dag|task|operator|dispatch_ms|run_ms|queued_at_epoch|warm_worker_id|state.
# It exits 2 unless both arms recorded at least one latency inside a measured
# window: one arm, or none, is not a comparison, and a 0 there read as a pass.
analyze() { # out
  python3 - "$1" <<'PY'
import datetime, json, os, statistics, sys

out = sys.argv[1]
blocks_path = os.path.join(out, "blocks.jsonl")
blocks = [json.loads(l) for l in open(blocks_path)] if os.path.exists(blocks_path) else []
path = os.path.join(out, "task-latencies.psv")
if not os.path.exists(path) or os.path.getsize(path) == 0:
    print("no task latencies were recorded; the run did not get far enough to compare")
    sys.exit(2)


def epoch(at):
    return datetime.datetime.fromisoformat(at.replace("Z", "+00:00")).timestamp()


# Build the measurement windows: every (measure_start, measure_end) pair. Times
# are compared as epoch seconds, so the database session's time zone and its
# timestamp format cannot misplace a row.
windows = []
start = None
for b in blocks:
    if b["phase"] == "measure_start":
        start = b
    elif b["phase"] == "measure_end" and start:
        windows.append((start["arm"], epoch(start["at"]), epoch(b["at"])))
        start = None

rows = []
for line in open(path):
    parts = line.rstrip("\n").split("|")
    if len(parts) < 7:
        continue
    dag, task, op, disp, run, qat, worker = parts[:7]
    state = parts[7] if len(parts) > 7 else ""
    rows.append({"dag": dag, "task": task, "op": op,
                 "dispatch_ms": float(disp or 0), "run_ms": float(run or 0),
                 "queued_at": float(qat), "worker": worker, "state": state})


def arm_of(queued_at):
    for arm, s, e in windows:
        if s <= queued_at <= e:
            return arm
    return None


by = {}
for r in rows:
    a = arm_of(r["queued_at"])
    if a is None:
        continue
    by.setdefault((a, r["op"]), []).append(r)

arms = {arm for arm, _ in by}
if arms != {"off", "on"}:
    print(f"latencies were recorded for arms {sorted(arms) or 'none'} inside the measured windows; "
          "a comparison needs both off and on")
    sys.exit(2)


def pct(v, p):
    if not v:
        return 0.0
    v = sorted(v)
    return v[min(len(v) - 1, int(p * (len(v) - 1) + 0.5))]


print()
print("| arm | operator | n | dispatch p50 ms | dispatch p95 ms | run p50 ms | distinct warm workers | failed |")
print("|---|---|---|---|---|---|---|---|")
for (arm, op), rs in sorted(by.items()):
    d = [r["dispatch_ms"] for r in rs]
    m = [r["run_ms"] for r in rs]
    workers = len({r["worker"] for r in rs if r["worker"]})
    failed = sum(1 for r in rs if r["state"] == "failed")
    print(f"| {arm} | {op} | {len(rs)} | {pct(d,0.5):.0f} | {pct(d,0.95):.0f} | {pct(m,0.5):.0f} | {workers} | {failed} |")
print()
print("Read it this way: warm pools buy dispatch latency, not run time. If the")
print("dispatch p50 of arm `on` is not clearly below arm `off` for the SAME")
print("operator, the pools are not being hit, and `distinct warm workers` tells")
print("you whether any pod was reused at all. Checklist row F1 also needs `failed`")
print("at 0 in both arms.")
PY
}

# self_test checks the parts of this script that decide its answer, without a
# cluster: the analysis on recorded fixtures, and two whole runs of the script
# with k3d, kubectl, docker, go, migrate and curl stubbed on PATH. Before #1920
# the script booted no control plane (no JWT secret), registered no DAG and
# exited 0 having measured nothing; each of those fails here.
self_test() {
  local tmp rc=0
  tmp="$(mktemp -d)" || return 1
  _ab_write_stubs "$tmp/stub"
  _ab_selftest_analyze "$tmp/analyze" || rc=1
  _ab_selftest_run "$tmp" dies || rc=1
  _ab_selftest_run "$tmp" boots || rc=1
  rm -rf "$tmp"
  return "$rc"
}

# _ab_write_stubs writes the stand-ins for every tool that would touch docker,
# a cluster or the network. Each logs its arguments to $AB_STUB_LOG.
_ab_write_stubs() { # dir
  local d="$1" t
  mkdir -p "$d"
  for t in k3d kubectl migrate; do
    # shellcheck disable=SC2016 # the stub expands $* when it runs, not here
    printf '#!/usr/bin/env bash\necho "%s $*" >> "${AB_STUB_LOG:?}"\n' "$t" > "$d/$t"
  done
  # docker: a psql through `docker exec` prints no row, which is the "nothing
  # was recorded" case the script must not report as a pass.
  cat > "$d/docker" <<'STUB'
#!/usr/bin/env bash
echo "docker $*" >> "${AB_STUB_LOG:?}"
case " $* " in *" psql "*) cat >/dev/null ;; esac
exit 0
STUB
  # go build writes a stub binary at -o: the server records its environment
  # and stays up only when AB_STUB_SERVER=boots; the CLI mints a token.
  cat > "$d/go" <<'STUB'
#!/usr/bin/env bash
echo "go $*" >> "${AB_STUB_LOG:?}"
out=""
while [ $# -gt 0 ]; do [ "$1" = -o ] && out="$2"; shift; done
[ -n "$out" ] || exit 0
case "$(basename "$out")" in
  leoflow-server) cat > "$out" <<'BIN'
#!/usr/bin/env bash
{ echo "--- server"; env; } >> "${AB_STUB_LOG:?}.server-env"
[ "${AB_STUB_SERVER:-}" = boots ] && exec sleep 300
exit 1
BIN
  ;;
  leoflow) cat > "$out" <<'BIN'
#!/usr/bin/env bash
echo "leoflow $*" >> "${AB_STUB_LOG:?}"
[ "${1:-}" = auth ] && echo stub-token
exit 0
BIN
  ;;
  *) printf '#!/usr/bin/env bash\nexit 0\n' > "$out" ;;
esac
chmod +x "$out"
STUB
  # curl answers the few calls the A/B makes, as a booted server would.
  cat > "$d/curl" <<'STUB'
#!/usr/bin/env bash
echo "curl $*" >> "${AB_STUB_LOG:?}"
url="" method=GET
for a in "$@"; do case "$a" in http*) url="$a" ;; POST|PATCH) method="$a" ;; esac; done
case "$method $url" in
  *"/readyz") [ "${AB_STUB_SERVER:-}" = boots ] ;;
  "PATCH "*"/api/v2/dags/"*) echo '{"is_paused": false}' ;;
  "POST "*"/dagRuns") echo '{"dag_run_id": "stub-run"}' ;;
  "GET "*"/dagRuns/"*) echo '{"state": "success"}' ;;
  *) exit 0 ;;
esac
STUB
  chmod +x "$d"/*
}

# _ab_selftest_run runs the whole script with the stubs: <tmp> <dies|boots>.
_ab_selftest_run() {
  local tmp="$1" mode="$2" dir code rc=0 img
  dir="$tmp/run-$mode"; mkdir -p "$dir"
  AB_STUB_LOG="$dir/calls.log" AB_STUB_SERVER="$mode" SOAK_AB_OUT="$dir/out" \
    SOAK_AB_WARMUP=0 PATH="$tmp/stub:$PATH" \
    bash "${BASH_SOURCE[0]}" --blocks 2 --runs-per-block 1 > "$dir/run.log" 2>&1
  code=$?
  if [ "$code" != 2 ]; then
    echo "FAIL run-$mode: a run that measured nothing must exit 2, got $code"; rc=1
  fi
  if [ "$mode" = boots ]; then
    for arm in off on; do
      grep -q "^LEOFLOW_EXECUTION_WARM_POOLS_ENABLED=$([ "$arm" = on ] && echo true || echo false)$" \
        "$dir/calls.log.server-env" 2>/dev/null \
        || { echo "FAIL run-boots: the control plane of arm $arm never started"; rc=1; }
    done
    if [ "$(grep -c '^LEOFLOW_AUTH_JWT_SECRET=.' "$dir/calls.log.server-env" 2>/dev/null)" != 2 ]; then
      echo "FAIL run-boots: every control-plane boot needs LEOFLOW_AUTH_JWT_SECRET, or it refuses to start"; rc=1
    fi
    grep -q '^migrate .* up' "$dir/calls.log" \
      || { echo "FAIL run-boots: the database was never migrated"; rc=1; }
    for dag in soak_ingest soak_operators; do
      grep -qE "^leoflow compile .*/$dag .*--build" "$dir/calls.log" \
        || { echo "FAIL run-boots: $dag was never compiled and built"; rc=1; }
      grep -qE "^leoflow push .*/$dag/dag.json" "$dir/calls.log" \
        || { echo "FAIL run-boots: $dag was never registered"; rc=1; }
      grep -qE "^curl .*PATCH.*/api/v2/dags/$dag\$" "$dir/calls.log" \
        || { echo "FAIL run-boots: $dag was never unpaused"; rc=1; }
    done
    for img in "$BASE_IMAGE" "$(dag_image soak_ingest)" "$(dag_image soak_operators)"; do
      grep -qE "^k3d image import .*$img" "$dir/calls.log" \
        || { echo "FAIL run-boots: $img was never imported into the cluster"; rc=1; }
    done
    grep -E '^curl .*POST.*/dagRuns$' "$dir/calls.log" | grep -q 'Authorization: Bearer stub-token' \
      || { echo "FAIL run-boots: runs were not triggered with the admin token"; rc=1; }
  fi
  if [ "$rc" = 0 ]; then
    echo "ok run-$mode: exit 2 with nothing measured"
  else
    sed 's/^/    run: /' "$dir/run.log"
  fi
  return "$rc"
}

# _ab_selftest_analyze feeds analyze recorded timelines and latencies.
_ab_selftest_analyze() { # dir
  local d="$1" rc=0 c code out
  for c in missing empty outside one-arm both; do
    mkdir -p "$d/$c"
    printf '%s\n' \
      '{"arm":"off","phase":"measure_start","at":"2026-10-17T10:00:00Z"}' \
      '{"arm":"off","phase":"measure_end","at":"2026-10-17T10:10:00Z"}' \
      '{"arm":"on","phase":"measure_start","at":"2026-10-17T10:20:00Z"}' \
      '{"arm":"on","phase":"measure_end","at":"2026-10-17T10:30:00Z"}' > "$d/$c/blocks.jsonl"
  done
  : > "$d/empty/task-latencies.psv"
  # 2026-10-17T10:05:00Z is 1792231500 and 10:25:00Z is 1792232700.
  echo 'soak_ingest|extract|python|900|1000|1792234800|' > "$d/outside/task-latencies.psv"
  echo 'soak_ingest|extract|python|900|1000|1792231500|' > "$d/one-arm/task-latencies.psv"
  printf '%s\n' 'soak_ingest|extract|python|900|1000|1792231500|' \
    'soak_ingest|extract|python|300|1000|1792232700|w-1' > "$d/both/task-latencies.psv"
  for c in missing empty outside one-arm; do
    out="$(analyze "$d/$c" 2>&1)"; code=$?
    if [ "$code" != 2 ]; then
      echo "FAIL analyze $c: no comparison must exit 2, got $code"; rc=1
    fi
  done
  out="$(analyze "$d/both" 2>&1)"; code=$?
  if [ "$code" != 0 ] || ! grep -q '^| off | python | 1 | 900 ' <<<"$out" \
    || ! grep -q '^| on | python | 1 | 300 ' <<<"$out"; then
    echo "FAIL analyze both: want exit 0 and one row per arm, got $code:"; echo "$out"; rc=1
  fi
  [ "$rc" = 0 ] && echo "ok analyze: exit 2 unless both arms recorded a latency"
  return "$rc"
}

while [ $# -gt 0 ]; do
  case "$1" in
    --self-test) self_test; exit $? ;;
    --blocks) BLOCKS="$2"; shift 2 ;;
    --runs-per-block) RUNS_PER_BLOCK="$2"; shift 2 ;;
    --keep) KEEP=1; shift ;;
    -h|--help) sed -n '2,57p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) echo "unknown flag: $1" >&2; exit 2 ;;
  esac
done

mkdir -p "$OUT"

cat <<'BANNER'
┌───────────────────────────────────────────────────────────────────────────┐
│ warmpool-ab.sh has NOT completed a green run on a cluster yet.            │
│ Its output format is a proposal. Do not quote a number from this script   │
│ as a result until a run has completed green and this banner is removed.   │
└───────────────────────────────────────────────────────────────────────────┘
BANNER

# shellcheck disable=SC2329 # invoked by the EXIT trap
cleanup() {
  [ -n "$SERVER_PID" ] && kill "$SERVER_PID" 2>/dev/null
  if [ "$KEEP" = "0" ]; then
    k3d cluster delete "$CLUSTER" >/dev/null 2>&1
    docker rm -f leoflow-soak-ab-postgres >/dev/null 2>&1
  fi
  echo; log "evidence: $OUT"
}
# A signal only exits, with 2, and the EXIT trap tears down once: trapping INT
# and TERM with cleanup itself tore down and then carried on with the script.
trap cleanup EXIT
trap 'exit 2' INT TERM

log "preflight"
for tool in k3d kubectl docker go jq curl python3 migrate; do
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
# The server does not migrate its own database: the schema comes from
# migrations/ through golang-migrate, as in the k3d CI jobs. pg_isready can
# answer on the bootstrap postmaster of a fresh volume (see soak.sh), so the
# migration is retried rather than trusted on the first attempt.
migrated=0
for _ in $(seq 1 10); do
  if migrate -path "$ROOT/migrations" -database "$DB_URL" up > "$OUT/migrate.log" 2>&1; then
    migrated=1; break
  fi
  sleep 2
done
[ "$migrated" = 1 ] || { tail -5 "$OUT/migrate.log" >&2; die "migrating the A/B database failed"; }
ok "postgres on 127.0.0.1:${PG_PORT}, migrated"

log "building binaries and the two DAG images"
go build -o "$OUT/leoflow" ./cmd/dexaflow || die "building leoflow"
go build -o "$OUT/leoflow-server" ./cmd/dexaflow-server || die "building leoflow-server"
go build -o "$OUT/leoflow-agent" ./cmd/dexaflow-agent || die "building leoflow-agent"
# The two DAGs are the same shapes the Lite soak runs, trimmed to the pair that
# makes the operator-mix comparison meaningful: one native python DAG and one
# airflow_operator DAG. They are built the way e2e.sh builds its generated
# Dockerfile leg: FROM the base image built here from runtime/Dockerfile, for the
# host's platform, then imported into the cluster.
docker build --provenance=false -q -f "$ROOT/runtime/Dockerfile" \
  --build-arg "PYTHON_VERSION=${PY_VERSION}" -t "$BASE_IMAGE" "$ROOT" >/dev/null \
  || die "building the base image $BASE_IMAGE failed"
case "$(uname -m)" in arm64|aarch64) HOST_PLATFORM="linux/arm64" ;; *) HOST_PLATFORM="linux/amd64" ;; esac
WS="$OUT/workspace"; mkdir -p "$WS"
images=("$BASE_IMAGE")
for dag in $DAGS; do
  cp -R "$ROOT/test/soak/dags/$dag" "$WS/"
  printf '\nbase_image: %s\n' "$BASE_IMAGE" >> "$WS/$dag/dexaflow.yaml"
  "$OUT/leoflow" compile "$WS/$dag" --image "$(dag_image "$dag")" --build \
    --platform "$HOST_PLATFORM" -o "$WS/$dag/dag.json" > "$OUT/compile-$dag.log" 2>&1 \
    || { tail -20 "$OUT/compile-$dag.log" >&2; die "compiling and building $dag failed"; }
  images+=("$(dag_image "$dag")")
done
k3d_import "$CLUSTER" "${images[@]}" || die "importing the images into $CLUSTER failed"
ok "images built and imported: ${images[*]}"

: > "$OUT/blocks.jsonl"

log "running $BLOCKS interleaved blocks of $RUNS_PER_BLOCK runs ($WARMUP_RUNS discarded per block)"
for i in $(seq 1 "$BLOCKS"); do
  if [ $((i % 2)) -eq 1 ]; then arm=off; else arm=on; fi
  log "block $i: warm pools $arm"
  if ! start_server "$arm"; then
    tail -20 "$OUT/server-$arm.log" >&2
    die "control plane did not boot for arm $arm (log above, full copy in $OUT/server-$arm.log)"
  fi
  if [ -z "$TOKEN" ]; then
    register_dags || die "registering the DAGs failed"
    ok "$DAGS registered and unpaused"
  fi
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
       extract(epoch from ti.queued_at) AS queued_at, ti.warm_worker_id, ti.state
FROM task_instances ti
JOIN dag_runs r ON r.id = ti.dag_run_id
JOIN dags d     ON d.id = r.dag_id
WHERE ti.started_at IS NOT NULL AND ti.queued_at IS NOT NULL
ORDER BY ti.queued_at
SQL

analyze "$OUT" | tee "$OUT/summary.md"
code="${PIPESTATUS[0]}"
log "done. Raw per-task latencies in $OUT/task-latencies.psv, block timeline in $OUT/blocks.jsonl"
exit "$code"
