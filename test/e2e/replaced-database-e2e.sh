#!/usr/bin/env bash
# A database emptied under a running chart install (#1996, ADR 0090): the
# control plane waits, says why, and recovers once the operator accepts it.
#
#   1. helm install on kind; register a DAG version and trigger a run.
#   2. Drop and recreate the database under the running release.
#   3. The serving pod exits with code 3 (decision 5) and restarts into the
#      wait: Running, not ready, /readyz names "database replaced" and
#      dexaflow_schema_ready{reason="database replaced"} reads 0 (decisions 1,
#      2 and 6). The release stays up all along: no CrashLoopBackOff.
#   4. A `helm upgrade` without the acknowledgement fails: the migration Job
#      refuses the database with exit code 8 (decision 3).
#   5. The documented acknowledgement, `helm upgrade --reuse-values --set
#      installation.id=<new id>`, migrates it and writes the id into the
#      database and the ConfigMap; the release is ready, the old DAG is gone,
#      and a new version registers and runs are accepted again.
#
# Task pods are not run: no task image is built here. The run that is
# triggered checks the write path the control plane reopens, not execution.
#
# Requirements: kind (a cluster named $CLUSTER already exists, as
# helm/kind-action creates it, or the script creates one), kubectl, helm,
# docker, go, jq, curl, python3 and uuidgen. DESTRUCTIVE to that cluster.
# Run from the repo root:  bash test/e2e/replaced-database-e2e.sh
set -euo pipefail

# shellcheck source=test/e2e/lib.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
CLUSTER="${DEXAFLOW_E2E_KIND_CLUSTER:-dexaflow-replaced-db}"
NS="dexaflow"
RELEASE="dexaflow"
DAG_ID="replaced_db_probe"
WORKDIR="$(mktemp -d)"
PF_PID=""
CREATED_CLUSTER=0

log() { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
pass() { printf '  \033[1;32mPASS\033[0m %s\n' "$*"; }
dump() {
  printf '\033[1;33m--- pods ---\033[0m\n' >&2
  kubectl -n "$NS" get pods -o wide >&2 2>&1 || true
  kubectl -n "$NS" describe deploy/"$RELEASE" 2>&1 | tail -20 >&2 || true
  printf '\033[1;33m--- control plane log (current, then previous) ---\033[0m\n' >&2
  kubectl -n "$NS" logs deploy/"$RELEASE" --tail=40 >&2 2>&1 || true
  kubectl -n "$NS" logs deploy/"$RELEASE" --previous --tail=20 >&2 2>&1 || true
  printf '\033[1;33m--- migration Job pods ---\033[0m\n' >&2
  for p in $(kubectl -n "$NS" get pods -l job-name="$RELEASE-migrate" -o name 2>/dev/null); do
    kubectl -n "$NS" logs "$p" --tail=20 >&2 2>&1 || true
  done
}
fail() { printf '\033[1;31mFAIL:\033[0m %s\n' "$*" >&2; dump; exit 1; }

stop_pf() {
  [ -n "$PF_PID" ] || return 0
  kill "$PF_PID" 2>/dev/null || true
  wait "$PF_PID" 2>/dev/null || true
  PF_PID=""
}
cleanup() {
  stop_pf
  [ "$CREATED_CLUSTER" = 1 ] && kind delete cluster --name "$CLUSTER" >/dev/null 2>&1
  rm -rf "$WORKDIR"
}
trap cleanup EXIT

for tool in kind kubectl helm docker go jq curl python3 uuidgen; do
  command -v "$tool" >/dev/null || fail "missing required tool: $tool"
done

# server_pod prints the name of the control plane pod.
server_pod() {
  kubectl -n "$NS" get pods -l "app.kubernetes.io/instance=$RELEASE,app.kubernetes.io/component!=migrate" \
    --field-selector=status.phase!=Succeeded,status.phase!=Failed \
    -o jsonpath='{.items[0].metadata.name}'
}

# forward points 127.0.0.1:18080 (API) and :19090 (metrics) at the control
# plane pod. It forwards to the pod, not the Service: a pod that is not ready
# has no Service endpoint, and its /readyz is what is asserted. A container
# restart ends the forward, so each check starts a new one.
forward() {
  local pod
  stop_pf
  pod="$(server_pod)" || return 1
  [ -n "$pod" ] || return 1
  kubectl -n "$NS" port-forward "pod/$pod" 18080:8080 19090:9090 >"$WORKDIR/pf.log" 2>&1 &
  PF_PID=$!
  for _ in $(seq 1 20); do
    curl -s -o /dev/null "http://127.0.0.1:18080/healthz" && return 0
    sleep 0.5
  done
  return 1
}

# readyz prints "<status> <body>" of the pod's /readyz.
readyz() {
  forward || { echo "000 no port-forward"; return 0; }
  curl -s -w ' %{http_code}' "http://127.0.0.1:18080/readyz" | awk '{code=$NF; $NF=""; print code, $0}'
}

API="http://127.0.0.1:18080"
BIN="$WORKDIR/dexaflow"
export PYTHONPATH="${ROOT}/parser:${PYTHONPATH:-}"

if ! kind get clusters 2>/dev/null | grep -qx "$CLUSTER"; then
  log "kind cluster $CLUSTER"
  kind create cluster --name "$CLUSTER" --wait 120s >/dev/null
  CREATED_CLUSTER=1
fi
kubectl config use-context "kind-$CLUSTER" >/dev/null

log "build the server, migrate and CLI binaries"
docker build -q -t dexaflow-server:e2e -f "$ROOT/deploy/Dockerfile.server" "$ROOT" >/dev/null
docker build -q -t dexaflow-migrate:e2e -f "$ROOT/deploy/Dockerfile.migrate" "$ROOT" >/dev/null
kind load docker-image dexaflow-server:e2e dexaflow-migrate:e2e --name "$CLUSTER" >/dev/null
(cd "$ROOT" && go build -o "$BIN" ./cmd/dexaflow)

log "Postgres and Redis in namespace $NS"
kubectl create namespace "$NS" >/dev/null
kubectl -n "$NS" apply -f "$ROOT/.github/ci/kind-datastores.yaml" >/dev/null
kubectl -n "$NS" rollout status deploy/postgres --timeout=150s >/dev/null
kubectl -n "$NS" rollout status deploy/redis --timeout=150s >/dev/null
kubectl -n "$NS" exec deploy/postgres -- pg_isready -U leoflow -d leoflow -t 60 >/dev/null

psql_db() { kubectl -n "$NS" exec deploy/postgres -- psql -U leoflow -d "$1" -tAc "$2"; }

log "helm install"
helm install "$RELEASE" "$ROOT/helm/dexaflow" -n "$NS" \
  --set image.repository=dexaflow-server --set image.tag=e2e --set image.pullPolicy=IfNotPresent \
  --set migrations.image.repository=dexaflow-migrate --set migrations.image.tag=e2e \
  --set migrations.image.pullPolicy=IfNotPresent \
  --set database.url='postgres://leoflow:leoflow@postgres:5432/leoflow?sslmode=disable' \
  --set redis.url='redis://redis:6379/0' \
  --set auth.jwtSecret=e2e-jwt-secret \
  --set 'secretKey=dexaflow-e2e-secret-key-32bytes!' \
  --set agentTLS.enabled=true \
  --set bootstrap.password=e2e-admin \
  --wait --timeout 300s >/dev/null || fail "helm install"
kubectl -n "$NS" rollout status deploy/"$RELEASE" --timeout=120s >/dev/null

FIRST_ID="$(kubectl -n "$NS" get configmap "$RELEASE-installation" -o jsonpath='{.data.installationId}')"
[ -n "$FIRST_ID" ] || fail "the install wrote no installation ConfigMap"
[ "$(psql_db leoflow 'SELECT id FROM installation')" = "$FIRST_ID" ] \
  || fail "the database does not carry the ConfigMap's installation id $FIRST_ID"
pass "installed with installation id $FIRST_ID, in the database and the ConfigMap"

# register_and_trigger registers a bash DAG version and triggers a run.
register_and_trigger() {
  local token code rid
  forward || fail "no port-forward to the control plane"
  token="$(admin_token "$BIN" "$API" e2e-admin)" || fail "the bootstrap admin cannot log in"
  code="$(curl -sS -o "$WORKDIR/register.body" -w '%{http_code}' -X POST \
    -H "Authorization: Bearer $token" -H 'Content-Type: application/json' \
    --data-binary @"$WORKDIR/project/dag.json" "$API/api/v2/dags/$DAG_ID/versions")"
  [ "$code" = 201 ] || fail "POST /versions answered $code: $(cat "$WORKDIR/register.body")"
  unpause_dag "$API" "$DAG_ID" "$token" || fail "unpausing $DAG_ID"
  rid="$(curl -fsS -X POST -H "Authorization: Bearer $token" -H 'Content-Type: application/json' \
    -d '{}' "$API/api/v2/dags/$DAG_ID/dagRuns" | jq -r '.dag_run_id')"
  [ -n "$rid" ] && [ "$rid" != null ] || fail "no dag_run_id returned"
  printf '%s' "$rid"
}

mkdir -p "$WORKDIR/project"
cat > "$WORKDIR/project/dexaflow.yaml" <<YAML
schema_version: "1.0"
dag_id: $DAG_ID
YAML
cat > "$WORKDIR/project/dag.py" <<'PY'
from airflow.sdk import DAG
from airflow.providers.standard.operators.bash import BashOperator

with DAG("replaced_db_probe", schedule=None):
    BashOperator(task_id="hello", bash_command="echo hello")
PY
"$BIN" compile "$WORKDIR/project" --image busybox:1.36 --dag-version e2e-1 \
  -o "$WORKDIR/project/dag.json" >/dev/null || fail "compiling the probe DAG"

log "register a DAG and trigger a run on the installed release"
RUN1="$(register_and_trigger)"
pass "DAG $DAG_ID registered, run $RUN1 accepted"

POD="$(server_pod)"
RESTARTS="$(kubectl -n "$NS" get pod "$POD" -o jsonpath='{.status.containerStatuses[0].restartCount}')"

log "drop and recreate the database under the running release"
stop_pf
psql_db postgres 'DROP DATABASE leoflow WITH (FORCE)' >/dev/null
psql_db postgres 'CREATE DATABASE leoflow OWNER leoflow' >/dev/null

log "the serving pod exits with code 3 and restarts into the wait"
exited=0
for _ in $(seq 1 60); do
  code="$(kubectl -n "$NS" get pod "$POD" -o jsonpath='{.status.containerStatuses[0].lastState.terminated.exitCode}' 2>/dev/null || true)"
  now="$(kubectl -n "$NS" get pod "$POD" -o jsonpath='{.status.containerStatuses[0].restartCount}' 2>/dev/null || true)"
  if [ -n "$code" ] && [ "${now:-0}" -gt "$RESTARTS" ]; then exited=1; break; fi
  sleep 2
done
[ "$exited" = 1 ] || fail "pod $POD did not restart after the database was emptied"
[ "$code" = 3 ] || fail "pod $POD exited with code $code, want 3 (the database was replaced under it)"
kubectl -n "$NS" logs "$POD" --previous 2>/dev/null | grep -q 'the database was replaced under this process' \
  || fail "the exited container's log does not say the database was replaced"
pass "pod $POD exited with code 3 and restarted"

waiting=0
for _ in $(seq 1 30); do
  out="$(readyz)"
  if [ "${out%% *}" = 503 ] && printf '%s' "$out" | grep -q 'database replaced'; then waiting=1; break; fi
  sleep 2
done
[ "$waiting" = 1 ] || fail "/readyz never answered 503 with a database replaced reason; last: $out"
pass "/readyz answers 503: ${out#* }"

# Held for a minute: the restarted pod stays up and not ready, it does not
# crash loop, and nothing migrated the empty database behind the operator.
sleep 60
phase="$(kubectl -n "$NS" get pod "$POD" -o jsonpath='{.status.phase} {.status.containerStatuses[0].ready} {.status.containerStatuses[0].state.waiting.reason}')"
[ "$phase" = "Running false " ] || fail "pod $POD is '$phase' after a minute, want Running and not ready"
now="$(kubectl -n "$NS" get pod "$POD" -o jsonpath='{.status.containerStatuses[0].restartCount}')"
[ "$now" -eq $((RESTARTS + 1)) ] || fail "pod $POD restarted $now times, want exactly one restart (no crash loop)"
[ -z "$(psql_db leoflow "SELECT to_regclass('public.schema_migrations')")" ] \
  || fail "something migrated the emptied database before it was accepted"
pass "the pod stays Running and not ready, restarted once, and the database stays empty"

forward || fail "no port-forward to the metrics port"
metric="$(curl -fsS http://127.0.0.1:19090/metrics | grep '^dexaflow_schema_ready{' || true)"
printf '%s\n' "$metric" | grep -qx 'dexaflow_schema_ready{reason="database replaced"} 0' \
  || fail "dexaflow_schema_ready does not read 0 with reason database replaced: ${metric:-<absent>}"
pass "$metric"
stop_pf

log "helm upgrade without the acknowledgement fails"
if helm upgrade "$RELEASE" "$ROOT/helm/dexaflow" -n "$NS" --reuse-values --wait --timeout 300s \
    >"$WORKDIR/upgrade-refused.log" 2>&1; then
  fail "helm upgrade migrated a replaced database without the acknowledgement"
fi
job_code=""
for p in $(kubectl -n "$NS" get pods -l job-name="$RELEASE-migrate" -o name 2>/dev/null); do
  job_code="$(kubectl -n "$NS" get "$p" -o jsonpath='{.status.containerStatuses[0].state.terminated.exitCode}')"
  kubectl -n "$NS" logs "$p" 2>/dev/null | grep -q 'database replaced' || fail "$p does not log database replaced"
done
[ "$job_code" = 8 ] || fail "the migration Job exited with code ${job_code:-<none>}, want 8"
pass "the migration Job refused the database with exit code 8, and helm upgrade failed"

NEW_ID="$(uuidgen | tr '[:upper:]' '[:lower:]')"
log "accept the emptied database with a new installation id"
helm upgrade "$RELEASE" "$ROOT/helm/dexaflow" -n "$NS" --reuse-values \
  --set "installation.id=$NEW_ID" --wait --timeout 300s >/dev/null || fail "helm upgrade with installation.id"
kubectl -n "$NS" rollout status deploy/"$RELEASE" --timeout=180s >/dev/null || fail "the release did not become ready"

[ "$(kubectl -n "$NS" get configmap "$RELEASE-installation" -o jsonpath='{.data.installationId}')" = "$NEW_ID" ] \
  || fail "the ConfigMap does not carry the new installation id"
[ "$(psql_db leoflow 'SELECT id FROM installation')" = "$NEW_ID" ] \
  || fail "the database does not carry the new installation id"
out="$(readyz)"
[ "${out%% *}" = 200 ] || fail "/readyz answers $out after the acknowledgement"
metric="$(curl -fsS http://127.0.0.1:19090/metrics | grep '^dexaflow_schema_ready{' || true)"
printf '%s\n' "$metric" | grep -qx 'dexaflow_schema_ready{reason=""} 1' \
  || fail "dexaflow_schema_ready does not read 1 after the acknowledgement: ${metric:-<absent>}"
pass "ready again with installation id $NEW_ID in the database and the ConfigMap; $metric"

token="$(admin_token "$BIN" "$API" e2e-admin)" || fail "the bootstrap admin cannot log in after the acknowledgement"
code="$(curl -s -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $token" "$API/api/v2/dags/$DAG_ID")"
[ "$code" = 404 ] || fail "GET /dags/$DAG_ID answered $code on the emptied database, want 404"
RUN2="$(register_and_trigger)"
pass "the DAG of the old database is gone; it registers again and run $RUN2 is accepted"

echo
echo "REPLACED DATABASE E2E: all assertions held"
