#!/usr/bin/env bash
#
# 03-managed.sh — Switch the Leoflow Pro install to managed datastores
#                 (Cloud SQL for Postgres + Memorystore for Redis).
#
# This is the production-realistic path, run AFTER 00/01/02 (the in-cluster test
# install). It provisions managed Postgres + Redis on the cluster's VPC via
# private IP (Private Services Access / VPC peering), then re-points the existing
# Helm release at them.
#
# Prereqs: 00-create-cluster.sh, 01-install-addons.sh, 02-install-leoflow.sh have
# run (02 created the agent-TLS cert + CA ConfigMap and the base secrets we reuse).
#
# Idempotent: API enable / address / peering / instance creates are guarded.
#
# KNOWN LIMITATIONS (tracked upstream — see the repo issues):
#   * Redis runs with AUTH but TLS DISABLED: the Go redis client has no CA-injection
#     knob, so it can't verify Memorystore's per-instance CA (issue #312). Security
#     here = private-IP-only VPC + AUTH.
#   * Postgres uses sslmode=require (encrypted, NOT cert-verified); verify-full needs
#     a mounted CA the chart doesn't expose yet (issue #315).
#   * `helm upgrade` that only changes datastore URLs (in the Secret) does NOT roll
#     the pod — the chart lacks a checksum/secret annotation (issue #316) — so this
#     script does an explicit `kubectl rollout restart`.
#
# Usage:
#   ./03-managed.sh
#   REGION=us-central1 PG_TIER=db-f1-micro REDIS_SIZE=1 ./03-managed.sh
#
set -euo pipefail

# ---- Parameters (override via environment) ---------------------------------
PROJECT="${PROJECT:-$(gcloud config get-value project 2>/dev/null)}"
REGION="${REGION:-us-central1}"            # cheapest US region
NETWORK="${NETWORK:-default}"              # the cluster's VPC (00 uses default)
NAMESPACE="${NAMESPACE:-leoflow}"
RELEASE="${RELEASE:-leoflow}"
PSA_RANGE_NAME="${PSA_RANGE_NAME:-leoflow-psa}"   # allocated range for private services access
PG_INSTANCE="${PG_INSTANCE:-leoflow-pg}"
PG_TIER="${PG_TIER:-db-f1-micro}"          # cheapest; needs --edition=ENTERPRISE
PG_DB="${PG_DB:-leoflow}"
REDIS_INSTANCE="${REDIS_INSTANCE:-leoflow-redis}"
REDIS_SIZE="${REDIS_SIZE:-1}"             # GB (Basic tier)

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CHART_DIR="$(cd "$SCRIPT_DIR/../../helm/dexaflow" && pwd)"
VALUES_LOCAL="$SCRIPT_DIR/values.local.yaml"            # from 02 (reused secrets)
VALUES_MANAGED="$SCRIPT_DIR/values.managed.local.yaml"  # gitignored output

log() { printf '\n\033[1;34m==>\033[0m %s\n' "$*"; }
die() { printf '\n\033[1;31mERROR:\033[0m %s\n' "$*" >&2; exit 1; }

[ -n "$PROJECT" ] || die "No GCP project set."
[ -f "$VALUES_LOCAL" ] || die "Missing $VALUES_LOCAL — run ./02-install-leoflow.sh first (it creates the agent-TLS cert/CA + base secrets this step reuses)."
kubectl cluster-info >/dev/null 2>&1 || die "No reachable cluster."

# ---- 1. APIs ----------------------------------------------------------------
log "Enabling managed-service APIs (servicenetworking, sqladmin, redis)"
gcloud services enable servicenetworking.googleapis.com sqladmin.googleapis.com redis.googleapis.com --project="$PROJECT"

# ---- 2. Private Services Access (peering) -----------------------------------
if ! gcloud compute addresses describe "$PSA_RANGE_NAME" --global --project="$PROJECT" >/dev/null 2>&1; then
  log "Allocating PSA range '$PSA_RANGE_NAME' on VPC '$NETWORK'"
  gcloud compute addresses create "$PSA_RANGE_NAME" --global --purpose=VPC_PEERING \
    --prefix-length=20 --network="$NETWORK" --project="$PROJECT"
fi
if ! gcloud services vpc-peerings list --network="$NETWORK" --project="$PROJECT" \
      --format="value(peering)" 2>/dev/null | grep -q servicenetworking; then
  log "Connecting servicenetworking VPC peering"
  gcloud services vpc-peerings connect --service=servicenetworking.googleapis.com \
    --ranges="$PSA_RANGE_NAME" --network="$NETWORK" --project="$PROJECT"
fi

# ---- 3. Cloud SQL (private IP) ----------------------------------------------
if gcloud sql instances describe "$PG_INSTANCE" --project="$PROJECT" >/dev/null 2>&1; then
  log "Cloud SQL '$PG_INSTANCE' already exists — skipping"
  PGPW="$(cat "$SCRIPT_DIR/.managed-pgpw" 2>/dev/null || true)"
  [ -n "$PGPW" ] || die "Cloud SQL exists but $SCRIPT_DIR/.managed-pgpw (password) is missing."
else
  PGPW="$(openssl rand -hex 16)"; printf '%s' "$PGPW" > "$SCRIPT_DIR/.managed-pgpw"; chmod 600 "$SCRIPT_DIR/.managed-pgpw"
  log "Creating Cloud SQL '$PG_INSTANCE' ($PG_TIER, ENTERPRISE, private IP) — ~8-10 min"
  # --edition=ENTERPRISE: the default ENTERPRISE_PLUS rejects shared-core tiers like db-f1-micro.
  gcloud sql instances create "$PG_INSTANCE" \
    --database-version=POSTGRES_16 --edition=ENTERPRISE --tier="$PG_TIER" \
    --region="$REGION" --network="projects/$PROJECT/global/networks/$NETWORK" \
    --no-assign-ip --root-password="$PGPW" --project="$PROJECT"
fi
log "Ensuring database '$PG_DB' exists"
gcloud sql databases create "$PG_DB" --instance="$PG_INSTANCE" --project="$PROJECT" 2>/dev/null \
  || echo "  (database already exists)"
PG_IP="$(gcloud sql instances describe "$PG_INSTANCE" --project="$PROJECT" --format='value(ipAddresses[0].ipAddress)')"

# ---- 4. Memorystore (private, AUTH; TLS off — see #312) --------------------
if ! gcloud redis instances describe "$REDIS_INSTANCE" --region="$REGION" --project="$PROJECT" >/dev/null 2>&1; then
  log "Creating Memorystore '$REDIS_INSTANCE' (Basic ${REDIS_SIZE}GB, AUTH) — ~5 min"
  # --enable-auth (NOT --auth-enabled). TLS omitted on purpose (issue #312).
  gcloud redis instances create "$REDIS_INSTANCE" \
    --size="$REDIS_SIZE" --region="$REGION" --redis-version=redis_7_0 \
    --network="projects/$PROJECT/global/networks/$NETWORK" \
    --connect-mode=PRIVATE_SERVICE_ACCESS --enable-auth --tier=basic --project="$PROJECT"
fi
REDIS_HOST="$(gcloud redis instances describe "$REDIS_INSTANCE" --region="$REGION" --project="$PROJECT" --format='value(host)')"
REDIS_PORT="$(gcloud redis instances describe "$REDIS_INSTANCE" --region="$REGION" --project="$PROJECT" --format='value(port)')"
REDIS_AUTH="$(gcloud redis instances get-auth-string "$REDIS_INSTANCE" --region="$REGION" --project="$PROJECT" --format='value(authString)')"

# ---- 5. Managed values (reuse 02's secrets + agent TLS) ---------------------
log "Writing $VALUES_MANAGED (gitignored)"
JWT=$(grep -E "^\s*jwtSecret:" "$VALUES_LOCAL" | sed 's/.*jwtSecret: *//; s/"//g')
SECRETKEY=$(grep -E "^secretKey:" "$VALUES_LOCAL" | sed 's/.*secretKey: *//; s/"//g')
BOOT=$(grep -A1 "^bootstrap:" "$VALUES_LOCAL" | grep password | sed 's/.*password: *//; s/"//g')
cat > "$VALUES_MANAGED" <<EOF
# GENERATED by 03-managed.sh — DO NOT COMMIT (gitignored *.local.yaml).
database:
  url: postgres://postgres:${PGPW}@${PG_IP}:5432/${PG_DB}?sslmode=require
redis:
  url: redis://:${REDIS_AUTH}@${REDIS_HOST}:${REDIS_PORT}/0
auth:
  jwtSecret: "${JWT}"
secretKey: "${SECRETKEY}"
bootstrap:
  password: "${BOOT}"
image:
  tag: "0.0.1-prealpha.28"
migrations:
  image:
    tag: "0.0.1-prealpha.28"
replicaCount: 1
agentTLS:
  enabled: true
  serverCertSecret: "leoflow-agent-tls"
  caConfigMap: "leoflow-agent-ca"
logs:
  persistence:
    enabled: true
    size: 5Gi
EOF
chmod 600 "$VALUES_MANAGED"

# ---- 6. Upgrade + roll the pod (issue #316) --------------------------------
log "helm upgrade -> managed datastores (migration hook runs against Cloud SQL)"
helm upgrade "$RELEASE" "$CHART_DIR" -n "$NAMESPACE" -f "$VALUES_MANAGED" --wait --timeout 8m
log "Rolling the Deployment so it picks up the new datastore Secret (chart lacks checksum/secret — #316)"
kubectl -n "$NAMESPACE" rollout restart deploy/"$RELEASE"
kubectl -n "$NAMESPACE" rollout status deploy/"$RELEASE" --timeout=180s

log "Done. Postgres -> ${PG_IP} (Cloud SQL), Redis -> ${REDIS_HOST} (Memorystore)."
echo "Verify:  kubectl -n $NAMESPACE port-forward svc/$RELEASE 8080:8080  then  curl -fsS http://127.0.0.1:8080/readyz"
