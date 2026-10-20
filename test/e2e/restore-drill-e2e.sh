#!/usr/bin/env bash
# End-to-end restore drill (#1995, ADR 0090): a logical backup restored into an
# emptied database brings the control plane back ready, with the same admin
# able to sign in. A backup that was never restored does not count.
#
# Needs a local Postgres and Redis (docker-compose.dev.yaml) and the
# golang-migrate CLI. DESTRUCTIVE: drops the public schema of DB_URL.
# Run from the repo root:  bash test/e2e/restore-drill-e2e.sh
set -euo pipefail
export PGOPTIONS="${PGOPTIONS:---client-min-messages=warning}"

PORT="${LEOFLOW_E2E_HTTP_PORT:-18096}"
DB_URL="${LEOFLOW_E2E_DATABASE_URL:-postgres://leoflow:leoflow@localhost:5432/leoflow_dev?sslmode=disable}"
REDIS_URL="${LEOFLOW_E2E_REDIS_URL:-redis://localhost:6379/0}"
BASE="http://127.0.0.1:${PORT}"
HOME_DIR="$(mktemp -d)"
SERVER_PID=""

pass() { printf '  \033[32mPASS\033[0m %s\n' "$1"; }
die() { printf '  \033[31mFAIL\033[0m %s\n' "$1"; exit 1; }
stop_server() {
  if [ -n "$SERVER_PID" ]; then
    kill "$SERVER_PID" 2>/dev/null || true
    wait "$SERVER_PID" 2>/dev/null || true
    SERVER_PID=""
  fi
}
cleanup() {
  stop_server
  chmod -R u+w "$HOME_DIR" 2>/dev/null || true
  rm -rf "$HOME_DIR"
}
trap cleanup EXIT

command -v migrate >/dev/null || die "the golang-migrate CLI is required (go install -tags postgres github.com/golang-migrate/migrate/v4/cmd/migrate@v4.20.1)"
command -v pg_dump >/dev/null && command -v psql >/dev/null || die "pg_dump and psql are required"

echo "==> building binaries"
go build -o "$HOME_DIR/dexaflow" ./cmd/dexaflow || die "building dexaflow"
go build -o "$HOME_DIR/dexaflow-server" ./cmd/dexaflow-server || die "building dexaflow-server"

mkdir -p "$HOME_DIR/bin" "$HOME_DIR/ws"
# Setup takes a host interpreter only on the version it reports (#1700).
printf '#!/bin/sh\necho "Python 3.11.15"\n' > "$HOME_DIR/bin/python3.11"
chmod +x "$HOME_DIR/bin/python3.11"
export PATH="$HOME_DIR/bin:$PATH"

echo "==> migrating a clean database"
# Not `dexaflow db reset`: it builds its own DSN from Lite's managed-datastore
# convention and ignores DB_URL (see lite-db-outage.sh).
psql -q "$DB_URL" -c "DROP SCHEMA public CASCADE; CREATE SCHEMA public;" >/dev/null || die "emptying $DB_URL"
migrate -path migrations -database "$DB_URL" up >/dev/null 2>&1 || die "applying migrations to $DB_URL"

echo "==> dexaflow setup"
SETUP_OUT="$(HOME="$HOME_DIR" LEOFLOW_DATABASE_URL="$DB_URL" "$HOME_DIR/dexaflow" setup --workspace "$HOME_DIR/ws" </dev/null 2>&1)"
PW="$(printf '%s\n' "$SETUP_OUT" | sed -n 's/^[[:space:]]*password:[[:space:]]*//p' | head -1)"
HASH="$(sed -n 's/^admin_password_hash:[[:space:]]*"\(.*\)"/\1/p' "$HOME_DIR/.dexaflow/config.yaml")"
EMAIL="$(sed -n 's/^admin_email:[[:space:]]*"\(.*\)"/\1/p' "$HOME_DIR/.dexaflow/config.yaml")"
[ -n "$PW" ] && [ -n "$HASH" ] && [ -n "$EMAIL" ] || die "setup did not produce an admin email/password/hash"

start_server() {
  LEOFLOW_SERVER_HTTP_ADDR="127.0.0.1:${PORT}" \
  LEOFLOW_SERVER_GRPC_ADDR=":$((PORT+1000))" \
  LEOFLOW_SERVER_METRICS_ADDR=":$((PORT+1001))" \
  LEOFLOW_DATABASE_URL="$DB_URL" \
  LEOFLOW_REDIS_URL="$REDIS_URL" \
  LEOFLOW_AUTH_JWT_SECRET="e2e-insecure-jwt-secret-please-change" \
  LEOFLOW_SECRET_KEY="e2e-insecure-secret-key-32bytes!" \
  LEOFLOW_BOOTSTRAP_PASSWORD_HASH="$HASH" \
  LEOFLOW_BOOTSTRAP_EMAIL="$EMAIL" \
  LEOFLOW_EXECUTOR_TYPE="subprocess" \
  LEOFLOW_LOGS_DIR="$HOME_DIR/logs" \
    "$HOME_DIR/dexaflow-server" >"$HOME_DIR/$1.log" 2>&1 &
  SERVER_PID=$!
  for _ in $(seq 1 60); do
    [ "$(curl -s -o /dev/null -w '%{http_code}' "${BASE}/readyz" || true)" = "200" ] && return 0
    kill -0 "$SERVER_PID" 2>/dev/null || die "server exited early:
$(cat "$HOME_DIR/$1.log")"
    sleep 0.5
  done
  die "server never became ready:
$(tail -20 "$HOME_DIR/$1.log")"
}

login() {
  local token
  token="$(curl -s -X POST "${BASE}/auth/token" -H 'Content-Type: application/json' \
    -d "{\"username\":\"${EMAIL}\",\"password\":\"${PW}\"}" | sed -n 's/.*"access_token":"\([^"]*\)".*/\1/p')"
  [ -n "$token" ] || return 1
  [ "$(curl -s -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $token" "${BASE}/api/v2/dags")" = "200" ]
}

echo "==> baseline: the control plane is ready and the admin signs in"
start_server baseline
login || die "baseline login failed"
pass "baseline control plane is ready and the admin signs in"
# A row only the original database has: the restore must bring it back.
psql -q "$DB_URL" -c "INSERT INTO variables (tenant_id, key, value) SELECT id, 'restore_drill_marker', 'kept' FROM tenants LIMIT 1" >/dev/null \
  || die "writing the marker variable"
stop_server

echo "==> taking a logical backup"
BACKUP_SQL="$HOME_DIR/backup.sql"
pg_dump --clean --if-exists -d "$DB_URL" > "$BACKUP_SQL" || die "pg_dump failed"
[ -s "$BACKUP_SQL" ] || die "pg_dump produced an empty backup"
pass "backup captured ($(wc -c < "$BACKUP_SQL") bytes)"

echo "==> emptying the database (the disaster)"
psql -q "$DB_URL" -c "DROP SCHEMA public CASCADE; CREATE SCHEMA public;" >/dev/null || die "emptying the database"

echo "==> restoring the backup"
psql -q -v ON_ERROR_STOP=1 "$DB_URL" < "$BACKUP_SQL" >/dev/null || die "restoring the backup"
[ "$(psql -tA "$DB_URL" -c "SELECT value FROM variables WHERE key = 'restore_drill_marker'")" = "kept" ] \
  || die "the restored database lacks the marker variable"
pass "backup restored into the empty database, marker row present"

echo "==> the control plane comes back on the restored database"
start_server restored
login || die "login failed on the restored database"
pass "server ready on the restored database and the admin signs in"

echo ""
echo "=== restore drill passed ==="
