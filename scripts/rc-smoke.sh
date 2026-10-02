#!/usr/bin/env bash
#
# RC pre-cut smoke battery — run on the release box before cutting an RC.
#
# Runs every mechanical gate that must be green before tagging a release
# candidate, collecting a PASS/FAIL for each (it does NOT stop at the first
# failure — the point is to see everything that is red in one pass). Exits
# non-zero if any step failed. The one thing it cannot do is the human UI check;
# it prints that reminder at the end.
#
# Usage:
#   bash scripts/rc-smoke.sh              # full battery (gates + k3d e2es, ~40-60 min)
#   SKIP_E2E=1 bash scripts/rc-smoke.sh   # gates only (fast, no k3d), ~5-10 min
#
# Requires (full run): docker, go, helm, helm unittest plugin, golangci-lint,
# ruff, k3d, kubectl, jq, node. A working `make dev-up` (Postgres + Redis).
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT" || exit 2

# Canonical --set flags the Pro chart's render guards require (external DB/Redis
# + agent TLS), so `helm lint`/template checks render instead of `fail`ing.
HELM_SET=(--set database.url=postgres://h/d --set redis.url=redis://h/0
  --set auth.jwtSecret=jwt --set agentTLS.serverCertSecret=tls --set agentTLS.caConfigMap=ca)

RESULTS=()
FAILED=0

bold() { printf '\033[1;34m==> %s\033[0m\n' "$*"; }
# run_step NAME CMD... — runs CMD, records PASS/FAIL, never aborts the battery.
run_step() {
  local name="$1"; shift
  bold "$name"
  if "$@"; then
    RESULTS+=("PASS  $name"); printf '\033[1;32m    PASS\033[0m %s\n' "$name"
  else
    RESULTS+=("FAIL  $name"); FAILED=1; printf '\033[1;31m    FAIL\033[0m %s\n' "$name"
  fi
}

# --- helpers wrapping the checks that need composition -----------------------
helm_lint()        { helm lint helm/dexaflow "${HELM_SET[@]}"; }
helm_docs_fresh()  {
  command -v helm-docs >/dev/null || { echo "helm-docs not installed"; return 1; }
  helm-docs -c helm/dexaflow >/dev/null 2>&1
  git diff --exit-code -- helm/dexaflow/README.md
}
ui_smoke() {
  # A real ui-smoke failure must surface as FAIL; only a MISSING node is skipped.
  if ! command -v node >/dev/null; then echo "node missing; skipping ui-smoke"; return 0; fi
  # The headless SPA smoke needs a running Lite control plane. Bring one up on the
  # migrated dev DB (setup -> bootstrap hash -> server), run the smoke against it
  # over IPv4 (localhost resolves to ::1 first; the server binds IPv4, so force
  # 127.0.0.1 to avoid a spurious connection refusal), then tear it down.
  local port=18080 base="http://127.0.0.1:18080"
  local db="postgres://leoflow:leoflow@localhost:5432/leoflow?sslmode=disable"
  local home_dir; home_dir="$(mktemp -d)"
  local pid="" rc=0 pw hash setup_out code=""
  # a no-op python3.11 so `setup` skips the CPython download (the parser is unused here)
  mkdir -p "$home_dir/bin"; printf '#!/bin/sh\n' > "$home_dir/bin/python3.11"; chmod +x "$home_dir/bin/python3.11"
  LEOFLOW_DATABASE_URL="$db" PATH="$home_dir/bin:$PATH" ./bin/leoflow db reset --yes >/dev/null 2>&1
  setup_out="$(HOME="$home_dir" PATH="$home_dir/bin:$PATH" ./bin/leoflow setup --workspace "$home_dir/ws" </dev/null 2>&1)"
  pw="$(printf '%s\n' "$setup_out" | sed -n 's/^[[:space:]]*password:[[:space:]]*//p' | head -1)"
  hash="$(sed -n 's/^admin_password_hash:[[:space:]]*"\(.*\)"/\1/p' "$home_dir/.dexaflow/config.yaml" 2>/dev/null)"
  if [ -z "$pw" ] || [ -z "$hash" ]; then echo "ui-smoke: setup did not yield admin creds"; rm -rf "$home_dir"; return 1; fi
  printf 'print("hello")\n' > "$home_dir/ws/dag.py"
  LEOFLOW_SERVER_HTTP_ADDR="127.0.0.1:${port}" LEOFLOW_SERVER_GRPC_ADDR="127.0.0.1:19091" \
  LEOFLOW_SERVER_METRICS_ADDR="127.0.0.1:19090" LEOFLOW_DATABASE_URL="$db" \
  LEOFLOW_REDIS_URL="redis://localhost:6379/0" LEOFLOW_AUTH_JWT_SECRET="e2e-insecure-jwt-secret-please-change" \
  LEOFLOW_SECRET_KEY="e2e-insecure-secret-key-32bytes!" LEOFLOW_BOOTSTRAP_PASSWORD_HASH="$hash" \
  LEOFLOW_BOOTSTRAP_EMAIL="admin@leoflow.local" LEOFLOW_UI_EDITION="lite" \
  LEOFLOW_UI_WORKSPACE="$home_dir/ws" LEOFLOW_LOGS_DIR="$home_dir/logs" \
    ./bin/leoflow-server >"$home_dir/server.log" 2>&1 &
  pid=$!
  for _ in $(seq 1 60); do
    code="$(curl -s -o /dev/null -w '%{http_code}' "${base}/readyz" || true)"
    [ "$code" = "200" ] && break
    kill -0 "$pid" 2>/dev/null || break
    sleep 0.5
  done
  if [ "$code" = "200" ]; then
    LEOFLOW_URL="$base" LEOFLOW_USER="admin@leoflow.local" LEOFLOW_PASS="$pw" node test/e2e/ui-smoke.js || rc=$?
  else
    echo "ui-smoke: lite control plane not ready (last /readyz=$code)"; tail -20 "$home_dir/server.log"; rc=1
  fi
  kill "$pid" 2>/dev/null || true
  chmod -R u+w "$home_dir" 2>/dev/null || true; rm -rf "$home_dir"
  return "$rc"
}

# --- preflight ---------------------------------------------------------------
bold "Preflight"
git rev-parse --abbrev-ref HEAD | grep -qx main || echo "  (warning: not on main)"
[ -z "$(git status --porcelain)" ] || echo "  (warning: working tree not clean)"
docker info >/dev/null 2>&1 || { echo "docker is not running — aborting"; exit 2; }

# --- prerequisites (must succeed; abort if not) ------------------------------
bold "Prerequisites: dev-up + build"
make dev-up   || { echo "make dev-up failed — aborting (need Postgres+Redis)"; exit 2; }
make build    || { echo "make build failed — aborting"; exit 2; }

# --- Go + Python gates -------------------------------------------------------
run_step "golangci-lint + ruff (make lint)"        make lint
run_step "unit tests + coverage floor (make test)" make test
run_step "integration tests (make test-integration)" make test-integration
run_step "govulncheck (make vuln)"                 make vuln

# --- Helm gates --------------------------------------------------------------
run_step "helm lint"                     helm_lint
run_step "helm unittest"                 helm unittest helm/dexaflow
run_step "helm-template-checks.sh"       bash scripts/helm-template-checks.sh
run_step "rbac-covers-executor.sh"       bash scripts/rbac-covers-executor.sh
run_step "check-dependabot-dirs.sh"      bash scripts/check-dependabot-dirs.sh
run_step "helm README fresh (helm-docs)" helm_docs_fresh
# Runs alongside the freshness check, not inside it: helm-docs renders a swallowed
# doc comment deterministically, so a broken README row SATISFIES helm_docs_fresh.
run_step "check-values-doc-comments.sh"  bash scripts/check-values-doc-comments.sh

# --- k3d end-to-end (slow; skippable) ----------------------------------------
if [ "${SKIP_E2E:-0}" = "1" ]; then
  bold "Skipping k3d e2es (SKIP_E2E=1)"
else
  run_step "e2e operators (make e2e)"        make e2e
  run_step "e2e split two-process (make e2e-split)" make e2e-split
  run_step "e2e dbt (make e2e-dbt)"          make e2e-dbt
  run_step "ui smoke (headless SPA)"         ui_smoke
fi

# --- summary -----------------------------------------------------------------
printf '\n\033[1m===== RC smoke summary =====\033[0m\n'
for r in "${RESULTS[@]}"; do
  case "$r" in FAIL*) printf '\033[1;31m%s\033[0m\n' "$r" ;; *) printf '\033[1;32m%s\033[0m\n' "$r" ;; esac
done

printf '\n\033[1mStill required by hand (this script cannot do it):\033[0m\n'
cat <<'MANUAL'
  - Lite (all role): `leoflow lite` (or the server with no role) → open the UI,
    trigger a DAG, confirm it runs and the dashboard/logs render.
  - Pro (split): `helm install ... --set split.enabled=true
    --set logs.persistence.accessMode=ReadWriteMany` on a real cluster → confirm
    the api and scheduler Deployments come up, a run dispatches, logs render.
MANUAL

if [ "$FAILED" -ne 0 ]; then
  printf '\n\033[1;31mRC smoke: FAILED — do not cut until green.\033[0m\n'; exit 1
fi
printf '\n\033[1;32mRC smoke: all mechanical gates PASS. Do the manual UI checks above, then cut.\033[0m\n'
