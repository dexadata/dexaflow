#!/usr/bin/env bash
# End-to-end happy path for Leoflow Lite: setup (as the installer runs it) →
# control plane with REAL auth → admin login. Asserts the login the wizard
# provisions actually works, and that a wrong password is rejected.
#
# Requires a local Postgres + Redis (docker-compose.dev.yaml). DESTRUCTIVE: it
# resets the leoflow_dev database. Run from the repo root:  bash test/e2e/lite-login.sh
set -euo pipefail

PORT=18099
DB_URL="postgres://leoflow:leoflow@localhost:5432/leoflow_dev?sslmode=disable"
REDIS_URL="redis://localhost:6379/0"
BASE="http://127.0.0.1:${PORT}"
HOME_DIR="$(mktemp -d)"
SERVER_PID=""
pass() { printf '  \033[32mPASS\033[0m %s\n' "$1"; }
fail() { printf '  \033[31mFAIL\033[0m %s\n' "$1"; cleanup; exit 1; }
cleanup() { [ -n "$SERVER_PID" ] && kill "$SERVER_PID" 2>/dev/null || true; chmod -R u+w "$HOME_DIR" 2>/dev/null || true; rm -rf "$HOME_DIR"; }
trap cleanup EXIT

echo "==> building binaries"
go build -o "$HOME_DIR/leoflow" ./cmd/dexaflow
go build -o "$HOME_DIR/leoflow-server" ./cmd/dexaflow-server

# A fake python3.11 on PATH so `setup` uses it instead of downloading a CPython
# (the parser is not exercised by this login test).
mkdir -p "$HOME_DIR/bin"
printf '#!/bin/sh\n' > "$HOME_DIR/bin/python3.11"
chmod +x "$HOME_DIR/bin/python3.11"
export PATH="$HOME_DIR/bin:$PATH"

echo "==> resetting the leoflow_dev database (migrated, empty)"
"$HOME_DIR/leoflow" db reset --yes >/dev/null

echo "==> leoflow setup (installer path) — generates the admin, prints the password once"
SETUP_OUT="$(HOME="$HOME_DIR" "$HOME_DIR/leoflow" setup --workspace "$HOME_DIR/ws" </dev/null 2>&1)"
PW="$(printf '%s\n' "$SETUP_OUT" | sed -n 's/^[[:space:]]*password:[[:space:]]*//p' | head -1)"
HASH="$(sed -n 's/^admin_password_hash:[[:space:]]*"\(.*\)"/\1/p' "$HOME_DIR/.leoflow/config.yaml")"
[ -n "$PW" ]   || fail "setup did not print a generated password"
[ -n "$HASH" ] || fail "setup did not store an admin_password_hash"
pass "setup generated an admin password (shown once) and stored only the hash"

# Seed the editor's workspace (a DAG project) and a fake Monaco bundle so the
# IDE surface can be exercised without the ~18 MB Monaco download (provisioning
# itself is unit-tested + smoke-verified separately).
printf 'print("hello")\n' > "$HOME_DIR/ws/dag.py"
mkdir -p "$HOME_DIR/monaco/vs"
printf '// fake monaco loader\n' > "$HOME_DIR/monaco/vs/loader.js"

echo "==> starting the control plane with REAL auth (no dev bypass)"
LEOFLOW_SERVER_HTTP_ADDR="127.0.0.1:${PORT}" \
LEOFLOW_SERVER_GRPC_ADDR=":19099" \
LEOFLOW_SERVER_METRICS_ADDR=":19098" \
LEOFLOW_DATABASE_URL="$DB_URL" \
LEOFLOW_REDIS_URL="$REDIS_URL" \
LEOFLOW_AUTH_JWT_SECRET="e2e-insecure-jwt-secret-please-change" \
LEOFLOW_SECRET_KEY="e2e-insecure-secret-key-32bytes!" \
LEOFLOW_BOOTSTRAP_PASSWORD_HASH="$HASH" \
LEOFLOW_BOOTSTRAP_EMAIL="admin@leoflow.local" \
LEOFLOW_UI_EDITION="lite" \
LEOFLOW_UI_WORKSPACE="$HOME_DIR/ws" \
LEOFLOW_UI_MONACO_DIR="$HOME_DIR/monaco" \
LEOFLOW_LOGS_DIR="$HOME_DIR/logs" \
  "$HOME_DIR/leoflow-server" >"$HOME_DIR/server.log" 2>&1 &
SERVER_PID=$!

echo "==> waiting for readiness"
for i in $(seq 1 40); do
  code="$(curl -s -o /dev/null -w '%{http_code}' "${BASE}/readyz" || true)"
  [ "$code" = "200" ] && break
  kill -0 "$SERVER_PID" 2>/dev/null || fail "server exited early:\n$(cat "$HOME_DIR/server.log")"
  sleep 0.5
done
[ "$code" = "200" ] || fail "server not ready (last /readyz=$code)\n$(cat "$HOME_DIR/server.log")"
pass "control plane is ready"

echo "==> login with the correct admin password (the happy path)"
login() { curl -s -o "$HOME_DIR/resp.json" -w '%{http_code}' -X POST "${BASE}/auth/token" \
  -H 'content-type: application/json' -d "$1"; }

code="$(login "{\"username\":\"admin@leoflow.local\",\"password\":$(printf '%s' "$PW" | python3 -c 'import json,sys;print(json.dumps(sys.stdin.read()))')}")"
[ "$code" = "200" ] || fail "login returned $code (want 200)\n$(cat "$HOME_DIR/resp.json")"
grep -q '"access_token"' "$HOME_DIR/resp.json" || fail "login 200 but no access_token\n$(cat "$HOME_DIR/resp.json")"
TOKEN="$(python3 -c 'import json;print(json.load(open("'"$HOME_DIR"'/resp.json"))["access_token"])')"
[ -n "$TOKEN" ] || fail "empty access_token"
pass "admin login succeeded and returned a JWT"

echo "==> wrong password must be rejected"
code="$(login '{"username":"admin@leoflow.local","password":"definitely-wrong"}')"
[ "$code" = "401" ] || fail "wrong password returned $code (want 401)"
pass "wrong password rejected (401)"

echo "==> the JWT authenticates an API call"
code="$(curl -s -o /dev/null -w '%{http_code}' -H "Authorization: Bearer ${TOKEN}" "${BASE}/api/v2/dags?limit=1")"
[ "$code" = "200" ] || fail "authed /api/v2/dags returned $code (want 200)"
code="$(curl -s -o /dev/null -w '%{http_code}' "${BASE}/api/v2/dags?limit=1")"
[ "$code" = "401" ] || [ "$code" = "403" ] || fail "unauthenticated /api/v2/dags returned $code (want 401/403)"
pass "JWT authorizes API calls; missing token is rejected"

echo "==> the Lite web editor (ADR 0025) is served and workspace-confined"
AUTH=(-H "Authorization: Bearer ${TOKEN}")

# The editor page is served (public shell) and references Monaco + the files API.
curl -s "${BASE}/ide" > "$HOME_DIR/ide.html"
grep -q "monaco" "$HOME_DIR/ide.html" && grep -q "/api/v2/ide/tree" "$HOME_DIR/ide.html" \
  || fail "/ide page missing expected markers"
pass "/ide editor page served"

# An unauthenticated visitor must NOT be served the app shell — the gate
# redirects to the login page (no flash of the logged-in UI before bouncing).
code="$(curl -s -o /dev/null -w '%{http_code}' "${BASE}/")"
[ "$code" = "302" ] || fail "unauthenticated / returned $code (want 302 to login)"
pass "unauthenticated shell redirects to login (gate)"

# The IDE entry button is injected into the UI shell, with its inline SVG icon
# (regression guard for #88 — the icon must not silently vanish). The shell is
# now behind the login gate, so request it WITH the token.
curl -s "${AUTH[@]}" "${BASE}/" > "$HOME_DIR/home.html"
grep -q "leoflow-ide-button" "$HOME_DIR/home.html" \
  && grep -q 'href="/ide"' "$HOME_DIR/home.html" \
  && grep -q "<svg" "$HOME_DIR/home.html" \
  || fail "UI shell missing the IDE button or its icon"
pass "IDE button with icon injected into the UI shell"

# The file tree lists the seeded workspace file.
curl -s "${AUTH[@]}" "${BASE}/api/v2/ide/tree" > "$HOME_DIR/tree.json"
grep -q '"dag.py"' "$HOME_DIR/tree.json" || fail "tree missing dag.py\n$(cat "$HOME_DIR/tree.json")"
pass "GET /api/v2/ide/tree lists the workspace"

# Write then read a file round-trips.
code="$(curl -s -o /dev/null -w '%{http_code}' "${AUTH[@]}" -X PUT "${BASE}/api/v2/ide/file" \
  -H 'content-type: application/json' -d '{"path":"dag.py","content":"print(\"edited\")\n"}')"
[ "$code" = "200" ] || fail "PUT /api/v2/ide/file returned $code (want 200)"
curl -s "${AUTH[@]}" "${BASE}/api/v2/ide/file?path=dag.py" > "$HOME_DIR/file.json"
grep -q 'edited' "$HOME_DIR/file.json" || fail "read-back missing the written content\n$(cat "$HOME_DIR/file.json")"
pass "PUT then GET /api/v2/ide/file round-trips"

# Path traversal is rejected (400), never reads outside the workspace.
code="$(curl -s -o /dev/null -w '%{http_code}' "${AUTH[@]}" "${BASE}/api/v2/ide/file?path=../../../../etc/passwd")"
[ "$code" = "400" ] || fail "traversal returned $code (want 400)"
pass "path traversal rejected (400)"

# Monaco assets are served from the configured dir.
code="$(curl -s -o /dev/null -w '%{http_code}' "${BASE}/ide/vs/loader.js")"
[ "$code" = "200" ] || fail "GET /ide/vs/loader.js returned $code (want 200)"
pass "Monaco assets served from the bundle dir"

# The files API is protected: no token is rejected.
code="$(curl -s -o /dev/null -w '%{http_code}' "${BASE}/api/v2/ide/tree")"
[ "$code" = "401" ] || [ "$code" = "403" ] || fail "unauthenticated /api/v2/ide/tree returned $code (want 401/403)"
pass "files API requires auth"

echo
echo "  ✅ Lite happy path verified: setup → control plane → login → web editor."
