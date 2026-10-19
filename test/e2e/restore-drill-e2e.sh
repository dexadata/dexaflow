#!/usr/bin/env bash
# End-to-end restore drill (#1995, ADR 0090):
# Verifies that a backup can be restored into an empty database and that the
# control plane boots cleanly and becomes ready on the restored datastore.
#
# A backup that was never restored does not count.
#
# Requires local Postgres + Redis (e.g. docker-compose.dev.yaml).
# DESTRUCTIVE: resets the leoflow_dev database.
# Run from the repo root:  bash test/e2e/restore-drill-e2e.sh
set -euo pipefail

PORT=18096
DB_URL="${LEOFLOW_E2E_DATABASE_URL:-postgres://leoflow:leoflow@localhost:5432/leoflow_dev?sslmode=disable}"
REDIS_URL="${LEOFLOW_E2E_REDIS_URL:-redis://localhost:6379/0}"
BASE="http://127.0.0.1:${PORT}"
HOME_DIR="$(mktemp -d)"
SERVER_PID=""

pass() { printf '  \033[32mPASS\033[0m %s\n' "$1"; }
fail() { printf '  \033[31mFAIL\033[0m %s\n' "$1"; cleanup; exit 1; }
cleanup() {
  if [ -n "$SERVER_PID" ]; then
    kill "$SERVER_PID" 2>/dev/null || true
  fi
  chmod -R u+w "$HOME_DIR" 2>/dev/null || true
  rm -rf "$HOME_DIR"
}
trap cleanup EXIT

echo "==> [Restore Drill] Step 1: building binaries"
go build -o "$HOME_DIR/dexaflow" ./cmd/dexaflow
go build -o "$HOME_DIR/dexaflow-server" ./cmd/dexaflow-server

mkdir -p "$HOME_DIR/bin"
printf '#!/bin/sh\necho "Python 3.12.4"\n' > "$HOME_DIR/bin/python3.12"
chmod +x "$HOME_DIR/bin/python3.12"
export PATH="$HOME_DIR/bin:$PATH"

echo "==> [Restore Drill] Step 2: resetting database and setting up control plane"
"$HOME_DIR/dexaflow" db reset --yes >/dev/null

mkdir -p "$HOME_DIR/ws"
printf 'print("drill dag")\n' > "$HOME_DIR/ws/dag.py"
SETUP_OUT="$(HOME="$HOME_DIR" "$HOME_DIR/dexaflow" setup --workspace "$HOME_DIR/ws" </dev/null 2>&1)"
PW="$(printf '%s\n' "$SETUP_OUT" | sed -n 's/^[[:space:]]*password:[[:space:]]*//p' | head -1)"
[ -n "$PW" ] || fail "setup did not emit an admin password"

JWT_SECRET="$(sed -n 's/^jwt_secret:[[:space:]]*"\(.*\)"/\1/p' "$HOME_DIR/.dexaflow/config.yaml")"
SECRET_KEY="$(sed -n 's/^secret_key:[[:space:]]*"\(.*\)"/\1/p' "$HOME_DIR/.dexaflow/config.yaml")"
[ -n "$JWT_SECRET" ] || fail "setup did not generate a jwt_secret"
[ -n "$SECRET_KEY" ] || fail "setup did not generate a secret_key"

echo "==> [Restore Drill] Step 3: starting initial server to verify baseline readiness"
LEOFLOW_SERVER_HTTP_ADDR="127.0.0.1:${PORT}" \
LEOFLOW_SERVER_GRPC_ADDR=":19096" \
LEOFLOW_SERVER_METRICS_ADDR=":19095" \
LEOFLOW_DATABASE_URL="$DB_URL" \
LEOFLOW_REDIS_URL="$REDIS_URL" \
LEOFLOW_AUTH_JWT_SECRET="$JWT_SECRET" \
LEOFLOW_SECRET_KEY="$SECRET_KEY" \
LEOFLOW_CONFIG_DIR="$HOME_DIR/.dexaflow" \
"$HOME_DIR/dexaflow-server" >"$HOME_DIR/server.log" 2>&1 &
SERVER_PID=$!

READY=0
for _ in $(seq 1 30); do
  if curl -fsS "${BASE}/readyz" >/dev/null 2>&1; then
    READY=1
    break
  fi
  sleep 0.5
done
[ "$READY" -eq 1 ] || { cat "$HOME_DIR/server.log"; fail "initial server failed to reach ready state"; }
pass "initial control plane is ready on database"

# Authenticate once to confirm admin credentials work on initial database
LOGIN_RESP="$(curl -fsS -X POST "${BASE}/api/v2/auth/token" \
  -H "Content-Type: application/json" \
  -d "{\"username\":\"admin@dexaflow.local\",\"password\":\"${PW}\"}")"
TOKEN="$(printf '%s' "$LOGIN_RESP" | jq -r '.access_token')"
[ -n "$TOKEN" ] && [ "$TOKEN" != "null" ] || fail "initial login failed to return access_token"
pass "admin login succeeded on initial database"

kill "$SERVER_PID" 2>/dev/null || true
wait "$SERVER_PID" 2>/dev/null || true
SERVER_PID=""

echo "==> [Restore Drill] Step 4: taking logical backup of datastore"
BACKUP_SQL="$HOME_DIR/backup.sql"
pg_dump --clean --if-exists -d "$DB_URL" > "$BACKUP_SQL"
[ -s "$BACKUP_SQL" ] || fail "pg_dump produced empty backup file"
pass "logical backup captured successfully ($(wc -c < "$BACKUP_SQL") bytes)"

echo "==> [Restore Drill] Step 5: destroying database state to simulate disaster / node loss"
psql "$DB_URL" -c "DROP SCHEMA public CASCADE; CREATE SCHEMA public;" >/dev/null

echo "==> [Restore Drill] Step 6: restoring backup into clean database"
psql "$DB_URL" < "$BACKUP_SQL" >/dev/null
pass "backup restored into empty database cleanly"

echo "==> [Restore Drill] Step 7: starting control plane on restored database"
LEOFLOW_SERVER_HTTP_ADDR="127.0.0.1:${PORT}" \
LEOFLOW_SERVER_GRPC_ADDR=":19096" \
LEOFLOW_SERVER_METRICS_ADDR=":19095" \
LEOFLOW_DATABASE_URL="$DB_URL" \
LEOFLOW_REDIS_URL="$REDIS_URL" \
LEOFLOW_AUTH_JWT_SECRET="$JWT_SECRET" \
LEOFLOW_SECRET_KEY="$SECRET_KEY" \
LEOFLOW_CONFIG_DIR="$HOME_DIR/.dexaflow" \
"$HOME_DIR/dexaflow-server" >"$HOME_DIR/server-restored.log" 2>&1 &
SERVER_PID=$!

READY=0
for _ in $(seq 1 30); do
  if curl -fsS "${BASE}/readyz" >/dev/null 2>&1; then
    READY=1
    break
  fi
  sleep 0.5
done
[ "$READY" -eq 1 ] || { cat "$HOME_DIR/server-restored.log"; fail "server failed to become ready on restored database"; }
pass "server reached ready state on restored database"

echo "==> [Restore Drill] Step 8: verifying admin login and auth on restored database"
LOGIN_RESTORED="$(curl -fsS -X POST "${BASE}/api/v2/auth/token" \
  -H "Content-Type: application/json" \
  -d "{\"username\":\"admin@dexaflow.local\",\"password\":\"${PW}\"}")"
RESTORED_TOKEN="$(printf '%s' "$LOGIN_RESTORED" | jq -r '.access_token')"
[ -n "$RESTORED_TOKEN" ] && [ "$RESTORED_TOKEN" != "null" ] || fail "login failed on restored database"
pass "admin authenticated successfully against restored datastore"

echo ""
echo "=== Restore drill completed successfully: backup restored and verified ready ==="
