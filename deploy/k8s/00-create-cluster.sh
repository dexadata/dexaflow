#!/usr/bin/env bash
#
# 00-create-cluster.sh — Provision a GKE Standard cluster for testing Dexaflow Pro.
#
# Design (see deploy/k8s/README.md for the rationale):
#   - GKE *Standard*, *zonal* (NOT Autopilot): we want a generic, vanilla-ish
#     Kubernetes so the Helm chart is validated for "any K8s", and so the chart's
#     resource requests/limits, PSA, NetworkPolicy, HPA and PDB are exercised
#     for real instead of being mutated by Autopilot.
#   - Single Spot node pool (e2-standard-2) with autoscaling 1->3 to keep the
#     idle cost near zero. Bump MACHINE_TYPE / MAX_NODES for the later load test.
#   - Dataplane V2 (Cilium) so the chart's NetworkPolicy template is enforced.
#   - Workload Identity enabled (for the future keyless cloud-auth work, #56).
#
# Idempotent: re-running skips resources that already exist. Safe to re-run.
#
# Usage:
#   ./00-create-cluster.sh                 # uses the defaults below
#   PROJECT=my-proj ZONE=us-central1-a ./00-create-cluster.sh
#
set -euo pipefail

# ---- Parameters (override via environment) ---------------------------------
# PROJECT is read from your active gcloud config so no environment-specific id is
# committed to this (public) repo. Override with `PROJECT=my-proj ./00-create-cluster.sh`.
PROJECT="${PROJECT:-$(gcloud config get-value project 2>/dev/null)}"
ZONE="${ZONE:-us-central1-a}"                 # cheapest US region (Iowa), single zone
CLUSTER="${CLUSTER:-leoflow-test}"            # cluster name
RELEASE_CHANNEL="${RELEASE_CHANNEL:-regular}" # regular | rapid | stable | None
MACHINE_TYPE="${MACHINE_TYPE:-e2-standard-2}" # 2 vCPU / 8 GB; bump for load test
DISK_TYPE="${DISK_TYPE:-pd-standard}"         # cheapest persistent disk
DISK_SIZE="${DISK_SIZE:-50}"                  # GB per node boot disk
MIN_NODES="${MIN_NODES:-1}"                   # autoscaler floor (idle = cheap)
MAX_NODES="${MAX_NODES:-3}"                   # autoscaler ceiling
NUM_NODES="${NUM_NODES:-1}"                   # initial node count
USE_SPOT="${USE_SPOT:-true}"                  # Spot VMs = ~70% cheaper; set false for load test stability

# ---- Helpers ----------------------------------------------------------------
log() { printf '\n\033[1;34m==>\033[0m %s\n' "$*"; }
die() { printf '\n\033[1;31mERROR:\033[0m %s\n' "$*" >&2; exit 1; }

[ -n "$PROJECT" ] || die "No GCP project set. Run 'gcloud config set project <id>' or pass PROJECT=<id>."
log "Using project: $PROJECT  |  zone: $ZONE  |  cluster: $CLUSTER"

# ---- 1. Enable the required APIs -------------------------------------------
log "Enabling required APIs (container, compute, artifactregistry)"
gcloud services enable \
  container.googleapis.com \
  compute.googleapis.com \
  artifactregistry.googleapis.com \
  --project="$PROJECT"

# ---- 2. Create the cluster (zonal, Standard) -------------------------------
if gcloud container clusters describe "$CLUSTER" --zone="$ZONE" --project="$PROJECT" >/dev/null 2>&1; then
  log "Cluster '$CLUSTER' already exists in $ZONE — skipping creation"
else
  log "Creating GKE Standard zonal cluster '$CLUSTER' in $ZONE"
  spot_flag=""
  [ "$USE_SPOT" = "true" ] && spot_flag="--spot"
  gcloud container clusters create "$CLUSTER" \
    --project="$PROJECT" \
    --zone="$ZONE" \
    --release-channel="$RELEASE_CHANNEL" \
    --enable-dataplane-v2 \
    --enable-ip-alias \
    --workload-pool="${PROJECT}.svc.id.goog" \
    --machine-type="$MACHINE_TYPE" \
    --disk-type="$DISK_TYPE" \
    --disk-size="$DISK_SIZE" \
    --num-nodes="$NUM_NODES" \
    --enable-autoscaling --min-nodes="$MIN_NODES" --max-nodes="$MAX_NODES" \
    --enable-autorepair --enable-autoupgrade \
    --addons=HorizontalPodAutoscaling,HttpLoadBalancing \
    $spot_flag
fi

# ---- 3. Fetch kubeconfig credentials ---------------------------------------
log "Fetching cluster credentials (writes to ~/.kube/config)"
gcloud container clusters get-credentials "$CLUSTER" --zone="$ZONE" --project="$PROJECT"

# ---- 4. Create the leoflow namespace ---------------------------------------
# The server creates task pods in 'leoflow' (see helm values.yaml: taskNamespace).
log "Ensuring 'leoflow' namespace exists"
kubectl create namespace leoflow --dry-run=client -o yaml | kubectl apply -f -

# ---- 5. Sanity check --------------------------------------------------------
log "Cluster nodes:"
kubectl get nodes -o wide

log "Done. Next: ./01-install-addons.sh  (cert-manager for the agent TLS channel)"
