#!/usr/bin/env bash
#
# End-to-end test for dbt MANAGED CONNECTIONS on a real Kubernetes cluster (k3d),
# ADR 0043 #2. A dbt task gets its warehouse credentials from a Dexaflow managed
# connection (not a baked profiles.yml): the runtime generates profiles.yml in the
# pod from the connection delivered over the agent seam.
#
# The proof is adversarial: the image bakes a DELIBERATELY BROKEN profiles.yml
# (bad host). If every task still reaches success and the mart materializes, the
# runtime must have generated the real profile from the managed connection before
# dbt ran — the baked one is never used.
#
# Requirements: k3d, kubectl, docker, jq, curl, dbt (dbt-core + dbt-postgres on
# PATH), `make build`, and a running dev database (`make dev-up`). Run from root.
# On Linux/CI set LEOFLOW_E2E_HOST_ADDR=host.k3d.internal.
set -euo pipefail

# shellcheck source=test/e2e/lib.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
CLUSTER="${LEOFLOW_E2E_CLUSTER:-leoflow-dbt-conn-e2e}"
HOST_ADDR="${LEOFLOW_E2E_HOST_ADDR:-$([ "$(uname -s)" = Linux ] && echo host.k3d.internal || echo host.docker.internal)}"
HTTP_PORT="${LEOFLOW_E2E_HTTP_PORT:-8080}"
GRPC_PORT="${LEOFLOW_E2E_GRPC_PORT:-9091}"
METRICS_PORT="${LEOFLOW_E2E_METRICS_PORT:-9090}"
WH_PORT="${LEOFLOW_E2E_WAREHOUSE_PORT:-15533}"
PY_VERSION="${LEOFLOW_E2E_PY_VERSION:-3.11}"
BASE_IMAGE="leoflow-base:py${PY_VERSION}"
DAG_IMAGE="leoflow-dbt-conn-dag:dev"
DAG_ID="dbt_conn"
WAREHOUSE="leoflow-dbt-conn-wh"
API="http://localhost:${HTTP_PORT}"
WORKDIR="$(mktemp -d)"

# dump_pods prints the task pods, a describe tail for those not Running, and
# their logs — the evidence for diagnosing a failed task before the cleanup trap
# deletes the cluster. Without it a failed run leaves only the server log, which
# says nothing about why a pod exited.
dump_pods() {
  printf '\033[1;33m--- task pods (namespace leoflow) ---\033[0m\n' >&2
  kubectl get pods -n leoflow -o wide >&2 2>&1 || true
  for p in $(kubectl get pods -n leoflow -o name 2>/dev/null); do
    phase="$(kubectl get -n leoflow "$p" -o jsonpath='{.status.phase}' 2>/dev/null)"
    if [ "$phase" != "Running" ]; then
      printf '\033[1;33m--- describe %s (%s) ---\033[0m\n' "$p" "$phase" >&2
      kubectl describe -n leoflow "$p" >&2 2>&1 | tail -25 || true
    fi
    printf '\033[1;33m--- logs %s ---\033[0m\n' "$p" >&2
    kubectl logs -n leoflow "$p" --all-containers --tail=80 >&2 2>&1 || true
  done
}
fail() { echo "FAIL: $*" >&2; dump_pods; exit 1; }
log() { printf '\033[1;34m==>\033[0m %s\n' "$*"; }

SERVER_PID=""
cleanup() {
  set +e
  [ -n "$SERVER_PID" ] && kill "$SERVER_PID" 2>/dev/null
  k3d cluster delete "$CLUSTER" >/dev/null 2>&1
  docker rm -f "$WAREHOUSE" >/dev/null 2>&1
  rm -rf "$WORKDIR"
}
trap cleanup EXIT

for tool in k3d kubectl docker jq curl dbt; do
  command -v "$tool" >/dev/null || fail "missing required tool: $tool"
done
[ -x "$ROOT/bin/leoflow" ] && [ -x "$ROOT/bin/leoflow-server" ] || fail "build first: make build"

log "Starting the shared Postgres warehouse on :${WH_PORT}"
docker rm -f "$WAREHOUSE" >/dev/null 2>&1 || true
docker run -d --name "$WAREHOUSE" -p "${WH_PORT}:5432" \
  -e POSTGRES_PASSWORD=pw -e POSTGRES_DB=wh postgres:16-alpine >/dev/null
for _ in $(seq 1 30); do
  docker exec "$WAREHOUSE" pg_isready -U postgres >/dev/null 2>&1 && break
  sleep 1
done

PROJ="$WORKDIR/$DAG_ID"
mkdir -p "$PROJ/models" "$PROJ/seeds"
cat >"$PROJ/dbt_project.yml" <<'YAML'
name: 'shop'
version: '1.0.0'
profile: 'shop'
model-paths: ["models"]
seed-paths: ["seeds"]
models: { shop: { +materialized: table } }
YAML
# The baked profiles.yml is DELIBERATELY BROKEN — if used, dbt cannot connect.
cat >"$PROJ/profiles.yml" <<'YAML'
shop:
  target: dev
  outputs:
    dev: { type: postgres, host: BROKEN.invalid, port: 1, user: nope, password: nope, dbname: nope, schema: nope }
YAML
printf 'id,v\n1,10\n2,20\n3,30\n' >"$PROJ/seeds/raw.csv"
echo "select id, v from {{ ref('raw') }}" >"$PROJ/models/stg.sql"
echo "select id, sum(v) as total from {{ ref('stg') }} group by id" >"$PROJ/models/mart.sql"

log "Generating the manifest (dbt parse is offline; the bad host is fine for parse)"
( cd "$PROJ" && DBT_PROFILES_DIR="$PROJ" dbt parse >/dev/null 2>&1 ) || fail "dbt parse failed"

# Pin the DAG image to the host arch (see e2e.sh): the loader defaults to
# linux/amd64, which fails FROM an arm64 base on a Lima/dev host → ErrImagePull.
case "$(uname -m)" in arm64|aarch64) HOST_PLATFORM="linux/arm64" ;; *) HOST_PLATFORM="linux/amd64" ;; esac
cat >"$PROJ/dexaflow.yaml" <<YAML
schema_version: "1.0"
dag_id: ${DAG_ID}
build:
  platforms:
    - ${HOST_PLATFORM}
dbt:
  project: .
  manifest: target/manifest.json
  granularity: node
  connection: warehouse_pg
YAML
cat >"$PROJ/Dockerfile" <<DOCKER
FROM ${BASE_IMAGE}
USER root
RUN pip install --no-cache-dir "dbt-postgres==1.9.*"
COPY . /home/leoflow/
ENV DBT_PROFILES_DIR=/home/leoflow
RUN chown -R 65532:65532 /home/leoflow
# Numeric UID so PodSecurity runAsNonRoot (on by default) admits the pod: the
# kubelet cannot verify a login NAME. Matches the base image's USER 65532:65532.
USER 65532:65532
WORKDIR /home/leoflow
DOCKER

log "Building the leoflow base image"
docker build --provenance=false -q -f "$ROOT/runtime/Dockerfile" --build-arg "PYTHON_VERSION=${PY_VERSION}" -t "$BASE_IMAGE" "$ROOT" >/dev/null

log "Creating k3d cluster '$CLUSTER' + the leoflow namespace"
k3d cluster delete "$CLUSTER" >/dev/null 2>&1 || true
k3d cluster create "$CLUSTER" --wait >/dev/null
kubectl create namespace leoflow >/dev/null

log "Starting the control plane (secret delivery enabled for the managed connection)"
export LEOFLOW_AUTH_JWT_SECRET="e2e-secret"
export LEOFLOW_BOOTSTRAP_PASSWORD="admin"
export LEOFLOW_EXECUTOR_AGENT_CONTROL_PLANE_ADDR="${HOST_ADDR}:${GRPC_PORT}"
export LEOFLOW_LOGS_DIR="${WORKDIR}/logs"
export LEOFLOW_SERVER_HTTP_ADDR="0.0.0.0:${HTTP_PORT}"
export LEOFLOW_SERVER_METRICS_ADDR="0.0.0.0:${METRICS_PORT}"
export LEOFLOW_AGENT_ALLOW_INSECURE_SECRETS=true
export LEOFLOW_SECRET_KEY="e2e-secret-key-32-bytes-padding!"
# Run under ENFORCE scoping so this gates #10: only DECLARED names are delivered,
# so the task only gets AIRFLOW_CONN_WAREHOUSE_PG because the compiler declared the
# dbt managed connection. Under permissive (the old default) the whole vault would
# be delivered and the declaration bug would hide.
export LEOFLOW_AUTH_SECRET_SCOPING=enforce
"$ROOT/bin/leoflow-server" >"$WORKDIR/server.log" 2>&1 &
SERVER_PID=$!
sleep 5

log "Compiling (commands must be wrapped with the --dbt-profile step) + building the image"
"$ROOT/bin/leoflow" compile "$PROJ" --image "$DAG_IMAGE" --build --dockerfile Dockerfile -o "$PROJ/dag.json"
jq -e '.tasks[] | select(.entrypoint | startswith("python -m leoflow_runtime --dbt-profile warehouse_pg shop &&"))' \
  "$PROJ/dag.json" >/dev/null || fail "task commands are not wrapped with the managed-profile step"
k3d_import "$CLUSTER" "$BASE_IMAGE" "$DAG_IMAGE"

log "Creating the managed Postgres connection FIRST, then pushing the DAG"
# The DAG declares warehouse_pg (the dbt compiler declares the managed connection,
# #10). Under enforce scoping, registration validates a declared connection exists
# (ADR 0055 D6), so the connection must be created BEFORE the push — the correct
# create-then-deploy order. (An external secrets backend would satisfy D6 via
# coverage instead, with no pre-created vault entry.)
TOKEN="$("$ROOT/bin/leoflow" auth create-token --server "$API" --username admin@leoflow.local --password admin)"
curl -fsS -X POST -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d "{\"connection_id\":\"warehouse_pg\",\"conn_type\":\"postgres\",\"host\":\"${HOST_ADDR}\",\"port\":${WH_PORT},\"login\":\"postgres\",\"password\":\"pw\",\"schema\":\"wh\"}" \
  "$API/api/v2/connections" >/dev/null || fail "creating the warehouse_pg connection"
"$ROOT/bin/leoflow" push "$PROJ/dag.json" --server "$API" --token "$TOKEN"

log "Triggering a run"
RUN_ID="$(curl -fsS -X POST -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{}' "$API/api/v2/dags/$DAG_ID/dagRuns" | jq -r '.dag_run_id')"
[ -n "$RUN_ID" ] && [ "$RUN_ID" != "null" ] || fail "no dag_run_id returned"

log "Waiting for success — the pods must generate profiles.yml from the connection (the baked one is broken)"
deadline=$(( $(date +%s) + 300 ))
while :; do
  states="$(curl -fsS -H "Authorization: Bearer $TOKEN" \
    "$API/api/v2/dags/$DAG_ID/dagRuns/$RUN_ID/taskInstances" | jq -r '.task_instances[].state')"
  echo "  states: $(echo "$states" | tr '\n' ' ')"
  if echo "$states" | grep -qE 'failed|upstream_failed'; then
    echo "--- server.log tail ---"; tail -30 "$WORKDIR/server.log"
    fail "a task failed — the managed-connection profile generation may be broken"
  fi
  if [ -n "$states" ] && ! echo "$states" | grep -qvE 'success'; then break; fi
  [ "$(date +%s)" -lt "$deadline" ] || fail "timeout waiting for all tasks to succeed"
  sleep 6
done

log "Asserting the mart materialized (proves the managed connection, not the broken baked profile)"
rows="$(docker exec "$WAREHOUSE" psql -U postgres -d wh -tAc 'select count(*) from public.mart;' | tr -d '[:space:]')"
[ "$rows" = "3" ] || fail "mart row count = $rows, want 3"

log "dbt managed-connection e2e passed"
