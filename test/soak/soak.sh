#!/usr/bin/env bash
#
# soak.sh: the Leoflow long-running resilience battery.
#
# One command provisions everything, runs a realistic scheduled workload, asserts
# invariants continuously, collects the evidence and tears down. It is safe to
# leave unattended: it has a wall-clock ceiling, a disk budget and a kill switch,
# and every artifact it produces is written as it goes, not at the end.
#
#   bash test/soak/soak.sh                          # 30 min, no faults
#   bash test/soak/soak.sh --duration 12h --faults standard
#   bash test/soak/soak.sh --duration 6m --faults selftest-red   # must go RED
#
# What it runs: test/soak/dags, six DAG projects on cron schedules, on
# `leoflow lite` with the subprocess executor against a dedicated Postgres
# container. No cloud, no cluster, no paid service. See test/soak/README.md for
# the design, the measurements and the cost budget.
#
# Exit codes: 0 every invariant held; 1 at least one violation; 2 the harness
# itself could not run.
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
SOAK_DIR="$ROOT/test/soak"
COMPOSE="$SOAK_DIR/docker-compose.soak.yaml"

# ── Defaults. Every one of these is a budget decision; see README.md. ─────────
DURATION="30m"
FAULTS="none"
MODE="soak"
LABEL="soak"
API_PORT="${SOAK_API_PORT:-18700}"
FIXTURE_PORT="${SOAK_FIXTURE_PORT:-18701}"
PG_PORT="${SOAK_PG_PORT:-55433}"
SAMPLE_INTERVAL="${SOAK_SAMPLE_INTERVAL:-10s}"
ROWS="${SOAK_ROWS:-400000}"
FANOUT_ROWS="${SOAK_FANOUT_ROWS:-200000}"
LONG_SECONDS="${SOAK_LONG_SECONDS:-420}"
TOKEN_SECONDS="${SOAK_TOKEN_SECONDS:-2400}"
# Empty means "leave the server default alone" (24h). A duration here lowers the
# ceiling past which the control plane STOPS renewing an attempt's credential,
# which is the only way to reach that bound in a run short enough to watch. See
# --mode credential-ceiling.
CREDENTIAL_CEILING="${SOAK_CREDENTIAL_CEILING:-}"
MAX_DB_BYTES="${SOAK_MAX_DB_BYTES:-$((4 * 1024 * 1024 * 1024))}"
MAX_DATA_BYTES="${SOAK_MAX_DATA_BYTES:-$((4 * 1024 * 1024 * 1024))}"
MIN_FREE_BYTES="${SOAK_MIN_FREE_BYTES:-$((10 * 1024 * 1024 * 1024))}"
OUT_ROOT="${SOAK_OUT_ROOT:-$ROOT/.soak}"
KEEP_DB=0
PURGE=0
FRESH=1

usage() {
  sed -n '2,30p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
  cat <<'USAGE'

Flags:
  --duration D      wall-clock ceiling as Ns/Nm/Nh/Nd (default 30m, hard max 72h)
  --faults PLAN     none | standard | selftest-red | "kind:dur:offset,..."
  --mode MODE       soak (default) | credential-ceiling. credential-ceiling
                    lowers auth.max_attempt_credential_lifetime and requires
                    soak_token to FAIL because its credential lapsed. It is an
                    inverted run: green means the bound was NOT enforced.
  --label NAME      label recorded in every sample (default soak)
  --out DIR         evidence directory (default .soak/<timestamp>)
  --keep-db         do not stop the Postgres container on teardown
  --purge           also delete the Postgres volume on teardown (destroys history)
  --no-fresh        keep the existing soak database instead of resetting it
  --rows N          rows the ingest DAG generates per run (default 400000)
  -h, --help        this text

Fault kinds:
  pgpause:DUR       docker pause the soak Postgres for DUR, then unpause
  sigstop:DUR       SIGSTOP the control-plane process for DUR, then SIGCONT
  restart           SIGKILL the control plane and start it again
  overrun:DUR       declare a 45s fault window but pause Postgres for DUR.
                    This is the deliberately-red self test: a real fault that
                    outlives the window the harness claimed for it.
USAGE
}

while [ $# -gt 0 ]; do
  case "$1" in
    --duration) DURATION="$2"; shift 2 ;;
    --faults)   FAULTS="$2"; shift 2 ;;
    --mode)     MODE="$2"; shift 2 ;;
    --label)    LABEL="$2"; shift 2 ;;
    --out)      OUT_DIR="$2"; shift 2 ;;
    --rows)     ROWS="$2"; shift 2 ;;
    --keep-db)  KEEP_DB=1; shift ;;
    --purge)    PURGE=1; shift ;;
    --no-fresh) FRESH=0; shift ;;
    -h|--help)  usage; exit 0 ;;
    *) echo "unknown flag: $1" >&2; usage; exit 2 ;;
  esac
done

OUT_DIR="${OUT_DIR:-$OUT_ROOT/$(date -u +%Y%m%dT%H%M%SZ)-$LABEL}"
mkdir -p "$OUT_DIR" || { echo "cannot create $OUT_DIR" >&2; exit 2; }
FAULTS_FILE="$OUT_DIR/faults.jsonl"
: > "$FAULTS_FILE"

# `leoflow lite` sets LEOFLOW_DATABASE_URL itself, unconditionally, to the DSN it
# derives from the running user's HOME (internal/cli/dev.go sharedServerEnv +
# internal/cli/dsn.go). A soak therefore cannot hand Lite a database URL. What it
# CAN do is give Lite a private HOME containing a `db-port` file, which is the
# supported knob Lite uses to find its datastore. That is how the soak gets its
# own Postgres on its own port without touching the developer's leoflow_dev.
#
# The private HOME is persistent on purpose: the per-DAG venvs cost minutes and
# gigabytes to rebuild, and a weekend battery should not pay that on every start.
SOAK_HOME="${SOAK_HOME:-$HOME/.leoflow-soak}"
mkdir -p "$SOAK_HOME/.leoflow/dev"
printf '%s' "$PG_PORT" > "$SOAK_HOME/.leoflow/dev/db-port"
export SOAK_DATA_DIR="${SOAK_DATA_DIR:-$SOAK_HOME/data}"
mkdir -p "$SOAK_DATA_DIR"

WORKSPACE="$SOAK_HOME/workspace"
DB_URL="postgres://leoflow:leoflow@127.0.0.1:${PG_PORT}/leoflow_dev?sslmode=disable"
API="http://127.0.0.1:${API_PORT}"

LITE_PID=""; FIXTURE_PID=""; MONITOR_PID=""; FAULT_PID=""; WATCHDOG_PID=""
VERDICT_CODE=2
# Teardown only tears down what THIS invocation started. Without that, a second
# harness that dies in preflight (a port already taken, docker not running)
# still runs the trap, and `docker compose down` would stop the Postgres of the
# soak that is already running, at hour 30 of a weekend, from a process that
# never provisioned anything. Scheduled runs make that collision routine.
OWNS_STACK=0

log()  { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
ok()   { printf '\033[1;32m    ok\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m    !!\033[0m %s\n' "$*" >&2; }
err()  { printf '\033[1;31m    xx\033[0m %s\n' "$*" >&2; }
die()  { printf '\033[1;31mFATAL:\033[0m %s\n' "$*" >&2; exit 2; }

# ── Teardown. Idempotent, runs on every exit path including the kill switch. ──
cleanup() {
  local code=$?
  log "tearing down"
  # Resolve the control plane's children FIRST. `leoflow lite` supervises the
  # leoflow-server that actually holds the API port, the database connections and
  # the scheduler; once the supervisor is killed its child is reparented and
  # pgrep -P can no longer find it, so an orphaned server would survive the
  # harness. A CONT goes with the TERM because a fault injector interrupted
  # mid-SIGSTOP leaves that child stopped, and a stopped process never acts on a
  # SIGTERM at all.
  local children=""
  [ -n "$LITE_PID" ] && children="$(pgrep -P "$LITE_PID" 2>/dev/null | tr '\n' ' ')"
  for pid in $children; do
    kill -CONT "$pid" 2>/dev/null
  done
  for pid in "$WATCHDOG_PID" "$FAULT_PID" "$MONITOR_PID" "$LITE_PID" "$FIXTURE_PID" $children; do
    [ -n "$pid" ] && kill "$pid" 2>/dev/null
  done
  # A paused container would survive the harness and hold its memory, so always
  # unpause before stopping, whatever state the fault injector left it in. Only
  # when this invocation owns the container: unpausing another run's database
  # mid-fault would corrupt ITS experiment, not ours.
  [ "$OWNS_STACK" = "1" ] && docker unpause leoflow-soak-postgres >/dev/null 2>&1
  sleep 1
  for pid in "$MONITOR_PID" "$LITE_PID" "$FIXTURE_PID" $children; do
    [ -n "$pid" ] && kill -9 "$pid" 2>/dev/null
  done
  collect_evidence
  if [ "$OWNS_STACK" = "1" ] && [ "$KEEP_DB" = "0" ]; then
    if [ "$PURGE" = "1" ]; then
      docker compose -f "$COMPOSE" down -v >/dev/null 2>&1 && ok "postgres stopped and volume removed"
    else
      docker compose -f "$COMPOSE" down >/dev/null 2>&1 && ok "postgres stopped (volume kept; --purge removes it)"
    fi
  fi
  echo
  log "evidence: $OUT_DIR"
  [ -f "$OUT_DIR/summary.md" ] && sed -n '1,6p' "$OUT_DIR/summary.md"
  exit "$([ "$VERDICT_CODE" != 2 ] && echo "$VERDICT_CODE" || echo "$code")"
}
trap cleanup EXIT INT TERM

collect_evidence() {
  # Everything here is best-effort: collection must never be the reason a soak
  # loses the report it already wrote.
  {
    echo "# environment"
    echo "date_utc=$(date -u +%FT%TZ)"
    echo "uname=$(uname -srm)"
    echo "go=$(go version 2>/dev/null)"
    echo "docker=$(docker --version 2>/dev/null)"
    echo "leoflow_commit=$(git -C "$ROOT" rev-parse --short HEAD 2>/dev/null)"
    echo "duration=$DURATION faults=$FAULTS label=$LABEL"
    echo "rows=$ROWS fanout_rows=$FANOUT_ROWS long_seconds=$LONG_SECONDS"
  } > "$OUT_DIR/environment.txt" 2>/dev/null
  # The metrics listener, not the API port: Lite serves /metrics on --port + 1010
  # (internal/cli/dev.go devMetricsPort) and the API router deliberately does not
  # serve it at all (internal/api/server.go).
  curl -fsS --max-time 5 "http://127.0.0.1:$((API_PORT + 1010))/metrics" > "$OUT_DIR/metrics-final.prom" 2>/dev/null
  curl -fsS --max-time 5 "http://127.0.0.1:${FIXTURE_PORT}/fixture/stats" > "$OUT_DIR/fixture-stats.json" 2>/dev/null
  docker exec leoflow-soak-postgres psql -U leoflow -d leoflow_dev -At -c \
    "SELECT relname, n_live_tup, pg_total_relation_size(relid) FROM pg_stat_user_tables ORDER BY 3 DESC" \
    > "$OUT_DIR/pg-table-sizes.txt" 2>/dev/null
  # Every tree the soak writes into, including the two the first cost budget
  # omitted: Lite's task logs and the evidence directory itself.
  du -sk "$SOAK_DATA_DIR" "$SOAK_HOME/.leoflow/dev/venvs" "$SOAK_HOME/.leoflow/dev/logs" "$OUT_DIR" \
    2>/dev/null > "$OUT_DIR/disk-usage.txt"
}

# ── Preflight. Refuse early and loudly rather than half-provisioning. ─────────
log "preflight"
for tool in docker go python3 curl jq; do
  command -v "$tool" >/dev/null || die "missing required tool: $tool"
done
docker info >/dev/null 2>&1 || die "docker is not running"
case "$PG_PORT" in
  ''|*[!0-9]*) die "SOAK_PG_PORT must be a positive integer, got '$PG_PORT'" ;;
esac
[ "$PG_PORT" -gt 0 ] || die "SOAK_PG_PORT must be a positive integer, got '$PG_PORT'"
export SOAK_PG_PORT="$PG_PORT"
# Lite derives its gRPC and metrics ports from --port (internal/cli/dev.go:
# +1011 and +1010) and refuses to boot if either is taken. The monitor reads two
# of its invariants from the metrics listener, so a busy port there is a run with
# two checks that cannot fire, which is worse than not starting.
METRICS_PORT=$((API_PORT + 1010))
GRPC_PORT_LITE=$((API_PORT + 1011))
for port in "$API_PORT" "$FIXTURE_PORT" "$PG_PORT" "$METRICS_PORT" "$GRPC_PORT_LITE"; do
  if lsof -nP -iTCP:"$port" -sTCP:LISTEN >/dev/null 2>&1; then
    die "port $port is already in use; set SOAK_API_PORT / SOAK_FIXTURE_PORT / SOAK_PG_PORT (Lite also takes --port +1010 for metrics and +1011 for gRPC)"
  fi
done
# The wall-clock ceiling is enforced twice: here, and inside the monitor. A soak
# that can outlive its own budget is not a bounded experiment.
DURATION_S="$(python3 -c '
import re,sys
m=re.fullmatch(r"(\d+)([smhd])", sys.argv[1])
if not m: sys.exit("bad --duration: use 30m, 12h, 2d")
n,u=int(m.group(1)),m.group(2)
print(n*{"s":1,"m":60,"h":3600,"d":86400}[u])
' "$DURATION")" || die "bad --duration $DURATION"
[ "$DURATION_S" -le 259200 ] || die "--duration exceeds the 72h ceiling"
# A live soak owns the API port, so the loop above has already refused (and
# refused without touching anything, because teardown is gated on ownership). A
# container that is up with the port free is the other case: a leftover from a
# harness that was SIGKILLed. Say so and adopt it, rather than blocking every
# scheduled run from then on.
if docker ps --filter name=leoflow-soak-postgres --filter status=running --format '{{.Names}}' 2>/dev/null | grep -q leoflow-soak-postgres; then
  warn "leoflow-soak-postgres is already running and no soak holds $API_PORT: adopting it as a leftover from an interrupted run"
fi
ok "preflight passed (duration ${DURATION_S}s)"

log "building binaries"
BIN="$SOAK_HOME/bin"; mkdir -p "$BIN"
go build -o "$BIN/leoflow" ./cmd/dexaflow || die "building leoflow"
go build -o "$BIN/leoflow-server" ./cmd/dexaflow-server || die "building leoflow-server"
go build -o "$BIN/leoflow-agent" ./cmd/dexaflow-agent || die "building leoflow-agent"
go build -o "$BIN/soak-fixture" ./test/soak/fixture || die "building the fixture server"
go build -o "$BIN/soak-monitor" ./test/soak/monitor || die "building the monitor"
export PATH="$BIN:$PATH"
ok "binaries in $BIN"

log "starting the soak Postgres (dedicated container, 1 CPU / 1 GiB)"
# --wait blocks on the compose healthcheck rather than on the container merely
# existing. That matters here: `postgres:16-alpine` runs initdb on a fresh volume
# behind a BOOTSTRAP postmaster, accepts on the Unix socket, then shuts it down
# and starts the real server. A readiness check that catches the bootstrap window
# reports ready a second or two too early.
docker compose -f "$COMPOSE" up -d --wait >/dev/null 2>&1 \
  || warn "docker compose --wait did not report healthy; falling back to the probe below"
# From here on this invocation owns the container and the teardown may stop it.
OWNS_STACK=1

# Belt and braces, because neither of the obvious probes is sufficient on its own:
# `pg_isready` answers on the bootstrap socket, and a TCP connect to the published
# port only proves docker-proxy is listening, which it does from container start.
# So the probe is a real query, and it has to succeed three times in a row.
pg_query_ok() { docker exec leoflow-soak-postgres psql -U leoflow -d leoflow_dev -At -c 'SELECT 1' >/dev/null 2>&1; }
# psql_q runs one read against the soak's own metadatabase and prints the value.
psql_q() { docker exec leoflow-soak-postgres psql -U leoflow -d leoflow_dev -At -c "$1" 2>/dev/null; }
streak=0
for _ in $(seq 1 120); do
  if pg_query_ok; then
    streak=$((streak + 1))
    [ "$streak" -ge 3 ] && break
  else
    streak=0
  fi
  sleep 1
done
if [ "$streak" -lt 3 ]; then
  docker logs leoflow-soak-postgres > "$OUT_DIR/postgres.log" 2>&1
  tail -20 "$OUT_DIR/postgres.log" >&2
  die "soak Postgres never answered three consecutive queries (container log above, full copy in $OUT_DIR/postgres.log)"
fi
# The warehouse the operator leg writes into: a separate database, so the
# workload never shares a table with Leoflow's own metadata and the two growth
# curves in the budget stay separable.
docker exec leoflow-soak-postgres psql -U leoflow -d postgres -At -c \
  "SELECT 1 FROM pg_database WHERE datname='soak_warehouse'" 2>/dev/null | grep -q 1 \
  || docker exec leoflow-soak-postgres createdb -U leoflow soak_warehouse >/dev/null 2>&1
ok "postgres up on 127.0.0.1:${PG_PORT}, warehouse database present"

export SOAK_DATABASE_URL="$DB_URL"

# ── Isolation, asserted BEFORE anything destructive runs. ────────────────────
#
# `leoflow db reset --yes` always drops the database named `leoflow_dev` on the
# port in <HOME>/.leoflow/dev/db-port (internal/cli/db.go dropDevDatabase ->
# devDSNs -> devDBPort), and it ignores LEOFLOW_DATABASE_URL entirely (#1185).
# So the only thing standing between this harness and the developer's own
# database is that file, under a HOME that is not the developer's. Re-reading
# the file the script just wrote proves nothing; these four checks are the ones
# that can actually fail, and they run before the drop, not after it.
assert_isolation() {
  [ "$SOAK_HOME" != "$HOME" ] \
    || die "isolation: SOAK_HOME is the developer's own HOME; a reset here would drop the developer's leoflow_dev"
  local written
  written="$(cat "$SOAK_HOME/.leoflow/dev/db-port" 2>/dev/null || true)"
  [ "$written" = "$PG_PORT" ] \
    || die "isolation: $SOAK_HOME/.leoflow/dev/db-port reads '$written', not '$PG_PORT'; a reset would target whatever that resolves to (an unreadable file falls back to the default dev port)"
  # The developer's own datastore must not be on the port we are about to reset.
  if [ -f "$HOME/.leoflow/dev/db-port" ]; then
    local devport; devport="$(cat "$HOME/.leoflow/dev/db-port" 2>/dev/null || true)"
    [ "$devport" != "$PG_PORT" ] \
      || die "isolation: the developer's own datastore is on port $PG_PORT; choose another with SOAK_PG_PORT"
  fi
  # And the thing listening on that port must be the soak container, not some
  # other Postgres that happens to answer there.
  local published
  published="$(docker inspect -f '{{range $p, $conf := .NetworkSettings.Ports}}{{range $conf}}{{.HostPort}} {{end}}{{end}}' leoflow-soak-postgres 2>/dev/null || true)"
  case " $published " in
    *" $PG_PORT "*) : ;;
    *) die "isolation: leoflow-soak-postgres does not publish $PG_PORT (it publishes '$published'); something else answers there" ;;
  esac
}
assert_isolation
ok "isolation asserted: private HOME, db-port $PG_PORT, published by leoflow-soak-postgres"

if [ "$FRESH" = "1" ]; then
  log "resetting the soak database (under the private HOME, so the developer's leoflow_dev is untouched)"
  # The output is captured rather than discarded: a fatal that will not say why
  # costs a whole CI round trip to diagnose, which is exactly what happened the
  # first time this ran on a clean runner.
  # Retried, because the probe above speaks to the container socket while this
  # speaks to the published port through `localhost`, and only the second one is
  # the path Lite will use. A retry is cheaper than a class of flake that only
  # shows up on a cold machine.
  reset_ok=0
  for attempt in 1 2 3 4 5; do
    if HOME="$SOAK_HOME" "$BIN/leoflow" db reset --yes > "$OUT_DIR/db-reset.log" 2>&1; then
      reset_ok=1
      break
    fi
    warn "leoflow db reset attempt $attempt failed; retrying"
    sleep 3
  done
  if [ "$reset_ok" = "0" ]; then
    tail -20 "$OUT_DIR/db-reset.log" >&2
    docker logs leoflow-soak-postgres > "$OUT_DIR/postgres.log" 2>&1
    die "leoflow db reset failed after 5 attempts (log above, full copies in $OUT_DIR)"
  fi
  ok "database migrated and empty"
else
  HOME="$SOAK_HOME" "$BIN/leoflow" db migrate > "$OUT_DIR/db-migrate.log" 2>&1 || true
  ok "database kept (--no-fresh)"
fi
# Re-asserted after the reset, because `db reset` recreates the dev directory and
# a regression there would send everything after this line somewhere else.
assert_isolation

log "materializing the workspace"
rm -rf "$WORKSPACE"; mkdir -p "$WORKSPACE"
cp -R "$SOAK_DIR/dags/." "$WORKSPACE/"
ok "$(find "$WORKSPACE" -name leoflow.yaml | wc -l | tr -d ' ') DAG projects in $WORKSPACE"

log "starting the local HTTP fixture on 127.0.0.1:${FIXTURE_PORT}"
"$BIN/soak-fixture" --addr "127.0.0.1:${FIXTURE_PORT}" > "$OUT_DIR/fixture.log" 2>&1 &
FIXTURE_PID=$!
for _ in $(seq 1 30); do curl -fsS "http://127.0.0.1:${FIXTURE_PORT}/healthz" >/dev/null 2>&1 && break; sleep 0.3; done
curl -fsS "http://127.0.0.1:${FIXTURE_PORT}/healthz" >/dev/null 2>&1 || die "fixture did not start"
ok "fixture serving deterministic JSON on loopback only"

start_lite() {
  # HOME points at the soak home so Lite provisions its venvs there, reads no
  # developer config.yaml (hence loopback no-auth), and resolves its datastore
  # through the db-port file written above.
  # `env` with an array, not a prefix of assignments. A conditional
  # ${VAR:+NAME=value} in the middle of an assignment prefix ends the prefix when
  # it expands to nothing, so bash treats the NEXT assignment as the command
  # name: with no credential ceiling set, which is the default, Lite never
  # started and the whole soak died at boot with "PYTHONPATH=...: No such file
  # or directory". An array holds the optional entry without that hazard.
  local envv=(
    "HOME=$SOAK_HOME"
    "SOAK_DATA_DIR=$SOAK_DATA_DIR"
    "SOAK_ROWS=$ROWS"
    "SOAK_FANOUT_ROWS=$FANOUT_ROWS"
    "SOAK_LONG_SECONDS=$LONG_SECONDS"
    "SOAK_TOKEN_SECONDS=$TOKEN_SECONDS"
    "PYTHONPATH=${PYTHONPATH:-$ROOT/parser}"
  )
  [ -n "$CREDENTIAL_CEILING" ] && envv+=("LEOFLOW_AUTH_MAX_ATTEMPT_CREDENTIAL_LIFETIME=$CREDENTIAL_CEILING")
  env "${envv[@]}" \
    "$BIN/leoflow" lite --no-up --executor subprocess --port "$API_PORT" "$WORKSPACE" \
      >> "$OUT_DIR/lite.log" 2>&1 &
  LITE_PID=$!
  for _ in $(seq 1 900); do
    curl -fsS "$API/readyz" >/dev/null 2>&1 && return 0
    kill -0 "$LITE_PID" 2>/dev/null || return 1
    sleep 1
  done
  return 1
}

log "booting Lite (subprocess executor). First boot provisions one venv per DAG and can take minutes."
start_lite || { tail -60 "$OUT_DIR/lite.log"; die "Lite did not become ready"; }
ok "control plane ready at $API"

log "registering managed connections (both point at loopback)"
HOME="$SOAK_HOME" "$BIN/leoflow" connections set soak_http --server "$API" \
  --conn-type http --host 127.0.0.1 --port "$FIXTURE_PORT" --schema http >/dev/null 2>&1 \
  || warn "could not create soak_http; the operator leg will fail and the soak will say so"
HOME="$SOAK_HOME" "$BIN/leoflow" connections set soak_pg --server "$API" \
  --conn-type postgres --host 127.0.0.1 --port "$PG_PORT" \
  --login leoflow --password leoflow --schema soak_warehouse >/dev/null 2>&1 \
  || warn "could not create soak_pg; the operator leg will fail and the soak will say so"
ok "soak_http and soak_pg registered"

log "waiting for all six DAGs to register"
registered=0
for _ in $(seq 1 120); do
  registered="$(curl -fsS "$API/api/v2/dags?limit=100" 2>/dev/null | jq -r '[.dags[]?.dag_id] | map(select(startswith("soak_"))) | length' 2>/dev/null || echo 0)"
  [ "${registered:-0}" -ge 6 ] && break
  sleep 2
done
# Not fatal: a DAG that fails to compile is itself a finding, and the monitor's
# import_error and cadence_starved checks will say so with evidence. Blocking the
# whole battery on it would trade a reported failure for no report at all.
[ "${registered:-0}" -ge 6 ] || warn "only $registered soak DAGs registered; the import_error and cadence checks will report the gap"
ok "$registered soak DAGs registered"

# ── Fault plan. Windows are appended to faults.jsonl as they happen, so the
#    monitor sees them live and a killed harness still leaves the record. ──────
declare_window() { # kind start_epoch end_epoch note
  python3 -c '
import json,sys,datetime
k,s,e,n=sys.argv[1],float(sys.argv[2]),float(sys.argv[3]),sys.argv[4]
iso=lambda t: datetime.datetime.fromtimestamp(t, datetime.timezone.utc).isoformat().replace("+00:00","Z")
print(json.dumps({"kind":k,"start":iso(s),"end":iso(e),"note":n}))
' "$1" "$2" "$3" "$4" >> "$FAULTS_FILE"
}

control_plane_pid() {
  # `leoflow lite` supervises a leoflow-server child; the scheduler lives there,
  # so a fault aimed at the scheduler must find the child, not the supervisor.
  pgrep -P "$LITE_PID" -f leoflow-server 2>/dev/null | head -1
}

inject_fault() { # kind duration_seconds
  local kind="$1" dur="$2" start end
  start="$(date +%s)"
  case "$kind" in
    pgpause)
      declare_window "pgpause" "$start" "$((start + dur))" "docker pause of the soak Postgres"
      docker pause leoflow-soak-postgres >/dev/null 2>&1
      sleep "$dur"
      docker unpause leoflow-soak-postgres >/dev/null 2>&1
      ;;
    overrun)
      # The deliberately-red self test. A REAL fault, declared honestly short:
      # the harness claims a 45 s window and then keeps Postgres paused far
      # longer. Nothing is faked and no assertion is relaxed; the monitor simply
      # observes an outage nobody told it about and says so. This is the shape of
      # the incident the battery exists to catch: the one we thought was over.
      declare_window "overrun" "$start" "$((start + 45))" "declared 45s, actually ${dur}s"
      docker pause leoflow-soak-postgres >/dev/null 2>&1
      sleep "$dur"
      docker unpause leoflow-soak-postgres >/dev/null 2>&1
      ;;
    sigstop)
      local pid; pid="$(control_plane_pid)"
      if [ -z "$pid" ]; then warn "sigstop: no leoflow-server child found"; return; fi
      declare_window "sigstop" "$start" "$((start + dur))" "SIGSTOP of pid $pid"
      kill -STOP "$pid" 2>/dev/null
      sleep "$dur"
      kill -CONT "$pid" 2>/dev/null
      ;;
    restart)
      local pid; pid="$(control_plane_pid)"
      declare_window "restart" "$start" "$((start + 60))" "SIGKILL and restart of the control plane"
      [ -n "$pid" ] && kill -9 "$pid" 2>/dev/null
      kill "$LITE_PID" 2>/dev/null; sleep 3; kill -9 "$LITE_PID" 2>/dev/null
      sleep 2
      start_lite || warn "control plane did not come back after restart"
      ;;
    *) warn "unknown fault kind: $kind" ;;
  esac
  end="$(date +%s)"
  log "fault $kind ran for $((end - start))s"
}

# assert_credential_ceiling_enforced decides the inverted run. It reads the
# database rather than the log, because a log line is prose that a reword
# silently breaks while the task state and its error are what the system acted
# on.
assert_credential_ceiling_enforced() {
  local failed reason
  failed="$(psql_q "SELECT count(*) FROM task_instances ti
                    JOIN dag_runs r ON r.id = ti.dag_run_id
                    JOIN dags d ON d.id = r.dag_id
                    WHERE d.dag_id = 'soak_token' AND ti.state = 'failed'")"
  failed="${failed//[[:space:]]/}"
  if [ "${failed:-0}" = "0" ]; then
    err "credential-ceiling: soak_token never failed, with the renewal ceiling set to $CREDENTIAL_CEILING and its body at ${TOKEN_SECONDS}s."
    err "  The bound that stops renewing a runaway attempt's credential did not fire, so an attempt can hold a live credential past the ceiling."
    return 1
  fi
  # The reason has to be the credential. A timeout or a compile error would also
  # show as failed, and accepting either would make a green run meaningless.
  reason="$(psql_q "SELECT coalesce(string_agg(DISTINCT lower(ti.error_message), ' | '), '')
                    FROM task_instances ti
                    JOIN dag_runs r ON r.id = ti.dag_run_id
                    JOIN dags d ON d.id = r.dag_id
                    WHERE d.dag_id = 'soak_token' AND ti.state = 'failed'")"
  case "$reason" in
    *unauthenticated*|*unauthorized*|*permissiondenied*|*permission\ denied*|*token*|*credential*)
      log "credential-ceiling: soak_token failed $failed time(s) and the reason names the credential"
      printf '  reason: %s\n' "$reason"
      return 0 ;;
    *)
      err "credential-ceiling: soak_token failed $failed time(s), but not for a credential reason."
      err "  reason recorded: ${reason:-<empty>}"
      err "  A timeout or an import error passing here would make this assertion decorative, so it fails."
      return 1 ;;
  esac
}

fault_plan_for() {
  case "$1" in
    none) echo "" ;;
    # Spread across the run: an outage a quarter in, a frozen scheduler past
    # halfway, a hard restart near the end. Offsets are percentages of the run.
    standard) echo "pgpause:45:25 sigstop:45:55 restart:0:80" ;;
    # 300 s of real outage behind a 45 s declared window.
    selftest-red) echo "overrun:300:30" ;;
    *) echo "$1" | tr ',' ' ' | tr ':' ':' ;;
  esac
}

run_fault_plan() {
  local plan; plan="$(fault_plan_for "$FAULTS")"
  [ -z "$plan" ] && return 0
  local t0; t0="$(date +%s)"
  for spec in $plan; do
    local kind dur pct at
    kind="${spec%%:*}"; spec="${spec#*:}"
    dur="${spec%%:*}"; pct="${spec##*:}"
    at=$(( DURATION_S * pct / 100 ))
    while [ "$(( $(date +%s) - t0 ))" -lt "$at" ]; do
      sleep 2
      kill -0 "$MONITOR_PID" 2>/dev/null || return 0
    done
    log "injecting fault: $kind (${dur}s) at +$(( $(date +%s) - t0 ))s"
    inject_fault "$kind" "$dur"
  done
}

log "starting the monitor (samples every $SAMPLE_INTERVAL, ceiling $DURATION)"
"$BIN/soak-monitor" \
  --db "$DB_URL" --api "$API" --out "$OUT_DIR" \
  --duration "${DURATION_S}s" --interval "$SAMPLE_INTERVAL" \
  --metrics "http://127.0.0.1:${METRICS_PORT}" \
  --faults "$FAULTS_FILE" --data-dir "$SOAK_DATA_DIR,$SOAK_HOME/.leoflow/dev/logs,$OUT_DIR" --label "$LABEL" \
  --max-db-bytes "$MAX_DB_BYTES" --max-data-bytes "$MAX_DATA_BYTES" --min-free-bytes "$MIN_FREE_BYTES" \
  > "$OUT_DIR/monitor.log" 2>&1 &
MONITOR_PID=$!
ok "monitor pid $MONITOR_PID, evidence in $OUT_DIR"

# The kill switch: an independent watchdog that stops everything at the ceiling
# plus five minutes of grace, so a hung monitor cannot turn an unattended run
# into an open-ended one.
( sleep "$((DURATION_S + 300))"; kill -TERM $$ 2>/dev/null ) &
WATCHDOG_PID=$!

run_fault_plan &
FAULT_PID=$!

log "soaking. tail -f $OUT_DIR/monitor.log, or read $OUT_DIR/summary.md at any time."
wait "$MONITOR_PID"
VERDICT_CODE=$?
log "monitor exited with $VERDICT_CODE"

# The credential-ceiling run is INVERTED, and the inversion is the whole point.
# Every other mode passes when nothing went wrong. This one lowered
# auth.max_attempt_credential_lifetime below soak_token's runtime, so the bound
# that deliberately stops renewing an attempt's credential MUST have fired: a
# green run here means the ceiling was not enforced and a runaway attempt can
# keep a live credential forever, which is the thing the setting exists to
# prevent.
#
# It asserts the REASON and not just the failure. soak_token has retries off and
# an execution_timeout far above its body, so the only expected way for it to
# die is the credential, and a timeout or an import error passing as success
# here would make this check decorative.
if [ "$MODE" = "credential-ceiling" ]; then
  assert_credential_ceiling_enforced
  VERDICT_CODE=$?
fi
