#!/usr/bin/env bash
# End-to-end gate for the managed-Postgres re-extract idempotency fix (#729).
#
# `leoflow lite --postgres managed` downloads a relocatable PostgreSQL under
# ~/.leoflow/postgres and extracts it. A re-run over an EXISTING install used to
# break two ways:
#   1. extractSymlink ended with os.Symlink, which fails EEXIST when the link
#      target already exists (the regular-file branch overwrites via O_TRUNC), so
#      only symlinked entries broke a re-extract over a non-empty postgres dir
#      (a partial/interrupted prior extract, or a cross-version layout change).
#   2. the presence guard keyed on the mere presence of bin/postgres, not its
#      version — so a clean upgrade silently kept the prior release's Postgres and
#      never re-extracted.
#
# This test boots managed Postgres, then simulates a cross-version on-disk state
# (older version sentinel) plus a pre-existing symlink, and asserts a second boot
# re-extracts idempotently (no EEXIST) and records the bundled version.
#
# Opt-in: it downloads a ~11 MB PostgreSQL from GitHub and needs a host where the
# relocatable build can run (glibc + ICU/Kerberos). Skipped unless enabled:
#   LEOFLOW_E2E_MANAGED_PG=1 bash test/e2e/lite-managed-pg-reextract.sh
set -euo pipefail

if [ "${LEOFLOW_E2E_MANAGED_PG:-0}" != "1" ]; then
  echo "SKIP lite-managed-pg-reextract: set LEOFLOW_E2E_MANAGED_PG=1 to run (needs network + a host that can run the managed PG)."
  exit 0
fi

PORT="${LEOFLOW_E2E_PORT:-18098}"
BASE="http://127.0.0.1:${PORT}"
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
TMP="$(mktemp -d)"
HOME_DIR="$TMP/home"          # sandbox ~/.leoflow so we never touch the real one
WS="$TMP/ws"                  # empty workspace: no DAGs => no venv installs
LEO_HOME="$HOME_DIR/.leoflow"
PG_DIR="$LEO_HOME/postgres"
LITE_PID=""

pass() { printf '  \033[32mPASS\033[0m %s\n' "$1"; }
fail() { printf '  \033[31mFAIL\033[0m %s\n' "$1"; [ -n "${2:-}" ] && tail -60 "$2"; cleanup; exit 1; }
cleanup() {
  [ -n "$LITE_PID" ] && kill "$LITE_PID" 2>/dev/null || true
  # stop any managed PG started under the sandbox home
  if [ -x "$PG_DIR/bin/pg_ctl" ] && [ -d "$LEO_HOME/pgdata" ]; then
    "$PG_DIR/bin/pg_ctl" -D "$LEO_HOME/pgdata" stop -m fast >/dev/null 2>&1 || true
  fi
  chmod -R u+w "$TMP" 2>/dev/null || true
  rm -rf "$TMP"
}
trap cleanup EXIT

export HOME="$HOME_DIR"
export LEOFLOW_AUTH_JWT_SECRET="e2e-insecure-jwt-secret-please-change"
export LEOFLOW_SECRET_KEY="e2e-insecure-secret-key-32bytes!"
export LEOFLOW_LOGS_DIR="$TMP/logs"
export PYTHONPATH="${PYTHONPATH:-$ROOT/parser}"
mkdir -p "$HOME_DIR" "$WS" "$TMP/logs"

echo "==> building binaries"
go build -o "$TMP/leoflow" ./cmd/dexaflow
go build -o "$TMP/leoflow-server" ./cmd/dexaflow-server
go build -o "$TMP/leoflow-agent" ./cmd/dexaflow-agent
export PATH="$TMP:$PATH"

start_lite() { # $1=logfile
  "$TMP/leoflow" lite --postgres managed --executor subprocess --port "$PORT" "$WS" >"$1" 2>&1 &
  LITE_PID=$!
  for _ in $(seq 1 "${LITE_READY_TRIES:-300}"); do
    curl -fsS "${BASE}/readyz" >/dev/null 2>&1 && return 0
    kill -0 "$LITE_PID" 2>/dev/null || { tail -60 "$1"; return 1; }
    sleep 1
  done
  return 1
}
stop_lite() {
  [ -n "$LITE_PID" ] && kill "$LITE_PID" 2>/dev/null || true
  wait "$LITE_PID" 2>/dev/null || true
  LITE_PID=""
  if [ -x "$PG_DIR/bin/pg_ctl" ] && [ -d "$LEO_HOME/pgdata" ]; then
    "$PG_DIR/bin/pg_ctl" -D "$LEO_HOME/pgdata" stop -m fast >/dev/null 2>&1 || true
  fi
  sleep 2
}

echo "==> session 1: first boot installs + extracts the managed Postgres"
start_lite "$TMP/s1.log" || fail "session 1 did not become ready" "$TMP/s1.log"
[ -x "$PG_DIR/bin/postgres" ] || fail "managed postgres binary was not extracted" "$TMP/s1.log"
[ -f "$PG_DIR/.pg-version" ] || fail "managed PG version sentinel was not recorded" "$TMP/s1.log"
BUNDLED="$(cat "$PG_DIR/.pg-version")"
pass "session 1 extracted managed PostgreSQL ($BUNDLED)"
stop_lite

echo "==> simulate a cross-version / partial prior extract"
# (a) older version on disk -> the guard must re-extract on the next boot
printf '15.0.0' > "$PG_DIR/.pg-version"
# (b) a pre-existing symlink that the re-extract will overwrite: the old
#     os.Symlink would fail EEXIST here.
ln -sf postgres "$PG_DIR/bin/leoflow-eexist-probe"
pass "planted stale version sentinel + pre-existing symlink"

echo "==> session 2: second boot must re-extract idempotently (no EEXIST)"
start_lite "$TMP/s2.log" || fail "session 2 did not become ready (re-extract likely failed with EEXIST)" "$TMP/s2.log"
[ -x "$PG_DIR/bin/postgres" ] || fail "managed postgres binary missing after re-extract" "$TMP/s2.log"
GOT="$(cat "$PG_DIR/.pg-version")"
[ "$GOT" = "$BUNDLED" ] || fail "version sentinel not refreshed on upgrade: got '$GOT', want '$BUNDLED'" "$TMP/s2.log"
pass "session 2 re-extracted cleanly and refreshed the version sentinel to $GOT"
stop_lite

echo
echo "  ✅ Managed-Postgres re-extract verified: idempotent symlink extraction + version-aware guard (#729)."
