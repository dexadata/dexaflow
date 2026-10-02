#!/usr/bin/env bash
# End-to-end gate for the Airflow on_failure_callback surface (#424): a @task's
# native on_failure_callback must actually run — in the task's own process, as the
# user's Python — when the task fails terminally. This closes the second half of
# the "no e2e for either surface" gap the #424 review flagged (lite-alerts.sh
# covers the native alerts: block; this covers the in-process callback).
#
# What the unit tests (runtime/python/tests/test_runner.py) can't prove is the
# wiring: the compiler marks the callback, the agent stamps LEOFLOW_ON_FAILURE_
# CALLBACK + LEOFLOW_MAX_TRIES, and the runtime re-imports dag.py in the pod to
# resolve and run the callback on the terminal attempt. This asserts that whole
# chain: a @task that raises fires its callback, which POSTs proof (the real
# task_id/dag_id from the Airflow context) to a local receiver.
#
# The task runs in Lite's subprocess executor (a per-DAG venv with the Airflow
# SDK), so the callback runs the same Python path a real pod would. retries default
# to 0, so max_tries=1 and the first failure is terminal — the callback fires once.
# (That it fires ONLY on the terminal attempt — not per-retry — is unit-tested.)
#
# Needs a local Postgres (docker-compose.dev.yaml) and the parser on PYTHONPATH.
# Run from the repo root:  PYTHONPATH=parser bash test/e2e/lite-callback.sh
set -euo pipefail

PORT=18094            # Lite control plane
RECV_PORT=18093       # the callback receiver
DB_URL="${LEOFLOW_E2E_DB_URL:-postgres://leoflow:leoflow@localhost:5432/leoflow_dev?sslmode=disable}"
BASE="http://127.0.0.1:${PORT}"
RECV="http://127.0.0.1:${RECV_PORT}"
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
TMP="$(mktemp -d)"
WS="$TMP/ws"
CAPTURE="$TMP/callback.jsonl"
LITE_PID=""
RECV_PID=""
pass() { printf '  \033[32mPASS\033[0m %s\n' "$1"; }
fail() { printf '  \033[31mFAIL\033[0m %s\n' "$1"; [ -n "${2:-}" ] && tail -60 "$2"; cleanup; exit 1; }
cleanup() {
  [ -n "$LITE_PID" ] && kill "$LITE_PID" 2>/dev/null || true
  [ -n "$RECV_PID" ] && kill "$RECV_PID" 2>/dev/null || true
  chmod -R u+w "$TMP" 2>/dev/null || true
  rm -rf "$TMP"
}
trap cleanup EXIT

export PYTHONPATH="${PYTHONPATH:-$ROOT/parser}"
export LEOFLOW_DATABASE_URL="$DB_URL"
export LEOFLOW_LOGS_DIR="$TMP/logs"
# Isolate HOME so Lite reads no ~/.dexaflow/config.yaml admin hash and falls back
# to no-auth loopback, so the API is reachable without a token.
export HOME="$TMP/home"
mkdir -p "$HOME"

echo "==> building binaries"
go build -o "$TMP/leoflow" ./cmd/dexaflow
go build -o "$TMP/leoflow-server" ./cmd/dexaflow-server
go build -o "$TMP/leoflow-agent" ./cmd/dexaflow-agent
export PATH="$TMP:$PATH"

echo "==> building the callback receiver (/callback -> capture)"
cat > "$TMP/receiver.go" <<'GO'
// Standalone receiver for the callback e2e: /callback appends each POST body
// (one JSON object per line) to the capture file so the test can assert the
// on_failure_callback ran and carried the real Airflow context.
package main

import (
	"io"
	"net/http"
	"os"
)

func main() {
	capture := os.Args[1]
	addr := os.Args[2]
	http.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		f, err := os.OpenFile(capture, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err == nil {
			_, _ = f.Write(append(b, '\n'))
			_ = f.Close()
		}
		w.WriteHeader(http.StatusOK)
	})
	_ = http.ListenAndServe(addr, nil)
}
GO
go build -o "$TMP/receiver" "$TMP/receiver.go"

echo "==> starting the receiver on ${RECV}"
: > "$CAPTURE"
"$TMP/receiver" "$CAPTURE" "127.0.0.1:${RECV_PORT}" &
RECV_PID=$!
disown "$RECV_PID" 2>/dev/null || true  # kill by PID in cleanup; keep job control quiet
for _ in $(seq 1 30); do
  curl -sS -o /dev/null "${RECV}/callback" 2>/dev/null && break
  kill -0 "$RECV_PID" 2>/dev/null || fail "receiver exited early"
  sleep 0.3
done

echo "==> resetting the database (migrated, empty)"
"$TMP/leoflow" db reset --yes >/dev/null

echo "==> workspace: a @task that raises, with an on_failure_callback that POSTs proof"
mkdir -p "$WS/cbdag"
cat > "$WS/cbdag/dexaflow.yaml" <<YAML
schema_version: "1.0"
dag_id: cbdag
YAML
cat > "$WS/cbdag/dag.py" <<PY
import json
import urllib.request

from airflow.sdk import DAG, task


def on_fail(context):
    # Runs in the task's own process on its terminal failure. Prove it by POSTing
    # the REAL Airflow context (task_id/dag_id/try) to the receiver.
    ti = context["ti"]
    body = json.dumps(
        {"task_id": ti.task_id, "dag_id": ti.dag_id, "try": ti.try_number}
    ).encode()
    req = urllib.request.Request(
        "${RECV}/callback", data=body, headers={"Content-Type": "application/json"}
    )
    urllib.request.urlopen(req, timeout=5).read()


with DAG("cbdag", schedule=None):

    @task(on_failure_callback=on_fail)
    def boom():
        raise RuntimeError("intentional failure for the on_failure_callback e2e")

    boom()
PY

start_lite() { # $1=logfile
  "$TMP/leoflow" lite --no-up --executor subprocess --port "$PORT" "$WS" >"$1" 2>&1 &
  LITE_PID=$!
  disown "$LITE_PID" 2>/dev/null || true  # kill by PID in cleanup; keep job control quiet
  for _ in $(seq 1 "${LITE_READY_TRIES:-300}"); do
    curl -fsS "${BASE}/readyz" >/dev/null 2>&1 && return 0
    kill -0 "$LITE_PID" 2>/dev/null || { tail -60 "$1"; return 1; }
    sleep 1
  done
  return 1
}

echo "==> booting Lite (subprocess executor, no-auth loopback)"
start_lite "$TMP/lite.log" || fail "Lite did not become ready" "$TMP/lite.log"
for _ in $(seq 1 60); do grep -q 'registered "cbdag"' "$TMP/lite.log" && break; sleep 1; done
grep -q 'registered "cbdag"' "$TMP/lite.log" || fail "cbdag was not registered" "$TMP/lite.log"
pass "Lite is ready and registered cbdag"

echo "==> triggering a run (the @task raises -> terminal failure -> callback fires)"
RUN_ID="$(curl -fsS -X POST "${BASE}/api/v2/dags/cbdag/dagRuns" \
  -H 'content-type: application/json' -d '{}' | jq -r '.dag_run_id')"
[ -n "$RUN_ID" ] && [ "$RUN_ID" != "null" ] || fail "no dag_run_id returned"
pass "triggered run $RUN_ID"

echo "==> waiting for the task to fail"
task_failed=""
for _ in $(seq 1 120); do
  states="$(curl -fsS "${BASE}/api/v2/dags/cbdag/dagRuns/${RUN_ID}/taskInstances" 2>/dev/null \
    | jq -r '.task_instances[] | "\(.task_id):\(.state)"' 2>/dev/null | tr '\n' ',' || true)"
  case "$states" in
    *boom:failed*) task_failed=1; break ;;
  esac
  sleep 1
done
[ -n "$task_failed" ] || fail "task 'boom' did not reach failed\nlast states: ${states:-<none>}" "$TMP/lite.log"
pass "task 'boom' failed"

echo "==> waiting for the on_failure_callback POST to arrive at the receiver"
for _ in $(seq 1 30); do [ -s "$CAPTURE" ] && break; sleep 1; done
[ -s "$CAPTURE" ] || fail "no callback POST captured (the on_failure_callback did not run)" "$TMP/lite.log"

# The callback ran in the task process and carried the real Airflow context.
BODY="$(tail -1 "$CAPTURE")"
echo "    captured: $BODY"
python3 - "$BODY" <<'PY' || fail "captured callback payload did not match" "$TMP/lite.log"
import json, sys
b = json.loads(sys.argv[1])
assert b.get("task_id") == "boom", f"task_id={b.get('task_id')!r}"
assert b.get("dag_id") == "cbdag", f"dag_id={b.get('dag_id')!r}"
print("    payload OK: task_id=boom dag_id=cbdag")
PY
pass "on_failure_callback ran in the task process with the real context"

# It fired exactly once (on the single terminal attempt), not per-invocation.
COUNT="$(grep -c . "$CAPTURE" || true)"
[ "$COUNT" = "1" ] || fail "callback fired $COUNT times, expected exactly 1 (terminal attempt only)" "$TMP/lite.log"
pass "callback fired exactly once (terminal attempt)"

echo "==> the runtime logged the callback lifecycle (in the task's own log)"
# The '[leoflow] running on_failure_callback' lifecycle line is emitted by the
# runtime to the TASK's log (shipped by the agent), not the control-plane log.
tasklog=""
for _ in $(seq 1 15); do
  tasklog="$(curl -fsS "${BASE}/api/v2/dags/cbdag/dagRuns/${RUN_ID}/taskInstances/boom/logs/1" 2>/dev/null || true)"
  printf '%s' "$tasklog" | grep -q 'running on_failure_callback' && break
  sleep 1
done
printf '%s' "$tasklog" | grep -q 'running on_failure_callback' \
  || fail "runtime did not log 'running on_failure_callback' in the task log" "$TMP/lite.log"
pass "runtime logged 'running on_failure_callback' in the task log"

echo
echo "  ✅ Airflow on_failure_callback verified: a @task's terminal failure ran its"
echo "     callback in-process, once, with the real Airflow context (#424)."
