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
#
# Postgres: the battery used to assume localhost:5432 everywhere and reset a
# database there, so on a machine where another project owns 5432 it could
# wipe that project's data. One knob now moves all of it:
#   DEXAFLOW_DEV_DB_PORT=55432 bash scripts/rc-smoke.sh
# starts the dev Postgres on that port and points the migrations, the
# integration tests, every k3d e2e control plane and the ui smoke at it
# (LEOFLOW_DEV_DB_PORT is the older name). DEXAFLOW_E2E_DATABASE_URL (or
# LEOFLOW_E2E_DATABASE_URL) names the control plane's database explicitly, and
# TEST_DATABASE_URL the integration tests' one. The ui smoke resets only a
# database on the Postgres `make dev-up` published on that port, never one
# named by an explicit URL.
#
#   bash scripts/rc-smoke.sh --self-test  # offline check of the knobs and the reset guard
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT" || exit 2

# Canonical --set flags the Pro chart's render guards require (external DB/Redis
# + agent TLS), so `helm lint`/template checks render instead of `fail`ing.
HELM_SET=(--set database.url=postgres://h/d --set redis.url=redis://h/0
  --set auth.jwtSecret=jwt --set agentTLS.serverCertSecret=tls --set agentTLS.caConfigMap=ca)

RESULTS=()
FAILED=0

# --- Postgres knobs ----------------------------------------------------------
# resolve_db sets RC_DB_PORT (the host port of the dev Postgres), RC_DB_URL (the
# control plane database of the e2es), RC_DB_EXPLICIT (1 when the user named
# that URL) and RC_TEST_DB_URL from the environment. DEXAFLOW_* wins over the
# LEOFLOW_* name it replaced.
resolve_db() {
  RC_DB_PORT="${DEXAFLOW_DEV_DB_PORT:-${LEOFLOW_DEV_DB_PORT:-5432}}"
  case "$RC_DB_PORT" in
    ''|*[!0-9]*) echo "DEXAFLOW_DEV_DB_PORT must be a port number, got '$RC_DB_PORT'" >&2; return 1 ;;
  esac
  if [ "$RC_DB_PORT" -lt 1 ] || [ "$RC_DB_PORT" -gt 65535 ]; then
    echo "DEXAFLOW_DEV_DB_PORT must be between 1 and 65535, got '$RC_DB_PORT'" >&2; return 1
  fi
  RC_DB_URL="${DEXAFLOW_E2E_DATABASE_URL:-${LEOFLOW_E2E_DATABASE_URL:-}}"
  RC_DB_EXPLICIT=0
  if [ -n "$RC_DB_URL" ]; then
    RC_DB_EXPLICIT=1
  else
    RC_DB_URL="postgres://leoflow:leoflow@localhost:${RC_DB_PORT}/leoflow?sslmode=disable"
  fi
  RC_TEST_DB_URL="${TEST_DATABASE_URL:-postgres://leoflow:leoflow@localhost:${RC_DB_PORT}/leoflow_test?sslmode=disable}"
}

# reset_guard EXPLICIT PORT PUBLISHED decides whether the ui smoke may run
# `db reset --yes`, which drops `leoflow_dev` on localhost:PORT. PUBLISHED is
# what `docker compose port postgres 5432` printed for this checkout's dev
# Postgres. Returns 0 to reset, 1 to use an explicit URL without resetting it,
# 2 to refuse: something other than the Postgres `make dev-up` started answers
# on PORT, and a reset there would drop another project's database.
reset_guard() {
  local explicit="$1" port="$2" published="$3" line
  if [ "$explicit" = "1" ]; then
    echo "explicit DEXAFLOW_E2E_DATABASE_URL: using it as is, not resetting a database this script did not start"
    return 1
  fi
  while IFS= read -r line; do
    case "$line" in *":$port") return 0 ;; esac
  done <<< "$published"
  echo "refusing to reset: the dev Postgres of this checkout does not publish port $port (it publishes '${published//$'\n'/ }'); something else answers there"
  return 2
}

# with_db CMD... runs CMD with the control plane database of the e2es exported
# under every name the server and the e2e scripts read.
with_db() {
  DEXAFLOW_DATABASE_URL="$RC_DB_URL" LEOFLOW_DATABASE_URL="$RC_DB_URL" \
  DEXAFLOW_E2E_DATABASE_URL="$RC_DB_URL" LEOFLOW_E2E_DATABASE_URL="$RC_DB_URL" "$@"
}

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
  local db guard=0
  local home_dir; home_dir="$(mktemp -d)"
  local pid="" rc=0 pw hash email setup_out code=""
  # a fake python3.11 so `setup` skips the CPython download (the parser is unused
  # here); setup takes a host interpreter only on the version it reports (#1700)
  mkdir -p "$home_dir/bin"; printf '#!/bin/sh\necho "Python 3.11.15"\n' > "$home_dir/bin/python3.11"; chmod +x "$home_dir/bin/python3.11"
  # `db reset` ignores LEOFLOW_DATABASE_URL (#1185): it drops `leoflow_dev` on
  # the port in <HOME>/.dexaflow/dev/db-port. Run under the throwaway HOME with
  # that file naming the dev Postgres, never the user's own HOME, whose Lite
  # database it used to drop.
  reset_guard "$RC_DB_EXPLICIT" "$RC_DB_PORT" "$(docker compose port postgres 5432 2>/dev/null)" || guard=$?
  case "$guard" in
    0)
      db="postgres://leoflow:leoflow@localhost:${RC_DB_PORT}/leoflow_dev?sslmode=disable"
      mkdir -p "$home_dir/.dexaflow/dev"; printf '%s' "$RC_DB_PORT" > "$home_dir/.dexaflow/dev/db-port"
      if ! HOME="$home_dir" PATH="$home_dir/bin:$PATH" ./bin/leoflow db reset --yes >"$home_dir/db-reset.log" 2>&1; then
        echo "ui-smoke: db reset failed"; tail -20 "$home_dir/db-reset.log"; rm -rf "$home_dir"; return 1
      fi ;;
    1) db="$RC_DB_URL" ;;
    *) rm -rf "$home_dir"; return 1 ;;
  esac
  setup_out="$(HOME="$home_dir" PATH="$home_dir/bin:$PATH" ./bin/dexaflow setup --workspace "$home_dir/ws" </dev/null 2>&1)"
  pw="$(printf '%s\n' "$setup_out" | sed -n 's/^[[:space:]]*password:[[:space:]]*//p' | head -1)"
  hash="$(sed -n 's/^admin_password_hash:[[:space:]]*"\(.*\)"/\1/p' "$home_dir/.dexaflow/config.yaml" 2>/dev/null)"
  email="$(sed -n 's/^admin_email:[[:space:]]*"\(.*\)"/\1/p' "$home_dir/.dexaflow/config.yaml" 2>/dev/null)"
  if [ -z "$pw" ] || [ -z "$hash" ] || [ -z "$email" ]; then echo "ui-smoke: setup did not yield admin creds"; rm -rf "$home_dir"; return 1; fi
  printf 'print("hello")\n' > "$home_dir/ws/dag.py"
  LEOFLOW_SERVER_HTTP_ADDR="127.0.0.1:${port}" LEOFLOW_SERVER_GRPC_ADDR="127.0.0.1:19091" \
  LEOFLOW_SERVER_METRICS_ADDR="127.0.0.1:19090" LEOFLOW_DATABASE_URL="$db" DEXAFLOW_DATABASE_URL="$db" \
  LEOFLOW_REDIS_URL="redis://localhost:6379/0" LEOFLOW_AUTH_JWT_SECRET="e2e-insecure-jwt-secret-please-change" \
  LEOFLOW_SECRET_KEY="e2e-insecure-secret-key-32bytes!" LEOFLOW_BOOTSTRAP_PASSWORD_HASH="$hash" \
  LEOFLOW_BOOTSTRAP_EMAIL="$email" LEOFLOW_UI_EDITION="lite" \
  LEOFLOW_UI_WORKSPACE="$home_dir/ws" LEOFLOW_LOGS_DIR="$home_dir/logs" \
  LEOFLOW_EXECUTOR_TYPE=subprocess \
    ./bin/leoflow-server >"$home_dir/server.log" 2>&1 &
  pid=$!
  for _ in $(seq 1 60); do
    code="$(curl -s -o /dev/null -w '%{http_code}' "${base}/readyz" || true)"
    [ "$code" = "200" ] && break
    kill -0 "$pid" 2>/dev/null || break
    sleep 0.5
  done
  if [ "$code" = "200" ]; then
    LEOFLOW_URL="$base" LEOFLOW_USER="$email" LEOFLOW_PASS="$pw" node test/e2e/ui-smoke.js || rc=$?
  else
    echo "ui-smoke: lite control plane not ready (last /readyz=$code)"; tail -20 "$home_dir/server.log"; rc=1
  fi
  kill "$pid" 2>/dev/null || true
  chmod -R u+w "$home_dir" 2>/dev/null || true; rm -rf "$home_dir"
  return "$rc"
}

self_test() {
  local fails=0 got
  _expect() { # NAME WANT GOT
    if [ "$2" = "$3" ]; then echo "ok   $1"; else echo "FAIL $1: want '$2', got '$3'"; fails=$((fails + 1)); fi
  }
  _resolve() { # VAR=VALUE... ; prints PORT|URL|EXPLICIT|TEST_URL, or ERR
    ( unset DEXAFLOW_DEV_DB_PORT LEOFLOW_DEV_DB_PORT DEXAFLOW_E2E_DATABASE_URL LEOFLOW_E2E_DATABASE_URL TEST_DATABASE_URL
      for kv in "$@"; do export "${kv?}"; done
      resolve_db 2>/dev/null || { echo ERR; exit 0; }
      echo "$RC_DB_PORT|$RC_DB_URL|$RC_DB_EXPLICIT|$RC_TEST_DB_URL" )
  }
  local u='postgres://leoflow:leoflow@localhost'
  _expect "default is today's localhost:5432" \
    "5432|$u:5432/leoflow?sslmode=disable|0|$u:5432/leoflow_test?sslmode=disable" "$(_resolve)"
  _expect "DEXAFLOW_DEV_DB_PORT moves both URLs" \
    "55432|$u:55432/leoflow?sslmode=disable|0|$u:55432/leoflow_test?sslmode=disable" "$(_resolve DEXAFLOW_DEV_DB_PORT=55432)"
  _expect "LEOFLOW_DEV_DB_PORT still works" "55432" "$(_resolve LEOFLOW_DEV_DB_PORT=55432 | cut -d'|' -f1)"
  _expect "DEXAFLOW_DEV_DB_PORT wins over LEOFLOW_DEV_DB_PORT" "55432" \
    "$(_resolve DEXAFLOW_DEV_DB_PORT=55432 LEOFLOW_DEV_DB_PORT=6000 | cut -d'|' -f1)"
  _expect "a non-numeric port is refused" "ERR" "$(_resolve DEXAFLOW_DEV_DB_PORT=55a32)"
  _expect "port 0 is refused" "ERR" "$(_resolve DEXAFLOW_DEV_DB_PORT=0)"
  _expect "a port above 65535 is refused" "ERR" "$(_resolve DEXAFLOW_DEV_DB_PORT=70000)"
  _expect "DEXAFLOW_E2E_DATABASE_URL is explicit" "postgres://u:p@db:6543/x|1" \
    "$(_resolve DEXAFLOW_E2E_DATABASE_URL=postgres://u:p@db:6543/x | cut -d'|' -f2,3)"
  _expect "LEOFLOW_E2E_DATABASE_URL still works" "postgres://u:p@db:6543/x|1" \
    "$(_resolve LEOFLOW_E2E_DATABASE_URL=postgres://u:p@db:6543/x | cut -d'|' -f2,3)"
  _expect "DEXAFLOW_E2E_DATABASE_URL wins" "postgres://new|1" \
    "$(_resolve DEXAFLOW_E2E_DATABASE_URL=postgres://new LEOFLOW_E2E_DATABASE_URL=postgres://old | cut -d'|' -f2,3)"
  _expect "TEST_DATABASE_URL is honored" "postgres://t" "$(_resolve TEST_DATABASE_URL=postgres://t | cut -d'|' -f4)"

  _guard() { local rc=0; reset_guard "$@" >/dev/null || rc=$?; echo "$rc"; }
  _expect "reset when the dev Postgres publishes the port" 0 "$(_guard 0 55432 '0.0.0.0:55432')"
  _expect "reset when one of IPv4/IPv6 publishes the port" 0 "$(_guard 0 5432 $'0.0.0.0:5432\n[::]:5432')"
  _expect "refuse when nothing of ours publishes it" 2 "$(_guard 0 5432 '')"
  _expect "refuse when ours publishes another port" 2 "$(_guard 0 5432 '0.0.0.0:55432')"
  _expect "refuse on a suffix that is not the port" 2 "$(_guard 0 5432 '0.0.0.0:15432')"
  _expect "an explicit URL is used, never reset" 1 "$(_guard 1 5432 '0.0.0.0:5432')"

  # with_db must hand the URL to every name the server and the e2es read.
  # shellcheck disable=SC2016 # expanded by the child shell, on purpose
  got="$(RC_DB_URL=postgres://x with_db sh -c 'echo "$DEXAFLOW_DATABASE_URL $LEOFLOW_DATABASE_URL $DEXAFLOW_E2E_DATABASE_URL $LEOFLOW_E2E_DATABASE_URL"')"
  _expect "with_db exports every database name" "postgres://x postgres://x postgres://x postgres://x" "$got"

  [ "$fails" -eq 0 ] || { echo "rc-smoke self-test: $fails failure(s)"; return 1; }
  echo "rc-smoke self-test: OK"
}

if [ "${1:-}" = "--self-test" ]; then
  self_test; exit $?
fi

# --- preflight ---------------------------------------------------------------
bold "Preflight"
git rev-parse --abbrev-ref HEAD | grep -qx main || echo "  (warning: not on main)"
[ -z "$(git status --porcelain)" ] || echo "  (warning: working tree not clean)"
docker info >/dev/null 2>&1 || { echo "docker is not running — aborting"; exit 2; }
resolve_db || exit 2
export DEXAFLOW_DEV_DB_PORT="$RC_DB_PORT" LEOFLOW_DEV_DB_PORT="$RC_DB_PORT"
if [ "$RC_DB_EXPLICIT" = "1" ]; then
  echo "  dev Postgres on port $RC_DB_PORT; control plane database from DEXAFLOW_E2E_DATABASE_URL"
else
  echo "  Postgres on localhost:$RC_DB_PORT (set DEXAFLOW_DEV_DB_PORT to move it)"
fi

# --- prerequisites (must succeed; abort if not) ------------------------------
bold "Prerequisites: dev-up + build"
make DATABASE_URL="$RC_DB_URL" dev-up || { echo "make dev-up failed, aborting (need Postgres+Redis)"; exit 2; }
make build    || { echo "make build failed — aborting"; exit 2; }

# --- Go + Python gates -------------------------------------------------------
run_step "golangci-lint + ruff (make lint)"        make lint
run_step "unit tests + coverage floor (make test)" make test
run_step "integration tests (make test-integration)" make TEST_DATABASE_URL="$RC_TEST_DB_URL" test-integration
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
  run_step "e2e operators (make e2e)"        with_db make e2e
  run_step "e2e split two-process (make e2e-split)" with_db make e2e-split
  run_step "e2e dbt (make e2e-dbt)"          with_db make e2e-dbt
  run_step "e2e source mode (make e2e-source-mode)" with_db make e2e-source-mode
  run_step "e2e warm pools (make e2e-warm-pools)" with_db make e2e-warm-pools
  # Linux only: its CLI container shares the host network. Elsewhere the rc
  # tag's run of e2e-daemonless-deploy.yaml is the record.
  if [ "$(uname -s)" = Linux ]; then
    run_step "e2e daemonless deploy (make e2e-daemonless-deploy)" with_db make e2e-daemonless-deploy
  else
    bold "Skipping the daemonless deploy e2e (Linux only; see .github/workflows/e2e-daemonless-deploy.yaml)"
  fi
  run_step "ui smoke (headless SPA)"         ui_smoke
fi

# --- summary -----------------------------------------------------------------
printf '\n\033[1m===== RC smoke summary =====\033[0m\n'
for r in "${RESULTS[@]}"; do
  case "$r" in FAIL*) printf '\033[1;31m%s\033[0m\n' "$r" ;; *) printf '\033[1;32m%s\033[0m\n' "$r" ;; esac
done

printf '\n\033[1mStill required by hand (this script cannot do it):\033[0m\n'
cat <<'MANUAL'
  - Lite (all role): `dexaflow lite` (or the server with no role) → open the UI,
    trigger a DAG, confirm it runs and the dashboard/logs render.
  - Pro (split): `helm install ... --set split.enabled=true
    --set logs.persistence.accessMode=ReadWriteMany` on a real cluster → confirm
    the api and scheduler Deployments come up, a run dispatches, logs render.
MANUAL

if [ "$FAILED" -ne 0 ]; then
  printf '\n\033[1;31mRC smoke: FAILED — do not cut until green.\033[0m\n'; exit 1
fi
printf '\n\033[1;32mRC smoke: all mechanical gates PASS. Do the manual UI checks above, then cut.\033[0m\n'
