#!/usr/bin/env bash
# A Helm rolling upgrade from the previous release with tasks in flight (#2305).
#
#   1. Install the previous release's published chart and images with the HA
#      shape: two replicas, a PodDisruptionBudget, RollingUpdate. Task logs go
#      to an emptyDir (kind has no ReadWriteMany class); this test does not
#      read them.
#   2. Register three DAGs built with the previous release's runtime and CLI,
#      as an operator's DAG images are: a long task, a fan-out of eight tasks
#      and a cron DAG that runs every minute. Trigger the first two.
#   3. `helm upgrade` to the chart and images of this tree while the tasks run.
#   4. Assert:
#      - the Service never had zero ready endpoints during the upgrade;
#      - every task of the triggered runs ends success (no user rail failure);
#      - no cron run was created twice for one logical date;
#      - every pod runs the candidate image and the schema is ready.
#
# Requirements: kind (a cluster named $CLUSTER already exists, as
# helm/kind-action creates it, or the script creates one), kubectl, helm,
# docker, go, git, jq, curl and python3. The previous release defaults to the
# newest GA tag (override with DEXAFLOW_E2E_PREVIOUS=v0.5.9); its chart and
# images are pulled from ghcr.io. DESTRUCTIVE to that cluster.
# Run from the repo root:  bash test/e2e/helm-rolling-upgrade-e2e.sh
set -euo pipefail

# shellcheck source=test/e2e/lib.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
CLUSTER="${DEXAFLOW_E2E_KIND_CLUSTER:-dexaflow-upgrade}"
NS="dexaflow"
RELEASE="dexaflow"
PY_VERSION="3.11"
WORKDIR="$(mktemp -d)"
PF_PID=""
WATCH_PID=""
CREATED_CLUSTER=0
CHART_OCI="oci://ghcr.io/dexadata/charts/dexaflow"

log() { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
pass() { printf '  \033[1;32mPASS\033[0m %s\n' "$*"; }
dump() {
  printf '\033[1;33m--- pods ---\033[0m\n' >&2
  kubectl -n "$NS" get pods -o wide >&2 2>&1 || true
  printf '\033[1;33m--- control plane logs ---\033[0m\n' >&2
  for p in $(kubectl -n "$NS" get pods -l "app.kubernetes.io/instance=$RELEASE,app.kubernetes.io/component!=migrate" -o name 2>/dev/null); do
    kubectl -n "$NS" logs "$p" --tail=30 >&2 2>&1 || true
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
  [ -n "$WATCH_PID" ] && kill "$WATCH_PID" 2>/dev/null
  git -C "$ROOT" worktree remove --force "$WORKDIR/prev" >/dev/null 2>&1 || true
  [ "$CREATED_CLUSTER" = 1 ] && kind delete cluster --name "$CLUSTER" >/dev/null 2>&1
  rm -rf "$WORKDIR"
}
trap cleanup EXIT

for tool in kind kubectl helm docker go git jq curl python3; do
  command -v "$tool" >/dev/null || fail "missing required tool: $tool"
done

PREV="${DEXAFLOW_E2E_PREVIOUS:-$(git -C "$ROOT" tag -l 'v[0-9]*.[0-9]*.[0-9]*' | grep -v -- '-' | sort -V | tail -1)}"
[ -n "$PREV" ] || fail "no GA tag found: fetch the tags, or set DEXAFLOW_E2E_PREVIOUS"
log "previous release: $PREV"

# forward points 127.0.0.1:18080 at the Service: the API is active-active, and
# a forward to one pod would end with that pod during the rollout.
forward() {
  stop_pf
  kubectl -n "$NS" port-forward "svc/$RELEASE" 18080:8080 >"$WORKDIR/pf.log" 2>&1 &
  PF_PID=$!
  for _ in $(seq 1 30); do
    curl -s -o /dev/null "http://127.0.0.1:18080/healthz" && return 0
    sleep 0.5
  done
  return 1
}
API="http://127.0.0.1:18080"

if ! kind get clusters 2>/dev/null | grep -qx "$CLUSTER"; then
  log "kind cluster $CLUSTER"
  kind create cluster --name "$CLUSTER" --wait 120s >/dev/null
  CREATED_CLUSTER=1
fi
kubectl config use-context "kind-$CLUSTER" >/dev/null

log "the previous release's runtime and CLI, from $PREV"
git -C "$ROOT" worktree add --detach "$WORKDIR/prev" "$PREV" >/dev/null 2>&1 || fail "checking out $PREV"
docker build -q --provenance=false -f "$WORKDIR/prev/runtime/Dockerfile" \
  --build-arg "PYTHON_VERSION=$PY_VERSION" -t "upgrade-base:$PREV" "$WORKDIR/prev" >/dev/null
(cd "$WORKDIR/prev" && go build -o "$WORKDIR/dexaflow-prev" ./cmd/dexaflow)

log "the candidate server and migrate images, from this tree"
docker build -q -t dexaflow-server:e2e -f "$ROOT/deploy/Dockerfile.server" "$ROOT" >/dev/null
docker build -q -t dexaflow-migrate:e2e -f "$ROOT/deploy/Dockerfile.migrate" "$ROOT" >/dev/null

# dag_image NAME DAG_FILE builds a DAG image on the previous runtime and
# compiles its dag.json with the previous CLI, as an operator built it before
# the upgrade.
dag_image() {
  local name="$1" dir="$WORKDIR/$1"
  mkdir -p "$dir"
  cp "$2" "$dir/dag.py"
  cat > "$dir/dexaflow.yaml" <<YAML
schema_version: "1.0"
dag_id: $name
YAML
  cat > "$dir/Dockerfile" <<DOCKER
FROM upgrade-base:$PREV
COPY dag.py /home/leoflow/dag.py
ENV PYTHONPATH=/home/leoflow
DOCKER
  docker build -q --provenance=false -t "upgrade-$name:e2e" "$dir" >/dev/null
  PYTHONPATH="$WORKDIR/prev/parser" "$WORKDIR/dexaflow-prev" compile "$dir" --image "upgrade-$name:e2e" --dag-version e2e-1 \
    -o "$dir/dag.json" >/dev/null || fail "compiling $name with the $PREV CLI"
}

cat > "$WORKDIR/long.py" <<'PY'
from airflow.sdk import DAG, task

@task
def long_running():
    import time
    for i in range(150):
        print(f"still running {i}", flush=True)
        time.sleep(1)
    return "done"

with DAG("upgrade_long", schedule=None, catchup=False):
    long_running()
PY
# Eight independent tasks: BashOperator, since the parser shim of the @task
# decorator has no .override (test/soak/dags/soak_chain/dag.py).
cat > "$WORKDIR/fanout.py" <<'PY'
from airflow.sdk import DAG
from airflow.providers.standard.operators.bash import BashOperator

with DAG("upgrade_fanout", schedule=None, catchup=False):
    for i in range(8):
        BashOperator(task_id=f"part_{i}", bash_command=f"sleep {20 + 3 * i}; echo part {i}")
PY
cat > "$WORKDIR/cron.py" <<'PY'
from airflow.sdk import DAG, task

@task
def tick():
    print("tick", flush=True)

with DAG("upgrade_cron", schedule="* * * * *", catchup=False):
    tick()
PY
dag_image upgrade_long "$WORKDIR/long.py"
dag_image upgrade_fanout "$WORKDIR/fanout.py"
dag_image upgrade_cron "$WORKDIR/cron.py"
kind load docker-image dexaflow-server:e2e dexaflow-migrate:e2e \
  upgrade-upgrade_long:e2e upgrade-upgrade_fanout:e2e upgrade-upgrade_cron:e2e --name "$CLUSTER" >/dev/null

log "Postgres and Redis in namespace $NS"
kubectl create namespace "$NS" >/dev/null
kubectl -n "$NS" apply -f "$ROOT/.github/ci/kind-datastores.yaml" >/dev/null
kubectl -n "$NS" rollout status deploy/postgres --timeout=150s >/dev/null
kubectl -n "$NS" rollout status deploy/redis --timeout=150s >/dev/null
kubectl -n "$NS" exec deploy/postgres -- pg_isready -U leoflow -d leoflow -t 60 >/dev/null

COMMON=(
  --set 'database.url=postgres://leoflow:leoflow@postgres:5432/leoflow?sslmode=disable'
  --set 'redis.url=redis://redis:6379/0'
  --set auth.jwtSecret=e2e-jwt-secret
  --set 'secretKey=dexaflow-e2e-secret-key-32bytes!'
  --set agentTLS.enabled=true
  --set bootstrap.password=e2e-admin
  --set replicaCount=2
  --set logs.persistence.enabled=false
  --set deployment.strategy=RollingUpdate
  --set deployment.preStopSleepSeconds=5
)

log "helm install of the $PREV chart with its published images"
helm install "$RELEASE" "$CHART_OCI" --version "${PREV#v}" -n "$NS" "${COMMON[@]}" \
  --wait --timeout 600s >"$WORKDIR/install.log" 2>&1 || { cat "$WORKDIR/install.log" >&2; fail "helm install of $PREV"; }
kubectl -n "$NS" get pdb "$RELEASE" >/dev/null 2>&1 || fail "the $PREV install with two replicas rendered no PodDisruptionBudget"
pass "$PREV installed with two replicas and a PodDisruptionBudget"

forward || fail "no port-forward to the API"
TOKEN="$(admin_token "$WORKDIR/dexaflow-prev" "$API" e2e-admin)" || fail "the bootstrap admin cannot log in"

register() {
  local code
  code="$(curl -sS -o "$WORKDIR/register.body" -w '%{http_code}' -X POST \
    -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
    --data-binary @"$WORKDIR/$1/dag.json" "$API/api/v2/dags/$1/versions")"
  [ "$code" = 201 ] || fail "registering $1 answered $code: $(cat "$WORKDIR/register.body")"
  unpause_dag "$API" "$1" "$TOKEN" || fail "unpausing $1"
}
trigger() {
  curl -fsS -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
    -d '{}' "$API/api/v2/dags/$1/dagRuns" | jq -r '.dag_run_id'
}
states() {
  curl -fsS -H "Authorization: Bearer $TOKEN" "$API/api/v2/dags/$1/dagRuns/$2/taskInstances?limit=100" \
    | jq -r '.task_instances[].state'
}

log "register the DAGs and start the work that crosses the upgrade"
register upgrade_long
register upgrade_fanout
register upgrade_cron
LONG_RUN="$(trigger upgrade_long)"
FANOUT_RUN="$(trigger upgrade_fanout)"
[ -n "$LONG_RUN" ] && [ "$LONG_RUN" != null ] && [ -n "$FANOUT_RUN" ] && [ "$FANOUT_RUN" != null ] \
  || fail "a trigger returned no dag_run_id"

deadline=$(( $(date +%s) + 180 ))
until states upgrade_long "$LONG_RUN" | grep -qx running; do
  [ "$(date +%s)" -gt "$deadline" ] && fail "the long task never reached running on $PREV"
  sleep 3
done
pass "the long task runs on $PREV (run $LONG_RUN), the fan-out started (run $FANOUT_RUN)"

# The endpoint watch samples the Service's ready endpoints every second for the
# whole upgrade and writes one count per line.
endpoint_watch() {
  while :; do
    kubectl -n "$NS" get endpointslices -l "kubernetes.io/service-name=$RELEASE" -o json 2>/dev/null \
      | jq '[.items[].endpoints[]? | select(.conditions.ready == true)] | length' 2>/dev/null || echo error
    sleep 1
  done
}
endpoint_watch >"$WORKDIR/endpoints.log" &
WATCH_PID=$!

log "helm upgrade to this tree's chart and images while the tasks run"
stop_pf
# --reset-then-reuse-values, as the upgrade guide says: plain --reuse-values
# drops the defaults of keys the previous chart did not have, and a template
# that validates such a key then fails on "<nil>".
helm upgrade "$RELEASE" "$ROOT/helm/dexaflow" -n "$NS" --reset-then-reuse-values \
  --set image.repository=dexaflow-server --set image.tag=e2e --set image.pullPolicy=IfNotPresent \
  --set migrations.image.repository=dexaflow-migrate --set migrations.image.tag=e2e \
  --set migrations.image.pullPolicy=IfNotPresent \
  --wait --timeout 600s >"$WORKDIR/upgrade.log" 2>&1 || { cat "$WORKDIR/upgrade.log" >&2; fail "helm upgrade to the candidate"; }
kubectl -n "$NS" rollout status deploy/"$RELEASE" --timeout=300s >/dev/null || fail "the rollout did not finish"
sleep 10
kill "$WATCH_PID" 2>/dev/null; wait "$WATCH_PID" 2>/dev/null || true; WATCH_PID=""

samples="$(grep -c . "$WORKDIR/endpoints.log" || true)"
[ "${samples:-0}" -ge 10 ] || fail "the endpoint watch took only ${samples:-0} samples"
if grep -qx 0 "$WORKDIR/endpoints.log"; then
  fail "the Service had zero ready endpoints during the upgrade ($(grep -cx 0 "$WORKDIR/endpoints.log") of $samples samples)"
fi
pass "the Service kept a ready endpoint in all $samples samples of the upgrade (min $(grep -x '[0-9]*' "$WORKDIR/endpoints.log" | sort -n | head -1))"

forward || fail "no port-forward to the API after the upgrade"
TOKEN="$(admin_token "$WORKDIR/dexaflow-prev" "$API" e2e-admin)" || fail "the bootstrap admin cannot log in after the upgrade"

log "every task of the runs that crossed the upgrade ends success"
for pair in "upgrade_long $LONG_RUN" "upgrade_fanout $FANOUT_RUN"; do
  read -r dag run <<<"$pair"
  deadline=$(( $(date +%s) + 420 ))
  while :; do
    s="$(states "$dag" "$run")"
    printf '%s' "$s" | grep -qE 'failed|upstream_failed' && fail "$dag run $run has a failed task: $(echo "$s" | tr '\n' ' ')"
    if [ -n "$s" ] && ! printf '%s\n' "$s" | grep -qvx success; then break; fi
    [ "$(date +%s)" -gt "$deadline" ] && fail "$dag run $run did not finish: $(echo "$s" | tr '\n' ' ')"
    sleep 5
  done
  pass "$dag run $run: $(printf '%s\n' "$s" | wc -l | tr -d ' ') tasks, all success"
done

log "no cron run was created twice"
runs="$(curl -fsS -H "Authorization: Bearer $TOKEN" "$API/api/v2/dags/upgrade_cron/dagRuns?limit=100")"
total="$(jq '.dag_runs | length' <<<"$runs")"
distinct="$(jq '[.dag_runs[].logical_date] | unique | length' <<<"$runs")"
[ "$total" -ge 2 ] || fail "the cron DAG created only $total runs across the upgrade"
[ "$total" = "$distinct" ] || fail "the cron DAG has $total runs for $distinct logical dates: a run was created twice"
pass "the cron DAG created $total runs, one per logical date"

log "the release runs the candidate and the schema is ready"
images="$(kubectl -n "$NS" get pods -l "app.kubernetes.io/instance=$RELEASE,app.kubernetes.io/component!=migrate" \
  --field-selector=status.phase=Running -o jsonpath='{range .items[*]}{.spec.containers[0].image}{"\n"}{end}' | sort -u)"
[ "$images" = "dexaflow-server:e2e" ] || fail "pods run $(echo "$images" | tr '\n' ' '), want only dexaflow-server:e2e"
code="$(curl -s -o /dev/null -w '%{http_code}' "$API/readyz")"
[ "$code" = 200 ] || fail "/readyz answers $code after the upgrade"
pass "every control plane pod runs dexaflow-server:e2e and /readyz answers 200"

echo
echo "HELM ROLLING UPGRADE E2E ($PREV to this tree): all assertions held"
