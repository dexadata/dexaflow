#!/usr/bin/env bash
#
# Stands up what the browser assertions in sso-login-page.js need and runs it
# twice: once against a control plane with SSO configured, once without.
#
# It covers two things a Go handler test cannot reach. #1160: a deployment
# configured for SSO must OFFER the flow, and a refused sign-on must come back to
# the sign-in page saying so. #1191: a break-glass password login on top of a
# live SSO session must REPLACE the identity. That second one is browser
# behavior and nothing else can prove it. A script cannot overwrite an HttpOnly
# cookie, so while the login page set the session cookie with document.cookie the
# browser silently kept the SSO identity while the server answered 200. Proving
# it needs a real SSO login to have happened first, so the fake IdP below signs
# real ID tokens rather than only serving discovery.
#
# The awkward part is that leoflow refuses a non-https issuer at boot, and OIDC
# discovery happens at boot, so a fake IdP has to serve
# /.well-known/openid-configuration over TLS before the server will start at all.
# This generates a throwaway CA for it and points the server at that CA through
# SSL_CERT_FILE, which the Linux build of Go's crypto/x509 reads. Nothing here
# touches the system trust store.
#
# Requirements: openssl, python3, node with playwright-core, the golang-migrate
# CLI (`migrate`), a built bin/leoflow-server, and a reachable dev Postgres.
#
# Usage: test/e2e/sso-login-page.sh
set -euo pipefail

# PLATFORM NOTE. This needs Go to trust a throwaway CA through SSL_CERT_FILE,
# because leoflow refuses a non-https issuer and discovery happens at boot. That
# works on Linux, which is where CI runs it. It does NOT work on macOS: the darwin
# build of crypto/x509 defers to the platform verifier and has no SSL_CERT_FILE
# path at all, so the variable is read by nothing.
#
# Measure that with a handshake, not with the cert pool. x509.SystemCertPool on
# darwin returns a pool whose Subjects() is empty whether or not SSL_CERT_FILE is
# set, so "empty pool" is the same observation in both cases and distinguishes
# nothing. An https GET against a server holding a cert signed by the named CA
# does distinguish, and on macOS it fails with "certificate signed by unknown
# authority" with the variable set (measured, go1.26, darwin/arm64).
#
# On a Mac, run it inside a Linux container or let CI run it.

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
WORK="$(mktemp -d)"
IDP_PORT="${IDP_PORT:-18443}"
SSO_PORT="${SSO_PORT:-18080}"
PLAIN_PORT="${PLAIN_PORT:-18081}"
DB="${LEOFLOW_E2E_DATABASE_URL:-postgres://leoflow:leoflow@localhost:5432/leoflow?sslmode=disable}"
PIDS=()

cleanup() {
  for p in "${PIDS[@]:-}"; do kill "$p" 2>/dev/null || true; done
  rm -rf "$WORK"
}
trap cleanup EXIT

log() { printf '\033[1;34m==>\033[0m %s\n' "$*"; }

log "Generating a throwaway CA and a localhost cert for the fake IdP"
openssl req -x509 -newkey rsa:2048 -keyout "$WORK/idp.key" -out "$WORK/idp.crt" \
  -days 1 -nodes -subj "/CN=localhost" \
  -addext "subjectAltName=DNS:localhost,IP:127.0.0.1" >/dev/null 2>&1
# The agent gRPC listener needs its own pair; the Pro edition refuses to boot
# without one, and it is unrelated to the IdP.
openssl req -x509 -newkey rsa:2048 -keyout "$WORK/grpc.key" -out "$WORK/grpc.crt" \
  -days 1 -nodes -subj "/CN=localhost" >/dev/null 2>&1
# The ID-token signing key, which is NOT the TLS key: the control plane fetches
# the matching public half from the IdP's JWKS and verifies every token against
# it. PKCS#1 DER for the public half because the IdP derives the JWK's n and e
# from it with a few lines of ASN.1 rather than a Python crypto dependency.
openssl genrsa -out "$WORK/signing.key" 2048 >/dev/null 2>&1
openssl rsa -in "$WORK/signing.key" -RSAPublicKey_out -outform DER \
  -out "$WORK/signing.pub.der" >/dev/null 2>&1

ISSUER="https://localhost:${IDP_PORT}"
cat > "$WORK/discovery.json" <<JSON
{
  "issuer": "${ISSUER}",
  "authorization_endpoint": "${ISSUER}/authorize",
  "token_endpoint": "${ISSUER}/token",
  "jwks_uri": "${ISSUER}/jwks",
  "response_types_supported": ["code"],
  "subject_types_supported": ["public"],
  "id_token_signing_alg_values_supported": ["RS256"]
}
JSON

log "Serving the fake IdP on ${ISSUER}"
# The identities this IdP will assert. SSO_EMAIL and BREAK_GLASS_EMAIL must
# differ: the whole point of the #1191 assertion is that after a break-glass
# login the session is the second one and not the first.
SSO_EMAIL="sso-user@example.com"
BREAK_GLASS_EMAIL="breakglass@example.com"
BREAK_GLASS_PASSWORD="break-glass-e2e-pw"
cat > "$WORK/idp.json" <<JSON
{
  "issuer": "${ISSUER}",
  "client_id": "leoflow",
  "subject": "sso-subject-1",
  "email": "${SSO_EMAIL}",
  "tenant_claim": "hd",
  "tenant_value": "example.com"
}
JSON
cat > "$WORK/idp.py" <<'PYEOF'
import base64
import hashlib
import http.server
import json
import os
import ssl
import subprocess
import sys
import time
from urllib.parse import parse_qs, quote, urlparse

work, port = sys.argv[1], int(sys.argv[2])
cfg = json.load(open(work + "/idp.json"))
doc = open(work + "/discovery.json", "rb").read()
KEY = work + "/signing.key"
KID = "idp-key-1"


def b64u(raw):
    return base64.urlsafe_b64encode(raw).rstrip(b"=").decode()


def der_value(buf, i):
    """Return (value, next_index) for the DER TLV at i. Enough ASN.1 to read an
    RSAPublicKey, which is a SEQUENCE of two INTEGERs and nothing else."""
    i += 1  # tag
    length = buf[i]
    i += 1
    if length & 0x80:
        n = length & 0x7F
        length = int.from_bytes(buf[i:i + n], "big")
        i += n
    return buf[i:i + length], i + length


def jwk():
    der = open(work + "/signing.pub.der", "rb").read()
    seq, _ = der_value(der, 0)
    modulus, after = der_value(seq, 0)
    exponent, _ = der_value(seq, after)
    # DER signs its INTEGERs; a JWK carries the unsigned big-endian bytes.
    return {
        "kty": "RSA", "use": "sig", "alg": "RS256", "kid": KID,
        "n": b64u(modulus.lstrip(b"\x00")), "e": b64u(exponent.lstrip(b"\x00")),
    }


# code -> {nonce, challenge}, staged by /authorize and spent by /token.
codes = {}


def sign_id_token(nonce):
    now = int(time.time())
    header = {"alg": "RS256", "typ": "JWT", "kid": KID}
    payload = {
        "iss": cfg["issuer"], "aud": cfg["client_id"], "sub": cfg["subject"],
        "exp": now + 600, "iat": now, "nonce": nonce,
        "email": cfg["email"], "email_verified": True,
        cfg["tenant_claim"]: cfg["tenant_value"],
    }
    signing_input = (
        b64u(json.dumps(header, separators=(",", ":")).encode())
        + "." + b64u(json.dumps(payload, separators=(",", ":")).encode())
    )
    # openssl rather than a Python crypto package: this script already requires
    # openssl and must run on a bare CI image with no pip install.
    sig = subprocess.run(
        ["openssl", "dgst", "-sha256", "-sign", KEY],
        input=signing_input.encode(), capture_output=True, check=True,
    ).stdout
    return signing_input + "." + b64u(sig)


class Handler(http.server.BaseHTTPRequestHandler):
    def reply(self, code, body, ctype):
        self.send_response(code)
        self.send_header("Content-Type", ctype)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):
        url = urlparse(self.path)
        if url.path.startswith("/.well-known/openid-configuration"):
            self.reply(200, doc, "application/json")
        elif url.path == "/jwks":
            self.reply(200, json.dumps({"keys": [jwk()]}).encode(), "application/json")
        elif url.path == "/authorize":
            self.authorize(parse_qs(url.query))
        else:
            self.send_response(404)
            self.end_headers()

    def authorize(self, q):
        """Stand in for the IdP's sign-in screen.

        It does not redirect straight back, for two reasons. A real one does not
        either, and the #1160 assertion is that the browser LANDS on the IdP with
        a usable authorization request; an instant bounce would leave nothing to
        assert it against. Continuing is one click, which is what the #1191
        assertion drives."""
        code = "code-" + base64.urlsafe_b64encode(os.urandom(12)).rstrip(b"=").decode()
        codes[code] = {
            "nonce": q.get("nonce", [""])[0],
            "challenge": q.get("code_challenge", [""])[0],
        }
        back = "%s?code=%s&state=%s" % (
            q.get("redirect_uri", [""])[0], code, quote(q.get("state", [""])[0]),
        )
        body = (
            "<html><body><h1>fake idp</h1>"
            "<a id=\"continue\" href=\"%s\">Continue</a></body></html>" % back
        ).encode()
        self.reply(200, body, "text/html")

    def do_POST(self):
        url = urlparse(self.path)
        if url.path != "/token":
            self.send_response(404)
            self.end_headers()
            return
        length = int(self.headers.get("Content-Length", "0"))
        form = parse_qs(self.rfile.read(length).decode())
        staged = codes.pop(form.get("code", [""])[0], None)
        if staged is None:
            self.reply(400, b'{"error":"invalid_grant"}', "application/json")
            return
        # Prove PKCE end to end: the verifier the control plane presents must
        # hash to the challenge the browser carried to /authorize.
        digest = hashlib.sha256(form.get("code_verifier", [""])[0].encode()).digest()
        if b64u(digest) != staged["challenge"]:
            self.reply(400, b'{"error":"invalid_grant","reason":"pkce"}', "application/json")
            return
        self.reply(200, json.dumps({
            "access_token": "fake-access-token",
            "token_type": "Bearer",
            "expires_in": 600,
            "id_token": sign_id_token(staged["nonce"]),
        }).encode(), "application/json")

    def log_message(self, *a):
        pass


ctx = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
ctx.load_cert_chain(work + "/idp.crt", work + "/idp.key")
srv = http.server.ThreadingHTTPServer(("127.0.0.1", port), Handler)
srv.socket = ctx.wrap_socket(srv.socket, server_side=True)
srv.serve_forever()
PYEOF
python3 "$WORK/idp.py" "$WORK" "$IDP_PORT" >"$WORK/idp.log" 2>&1 &
PIDS+=($!)

for _ in $(seq 1 20); do
  curl -sf --cacert "$WORK/idp.crt" "${ISSUER}/.well-known/openid-configuration" >/dev/null 2>&1 && break
  sleep 1
done
curl -sf --cacert "$WORK/idp.crt" "${ISSUER}/.well-known/openid-configuration" >/dev/null \
  || { echo "FAIL: the fake IdP never served discovery; see $WORK/idp.log" >&2; exit 1; }

# tenant_claims is required by validateOIDC and is a MAP, so it has no
# environment-variable route at all: viper binds env only for the scalar leaves,
# and this one is excluded because a Google `hd` key is a dotted domain (#826).
# It comes from the file named by LEOFLOW_CONFIG, which is the same shape the
# chart mounts. Writing it here is not test scaffolding trivia; it is the whole
# reason the chart needed a ConfigMap.
cat > "$WORK/config.yaml" <<'CFGEOF'
auth:
  oidc:
    tenant_claims:
      "example.com": "default"
CFGEOF

# Migrate BEFORE either server boots. The server refuses to serve anything at all
# against an unmigrated schema (it says so and exits), so without this the login
# page is never served and the failure reads as a boot timeout. Doing it up front
# also removes the race the two servers would otherwise run: both migrate the same
# database at boot, and golang-migrate's advisory lock serializes them only if
# both get that far.
#
# `dexaflow db migrate` is NOT the tool here: it is hardcoded to the Lite dev
# database (schema leoflow_dev). The server's own schema is owned by migrations/
# and golang-migrate, the same way every k3d job in CI applies it.
command -v migrate >/dev/null 2>&1 \
  || { echo "FAIL: the golang-migrate CLI is not on PATH (go install -tags postgres github.com/golang-migrate/migrate/v4/cmd/migrate@latest)" >&2; exit 1; }
log "Migrating $(echo "$DB" | sed 's#://[^@]*@#://#')"
migrate -path "$ROOT/migrations" -database "$DB" up 2>&1 | tail -3 \
  || { echo "FAIL: migrations did not apply" >&2; exit 1; }

# The redirect URL is http, not https: this control plane listens on plain http,
# and validateRedirectURL allows http for loopback hosts precisely so a local
# flow can complete. It used to say https here, which no server ever answered;
# it went unnoticed because nothing drove the flow past /authorize.
log "Booting the control plane WITH sso on :${SSO_PORT}"
env LEOFLOW_UI_EDITION=pro \
    LEOFLOW_AUTH_PROVIDER=oidc \
    LEOFLOW_AUTH_JWT_SECRET=sso-e2e \
    LEOFLOW_CONFIG="$WORK/config.yaml" \
    LEOFLOW_AUTH_OIDC_ISSUER="$ISSUER" \
    LEOFLOW_AUTH_OIDC_CLIENT_ID=leoflow \
    LEOFLOW_AUTH_OIDC_REDIRECT_URL="http://localhost:${SSO_PORT}/api/v2/auth/oidc/callback" \
    LEOFLOW_AUTH_OIDC_TENANT_CLAIM=hd \
    LEOFLOW_AUTH_OIDC_JIT_PROVISIONING=true \
    LEOFLOW_AUTH_OIDC_DEFAULT_ROLE=viewer \
    LEOFLOW_AUTH_OIDC_BREAK_GLASS_EMAILS="$BREAK_GLASS_EMAIL" \
    LEOFLOW_BOOTSTRAP_EMAIL="$BREAK_GLASS_EMAIL" \
    LEOFLOW_BOOTSTRAP_PASSWORD="$BREAK_GLASS_PASSWORD" \
    LEOFLOW_SERVER_GRPC_TLS_CERT="$WORK/grpc.crt" \
    LEOFLOW_SERVER_GRPC_TLS_KEY="$WORK/grpc.key" \
    LEOFLOW_SERVER_HTTP_ADDR="0.0.0.0:${SSO_PORT}" \
    LEOFLOW_SERVER_METRICS_ADDR="0.0.0.0:19090" \
    LEOFLOW_DATABASE_URL="$DB" \
    SSL_CERT_FILE="$WORK/idp.crt" \
    "$ROOT/bin/leoflow-server" >"$WORK/sso-server.log" 2>&1 &
PIDS+=($!)

log "Booting a control plane WITHOUT sso on :${PLAIN_PORT}"
# Same bootstrap account as the SSO arm, deliberately: the two servers share one
# database, and BootstrapAdmin only seeds a tenant that has no users, so racing
# them with different accounts would make whichever booted first decide what the
# password is. Here the race has one outcome.
env LEOFLOW_AUTH_JWT_SECRET=plain-e2e \
    LEOFLOW_BOOTSTRAP_EMAIL="$BREAK_GLASS_EMAIL" \
    LEOFLOW_BOOTSTRAP_PASSWORD="$BREAK_GLASS_PASSWORD" \
    LEOFLOW_SERVER_HTTP_ADDR="0.0.0.0:${PLAIN_PORT}" \
    LEOFLOW_SERVER_METRICS_ADDR="0.0.0.0:19091" \
    LEOFLOW_SERVER_GRPC_ADDR="0.0.0.0:19092" \
    LEOFLOW_DATABASE_URL="$DB" \
    "$ROOT/bin/leoflow-server" >"$WORK/plain-server.log" 2>&1 &
PIDS+=($!)

wait_ready() { # <port> <name>
  for _ in $(seq 1 40); do
    if curl -sf "http://localhost:$1/api/v2/auth/login" >/dev/null 2>&1; then return 0; fi
    sleep 1
  done
  echo "FAIL: $2 never served its login page; see $WORK" >&2
  tail -20 "$WORK/$2.log" >&2 || true
  return 1
}
wait_ready "$SSO_PORT" sso-server
wait_ready "$PLAIN_PORT" plain-server

# Fail here rather than inside the browser if the break-glass account is not the
# one this run expects. BootstrapAdmin only seeds a tenant with no users, so a
# reused database that already has a different admin would otherwise surface as
# an unreadable "the identity did not change" from a login that was never going
# to succeed.
if ! curl -sf -o /dev/null -X POST "http://localhost:${PLAIN_PORT}/auth/token" \
     -H 'Content-Type: application/json' \
     -d "{\"username\":\"${BREAK_GLASS_EMAIL}\",\"password\":\"${BREAK_GLASS_PASSWORD}\"}"; then
  echo "FAIL: the break-glass account ${BREAK_GLASS_EMAIL} cannot log in. The bootstrap only seeds a tenant" >&2
  echo "      with no users, so this database probably already has a different admin. Point" >&2
  echo "      LEOFLOW_E2E_DATABASE_URL at a fresh database." >&2
  exit 1
fi

log "Asserting the SSO deployment offers the flow, signs a user in, and lets break-glass take over"
LEOFLOW_URL="http://localhost:${SSO_PORT}" LEOFLOW_IDP_ORIGIN="$ISSUER" \
  LEOFLOW_SSO_EMAIL="$SSO_EMAIL" \
  LEOFLOW_BREAK_GLASS_EMAIL="$BREAK_GLASS_EMAIL" \
  LEOFLOW_BREAK_GLASS_PASSWORD="$BREAK_GLASS_PASSWORD" \
  node "$ROOT/test/e2e/sso-login-page.js"

log "Asserting the JWT-only deployment does not advertise a route it never registered"
LEOFLOW_URL="http://localhost:${PLAIN_PORT}" LEOFLOW_EXPECT_SSO=0 \
  LEOFLOW_BREAK_GLASS_EMAIL="$BREAK_GLASS_EMAIL" \
  LEOFLOW_BREAK_GLASS_PASSWORD="$BREAK_GLASS_PASSWORD" \
  node "$ROOT/test/e2e/sso-login-page.js"

log "sso login page e2e passed"
