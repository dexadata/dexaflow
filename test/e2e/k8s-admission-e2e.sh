#!/usr/bin/env bash
#
# End-to-end gate for the dispatch admission webhook on the pod path (ADR 0100,
# #2177): with execution.admission.webhook.url set, a refused task attempt
# fails at once with the endpoint's reason, and the server creates no pod for
# it, so no agent token ever reaches the cluster (the token travels in the
# pod's spec, and the hook runs before the token is minted).
#
# test/e2e/lite-admission.sh proves the same on Lite's subprocess executor.
# This one proves it where it matters most, on a real Kubernetes API: a watch
# on the task namespace records every pod the server creates during the test,
# and the refused DAG must have none. An admitted DAG is the positive control:
# its pod must show up in the same watch, which proves the watch sees what the
# server creates. Neither DAG image exists; the admitted pod only has to be
# created, not to run, so the test builds no image.
#
# Requirements: k3d, kubectl, docker, go, jq, curl, a migrated dev database,
# the binaries (`make build`) and the parser on PYTHONPATH. CI runs it in the
# operator e2e job, after test/e2e/e2e.sh.
#
# Usage: test/e2e/k8s-admission-e2e.sh [cluster-name]
set -euo pipefail

# shellcheck source=test/e2e/lib.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

CLUSTER="${1:-dexaflow-admission}"
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
WORKDIR="$(mktemp -d)"
HTTP_PORT="${LEOFLOW_E2E_HTTP_PORT:-8080}"
API="http://localhost:${HTTP_PORT}"
GRPC_PORT="9091"
HOOK_PORT="${DEXAFLOW_E2E_HOOK_PORT:-18097}"
HOST_ADDR="${LEOFLOW_E2E_HOST_ADDR:-$([ "$(uname -s)" = Linux ] && echo host.k3d.internal || echo host.docker.internal)}"
NS="leoflow"
REASON_WANT="dispatch refused: admission: budget exhausted for this team"
SERVER_PID=""
HOOK_PID=""
WATCH_PID=""

log()  { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
pass() { printf '  \033[32mPASS\033[0m %s\n' "$*"; }
fail() {
  printf '\033[1;31mFAIL:\033[0m %b\n' "$*" >&2
  [ -f "$WORKDIR/server.log" ] && { printf '\033[1;33m--- server log ---\033[0m\n' >&2; tail -60 "$WORKDIR/server.log" >&2; }
  [ -f "$WORKDIR/pods.log" ] && { printf '\033[1;33m--- pods seen ---\033[0m\n' >&2; cat "$WORKDIR/pods.log" >&2; }
  exit 1
}
cleanup() {
  [ -n "$WATCH_PID" ] && kill "$WATCH_PID" 2>/dev/null || true
  [ -n "$SERVER_PID" ] && kill "$SERVER_PID" 2>/dev/null || true
  [ -n "$HOOK_PID" ] && kill "$HOOK_PID" 2>/dev/null || true
  k3d cluster delete "$CLUSTER" >/dev/null 2>&1 || true
  rm -rf "$WORKDIR"
}
trap cleanup EXIT

for tool in k3d kubectl docker go jq curl; do
  command -v "$tool" >/dev/null || fail "missing required tool: $tool"
done
[ -x "$ROOT/bin/leoflow" ] && [ -x "$ROOT/bin/leoflow-server" ] || fail "run 'make build' first"

log "Building the admission webhook (refuses dag 'refused', admits the rest)"
cat > "$WORKDIR/hook.go" <<'GO'
// Admission webhook for the e2e: it refuses every attempt of the DAG
// "refused" and admits all others.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
)

func main() {
	http.HandleFunc("/admit", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusOK)
			return
		}
		var review struct {
			Request struct {
				UID   string `json:"uid"`
				DagID string `json:"dag_id"`
			} `json:"request"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&review); err != nil {
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
	_ = http.ListenAndServe(os.Args[1], nil)
}
GO
go build -o "$WORKDIR/hook" "$WORKDIR/hook.go"
"$WORKDIR/hook" "127.0.0.1:${HOOK_PORT}" &
HOOK_PID=$!
for _ in $(seq 1 30); do
  curl -fsS -o /dev/null "http://127.0.0.1:${HOOK_PORT}/admit" 2>/dev/null && break
  kill -0 "$HOOK_PID" 2>/dev/null || fail "the webhook exited early"
  sleep 0.3
done

log "Compiling two DAGs whose images are never pulled"
for dag in refused admitted; do
  mkdir -p "$WORKDIR/$dag"
  cat > "$WORKDIR/$dag/dexaflow.yaml" <<YAML
schema_version: "1.0"
dag_id: $dag
YAML
  cat > "$WORKDIR/$dag/dag.py" <<PY
from airflow.sdk import DAG, task

with DAG("$dag", schedule=None):

    @task
    def work():
        print("ran")

    work()
PY
  "$ROOT/bin/leoflow" compile "$WORKDIR/$dag" --image "dexaflow-e2e/never-pulled:$dag" \
    -o "$WORKDIR/$dag/dag.json" >/dev/null
done

log "Creating k3d cluster '$CLUSTER' and the '$NS' namespace"
k3d cluster create "$CLUSTER" --wait >/dev/null
kubectl create namespace "$NS" >/dev/null

log "Watching every pod created in '$NS' from now on"
kubectl get pods -n "$NS" --watch \
  -o custom-columns='NAME:.metadata.name,DAG:.metadata.labels.leoflow\.io/dag-id' --no-headers \
  >"$WORKDIR/pods.log" 2>&1 &
WATCH_PID=$!

log "Starting the control plane with the admission webhook"
export LEOFLOW_AUTH_JWT_SECRET="e2e-secret"
export LEOFLOW_BOOTSTRAP_PASSWORD="admin"
export LEOFLOW_EXECUTOR_AGENT_CONTROL_PLANE_ADDR="${HOST_ADDR}:${GRPC_PORT}"
export LEOFLOW_LOGS_DIR="${WORKDIR}/logs"
export LEOFLOW_SERVER_HTTP_ADDR="0.0.0.0:${HTTP_PORT}"
export DEXAFLOW_EXECUTION_ADMISSION_WEBHOOK_URL="http://127.0.0.1:${HOOK_PORT}/admit"
"$ROOT/bin/leoflow-server" >"$WORKDIR/server.log" 2>&1 &
SERVER_PID=$!
for _ in $(seq 1 60); do
  curl -fsS "$API/readyz" >/dev/null 2>&1 && break
  kill -0 "$SERVER_PID" 2>/dev/null || fail "the server exited"
  sleep 1
done
curl -fsS "$API/readyz" >/dev/null || fail "the server did not become ready"
grep -q "dispatch admission hooks installed" "$WORKDIR/server.log" || fail "the server did not install the webhook"
pass "server ready with the webhook installed"

TOKEN="$(admin_token "$ROOT/bin/leoflow" "$API" admin)" || fail "no admin token"
for dag in refused admitted; do
  "$ROOT/bin/leoflow" push "$WORKDIR/$dag/dag.json" --server "$API" --token "$TOKEN" >/dev/null
  unpause_dag "$API" "$dag" "$TOKEN" || fail "unpausing $dag failed"
done

trigger() { # $1=dag; prints the run id
  curl -fsS -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
    -d '{}' "$API/api/v2/dags/$1/dagRuns" | jq -r '.dag_run_id'
}
task_instance() { # $1=dag $2=run
  curl -fsS -H "Authorization: Bearer $TOKEN" "$API/api/v2/dags/$1/dagRuns/$2/taskInstances" 2>/dev/null |
    jq -c '.task_instances[] | select(.task_id == "work")' 2>/dev/null || true
}

log "A refused attempt fails with the endpoint's reason"
RUN="$(trigger refused)"
[ -n "$RUN" ] && [ "$RUN" != "null" ] || fail "no dag_run_id for refused"
TI=""
for _ in $(seq 1 120); do
  TI="$(task_instance refused "$RUN")"
  [ "$(printf '%s' "$TI" | jq -r '.state' 2>/dev/null)" = "failed" ] && break
  sleep 1
done
[ "$(printf '%s' "$TI" | jq -r '.state')" = "failed" ] || fail "the refused task did not fail\nlast: $TI"
REASON="$(printf '%s' "$TI" | jq -r '.failure_reason // empty')"
case "$REASON" in
  *"$REASON_WANT"*) ;;
  *) fail "failure reason is '$REASON', want '$REASON_WANT'" ;;
esac
[ "$(printf '%s' "$TI" | jq -r '.try_number')" -le 1 ] || fail "the refused task was retried: $TI"
pass "refused: failed with '$REASON'"

log "An admitted attempt gets its pod (the watch sees what the server creates)"
RUN_OK="$(trigger admitted)"
[ -n "$RUN_OK" ] && [ "$RUN_OK" != "null" ] || fail "no dag_run_id for admitted"
for _ in $(seq 1 90); do
  awk '$2 == "admitted"' "$WORKDIR/pods.log" | grep -q . && break
  sleep 1
done
awk '$2 == "admitted"' "$WORKDIR/pods.log" | grep -q . || fail "no pod was seen for the admitted DAG; the watch cannot prove an absence"
pass "admitted: its pod was created"

log "No pod was ever created for the refused DAG, so no agent token reached the cluster"
if awk '$2 == "refused"' "$WORKDIR/pods.log" | grep -q .; then
  fail "the server created a pod for the refused DAG"
fi
[ -z "$(kubectl get pods -n "$NS" -l 'leoflow.io/dag-id=refused' -o name 2>/dev/null)" ] ||
  fail "a pod of the refused DAG exists"
pass "refused: no pod created"

METRICS="$(curl -fsS "http://localhost:9090/metrics" 2>/dev/null || true)"
printf '%s\n' "$METRICS" | grep -E '^dexaflow_dispatch_admission_total\{[^}]*outcome="refused"' | awk '{ if ($2 >= 1) found=1 } END { exit !found }' ||
  fail "dexaflow_dispatch_admission_total{outcome=\"refused\"} did not count the refusal"
pass "the refusal is counted in dexaflow_dispatch_admission_total"

log "k8s admission e2e: all checks passed"
