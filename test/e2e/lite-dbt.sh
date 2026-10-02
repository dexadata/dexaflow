#!/usr/bin/env bash
# End-to-end gate for dbt on the LITE edition (subprocess executor) against duckdb
# — the credential-free focus adapter that had no e2e (#573). Pro dbt is covered
# by dbt-e2e.sh (k3d + Postgres); this is the Lite half: a duckdb dbt project must
# compile with the zero-config duckdb profile step (#575), run through the
# subprocess executor, and actually materialize its models in the duckdb file.
#
# What the unit tests can't prove is the wiring composed end to end: leoflow
# compiles the project with the `--dbt-default-duckdb` prefix + absolute
# --project-dir (#575), Lite provisions the per-DAG venv from `dependencies:`
# (so `dbt` resolves on PATH), and the task's `python -m leoflow_runtime
# --dbt-default-duckdb … && dbt …` command writes profiles.yml and materializes.
#
# The manifest is pre-parsed here (like dbt-e2e.sh) so Lite reads a baked
# target/manifest.json rather than depending on parse-on-save ordering.
#
# Needs a local Postgres (docker-compose.dev.yaml) and dbt-duckdb available to
# pre-parse. Run from the repo root:  bash test/e2e/lite-dbt.sh
set -euo pipefail

PORT=18096
DB_URL="${LEOFLOW_E2E_DB_URL:-postgres://leoflow:leoflow@localhost:5432/leoflow_dev?sslmode=disable}"
BASE="http://127.0.0.1:${PORT}"
DAG_ID="shopdbt"
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
TMP="$(mktemp -d)"
WS="$TMP/ws"
PROJ="$WS/$DAG_ID"
LITE_PID=""
pass() { printf '  \033[32mPASS\033[0m %s\n' "$1"; }
fail() { printf '  \033[31mFAIL\033[0m %s\n' "$1"; [ -n "${2:-}" ] && tail -60 "$2"; cleanup; exit 1; }
cleanup() {
  [ -n "$LITE_PID" ] && kill "$LITE_PID" 2>/dev/null || true
  chmod -R u+w "$TMP" 2>/dev/null || true
  rm -rf "$TMP"
}
trap cleanup EXIT

# Isolate HOME so Lite runs no-auth loopback and owns a clean per-DAG venv root.
export HOME="$TMP/home"; mkdir -p "$HOME"
export LEOFLOW_DATABASE_URL="$DB_URL"
export LEOFLOW_LOGS_DIR="$TMP/logs"
# leoflow lite spawns the parser via `python3 -m leoflow_parser`; put it on PYTHONPATH.
export PYTHONPATH="${PYTHONPATH:-$ROOT/parser}"

echo "==> building binaries"
go build -o "$TMP/leoflow" ./cmd/dexaflow
go build -o "$TMP/leoflow-server" ./cmd/dexaflow-server
go build -o "$TMP/leoflow-agent" ./cmd/dexaflow-agent
export PATH="$TMP:$PATH"

echo "==> a duckdb dbt project with NO profiles.yml (zero-config), plus dexaflow.yaml"
mkdir -p "$PROJ/models" "$PROJ/seeds"
cat >"$PROJ/dbt_project.yml" <<'YAML'
name: 'shop'
version: '1.0.0'
profile: 'shop'
model-paths: ["models"]
seed-paths: ["seeds"]
models: { shop: { +materialized: table } }
YAML
printf 'id,v\n1,10\n2,20\n3,30\n' >"$PROJ/seeds/raw.csv"
echo "select id, v from {{ ref('raw') }}" >"$PROJ/models/stg.sql"
echo "select id, sum(v) as total from {{ ref('stg') }} group by id" >"$PROJ/models/mart.sql"
cat >"$PROJ/dexaflow.yaml" <<YAML
schema_version: "1.0"
dag_id: ${DAG_ID}
dbt:
  project: .
  manifest: target/manifest.json
  granularity: node
# Lite installs these into the per-DAG venv, so \`dbt\` resolves on PATH for the task.
dependencies:
  - "dbt-duckdb==1.9.*"
YAML

echo "==> pre-parsing the manifest with dbt-duckdb (profile in a separate dir; project stays profiles-less)"
PARSE_VENV="$TMP/parsevenv"
python3 -m venv "$PARSE_VENV"
"$PARSE_VENV/bin/pip" install -q "dbt-core==1.9.*" "dbt-duckdb==1.9.*"
mkdir -p "$TMP/parseprofiles"
cat >"$TMP/parseprofiles/profiles.yml" <<YAML
shop:
  target: dev
  outputs:
    dev: { type: duckdb, path: "${PROJ}/leoflow_local.duckdb", threads: 4 }
YAML
( cd "$PROJ" && DBT_PROFILES_DIR="$TMP/parseprofiles" "$PARSE_VENV/bin/dbt" parse >/dev/null 2>&1 ) \
  || fail "dbt parse failed"
[ -f "$PROJ/target/manifest.json" ] || fail "manifest.json was not produced"
[ -f "$PROJ/profiles.yml" ] && fail "project must NOT ship a profiles.yml (zero-config duckdb path)"
pass "manifest pre-parsed; project is profiles-less"

echo "==> resetting the database"
"$TMP/leoflow" db reset --yes >/dev/null

start_lite() {
  "$TMP/leoflow" lite --no-up --executor subprocess --port "$PORT" "$WS" >"$1" 2>&1 &
  LITE_PID=$!
  disown "$LITE_PID" 2>/dev/null || true
  for _ in $(seq 1 "${LITE_READY_TRIES:-600}"); do
    curl -fsS "${BASE}/readyz" >/dev/null 2>&1 && return 0
    kill -0 "$LITE_PID" 2>/dev/null || { tail -80 "$1"; return 1; }
    sleep 1
  done
  return 1
}

echo "==> booting Lite (subprocess executor) — provisions the per-DAG venv from dependencies"
start_lite "$TMP/lite.log" || fail "Lite did not become ready" "$TMP/lite.log"
for _ in $(seq 1 300); do grep -q "registered \"${DAG_ID}\"" "$TMP/lite.log" && break; sleep 1; done
grep -q "registered \"${DAG_ID}\"" "$TMP/lite.log" || fail "${DAG_ID} was not registered" "$TMP/lite.log"
pass "Lite is ready and registered ${DAG_ID}"

echo "==> triggering a run"
RUN_ID="$(curl -fsS -X POST "${BASE}/api/v2/dags/${DAG_ID}/dagRuns" -H 'content-type: application/json' -d '{}' | jq -r '.dag_run_id')"
[ -n "$RUN_ID" ] && [ "$RUN_ID" != "null" ] || fail "no dag_run_id returned"
pass "triggered run $RUN_ID"

echo "==> waiting for all tasks to succeed (first Lite dbt run also installs dbt-duckdb into the venv)"
ok=""
for _ in $(seq 1 600); do
  states="$(curl -fsS "${BASE}/api/v2/dags/${DAG_ID}/dagRuns/${RUN_ID}/taskInstances" 2>/dev/null \
    | jq -r '.task_instances[] | "\(.task_id):\(.state)"' 2>/dev/null | tr '\n' ',' || true)"
  case "$states" in
    *failed*) fail "a task failed (also matches upstream_failed)\nstates: ${states}" "$TMP/lite.log" ;;
  esac
  # all three present and success?
  if [ "$(printf '%s' "$states" | grep -o 'success' | wc -l | tr -d ' ')" = "3" ]; then ok=1; break; fi
  sleep 1
done
[ -n "$ok" ] || fail "tasks did not all succeed\nlast states: ${states:-<none>}" "$TMP/lite.log"
pass "all three dbt tasks succeeded (raw, stg, mart)"

echo "==> models actually materialized in the duckdb file"
DB="$PROJ/leoflow_local.duckdb"
[ -f "$DB" ] || fail "duckdb file not created at $DB — the zero-config profile step did not run" "$TMP/lite.log"
ROWS="$("$PARSE_VENV/bin/python" - "$DB" <<'PY'
import sys, duckdb
con = duckdb.connect(sys.argv[1])
print(con.execute("select count(*) from mart").fetchone()[0])
PY
)"
[ "$ROWS" = "3" ] || fail "mart has $ROWS rows, want 3 (models did not materialize)" "$TMP/lite.log"
pass "mart materialized with 3 rows in duckdb"

echo "==> #882: the generated profiles.yml must land in the private scratch, NOT anywhere in the workspace"
# The task's CWD is the workspace ($WS); pre-fix the profile step wrote profiles.yml
# to os.getcwd() = $WS (not the nested $PROJ). Assert it appears NOWHERE under the
# whole workspace tree — this is the assertion that goes RED on pre-fix code.
LEAKED="$(find "$WS" -name profiles.yml 2>/dev/null || true)"
[ -n "$LEAKED" ] && fail "profiles.yml leaked into the workspace at: $LEAKED — #882: the dbt profile step must write to the private scratch (DBT_PROFILES_DIR the Lite executor injects), never the task CWD, or a managed connection's secret would clobber a versioned profiles.yml" "$TMP/lite.log"
pass "workspace stayed profiles-less after the run (generated profile went to the private scratch, #882)"

echo
echo "  ✅ Lite dbt (duckdb) verified end to end: zero-config compile (#575) -> subprocess"
echo "     execution -> models materialized, no warehouse and no profiles.yml needed."
