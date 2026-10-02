#!/usr/bin/env bash
# End-to-end gate for the `leoflow connections` and `leoflow variables` CLI groups
# (#881) on the LITE edition (no-auth loopback). The unit tests prove the flag ->
# request mapping against an httptest server; what they cannot prove is the CLI
# talking to the REAL control plane and — the load-bearing claim — that the same
# masking the embedded Airflow Admin UI relies on is applied to what the CLI and
# a raw curl see. So this:
#   1. `connections set` a connection carrying a secret in BOTH --password and
#      --extra (a token),
#   2. asserts `connections get`/`list` never print either secret,
#   3. curls the SAME GET /api/v2/connections/<id> the UI reads and asserts the
#      response masks the extra token and omits the password — the CLI<->UI-data
#      integration proof,
#   4. runs the same round-trip for `variables set/get/list/delete` and
#      `connections delete`,
#   5. cleans up every planted connection/variable before exit.
#
# Needs a local Postgres (docker-compose.dev.yaml). Run from the repo root:
#   bash test/e2e/lite-connections.sh
set -euo pipefail

PORT=18097
DB_URL="${LEOFLOW_E2E_DB_URL:-postgres://leoflow:leoflow@localhost:5432/leoflow_dev?sslmode=disable}"
BASE="http://127.0.0.1:${PORT}"
CONN_ID="e2e_pg"
VAR_PLAIN="e2e_region"
VAR_SECRET="e2e_api_token"
# Distinctive sentinels: if either appears in CLI output or a curl body, a secret
# leaked. They must never be echoed anywhere.
PW_SENTINEL="PWSENTINEL_do_not_leak_2f9c"
EXTRA_SENTINEL="EXTRASECRET_do_not_leak_7b1a"
VALUE_SENTINEL="VARSECRET_do_not_leak_c3d5"
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
TMP="$(mktemp -d)"
WS="$TMP/ws"
LITE_PID=""
CLEANED=""
pass() { printf '  \033[32mPASS\033[0m %s\n' "$1"; }
fail() { printf '  \033[31mFAIL\033[0m %s\n' "$1"; [ -n "${2:-}" ] && tail -60 "$2"; exit 1; }

# plant_cleanup best-effort deletes everything this script created, so a failed
# run never ambushes the operator's next real action with leftover test data.
plant_cleanup() {
  [ -n "$CLEANED" ] && return 0
  CLEANED=1
  if [ -n "$LITE_PID" ] && kill -0 "$LITE_PID" 2>/dev/null; then
    "$TMP/leoflow" connections delete "$CONN_ID" --server "$BASE" >/dev/null 2>&1 || true
    "$TMP/leoflow" variables delete "$VAR_PLAIN" --server "$BASE" >/dev/null 2>&1 || true
    "$TMP/leoflow" variables delete "$VAR_SECRET" --server "$BASE" >/dev/null 2>&1 || true
  fi
}
cleanup() {
  plant_cleanup
  [ -n "$LITE_PID" ] && kill "$LITE_PID" 2>/dev/null || true
  chmod -R u+w "$TMP" 2>/dev/null || true
  rm -rf "$TMP"
}
trap cleanup EXIT

# Isolate HOME so Lite runs no-auth loopback with a clean config.
export HOME="$TMP/home"; mkdir -p "$HOME" "$WS"
export LEOFLOW_DATABASE_URL="$DB_URL"
export LEOFLOW_LOGS_DIR="$TMP/logs"
# leoflow lite spawns the parser via `python3 -m leoflow_parser`; put it on PYTHONPATH.
export PYTHONPATH="${PYTHONPATH:-$ROOT/parser}"

echo "==> building binaries"
go build -o "$TMP/leoflow" ./cmd/dexaflow
go build -o "$TMP/leoflow-server" ./cmd/dexaflow-server
go build -o "$TMP/leoflow-agent" ./cmd/dexaflow-agent
export PATH="$TMP:$PATH"

echo "==> resetting the database"
"$TMP/leoflow" db reset --yes >/dev/null

echo "==> booting Lite (subprocess executor, no-auth loopback)"
"$TMP/leoflow" lite --no-up --executor subprocess --port "$PORT" "$WS" >"$TMP/lite.log" 2>&1 &
LITE_PID=$!
disown "$LITE_PID" 2>/dev/null || true
ready=""
for _ in $(seq 1 "${LITE_READY_TRIES:-600}"); do
  curl -fsS "${BASE}/readyz" >/dev/null 2>&1 && { ready=1; break; }
  kill -0 "$LITE_PID" 2>/dev/null || fail "Lite exited early" "$TMP/lite.log"
  sleep 1
done
[ -n "$ready" ] || fail "Lite did not become ready" "$TMP/lite.log"
pass "Lite control plane is ready"

CLI=("$TMP/leoflow")
srv=(--server "$BASE")

# ---------------------------------------------------------------------------
echo "==> connections set (with a secret in --password AND --extra)"
"${CLI[@]}" connections set "$CONN_ID" "${srv[@]}" \
  --conn-type postgres --host db.internal --login analytics \
  --schema public --port 5432 \
  --password "$PW_SENTINEL" \
  --extra "{\"token\":\"${EXTRA_SENTINEL}\"}" >"$TMP/set.out" 2>&1 \
  || fail "connections set failed" "$TMP/set.out"
grep -q "$CONN_ID" "$TMP/set.out" || fail "set output did not name the connection" "$TMP/set.out"
grep -q "$PW_SENTINEL" "$TMP/set.out" && fail "set output LEAKED the password" "$TMP/set.out"
grep -q "$EXTRA_SENTINEL" "$TMP/set.out" && fail "set output LEAKED the extra secret" "$TMP/set.out"
pass "connections set upserted ${CONN_ID} and printed no secret"

echo "==> connections get / list never print secrets"
"${CLI[@]}" connections get "$CONN_ID" "${srv[@]}" >"$TMP/get.out" 2>&1 \
  || fail "connections get failed" "$TMP/get.out"
grep -q "$CONN_ID" "$TMP/get.out" || fail "get did not show the connection" "$TMP/get.out"
grep -q "$PW_SENTINEL" "$TMP/get.out" && fail "get LEAKED the password" "$TMP/get.out"
grep -q "$EXTRA_SENTINEL" "$TMP/get.out" && fail "get LEAKED the extra secret" "$TMP/get.out"
"${CLI[@]}" connections list "${srv[@]}" >"$TMP/list.out" 2>&1 \
  || fail "connections list failed" "$TMP/list.out"
grep -q "$CONN_ID" "$TMP/list.out" || fail "list did not include the connection" "$TMP/list.out"
grep -q "$PW_SENTINEL" "$TMP/list.out" && fail "list LEAKED the password" "$TMP/list.out"
grep -q "$EXTRA_SENTINEL" "$TMP/list.out" && fail "list LEAKED the extra secret" "$TMP/list.out"
pass "connections get/list show the connection with secrets masked"

echo "==> curl the SAME endpoint the embedded Airflow UI reads (masking proof)"
curl -fsS "${BASE}/api/v2/connections/${CONN_ID}" >"$TMP/ui.json" \
  || fail "curl GET /api/v2/connections/${CONN_ID} failed" "$TMP/lite.log"
grep -q "$PW_SENTINEL" "$TMP/ui.json" && fail "UI endpoint returned the password" "$TMP/ui.json"
grep -q "$EXTRA_SENTINEL" "$TMP/ui.json" && fail "UI endpoint returned the raw extra secret" "$TMP/ui.json"
# The extra's token key is present but masked to *** (Airflow-style redaction).
grep -q '\*\*\*' "$TMP/ui.json" || fail "UI endpoint did not mask the extra token" "$TMP/ui.json"
# password must be entirely absent from the JSON (write-only).
python3 -c 'import json,sys; d=json.load(open(sys.argv[1])); sys.exit(0 if "password" not in d else 1)' "$TMP/ui.json" \
  || fail "UI endpoint response contained a password field" "$TMP/ui.json"
pass "UI-data endpoint masks extra and omits password (CLI<->UI integration proof)"

echo "==> partial set (change only --host) must PRESERVE the omitted secret extra"
"${CLI[@]}" connections set "$CONN_ID" "${srv[@]}" \
  --conn-type postgres --host db.internal2 >"$TMP/partial.out" 2>&1 \
  || fail "partial connections set failed" "$TMP/partial.out"
curl -fsS "${BASE}/api/v2/connections/${CONN_ID}" >"$TMP/ui2.json" \
  || fail "curl after partial set failed" "$TMP/lite.log"
# The host changed; the extra we did NOT pass must still be there (masked), not
# wiped. Before the COALESCE upsert this partial write nulled the extra + password.
python3 -c 'import json,sys; d=json.load(open(sys.argv[1])); sys.exit(0 if d.get("host")=="db.internal2" else 1)' "$TMP/ui2.json" \
  || fail "partial set did not update the host" "$TMP/ui2.json"
grep -q '\*\*\*' "$TMP/ui2.json" || fail "partial set WIPED the omitted extra (no masked value remains)" "$TMP/ui2.json"
pass "partial set changed the host and preserved the omitted extra (safe merge)"

echo "==> explicit clear: set --login '' blanks the login but preserves the rest (#887)"
"${CLI[@]}" connections set "$CONN_ID" "${srv[@]}" \
  --conn-type postgres --login "" >"$TMP/clear.out" 2>&1 \
  || fail "explicit-clear connections set failed" "$TMP/clear.out"
curl -fsS "${BASE}/api/v2/connections/${CONN_ID}" >"$TMP/ui3.json" \
  || fail "curl after explicit clear failed" "$TMP/lite.log"
# login was "analytics"; an explicit "" must clear it (rendered as null/absent).
python3 -c 'import json,sys; d=json.load(open(sys.argv[1])); v=d.get("login"); sys.exit(0 if (v is None or v=="") else 1)' "$TMP/ui3.json" \
  || fail "explicit --login \"\" did not clear the login" "$TMP/ui3.json"
# Clearing login must not disturb the omitted extra (still masked-present).
grep -q '\*\*\*' "$TMP/ui3.json" || fail "explicit clear WIPED the omitted extra" "$TMP/ui3.json"
pass "explicit --login '' cleared the login and preserved the omitted extra"

echo "==> masked write-back: extra token as *** must PRESERVE the real secret (#874)"
# Simulate the Admin UI GETting the connection (extra token shown as ***) and
# saving it straight back. The mask must be treated as "unchanged", not persisted
# over the real secret.
"${CLI[@]}" connections set "$CONN_ID" "${srv[@]}" \
  --conn-type postgres --extra '{"token":"***"}' >"$TMP/mask.out" 2>&1 \
  || fail "masked-extra connections set failed" "$TMP/mask.out"
curl -fsS "${BASE}/api/v2/connections/${CONN_ID}" >"$TMP/ui4.json" \
  || fail "curl after masked write-back failed" "$TMP/lite.log"
# The extra must still be present and masked — not cleared, and never the literal
# mask leaked as a real value. The value-level proof (that the stored secret is
# still EXTRA_SENTINEL rather than "***") lives in the Go integration tests
# (internal/storage + internal/api full-stack), which the delivery path cannot
# expose over HTTP; here we prove the write did not wipe or leak.
grep -q '\*\*\*' "$TMP/ui4.json" || fail "masked write-back WIPED the extra" "$TMP/ui4.json"
grep -q "$EXTRA_SENTINEL" "$TMP/ui4.json" && fail "masked write-back leaked the real secret" "$TMP/ui4.json"
pass "masked extra write-back preserved a masked extra (no wipe, no leak)"

# ---------------------------------------------------------------------------
echo "==> variables set (plain + a secret-keyed one) via positional and --value-stdin"
"${CLI[@]}" variables set "$VAR_PLAIN" us-east-1 "${srv[@]}" --description "default region" >"$TMP/vset.out" 2>&1 \
  || fail "variables set (plain) failed" "$TMP/vset.out"
grep -q "$VAR_PLAIN" "$TMP/vset.out" || fail "variables set did not name the key" "$TMP/vset.out"
printf '%s\n' "$VALUE_SENTINEL" | "${CLI[@]}" variables set "$VAR_SECRET" "${srv[@]}" --value-stdin >"$TMP/vset2.out" 2>&1 \
  || fail "variables set (--value-stdin) failed" "$TMP/vset2.out"
grep -q "$VALUE_SENTINEL" "$TMP/vset2.out" && fail "variables set LEAKED the stdin value" "$TMP/vset2.out"
pass "variables set upserted both keys; the stdin value was not echoed"

echo "==> variables get / list mask the secret-keyed value"
"${CLI[@]}" variables get "$VAR_PLAIN" "${srv[@]}" >"$TMP/vget.out" 2>&1 \
  || fail "variables get (plain) failed" "$TMP/vget.out"
grep -q "us-east-1" "$TMP/vget.out" || fail "plain variable value not shown" "$TMP/vget.out"
"${CLI[@]}" variables get "$VAR_SECRET" "${srv[@]}" >"$TMP/vget2.out" 2>&1 \
  || fail "variables get (secret) failed" "$TMP/vget2.out"
grep -q "$VALUE_SENTINEL" "$TMP/vget2.out" && fail "variables get LEAKED the secret value" "$TMP/vget2.out"
"${CLI[@]}" variables list "${srv[@]}" >"$TMP/vlist.out" 2>&1 \
  || fail "variables list failed" "$TMP/vlist.out"
grep -q "$VAR_PLAIN" "$TMP/vlist.out" || fail "variables list missing the plain key" "$TMP/vlist.out"
grep -q "$VAR_SECRET" "$TMP/vlist.out" || fail "variables list missing the secret key" "$TMP/vlist.out"
grep -q "$VALUE_SENTINEL" "$TMP/vlist.out" && fail "variables list LEAKED the secret value" "$TMP/vlist.out"
pass "variables get/list show keys; the secret-keyed value stays masked"

echo "==> curl the SAME variables endpoint the UI reads (masking proof)"
curl -fsS "${BASE}/api/v2/variables/${VAR_SECRET}" >"$TMP/uv.json" \
  || fail "curl GET /api/v2/variables/${VAR_SECRET} failed" "$TMP/lite.log"
grep -q "$VALUE_SENTINEL" "$TMP/uv.json" && fail "UI variables endpoint returned the raw secret value" "$TMP/uv.json"
grep -q '\*\*\*' "$TMP/uv.json" || fail "UI variables endpoint did not mask the secret value" "$TMP/uv.json"
pass "UI-data variables endpoint masks the secret-keyed value"

# ---------------------------------------------------------------------------
echo "==> delete round-trip (variables + connection) and confirm they are gone"
"${CLI[@]}" variables delete "$VAR_PLAIN" "${srv[@]}" >/dev/null 2>&1 || fail "variables delete (plain) failed"
"${CLI[@]}" variables delete "$VAR_SECRET" "${srv[@]}" >/dev/null 2>&1 || fail "variables delete (secret) failed"
"${CLI[@]}" connections delete "$CONN_ID" "${srv[@]}" >/dev/null 2>&1 || fail "connections delete failed"
# A follow-up get must now fail (deleted).
"${CLI[@]}" connections get "$CONN_ID" "${srv[@]}" >/dev/null 2>&1 && fail "connection still present after delete"
code="$(curl -s -o /dev/null -w '%{http_code}' "${BASE}/api/v2/variables/${VAR_SECRET}")"
[ "$code" = "404" ] || fail "deleted variable returned $code (want 404)"
CLEANED=1  # everything already removed
pass "connections/variables delete removed every planted record"

echo
echo "  ✅ leoflow connections + variables verified end to end: CLI upsert/list/get/delete,"
echo "     secrets never printed, and the UI-data endpoints mask extra/value and omit password."
