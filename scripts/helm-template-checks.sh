#!/usr/bin/env bash
# helm-template-checks.sh — render the chart with a minimal Pro-shaped values
# set and assert the rendered output contains the env vars and Secret keys the
# control plane needs at runtime.
#
# This is the seed of the eventual chart-test CI gate (issue #143). It is
# intentionally small and shell-only so it can run in any CI runner with `helm`
# installed (or locally with `brew install helm`).
#
# Run from the repo root:
#   bash scripts/helm-template-checks.sh
set -euo pipefail

CHART="helm/dexaflow"
if [ ! -f "$CHART/Chart.yaml" ]; then
  echo "helm-template-checks: $CHART not found; run from the repo root" >&2
  exit 2
fi

# Minimal Pro-shaped values: external Postgres + external Redis + a jwtSecret
# and a secretKey (the values the chart will encrypt at rest and inject as env).
# Fixture keys are NOT real credentials — only used to render the chart.
RENDERED=$(helm template dexaflow-test "$CHART" \
  --set database.url='postgres://leoflow:p@db:5432/leoflow?sslmode=disable' \
  --set redis.url='redis://r:6379/0' \
  --set auth.jwtSecret='helm-template-check-jwt-fixture' \
  --set secretKey='leoflow-tmpl-check-secret-key-32' \
  --set agentTLS.serverCertSecret='leoflow-agent-tls-fixture' \
  --set agentTLS.caConfigMap='leoflow-agent-ca-fixture')

fail=0
expect_substring() {
  local needle="$1"
  local description="$2"
  if ! grep -qF -- "$needle" <<<"$RENDERED"; then
    echo "FAIL: missing $description ($needle)" >&2
    fail=1
  else
    echo "OK:   $description"
  fi
}

# Each line below is a contract the chart must honour. Add to this list as new
# config keys land.
expect_substring 'name: LEOFLOW_DATABASE_URL'  "DB URL env entry in deployment"
expect_substring 'name: LEOFLOW_REDIS_URL'     "Redis URL env entry in deployment"
expect_substring 'name: LEOFLOW_AUTH_JWT_SECRET' "JWT secret env entry in deployment"
expect_substring 'name: LEOFLOW_SECRET_KEY'    "Connection encryption key env entry in deployment (ADR 0019)"
expect_substring 'jwtSecret:'                  "jwtSecret key in chart-managed Secret"
expect_substring 'secretKey:'                  "secretKey in chart-managed Secret (ADR 0019)"
# The warm-pool flag and the two agent-credential knobs it is coupled to (ADR
# 0058 D2) are stamped on EVERY render, defaults included, so a `helm upgrade`
# reasserts them over an out-of-band `kubectl set env`. Reaching the feature
# through the chart at all is what these three make possible.
expect_substring 'name: LEOFLOW_EXECUTION_WARM_POOLS_ENABLED'  "warm-pool flag env entry in deployment (ADR 0058)"
expect_substring 'name: LEOFLOW_AUTH_AGENT_TOKEN_TRANSPORT'    "agent token transport env entry in deployment (ADR 0055)"
expect_substring 'name: LEOFLOW_AUTH_SECRET_LIVENESS_MODE'     "secret liveness mode env entry in deployment (ADR 0055)"
# The scope-by-declaration policy is stamped for the discoverability reason
# rather than the coupling one (#803): it participates in no render-time guard,
# but it is what decides whether a task receives the whole tenant vault, so the
# chart has to be where an operator finds it and its default.
expect_substring 'name: LEOFLOW_AUTH_SECRET_SCOPING'           "secret scoping policy env entry in deployment (ADR 0055)"

# Migration Job must be hardened the same way as the Deployment (#174):
# restricted-PSA clusters require runAsNonRoot+numeric runAsUser on EVERY
# pod, including pre-install hooks. Scope the rendering to just the Job
# template so we don't accidentally validate the Deployment's hardening
# here (that lives in the same rendered output but is asserted above by
# proxy of the env contract).
JOB_RENDERED=$(helm template dexaflow-test "$CHART" \
  --set database.url='postgres://leoflow:p@db:5432/leoflow?sslmode=disable' \
  --set redis.url='redis://r:6379/0' \
  --set auth.jwtSecret='helm-template-check-jwt-fixture' \
  --set secretKey='leoflow-tmpl-check-secret-key-32' \
  --set agentTLS.serverCertSecret='leoflow-agent-tls-fixture' \
  --set agentTLS.caConfigMap='leoflow-agent-ca-fixture' \
  --show-only templates/migration-job.yaml)

expect_in_job() {
  local needle="$1"
  local description="$2"
  if ! grep -qF -- "$needle" <<<"$JOB_RENDERED"; then
    echo "FAIL: migrate Job missing $description ($needle)" >&2
    fail=1
  else
    echo "OK:   migrate Job has $description"
  fi
}
expect_in_job 'runAsNonRoot: true' "runAsNonRoot:true (restricted PSA gate)"
expect_in_job 'runAsUser: 65532'   "runAsUser:65532 (distroless nonroot UID)"

# Regression guard for PR #171: the PoC values fixture (the recipe in
# helm/dexaflow/examples/) shipped with a 34-byte secretKey, which
# silently broke ParseKey (AES-256 requires exactly 32 bytes) and made
# Connection management 503 for anyone following the recipe verbatim.
# Validate every YAML fixture under helm/dexaflow/examples/ that sets
# `secretKey:` so a future fixture edit can't repeat the same class of
# bug. The check is plain bash — no python / yq dependency.
for fixture in helm/dexaflow/examples/*.yaml; do
  [ -f "$fixture" ] || continue
  # `|| true` because the script runs under `set -o pipefail` and grep exits 1 when
  # a fixture legitimately sets no secretKey (an SSO example needs none): without it
  # the pipeline's status becomes the assignment's, `set -e` kills the run HERE, and
  # every check below this loop (the whole agent-TLS auto-gen section) is silently
  # skipped while the script still prints nothing but OK lines.
  fixture_key=$(grep -E '^secretKey: ' "$fixture" 2>/dev/null \
    | head -1 \
    | sed -E 's/^secretKey: *//; s/^["'\'']?//; s/["'\'']?$//; s/ *#.*$//' || true)
  if [ -n "$fixture_key" ]; then
    if [ "${#fixture_key}" -eq 32 ]; then
      echo "OK:   $(basename "$fixture") secretKey is exactly 32 bytes (AES-256)"
    else
      echo "FAIL: $(basename "$fixture") secretKey is ${#fixture_key} bytes, want 32 — ParseKey would reject this and the recipe would break Connection management" >&2
      fail=1
    fi
  fi
done

# ---------------------------------------------------------------------------
# Zero-prereq agent TLS (#690). With NO cert-manager and NO pre-created Secret,
# `helm install` must still bring up TLS on the agent gRPC channel: the chart
# auto-generates a stable self-signed CA + server cert by default
# (agentTLS.autoGenerate=true). Render with ONLY the mandatory Pro datastore /
# key values — deliberately NOT setting agentTLS.serverCertSecret or
# agentTLS.caConfigMap — and assert the generated material renders and is wired.
# The release name here is "dexaflow-test", so the chart fullname (and thus the
# generated resource names) are prefixed with it.
AUTOGEN_RENDERED=$(helm template dexaflow-test "$CHART" \
  --set database.url='postgres://leoflow:p@db:5432/leoflow?sslmode=disable' \
  --set redis.url='redis://r:6379/0' \
  --set auth.jwtSecret='helm-template-check-jwt-fixture' \
  --set secretKey='leoflow-tmpl-check-secret-key-32' 2>&1 || true)

expect_in() { # haystack needle description
  if ! grep -qF -- "$2" <<<"$1"; then
    echo "FAIL: missing $3 ($2)" >&2
    fail=1
  else
    echo "OK:   $3"
  fi
}
refute_in() { # haystack needle description
  if grep -qF -- "$2" <<<"$1"; then
    echo "FAIL: unexpected $3 ($2)" >&2
    fail=1
  else
    echo "OK:   $3"
  fi
}

# The default render must NOT fail — that is the whole point of #690 (no
# cert-manager, no pre-created Secret required). A `fail` would surface as
# "Error:" in the captured stderr instead of a rendered Deployment.
refute_in "$AUTOGEN_RENDERED" 'Error:'      "no render error on the zero-prereq default path (#690)"
expect_in "$AUTOGEN_RENDERED" 'kind: Deployment' "the control-plane Deployment on the auto-gen default path"

# The auto-generated server cert Secret (kubernetes.io/tls) + its keys.
expect_in "$AUTOGEN_RENDERED" 'type: kubernetes.io/tls'      "auto-generated kubernetes.io/tls Secret"
expect_in "$AUTOGEN_RENDERED" 'name: dexaflow-test-agent-tls' "auto-gen server cert Secret named <fullname>-agent-tls"
expect_in "$AUTOGEN_RENDERED" 'tls.crt:'                     "tls.crt in the auto-gen Secret"
expect_in "$AUTOGEN_RENDERED" 'tls.key:'                     "tls.key in the auto-gen Secret"

# The auto-generated CA ConfigMap task pods mount to verify the server cert.
expect_in "$AUTOGEN_RENDERED" 'name: dexaflow-test-agent-ca'  "auto-gen CA ConfigMap named <fullname>-agent-ca"
expect_in "$AUTOGEN_RENDERED" 'ca.crt:'                      "ca.crt key in the auto-gen CA ConfigMap"

# The Deployment must WIRE the generated material: mount the generated Secret as
# the gRPC server cert volume and point the executor at the generated CA ConfigMap.
expect_in "$AUTOGEN_RENDERED" 'secretName: dexaflow-test-agent-tls' "Deployment mounts the auto-gen server cert Secret"
expect_in "$AUTOGEN_RENDERED" 'value: "dexaflow-test-agent-ca"'     "Deployment points the executor at the auto-gen CA ConfigMap"

# The generated tls.crt must be a real, parseable X.509 cert (not an empty or
# malformed PEM). Extract the single-line base64 Secret value, decode it, and
# let openssl parse it — a non-cert value makes `openssl x509` exit non-zero.
AUTOGEN_TLS_CRT=$(grep -E '^\s*tls\.crt:' <<<"$AUTOGEN_RENDERED" | head -1 | awk '{print $2}')
if [ -n "$AUTOGEN_TLS_CRT" ] && printf '%s' "$AUTOGEN_TLS_CRT" | base64 -d 2>/dev/null | openssl x509 -noout -subject >/dev/null 2>&1; then
  echo "OK:   auto-gen tls.crt is a valid, parseable X.509 certificate"
else
  echo "FAIL: auto-gen tls.crt is not a valid X.509 certificate (empty or malformed PEM)" >&2
  fail=1
fi

# BYO precedence (#280): when the operator supplies BOTH serverCertSecret and
# caConfigMap, the chart must use them verbatim and render NO auto-gen material.
# $RENDERED above is exactly that BYO shape.
refute_in "$RENDERED" 'type: kubernetes.io/tls'      "no auto-gen tls Secret rendered on the BYO path"
refute_in "$RENDERED" 'dexaflow-test-agent-tls'       "no auto-gen server cert Secret name on the BYO path"
refute_in "$RENDERED" 'dexaflow-test-agent-ca'        "no auto-gen CA ConfigMap name on the BYO path"
expect_in "$RENDERED" 'secretName: leoflow-agent-tls-fixture' "BYO server cert Secret mounted verbatim"

# ---------------------------------------------------------------------------
# OIDC / SSO (#1143). The chart exposed no auth.oidc.* key and mounted no server
# config file, so SSO was not reachable at all: auth.oidc.tenant_claims is a MAP,
# no LEOFLOW_AUTH_OIDC_* env var can carry one, and the server fails boot closed
# without it. The checks below cover the two halves of the fix and the one thing
# that must never travel with them.

# Half 1: OFF renders nothing. $RENDERED above is a default (non-SSO) install, and
# an operator upgrading onto this chart version without touching their values must
# get the pod spec they already had.
refute_in "$RENDERED" 'name: LEOFLOW_AUTH_PROVIDER' "no auth provider env on a non-SSO install"
refute_in "$RENDERED" 'name: LEOFLOW_CONFIG'        "no server config file env on a non-SSO install"
refute_in "$RENDERED" 'oidc-config'                 "no OIDC volume or ConfigMap on a non-SSO install"

# Half 2: ON delivers the scalars as env and the two maps as a mounted file. The
# dotted tenant key is the point of the file, viper's key delimiter is "." and
# would split a Google `hd` domain into nested maps (#826): so assert it survives
# the render quoted and intact.
OIDC_RENDERED=$(helm template dexaflow-test "$CHART" \
  --set database.url='postgres://leoflow:p@db:5432/leoflow?sslmode=disable' \
  --set redis.url='redis://r:6379/0' \
  --set auth.jwtSecret='helm-template-check-jwt-fixture' \
  --set agentTLS.serverCertSecret='leoflow-agent-tls-fixture' \
  --set agentTLS.caConfigMap='leoflow-agent-ca-fixture' \
  --set auth.oidc.enabled=true \
  --set auth.oidc.issuer='https://accounts.google.com' \
  --set auth.oidc.clientId='helm-template-check-client-id' \
  --set auth.oidc.clientSecret='helm-template-check-oidc-fixture' \
  --set auth.oidc.redirectUrl='https://leoflow.example.com/api/v2/auth/oidc/callback' \
  --set auth.oidc.tenantClaim=hd \
  --set 'auth.oidc.tenantClaims.example\.com=default')

expect_in "$OIDC_RENDERED" 'value: "oidc"'                     "auth provider set to oidc when SSO is on"
expect_in "$OIDC_RENDERED" 'value: "/etc/leoflow/oidc/config.yaml"' "LEOFLOW_CONFIG points at the mounted OIDC file"
expect_in "$OIDC_RENDERED" 'name: LEOFLOW_AUTH_OIDC_ISSUER'    "OIDC issuer env entry in deployment"
expect_in "$OIDC_RENDERED" 'name: LEOFLOW_AUTH_OIDC_TENANT_CLAIM' "OIDC tenant claim env entry in deployment"
expect_in "$OIDC_RENDERED" 'name: dexaflow-test-oidc'           "OIDC ConfigMap named <fullname>-oidc"
expect_in "$OIDC_RENDERED" 'mountPath: /etc/leoflow/oidc'      "Deployment mounts the OIDC ConfigMap"
expect_in "$OIDC_RENDERED" '"example.com": "default"'          "dotted tenant_claims key survives the render quoted (#826)"
expect_in "$OIDC_RENDERED" 'oidcClientSecret:'                 "IdP client secret in the chart-managed Secret"

# The one thing that must never travel in the ConfigMap. A ConfigMap is readable
# by anything with get on the namespace and is not encrypted at rest, so the
# code-exchange credential takes the jwtSecret route (secretKeyRef) instead. The
# needle is the fixture VALUE, not the key name: the key name legitimately appears
# in the Secret, and it is the value leaking into the ConfigMap that would matter.
OIDC_CONFIGMAP=$(helm template dexaflow-test "$CHART" \
  --set database.url='postgres://leoflow:p@db:5432/leoflow?sslmode=disable' \
  --set redis.url='redis://r:6379/0' \
  --set auth.jwtSecret='helm-template-check-jwt-fixture' \
  --set agentTLS.serverCertSecret='leoflow-agent-tls-fixture' \
  --set agentTLS.caConfigMap='leoflow-agent-ca-fixture' \
  --set auth.oidc.enabled=true \
  --set auth.oidc.issuer='https://accounts.google.com' \
  --set auth.oidc.clientId='helm-template-check-client-id' \
  --set auth.oidc.clientSecret='helm-template-check-oidc-fixture' \
  --set auth.oidc.redirectUrl='https://leoflow.example.com/api/v2/auth/oidc/callback' \
  --set auth.oidc.tenantClaim=hd \
  --set 'auth.oidc.tenantClaims.example\.com=default' \
  --show-only templates/oidc-config.yaml)
refute_in "$OIDC_CONFIGMAP" 'helm-template-check-oidc-fixture' "IdP client secret absent from the OIDC ConfigMap"

# The render guard. The server reports every missing OIDC key in one error because
# each boot failure on Kubernetes costs a values edit, an upgrade and a rollout to
# learn the next one; the chart owes the same one step earlier, where a GitOps sync
# can show it. An install missing the tenant pin must not render at all.
OIDC_NO_PIN=$(helm template dexaflow-test "$CHART" \
  --set database.url='postgres://leoflow:p@db:5432/leoflow?sslmode=disable' \
  --set redis.url='redis://r:6379/0' \
  --set auth.jwtSecret='helm-template-check-jwt-fixture' \
  --set agentTLS.serverCertSecret='leoflow-agent-tls-fixture' \
  --set agentTLS.caConfigMap='leoflow-agent-ca-fixture' \
  --set auth.oidc.enabled=true \
  --set auth.oidc.issuer='https://accounts.google.com' \
  --set auth.oidc.clientId='helm-template-check-client-id' \
  --set auth.oidc.redirectUrl='https://leoflow.example.com/api/v2/auth/oidc/callback' 2>&1 || true)
expect_in "$OIDC_NO_PIN" 'auth.oidc.tenantClaim, auth.oidc.tenantClaims' "render refused for an SSO install with no tenant pin"

if [ "$fail" -ne 0 ]; then
  echo
  echo "helm-template-checks: one or more contracts unmet (see FAIL lines above)" >&2
  exit 1
fi
echo
echo "helm-template-checks: all contracts met"
