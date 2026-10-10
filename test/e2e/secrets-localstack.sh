#!/usr/bin/env bash
#
# End-to-end test for the ADR 0060 external secrets resolver against a REAL
# (emulated) AWS Secrets Manager — LocalStack running inside a k3d cluster.
#
# What it proves, in a real pod, that the unit and in-process tests cannot:
#   * a DAG declares a connection + a variable that are ABSENT from the leoflow
#     vault; registration still accepts them because the operator's backend
#     covers them (ADR 0060 B1 coverage-at-registration);
#   * at run time the in-pod agent spawns the Python resolver, which drives the
#     Airflow SecretsManagerBackend under the pod, fetches both names from
#     LocalStack, and the task sees them as AIRFLOW_CONN_*/AIRFLOW_VAR_* — the
#     full yaml-declaration -> agent -> python resolver -> provider SM chain.
#
# Keyless scope: LocalStack cannot exercise the IRSA / EKS Pod Identity / GKE
# Workload Identity token handshake, so the backend kwargs here carry static test
# credentials (LocalStack accepts test/test). That keyless identity binding is the
# ONE piece this test deliberately does not cover — it remains the EKS/GKE cluster
# validation. Everything from declaration to a resolved value in the pod is real.
# NetworkPolicy egress is covered separately by pro-netpol-rwx.sh (needs Calico;
# k3d's default CNI does not enforce policy, so asserting it here would be theater).
#
# Requirements: k3d, kubectl, docker, jq, curl, the leoflow binaries
# (`make build`). Developer/CI tool, not part of `go test`.
#
# Usage: test/e2e/secrets-localstack.sh [cluster-name]
set -euo pipefail

# shellcheck source=test/e2e/lib.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

CLUSTER="${1:-leoflow-secrets-e2e}"
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
WORKDIR="$(mktemp -d)"
PY_VERSION="3.11"
BASE_IMAGE="leoflow-base:py${PY_VERSION}"
DAG_IMAGE="leoflow-secrets-e2e-dag:dev"
DAG_ID="secretsdemo"
HTTP_PORT="${LEOFLOW_E2E_HTTP_PORT:-8080}"
API="http://localhost:${HTTP_PORT}"
GRPC_PORT="9091"
HOST_ADDR="${LEOFLOW_E2E_HOST_ADDR:-$([ "$(uname -s)" = Linux ] && echo host.k3d.internal || echo host.docker.internal)}"
SERVER_PID=""
PF_PID=""

# The secrets we seed into LocalStack — NOT into the leoflow vault. That the task
# ends up with these exact values is the proof the external resolver ran.
SM_VAR_REGION="eu-west-external-99"
SM_CONN_WAREHOUSE="postgres://svc:pw@warehouse.internal:5432/analytics"
# LocalStack's in-cluster address the POD-SIDE resolver dials (cluster DNS).
LOCALSTACK_ENDPOINT="http://localstack.leoflow.svc.cluster.local:4566"

log()  { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
dump_pods() {
  printf '\033[1;33m--- pods (namespace leoflow) ---\033[0m\n' >&2
  kubectl get pods -n leoflow -o wide >&2 2>&1 || true
  for p in $(kubectl get pods -n leoflow -o name 2>/dev/null); do
    printf '\033[1;33m--- logs %s ---\033[0m\n' "$p" >&2
    kubectl logs -n leoflow "$p" --all-containers --tail=100 >&2 2>&1 || true
  done
}
fail() { printf '\033[1;31mFAIL:\033[0m %s\n' "$*" >&2; dump_pods; exit 1; }

cleanup() {
  [ -n "$PF_PID" ] && kill "$PF_PID" 2>/dev/null || true
  [ -n "$SERVER_PID" ] && kill "$SERVER_PID" 2>/dev/null || true
  k3d cluster delete "$CLUSTER" >/dev/null 2>&1 || true
  rm -rf "$WORKDIR"
}
trap cleanup EXIT

for tool in k3d kubectl docker jq curl; do
  command -v "$tool" >/dev/null || fail "missing required tool: $tool"
done

log "Scaffolding the DAG project ($DAG_ID) that DECLARES an external connection + variable"
mkdir -p "$WORKDIR/$DAG_ID"
case "$(uname -m)" in arm64|aarch64) HOST_PLATFORM="linux/arm64" ;; *) HOST_PLATFORM="linux/amd64" ;; esac
cat > "$WORKDIR/$DAG_ID/dexaflow.yaml" <<YAML
schema_version: "1.0"
dag_id: ${DAG_ID}
description: Declares a connection + variable resolved from an external backend.
python_version: "3.11"
dependencies: [apache-airflow-providers-amazon]
# Declared, but intentionally NOT present in the leoflow vault — resolved from the
# operator-configured external backend (LocalStack Secrets Manager) at run time.
connections:
  - warehouse
variables:
  - region
build:
  platforms:
    - ${HOST_PLATFORM}
YAML

# The task reads the declared secrets from its env and asserts they carry the
# values seeded ONLY in LocalStack — so a green task is proof the resolver ran.
cat > "$WORKDIR/$DAG_ID/dag.py" <<PY
"""${DAG_ID} — external secrets resolver e2e."""
from __future__ import annotations

import os
from urllib.parse import urlsplit

from airflow.sdk import DAG, task


@task
def use_secrets() -> None:
    region = os.environ.get("AIRFLOW_VAR_REGION", "<MISSING>")
    conn = os.environ.get("AIRFLOW_CONN_WAREHOUSE", "")
    host = urlsplit(conn).hostname or "<MISSING>"
    print(f"SECRETS_E2E_VAR={region}")
    print(f"SECRETS_E2E_CONN_HOST={host}")
    assert region == "${SM_VAR_REGION}", f"variable not resolved from the external backend: {region!r}"
    assert host == "warehouse.internal", f"connection not resolved from the external backend: {conn!r}"


with DAG("${DAG_ID}", schedule="@daily", catchup=False, tags=["e2e", "secrets"]):
    use_secrets()
PY

cat > "$WORKDIR/$DAG_ID/Dockerfile" <<DOCKER
FROM ${BASE_IMAGE}
# The pod-side resolver imports the Airflow AWS provider (SecretsManagerBackend);
# it must be present in the image, exactly as a real project's dependencies: are.
RUN pip install --no-cache-dir apache-airflow-providers-amazon
COPY dag.py /home/leoflow/dag.py
ENV PYTHONPATH=/home/leoflow
DOCKER

log "Building base and DAG images"
docker build --provenance=false -f "$ROOT/runtime/Dockerfile" --build-arg "PYTHON_VERSION=${PY_VERSION}" -t "$BASE_IMAGE" "$ROOT"

log "Creating k3d cluster '$CLUSTER'"
k3d cluster create "$CLUSTER" --wait

log "Creating the 'leoflow' namespace"
kubectl create namespace leoflow

log "Deploying LocalStack (Secrets Manager) into the cluster"
kubectl apply -n leoflow -f - <<'YAML'
apiVersion: apps/v1
kind: Deployment
metadata:
  name: localstack
  labels: { app: localstack }
spec:
  replicas: 1
  selector: { matchLabels: { app: localstack } }
  template:
    metadata: { labels: { app: localstack } }
    spec:
      containers:
        - name: localstack
          image: localstack/localstack:3
          env:
            - { name: SERVICES, value: "secretsmanager" }
            - { name: EAGER_SERVICE_LOADING, value: "1" }
          ports:
            - containerPort: 4566
          readinessProbe:
            httpGet: { path: /_localstack/health, port: 4566 }
            initialDelaySeconds: 5
            periodSeconds: 3
            timeoutSeconds: 3
            failureThreshold: 40
---
apiVersion: v1
kind: Service
metadata:
  name: localstack
spec:
  selector: { app: localstack }
  ports:
    - { port: 4566, targetPort: 4566 }
YAML

log "Waiting for LocalStack to become ready"
kubectl -n leoflow rollout status deploy/localstack --timeout=180s \
  || fail "LocalStack did not become ready"

log "Seeding the secrets into LocalStack (airflow/connections/warehouse, airflow/variables/region)"
# Seed from the host over a port-forward, hitting the Secrets Manager JSON API
# directly with curl — no extra container image to pull (the aws-cli image pull
# was a Docker Hub rate-limit flake). LocalStack does not validate SigV4, so a
# placeholder Authorization header is enough; X-Amz-Target routes the call.
kubectl -n leoflow port-forward svc/localstack 4566:4566 >/dev/null 2>&1 &
PF_PID=$!
for _ in $(seq 1 30); do
  curl -fsS "http://localhost:4566/_localstack/health" >/dev/null 2>&1 && break
  sleep 2
done
sm_create() {
  curl -fsS -X POST "http://localhost:4566/" \
    -H "Content-Type: application/x-amz-json-1.1" \
    -H "X-Amz-Target: secretsmanager.CreateSecret" \
    -H "Authorization: AWS4-HMAC-SHA256 Credential=test/20230101/us-east-1/secretsmanager/aws4_request, SignedHeaders=host, Signature=test" \
    -d "$1"
}
# CreateSecret over the raw JSON API requires a ClientRequestToken of 32-64 chars
# (boto/aws-cli generate one; the raw call must supply it). Fixed UUIDs keep the
# seed deterministic.
sm_create "{\"Name\":\"airflow/connections/warehouse\",\"ClientRequestToken\":\"11111111-1111-1111-1111-111111111111\",\"SecretString\":\"${SM_CONN_WAREHOUSE}\"}" | grep -q '"ARN"' \
  || fail "seeding the warehouse connection into LocalStack failed"
sm_create "{\"Name\":\"airflow/variables/region\",\"ClientRequestToken\":\"22222222-2222-2222-2222-222222222222\",\"SecretString\":\"${SM_VAR_REGION}\"}" | grep -q '"ARN"' \
  || fail "seeding the region variable into LocalStack failed"
kill "$PF_PID" 2>/dev/null || true
PF_PID=""
log "seeded"

log "Starting the control plane with the external secrets backend configured"
export LEOFLOW_AUTH_JWT_SECRET="e2e-secret"
export LEOFLOW_BOOTSTRAP_PASSWORD="admin"
export LEOFLOW_EXECUTOR_AGENT_CONTROL_PLANE_ADDR="${HOST_ADDR}:${GRPC_PORT}"
export LEOFLOW_LOGS_DIR="${WORKDIR}/logs"
export LEOFLOW_AGENT_ALLOW_INSECURE_SECRETS=true
export LEOFLOW_SECRET_KEY="e2e-secret-key-32-bytes-padding!"
export LEOFLOW_SERVER_HTTP_ADDR="0.0.0.0:${HTTP_PORT}"
# ADR 0060: the pod-side resolver drives this Airflow backend under the pod's own
# identity. endpoint_url + static creds target LocalStack (the keyless handshake is
# the only part LocalStack can't emulate). Prefixes both declare coverage (B1) and
# route the SecretId (airflow/connections/<name>, airflow/variables/<name>).
export LEOFLOW_SECRETS_BACKEND="airflow.providers.amazon.aws.secrets.secrets_manager.SecretsManagerBackend"
export LEOFLOW_SECRETS_BACKEND_KWARGS="{\"connections_prefix\":\"airflow/connections\",\"variables_prefix\":\"airflow/variables\",\"region_name\":\"us-east-1\",\"endpoint_url\":\"${LOCALSTACK_ENDPOINT}\",\"aws_access_key_id\":\"test\",\"aws_secret_access_key\":\"test\"}"
"$ROOT/bin/leoflow-server" &
SERVER_PID=$!
sleep 5

log "Compiling, building, and importing the DAG image"
"$ROOT/bin/leoflow" compile "$WORKDIR/$DAG_ID" --image "$DAG_IMAGE" \
  --build --dockerfile Dockerfile -o "$WORKDIR/$DAG_ID/dag.json"
log "Asserting the declaration reached dag.json (connections + variables)"
jq -e '.connections | index("warehouse")' "$WORKDIR/$DAG_ID/dag.json" >/dev/null \
  || fail "warehouse connection did not compile into dag.json"
jq -e '.variables | index("region")' "$WORKDIR/$DAG_ID/dag.json" >/dev/null \
  || fail "region variable did not compile into dag.json"
k3d_import "$CLUSTER" "$BASE_IMAGE" "$DAG_IMAGE"

log "Pushing the DAG — registration must ACCEPT names absent from the vault (B1 coverage)"
TOKEN="$("$ROOT/bin/leoflow" auth create-token --server "$API" --username admin@leoflow.local --password admin)"
"$ROOT/bin/leoflow" push "$WORKDIR/$DAG_ID/dag.json" --server "$API" --token "$TOKEN" \
  || fail "push rejected a DAG whose declared names live only in the external backend (B1 coverage-at-registration broke)"

log "Triggering a run"
RUN_ID="$(curl -fsS -X POST -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{}' "$API/api/v2/dags/$DAG_ID/dagRuns" | jq -r '.dag_run_id')"
[ -n "$RUN_ID" ] && [ "$RUN_ID" != "null" ] || fail "no dag_run_id returned"
log "run = $RUN_ID"

log "Waiting for the task to succeed (its in-task asserts guard external resolution)"
deadline=$(( $(date +%s) + 300 ))
while :; do
  states="$(curl -fsS -H "Authorization: Bearer $TOKEN" \
    "$API/api/v2/dags/$DAG_ID/dagRuns/$RUN_ID/taskInstances" | jq -r '.task_instances[].state')"
  log "task states: [$(echo "$states" | tr '\n' ' ')] pods: [$(kubectl get pods -n leoflow --no-headers 2>/dev/null | awk '{print $1"="$3}' | tr '\n' ' ')]"
  echo "$states" | grep -qE 'failed|upstream_failed' && fail "task failed — external resolution did not deliver the declared secrets: $states"
  if [ -n "$states" ] && ! echo "$states" | grep -qvE 'success|skipped'; then
    log "task terminal-success: $states"
    break
  fi
  [ "$(date +%s)" -gt "$deadline" ] && fail "timed out; last states: ${states:-<none>}"
  sleep 3
done

log "Asserting the resolved values in the task logs came from LocalStack"
LOG_PATH="$API/api/v2/dags/$DAG_ID/dagRuns/$RUN_ID/taskInstances/use_secrets/logs"
log_body=""
for try in 1 0 2; do
  body="$(curl -fsS -H "Authorization: Bearer $TOKEN" "$LOG_PATH/$try" 2>/dev/null || true)"
  if [ -n "$body" ] && ! echo "$body" | grep -q '"status":404'; then log_body="$body"; break; fi
done
[ -n "$log_body" ] || fail "no logs shipped from the task pod"
echo "$log_body" | grep -q "SECRETS_E2E_VAR=${SM_VAR_REGION}" \
  || fail "variable value from LocalStack missing in task logs (got: $(echo "$log_body" | grep -o 'SECRETS_E2E_VAR=[^\"]*' | head -1))"
echo "$log_body" | grep -q "SECRETS_E2E_CONN_HOST=warehouse.internal" \
  || fail "connection value from LocalStack missing in task logs (got: $(echo "$log_body" | grep -o 'SECRETS_E2E_CONN_HOST=[^\"]*' | head -1))"

log "PASS: the declared connection + variable were resolved from LocalStack Secrets Manager in a real pod (ADR 0060)"
