"""Adapter contract tests: the profiles.yml Dexaflow emits for each cloud warehouse
must be accepted by that warehouse's *real* dbt adapter.

The mapper (leoflow_runtime.dbt) emits a profile dict; the unit tests in test_dbt.py
lock its shape, but only the adapter itself knows whether that shape is valid — the
right field names, the alias resolution (project→database, catalog→database,
dataset→schema), the required fields, and the auth-mode wiring. Here we feed our
emitted profile through the adapter's own credential parsing (translate_aliases +
the Credentials dataclass), which validates the shape WITHOUT connecting to a
warehouse. This is the credential-free half of the cloud-adapter assurance (#574);
a live-query gate needs real secrets and is the maintainer's (see docs/dbt.md).

Each adapter is optional: the test skips when its package is not installed, so the
default runtime suite (no cloud adapters) is unaffected. CI installs one adapter per
matrix leg — see .github/workflows/ci.yaml `dbt-adapter-contracts`.
"""
from __future__ import annotations

import json
from urllib.parse import urlencode

import pytest

from leoflow_runtime.dbt import dbt_profile_from_uri


def _conn_uri(scheme, login="", password="", host="", schema="", extra=None):
    """Build an Airflow connection URI the way Dexaflow delivers it (conn_type with
    _->-, extra as a single __extra__ JSON query param). Mirrors test_dbt._conn_uri."""
    netloc = f"{login}:{password}@" if (login or password) else ""
    netloc += host
    path = f"/{schema}" if schema else ""
    query = "?" + urlencode({"__extra__": json.dumps(extra)}) if extra else ""
    return f"{scheme.replace('_', '-')}://{netloc}{path}{query}"


def _as_credentials(cls, profile):
    """Feed a Dexaflow-emitted profile through the adapter's real credential parsing:
    drop the profile-level keys the Credentials dataclass doesn't take (type,
    threads), apply the adapter's own alias resolution, and construct. Raises exactly
    as the adapter would when dbt loads the profile — no network."""
    kwargs = {k: v for k, v in profile.items() if k not in ("type", "threads")}
    return cls(**cls.translate_aliases(kwargs))


# --- Snowflake ------------------------------------------------------------------

def _snowflake_creds():
    return pytest.importorskip("dbt.adapters.snowflake.connections").SnowflakeCredentials


def test_snowflake_password_profile_is_valid():
    cls = _snowflake_creds()
    prof = dbt_profile_from_uri(_conn_uri(
        "snowflake", login="u", password="pw", schema="ANALYTICS",
        extra={"account": "ab12345", "warehouse": "WH", "database": "DB", "role": "R"}))
    c = _as_credentials(cls, prof)
    assert c.account == "ab12345" and c.user == "u" and c.password == "pw"


def test_snowflake_key_pair_profile_is_valid():
    cls = _snowflake_creds()
    pem = "not-a-real-key"  # the adapter stores it verbatim on the dataclass
    prof = dbt_profile_from_uri(_conn_uri(
        "snowflake", login="svc", schema="ANALYTICS",
        extra={"account": "ab12345", "warehouse": "WH", "database": "DB",
               "private_key_content": pem, "private_key_passphrase": "pp"}))
    c = _as_credentials(cls, prof)
    assert c.private_key == pem and c.private_key_passphrase == "pp"
    assert not c.password  # key-pair drops the password


# --- BigQuery -------------------------------------------------------------------

def _bigquery_creds():
    return pytest.importorskip("dbt.adapters.bigquery.credentials").BigQueryCredentials


def test_bigquery_keyless_oauth_profile_is_valid():
    cls = _bigquery_creds()
    prof = dbt_profile_from_uri(_conn_uri(
        "google_cloud_platform", schema="my_dataset",
        extra={"method": "oauth", "project": "my-proj"}))
    c = _as_credentials(cls, prof)
    # project→database, dataset→schema are resolved by the adapter's aliases.
    assert str(c.method) == "oauth" and c.database == "my-proj" and c.schema == "my_dataset"


def test_bigquery_service_account_json_profile_is_valid():
    cls = _bigquery_creds()
    keyfile = json.dumps({"type": "service_account", "project_id": "p"})
    prof = dbt_profile_from_uri(_conn_uri(
        "google_cloud_platform", schema="ds",
        extra={"project": "my-proj", "keyfile_dict": keyfile}))
    c = _as_credentials(cls, prof)
    assert "service-account" in str(c.method) and c.database == "my-proj"


# --- Databricks -----------------------------------------------------------------

def _databricks_creds():
    return pytest.importorskip("dbt.adapters.databricks.credentials").DatabricksCredentials


def test_databricks_oauth_m2m_profile_is_valid():
    cls = _databricks_creds()
    prof = dbt_profile_from_uri(_conn_uri(
        "databricks", host="dbc.databricks.com", schema="analytics",
        extra={"http_path": "/sql/1.0/warehouses/abc", "catalog": "main",
               "auth_type": "oauth", "client_id": "svc", "client_secret": "sek"}))
    c = _as_credentials(cls, prof)
    # catalog→database is resolved by the adapter's aliases.
    assert c.auth_type == "oauth" and c.client_id == "svc"
    assert c.http_path == "/sql/1.0/warehouses/abc" and c.database == "main"


def test_databricks_pat_profile_is_valid():
    cls = _databricks_creds()
    prof = dbt_profile_from_uri(_conn_uri(
        "databricks", password="dapitoken", host="h", schema="s",
        extra={"http_path": "/sql/x", "catalog": "main"}))
    c = _as_credentials(cls, prof)
    assert c.token == "dapitoken" and c.http_path == "/sql/x"
