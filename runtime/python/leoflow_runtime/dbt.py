"""Map a managed Leoflow/Airflow connection URI to a dbt profile (ADR 0043, #2).

Connections are delivered to the task pod as ``AIRFLOW_CONN_<ID>`` (an Airflow
connection URI). A dbt task generates its ``profiles.yml`` from that at runtime,
so no warehouse credential is baked into the image. Supports postgres, snowflake,
bigquery, databricks (the official ``dbt-databricks`` adapter), and duckdb
(embedded, for zero-server local development).
"""

from __future__ import annotations

import json
import os
from urllib.parse import parse_qs, unquote, urlsplit


def _uri_extra(query: str) -> dict:
    """Merge direct query params and the ``__extra__`` JSON blob Leoflow carries in
    the URI into one dict."""
    parsed = {k: v[0] for k, v in parse_qs(query).items()}
    raw = parsed.pop("__extra__", None)
    if raw:
        try:
            extra = json.loads(raw)
            if isinstance(extra, dict):
                parsed.update(extra)
        except (TypeError, ValueError):
            pass
    return parsed


def _eget(extra: dict, conn_type: str, key: str, default=None):
    """Look up an extra key, trying the plain name then Airflow's
    ``extra__<conn_type>__<key>`` form."""
    if key in extra:
        return extra[key]
    return extra.get(f"extra__{conn_type}__{key}", default)


def _threads(extra: dict) -> int:
    return int(extra.get("threads", 4))


def _postgres(parts, _ct, login, password, path, extra):
    return {
        "type": "postgres", "host": parts.hostname or "", "port": parts.port or 5432,
        "user": login, "password": password, "dbname": path,
        "schema": extra.get("schema", "public"), "threads": _threads(extra),
    }


def _snowflake(_parts, ct, login, password, path, extra):
    account = _eget(extra, ct, "account")
    if not account:
        raise ValueError("snowflake connection needs `account` in its extra")
    profile = {
        "type": "snowflake", "account": account, "user": login,
        "role": _eget(extra, ct, "role"), "database": _eget(extra, ct, "database"),
        "warehouse": _eget(extra, ct, "warehouse"),
        "schema": path or _eget(extra, ct, "schema", "PUBLIC"), "threads": _threads(extra),
    }
    # Auth: key-pair (service account) is Snowflake's guidance for automation and
    # is preferred when a private key is present; otherwise fall back to a password.
    # The Airflow snowflake provider carries the key in Extra as private_key_content
    # (inline PEM) / private_key_file (path) — accept those, plus the dbt-native
    # aliases. These (and private_key_passphrase) ride the connection's free-form
    # extra pass-through, so they need no catalog field. The two forms are mutually
    # exclusive in the adapter, and password/token are dropped for key-pair.
    private_key = _eget(extra, ct, "private_key_content") or _eget(extra, ct, "private_key")
    private_key_path = _eget(extra, ct, "private_key_file") or _eget(extra, ct, "private_key_path")
    if private_key and private_key_path:
        raise ValueError(
            "snowflake connection: set only one of `private_key` or `private_key_path`"
        )
    if private_key or private_key_path:
        if private_key:
            profile["private_key"] = private_key
        else:
            profile["private_key_path"] = private_key_path
        passphrase = _eget(extra, ct, "private_key_passphrase")
        if passphrase:
            profile["private_key_passphrase"] = passphrase
    else:
        profile["password"] = password
    return profile


def _bigquery(_parts, ct, _login, _password, path, extra):
    project = _eget(extra, ct, "project")
    dataset = path or _eget(extra, ct, "dataset")
    # Keyless: method=oauth uses Application Default Credentials — the pod's
    # identity via GKE Workload Identity on Pro — so no key file is shipped.
    # `method` is a leoflow dbt-mapping selector carried in the free-form extra
    # (not a catalog field). dataset is required by the adapter; project is
    # optional (defers to the ADC environment project when absent).
    if _eget(extra, ct, "method") == "oauth":
        out = {
            "type": "bigquery", "method": "oauth",
            "dataset": dataset, "threads": _threads(extra),
        }
        if project:
            out["project"] = project
        return out
    keyfile = _eget(extra, ct, "keyfile_dict")
    if not keyfile:
        raise ValueError(
            "bigquery connection needs `keyfile_dict` in its extra "
            "(or set `method: oauth` for keyless / Workload Identity)"
        )
    return {
        "type": "bigquery", "method": "service-account-json",
        "project": project, "dataset": dataset,
        "keyfile_json": json.loads(keyfile) if isinstance(keyfile, str) else keyfile,
        "threads": _threads(extra),
    }


def _databricks(parts, ct, _login, password, path, extra):
    http_path = _eget(extra, ct, "http_path")
    if not http_path:
        raise ValueError("databricks connection needs `http_path` in its extra")
    profile = {
        "type": "databricks", "host": parts.hostname or "", "http_path": http_path,
        "catalog": _eget(extra, ct, "catalog"),
        "schema": path or _eget(extra, ct, "schema"), "threads": _threads(extra),
    }
    # Auth: service-principal OAuth M2M (client_id/client_secret) is Databricks'
    # guidance for automation and is preferred when present; otherwise fall back to
    # a Personal Access Token. dbt-databricks treats auth_type=oauth and token as
    # mutually exclusive, so emit exactly one.
    client_id = _eget(extra, ct, "client_id")
    client_secret = _eget(extra, ct, "client_secret")
    if _eget(extra, ct, "auth_type") == "oauth" or client_id or client_secret:
        if not (client_id and client_secret):
            raise ValueError(
                "databricks OAuth M2M needs both `client_id` and `client_secret` in its extra"
            )
        profile.update(auth_type="oauth", client_id=client_id, client_secret=client_secret)
    else:
        profile["token"] = password or _eget(extra, ct, "token")
    return profile


def _duckdb(_parts, ct, _login, _password, path, extra):
    # duckdb is an embedded, file-based warehouse — no host or credentials. The DB
    # file comes from the `path` extra or the URI path (empty means in-memory). Ideal
    # for zero-server local development.
    db = _eget(extra, ct, "path") or (("/" + path) if path else ":memory:")
    return {"type": "duckdb", "path": db, "threads": _threads(extra)}


# conn_type -> dbt profile mapper. The official dbt-databricks adapter is used for
# databricks (not the community dbt-spark); duckdb backs zero-server local dev.
_MAPPERS = {
    "postgres": _postgres,
    "postgresql": _postgres,
    "snowflake": _snowflake,
    "google_cloud_platform": _bigquery,
    "databricks": _databricks,
    "duckdb": _duckdb,
}


def dbt_profile_from_uri(uri: str) -> dict:
    """Map an Airflow connection URI to a dbt ``outputs.<target>`` block, dispatching
    by connection type. Supports postgres, snowflake, bigquery
    (``google_cloud_platform``), databricks, and duckdb; any other type is a loud error.
    """
    parts = urlsplit(uri)
    conn_type = parts.scheme.lower().replace("-", "_")
    mapper = _MAPPERS.get(conn_type)
    if mapper is None:
        raise ValueError(
            f"unsupported dbt adapter for connection type {conn_type!r} "
            "(supported: postgres, snowflake, bigquery, databricks, duckdb)"
        )
    extra = _uri_extra(parts.query)
    return mapper(
        parts, conn_type,
        unquote(parts.username or ""), unquote(parts.password or ""),
        unquote(parts.path.lstrip("/")), extra,
    )


def write_dbt_profile(
    conn_id: str, profile_name: str, profiles_dir: str, env=None, schema=None
) -> str:
    """Generate ``<profiles_dir>/profiles.yml`` from the ``AIRFLOW_CONN_<conn_id>``
    the agent delivered, under the dbt project's ``profile_name``. Written as JSON,
    which is valid YAML, so dbt reads it without us depending on PyYAML. Returns the
    written path; a missing connection is a loud error.
    """
    env = os.environ if env is None else env
    key = f"AIRFLOW_CONN_{conn_id.upper()}"
    uri = env.get(key)
    if not uri:
        msg = f"dbt connection {conn_id!r} was not delivered to the task ({key} is unset)"
        # Backend-aware hint (#1): when an external secrets backend is configured, a
        # missing declared connection is most often the pod identity/permissions —
        # the provider backend reports an auth failure as a miss, so it falls through
        # silently. Point at that instead of leaving a bare "unset".
        if env.get("LEOFLOW_SECRETS_BACKEND"):
            msg += (
                "; an external secrets backend is configured — if this connection "
                "should come from it, check the task pod's ServiceAccount and the "
                "backend's read permissions (an auth failure surfaces as a miss)"
            )
        raise RuntimeError(msg)
    output = dbt_profile_from_uri(uri)
    if schema:
        output["schema"] = schema  # explicit dexaflow.yaml schema wins over the URI/default
    profile = {profile_name: {"target": "dev", "outputs": {"dev": output}}}
    path = os.path.join(profiles_dir, "profiles.yml")
    # profiles.yml carries warehouse credentials (password / private key / keyfile /
    # token), so write it owner-only (0600), not the default 0644. The opener sets
    # the mode on create; chmod also covers a pre-existing file.
    with open(path, "w", encoding="utf-8", opener=lambda p, f: os.open(p, f, 0o600)) as handle:
        json.dump(profile, handle)
    os.chmod(path, 0o600)
    return path


def write_dbt_default_duckdb(profile_name: str, profiles_dir: str, db_path: str = "") -> str:
    """Write a default duckdb ``profiles.yml`` — Leoflow's zero-server local warehouse
    when a dbt group has no managed connection (Lite). An empty db_path is in-memory.
    Written as JSON (valid YAML) so dbt reads it with no PyYAML dependency.
    """
    output = {"type": "duckdb", "path": db_path or ":memory:", "threads": 4}
    profile = {profile_name: {"target": "dev", "outputs": {"dev": output}}}
    path = os.path.join(profiles_dir, "profiles.yml")
    # 0600 to match write_dbt_profile: a duckdb profile carries no secret today, but
    # keeping both profiles.yml writers owner-only avoids a future footgun if this
    # ever grows credentialed fields (and keeps the two paths consistent).
    with open(path, "w", encoding="utf-8", opener=lambda p, f: os.open(p, f, 0o600)) as handle:
        json.dump(profile, handle)
    os.chmod(path, 0o600)
    return path
