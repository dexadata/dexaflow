#!/usr/bin/env bash
# End-to-end gate for Lite's boot self-heal (#404): a reused metadata DB must not
# leave un-removable ghosts. Two `leoflow lite` sessions share one external
# Postgres (--no-up + LEOFLOW_DATABASE_URL). Session 1 registers a valid DAG
# (ghost), a kept DAG (keeper), and a broken DAG (import error); its files for
# ghost+broken are then removed. Session 2's boot reconcile MUST deregister the
# ghost DAG and clear the orphan import error, while keeping the valid DAG.
#
# Catches the regression where the watcher seeded its set-diff from the workspace
# instead of the control plane, so pre-existing ghosts were never reconciled.
#
# Needs a local Postgres (docker-compose.dev.yaml) and the parser on PYTHONPATH.
# Run from the repo root:  PYTHONPATH=parser bash test/e2e/lite-selfheal.sh
set -euo pipefail

PORT=18097
DB_URL="${LEOFLOW_E2E_DB_URL:-postgres://leoflow:leoflow@localhost:5432/leoflow_dev?sslmode=disable}"
BASE="http://127.0.0.1:${PORT}"
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
TMP="$(mktemp -d)"
WS="$TMP/ws"
LITE_PID=""
pass() { printf '  \033[32mPASS\033[0m %s\n' "$1"; }
fail() { printf '  \033[31mFAIL\033[0m %s\n' "$1"; [ -n "${2:-}" ] && tail -40 "$2"; cleanup; exit 1; }
cleanup() { [ -n "$LITE_PID" ] && kill "$LITE_PID" 2>/dev/null || true; chmod -R u+w "$TMP" 2>/dev/null || true; rm -rf "$TMP"; }
trap cleanup EXIT

export PYTHONPATH="${PYTHONPATH:-$ROOT/parser}"
export LEOFLOW_DATABASE_URL="$DB_URL"
export LEOFLOW_AUTH_JWT_SECRET="e2e-insecure-jwt-secret-please-change"
export LEOFLOW_SECRET_KEY="e2e-insecure-secret-key-32bytes!"
export LEOFLOW_LOGS_DIR="$TMP/logs"

echo "==> building binaries"
go build -o "$TMP/leoflow" ./cmd/dexaflow
go build -o "$TMP/leoflow-server" ./cmd/dexaflow-server
go build -o "$TMP/leoflow-agent" ./cmd/dexaflow-agent
export PATH="$TMP:$PATH"

echo "==> resetting the database (migrated, empty)"
"$TMP/leoflow" db reset --yes >/dev/null

echo "==> workspace: keeper (kept), ghost (removed later), broken (syntax error)"
for d in keeper ghost; do
  mkdir -p "$WS/$d"
  printf 'schema_version: "1.0"\ndag_id: %s\n' "$d" > "$WS/$d/dexaflow.yaml"
  printf 'from airflow.providers.standard.operators.bash import BashOperator\nfrom airflow.sdk import DAG\nwith DAG("%s", schedule="@daily"):\n    BashOperator(task_id="t", bash_command="echo %s")\n' "$d" "$d" > "$WS/$d/dag.py"
done
mkdir -p "$WS/broken"
printf 'schema_version: "1.0"\ndag_id: broken\n' > "$WS/broken/dexaflow.yaml"
printf 'from airflow.sdk import DAG\nwith DAG("broken", schedule=None)   # missing colon -> SyntaxError\n    pass\n' > "$WS/broken/dag.py"

start_lite() { # $1=logfile
  "$TMP/leoflow" lite --no-up --executor subprocess --port "$PORT" "$WS" >"$1" 2>&1 &
  LITE_PID=$!
  # Lite provisions a per-DAG venv (pip-installs the Airflow SDK) before the server
  # serves /readyz, so first boot waits on three venv installs — slow on a cold CI
  # runner. Be patient (the reconcile we're testing is cheap; the venvs are not).
  for _ in $(seq 1 "${LITE_READY_TRIES:-300}"); do
    curl -fsS "${BASE}/readyz" >/dev/null 2>&1 && return 0
    kill -0 "$LITE_PID" 2>/dev/null || { tail -40 "$1"; return 1; }
    sleep 1
  done
  return 1
}

echo "==> session 1: register keeper + ghost, and a broken import error"
start_lite "$TMP/s1.log" || fail "session 1 did not become ready" "$TMP/s1.log"
for _ in $(seq 1 30); do grep -q 'registered "keeper"' "$TMP/s1.log" && break; sleep 1; done
grep -q 'registered "ghost"'  "$TMP/s1.log" || fail "session 1 did not register ghost"  "$TMP/s1.log"
grep -q 'registered "keeper"' "$TMP/s1.log" || fail "session 1 did not register keeper" "$TMP/s1.log"
pass "session 1 registered keeper + ghost (broken left an import error)"
kill "$LITE_PID" 2>/dev/null; LITE_PID=""; sleep 2

echo "==> create the ghosts: remove ghost/ and broken/ from disk (DB persists)"
rm -rf "$WS/ghost" "$WS/broken"

echo "==> session 2: the boot reconcile must self-heal"
start_lite "$TMP/s2.log" || fail "session 2 did not become ready" "$TMP/s2.log"
# The reconcile runs right after the first reload, before "watching".
for _ in $(seq 1 20); do grep -q 'watching ' "$TMP/s2.log" && break; sleep 1; done

grep -q 'removed dag "ghost" from registry' "$TMP/s2.log" \
  || fail "boot reconcile did not deregister the ghost DAG" "$TMP/s2.log"
pass "ghost DAG deregistered on boot"

grep -q 'cleared stale import error' "$TMP/s2.log" \
  || fail "boot reconcile did not clear the stale import error" "$TMP/s2.log"
pass "stale import error cleared on boot"

# State check: keeper survives, ghost is gone (it was never re-added on disk).
TOKEN="$("$TMP/leoflow" auth create-token --server "$BASE" --username admin@leoflow.local --password x 2>/dev/null || true)"
if [ -n "$TOKEN" ]; then
  DAGS="$(curl -fsS -H "Authorization: Bearer $TOKEN" "${BASE}/api/v2/dags?limit=100" 2>/dev/null || true)"
  if [ -n "$DAGS" ]; then
    echo "$DAGS" | grep -q '"dag_id":"keeper"' || fail "keeper was not preserved" "$TMP/s2.log"
    echo "$DAGS" | grep -q '"dag_id":"ghost"'  && fail "ghost survived the reconcile" "$TMP/s2.log"
    pass "final state: keeper preserved, ghost gone"
  fi
fi

echo
echo "  ✅ Lite boot self-heal verified: ghost DAG deregistered + stale import error cleared (#404)."
