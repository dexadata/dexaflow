"""Tests for the managed-connection -> dbt profile mapping (ADR 0043, #2)."""
from __future__ import annotations

import json
from urllib.parse import urlencode

import pytest

from leoflow_runtime.dbt import dbt_profile_from_uri, write_dbt_profile

# Stand-in for a private key. The mapper passes the value through verbatim (it does
# not parse it), so a real PEM body is unnecessary here — and a real PEM armor block
# would trip secret scanners (gitleaks) on a test fixture. The real PEM format users
# provide is documented in the connection docs.
FAKE_PRIVATE_KEY = "test-private-key-material-xxxx-not-a-real-key"


def _conn_uri(scheme, login="", password="", host="", port=None, schema="", extra=None):
    """Build an Airflow connection URI the way Dexaflow delivers it (conn_type with
    _->-, extra as a single __extra__ JSON query param)."""
    netloc = ""
    if login or password:
        netloc += f"{login}:{password}@"
    netloc += host + (f":{port}" if port else "")
    path = f"/{schema}" if schema else ""
    query = "?" + urlencode({"__extra__": json.dumps(extra)}) if extra else ""
    return f"{scheme.replace('_', '-')}://{netloc}{path}{query}"


def test_postgres_uri_maps_to_dbt_profile():
    uri = "postgres://user:pass@db.example.com:5432/warehouse?schema=analytics&threads=8"
    assert dbt_profile_from_uri(uri) == {
        "type": "postgres",
        "host": "db.example.com",
        "port": 5432,
        "user": "user",
        "password": "pass",
        "dbname": "warehouse",
        "schema": "analytics",
        "threads": 8,
    }


def test_postgres_defaults_port_schema_threads():
    out = dbt_profile_from_uri("postgresql://u:p@h/db")
    assert out["type"] == "postgres"
    assert out["port"] == 5432
    assert out["dbname"] == "db"
    assert out["schema"] == "public"
    assert out["threads"] == 4


def test_url_encoded_credentials_are_decoded():
    out = dbt_profile_from_uri("postgres://u%40corp:p%3Aword@h:5432/db")
    assert out["user"] == "u@corp"
    assert out["password"] == "p:word"


def test_duckdb_uri_maps_to_dbt_profile():
    # duckdb is embedded/file-based — the URI path is the DB file (for local dev).
    out = dbt_profile_from_uri("duckdb:///tmp/wh.duckdb?threads=2")
    assert out == {"type": "duckdb", "path": "/tmp/wh.duckdb", "threads": 2}


def test_duckdb_defaults_to_in_memory():
    out = dbt_profile_from_uri("duckdb://")
    assert out["type"] == "duckdb"
    assert out["path"] == ":memory:"
    assert out["threads"] == 4


def test_default_duckdb_profile_is_written(tmp_path):
    from leoflow_runtime.dbt import write_dbt_default_duckdb

    db = str(tmp_path / "wh.duckdb")
    path = write_dbt_default_duckdb("shop", str(tmp_path), db)
    prof = json.load(open(path))
    assert prof == {"shop": {"target": "dev", "outputs": {"dev": {
        "type": "duckdb", "path": db, "threads": 4}}}}


def test_default_duckdb_defaults_to_memory(tmp_path):
    from leoflow_runtime.dbt import write_dbt_default_duckdb

    path = write_dbt_default_duckdb("shop", str(tmp_path), "")
    prof = json.load(open(path))
    assert prof["shop"]["outputs"]["dev"]["path"] == ":memory:"


def test_profile_step_never_writes_to_cwd(tmp_path, monkeypatch):
    # #882: with DBT_PROFILES_DIR unset, the profile step must NOT write profiles.yml
    # into the CWD. In Lite the CWD is the dbt project in the user's working tree, and
    # profiles.yml carries the connection secret in clear — writing it there clobbers
    # the repo's versioned file (a git-add from leaking a credential). It goes to a
    # private scratch instead (asserted in test_main.py::test_dbt_profiles_dir_never_cwd),
    # and never ~/.dbt. (Superseded the old "writes to cwd" regression, which encoded
    # the leaky behavior this fixes.)
    from leoflow_runtime.__main__ import main

    monkeypatch.chdir(tmp_path)
    monkeypatch.delenv("DBT_PROFILES_DIR", raising=False)
    assert main(["--dbt-default-duckdb", "shop"]) == 0
    assert not (tmp_path / "profiles.yml").exists(), (
        "must NOT write profiles.yml into the CWD (#882)"
    )


def test_unsupported_adapter_is_a_loud_error():
    with pytest.raises(ValueError):
        dbt_profile_from_uri("mysql://u:p@h/db")


def test_snowflake_uri_maps_to_dbt_profile():
    uri = _conn_uri(
        "snowflake", login="user", password="pass", schema="analytics",
        extra={"account": "ab12345", "warehouse": "WH", "database": "DB", "role": "TRANSFORMER"},
    )
    assert dbt_profile_from_uri(uri) == {
        "type": "snowflake", "account": "ab12345", "user": "user", "password": "pass",
        "role": "TRANSFORMER", "database": "DB", "warehouse": "WH",
        "schema": "analytics", "threads": 4,
    }


def test_snowflake_key_pair_auth_maps_to_private_key():
    # Key-pair (service principal) is Snowflake's guidance for automation. When a
    # private key is present it wins over a password and emits `private_key`
    # (inline PEM), dropping `password`/`token`. `user` is still required.
    pem = FAKE_PRIVATE_KEY
    # private_key_content is the Airflow snowflake provider's Extra field.
    uri = _conn_uri(
        "snowflake", login="svc_user", schema="analytics",
        extra={"account": "ab12345", "warehouse": "WH", "database": "DB",
               "private_key_content": pem, "private_key_passphrase": "sekret"},
    )
    out = dbt_profile_from_uri(uri)
    assert out["type"] == "snowflake"
    assert out["user"] == "svc_user"
    assert out["private_key"] == pem
    assert out["private_key_passphrase"] == "sekret"
    assert "password" not in out and "token" not in out


def test_snowflake_key_pair_dbt_native_alias():
    # The dbt-native name `private_key` is accepted as an alias of the provider's
    # private_key_content (for a connection whose Extra was written by hand).
    pem = FAKE_PRIVATE_KEY
    uri = _conn_uri("snowflake", login="u", extra={"account": "a", "private_key": pem})
    assert dbt_profile_from_uri(uri)["private_key"] == pem


def test_snowflake_key_pair_path_variant():
    # private_key_file is the Airflow provider's path field.
    uri = _conn_uri("snowflake", login="svc",
                    extra={"account": "a", "private_key_file": "/keys/rsa_key.p8"})
    out = dbt_profile_from_uri(uri)
    assert out["private_key_path"] == "/keys/rsa_key.p8"
    assert "private_key" not in out and "password" not in out
    assert "private_key_passphrase" not in out  # omitted when unset


def test_snowflake_key_pair_path_dbt_native_alias():
    # private_key_path is the dbt-native alias of the provider's private_key_file.
    uri = _conn_uri("snowflake", login="u", extra={"account": "a", "private_key_path": "/k.p8"})
    assert dbt_profile_from_uri(uri)["private_key_path"] == "/k.p8"


def test_snowflake_key_pair_via_airflow_prefixed_extra():
    # Legacy Airflow export form extra__<conn_type>__<key> must also deliver the key.
    pem = FAKE_PRIVATE_KEY
    uri = _conn_uri("snowflake", login="u",
                    extra={"account": "a", "extra__snowflake__private_key_content": pem})
    assert dbt_profile_from_uri(uri)["private_key"] == pem


def test_bigquery_keyless_via_airflow_prefixed_extra():
    # The keyless selector must also arrive via the prefixed extra form.
    uri = _conn_uri("google_cloud_platform", schema="ds",
                    extra={"extra__google_cloud_platform__method": "oauth"})
    out = dbt_profile_from_uri(uri)
    assert out["method"] == "oauth" and "keyfile_json" not in out


def test_snowflake_rejects_both_private_key_forms():
    # The adapter errors if both inline and path keys are set; reject early.
    pem = FAKE_PRIVATE_KEY
    uri = _conn_uri("snowflake", extra={
        "account": "a", "private_key_content": pem, "private_key_file": "/k.p8"})
    with pytest.raises(ValueError):
        dbt_profile_from_uri(uri)


def test_snowflake_password_still_works():
    # Backward compatibility: no private key → password auth, no key-pair keys.
    uri = _conn_uri("snowflake", login="u", password="p", extra={"account": "a"})
    out = dbt_profile_from_uri(uri)
    assert out["password"] == "p"
    assert "private_key" not in out and "private_key_path" not in out


def test_bigquery_keyless_oauth_maps_to_adc():
    # Keyless: method=oauth uses Application Default Credentials (GKE Workload
    # Identity on Pro) — no key file shipped. dataset required, project optional.
    uri = _conn_uri("google_cloud_platform", schema="my_dataset",
                    extra={"method": "oauth", "project": "my-proj"})
    out = dbt_profile_from_uri(uri)
    assert out == {"type": "bigquery", "method": "oauth",
                   "project": "my-proj", "dataset": "my_dataset", "threads": 4}
    assert "keyfile_json" not in out


def test_bigquery_keyless_project_optional():
    uri = _conn_uri("google_cloud_platform", schema="ds", extra={"method": "oauth"})
    out = dbt_profile_from_uri(uri)
    assert out["method"] == "oauth" and out["dataset"] == "ds"
    assert "project" not in out  # deferred to the ADC environment project


def test_bigquery_uri_maps_to_dbt_profile():
    keyfile = '{"type": "service_account", "project_id": "p"}'
    uri = _conn_uri("google_cloud_platform", schema="my_dataset",
                    extra={"project": "my-proj", "keyfile_dict": keyfile})
    out = dbt_profile_from_uri(uri)
    assert out["type"] == "bigquery"
    assert out["method"] == "service-account-json"
    assert out["project"] == "my-proj"
    assert out["dataset"] == "my_dataset"
    assert out["keyfile_json"] == {"type": "service_account", "project_id": "p"}


def test_databricks_uri_maps_to_dbt_profile():
    uri = _conn_uri(
        "databricks", password="dapitoken", host="dbc.databricks.com", schema="analytics",
        extra={"http_path": "/sql/1.0/warehouses/abc", "catalog": "main"},
    )
    assert dbt_profile_from_uri(uri) == {
        "type": "databricks", "host": "dbc.databricks.com", "http_path": "/sql/1.0/warehouses/abc",
        "token": "dapitoken", "catalog": "main", "schema": "analytics", "threads": 4,
    }


def test_databricks_oauth_m2m_maps_to_service_principal_profile():
    # Service-principal OAuth M2M (client_id/client_secret) is Databricks' guidance
    # for automation. When present it wins over a PAT and emits auth_type=oauth
    # INSTEAD of token (the two are mutually exclusive in dbt-databricks).
    uri = _conn_uri(
        "databricks", host="dbc.databricks.com", schema="analytics",
        extra={
            "http_path": "/sql/1.0/warehouses/abc", "catalog": "main",
            "auth_type": "oauth", "client_id": "svc-123", "client_secret": "sekret",
        },
    )
    assert dbt_profile_from_uri(uri) == {
        "type": "databricks", "host": "dbc.databricks.com", "http_path": "/sql/1.0/warehouses/abc",
        "auth_type": "oauth", "client_id": "svc-123", "client_secret": "sekret",
        "catalog": "main", "schema": "analytics", "threads": 4,
    }


def test_databricks_oauth_selected_by_credentials_without_explicit_auth_type():
    # client_id/client_secret present (no explicit auth_type) selects OAuth too, and
    # a stray PAT is ignored in favor of the service principal.
    uri = _conn_uri(
        "databricks", password="dapi-ignored", host="h",
        extra={"http_path": "/sql/x", "client_id": "svc", "client_secret": "sek"},
    )
    out = dbt_profile_from_uri(uri)
    assert out["auth_type"] == "oauth"
    assert out["client_id"] == "svc" and out["client_secret"] == "sek"
    assert "token" not in out


def test_databricks_oauth_needs_both_client_id_and_secret():
    uri = _conn_uri("databricks", host="h",
                    extra={"http_path": "/sql/x", "client_id": "svc"})  # secret missing
    with pytest.raises(ValueError):
        dbt_profile_from_uri(uri)


def test_databricks_pat_still_works():
    # Backward compatibility: a plain PAT connection keeps emitting token, no OAuth keys.
    uri = _conn_uri("databricks", password="dapitoken", host="h",
                    extra={"http_path": "/sql/x"})
    out = dbt_profile_from_uri(uri)
    assert out["token"] == "dapitoken"
    assert "auth_type" not in out and "client_id" not in out


def test_airflow_prefixed_extra_keys_are_accepted():
    # Older Airflow exports extra as extra__<conn_type>__<key>; accept both forms.
    uri = _conn_uri("databricks", password="t", host="h",
                    extra={"extra__databricks__http_path": "/sql/x"})
    assert dbt_profile_from_uri(uri)["http_path"] == "/sql/x"


def test_snowflake_missing_account_is_loud():
    # A misconfigured cloud connection must fail loudly, not produce a broken profile.
    uri = _conn_uri("snowflake", login="u", password="p", extra={"warehouse": "WH"})
    with pytest.raises(ValueError):
        dbt_profile_from_uri(uri)


def test_bigquery_missing_keyfile_is_loud():
    uri = _conn_uri("google_cloud_platform", schema="ds", extra={"project": "p"})
    with pytest.raises(ValueError):
        dbt_profile_from_uri(uri)


def test_databricks_missing_http_path_is_loud():
    uri = _conn_uri("databricks", password="t", host="h", extra={"catalog": "main"})
    with pytest.raises(ValueError):
        dbt_profile_from_uri(uri)


def test_malformed_extra_degrades_to_missing_field_error():
    # A non-JSON __extra__ is ignored (not a crash); the normal missing-required-field
    # error then surfaces, rather than a confusing JSON parse traceback.
    with pytest.raises(ValueError):
        dbt_profile_from_uri("databricks://t@h?__extra__=not-json")  # http_path missing


def test_write_dbt_profile_from_env(tmp_path, monkeypatch):
    monkeypatch.setenv("AIRFLOW_CONN_WAREHOUSE_PG", "postgres://u:p@h:5432/wh?schema=analytics")
    path = write_dbt_profile("warehouse_pg", "analytics", str(tmp_path))
    # JSON is valid YAML, so dbt reads what we write; assert the structure
    data = json.loads((tmp_path / "profiles.yml").read_text())
    assert path.endswith("profiles.yml")
    assert data["analytics"]["target"] == "dev"
    out = data["analytics"]["outputs"]["dev"]
    assert out["type"] == "postgres"
    assert out["dbname"] == "wh"
    assert out["schema"] == "analytics"


def test_write_dbt_profile_missing_connection_is_loud(tmp_path):
    with pytest.raises(RuntimeError):
        write_dbt_profile("absent", "p", str(tmp_path), env={})


def test_write_dbt_profile_schema_override(tmp_path, monkeypatch):
    # the connection has no schema, which would default to public; an explicit
    # schema (from dexaflow.yaml) wins so models land where the team expects.
    monkeypatch.setenv("AIRFLOW_CONN_WH", "postgres://u:p@h/db")
    write_dbt_profile("wh", "p", str(tmp_path), schema="marts")
    data = json.loads((tmp_path / "profiles.yml").read_text())
    assert data["p"]["outputs"]["dev"]["schema"] == "marts"


def test_write_dbt_profile_missing_conn_is_backend_aware(tmp_path):
    # With an external secrets backend configured, a missing declared connection is
    # most often the pod identity/permissions (the provider backend reports an auth
    # failure as a miss). The error must point there instead of a bare "unset" (#1).
    with pytest.raises(RuntimeError) as e:
        write_dbt_profile(
            "warehouse", "shop", str(tmp_path),
            env={"LEOFLOW_SECRETS_BACKEND": "some.Backend"},
        )
    msg = str(e.value)
    assert "was not delivered" in msg
    assert "ServiceAccount" in msg


def test_write_dbt_profile_missing_conn_no_backend_no_hint(tmp_path):
    # With no backend configured, the message stays the plain vault-only form.
    with pytest.raises(RuntimeError) as e:
        write_dbt_profile("warehouse", "shop", str(tmp_path), env={})
    msg = str(e.value)
    assert "was not delivered" in msg
    assert "ServiceAccount" not in msg


def test_write_dbt_profile_is_owner_only(tmp_path):
    # profiles.yml carries warehouse credentials, so it must be written 0600
    # (owner-only), not the default world-readable 0644 (#852 security review).
    import os
    import stat

    from leoflow_runtime.dbt import write_dbt_profile

    path = write_dbt_profile(
        "warehouse", "shop", str(tmp_path),
        env={"AIRFLOW_CONN_WAREHOUSE": "postgres://u:p@h:5432/db"},
    )
    mode = stat.S_IMODE(os.stat(path).st_mode)
    assert mode == 0o600, f"profiles.yml mode = {oct(mode)}, want 0o600"
