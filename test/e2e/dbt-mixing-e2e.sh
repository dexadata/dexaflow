#!/usr/bin/env bash
#
# End-to-end test for mixing dbt with operators in one DAG (ADR 0043) on a real
# k3d cluster. A dag.py wires real operators around a dbt_group(); the compiler
# merges them into one dag.json, and the scheduler runs operators AND per-model
# dbt pods in the right order, against a shared Postgres warehouse.
#
# Layout: the dbt project is a subdir (transform/) next to dag.py — the realistic
# mixing layout. Each dbt command is scoped with --project-dir (#401) so the pod
# finds dbt_project.yml regardless of its working dir.
#
# Requirements: k3d, kubectl, docker, jq, curl, dbt, python3, `make build`, dev DB.
# On Linux/CI: LEOFLOW_E2E_HOST_ADDR=host.k3d.internal and PYTHONPATH=parser. A
# stale ~/.dexaflow parser cache (issue #400) can be bypassed with
# LEOFLOW_E2E_PARSER_CMD.
set -euo pipefail

# shellcheck source=test/e2e/lib.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
CLUSTER="${LEOFLOW_E2E_CLUSTER:-leoflow-dbt-mix-e2e}"
HOST_ADDR="${LEOFLOW_E2E_HOST_ADDR:-$([ "$(uname -s)" = Linux ] && echo host.k3d.internal || echo host.docker.internal)}"
HTTP_PORT="${LEOFLOW_E2E_HTTP_PORT:-8080}"
GRPC_PORT="${LEOFLOW_E2E_GRPC_PORT:-9091}"
METRICS_PORT="${LEOFLOW_E2E_METRICS_PORT:-9090}"
WH_PORT="${LEOFLOW_E2E_WAREHOUSE_PORT:-15544}"
PY_VERSION="${LEOFLOW_E2E_PY_VERSION:-3.11}"
BASE_IMAGE="leoflow-base:py${PY_VERSION}"
DAG_IMAGE="leoflow-dbt-mix-dag:dev"
DAG_ID="mix"
WAREHOUSE="leoflow-dbt-mix-wh"
API="http://localhost:${HTTP_PORT}"
WORKDIR="$(mktemp -d)"
# shellcheck disable=SC2206
# Expanded below as ${PARSER_CMD[@]+...}: on bash 3.2 — macOS, where releases are
# cut from — "${arr[@]}" over an EMPTY array is an error under set -u. The
# assignment here is fine; only the expansion at the compile step is not, so the
# script died AFTER the warehouse, the dbt parse, the base image build and the
# k3d cluster create. Five minutes in, which is why nobody ran it locally twice.
PARSER_CMD=(${LEOFLOW_E2E_PARSER_CMD:+--parser-cmd "$LEOFLOW_E2E_PARSER_CMD"})

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

for tool in k3d kubectl docker jq curl dbt python3; do
  command -v "$tool" >/dev/null || fail "missing required tool: $tool"
done
[ -x "$ROOT/bin/leoflow" ] && [ -x "$ROOT/bin/leoflow-server" ] || fail "build first: make build"

log "Starting the shared Postgres warehouse on :${WH_PORT}"
docker rm -f "$WAREHOUSE" >/dev/null 2>&1 || true
docker run -d --name "$WAREHOUSE" -p "${WH_PORT}:5432" \
  -e POSTGRES_PASSWORD=pw -e POSTGRES_DB=wh postgres:16-alpine >/dev/null
for _ in $(seq 1 30); do docker exec "$WAREHOUSE" pg_isready -U postgres >/dev/null 2>&1 && break; sleep 1; done

# The DAG = a dbt project at the root + a dag.py that wires operators around it.
PROJ="$WORKDIR/$DAG_ID"
mkdir -p "$PROJ/transform/models" "$PROJ/transform/seeds"
# PythonOperators, not Bash. A bash task never imports dag.py, so this fixture
# used to execute the group without ever executing the DAG module — which is
# exactly why #17 (`from leoflow import dbt_group` unresolvable in the task
# image) shipped green. The runtime re-imports this file for every python task.
cat >"$PROJ/dag.py" <<'PY'
from leoflow import dbt_group
from airflow.providers.standard.operators.python import PythonOperator
from airflow.sdk import DAG


def pre():
    print("pre")


def post():
    print("post")


with DAG("mix", schedule="@daily"):
    before = PythonOperator(task_id="pre", python_callable=pre)
    models = dbt_group("transform")
    after = PythonOperator(task_id="post", python_callable=post)
    before >> models >> after
PY
cat >"$PROJ/dexaflow.yaml" <<'YAML'
schema_version: "1.0"
dag_id: mix
# The adapter is declared, not installed by a hand-written Dockerfile: the
# generated one has to carry it, and that is half of what this test now proves.
dependencies:
  - dbt-postgres==1.9.*
  # A version FLOOR, on purpose (#1064). `RUN` is /bin/sh -c, so an unquoted
  # `>=` is a redirection: pip used to receive a bare `wheel`, the floor
  # vanished, and a file named `=0.45` appeared in the image. Tiny package with
  # no dependencies, so this costs the build almost nothing.
  - "wheel>=0.45"
dbt_groups:
  transform:
    project: ./transform
    manifest: target/manifest.json
    granularity: node
YAML
# Pin the DAG image to the host arch (see e2e.sh): the loader defaults to
# linux/amd64, which fails FROM an arm64 base on a Lima/dev host → ErrImagePull.
case "$(uname -m)" in arm64|aarch64) HOST_PLATFORM="linux/arm64" ;; *) HOST_PLATFORM="linux/amd64" ;; esac
cat >>"$PROJ/dexaflow.yaml" <<YAML
# base_image: the base THIS checkout just built. Without it the generated
# Dockerfile resolves to the published moving tag, so the test would measure the
# last release's image instead of the code under review — wrong in either
# direction, and today it happens to fail rather than pass, because the published
# py3.11 tag predates #991 and has no leoflow package. The hand-written Dockerfile
# this fixture used to ship made the FROM explicit, so the question never arose.
base_image: ${BASE_IMAGE}
build:
  platforms:
    - ${HOST_PLATFORM}
YAML
cat >"$PROJ/transform/dbt_project.yml" <<'YAML'
name: 'shop'
version: '1.0.0'
profile: 'shop'
model-paths: ["models"]
seed-paths: ["seeds"]
models: { shop: { +materialized: table } }
YAML
cat >"$PROJ/transform/profiles.yml" <<YAML
shop:
  target: dev
  outputs:
    dev: { type: postgres, host: ${HOST_ADDR}, port: ${WH_PORT}, user: postgres, password: pw, dbname: wh, schema: public, threads: 4 }
YAML
printf 'id,v\n1,10\n2,20\n3,30\n' >"$PROJ/transform/seeds/raw.csv"
echo "select id, v from {{ ref('raw') }}" >"$PROJ/transform/models/stg.sql"
echo "select id, sum(v) as total from {{ ref('stg') }} group by id" >"$PROJ/transform/models/mart.sql"

log "Generating the manifest"
( cd "$PROJ/transform" && DBT_PROFILES_DIR="$PROJ/transform" dbt parse >/dev/null 2>&1 ) || fail "dbt parse failed"

# No hand-written Dockerfile. This fixture used to ship one, and it was an
# escape hatch that hid three defects at once: it COPYed the whole context (so
# #20's missing dbt_groups COPY never bit), it set DBT_PROFILES_DIR at the
# project (so #994's unreachable profiles.yml never bit), and it chown'd
# /home/leoflow to the task user (so #852's root-owned project was never actually
# exercised). Two of those are the generated Dockerfile's job; the third is not —
# --profiles-dir is decorated onto the entrypoint by dbtProfilesDir, and the
# Dockerfile carries no profiles logic at all.

log "Building the leoflow base image"
docker build --provenance=false -q -f "$ROOT/runtime/Dockerfile" --build-arg "PYTHON_VERSION=${PY_VERSION}" -t "$BASE_IMAGE" "$ROOT" >/dev/null

log "Creating k3d cluster + namespace"
k3d cluster delete "$CLUSTER" >/dev/null 2>&1 || true
k3d cluster create "$CLUSTER" --wait >/dev/null
kubectl create namespace leoflow >/dev/null

log "Starting the control plane"
export LEOFLOW_AUTH_JWT_SECRET="e2e-secret"
export LEOFLOW_BOOTSTRAP_PASSWORD="admin"
export LEOFLOW_EXECUTOR_AGENT_CONTROL_PLANE_ADDR="${HOST_ADDR}:${GRPC_PORT}"
export LEOFLOW_LOGS_DIR="${WORKDIR}/logs"
export LEOFLOW_SERVER_HTTP_ADDR="0.0.0.0:${HTTP_PORT}"
export LEOFLOW_SERVER_METRICS_ADDR="0.0.0.0:${METRICS_PORT}"
"$ROOT/bin/leoflow-server" >"$WORKDIR/server.log" 2>&1 &
SERVER_PID=$!
sleep 5

log "Compiling (parser merges the operators with the dbt_group) + building the image"
"$ROOT/bin/leoflow" compile "$PROJ" ${PARSER_CMD[@]+"${PARSER_CMD[@]}"} --image "$DAG_IMAGE" --build -o "$PROJ/dag.json"
log "Asserting the merge: operators + namespaced dbt tasks wired together"
for want in pre post transform__raw transform__stg transform__mart; do
  jq -e --arg t "$want" '.tasks[] | select(.task_id==$t)' "$PROJ/dag.json" >/dev/null || fail "missing task $want"
done
jq -e '.tasks[] | select(.task_id=="transform__raw") | .depends_on | index("pre")' "$PROJ/dag.json" >/dev/null \
  || fail "group root transform__raw is not wired to the upstream operator 'pre'"
jq -e '.tasks[] | select(.task_id=="post") | .depends_on | index("transform__mart")' "$PROJ/dag.json" >/dev/null \
  || fail "downstream operator 'post' is not wired to the group leaf"
# The image the COMPILER produced, asserted directly. A red run only says "a task
# failed"; these name the property.
#
# The write probe below is the one the cluster run cannot stand in for. Nothing dbt
# does in the pod touches the project: the base image aims DBT_TARGET_PATH /
# DBT_LOG_PATH / DBT_PROFILES_DIR at /tmp (#852), and the one file dbt would drop in
# the profiles dir — its .user.yml tracking cookie — is already baked in by the host
# `dbt parse` above, so it is read, not written. Where no cookie is baked, dbt
# swallows the EACCES (tracking.py initialize_from_flags) and stops tracking. Either
# way a writable project produces an identical green run, so only this block can tell
# the two apart.
log "Asserting the generated image"
docker run --rm --entrypoint sh "$DAG_IMAGE" -c 'test -f /home/leoflow/dag.py' \
  || fail "the generated Dockerfile did not COPY the DAG source"
docker run --rm --entrypoint sh "$DAG_IMAGE" -c 'test -f /home/leoflow/transform/dbt_project.yml' \
  || fail "the generated Dockerfile did not COPY the dbt_groups project (#20)"
docker run --rm -w / -e PYTHONPATH= --entrypoint python "$DAG_IMAGE" -c 'from leoflow import dbt_group; import leoflow_runtime' \
  || fail "the authoring package is not importable in the task image (#17)"
docker run --rm --entrypoint sh "$DAG_IMAGE" -c 'id -u | grep -qx 65532' \
  || fail "the image does not end on the numeric non-root UID the kubelet checks"
docker run --rm --entrypoint sh "$DAG_IMAGE" -c 'test -d /home/leoflow/transform && ! touch /home/leoflow/transform/WRITE_PROBE 2>/dev/null' \
  || fail "the dbt project is writable by the task user — #852's root-owned COPY is not holding"
jq -e '[.tasks[] | select(.task_id | startswith("transform__")) | .entrypoint | test("--profiles-dir")] | all' "$PROJ/dag.json" >/dev/null \
  || fail "no --profiles-dir baked: the project ships profiles.yml and the base image points DBT_PROFILES_DIR at /tmp (#994)"
jq -e '[.tasks[] | .entrypoint // "" | test("--(project|profiles)-dir /") | not] | all' "$PROJ/dag.json" >/dev/null \
  || fail "an absolute host path was baked into a task entrypoint (#993)"

# The adapter must come from dependencies:, since no hand-written Dockerfile
# installs it any more. Without this, deleting the pip layer from the generated
# Dockerfile leaves every assertion above green. Import the adapter rather than
# probing for the dbt binary, so the check and its message are one statement.
docker run --rm --entrypoint python "$DAG_IMAGE" -c 'import dbt.adapters.postgres' \
  || fail "the adapter declared in dependencies: is not installed in the generated image"

# #1064 — a version floor must survive into the image. An unquoted `>=` was a
# shell redirection, so the operand became a filename holding pip's stdout.
#
# This asserts the ARTIFACT, not the version, and that is deliberate. The
# obvious assertion — "the installed wheel satisfies the declared floor" — is
# one that cannot fail here: the runtime base already ships wheel 0.46.3, above
# any floor low enough to be safe to declare, so pip leaves it alone whether or
# not the constraint survived. The version symptom only appears when the base
# carries something OLDER, and there is no way to stage that through a
# chart-generated Dockerfile. A test that cannot fail is worse than no test, so
# it is not written; the version property is covered by the unit tier, which
# asserts what pip was actually handed.
docker run --rm --entrypoint sh "$DAG_IMAGE" -c 'ls -A /home/leoflow | grep -q "^=" && exit 1 || exit 0' \
  || fail "a redirection artifact (=<version>) is in the image: a dependency operator escaped the RUN quoting (#1064)"

# #993 CANNOT manifest under --build: `local` is false either way there, so the
# assertion above is a canary, not a proof. The defect needs a bare compile —
# which is also the door a CI or prebuilt-image workflow actually walks through.
"$ROOT/bin/leoflow" compile "$PROJ" ${PARSER_CMD[@]+"${PARSER_CMD[@]}"} --image "$DAG_IMAGE" -o "$PROJ/nobuild.json"
# Anchor first. The #993 check below iterates ALL tasks, so if the group ever
# stopped expanding it would not go green on an empty array — it would go green
# measuring `pre`/`post`, which carry no --project-dir at all. Same shape it was
# added to remove, one level down. 3 = 1 seed + 2 models, the five nodes named above.
jq -e '[.tasks[] | select(.task_id | startswith("transform__"))] | length == 3' "$PROJ/nobuild.json" >/dev/null \
  || fail "the bare compile expanded $(jq '[.tasks[]|select(.task_id|startswith("transform__"))]|length' "$PROJ/nobuild.json") transform__ tasks, want 3 — the #993 check below would pass without measuring the dbt tasks"
jq -e '[.tasks[] | .entrypoint // "" | test("--(project|profiles)-dir /") | not] | all' "$PROJ/nobuild.json" >/dev/null \
  || fail "a bare compile baked an absolute host path into a pod-bound dag.json (#993)"

k3d_import "$CLUSTER" "$BASE_IMAGE" "$DAG_IMAGE"

log "Pushing + triggering"
TOKEN="$("$ROOT/bin/leoflow" auth create-token --server "$API" --username admin@leoflow.local --password admin)"
"$ROOT/bin/leoflow" push "$PROJ/dag.json" --server "$API" --token "$TOKEN"
RUN_ID="$(curl -fsS -X POST -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{}' "$API/api/v2/dags/$DAG_ID/dagRuns" | jq -r '.dag_run_id')"
[ -n "$RUN_ID" ] && [ "$RUN_ID" != "null" ] || fail "no dag_run_id returned"

log "Waiting for all tasks (operators + dbt pods) to succeed"
deadline=$(( $(date +%s) + 300 ))
while :; do
  states="$(curl -fsS -H "Authorization: Bearer $TOKEN" \
    "$API/api/v2/dags/$DAG_ID/dagRuns/$RUN_ID/taskInstances" | jq -r '.task_instances[].state')"
  echo "  states: $(echo "$states" | tr '\n' ' ')"
  if echo "$states" | grep -qE 'failed|upstream_failed'; then
    echo "--- server.log tail ---"; tail -30 "$WORKDIR/server.log"; fail "a task failed"
  fi
  if [ -n "$states" ] && ! echo "$states" | grep -qvE 'success'; then break; fi
  [ "$(date +%s)" -lt "$deadline" ] || fail "timeout"
  sleep 6
done

log "Asserting the mart materialized (dbt pods ran in the merged DAG)"
rows="$(docker exec "$WAREHOUSE" psql -U postgres -d wh -tAc 'select count(*) from public.mart;' | tr -d '[:space:]')"
[ "$rows" = "3" ] || fail "mart row count = $rows, want 3"

log "dbt + operators mixed e2e passed"
