#!/usr/bin/env bash
# End-to-end for #1087: what the control plane tells a caller when its database
# is unreachable MID-FLIGHT, with a token that was valid seconds earlier.
#
# This is the seam unit tests cannot reach. They assert the mapping given an
# injected error; only a real outage proves the error a real driver produces on
# a real pool takes that mapping — and the defect this locks was precisely a
# mapping that looked right in isolation. It was found by hand during the
# v0.4.6 cluster validation and had no executable form until now.
#
# What is asserted, and why each one is the thing that misled someone:
#
#   1. A protected route answers 503, NOT 401. A 401 tells a well-behaved client
#      to re-authenticate against the database that is already down, and tells
#      whoever is on call that they have an auth incident.
#   2. /api/v2/auth/token/renew answers 503, NOT 401 — the same conflation lived
#      one function below the first, and its handler recorded no cause at all,
#      so the outage was invisible in the log.
#   3. The response bodies carry no driver text, and the LOG carries the cause.
#      Sanitizing without keeping the cause is not privacy, it is losing the
#      diagnosis (#961).
#   4. Recovery: the same token works again once Postgres is back, so the 503 is
#      about availability and never revoked anything.
#
# NOT covered here, deliberately: #1071's 499-vs-500 split in handleRepoError.
# With the database down, auth fails first and the request never reaches the
# repository, so that path is unreachable from this shape. Exercising it needs a
# fault-injection proxy that can fail queries while auth still succeeds.
#
# Requires Postgres + Redis as CONTAINERS (this stops and restarts the Postgres
# one) and docker on PATH. DESTRUCTIVE: resets the leoflow_dev database.
# Run from the repo root:  bash test/e2e/lite-db-outage.sh
set -uo pipefail

# Overridable so a local run can point at a DEDICATED Postgres/Redis pair
# instead of whatever shared containers the machine happens to be running —
# this test stops the database, and stopping one somebody else is using is a
# rude way to find out they were using it.
PORT="${LEOFLOW_E2E_HTTP_PORT:-18097}"
DB_URL="${LEOFLOW_E2E_DATABASE_URL:-postgres://leoflow:leoflow@localhost:5432/leoflow_dev?sslmode=disable}"
REDIS_URL="${LEOFLOW_E2E_REDIS_URL:-redis://localhost:6379/0}"
# The container to stop. Auto-detected when unset; set it explicitly when more
# than one Postgres is running, so the test cannot pick the wrong one.
PG_CONTAINER_OVERRIDE="${LEOFLOW_E2E_PG_CONTAINER:-}"
BASE="http://127.0.0.1:${PORT}"
HOME_DIR="$(mktemp -d)"
SERVER_PID=""
PG_CONTAINER=""
FAILURES=0

pass() { printf '  \033[32mPASS\033[0m %s\n' "$1"; }
fail() { printf '  \033[31mFAIL\033[0m %s\n' "$1"; FAILURES=$((FAILURES+1)); }
die()  { printf '  \033[31mFAIL\033[0m %s\n' "$1"; exit 1; }

cleanup() {
  # Restore Postgres FIRST: leaving it stopped would break every later job on
  # this runner, and a local run would look like the database "just died".
  if [ -n "$PG_CONTAINER" ]; then
    docker start "$PG_CONTAINER" >/dev/null 2>&1 || true
  fi
  [ -n "$SERVER_PID" ] && kill "$SERVER_PID" 2>/dev/null || true
  chmod -R u+w "$HOME_DIR" 2>/dev/null || true
  rm -rf "$HOME_DIR"
}
trap cleanup EXIT

command -v docker >/dev/null || die "docker is required: this test stops the Postgres container"

echo "==> locating the Postgres container"
if [ -n "$PG_CONTAINER_OVERRIDE" ]; then
  PG_CONTAINER="$PG_CONTAINER_OVERRIDE"
else
  # docker ps lists running containers only, and the expose filter matches the
  # image's own EXPOSE — so a Redis beside the Postgres (the CI shape, and the
  # usual local one) never matches. The name fallback covers an image that
  # declares no EXPOSE at all.
  CANDIDATES="$(docker ps --filter 'expose=5432' --format '{{.ID}} {{.Image}}')"
  [ -n "$CANDIDATES" ] || CANDIDATES="$(docker ps --format '{{.ID}} {{.Image}}' | awk '$2 ~ /postgres/')"
  [ -n "$CANDIDATES" ] || die "no running Postgres container found; this test needs one it can stop"
  # Taking the first of several would stop a database somebody else is using,
  # and docker's ordering decides which one. Refuse instead: the caller knows
  # which Postgres is theirs, and LEOFLOW_E2E_PG_CONTAINER is how they say so.
  if [ "$(printf '%s\n' "$CANDIDATES" | grep -c .)" -gt 1 ]; then
    printf '%s\n' "$CANDIDATES" | sed 's/^/      /' >&2
    die "more than one Postgres container is running (listed above); set LEOFLOW_E2E_PG_CONTAINER to the one this test may stop"
  fi
  PG_CONTAINER="${CANDIDATES%% *}"
fi
[ -n "$PG_CONTAINER" ] || die "no running Postgres container found; this test needs one it can stop"
echo "    container: $PG_CONTAINER"

echo "==> building binaries"
go build -o "$HOME_DIR/leoflow" ./cmd/dexaflow || die "building leoflow"
go build -o "$HOME_DIR/leoflow-server" ./cmd/dexaflow-server || die "building leoflow-server"

mkdir -p "$HOME_DIR/bin"
printf '#!/bin/sh\n' > "$HOME_DIR/bin/python3.11"
chmod +x "$HOME_DIR/bin/python3.11"
export PATH="$HOME_DIR/bin:$PATH"

echo "==> migrating the database"
# `leoflow db reset` is NOT used here, and the reason is worth stating: it builds
# its own DSN from Lite's managed-datastore convention (devDSNs → devDBPort) and
# ignores DB_URL entirely. With DB_URL overridden it migrates one database while
# the server connects to another, and the symptom surfaces much later as "schema
# is not current" pointing at the server. Migrating the DSN this test actually
# uses keeps the two from drifting.
command -v migrate >/dev/null || die "the golang-migrate CLI is required (go install -tags postgres github.com/golang-migrate/migrate/v4/cmd/migrate@latest)"
migrate -path migrations -database "$DB_URL" up >/dev/null 2>&1 || die "applying migrations to $DB_URL"

echo "==> leoflow setup"
SETUP_OUT="$(HOME="$HOME_DIR" LEOFLOW_DATABASE_URL="$DB_URL" "$HOME_DIR/leoflow" setup --workspace "$HOME_DIR/ws" </dev/null 2>&1)"
PW="$(printf '%s\n' "$SETUP_OUT" | sed -n 's/^[[:space:]]*password:[[:space:]]*//p' | head -1)"
HASH="$(sed -n 's/^admin_password_hash:[[:space:]]*"\(.*\)"/\1/p' "$HOME_DIR/.leoflow/config.yaml")"
[ -n "$PW" ] && [ -n "$HASH" ] || die "setup did not produce an admin password/hash"

echo "==> starting the control plane with REAL auth"
LEOFLOW_SERVER_HTTP_ADDR="127.0.0.1:${PORT}" \
LEOFLOW_SERVER_GRPC_ADDR=":$((PORT+1000))" \
LEOFLOW_SERVER_METRICS_ADDR=":$((PORT+1001))" \
LEOFLOW_DATABASE_URL="$DB_URL" \
LEOFLOW_REDIS_URL="$REDIS_URL" \
LEOFLOW_AUTH_JWT_SECRET="e2e-insecure-jwt-secret-please-change" \
LEOFLOW_SECRET_KEY="e2e-insecure-secret-key-32bytes!" \
LEOFLOW_BOOTSTRAP_PASSWORD_HASH="$HASH" \
LEOFLOW_BOOTSTRAP_EMAIL="admin@leoflow.local" \
LEOFLOW_EXECUTOR_TYPE="subprocess" \
LEOFLOW_LOGS_DIR="$HOME_DIR/logs" \
  "$HOME_DIR/leoflow-server" >"$HOME_DIR/server.log" 2>&1 &
SERVER_PID=$!

for _ in $(seq 1 60); do
  [ "$(curl -s -o /dev/null -w '%{http_code}' "${BASE}/readyz" || true)" = "200" ] && break
  kill -0 "$SERVER_PID" 2>/dev/null || die "server exited early:
$(cat "$HOME_DIR/server.log")"
  sleep 0.5
done
[ "$(curl -s -o /dev/null -w '%{http_code}' "${BASE}/readyz")" = "200" ] || die "never became ready:
$(tail -20 "$HOME_DIR/server.log")"

echo "==> logging in (the token below is valid; nothing about it changes later)"
TOKEN="$(curl -s -X POST "${BASE}/auth/token" -H 'Content-Type: application/json' \
  -d "{\"username\":\"admin@leoflow.local\",\"password\":\"${PW}\"}" | sed -n 's/.*"access_token":"\([^"]*\)".*/\1/p')"
[ -n "$TOKEN" ] || die "login did not return a token:
$(tail -10 "$HOME_DIR/server.log")"

code="$(curl -s -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $TOKEN" "${BASE}/api/v2/dags")"
if [ "$code" = "200" ]; then
  pass "baseline: the token works (/api/v2/dags = 200)"
else
  die "baseline /api/v2/dags = $code, expected 200"
fi

echo "==> STOPPING Postgres — the token stays valid, the store does not"
docker stop -t 2 "$PG_CONTAINER" >/dev/null || die "could not stop the Postgres container"

# The pool may hold a connection that has not noticed yet; poll until the
# outage is observable, then assert. Without this the assertions could run
# against a request the pool served from a live connection and pass vacuously.
# Every attempt carries its own X-Request-Id, which the server honors and echoes
# into the one structured line it logs for that request. The id of the attempt
# that OBSERVED the outage is what anchors the log assertion further down to
# THIS request — see the comment there for why an unanchored grep proves nothing.
observed=""
OBSERVED_REQ_ID=""
for attempt in $(seq 1 40); do
  req_id="lite-db-outage-dags-${attempt}"
  code="$(curl -s -m 20 -o "$HOME_DIR/body.json" -w '%{http_code}' \
    -H "X-Request-Id: ${req_id}" -H "Authorization: Bearer $TOKEN" "${BASE}/api/v2/dags")"
  [ "$code" != "200" ] && { observed="$code"; OBSERVED_REQ_ID="$req_id"; break; }
  sleep 0.5
done
[ -n "$observed" ] || die "the database was stopped but /api/v2/dags kept answering 200 — every assertion below would be vacuous"

echo "==> assertions"
# The status alone does not say WHICH mechanism answered: a readiness gate, a
# proxy or a future handler could also produce 503 on this route and the
# assertion would pass without proving anything about the auth store. The
# problem detail is what pins it to the store being unreachable.
if [ "$observed" != "503" ]; then
  fail "a protected route answered $observed, want 503 (401 tells the client to re-authenticate against the dead database)"
  printf '       body: %s\n' "$(head -c 200 "$HOME_DIR/body.json")"
elif ! grep -q 'authentication temporarily unavailable' "$HOME_DIR/body.json"; then
  fail "the 503 did not come from the unreachable auth store: $(head -c 200 "$HOME_DIR/body.json")"
else
  pass "a protected route answers 503 during the outage (was 401: #1087)"
fi

RENEW_REQ_ID="lite-db-outage-renew"
rcode="$(curl -s -m 20 -o "$HOME_DIR/renew.json" -w '%{http_code}' -X POST \
  -H "X-Request-Id: ${RENEW_REQ_ID}" -H "Authorization: Bearer $TOKEN" "${BASE}/api/v2/auth/token/renew")"
if [ "$rcode" != "503" ]; then
  fail "token renew answered $rcode, want 503"
  printf '       body: %s\n' "$(head -c 200 "$HOME_DIR/renew.json")"
elif ! grep -q 'authentication temporarily unavailable' "$HOME_DIR/renew.json"; then
  fail "renew's 503 did not come from the unreachable auth store: $(head -c 200 "$HOME_DIR/renew.json")"
else
  pass "token renew answers 503 during the outage (was 401, with no cause logged)"
fi

leaked=0
# An empty body makes every pattern below miss, so the scan would report a clean
# pass while having read nothing. curl leaves the file empty when it times out,
# so check before scanning rather than trusting that it never happens.
for body in "$HOME_DIR/body.json" "$HOME_DIR/renew.json"; do
  if [ ! -s "$body" ]; then
    fail "no response body was captured in ${body##*/}; the leak scan below would be vacuous"
    leaked=1
  fi
done
for pat in 'dial tcp' 'connection refused' 'SQLSTATE' 'pq:' 'pgx' 'user=leoflow' '5432'; do
  if grep -qiE "$pat" "$HOME_DIR/body.json" "$HOME_DIR/renew.json" 2>/dev/null; then
    fail "a response body leaks driver text: $pat"
    leaked=1
  fi
done
[ "$leaked" = "0" ] && pass "neither body carries driver text (#961)"

# The cause must be in the LOG, otherwise this is not sanitization, it is losing
# the diagnosis.
#
# Anchored to the request ids above, and that anchoring is the whole assertion.
# The scheduler loop ticks every second and its own failures carry the driver's
# "dial tcp ... connection refused" verbatim, so a grep for driver text over the
# lines written during the outage passes whether or not either handler recorded
# anything — which is precisely the defect being locked. Only the line belonging
# to the request that was answered proves the request's cause was kept.
log_records_cause() {
  # StructuredLogger writes its line after the response is flushed, so the body
  # can be in hand before the line is on disk. Poll rather than read once.
  for _ in $(seq 1 20); do
    if grep -F "$1" "$HOME_DIR/server.log" | grep -q 'cause'; then
      return 0
    fi
    sleep 0.25
  done
  return 1
}
if log_records_cause "$OBSERVED_REQ_ID" && log_records_cause "$RENEW_REQ_ID"; then
  pass "the server log carries the cause the bodies withheld"
else
  fail "the log carries no cause for the outage — the diagnosis was lost, not withheld"
  printf '       log tail: %s\n' "$(tail -n 5 "$HOME_DIR/server.log")"
fi

rz="$(curl -s -m 10 -o "$HOME_DIR/ready.json" -w '%{http_code}' "${BASE}/readyz")"
if [ "$rz" = "503" ] && grep -qi 'postgres' "$HOME_DIR/ready.json"; then
  pass "/readyz is 503 and NAMES the dependency (#1042)"
else
  fail "/readyz = $rz, body=$(head -c 160 "$HOME_DIR/ready.json") — want 503 naming postgres"
fi

echo "==> restarting Postgres"
docker start "$PG_CONTAINER" >/dev/null || die "could not restart Postgres"
recovered=""
for _ in $(seq 1 60); do
  code="$(curl -s -m 10 -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $TOKEN" "${BASE}/api/v2/dags")"
  [ "$code" = "200" ] && { recovered=yes; break; }
  sleep 1
done
if [ -n "$recovered" ]; then
  pass "the SAME token works again after recovery — the 503 revoked nothing"
else
  fail "the token did not work again after Postgres came back"
fi

if [ "$FAILURES" -gt 0 ]; then
  printf '\n\033[31m%d assertion(s) failed\033[0m\n' "$FAILURES"
  exit 1
fi
printf '\n\033[32mdatabase-outage e2e passed\033[0m\n'
