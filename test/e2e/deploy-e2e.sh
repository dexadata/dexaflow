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
# Then it redeploys while that run is in flight (#1707): a second `dexaflow
# deploy` registers v2 while the v1 run holds its first task open. The v1 run
# must finish on v1 (the code its tasks print and the version they report), and
# the next run must run v2. The same scenario runs in both warm pool modes,
# chosen by DEXAFLOW_E2E_WARM_POOLS, because deploy behaves the same with and
# without warm pools:
#
#   off (default)  the shipped default: a dedicated pod per attempt (ADR 0002),
#                  and no warm worker pod may appear.
#   on             execution.warm_pools_enabled with the token exchange and
#                  liveness enforcement its boot check requires (ADR 0058 D2)
#                  and one idle worker per active version. Run 2 must have a
#                  task served by a warm worker (task_instances.warm_worker_id),
#                  and every warm-served task must have run on a warm pod of its
#                  own run's version and image, so no v1 pod serves v2 work.
#                  v1's pool drains once v2 is current (ADR 0082): run 1's
#                  later task may take a dedicated pod, and once run 1 ends no
#                  live warm pod of v1 may be left.
#
# Requirements: k3d, kubectl, docker, jq, curl, the leoflow binaries
# (`make build`), and a running dev database (`make dev-up`); with warm pools on,
# psql too. Developer/CI tool.
#
# Environment:
#   DEXAFLOW_E2E_WARM_POOLS     off (default) or on, see above
#   DEXAFLOW_E2E_HOLD_SECONDS   how long the first task of each run holds it open
#                               (default 30, 60 with warm pools on): long enough
#                               for the redeploy to land inside the run and, with
#                               warm pools on, for the reconciler (a 30s tick) to
#                               start a worker before the second task
#   DEXAFLOW_E2E_DATABASE_URL   the database the server uses, read with psql in
#                               warm mode (default: the server's default URL)
#
# The registry is named `k3d-registry.localhost`: `.localhost` resolves to the
# loopback on the host, so `docker push` works over HTTP (loopback is insecure-OK)
# and the SAME ref resolves inside the cluster via k3d's registry mirror — no
# /etc/hosts edit, no insecure-registry config.
#
# Usage: test/e2e/deploy-e2e.sh [cluster-name]
set -euo pipefail

# shellcheck source=test/e2e/lib.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

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
WARM_POOLS="${DEXAFLOW_E2E_WARM_POOLS:-off}"
# The warm leg needs the longer hold: its worker has to be up before `after`.
if [ "${DEXAFLOW_E2E_WARM_POOLS:-off}" = on ]; then
  HOLD_SECONDS="${DEXAFLOW_E2E_HOLD_SECONDS:-60}"
else
  HOLD_SECONDS="${DEXAFLOW_E2E_HOLD_SECONDS:-30}"
fi
DB_URL="${DEXAFLOW_E2E_DATABASE_URL:-postgres://leoflow:leoflow@localhost:5432/leoflow?sslmode=disable}"
NS="leoflow"
# Every warm worker pod seen while the runs were live, one "name<TAB>version
# label<TAB>image" line per sighting. A warm pod can be recycled before the
# end, so the bindings are checked against what was sampled, not against what
# is left.
WARM_SEEN="${WORKDIR}/warm-pods.tsv"
: > "$WARM_SEEN"
case "$WARM_POOLS" in
  on|off) ;;
  *) printf 'DEXAFLOW_E2E_WARM_POOLS must be on or off, got %q\n' "$WARM_POOLS" >&2; exit 2 ;;
esac

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
fail() {
  printf '\033[1;31mFAIL:\033[0m %s\n' "$*" >&2
  dump_pods
  if [ -s "${WARM_SEEN:-}" ]; then
    printf '\033[1;33m--- warm worker pods seen (name, version, image) ---\033[0m\n' >&2
    sort -u "$WARM_SEEN" >&2 || true
  fi
  exit 1
}

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
# The warm binding (task_instances.warm_worker_id) is not on the API, so warm
# mode reads it from the database.
if [ "$WARM_POOLS" = on ]; then
  command -v psql >/dev/null || fail "missing required tool for warm pools on: psql"
  psql "$DB_URL" -Atc 'SELECT 1' >/dev/null || fail "psql cannot reach $DB_URL (set DEXAFLOW_E2E_DATABASE_URL)"
fi

log "Building the task base image ($BASE_IMAGE)"
docker build --provenance=false -f "$ROOT/runtime/Dockerfile" --build-arg "PYTHON_VERSION=${PY_VERSION}" -t "$BASE_IMAGE" "$ROOT"

log "Creating k3d registry ($REG_HOST) + cluster '$CLUSTER' (cluster pulls from the registry)"
k3d registry create "$REG_NAME" --port "$REG_PORT" >/dev/null
k3d cluster create "$CLUSTER" --registry-use "$REG_HOST" --wait
kubectl create namespace leoflow
# Kubernetes creates the namespace's default ServiceAccount a moment after
# the namespace; wait for it so the deploy preflight finds it.
for _ in $(seq 1 60); do
  kubectl get serviceaccount default -n leoflow >/dev/null 2>&1 && break
  sleep 1
done
kubectl get serviceaccount default -n leoflow >/dev/null || fail "the default ServiceAccount of namespace leoflow did not appear within 60s"

log "Starting the control plane (agents dial ${HOST_ADDR}:${GRPC_PORT})"
export LEOFLOW_AUTH_JWT_SECRET="e2e-secret"
export LEOFLOW_BOOTSTRAP_PASSWORD="admin"
export LEOFLOW_SERVER_HTTP_ADDR="0.0.0.0:${HTTP_PORT}"
export LEOFLOW_EXECUTOR_AGENT_CONTROL_PLANE_ADDR="${HOST_ADDR}:${GRPC_PORT}"
export LEOFLOW_LOGS_DIR="${WORKDIR}/logs"
export LEOFLOW_AGENT_ALLOW_INSECURE_SECRETS=true
if [ "$WARM_POOLS" = on ]; then
  # The settings the boot check requires with warm pools (ADR 0058 D2), and one
  # idle worker per active version, since the default 0 is scale to zero. The
  # exchange transport applies to every task pod, warm or dedicated.
  export DEXAFLOW_EXECUTION_WARM_POOLS_ENABLED=true
  export DEXAFLOW_AUTH_AGENT_TOKEN_TRANSPORT=exchange
  export DEXAFLOW_AUTH_SECRET_LIVENESS_MODE=enforce
  export DEXAFLOW_EXECUTION_MIN_IDLE_WORKERS=1
fi
log "Warm pools: $WARM_POOLS"
"$ROOT/bin/leoflow-server" &
SERVER_PID=$!
# Ready, or exited: with warm pools on a refused boot exits at once, and the
# login below would otherwise fail with a message about something else.
for _ in $(seq 1 90); do
  curl -fsS "$API/readyz" >/dev/null 2>&1 && break
  kill -0 "$SERVER_PID" 2>/dev/null || fail "the control plane exited during boot (warm pools $WARM_POOLS)"
  sleep 1
done
curl -fsS "$API/readyz" >/dev/null 2>&1 || fail "the control plane did not become ready (warm pools $WARM_POOLS)"

log "Scaffolding a DAG project with a registry: block ($DAG_ID, platform $PLATFORM)"
"$ROOT/bin/leoflow" init "$WORKDIR/$DAG_ID" >/dev/null
# write_dag CODE writes the DAG for code version CODE. Each task prints the code
# it was built with, so a task log names the image that ran it: a version label
# or a database row can say v1 while v2 code runs, a line baked into the image
# cannot. The first task holds the run open so the redeploy lands inside it.
write_dag() {
  cat > "$WORKDIR/$DAG_ID/dag.py" <<PY
"""deploydag: dexaflow deploy smoke DAG, code $1."""
from __future__ import annotations

import time

from airflow.sdk import DAG, task


@task
def hold() -> None:
    print("E2E_CODE=$1 task=hold", flush=True)
    time.sleep(${HOLD_SECONDS})


@task
def after() -> None:
    print("E2E_CODE=$1 task=after", flush=True)


with DAG("deploydag", schedule="@daily", catchup=False, tags=["deploy-e2e"]):
    hold() >> after()
PY
}
write_dag v1
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
ADMIN_USER="$(admin_user "$ROOT/bin/leoflow" "$API" admin)"
"$ROOT/bin/leoflow" auth login --server "$API" \
  --username "$ADMIN_USER" --password admin --config "$CFG"

# Unique versions per run so a re-run against the shared dev DB does not collide
# with a prior run's registered version (dag_versions is unique per dag+version).
STAMP="$(date +%s)"
V1="e2e-${STAMP}-v1"
V2="e2e-${STAMP}-v2"

log "dexaflow deploy v1 ($V1): compile, build($PLATFORM), push($REG_HOST), digest re-pin, register"
"$ROOT/bin/leoflow" deploy "$WORKDIR/$DAG_ID" --yes --config "$CFG" --dag-version "$V1"

log "Asserting dag.json was re-pinned by digest"
IMG1="$(jq -r '.image' "$WORKDIR/$DAG_ID/dag.json")"
echo "$IMG1" | grep -q "@sha256:" || fail "dag.json image not digest-pinned: $IMG1"
echo "$IMG1" | grep -q "^${REG_HOST}/${DAG_ID}@sha256:" || fail "unexpected image ref: $IMG1"
log "registered v1 image: $IMG1"

TOKEN="$(admin_token "$ROOT/bin/leoflow" "$API" admin)"

api() { curl -fsS -H "Authorization: Bearer $TOKEN" "$API$1"; }

# sample_warm records every warm worker pod on the cluster right now.
sample_warm() {
  kubectl get pods -n "$NS" -l leoflow.io/warm-worker=true -o json 2>/dev/null \
    | jq -r '.items[] | [.metadata.name, (.metadata.labels["leoflow.io/dag-version-id"] // ""), .spec.containers[0].image] | @tsv' \
    >> "$WARM_SEEN" 2>/dev/null || true
}

trigger_run() {
  local rid
  rid="$(curl -fsS -X POST -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
    -d '{}' "$API/api/v2/dags/$DAG_ID/dagRuns" | jq -r '.dag_run_id')"
  [ -n "$rid" ] && [ "$rid" != "null" ] || fail "no dag_run_id returned"
  printf '%s' "$rid"
}

# wait_task_running RUN TASK waits until TASK of RUN runs, and fails if it is
# already over: the run must still be held open when the caller goes on.
wait_task_running() {
  local deadline state
  deadline=$(( $(date +%s) + 300 ))
  while :; do
    sample_warm
    state="$(api "/api/v2/dags/$DAG_ID/dagRuns/$1/taskInstances" \
      | jq -r --arg t "$2" '.task_instances[] | select(.task_id == $t) | .state')"
    case "$state" in
      running) return 0 ;;
      success|failed|upstream_failed|skipped)
        fail "$2 of run $1 is already $state, so the run was not held open (raise DEXAFLOW_E2E_HOLD_SECONDS)" ;;
    esac
    [ "$(date +%s)" -gt "$deadline" ] && fail "$2 of run $1 never ran; last state: ${state:-<none>}"
    sleep 2
  done
}

# wait_run_success RUN waits until every task of RUN succeeded.
wait_run_success() {
  local deadline states
  deadline=$(( $(date +%s) + 300 + HOLD_SECONDS ))
  while :; do
    sample_warm
    states="$(api "/api/v2/dags/$DAG_ID/dagRuns/$1/taskInstances" | jq -r '.task_instances[].state')"
    log "run $1 task states: [$(echo "$states" | tr '\n' ' ')] pods: [$(kubectl get pods -n "$NS" --no-headers 2>/dev/null | awk '{print $1"="$3}' | tr '\n' ' ')]"
    echo "$states" | grep -qE 'failed|upstream_failed' && fail "a task of run $1 failed: $states"
    if [ -n "$states" ] && ! echo "$states" | grep -qvE 'success|skipped'; then
      return 0
    fi
    [ "$(date +%s)" -gt "$deadline" ] && fail "run $1 timed out; last states: ${states:-<none>}"
    sleep 3
  done
}

# task_log RUN TASK prints the first try's log of TASK that has an E2E_CODE line.
task_log() {
  local body try
  for try in 1 0 2; do
    body="$(api "/api/v2/dags/$DAG_ID/dagRuns/$1/taskInstances/$2/logs/$try" 2>/dev/null || true)"
    if grep -q "E2E_CODE=" <<<"$body"; then printf '%s' "$body"; return 0; fi
  done
  fail "run $1: no log of $2 carries an E2E_CODE line; the deployed image did not run"
}

# assert_run_on RUN CODE LABEL IMAGE checks that every task of RUN ran code CODE
# and that the run is pinned to version LABEL on IMAGE. The API resolves
# dag_version once per run, not per task, so it proves the run's pin only; the
# per-task proof is the E2E_CODE line each task logs from its own image (and,
# with warm pools on, the version label of the warm pod that served it).
assert_run_on() {
  local task body other
  other=v1; [ "$2" = v1 ] && other=v2
  for task in hold after; do
    body="$(task_log "$1" "$task")"
    grep -q "E2E_CODE=$2 task=$task" <<<"$body" || fail "run $1: $task did not run code $2"
    if grep -q "E2E_CODE=$other" <<<"$body"; then fail "run $1: $task ran code $other, want $2"; fi
  done
  api "/api/v2/dags/$DAG_ID/dagRuns/$1/taskInstances" > "$WORKDIR/tis.json"
  jq -e --arg v "$3" --arg img "$4" \
    '(.task_instances | length) == 2 and (.task_instances | all(.dag_version.bundle_version == $v and .dag_version.image == $img))' \
    "$WORKDIR/tis.json" >/dev/null \
    || fail "run $1: the task instances do not all report version $3 on $4: $(jq -c '[.task_instances[] | {task_id, v: .dag_version.bundle_version, image: .dag_version.image}]' "$WORKDIR/tis.json")"
  log "run $1 ran code $2 and its task instances report $3"
}

# version_id LABEL prints the id of the registered version LABEL, the value
# warm pods carry in their leoflow.io/dag-version-id label.
version_id() {
  api "/api/v2/dags/$DAG_ID/dagVersions" \
    | jq -er --arg v "$1" '[.dag_versions[] | select(.bundle_version == $v)][0].id'
}

# wait_version_drained LABEL: with warm pools on, once LABEL is no longer the
# current version and its runs ended, the reconciler drains its pool (ADR 0082):
# within two reconcile ticks no warm pod of it is left that is not terminating.
wait_version_drained() {
  [ "$WARM_POOLS" = on ] || return 0
  local vid start live
  vid="$(version_id "$1")" || fail "no registered version $1"
  start=$(date +%s)
  while :; do
    live="$(kubectl get pods -n "$NS" -l "leoflow.io/warm-worker=true,leoflow.io/dag-version-id=$vid" -o json 2>/dev/null \
      | jq '[.items[] | select(.metadata.deletionTimestamp == null and (.status.phase == "Running" or .status.phase == "Pending"))] | length')"
    if [ "${live:-0}" -eq 0 ]; then
      log "version $1: its warm pool drained $(( $(date +%s) - start ))s after its last run ended"
      return 0
    fi
    [ $(( $(date +%s) - start )) -ge 90 ] \
      && fail "version $1 is superseded and has no active run, yet $live warm pods of it are still live after 90s; the previous pool must drain (ADR 0082)"
    sleep 3
  done
}

# wait_warm_worker RUN LABEL: with warm pools on, once RUN's first task holds
# the run open, a warm worker of the run's version must be Running before the
# hold ends (HOLD_SECONDS minus 10s of margin), or the second task cannot be
# served warm. Failing here says why, instead of a later "no warm hit".
wait_warm_worker() {
  [ "$WARM_POOLS" = on ] || return 0
  local vid start budget phases
  vid="$(version_id "$2")" || fail "no registered version $2"
  start=$(date +%s)
  budget=$(( HOLD_SECONDS - 10 ))
  while :; do
    sample_warm
    phases="$(kubectl get pods -n "$NS" -l "leoflow.io/warm-worker=true,leoflow.io/dag-version-id=$vid" \
      -o jsonpath='{range .items[*]}{.status.phase}{"\n"}{end}' 2>/dev/null || true)"
    if grep -qx Running <<<"$phases"; then
      log "run $1: a warm worker of version $2 is Running after $(( $(date +%s) - start ))s"
      return 0
    fi
    [ $(( $(date +%s) - start )) -ge "$budget" ] \
      && fail "run $1: no warm worker of version $2 ($vid) was Running $(( $(date +%s) - start ))s after its first task started (budget ${budget}s) (phases: [$(tr '\n' ' ' <<<"$phases")]); raise DEXAFLOW_E2E_HOLD_SECONDS or look at the warm pool reconciler"
    sleep 2
  done
}

# wait_host_dns waits until pods resolve $HOST_ADDR. k3d writes the host
# record into the CoreDNS NodeHosts ConfigMap at cluster creation, but the
# kubelet can take up to a minute to project it into the CoreDNS pod. A task
# that starts before that fails its token exchange ("produced zero
# addresses"). The probe runs the v1 image, so it also warms the node's image
# cache. It runs in the default namespace to stay out of the task pod listings.
wait_host_dns() {
  local probe=e2e-host-dns-probe phase deadline
  kubectl run "$probe" -n default --image="$IMG1" --restart=Never --command -- \
    python -c 'import socket, sys, time
for _ in range(90):
    try:
        print(socket.gethostbyname(sys.argv[1])); sys.exit(0)
    except OSError:
        time.sleep(2)
sys.exit(1)' "$HOST_ADDR" >/dev/null
  deadline=$(( $(date +%s) + 240 ))
  while :; do
    phase="$(kubectl get pod "$probe" -n default -o jsonpath='{.status.phase}' 2>/dev/null || true)"
    case "$phase" in
      Succeeded) break ;;
      Failed)
        kubectl logs -n default "$probe" >&2 2>&1 || true
        fail "pods never resolved $HOST_ADDR (CoreDNS NodeHosts)" ;;
    esac
    [ "$(date +%s)" -ge "$deadline" ] && fail "the $HOST_ADDR DNS probe did not finish (phase: ${phase:-none})"
    sleep 2
  done
  log "pods resolve $HOST_ADDR as $(kubectl logs -n default "$probe" 2>/dev/null)"
  kubectl delete pod "$probe" -n default --wait=false >/dev/null 2>&1 || true
}
wait_host_dns

# New DAGs are paused at creation, and a manual run of a paused DAG waits
# until the DAG is unpaused.
unpause_dag "$API" "$DAG_ID" "$TOKEN" || fail "unpausing $DAG_ID failed"
log "Triggering run 1 on v1 (the cluster must pull the digest-pinned image from the registry)"
RUN1="$(trigger_run)"
log "run 1 = $RUN1"
wait_task_running "$RUN1" hold
wait_warm_worker "$RUN1" "$V1"

log "dexaflow deploy v2 ($V2) while run 1 is in flight"
write_dag v2
"$ROOT/bin/leoflow" deploy "$WORKDIR/$DAG_ID" --yes --config "$CFG" --dag-version "$V2"
IMG2="$(jq -r '.image' "$WORKDIR/$DAG_ID/dag.json")"
echo "$IMG2" | grep -q "^${REG_HOST}/${DAG_ID}@sha256:" || fail "unexpected v2 image ref: $IMG2"
[ "$IMG2" != "$IMG1" ] || fail "v2 registered the v1 image $IMG1"
log "registered v2 image: $IMG2"
# The redeploy is only "during the run" if the run is still going after it.
RUN1_STATE="$(api "/api/v2/dags/$DAG_ID/dagRuns/$RUN1" | jq -r '.state')"
[ "$RUN1_STATE" = running ] \
  || fail "run 1 is '$RUN1_STATE' once v2 is registered, so the redeploy did not land in flight (raise DEXAFLOW_E2E_HOLD_SECONDS)"
# And its first task is still holding: `after` is dispatched only when `hold`
# ends, so this is what proves `after` was dispatched after v2 became current.
HOLD1_STATE="$(api "/api/v2/dags/$DAG_ID/dagRuns/$RUN1/taskInstances" \
  | jq -r '.task_instances[] | select(.task_id == "hold") | .state')"
[ "$HOLD1_STATE" = running ] \
  || fail "run 1's hold is '$HOLD1_STATE' once v2 is registered, so its after may have been dispatched before v2 (raise DEXAFLOW_E2E_HOLD_SECONDS)"
api "/api/v2/dags/$DAG_ID/dagVersions" \
  | jq -e --arg v "$V2" '[.dag_versions[] | select(.is_current)] | length == 1 and .[0].bundle_version == $v' >/dev/null \
  || fail "v2 is not the current version after its deploy"

log "Waiting for run 1 to finish on v1 (its second task is dispatched after v2 became current)"
wait_run_success "$RUN1"
assert_run_on "$RUN1" v1 "$V1" "$IMG1"
# v1 is superseded and its run ended: its warm pool drains (ADR 0082).
wait_version_drained "$V1"

log "Triggering run 2, which must run v2"
RUN2="$(trigger_run)"
log "run 2 = $RUN2"
wait_task_running "$RUN2" hold
wait_warm_worker "$RUN2" "$V2"
wait_run_success "$RUN2"
assert_run_on "$RUN2" v2 "$V2" "$IMG2"
sample_warm

# warm_bindings RUN prints task|version id|image|warm pod for each attempt of RUN,
# from the database: the warm binding is not served on the API.
warm_bindings() {
  psql "$DB_URL" -At -F '|' -v ON_ERROR_STOP=1 -v dag="$DAG_ID" -v run="$1" <<'SQL'
SELECT ti.task_id, r.dag_version_id::text, v.image_reference, COALESCE(ti.warm_worker_id, '')
FROM task_instances ti
JOIN dag_runs r ON r.id = ti.dag_run_id
JOIN dags d ON d.id = r.dag_id
JOIN dag_versions v ON v.id = r.dag_version_id
WHERE d.dag_id = :'dag' AND r.run_id = :'run'
ORDER BY ti.task_id, ti.try_number
SQL
}

# assert_warm_run RUN IMAGE [MIN] checks that a warm worker served at least MIN
# (default 1) tasks of RUN, and that every warm-served task ran on a warm pod of
# the run's own version and image, as sampled while the pod was live.
assert_warm_run() {
  local rows served=0 task ver img pod seen min="${3:-1}"
  rows="$(warm_bindings "$1")" || fail "reading the warm bindings of run $1 failed"
  [ -n "$rows" ] || fail "no task instance of run $1 in $DB_URL; is it the server's database?"
  while IFS='|' read -r task ver img pod; do
    [ "$img" = "$2" ] || fail "run $1 is pinned to image $img in the database, want $2"
    if [ -z "$pod" ]; then
      log "run $1: $task ran on a dedicated pod"
      continue
    fi
    served=$((served + 1))
    seen="$(awk -F '\t' -v p="$pod" '$1 == p' "$WARM_SEEN" | sort -u)"
    [ -n "$seen" ] || fail "run $1: $task is bound to warm pod $pod, which was never seen on the cluster"
    [ "$(printf '%s\n' "$seen" | wc -l)" -eq 1 ] || fail "warm pod $pod was seen with more than one version or image: $seen"
    [ "$(printf '%s' "$seen" | cut -f2)" = "$ver" ] \
      || fail "run $1: $task ran on warm pod $pod of version $(printf '%s' "$seen" | cut -f2), not the run's version $ver"
    [ "$(printf '%s' "$seen" | cut -f3)" = "$img" ] \
      || fail "run $1: $task ran on warm pod $pod on image $(printf '%s' "$seen" | cut -f3), not the run's image $img"
    log "run $1: $task ran on warm pod $pod, of its own version"
  done <<<"$rows"
  [ "$served" -ge "$min" ] \
    || fail "run $1: $served tasks were served by a warm worker, want at least $min, so this leg did not exercise warm pools (raise DEXAFLOW_E2E_HOLD_SECONDS)"
}


if [ "$WARM_POOLS" = on ]; then
  log "Warm pools on: every warm-served task ran on a warm worker of its own version"
  # Run 1's version is superseded mid-run, so its pool drains at once (ADR
  # 0082) and its second task may take a dedicated pod: no warm hit required.
  assert_warm_run "$RUN1" "$IMG1" 0
  assert_warm_run "$RUN2" "$IMG2"
else
  # Off is the shipped default: a dedicated pod per attempt, and no warm pod.
  [ ! -s "$WARM_SEEN" ] || fail "warm pools are off, yet warm worker pods ran: $(sort -u "$WARM_SEEN" | tr '\n' ' ')"
fi
log "Redeploy in flight: run 1 finished on v1, run 2 ran v2 (warm pools $WARM_POOLS)"

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

log "DEPLOY E2E passed (warm pools $WARM_POOLS): deploy, run, redeploy in flight and run again, and the two-step CI/CD path, all built, pushed, pinned and ran"
