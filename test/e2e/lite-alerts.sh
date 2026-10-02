#!/usr/bin/env bash
# End-to-end gate for native on-failure alerting (#424): the `alerts:` block in
# leoflow.yaml must fire a real webhook POST when a DagRun reaches the terminal
# failed state — entirely in the Go control plane, with no task pod and no Python
# in the hot path. This closes the "no e2e for the native surface" gap the #424
# review flagged (the pure notifier/dispatcher logic is unit-tested; this asserts
# the whole chain against a real DB, real connection encryption, and a real POST).
#
# The chain under test:
#   leoflow.yaml alerts: ─► compile ─► dag.json ─► scheduler sees the run fail
#     ─► resolve the managed connection to its (encrypted) endpoint URL
#     ─► render the message ─► POST the structured webhook payload
#
# A single Python task raises to drive the failure (ADR 0047: no inline http_api).
# (maxRetries=0, so a non-2xx fails it at once — no pod, no venv, no backoff),
# which makes the run fail, which fires the alert. One tiny Go receiver plays both
# roles: /fail returns 500 (fails the task) and /alert captures the alert POST.
#
# Needs a local Postgres (docker-compose.dev.yaml) and the parser on PYTHONPATH.
# Run from the repo root:  PYTHONPATH=parser bash test/e2e/lite-alerts.sh
set -euo pipefail

PORT=18096            # Lite control plane
RECV_PORT=18095       # the webhook receiver (task-fail sink + alert sink)
DB_URL="${LEOFLOW_E2E_DB_URL:-postgres://leoflow:leoflow@localhost:5432/leoflow_dev?sslmode=disable}"
BASE="http://127.0.0.1:${PORT}"
RECV="http://127.0.0.1:${RECV_PORT}"
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
TMP="$(mktemp -d)"
WS="$TMP/ws"
CAPTURE="$TMP/alert.jsonl"
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
# Isolate HOME so Lite reads no ~/.leoflow/config.yaml admin hash and falls back
# to no-auth loopback (LEOFLOW_AUTH_DEV_NO_AUTH), so the API is reachable without
# a token. Connection encryption round-trips inside the server with its own key.
export HOME="$TMP/home"
mkdir -p "$HOME"

echo "==> building binaries"
go build -o "$TMP/leoflow" ./cmd/dexaflow
go build -o "$TMP/leoflow-server" ./cmd/dexaflow-server
go build -o "$TMP/leoflow-agent" ./cmd/dexaflow-agent
export PATH="$TMP:$PATH"

echo "==> building the webhook receiver (/fail -> 500, /alert -> capture)"
cat > "$TMP/receiver.go" <<'GO'
// Standalone test webhook receiver for the alerting e2e. /fail returns 500 so the
// the task under test fails (raises); /alert appends each POST body (one JSON object
// per line) to the capture file so the test can assert what the alerter sent.
package main

import (
	"io"
	"net/http"
	"os"
)

func main() {
	capture := os.Args[1]
	addr := os.Args[2]
	http.HandleFunc("/fail", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	http.HandleFunc("/alert", func(w http.ResponseWriter, r *http.Request) {
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
  # /fail answers 500; probe without -f (a reachable port is a 0 exit regardless
  # of HTTP status), so this succeeds once the listener is up.
  curl -sS -o /dev/null "${RECV}/fail" 2>/dev/null && break
  kill -0 "$RECV_PID" 2>/dev/null || fail "receiver exited early"
  sleep 0.3
done

echo "==> resetting the database (migrated, empty)"
"$TMP/leoflow" db reset --yes >/dev/null

echo "==> workspace: a DAG whose single task raises, with a webhook alert rule"
mkdir -p "$WS/alertdag"
cat > "$WS/alertdag/leoflow.yaml" <<YAML
schema_version: "1.0"
dag_id: alertdag
alerts:
  on_failure:
    - type: webhook
      conn: alerthook
      message: "boom {{dag}}/{{task}} run={{run_id}}"
YAML
cat > "$WS/alertdag/dag.py" <<'PY'
from airflow.sdk import DAG, task

with DAG("alertdag", schedule=None):
    @task
    def call():
        raise RuntimeError("boom: intentional failure to drive the on-failure alert")
    call()
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
for _ in $(seq 1 60); do grep -q 'registered "alertdag"' "$TMP/lite.log" && break; sleep 1; done
grep -q 'registered "alertdag"' "$TMP/lite.log" || fail "alertdag was not registered" "$TMP/lite.log"
pass "Lite is ready and registered alertdag"

echo "==> creating the alert connection (webhook URL is the connection secret)"
# no-auth Lite serves the API on loopback without a token.
code="$(curl -s -o "$TMP/conn.json" -w '%{http_code}' -X POST "${BASE}/api/v2/connections" \
  -H 'content-type: application/json' \
  -d "{\"connection_id\":\"alerthook\",\"conn_type\":\"http\",\"password\":\"${RECV}/alert\"}")"
[ "$code" = "200" ] || [ "$code" = "201" ] || fail "creating alerthook returned $code\n$(cat "$TMP/conn.json")"
pass "alerthook connection created (endpoint stored as the encrypted secret)"

echo "==> triggering a run (its task raises -> run fails -> alert fires)"
RUN_ID="$(curl -fsS -X POST "${BASE}/api/v2/dags/alertdag/dagRuns" \
  -H 'content-type: application/json' -d '{}' | jq -r '.dag_run_id')"
[ -n "$RUN_ID" ] && [ "$RUN_ID" != "null" ] || fail "no dag_run_id returned"
pass "triggered run $RUN_ID"

echo "==> waiting for the run to fail"
run_failed=""
for _ in $(seq 1 90); do
  states="$(curl -fsS "${BASE}/api/v2/dags/alertdag/dagRuns/${RUN_ID}/taskInstances" 2>/dev/null \
    | jq -r '.task_instances[] | "\(.task_id):\(.state)"' 2>/dev/null | tr '\n' ',' || true)"
  case "$states" in
    *call:failed*) run_failed=1; break ;;
  esac
  sleep 1
done
[ -n "$run_failed" ] || fail "task 'call' did not reach failed\nlast states: ${states:-<none>}" "$TMP/lite.log"
pass "task 'call' failed (drives the run to failed)"

echo "==> waiting for the alert POST to arrive at the receiver"
for _ in $(seq 1 30); do [ -s "$CAPTURE" ] && break; sleep 1; done
[ -s "$CAPTURE" ] || fail "no alert POST captured (expected a webhook delivery)" "$TMP/lite.log"

# Assert the structured webhook payload: dag_id, failed_tasks, and the rendered
# message with placeholders substituted (not the literal template).
BODY="$(tail -1 "$CAPTURE")"
echo "    captured: $BODY"
python3 - "$BODY" <<'PY' || fail "captured alert payload did not match" "$TMP/lite.log"
import json, sys
b = json.loads(sys.argv[1])
assert b.get("dag_id") == "alertdag", f"dag_id={b.get('dag_id')!r}"
assert "call" in (b.get("failed_tasks") or []), f"failed_tasks={b.get('failed_tasks')!r}"
msg = b.get("message") or ""
assert msg == f"boom alertdag/call run={b.get('run_id')}", f"message={msg!r}"
assert "{{" not in msg, f"template not substituted: {msg!r}"
print("    payload OK:", msg)
PY
pass "alert payload correct: dag_id, failed_tasks, and rendered message"

echo "==> the scheduler logged the successful dispatch"
for _ in $(seq 1 10); do grep -q 'on-failure alert sent' "$TMP/lite.log" && break; sleep 1; done
grep -q 'on-failure alert sent' "$TMP/lite.log" || fail "scheduler did not log 'on-failure alert sent'" "$TMP/lite.log"
pass "scheduler logged 'on-failure alert sent'"

echo
echo "  ✅ Native on-failure alerting verified: a failed run fired a real webhook POST"
echo "     with the rendered message, resolved from an encrypted connection (#424)."
