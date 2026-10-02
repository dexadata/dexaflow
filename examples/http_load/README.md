# http_load — call an HTTP endpoint via a managed Connection

This example is a **test DAG**: it exercises the connection-delivery contract
end-to-end against a real HTTP endpoint, mirroring `postgres_load` and
`redis_load`. Running it is the manual companion to the Go-side
chain-of-custody test
(`TestHTTPConnectionURIShapeIntegration`).

Uses only Python's stdlib (`urllib.request`) — no third-party HTTP client.

## What it tests

1. Admin creates an `http` Connection in the UI (host, optional basic auth,
   custom headers in Extra).
2. The control plane encrypts and stores it (ADR 0019).
3. The agent fetches the URI via gRPC and exports it as
   `AIRFLOW_CONN_HTTP_TARGET`.
4. The user task `call()` reads the env var, **strips the `__extra__` query
   param**, parses headers from it, and POSTs a JSON payload to
   `/anything` (go-httpbin's echo endpoint).
5. The task asserts the server echoed the exact payload back.

Without a Connection, `call()` falls back to a hardcoded local URI so the
example also runs in a quick demo on a developer machine.

## How to run it (Lima / subprocess executor)

### 1. Spin up an echo server

```sh
docker run --rm -d --name leoflow-httpbin \
  -p 58080:8080 \
  mccutchen/go-httpbin
```

The DAG's built-in fallback is `http://localhost:58080`, so the quick demo
on Lite/subprocess works out-of-the-box with the command above. Inside k3d,
either port-forward into the cluster or create the `http_target` Connection
(below) — the in-cluster hostname is no longer the default.

### 2. Create the Connection in the UI

Open `http://localhost:8088` → **Admin → Connections → +**.

| Field | Value |
|---|---|
| Conn Id | `http_target` |
| Conn Type | `http` |
| Host | `localhost` (host) or `host.k3d.internal` (k3d) |
| Port | `58080` |
| Schema | _(blank — HTTP has no schema)_ |
| Login | _(blank, or your basic-auth username)_ |
| Password | _(blank, or your basic-auth password)_ |
| Extra | `{"X-Tenant":"acme"}` _(any custom headers go here)_ |

Save. The password and the Extra blob are encrypted at rest.

### 3. Trigger the DAG

```sh
dexaflow lite path/to/this/example
```

In the UI: open `http_load` → **Trigger DAG**.

### 4. Verify

The DAG asserts the echo itself; a green run is the verification. The task
log prints `call: echo OK (N fields)`.

To inspect raw traffic:

```sh
docker logs leoflow-httpbin | tail
```

## Notes that make this connector different

- **The Schema field is normally blank.** HTTP has no schema/db namespace.
  Path prefixes (e.g. `/v2/...`) are typically appended by the operator
  / DAG, not stored in the Connection.
- **Headers go in Extra**, NOT in the URI itself. The URI builder packs
  Extra under a `__extra__` query parameter; the example DAG strips it
  before constructing the actual request URL (otherwise every request
  would carry `?__extra__=...` and your server logs would be noisy).
- **Basic auth lives in the URI**. The URI builder percent-escapes
  reserved characters in the password; the example decodes via
  `urllib.parse.urlparse(...).username` / `.password`.
- **HTTPS is a different Conn Type.** `https` uses the same Connection
  shape, just with TLS. See the cookbook page.
- **Tier 1 in CI** (#162) — the chain-of-custody assertion is the Go
  integration test alone; the example DAG is for operators to validate
  their own deployment. Adding a Python httpbin-driven test to CI is a
  future possibility but not gated here.

## What can go wrong

- **AIRFLOW_CONN_HTTP_TARGET not set** → the DAG falls back to the
  hardcoded URI. The log line tells you which path was taken.
- **The `__extra__` query param leaks into the real request URL**
  (regression) → server logs will show it; the strip-Extra logic in
  `_resolve_target()` exists to prevent this.
- **Self-signed TLS cert (https://)** → `urllib.request.urlopen` will
  reject it without an explicit context. The example doesn't cover this;
  use a real cert or pass `ssl.SSLContext(...)`.

## Related

- `docs/connections/http.md` — the cookbook entry for the http connector
  (URI shape, Bearer auth via Extra, http-vs-https).
- ADR 0019 — secret encryption at rest.
- ADR 0021 — agent secret delivery (`AIRFLOW_CONN_<CONN_ID>`).
- Issue #142 — connector cookbook umbrella.
- Issue #75 — http connector umbrella.
