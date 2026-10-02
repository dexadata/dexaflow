---
# --- AUTO redirect aliases (build_redirects.py) — do not edit by hand ---
aliases:
  - /connections/http.html
# --- end AUTO redirect aliases ---
title: HTTP connection
linkTitle: HTTP
weight: 230
description: HTTP connection
---

Connect a task to an external HTTP / REST API: the base URL, optional basic
auth, and any custom headers (including `Authorization: Bearer ...`) live in
a managed Connection. The DAG reads the URI and constructs requests against
it.

## URI shape

```
http://[<user>:<password>@]<host>[:<port>][/]?__extra__=<json>
```

| Connection fields | URI |
|---|---|
| host=`api.example.com` port=`443` | `http://api.example.com:443` |
| host=`api` login=`etl` password=`s3cret` | `http://etl:s3cret@api` |
| host=`api` extra=`{"X-Tenant":"acme"}` | `http://api?__extra__=%7B%22X-Tenant%22%3A%22acme%22%7D` |

Two non-obvious things:

1. **The `__extra__` query parameter is delivery metadata**, not part of
   the request URL. User code must strip it before building the actual
   request. The example DAG (`examples/http_load`) demonstrates the
   correct strip+parse pattern with `urllib.parse`.
2. **HTTP has no schema/db namespace.** Leave the Schema field blank.
   Path prefixes like `/v2/...` are typically the operator's concern,
   appended at request time — not stored on the Connection.

## Fields the UI asks for

| Field | Required | Notes |
|---|---|---|
| Conn Id | yes | e.g. `http_target`. Exported as `AIRFLOW_CONN_HTTP_TARGET`. |
| Conn Type | yes | `http` for plain HTTP, `https` for TLS. |
| Host | yes | Hostname (no scheme, no port). |
| Port | optional | Default depends on Conn Type (80 / 443). |
| Schema | no | Leave blank. |
| Login | optional | Basic-auth username. Encrypted at rest with the password. |
| Password | optional | Basic-auth password. Percent-escaped in the URI; user code recovers it with `urllib.parse`. |
| Extra | optional | JSON for custom headers, e.g. `{"Authorization":"Bearer <token>","X-Tenant":"acme"}`. Encrypted at rest. |

## How user code reads it

Two-step pattern: parse the URI, strip `__extra__`, build the request URL.

```python
import json, urllib.request
from urllib.parse import parse_qs, urlparse

raw = os.environ["AIRFLOW_CONN_HTTP_TARGET"]
parsed = urlparse(raw)
headers = {}
qs = parse_qs(parsed.query)
if "__extra__" in qs:
    headers = json.loads(qs["__extra__"][0])

base = f"{parsed.scheme}://{parsed.hostname}"
if parsed.port:
    base += f":{parsed.port}"

req = urllib.request.Request(f"{base}/v2/foo", headers=headers)
if parsed.username:
    import base64
    token = base64.b64encode(f"{parsed.username}:{parsed.password}".encode()).decode()
    req.add_header("Authorization", f"Basic {token}")

with urllib.request.urlopen(req) as resp:
    body = resp.read()
```

## Authentication patterns

- **Bearer token** — set `Extra = {"Authorization":"Bearer <token>"}`.
  The Extra blob is encrypted at rest (ADR 0019).
- **Basic auth** — populate Login + Password. The URI carries them in the
  `user:password@host` form, percent-escaped.
- **API key in a custom header** — set `Extra =
  {"X-API-Key":"<value>"}`.
- **OAuth2** — out of scope for the Connection itself; the DAG performs
  the token exchange and caches the token (in XCom or Variables). The
  Connection carries the client secret in Extra.

## TLS / `https://`

For HTTPS, set Conn Type to `https`. The URI builder renders the scheme
accordingly; `urllib.request.urlopen` handles TLS via the system trust
store. For self-signed certs, pass an explicit `ssl.SSLContext` — the
example DAG doesn't cover this, see the Python `ssl` docs.

## Example DAG

[`examples/http_load`](https://github.com/dexadata/leoflow/tree/main/examples/http_load) calls a local `go-httpbin`
echo server and asserts the JSON round-trips. Stdlib only — no `requests`
dep. The example's
[README](https://github.com/dexadata/leoflow/tree/main/examples/http_load/README.md)
walks through Connection setup and verification.

## Lite vs Pro caveats

- **Lite (subprocess)** runs the task on the host. Outbound HTTP follows
  the host's network. CORS, proxy, and DNS are the host's problem.
- **Lite (k3d)** runs the task in a pod. Outbound calls go through the
  k3d cluster network; for host-side services use `host.k3d.internal`.
- **Pro (Kubernetes)** typical pattern is a NetworkPolicy gating outbound
  traffic and a sidecar proxy (Envoy / Istio) for retries and mTLS. The
  Dexaflow Connection itself is unchanged.

## Tier 1 integration test

`TestHTTPConnectionURIShapeIntegration` (in `internal/storage/`) covers
this entry. It validates the full delivery chain — Repository ->
SecretConnectionURIs -> URI shape -> basic auth round-trip -> `__extra__`
round-trip — without standing up a real HTTP server. The example DAG is
the operator's manual verification step.

Tier 1 cost is zero — no service container needed (see
[#162](https://github.com/dexadata/leoflow/issues/162)).

## Troubleshooting

| Symptom | Cause | Fix |
|---|---|---|
| `urllib.error.HTTPError: 401` | Auth header missing or stale | Confirm the Connection has Login+Password or the right Extra header. Re-fetch the token if Bearer. |
| Server logs `?__extra__=...` in the URL | DAG didn't strip the delivery metadata | Add the `parse_qs / strip __extra__` step shown above. |
| `ssl.SSLCertVerificationError` | Self-signed cert | Use a real cert (Let's Encrypt) or pass an `ssl.SSLContext` with the CA loaded. |
| `urllib.error.URLError: <unreachable>` | k3d pod can't reach the host | Use `host.k3d.internal` instead of `localhost`. |
| Bearer token leaks into logs | DAG prints the env var | Don't log `AIRFLOW_CONN_HTTP_TARGET`; log a hash or "via managed Connection" instead. |

## Related

- ADR 0019 — secret encryption at rest.
- ADR 0021 — agent secret delivery (`AIRFLOW_CONN_<CONN_ID>`).
- #75 — http connector umbrella.
- #138 — chain-of-custody contract test (http has its own dedicated test).
- #142 — connector cookbook umbrella.
- #162 — tiered integration-test pipeline (http is Tier 1).
