#!/usr/bin/env bash
# Skip-version upgrade on a seeded database (#2311).
#
# Production installs do not take every weekly patch. This upgrades a database
# from the oldest release the upgrade guide supports straight to the checked
# out commit, all the migrations in between in one step, on a production-sized
# history, and asserts:
#   1. every migration finishes under MIGRATION_BOUND_SECONDS (300 s);
#   2. no index is left INVALID;
#   3. the schema equals a fresh install of this commit (`pg_dump -s` diff);
#   4. the control plane boots on it and becomes ready;
#   5. a DAG deployed after the upgrade runs to success, and the seeded
#      history is still served.
#
# The old schema comes from the old release's own migration files
# (`git archive <from> migrations`), applied by this commit's migrator, the
# same SQL its migration Job applied. The seed (test/upgrade/seed.sql) uses
# only the columns that schema has.
#
# Needs Postgres 16 reachable at PG_URL as a user that may create databases,
# pg_dump and psql of the same major version, go, python3, jq and curl. It
# creates and drops the databases upgrade_skip and upgrade_fresh. Run from the
# repo root:
#   bash test/upgrade/skip-version.sh [from-tag]
# Environment: PG_URL (postgres://leoflow:leoflow@localhost:5432/postgres),
# FROM (default OLDEST_SUPPORTED below), DAGS (1000), RUNS_PER_DAG (1000),
# TASKS_PER_RUN (5), HISTORY_EVERY (10), AUDIT_ROWS (1000000),
# MIGRATION_BOUND_SECONDS (300), KEEP=1 keeps the databases.
set -euo pipefail

# The oldest release the upgrade guide supports upgrading from in one step:
# the first GA of the previous minor. Move it when a minor ships, with the
# "Supported upgrade paths" section of website/content/operate/upgrades.md.
OLDEST_SUPPORTED=v0.5.0

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT"
FROM="${1:-${FROM:-$OLDEST_SUPPORTED}}"
PG_URL="${PG_URL:-postgres://leoflow:leoflow@localhost:5432/postgres?sslmode=disable}"
BOUND="${MIGRATION_BOUND_SECONDS:-300}"
PORT="${PORT:-18093}"
BASE="http://127.0.0.1:${PORT}"
TMP="$(mktemp -d)"
SERVER_PID=""

db_url() { # $1=database
  python3 -c 'import sys, urllib.parse as u; p = u.urlsplit(sys.argv[1]); print(u.urlunsplit(p._replace(path="/" + sys.argv[2])))' "$PG_URL" "$1"
}
SKIP_URL="$(db_url upgrade_skip)"
FRESH_URL="$(db_url upgrade_fresh)"

log() { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
pass() { printf '  \033[1;32mPASS\033[0m %s\n' "$*"; }
fail() { printf '  \033[1;31mFAIL\033[0m %s\n' "$1" >&2; [ -n "${2:-}" ] && tail -60 "$2" >&2; exit 1; }
cleanup() {
  [ -n "$SERVER_PID" ] && kill "$SERVER_PID" 2>/dev/null || true
  if [ "${KEEP:-0}" != 1 ]; then
    psql -q "$PG_URL" -c 'DROP DATABASE IF EXISTS upgrade_skip WITH (FORCE)' -c 'DROP DATABASE IF EXISTS upgrade_fresh WITH (FORCE)' >/dev/null 2>&1 || true
  fi
  chmod -R u+w "$TMP" 2>/dev/null || true
  rm -rf "$TMP"
}
trap cleanup EXIT

for tool in go psql pg_dump python3 jq curl git; do
  command -v "$tool" >/dev/null || fail "$tool is required"
done
git rev-parse -q --verify "refs/tags/$FROM" >/dev/null || git fetch -q origin "refs/tags/$FROM:refs/tags/$FROM" \
  || fail "tag $FROM is not available; fetch the tags"

log "building this commit ($(git rev-parse --short HEAD)) and taking the migrations of $FROM"
go build -o "$TMP/dexaflow-migrate" ./cmd/dexaflow-migrate
go build -o "$TMP/leoflow" ./cmd/dexaflow
go build -o "$TMP/leoflow-server" ./cmd/dexaflow-server
go build -o "$TMP/leoflow-agent" ./cmd/dexaflow-agent
mkdir -p "$TMP/old"
git archive "$FROM" migrations | tar -x -C "$TMP/old"

for db in upgrade_skip upgrade_fresh; do
  psql -q "$PG_URL" -c "DROP DATABASE IF EXISTS $db WITH (FORCE)" -c "CREATE DATABASE $db" >/dev/null
done

log "migrating upgrade_skip to the schema of $FROM"
"$TMP/dexaflow-migrate" -path "$TMP/old/migrations" -database "$SKIP_URL" up >"$TMP/old-up.log" 2>&1 \
  || fail "the migrations of $FROM did not apply" "$TMP/old-up.log"
from_version="$(psql -qAt "$SKIP_URL" -c 'SELECT version FROM schema_migrations')"
pass "schema of $FROM at migration $from_version"

log "seeding ${DAGS:-1000} DAGs x ${RUNS_PER_DAG:-1000} runs x ${TASKS_PER_RUN:-5} tasks"
start=$(date +%s)
psql -q "$SKIP_URL" -v dags="${DAGS:-1000}" -v runs_per_dag="${RUNS_PER_DAG:-1000}" \
  -v tasks_per_run="${TASKS_PER_RUN:-5}" -v history_every="${HISTORY_EVERY:-10}" \
  -v audit_rows="${AUDIT_ROWS:-1000000}" -f test/upgrade/seed.sql >"$TMP/seed.log" 2>&1 \
  || fail "seeding failed" "$TMP/seed.log"
read -r runs tis <<<"$(psql -qAt -F ' ' "$SKIP_URL" -c 'SELECT (SELECT count(*) FROM dag_runs), (SELECT count(*) FROM task_instances)')"
pass "seeded $runs runs and $tis task instances in $(( $(date +%s) - start )) s ($(psql -qAt "$SKIP_URL" -c "SELECT pg_size_pretty(pg_database_size(current_database()))"))"

log "upgrading $FROM -> $(git describe --tags --always) in one step"
start=$(date +%s)
"$TMP/dexaflow-migrate" -path migrations -database "$SKIP_URL" up >"$TMP/upgrade.log" 2>&1 \
  || fail "the upgrade failed" "$TMP/upgrade.log"
total=$(( $(date +%s) - start ))
to_version="$(psql -qAt "$SKIP_URL" -c 'SELECT version FROM schema_migrations')"
# The migrator prints one "NNN/u name (duration)" line per migration.
python3 - "$TMP/upgrade.log" "$BOUND" <<'PY' || fail "a migration ran past the bound" "$TMP/upgrade.log"
import re, sys

units = {"ns": 1e-9, "us": 1e-6, "µs": 1e-6, "ms": 1e-3, "s": 1, "m": 60, "h": 3600}
def seconds(d):
    return sum(float(n) * units[u] for n, u in re.findall(r"([0-9.]+)(ns|us|µs|ms|s|m|h)", d))

bound = float(sys.argv[2])
rows = []
for line in open(sys.argv[1]):
    m = re.match(r"\s*(\d+)/u (\S+) \((.+)\)\s*$", line)
    if m:
        rows.append((int(m.group(1)), m.group(2), seconds(m.group(3))))
if not rows:
    sys.exit("no migration lines in the migrator output")
print("  migration                                          seconds")
for v, name, s in rows:
    flag = "  OVER" if s > bound else ""
    print(f"  {v:03d} {name:<46} {s:8.2f}{flag}")
slow = [r for r in rows if r[2] > bound]
if slow:
    sys.exit(f"{len(slow)} migration(s) over {bound:.0f} s")
PY
pass "migrations $(( from_version + 1 )) to $to_version applied in $total s, each under $BOUND s"

invalid="$(psql -qAt "$SKIP_URL" -c 'SELECT indexrelid::regclass FROM pg_index WHERE NOT indisvalid')"
[ -z "$invalid" ] || fail "indexes left INVALID: $invalid"
pass "no INVALID index"

log "comparing the schema with a fresh install"
"$TMP/dexaflow-migrate" -path migrations -database "$FRESH_URL" up >"$TMP/fresh.log" 2>&1 \
  || fail "the fresh install failed" "$TMP/fresh.log"
dump() { pg_dump --schema-only --no-owner --no-privileges --no-comments "$1" | grep -v '^\\\(un\)\?restrict ' | grep -v '^--' | sed '/^$/d'; }
dump "$SKIP_URL" >"$TMP/skip.sql"
dump "$FRESH_URL" >"$TMP/fresh.sql"
if ! diff -u "$TMP/fresh.sql" "$TMP/skip.sql" >"$TMP/schema.diff"; then
  fail "the upgraded schema differs from a fresh install" "$TMP/schema.diff"
fi
pass "the upgraded schema equals a fresh install ($(wc -l <"$TMP/skip.sql") lines)"

log "booting the control plane on the upgraded database"
# The server binary itself, not `dexaflow lite`: Lite only ever uses its own
# database. Loopback without authentication, subprocess executor, no Redis
# (the embedded datastore).
# The parser compiles the probe; the runtime and the parser's airflow shim
# run its task in the agent.
export PYTHONPATH="${PYTHONPATH:+$PYTHONPATH:}$ROOT/parser:$ROOT/runtime/python:$ROOT/parser/leoflow_parser/_shim"
export HOME="$TMP/home"
export PATH="$TMP:$PATH"
mkdir -p "$HOME" "$TMP/ws/upgrade_probe"
cat >"$TMP/ws/upgrade_probe/dexaflow.yaml" <<'YAML'
schema_version: "1.0"
dag_id: upgrade_probe
YAML
cat >"$TMP/ws/upgrade_probe/dag.py" <<'PY'
from airflow.sdk import DAG, task


@task
def after_upgrade() -> str:
    return "ran after a skip-version upgrade"


with DAG("upgrade_probe", schedule=None):
    after_upgrade()
PY
"$TMP/leoflow" compile "$TMP/ws/upgrade_probe" --output "$TMP/probe.json" >"$TMP/compile.log" 2>&1 \
  || fail "compiling the probe DAG failed" "$TMP/compile.log"
LEOFLOW_SERVER_HTTP_ADDR="127.0.0.1:${PORT}" \
LEOFLOW_SERVER_GRPC_ADDR="127.0.0.1:$((PORT + 1000))" \
LEOFLOW_SERVER_METRICS_ADDR="127.0.0.1:$((PORT + 1001))" \
LEOFLOW_DATABASE_URL="$SKIP_URL" \
LEOFLOW_AUTH_DEV_NO_AUTH=true \
LEOFLOW_AUTH_JWT_SECRET="upgrade-test-insecure-jwt-secret-not-for-use" \
LEOFLOW_SECRET_KEY="upgrade-test-insecure-secret-32b" \
LEOFLOW_EXECUTOR_TYPE=subprocess \
LEOFLOW_LOGS_DIR="$TMP/logs" \
  "$TMP/leoflow-server" >"$TMP/server.log" 2>&1 &
SERVER_PID=$!
disown "$SERVER_PID" 2>/dev/null || true
for _ in $(seq 1 300); do
  curl -fsS "$BASE/readyz" >/dev/null 2>&1 && break
  kill -0 "$SERVER_PID" 2>/dev/null || fail "the control plane exited" "$TMP/server.log"
  sleep 1
done
curl -fsS "$BASE/readyz" >/dev/null 2>&1 || fail "the control plane never became ready" "$TMP/server.log"
pass "the control plane is ready on the upgraded database"

code="$(curl -sS -o "$TMP/register.json" -w '%{http_code}' -X POST "$BASE/api/v2/dags/upgrade_probe/versions" \
  -H 'content-type: application/json' --data-binary @"$TMP/probe.json")"
case "$code" in 2*) ;; *) fail "registering the probe DAG returned $code: $(cat "$TMP/register.json")" "$TMP/server.log" ;; esac
curl -fsS -X PATCH "$BASE/api/v2/dags/upgrade_probe" -H 'content-type: application/json' -d '{"is_paused":false}' >/dev/null \
  || fail "unpausing the probe DAG failed" "$TMP/server.log"
run_id="$(curl -fsS -X POST "$BASE/api/v2/dags/upgrade_probe/dagRuns" -H 'content-type: application/json' -d '{}' | jq -r .dag_run_id)"
[ -n "$run_id" ] && [ "$run_id" != null ] || fail "no dag_run_id returned" "$TMP/server.log"
state=""
for _ in $(seq 1 180); do
  state="$(curl -fsS "$BASE/api/v2/dags/upgrade_probe/dagRuns/$run_id" | jq -r .state)"
  case "$state" in success|failed) break ;; esac
  sleep 1
done
if [ "$state" != success ]; then curl -sS "$BASE/api/v2/dags/upgrade_probe/dagRuns/$run_id/taskInstances/after_upgrade/logs/1" | tail -c 3000 >&2; fail "the probe run ended $state" "$TMP/server.log"; fi
pass "a DAG deployed after the upgrade ran to success"

seeded="$(curl -fsS "$BASE/api/v2/dags/upgrade_seed_1/dagRuns?limit=1" | jq -r .total_entries)"
[ "$seeded" = "${RUNS_PER_DAG:-1000}" ] || fail "upgrade_seed_1 lists $seeded runs, want ${RUNS_PER_DAG:-1000}" "$TMP/server.log"
pass "the seeded history is served ($seeded runs of upgrade_seed_1)"

echo
echo "  skip-version upgrade $FROM -> $(git describe --tags --always): $runs runs, $tis task instances, migrations in $total s"
