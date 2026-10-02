#!/usr/bin/env bash
#
# 01-install-addons.sh — Cluster-level prerequisites for Dexaflow Pro.
#
# Installs cert-manager. The Pro Helm chart ships with agentTLS.enabled=true by
# default (the agent <-> control-plane gRPC channel is TLS, issue #58), and the
# server refuses to boot in insecure mode. cert-manager is the standard way to
# produce the server cert Secret + CA trust bundle the chart consumes via
# agentTLS.serverCertSecret / agentTLS.caConfigMap.
#
# metrics-server (needed for HPA) and the HPA controller already ship with GKE,
# so they are NOT installed here.
#
# Idempotent: kubectl apply + helm upgrade --install are safe to re-run.
#
# Usage:
#   ./01-install-addons.sh
#   CERT_MANAGER_VERSION=v1.16.3 ./01-install-addons.sh
#
set -euo pipefail

CERT_MANAGER_VERSION="${CERT_MANAGER_VERSION:-v1.16.3}"

log() { printf '\n\033[1;34m==>\033[0m %s\n' "$*"; }

# ---- cert-manager (CRDs + controllers) -------------------------------------
log "Installing cert-manager $CERT_MANAGER_VERSION"
kubectl apply -f "https://github.com/cert-manager/cert-manager/releases/download/${CERT_MANAGER_VERSION}/cert-manager.yaml"

log "Waiting for cert-manager to be ready"
kubectl -n cert-manager rollout status deploy/cert-manager --timeout=180s
kubectl -n cert-manager rollout status deploy/cert-manager-webhook --timeout=180s
kubectl -n cert-manager rollout status deploy/cert-manager-cainjector --timeout=180s

log "cert-manager ready. Next: wire the agent-TLS issuer + cert in the Helm install (see README 'Next phase')."
