"""http_load — call an external HTTP endpoint via a managed Connection.

The base URL and auth come from a managed Dexaflow Connection injected as
AIRFLOW_CONN_HTTP_TARGET (create it in Admin -> Connections). The DAG echoes a
small payload via go-httpbin's /anything endpoint and asserts the round-trip,
which is the simplest verification step for an HTTP connector.

Falls back to a local URI for a quick run on a developer machine.
"""
from __future__ import annotations

import json
import os
import urllib.error
import urllib.request
from urllib.parse import parse_qs, urlparse

from airflow.sdk import DAG, task


def _resolve_target() -> tuple[str, dict[str, str], tuple[str, str] | None]:
    """Return (base_url, extra_headers, basic_auth) from the Connection URI.

    The URI shape is `http://user:password@host:port/?__extra__=<json>`.
    Strip __extra__ (it is delivery metadata, not part of the request URL),
    pull headers out of it, and surface basic auth separately.
    """
    # The fallback targets `localhost:58080` because that's where the README
    # tells operators to run go-httpbin on Lite/subprocess (the quick-demo
    # path). On k3d, port-forward into the cluster or — preferred — create the
    # AIRFLOW_CONN_HTTP_TARGET Connection. The previous default
    # (`host.k3d.internal:58080`) only resolved *inside* k3d networking and
    # surfaced as `gaierror Errno 8` on Lite/macOS — see #348.
    raw = os.environ.get("AIRFLOW_CONN_HTTP_TARGET") or os.environ.get(
        "HTTP_TARGET_URI", "http://localhost:58080"
    )
    parsed = urlparse(raw)
    qs = parse_qs(parsed.query)
    headers: dict[str, str] = {}
    if "__extra__" in qs:
        try:
            blob = json.loads(qs["__extra__"][0])
        except json.JSONDecodeError:
            blob = {}
        if isinstance(blob, dict):
            headers = {str(k): str(v) for k, v in blob.items()}
    auth = None
    if parsed.username:
        auth = (parsed.username, parsed.password or "")
    netloc = parsed.hostname or ""
    if parsed.port:
        netloc = f"{netloc}:{parsed.port}"
    base = f"{parsed.scheme}://{netloc}"
    return base, headers, auth


@task
def call() -> dict[str, str]:
    base, headers, auth = _resolve_target()
    # The diagnostic prints only which delivery path was taken (Connection vs
    # fallback) — never the URL itself, since the env var may carry basic-auth
    # in `user:password@host` form. Operators can see the configured host in
    # the Admin -> Connections UI.
    src = "managed Connection http_target" if os.environ.get("AIRFLOW_CONN_HTTP_TARGET") else "fallback URI"
    print(f"call: via {src}")
    payload = {"name": "leoflow", "value": "42"}
    req = urllib.request.Request(
        f"{base}/anything",
        method="POST",
        data=json.dumps(payload).encode("utf-8"),
        headers={"Content-Type": "application/json", **headers},
    )
    if auth is not None:
        import base64

        token = base64.b64encode(f"{auth[0]}:{auth[1]}".encode()).decode()
        req.add_header("Authorization", f"Basic {token}")
    try:
        with urllib.request.urlopen(req, timeout=10) as resp:  # noqa: S310 - example DAG
            body = json.loads(resp.read().decode("utf-8"))
    except urllib.error.URLError as err:
        # Most common cause in a quick demo: nothing is listening on
        # localhost:58080. Point operators at the README's setup steps
        # (docker run mccutchen/go-httpbin) and at the optional Connection.
        hint = (
            "is anything listening on the target? "
            "Start the echo server (`docker run --rm -d --name leoflow-httpbin "
            "-p 58080:8080 mccutchen/go-httpbin`) or configure the "
            "`http_target` Connection — see examples/http_load/README.md"
        )
        raise RuntimeError(f"http call failed ({base}/anything): {err} — {hint}") from err
    echoed = body.get("json")
    if echoed != payload:
        raise AssertionError(f"echo mismatch: sent={payload}, echoed={echoed}")
    print(f"call: echo OK ({len(echoed)} fields)")
    return echoed


with DAG("http_load", schedule=None, catchup=False, tags=["example"]):
    call()
