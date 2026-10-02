#!/usr/bin/env bash
#
# End-to-end smoke test for Dexaflow on a local Kubernetes cluster (k3d).
#
# Exercises the full pod-path: build the base + DAG images, import them into
# k3d, run the control plane on the host against the dev Postgres/Redis
# (`make dev-up`), push and trigger a DAG, and assert every task instance reaches
# 'success' — i.e. each task ran in a real pod whose agent reported state over
# gRPC.
#
# Requirements: k3d, kubectl, docker, jq, curl, the leoflow binaries
# (`make build`), and a running dev database (`make dev-up`). Developer/CI tool,
# not part of `go test`; production e2e runs against a real cluster via Helm.
#
# Usage: test/e2e/e2e.sh [cluster-name]
set -euo pipefail

# shellcheck source=test/e2e/lib.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

CLUSTER="${1:-leoflow-e2e}"
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
WORKDIR="$(mktemp -d)"
PY_VERSION="3.11"
BASE_IMAGE="leoflow-base:py${PY_VERSION}"
DAG_IMAGE="leoflow-e2e-dag:dev"
DAG_ID="e2edag"
# HTTP port is overridable (LEOFLOW_E2E_HTTP_PORT) so a developer whose 8080 is
# already taken — a forwarded VM, another server — can run without a conflict. CI
# uses the default.
HTTP_PORT="${LEOFLOW_E2E_HTTP_PORT:-8080}"
API="http://localhost:${HTTP_PORT}"
GRPC_PORT="9091"
# Address task pods dial to reach the host control plane's gRPC. On Docker
# Desktop (macOS/Windows) host.docker.internal resolves to the host; k3d does
# not inject host.k3d.internal into CoreDNS there. Override for Linux/CI.
HOST_ADDR="${LEOFLOW_E2E_HOST_ADDR:-$([ "$(uname -s)" = Linux ] && echo host.k3d.internal || echo host.docker.internal)}"
SERVER_PID=""

log()  { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
# dump_pods prints the task pods and their logs — invaluable for diagnosing a
# failed task before the cleanup trap deletes the cluster.
dump_pods() {
  printf '\033[1;33m--- task pods (namespace leoflow) ---\033[0m\n' >&2
  kubectl get pods -n leoflow -o wide >&2 2>&1 || true
  for p in $(kubectl get pods -n leoflow -o name 2>/dev/null); do
    printf '\033[1;33m--- logs %s ---\033[0m\n' "$p" >&2
    kubectl logs -n leoflow "$p" --all-containers --tail=80 >&2 2>&1 || true
  done
}
fail() { printf '\033[1;31mFAIL:\033[0m %s\n' "$*" >&2; dump_pods; exit 1; }

cleanup() {
  [ -n "$SERVER_PID" ] && kill "$SERVER_PID" 2>/dev/null || true
  k3d cluster delete "$CLUSTER" >/dev/null 2>&1 || true
  rm -rf "$WORKDIR"
}
trap cleanup EXIT

for tool in k3d kubectl docker jq curl; do
  command -v "$tool" >/dev/null || fail "missing required tool: $tool"
done

log "Scaffolding a minimal DAG project ($DAG_ID)"
"$ROOT/bin/leoflow" init "$WORKDIR/$DAG_ID"
# Declare the sensor's provider so the ADR 0040 A5 compile check passes; the
# Dockerfile below installs it into the image.
sed -i.bak 's/^dependencies: \[\]/dependencies: [apache-airflow-providers-http]/' \
  "$WORKDIR/$DAG_ID/dexaflow.yaml" && rm -f "$WORKDIR/$DAG_ID/dexaflow.yaml.bak"
# The config loader defaults build.platforms to linux/amd64 (the prod default);
# on an arm64 dev/Lima host the DAG image must build for the host arch, else its
# base-image FROM mismatches (InvalidBaseImagePlatform) and the pods ErrImagePull.
case "$(uname -m)" in arm64|aarch64) HOST_PLATFORM="linux/arm64" ;; *) HOST_PLATFORM="linux/amd64" ;; esac
cat >> "$WORKDIR/$DAG_ID/dexaflow.yaml" <<YAML
build:
  platforms:
    - ${HOST_PLATFORM}
YAML
# A real Airflow-SDK DAG (the parser requires an actual DAG object, not a bare
# function). Two tasks in sequence prove pod-per-task AND cross-pod ordering:
# each runs in its own pod whose agent reports state over gRPC.
cat > "$WORKDIR/$DAG_ID/dag.py" <<'PY'
"""e2edag — Dexaflow pod-per-task smoke DAG."""
from __future__ import annotations

from airflow.sdk import DAG, task
# A provider SENSOR (poke mode) — captured generically as type=airflow_operator
# (ADR 0040). HttpSensor pokes an endpoint until it returns 200; we point it at
# the control plane's own public /readyz, so it succeeds on the first poke and
# exercises the FULL operator chain end-to-end: scheduler -> pod -> agent
# `--operator` -> runtime run_operator -> execute(), plus connection delivery.
from airflow.providers.http.sensors.http import HttpSensor
from airflow.providers.standard.operators.bash import BashOperator
from airflow.providers.standard.sensors.date_time import DateTimeSensor


@task
def extract(**context) -> str:
    print("hello from leoflow e2e: extract")
    # Multi-key XCom (ADR 0040): a custom-keyed push must ship as its OWN XCom via
    # LEOFLOW_PUSHES_PATH — the channel deliberately renamed off the LEOFLOW_XCOM_
    # prefix so _merge_operator_xcom does not swallow it as a kwarg. Asserted below.
    context["ti"].xcom_push(key="row_count", value=42)
    return "payload-42"


@task
def transform(value: str) -> str:
    # Consuming extract's output proves TaskFlow value passing end-to-end (#51).
    print(f"transform received: {value}")
    return value.upper()


@task
def read_var(_value: str, ds=None) -> None:
    # The agent delivers Admin Variables as AIRFLOW_VAR_* env (#54 / ADR 0021).
    import os
    print("e2e variable:", os.environ.get("AIRFLOW_VAR_E2E_GREETING"))
    # A @task gets the run context too (ADR 0040 native parity): `ds` is injected by
    # name (a string when injected, None if injection regressed). Assert + log it.
    assert ds is not None, "@task did not receive 'ds' from the run context"
    print("e2e context ds:", ds)


@task
def endpoint_name() -> str:
    # A value-returning upstream the operator consumes via ti.xcom_pull below.
    return "readyz"


@task
def recover_cloud_creds() -> None:
    # The "a user pastes a credential in the UI" path, end to end: the main CLOUD
    # connections created via the API (below) must reach the task INTACT through
    # encrypted-at-rest storage (ADR 0019) + agent delivery (AIRFLOW_CONN_*). Pure
    # stdlib — this asserts the delivery contract, no provider needed.
    import json
    import os
    from urllib.parse import parse_qs, urlsplit

    def extra_of(env: str) -> dict:
        q = parse_qs(urlsplit(os.environ.get(env, "")).query)
        return json.loads(q["__extra__"][0]) if "__extra__" in q else {}

    # GCP: a pasted service-account key lives in extra.keyfile_dict (a JSON blob).
    gcp = json.loads(extra_of("AIRFLOW_CONN_GCP_UI")["keyfile_dict"])
    print("e2e gcp SA:", gcp["client_email"])
    # AWS: access key id / secret ride as login:password in the URI userinfo.
    aws = urlsplit(os.environ.get("AIRFLOW_CONN_AWS_UI", ""))
    print("e2e aws:", aws.username, extra_of("AIRFLOW_CONN_AWS_UI").get("region_name"))
    # Azure: a client secret rides as the password.
    az = urlsplit(os.environ.get("AIRFLOW_CONN_AZURE_UI", ""))
    print("e2e azure:", az.username, az.password)


with DAG("e2edag", schedule="@daily", catchup=False, tags=["e2e"]):
    tail = read_var(transform(extract()))
    target = endpoint_name()
    # A real provider sensor (poke mode) downstream of the @task chain. Proves a
    # type=airflow_operator task runs in its own pod and resolves a managed
    # Connection (AIRFLOW_CONN_CP) — the generic-executor path. Its endpoint is
    # CHAINED from an upstream operator's return_value via ti.xcom_pull (ADR 0040),
    # so this exercises the real chaining wire: the agent must authorize fetching a
    # depends_on upstream AND deliver a NON-EMPTY upstream-xcom map (the case a
    # None-returning upstream would not, which let a kwarg-collision regression slip
    # past CI once). If chaining breaks, the endpoint renders empty and the poke fails.
    probe = HttpSensor(task_id="probe", http_conn_id="cp",
                       endpoint="{{ ti.xcom_pull('endpoint_name') }}",
                       poke_interval=2, timeout=60)
    # A native bash task carrying a Jinja macro: the runtime must render {{ ds }} in
    # the pod (#382) before exec'ing bash — a plain command would stay a direct
    # `bash -c`. The rendered ds must match read_var's; asserted from logs below.
    greet = BashOperator(task_id="greet", bash_command='echo "e2e bash ds={{ ds }}"')
    recover_cloud_creds()
    # A reschedule-mode sensor (#380): it pokes not-ready until the `wait_until` time
    # (an Admin Variable the e2e sets ~12s in the future before triggering), RELEASING
    # its pod on each poke (up_for_reschedule) and being re-dispatched, then succeeds —
    # the reschedule pod-path end to end. target_time is a string (JSON-serialisable,
    # unlike TimeDeltaSensor's timedelta) rendered from var.value. Independent leaf.
    DateTimeSensor(task_id="waiter", target_time="{{ var.value.wait_until }}",
                   mode="reschedule", poke_interval=3)
    tail >> probe
    target >> probe
    tail >> greet
PY
cat > "$WORKDIR/$DAG_ID/Dockerfile" <<DOCKER
FROM ${BASE_IMAGE}
# The DAG uses a provider sensor; install its provider into the image (this is
# what connectors:/dependencies: do in a real project).
RUN pip install --no-cache-dir apache-airflow-providers-http
COPY dag.py /home/leoflow/dag.py
ENV PYTHONPATH=/home/leoflow
DOCKER

log "Building base and DAG images"
docker build --provenance=false -f "$ROOT/runtime/Dockerfile" --build-arg "PYTHON_VERSION=${PY_VERSION}" -t "$BASE_IMAGE" "$ROOT"

# ─────────────────────────────────────────────────────────────────────────────
# The base image's own properties. Every one of these was, until now, asserted
# by a comment in runtime/Dockerfile and by nothing else — so each moved once
# already without a red run: the Debian suite when the FROM was edited, and the
# home-dir mode when Debian's `passwd` changed its HOME_MODE default under an
# unchanged `useradd` line. Three `docker` invocations against an image this
# script has just built anyway.
#
# The expected suite is read out of runtime/Dockerfile rather than written here.
# A hardcoded "trixie" would be a fourth copy of the same fact, green on the day
# it was typed and wrong on the day of the next move — which is the failure this
# whole block exists to stop.
log "Asserting the task base image's own properties (suite, home-dir mode, final USER)"
WANT_SUITE="$(sed -n 's/^FROM python:\${PYTHON_VERSION}-slim-\([A-Za-z0-9.]*\).*/\1/p' "$ROOT/runtime/Dockerfile")"
[ -n "$WANT_SUITE" ] \
  || fail "runtime/Dockerfile's stage-2 FROM pins no Debian suite; this assertion has nothing to compare against"
GOT_SUITE="$(docker run --rm --entrypoint sh "$BASE_IMAGE" -c '. /etc/os-release; printf %s "$VERSION_CODENAME"')"
[ "$GOT_SUITE" = "$WANT_SUITE" ] \
  || fail "base image is Debian '$GOT_SUITE' but runtime/Dockerfile's stage-2 FROM pins '$WANT_SUITE'"
log "base suite: $GOT_SUITE (matches runtime/Dockerfile)"

# 65532:65532 and 0700. The mode is pinned by an explicit chmod because the
# distro default moved under us; the ownership is what makes 0700 survivable,
# since a task pod runs as exactly this UID (buildSecurityContext sets
# runAsNonRoot with no runAsUser, so the kubelet resolves the image's user) and
# an owner traverses its own home whatever the mode. Assert the pair together:
# 0700 with the wrong owner is an unreadable home, and that is the regression.
GOT_HOME="$(docker run --rm --entrypoint stat "$BASE_IMAGE" -c '%u:%g %a' /home/leoflow)"
[ "$GOT_HOME" = "65532:65532 700" ] \
  || fail "/home/leoflow is '$GOT_HOME', want '65532:65532 700' (owner+mode pair, see runtime/Dockerfile)"
log "home dir: $GOT_HOME"

# A NUMERIC non-root user, not the name `leoflow`: PodSecurity `restricted`
# verifies runAsNonRoot against the image's user and the kubelet can only
# resolve a numeric UID, so a name here is a rejected container.
GOT_USER="$(docker image inspect -f '{{.Config.User}}' "$BASE_IMAGE")"
[ "$GOT_USER" = "65532:65532" ] \
  || fail "base image final USER is '$GOT_USER', want numeric '65532:65532'"
log "final USER: $GOT_USER"

log "Creating k3d cluster '$CLUSTER'"
k3d cluster create "$CLUSTER" --wait

log "Creating the 'leoflow' namespace (where task pods are created)"
kubectl create namespace leoflow

log "Starting the control plane (agents dial ${HOST_ADDR}:${GRPC_PORT})"
export LEOFLOW_AUTH_JWT_SECRET="e2e-secret"
export LEOFLOW_BOOTSTRAP_PASSWORD="admin"
export LEOFLOW_EXECUTOR_AGENT_CONTROL_PLANE_ADDR="${HOST_ADDR}:${GRPC_PORT}"
# The default logs.dir (/var/log/leoflow) is not writable by a normal user, which
# made the server's log sink fail to open and surfaced as a bare stream EOF on the
# agent (#36). Use a writable temp dir so pod logs actually land on disk.
export LEOFLOW_LOGS_DIR="${WORKDIR}/logs"
# Allow secret delivery over the e2e's plaintext gRPC (dev only; prod uses TLS,
# issue #58) so the agent can fetch Admin Variables/Connections (#54).
export LEOFLOW_AGENT_ALLOW_INSECURE_SECRETS=true
# Connections are encrypted at rest (ADR 0019); the server refuses to store one
# without a key. Set a 32-byte key so the operator's Connection can be created.
export LEOFLOW_SECRET_KEY="e2e-secret-key-32-bytes-padding!"
export LEOFLOW_SERVER_HTTP_ADDR="0.0.0.0:${HTTP_PORT}"
"$ROOT/bin/leoflow-server" &
SERVER_PID=$!
sleep 5

log "Compiling, building, and importing the DAG image"
"$ROOT/bin/leoflow" compile "$WORKDIR/$DAG_ID" --image "$DAG_IMAGE" \
  --build --dockerfile Dockerfile -o "$WORKDIR/$DAG_ID/dag.json"
log "Asserting the sensor compiled to type=airflow_operator (ADR 0040 generic path)"
jq -e '.tasks[] | select(.task_id=="probe" and .type=="airflow_operator")' \
  "$WORKDIR/$DAG_ID/dag.json" >/dev/null || fail "probe did not compile to airflow_operator"
log "Asserting probe chains from endpoint_name (ti.xcom_pull, ADR 0040)"
jq -e '.tasks[] | select(.task_id=="probe") | .depends_on | index("endpoint_name")' \
  "$WORKDIR/$DAG_ID/dag.json" >/dev/null || fail "probe does not depend on endpoint_name (chaining wire missing)"
k3d_import "$CLUSTER" "$BASE_IMAGE" "$DAG_IMAGE"

log "Pushing the DAG"
TOKEN="$("$ROOT/bin/leoflow" auth create-token --server "$API" --username admin@leoflow.local --password admin)"
"$ROOT/bin/leoflow" push "$WORKDIR/$DAG_ID/dag.json" --server "$API" --token "$TOKEN"

log "Creating an Admin Variable for the runtime-delivery check (#54)"
curl -fsS -X POST -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{"key":"e2e_greeting","value":"hi-from-admin"}' "$API/api/v2/variables" >/dev/null

log "Creating the 'cp' http Connection the operator sensor pokes (ADR 0040)"
# Point it at the host control plane (the pod reaches the host the same way the
# agent dials gRPC). HttpSensor pokes http://${HOST_ADDR}:${HTTP_PORT}/readyz.
curl -fsS -X POST -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d "{\"connection_id\":\"cp\",\"conn_type\":\"http\",\"host\":\"${HOST_ADDR}\",\"port\":${HTTP_PORT},\"schema\":\"http\"}" \
  "$API/api/v2/connections" >/dev/null

log "Creating the main CLOUD connections via the API (the UI 'paste a credential' path): aws, gcp, azure"
# Fake credentials — the point is integrity of delivery, not real auth. Nested JSON
# (extra carries a JSON string; the GCP key is a JSON-in-JSON blob) is built in
# Python to dodge shell-quoting hazards.
python3 - "$WORKDIR" <<'PY'
import json
import pathlib
import sys

d = pathlib.Path(sys.argv[1])
keyfile = json.dumps({
    "type": "service_account",
    "client_email": "leoflow-e2e@example.iam.gserviceaccount.com",
    "private_key": "-----BEGIN PRIVATE KEY-----\nFAKEKEYNOTREAL\n-----END PRIVATE KEY-----\n",
})
bodies = {
    "gcp_ui": {"connection_id": "gcp_ui", "conn_type": "google_cloud_platform",
               "extra": json.dumps({"keyfile_dict": keyfile})},
    "aws_ui": {"connection_id": "aws_ui", "conn_type": "aws",
               "login": "AKIAEXAMPLEFAKE123", "password": "FAKEsecretkey00000000000000000000000000",
               "extra": json.dumps({"region_name": "sa-east-1"})},
    "azure_ui": {"connection_id": "azure_ui", "conn_type": "azure",
                 "login": "fake-client-id-0000", "password": "FAKEazuresecret9999",
                 "extra": json.dumps({"tenantId": "fake-tenant-0000"})},
}
for name, body in bodies.items():
    (d / f"conn_{name}.json").write_text(json.dumps(body))
PY
for name in gcp_ui aws_ui azure_ui; do
  curl -fsS -X POST -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
    -d @"$WORKDIR/conn_${name}.json" "$API/api/v2/connections" >/dev/null \
    || fail "failed to create the $name cloud connection via the API"
done

log "Setting the 'wait_until' Variable far in the future so the reschedule sensor MUST poke not-ready (#380)"
# Deterministic, not wall-clock-racy. A far-future target guarantees the sensor enters
# up_for_reschedule (its pod is released + re-dispatched); once the poll loop observes
# that, it flips this Variable to a PAST time so the next re-dispatch pokes ready and
# succeeds. The old "~12s ahead" margin flaked: under pod-startup latency the first poke
# could land AFTER the target and succeed immediately, never rescheduling (seen on main
# after merge). The agent fetches Variables live per pod (agentrpc secrets), so a
# re-dispatched pod renders the flipped value.
WAIT_FUTURE="2999-01-01T00:00:00Z"
WAIT_PAST="2000-01-01T00:00:00Z"
set_wait_until() {
  curl -fsS -X POST -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
    -d "{\"key\":\"wait_until\",\"value\":\"$1\"}" "$API/api/v2/variables" >/dev/null
}
set_wait_until "$WAIT_FUTURE"
log "wait_until = ${WAIT_FUTURE} (far future — the sensor will reschedule)"

log "Triggering a run"
RUN_ID="$(curl -fsS -X POST -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{}' "$API/api/v2/dags/$DAG_ID/dagRuns" | jq -r '.dag_run_id')"
[ -n "$RUN_ID" ] && [ "$RUN_ID" != "null" ] || fail "no dag_run_id returned"
log "run = $RUN_ID"

log "Waiting for all task instances to succeed"
deadline=$(( $(date +%s) + 300 ))
saw_reschedule=0
flipped=0
while :; do
  states="$(curl -fsS -H "Authorization: Bearer $TOKEN" \
    "$API/api/v2/dags/$DAG_ID/dagRuns/$RUN_ID/taskInstances" | jq -r '.task_instances[].state')"
  log "task states: [$(echo "$states" | tr '\n' ' ')] pods: [$(kubectl get pods -n leoflow --no-headers 2>/dev/null | awk '{print $1"="$3}' | tr '\n' ' ')]"
  echo "$states" | grep -qE 'failed|upstream_failed' && fail "a task failed: $states"
  # The reschedule-mode 'waiter' must visibly pass through up_for_reschedule — proof its
  # pod was released and the scheduler re-dispatched it (#380). Once observed, flip the
  # target to the past so the next re-dispatch pokes ready and the run completes (the
  # re-dispatched pod fetches Variables fresh, so it renders the new value).
  if echo "$states" | grep -q 'up_for_reschedule'; then
    saw_reschedule=1
    if [ "$flipped" = "0" ]; then
      set_wait_until "$WAIT_PAST"
      flipped=1
      log "observed up_for_reschedule → flipped wait_until = ${WAIT_PAST} (sensor will now succeed)"
    fi
  fi
  if [ -n "$states" ] && ! echo "$states" | grep -qvE 'success|skipped'; then
    log "all tasks terminal-success: $states"
    break
  fi
  [ "$(date +%s)" -gt "$deadline" ] && fail "timed out; last states: ${states:-<none>}"
  sleep 3
done

log "Asserting reschedule-mode sensor passed through up_for_reschedule (#380)"
[ "$saw_reschedule" = "1" ] || fail "the 'waiter' reschedule sensor never entered up_for_reschedule — its pod was not released/re-dispatched (reschedule pod-path broke)"
log "reschedule OK: waiter released its pod (up_for_reschedule), was re-dispatched, and succeeded after the flip"

log "Asserting task logs were shipped from the pod (#36)"
read -r FIRST_TASK FIRST_TRY < <(curl -fsS -H "Authorization: Bearer $TOKEN" \
  "$API/api/v2/dags/$DAG_ID/dagRuns/$RUN_ID/taskInstances" \
  | jq -r '.task_instances[0] | "\(.task_id) \(.try_number)"')
LOG_PATH="$API/api/v2/dags/$DAG_ID/dagRuns/$RUN_ID/taskInstances/$FIRST_TASK/logs"
log_body=""
for try in "$FIRST_TRY" 0 1 2; do
  body="$(curl -fsS -H "Authorization: Bearer $TOKEN" "$LOG_PATH/$try" 2>/dev/null || true)"
  if [ -n "$body" ] && ! echo "$body" | grep -q '"status":404'; then
    log_body="$body"
    log "logs for $FIRST_TASK (try=$try): $(echo "$body" | head -1 | cut -c1-100)"
    break
  fi
done
[ -n "$log_body" ] || fail "no logs shipped from pod for task '$FIRST_TASK' — agent log streaming broke (#36)"

# And the structured JSON view (the UI drill-down) must parse with content.
struct="$(curl -fsS -H "Authorization: Bearer $TOKEN" -H "Accept: application/json" "$LOG_PATH/$FIRST_TRY" 2>/dev/null \
  || curl -fsS -H "Authorization: Bearer $TOKEN" -H "Accept: application/json" "$LOG_PATH/0" 2>/dev/null || true)"
if echo "$struct" | jq -e '.content | length > 0' >/dev/null 2>&1; then
  log "structured logs OK: $(echo "$struct" | jq -r '.content | length') content items (#43)"
else
  fail "structured JSON logs missing content (#43)"
fi

log "Asserting TaskFlow value passing (#51): transform received extract's output"
tlog=""
for try in 0 1 2; do
  body="$(curl -fsS -H "Authorization: Bearer $TOKEN" \
    "$API/api/v2/dags/$DAG_ID/dagRuns/$RUN_ID/taskInstances/transform/logs/$try" 2>/dev/null || true)"
  if echo "$body" | grep -q "transform received: payload-42"; then tlog="$body"; break; fi
done
[ -n "$tlog" ] || fail "transform did not receive extract's output — TaskFlow value passing broken (#51)"
log "value passing OK: transform received payload-42"

log "Asserting Admin Variable delivery to the task (#54): read_var sees AIRFLOW_VAR_*"
vlog=""
for try in 0 1 2; do
  body="$(curl -fsS -H "Authorization: Bearer $TOKEN" \
    "$API/api/v2/dags/$DAG_ID/dagRuns/$RUN_ID/taskInstances/read_var/logs/$try" 2>/dev/null || true)"
  if echo "$body" | grep -q "e2e variable: hi-from-admin"; then vlog="$body"; break; fi
done
[ -n "$vlog" ] || fail "read_var did not see the Admin Variable — var/conn runtime delivery broken (#54)"
log "variable delivery OK: read_var saw hi-from-admin"

log "Asserting @task run-context injection (ADR 0040): read_var received 'ds'"
echo "$vlog" | grep -q "e2e context ds:" || fail "read_var did not receive ds from the run context — @task context injection broken"
log "@task context OK: read_var received ds"

log "Asserting CLOUD connection delivery (UI paste -> encrypted storage -> pod task): recover_cloud_creds recovered each credential intact"
clog=""
for try in 0 1 2; do
  body="$(curl -fsS -H "Authorization: Bearer $TOKEN" \
    "$API/api/v2/dags/$DAG_ID/dagRuns/$RUN_ID/taskInstances/recover_cloud_creds/logs/$try" 2>/dev/null || true)"
  if echo "$body" | grep -q "e2e gcp SA: leoflow-e2e@example.iam.gserviceaccount.com"; then clog="$body"; break; fi
done
[ -n "$clog" ] || fail "recover_cloud_creds did not recover the pasted GCP keyfile_dict — cloud connection key-in-extra delivery broke"
echo "$clog" | grep -q "e2e aws: AKIAEXAMPLEFAKE123 sa-east-1" || fail "AWS access key id / region not recovered intact in the task"
echo "$clog" | grep -q "e2e azure: fake-client-id-0000 FAKEazuresecret9999" || fail "Azure client id / secret not recovered intact in the task"
log "cloud connection delivery OK: gcp keyfile_dict + aws keys + azure secret all recovered intact in the pod"

log "Asserting multi-key XCom (ADR 0040): extract's custom 'row_count' key shipped as its own XCom"
rc_xcom="$(curl -fsS -H "Authorization: Bearer $TOKEN" \
  "$API/api/v2/xcoms/$DAG_ID/$RUN_ID/extract/row_count" 2>/dev/null || true)"
echo "$rc_xcom" | jq -e '.value == 42' >/dev/null 2>&1 \
  || fail "extract's custom XCom key 'row_count' was not shipped (multi-key XCom via LEOFLOW_PUSHES_PATH broke): ${rc_xcom:-<none>}"
log "multi-key XCom OK: row_count=42 shipped as its own XCom"

log "Asserting native bash Jinja templating (#382): greet rendered {{ ds }} in the pod"
glog=""
for try in 0 1 2; do
  body="$(curl -fsS -H "Authorization: Bearer $TOKEN" \
    "$API/api/v2/dags/$DAG_ID/dagRuns/$RUN_ID/taskInstances/greet/logs/$try" 2>/dev/null || true)"
  if echo "$body" | grep -qE "e2e bash ds=[0-9]{4}-[0-9]{2}-[0-9]{2}"; then glog="$body"; break; fi
done
[ -n "$glog" ] || fail "greet did not render {{ ds }} to a date — native bash Jinja templating broke (#382)"
log "bash Jinja OK: greet rendered a real ds"

# ─────────────────────────────────────────────────────────────────────────────
# on_failure_callback on the REAL pod path (#424). The Lite e2e (lite-callback.sh)
# covers the runtime logic; this proves the pod wiring: the k8s executor's pod →
# the agent (PID 1) stamps LEOFLOW_ON_FAILURE_CALLBACK/MAX_TRIES from the gRPC
# TaskSpec → the runtime resolves the callback off the loaded task object (works
# for an unbound `with DAG():`) and runs it, once, on the terminal failure. A
# separate DAG whose only task raises — kept out of the all-success run above.
# Proof needs no receiver: the callback prints a marker to the task log.
log "Callback pod-path (#424): a failing @task must run its on_failure_callback IN the pod"
CBID="cbdag"
mkdir -p "$WORKDIR/$CBID"
cat > "$WORKDIR/$CBID/dexaflow.yaml" <<YAML
schema_version: "1.0"
dag_id: cbdag
build:
  platforms:
    - ${HOST_PLATFORM}
YAML
cat > "$WORKDIR/$CBID/dag.py" <<'PY'
from airflow.sdk import DAG, task


def on_fail(context):
    # Runs in the pod on the terminal failure. Print a marker to the task log so
    # the e2e can assert it ran (no receiver / pod networking needed).
    print("E2E_CALLBACK_FIRED task=" + context["ti"].task_id, flush=True)


with DAG("cbdag", schedule=None):

    @task(on_failure_callback=on_fail)
    def boom():
        raise RuntimeError("intentional e2e failure for the on_failure_callback pod-path")

    boom()
PY
cat > "$WORKDIR/$CBID/Dockerfile" <<DOCKER
FROM ${BASE_IMAGE}
COPY dag.py /home/leoflow/dag.py
ENV PYTHONPATH=/home/leoflow
DOCKER
CB_IMAGE="leoflow-e2e-cbdag:local"
"$ROOT/bin/leoflow" compile "$WORKDIR/$CBID" --image "$CB_IMAGE" \
  --build --dockerfile Dockerfile -o "$WORKDIR/$CBID/dag.json"
jq -e '.tasks[] | select(.task_id=="boom" and .on_failure_callback==true)' \
  "$WORKDIR/$CBID/dag.json" >/dev/null || fail "boom did not compile with on_failure_callback=true"
k3d_import "$CLUSTER" "$CB_IMAGE"
"$ROOT/bin/leoflow" push "$WORKDIR/$CBID/dag.json" --server "$API" --token "$TOKEN"
CB_RUN="$(curl -fsS -X POST -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{}' "$API/api/v2/dags/cbdag/dagRuns" | jq -r '.dag_run_id')"
[ -n "$CB_RUN" ] && [ "$CB_RUN" != "null" ] || fail "no cbdag dag_run_id returned"
log "cbdag run = $CB_RUN"
cb_deadline=$(( $(date +%s) + 240 ))
while :; do
  cb_state="$(curl -fsS -H "Authorization: Bearer $TOKEN" \
    "$API/api/v2/dags/cbdag/dagRuns/$CB_RUN/taskInstances" \
    | jq -r '.task_instances[] | select(.task_id=="boom") | .state')"
  [ "$cb_state" = "failed" ] && break
  echo "${cb_state:-}" | grep -qE 'success' && fail "cbdag boom unexpectedly succeeded"
  [ "$(date +%s)" -gt "$cb_deadline" ] && fail "cbdag boom did not reach failed (state=${cb_state:-<none>})"
  sleep 3
done
log "cbdag boom failed terminally (pod ran)"
cblog=""
for try in 1 0 2; do
  body="$(curl -fsS -H "Authorization: Bearer $TOKEN" \
    "$API/api/v2/dags/cbdag/dagRuns/$CB_RUN/taskInstances/boom/logs/$try" 2>/dev/null || true)"
  if echo "$body" | grep -q "E2E_CALLBACK_FIRED"; then cblog="$body"; break; fi
done
[ -n "$cblog" ] || fail "on_failure_callback did not run in the pod (#424): no E2E_CALLBACK_FIRED in boom's log"
echo "$cblog" | grep -q "running on_failure_callback" \
  || fail "runtime did not log the on_failure_callback lifecycle in the pod (#424)"
log "callback pod-path OK: on_failure_callback ran in the pod on terminal failure (#424)"

# ─────────────────────────────────────────────────────────────────────────────
# system_packages: on the GENERATED Dockerfile path, and one real TLS handshake
# from inside a task pod.
#
# Both halves were uncovered. `grep -rn system_packages test/ .github/ examples/`
# returned nothing: no fixture anywhere built a DAG image with the key set, so
# the `apt-get install` the compiler emits into every such image was certified
# by whoever last ran a build by hand. That matters more than an unused key
# normally would, because the suite the base is built on is what those package
# names and version pins resolve against — the apt version and its signature
# verifier move with the base image, and this is the only place we would find
# out.
#
# This leg deliberately ships NO Dockerfile. Every other project in this script
# hand-writes one, which is the documented escape hatch and not the path almost
# every user takes; a `system_packages:` bolted onto a project with its own
# Dockerfile would be silently ignored (ensureDockerfile honours the file on
# disk), and the leg would pass while testing nothing. `base_image` points at
# the base built above rather than the published one, so what is exercised is
# the base in this diff, not the last release's.
#
# The TLS half answers the question a version string cannot. python:*-slim
# links CPython's `ssl` against the system OpenSSL, so a base-image move
# relocates every outbound handshake a task makes onto a different libssl, and
# whatever that library refuses is refused at task runtime in a pod, against
# one endpoint, long after a green build. `ssl.OPENSSL_VERSION` cannot see any
# of that; only a handshake can, which is why this is here and not a printed
# version.
#
# Do not read this as guarding a specific policy difference. The certificate
# floor was measured across the bookworm→trixie move and did not move with it:
# both suites already run at security level 2, and an RSA-1024 leaf, an
# RSA-1024 CA, a SHA-1-signed leaf and TLS 1.1 are refused on both. What did
# change is the first flight's size (OpenSSL 3.5 offers a hybrid post-quantum
# group by default, 517 → 1525 bytes), which is the kind of thing no assertion
# can predict in advance and a completed handshake covers generically.
#
# pypi.org is the peer because this leg's own image build already had to reach
# it — if it is unreachable the build failed first, so this adds no external
# dependency the leg did not already have.
log "system_packages + in-pod TLS (generated Dockerfile path)"
SPID="sysdag"
mkdir -p "$WORKDIR/$SPID"
cat > "$WORKDIR/$SPID/dexaflow.yaml" <<YAML
schema_version: "1.0"
dag_id: ${SPID}
base_image: ${BASE_IMAGE}
system_packages:
  - curl
  - ca-certificates
build:
  platforms:
    - ${HOST_PLATFORM}
YAML
cat > "$WORKDIR/$SPID/dag.py" <<'PY'
"""sysdag — system_packages install + one real outbound TLS handshake, in a pod."""
from __future__ import annotations

from airflow.sdk import DAG, task


@task
def apt_and_tls() -> None:
    import shutil
    import ssl
    import urllib.error
    import urllib.request

    # The apt half: a binary that is NOT in python:*-slim and can only have
    # arrived through the generated Dockerfile's `apt-get install`. If apt or
    # its repository signature verification broke on the current suite, the
    # image build fails before this ever runs; this catches the subtler case
    # where the build succeeded and installed nothing usable.
    curl = shutil.which("curl")
    assert curl, "system_packages did not put curl on PATH (generated apt layer did not land)"
    print("E2E_SYSPKG curl=" + curl, flush=True)

    # The TLS half. Report the version for the log, then actually shake hands.
    print("E2E_OPENSSL " + ssl.OPENSSL_VERSION, flush=True)
    ctx = ssl.create_default_context()
    try:
        with urllib.request.urlopen(
            "https://pypi.org/simple/pip/", timeout=60, context=ctx
        ) as resp:
            assert resp.status == 200, f"unexpected HTTPS status {resp.status}"
    except urllib.error.URLError as err:
        # urlopen wraps the TLS failure in URLError and puts the ssl exception in
        # .reason, so an `except ssl.SSLCertVerificationError` here would never
        # fire and every TLS failure would be reported as a network one.
        # Classify off the wrapped cause instead: an ssl.SSLError is the
        # algorithm-policy regression this assertion exists for, and anything
        # else is the network, which is a rerun rather than a bug in the base.
        cause = getattr(err, "reason", None)
        if isinstance(cause, ssl.SSLError):
            raise AssertionError(
                f"outbound TLS failed under {ssl.OPENSSL_VERSION} — algorithm policy, "
                f"not reachability: {cause}"
            ) from err
        raise AssertionError(f"outbound HTTPS could not be attempted (network, not TLS): {err}") from err
    print("E2E_TLS_OK", flush=True)


with DAG("sysdag", schedule=None, tags=["e2e"]):
    apt_and_tls()
PY
[ -e "$WORKDIR/$SPID/Dockerfile" ] \
  && fail "sysdag must not ship a Dockerfile: the hand-written one would win and system_packages would never be installed"
SP_IMAGE="leoflow-e2e-sysdag:local"
# A unique version per run, as deploy-e2e.sh does and for the same reason: a
# DAG version is immutable, so against the shared dev database a re-run whose
# body changed is rejected 409 — and one whose body did not is accepted while
# quietly running the version already stored. The second is the dangerous half
# and it looks like a pass. CI gets a fresh Postgres per run and never sees
# either; a developer editing this leg locally sees both.
"$ROOT/bin/leoflow" compile "$WORKDIR/$SPID" --image "$SP_IMAGE" \
  --build --dag-version "e2e-$(date +%s)" -o "$WORKDIR/$SPID/dag.json"
# The generated Dockerfile installs as root and drops back to the numeric UID as
# its LAST instruction (#852). A root-final image is a CreateContainerConfigError
# under runAsNonRoot, discovered in the user's cluster; assert it here instead.
SP_USER="$(docker image inspect -f '{{.Config.User}}' "$SP_IMAGE")"
[ "$SP_USER" = "65532:65532" ] \
  || fail "the generated system_packages image ends on USER '$SP_USER', want numeric '65532:65532'"
log "generated DAG image final USER: $SP_USER"
k3d_import "$CLUSTER" "$SP_IMAGE"
"$ROOT/bin/leoflow" push "$WORKDIR/$SPID/dag.json" --server "$API" --token "$TOKEN"
SP_RUN="$(curl -fsS -X POST -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{}' "$API/api/v2/dags/$SPID/dagRuns" | jq -r '.dag_run_id')"
[ -n "$SP_RUN" ] && [ "$SP_RUN" != "null" ] || fail "no sysdag dag_run_id returned"
log "sysdag run = $SP_RUN"
sp_deadline=$(( $(date +%s) + 300 ))
while :; do
  sp_state="$(curl -fsS -H "Authorization: Bearer $TOKEN" \
    "$API/api/v2/dags/$SPID/dagRuns/$SP_RUN/taskInstances" \
    | jq -r '.task_instances[] | select(.task_id=="apt_and_tls") | .state')"
  [ "$sp_state" = "success" ] && break
  echo "${sp_state:-}" | grep -qE 'failed|upstream_failed' && fail "sysdag apt_and_tls failed (state=$sp_state)"
  [ "$(date +%s)" -gt "$sp_deadline" ] && fail "sysdag apt_and_tls did not succeed (state=${sp_state:-<none>})"
  sleep 3
done
splog=""
for try in 0 1 2; do
  body="$(curl -fsS -H "Authorization: Bearer $TOKEN" \
    "$API/api/v2/dags/$SPID/dagRuns/$SP_RUN/taskInstances/apt_and_tls/logs/$try" 2>/dev/null || true)"
  if echo "$body" | grep -q "E2E_SYSPKG"; then splog="$body"; break; fi
done
[ -n "$splog" ] || fail "no sysdag log shipped: cannot confirm the apt install or the TLS handshake"
echo "$splog" | grep -q "E2E_SYSPKG curl=" \
  || fail "system_packages did not install curl into the generated DAG image"
echo "$splog" | grep -q "E2E_TLS_OK" \
  || fail "no outbound TLS handshake completed from the task pod (OpenSSL security level?)"
log "system_packages installed and one real HTTPS handshake completed inside a task pod"
echo "$splog" | grep "E2E_OPENSSL" || true

log "E2E passed"
