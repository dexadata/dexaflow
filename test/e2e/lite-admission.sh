#!/usr/bin/env bash
# End-to-end gate for the dispatch admission webhook (ADR 0100, #2177): with
# execution.admission.webhook.url set, every task attempt is sent to the
# endpoint before its agent token is minted. This proves the wiring the unit
# tests cannot: the setting reaches the server through Lite, the server POSTs
# the attempt, a refusal fails the task at once with the endpoint's reason and
# nothing runs, and an admitted attempt runs as before.
#
# Lite's subprocess executor stands in for the pod path: the hook runs before
# the executor is called on both, so a refused attempt never starts the agent
# here and never creates a pod on Cluster. The endpoint is plain http on
# loopback, which the server allows only for a loopback host.
#
# Needs a local Postgres (docker-compose.dev.yaml) and the parser on PYTHONPATH.
# Run from the repo root:  PYTHONPATH=parser bash test/e2e/lite-admission.sh
set -euo pipefail

PORT=18096            # Lite control plane
HOOK_PORT=18095       # the admission webhook
DB_URL="${LEOFLOW_E2E_DB_URL:-postgres://leoflow:leoflow@localhost:5432/leoflow_dev?sslmode=disable}"
BASE="http://127.0.0.1:${PORT}"
HOOK="http://127.0.0.1:${HOOK_PORT}"
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
TMP="$(mktemp -d)"
WS="$TMP/ws"
CAPTURE="$TMP/reviews.jsonl"
LITE_PID=""
HOOK_PID=""
pass() { printf '  \033[32mPASS\033[0m %s\n' "$1"; }
fail() { printf '  \033[31mFAIL\033[0m %b\n' "$1"; [ -n "${2:-}" ] && tail -60 "$2"; cleanup; exit 1; }
cleanup() {
  [ -n "$LITE_PID" ] && kill "$LITE_PID" 2>/dev/null || true
  [ -n "$HOOK_PID" ] && kill "$HOOK_PID" 2>/dev/null || true
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
export DEXAFLOW_EXECUTION_ADMISSION_WEBHOOK_URL="${HOOK}/admit"

echo "==> building binaries"
go build -o "$TMP/leoflow" ./cmd/dexaflow
go build -o "$TMP/leoflow-server" ./cmd/dexaflow-server
go build -o "$TMP/leoflow-agent" ./cmd/dexaflow-agent
export PATH="$TMP:$PATH"

echo "==> building the admission webhook (refuses dag 'refused', admits the rest)"
cat > "$TMP/hook.go" <<'GO'
// Admission webhook for the e2e: it appends each request to the capture file
// and refuses every attempt of the DAG "refused", admitting all others.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
)

func main() {
	capture := os.Args[1]
	addr := os.Args[2]
	http.HandleFunc("/admit", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusOK)
			return
		}
		b, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if f, err := os.OpenFile(capture, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644); err == nil {
			_, _ = f.Write(append(b, '\n'))
			_ = f.Close()
		}
		var review struct {
			Request struct {
				UID   string `json:"uid"`
				DagID string `json:"dag_id"`
			} `json:"request"`
		}
		if err := json.Unmarshal(b, &review); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		decision := `"decision":"admit"`
		if review.Request.DagID == "refused" {
			decision = `"decision":"refuse","reason":"budget exhausted for this team"`
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"api_version":"dexaflow.io/admission/v1","kind":"AdmissionReview","response":{"uid":%q,%s}}`,
			review.Request.UID, decision)
	})
	_ = http.ListenAndServe(addr, nil)
}
GO
go build -o "$TMP/hook" "$TMP/hook.go"

echo "==> starting the webhook on ${HOOK}"
: > "$CAPTURE"
"$TMP/hook" "$CAPTURE" "127.0.0.1:${HOOK_PORT}" &
HOOK_PID=$!
disown "$HOOK_PID" 2>/dev/null || true  # kill by PID in cleanup; keep job control quiet
for _ in $(seq 1 30); do
  curl -sS -o /dev/null "${HOOK}/admit" 2>/dev/null && break
  kill -0 "$HOOK_PID" 2>/dev/null || fail "webhook exited early"
  sleep 0.3
done

echo "==> resetting the database (migrated, empty)"
"$TMP/leoflow" db reset --yes >/dev/null

echo "==> workspace: dag 'refused' writes a marker if it ever runs; dag 'admitted' succeeds"
for dag in refused admitted; do
  mkdir -p "$WS/$dag"
  cat > "$WS/$dag/dexaflow.yaml" <<YAML
schema_version: "1.0"
dag_id: $dag
YAML
  cat > "$WS/$dag/dag.py" <<PY
from airflow.sdk import DAG, task


with DAG("$dag", schedule=None):

    @task
    def work():
        open("$TMP/ran-$dag", "w").close()

    work()
PY
done

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

echo "==> booting Lite with the admission webhook"
start_lite "$TMP/lite.log" || fail "Lite did not become ready" "$TMP/lite.log"
for dag in refused admitted; do
  for _ in $(seq 1 60); do grep -q "registered \"$dag\"" "$TMP/lite.log" && break; sleep 1; done
  grep -q "registered \"$dag\"" "$TMP/lite.log" || fail "$dag was not registered" "$TMP/lite.log"
done
grep -q "dispatch admission hooks installed" "$TMP/lite.log" || fail "the server did not install the webhook" "$TMP/lite.log"
pass "Lite is ready with the webhook installed"

trigger() { # $1=dag
  curl -fsS -X POST "${BASE}/api/v2/dags/$1/dagRuns" -H 'content-type: application/json' -d '{}' | jq -r '.dag_run_id'
}
wait_task() { # $1=dag $2=run $3=state; prints the task instance JSON
  local ti=""
  for _ in $(seq 1 120); do
    ti="$(curl -fsS "${BASE}/api/v2/dags/$1/dagRuns/$2/taskInstances" 2>/dev/null \
      | jq -c '.task_instances[] | select(.task_id == "work")' 2>/dev/null || true)"
    [ "$(printf '%s' "$ti" | jq -r '.state' 2>/dev/null)" = "$3" ] && { printf '%s' "$ti"; return 0; }
    sleep 1
  done
  printf '%s' "$ti"
  return 1
}

echo "==> a refused attempt fails at once with the endpoint's reason and never runs"
RUN="$(trigger refused)"
[ -n "$RUN" ] && [ "$RUN" != "null" ] || fail "no dag_run_id returned for refused"
TI="$(wait_task refused "$RUN" failed)" || fail "task of 'refused' did not fail\nlast: $TI" "$TMP/lite.log"
REASON="$(printf '%s' "$TI" | jq -r '.failure_reason // empty')"
case "$REASON" in
  *"dispatch refused: admission: budget exhausted for this team"*) ;;
  *) fail "failure reason is '$REASON', want the webhook's refusal" "$TMP/lite.log" ;;
esac
[ ! -e "$TMP/ran-refused" ] || fail "the refused task ran"
[ "$(printf '%s' "$TI" | jq -r '.try_number')" -le 1 ] || fail "the refused task was retried: $TI"
pass "refused: failed with '$REASON', never ran"

echo "==> an admitted attempt runs as before"
RUN="$(trigger admitted)"
[ -n "$RUN" ] && [ "$RUN" != "null" ] || fail "no dag_run_id returned for admitted"
TI="$(wait_task admitted "$RUN" success)" || fail "task of 'admitted' did not succeed\nlast: $TI" "$TMP/lite.log"
[ -e "$TMP/ran-admitted" ] || fail "the admitted task reported success but did not run"
pass "admitted: ran and succeeded"

echo "==> the webhook saw both attempts, with no environment, source or token"
python3 - "$CAPTURE" <<'PY' || fail "captured reviews did not match" "$TMP/lite.log"
import json, sys
reviews = [json.loads(l) for l in open(sys.argv[1]) if l.strip()]
dags = sorted({r["request"]["dag_id"] for r in reviews})
assert dags == ["admitted", "refused"], dags
for r in reviews:
    assert r["api_version"] == "dexaflow.io/admission/v1", r
    req = r["request"]
    assert req["task_id"] == "work" and req["uid"], req
    for k in ("env", "source", "token", "secrets"):
        assert k not in req, (k, req)
print(f"    {len(reviews)} reviews OK")
PY
pass "the webhook received each attempt, without env, source or token"

echo
echo "  ✅ Admission webhook verified: a refusal fails the task before it runs, with"
echo "     the endpoint's reason, and an admitted attempt runs as before (#2177)."
