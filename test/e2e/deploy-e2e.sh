#!/usr/bin/env bash
#
# End-to-end smoke test for `dexaflow deploy` (ADR 0041) on a local k3d cluster.
#
# Unlike e2e.sh (which `k3d image import`s the DAG image), this exercises the
# REAL deploy path: a k3d-managed registry the cluster pulls from, `dexaflow auth
# login` to persist a token, then a single `dexaflow deploy` that compiles, builds
# for the cluster arch, PUSHES to the registry, captures the image digest,
# re-pins dag.json by digest, and registers it. It then triggers a run and
# asserts the task reaches 'success' — i.e. the cluster pulled the digest-pinned
# image from the registry and ran it. This is the slice unit tests left to e2e.
#
# Requirements: k3d, kubectl, docker, jq, curl, the leoflow binaries
# (`make build`), and a running dev database (`make dev-up`). Developer/CI tool.
#
# The registry is named `k3d-registry.localhost`: `.localhost` resolves to the
# loopback on the host, so `docker push` works over HTTP (loopback is insecure-OK)
# and the SAME ref resolves inside the cluster via k3d's registry mirror — no
# /etc/hosts edit, no insecure-registry config.
#
# Usage: test/e2e/deploy-e2e.sh [cluster-name]
set -euo pipefail

CLUSTER="${1:-leoflow-deploy-e2e}"
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
WORKDIR="$(mktemp -d)"
PY_VERSION="3.11"
BASE_IMAGE="leoflow-base:py${PY_VERSION}"
DAG_ID="deploydag"
REG_NAME="registry.localhost"          # k3d prefixes 'k3d-' → k3d-registry.localhost
REG_PORT="5111"
REG_FULLNAME="k3d-${REG_NAME}"         # the registry node name (for k3d registry delete)
REG_HOST="${REG_FULLNAME}:${REG_PORT}" # same ref on host (loopback) and in-cluster
# Default off 8080: on the dev Mac that port is the Lima VM's forward (do not
# fight it). Override with LEOFLOW_E2E_HTTP_PORT.
HTTP_PORT="${LEOFLOW_E2E_HTTP_PORT:-18080}"
API="http://localhost:${HTTP_PORT}"
GRPC_PORT="9091"
HOST_ADDR="${LEOFLOW_E2E_HOST_ADDR:-$([ "$(uname -s)" = Linux ] && echo host.k3d.internal || echo host.docker.internal)}"
CFG="${WORKDIR}/leoflow-config.yaml"   # login persists token+server here
SERVER_PID=""

# The parser runs in-process via `python3 -m leoflow_parser` (compile, inside
# deploy). Make it resolvable without an install.
export PYTHONPATH="${ROOT}/parser:${PYTHONPATH:-}"

# Build for the cluster's architecture. k3d nodes run the host arch, so on an
# arm64 Mac the image must be linux/arm64 (deploy's linux/amd64 default would
# exec-format-error on an arm64 node); on amd64 CI it is linux/amd64.
case "$(uname -m)" in
  arm64|aarch64) PLATFORM="linux/arm64" ;;
  *)             PLATFORM="linux/amd64" ;;
esac

log()  { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
dump_pods() {
  printf '\033[1;33m--- task pods (namespace leoflow) ---\033[0m\n' >&2
  kubectl get pods -n leoflow -o wide >&2 2>&1 || true
  for p in $(kubectl get pods -n leoflow -o name 2>/dev/null); do
    printf '\033[1;33m--- describe/logs %s ---\033[0m\n' "$p" >&2
    kubectl describe -n leoflow "$p" >&2 2>&1 | tail -20 || true
    kubectl logs -n leoflow "$p" --all-containers --tail=60 >&2 2>&1 || true
  done
}
fail() { printf '\033[1;31mFAIL:\033[0m %s\n' "$*" >&2; dump_pods; exit 1; }

cleanup() {
  [ -n "$SERVER_PID" ] && kill "$SERVER_PID" 2>/dev/null || true
  k3d cluster delete "$CLUSTER" >/dev/null 2>&1 || true
  k3d registry delete "$REG_FULLNAME" >/dev/null 2>&1 || true
  rm -rf "$WORKDIR"
}
trap cleanup EXIT

for tool in k3d kubectl docker jq curl; do
  command -v "$tool" >/dev/null || fail "missing required tool: $tool"
done

log "Building the task base image ($BASE_IMAGE)"
docker build --provenance=false -f "$ROOT/runtime/Dockerfile" --build-arg "PYTHON_VERSION=${PY_VERSION}" -t "$BASE_IMAGE" "$ROOT"

log "Creating k3d registry ($REG_HOST) + cluster '$CLUSTER' (cluster pulls from the registry)"
k3d registry create "$REG_NAME" --port "$REG_PORT" >/dev/null
k3d cluster create "$CLUSTER" --registry-use "$REG_HOST" --wait
kubectl create namespace leoflow

log "Starting the control plane (agents dial ${HOST_ADDR}:${GRPC_PORT})"
export LEOFLOW_AUTH_JWT_SECRET="e2e-secret"
export LEOFLOW_BOOTSTRAP_PASSWORD="admin"
export LEOFLOW_SERVER_HTTP_ADDR="0.0.0.0:${HTTP_PORT}"
export LEOFLOW_EXECUTOR_AGENT_CONTROL_PLANE_ADDR="${HOST_ADDR}:${GRPC_PORT}"
export LEOFLOW_LOGS_DIR="${WORKDIR}/logs"
export LEOFLOW_AGENT_ALLOW_INSECURE_SECRETS=true
"$ROOT/bin/leoflow-server" &
SERVER_PID=$!
sleep 5

log "Scaffolding a DAG project with a registry: block ($DAG_ID, platform $PLATFORM)"
"$ROOT/bin/leoflow" init "$WORKDIR/$DAG_ID" >/dev/null
cat > "$WORKDIR/$DAG_ID/dag.py" <<'PY'
"""deploydag — dexaflow deploy smoke DAG."""
from __future__ import annotations

from airflow.sdk import DAG, task


@task
def hello() -> None:
    print("hello from dexaflow deploy e2e")


with DAG("deploydag", schedule="@daily", catchup=False, tags=["deploy-e2e"]):
    hello()
PY
# The DAG image layers FROM the local base; deploy pushes the COMPLETE image
# (all layers baked in) to the registry, so the cluster pulls it whole — no need
# to seed the base into the registry.
cat > "$WORKDIR/$DAG_ID/Dockerfile" <<DOCKER
FROM ${BASE_IMAGE}
COPY dag.py /home/leoflow/dag.py
ENV PYTHONPATH=/home/leoflow
DOCKER
# dexaflow.yaml: registry: makes deploy mandatory-registry happy; build.platforms
# matches the cluster arch.
cat > "$WORKDIR/$DAG_ID/dexaflow.yaml" <<YAML
dag_id: ${DAG_ID}
python_version: "${PY_VERSION}"
build:
  platforms:
    - ${PLATFORM}
registry:
  url: ${REG_HOST}
  image_name: ${DAG_ID}
YAML

log "dexaflow auth login (persists token to \$CFG)"
"$ROOT/bin/leoflow" auth login --server "$API" \
  --username admin@leoflow.local --password admin --config "$CFG"

log "dexaflow deploy — compile → build($PLATFORM) → push($REG_HOST) → digest re-pin → register"
# Unique version per run so a re-run against the shared dev DB does not collide
# with a prior run's registered version (dag_versions is unique per dag+version).
"$ROOT/bin/leoflow" deploy "$WORKDIR/$DAG_ID" --yes --config "$CFG" \
  --dag-version "e2e-$(date +%s)"

log "Asserting dag.json was re-pinned by digest"
IMG="$(jq -r '.image' "$WORKDIR/$DAG_ID/dag.json")"
echo "$IMG" | grep -q "@sha256:" || fail "dag.json image not digest-pinned: $IMG"
echo "$IMG" | grep -q "^${REG_HOST}/${DAG_ID}@sha256:" || fail "unexpected image ref: $IMG"
log "registered image: $IMG"

TOKEN="$("$ROOT/bin/leoflow" auth create-token --server "$API" --username admin@leoflow.local --password admin)"

log "Triggering a run (the cluster must pull the digest-pinned image from the registry)"
RUN_ID="$(curl -fsS -X POST -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{}' "$API/api/v2/dags/$DAG_ID/dagRuns" | jq -r '.dag_run_id')"
[ -n "$RUN_ID" ] && [ "$RUN_ID" != "null" ] || fail "no dag_run_id returned"
log "run = $RUN_ID"

log "Waiting for the task to succeed (proves registry pull + pod-per-task run)"
deadline=$(( $(date +%s) + 300 ))
while :; do
  states="$(curl -fsS -H "Authorization: Bearer $TOKEN" \
    "$API/api/v2/dags/$DAG_ID/dagRuns/$RUN_ID/taskInstances" | jq -r '.task_instances[].state')"
  log "task states: [$(echo "$states" | tr '\n' ' ')] pods: [$(kubectl get pods -n leoflow --no-headers 2>/dev/null | awk '{print $1"="$3}' | tr '\n' ' ')]"
  echo "$states" | grep -qE 'failed|upstream_failed' && fail "a task failed: $states"
  if [ -n "$states" ] && ! echo "$states" | grep -qvE 'success|skipped'; then
    log "all tasks terminal-success: $states"
    break
  fi
  [ "$(date +%s)" -gt "$deadline" ] && fail "timed out; last states: ${states:-<none>}"
  sleep 3
done

log "Asserting the task logged from its pod"
hlog=""
for try in 0 1 2; do
  body="$(curl -fsS -H "Authorization: Bearer $TOKEN" \
    "$API/api/v2/dags/$DAG_ID/dagRuns/$RUN_ID/taskInstances/hello/logs/$try" 2>/dev/null || true)"
  if echo "$body" | grep -q "hello from dexaflow deploy e2e"; then hlog="$body"; break; fi
done
[ -n "$hlog" ] || fail "no 'hello' log shipped — the deployed image did not run"

# ---------------------------------------------------------------------------
# The TWO-STEP path, with a non-default tag strategy.
#
# Everything above drives a single `dexaflow deploy`, which compiles, builds,
# pushes and registers in one process. That is one of the two ways this is used
# and it is the one that cannot expose a disagreement, because only one tag
# resolver ever runs.
#
# CI/CD uses the other way: build in one job, deploy in another with
# --skip-build. There the build names the image and the deploy goes looking for
# it, and if the two derive the tag by different rules the deploy fails with
# "no such object" against an image that was pushed under another name. That is
# exactly what happened (#1227), and it happened on a real cloud run rather than
# here, because this file never separated the steps and never set a
# tag_strategy: with the default the two rules agree by coincidence.
#
# So this second project sets `tag_strategy: git_sha` on purpose, and the two
# commands are run separately. It asserts the deploy finds what the build
# pushed, which no unit test can: comparing the two resolvers passes if both are
# wrong in the same way, and only a real push and a real lookup rule that out.
TWO_STEP_ID="deploydag2step"
log "Two-step path: scaffolding $TWO_STEP_ID with tag_strategy: git_sha"
"$ROOT/bin/leoflow" init "$WORKDIR/$TWO_STEP_ID" >/dev/null
# Written out rather than copied from the first project. Copying its dag.py
# brought DAG("deploydag") along, which does not match this project's dag_id,
# and the parser rejects that pair. The first attempt did exactly that and died
# in step 1, never reaching the two-step seam it exists to exercise.
cat > "$WORKDIR/$TWO_STEP_ID/dag.py" <<PYDAG
"""${TWO_STEP_ID}: the two-step build and deploy path."""
from __future__ import annotations

from airflow.sdk import DAG, task


@task
def hello() -> None:
    print("hello from the two-step deploy e2e")


with DAG("${TWO_STEP_ID}", schedule="@daily", catchup=False, tags=["deploy-e2e"]):
    hello()
PYDAG
cat > "$WORKDIR/$TWO_STEP_ID/Dockerfile" <<DOCKER2
FROM ${BASE_IMAGE}
COPY dag.py /home/leoflow/dag.py
ENV PYTHONPATH=/home/leoflow
DOCKER2
cat > "$WORKDIR/$TWO_STEP_ID/dexaflow.yaml" <<YAML
dag_id: ${TWO_STEP_ID}
python_version: "${PY_VERSION}"
build:
  platforms:
    - ${PLATFORM}
registry:
  url: ${REG_HOST}
  image_name: ${TWO_STEP_ID}
  tag_strategy: git_sha
YAML

# A dag-version that is deliberately NOT the git sha, so a build that ignores
# tag_strategy pushes a different tag than a deploy that honors it. With the two
# equal the bug is invisible, which is how it survived.
TWO_STEP_VERSION="e2e-twostep-$(date +%s)"
log "Step 1: compile --build --push (version $TWO_STEP_VERSION, strategy git_sha)"
# Output captured and echoed on failure. "the two-step build failed" on its own
# is the shape that cost real money elsewhere in this repo, where a fatal call
# sent its stderr to /dev/null and the run reported that it failed without
# reporting why.
if ! "$ROOT/bin/leoflow" compile "$WORKDIR/$TWO_STEP_ID" \
     --output "$WORKDIR/$TWO_STEP_ID/dag.json" \
     --build --push --dag-version "$TWO_STEP_VERSION" > "$WORKDIR/twostep-build.log" 2>&1; then
  sed 's/^/    /' "$WORKDIR/twostep-build.log" >&2
  fail "the two-step build failed (its output is above)"
fi
grep -E 'naming to|Compiled' "$WORKDIR/twostep-build.log" | sed 's/^/    /' || true

log "Step 2: deploy --skip-build, which must find the image step 1 pushed"
if ! "$ROOT/bin/leoflow" deploy "$WORKDIR/$TWO_STEP_ID" --yes --config "$CFG" \
     --skip-build --dag-version "$TWO_STEP_VERSION" > "$WORKDIR/twostep-deploy.log" 2>&1; then
  sed 's/^/    /' "$WORKDIR/twostep-deploy.log" >&2
  fail "deploy --skip-build could not find the image the build pushed; the two steps disagree about the tag (#1227)"
fi

TWO_STEP_IMG="$(jq -r '.image' "$WORKDIR/$TWO_STEP_ID/dag.json")"
echo "$TWO_STEP_IMG" | grep -q "@sha256:" \
  || fail "the two-step dag.json is not digest-pinned: $TWO_STEP_IMG"
log "two-step registered image: $TWO_STEP_IMG"

log "DEPLOY E2E passed: one-shot deploy and the two-step CI/CD path both built, pushed, pinned and ran"
