#!/usr/bin/env python3
"""Generate the connector catalog from a real Apache Airflow install.

This is the single source of truth for `internal/connectors/catalog.json`: it asks
Airflow's OWN serializer (`HookMetaService.hook_meta_data()`, the exact code path
behind `/ui/connections/hook_meta`) for the connection-form metadata, and
`ProvidersManager` for the pip package each conn_type comes from. The output is the
precise shape the embedded Airflow 3.2 SPA renders via its FlexibleForm component —
so we never hand-translate WTForms widgets and never drift from upstream.

Run it in an environment with apache-airflow + the target providers installed:

    python3 -m venv .venv-conngen && . .venv-conngen/bin/activate
    pip install "apache-airflow==3.2.1" \
        apache-airflow-providers-postgres apache-airflow-providers-amazon ...
    python scripts/gen_connectors.py            # writes internal/connectors/catalog.json
    python scripts/gen_connectors.py /tmp/x.json # or an explicit path

The catalog grows by installing more providers and re-running — the script
introspects whatever is present. Core conn types with no provider package
(generic, email) are emitted with pip_package=null: usable in the form dropdown,
but not resolvable by the `connectors:` sugar.

The output is written to a FILE (not stdout) on purpose: Airflow's structlog
emits deprecation warnings to stdout on import, which would corrupt a piped JSON.
"""
from __future__ import annotations

import json
import os
import sys

DEFAULT_OUT = os.path.join(
    os.path.dirname(os.path.dirname(os.path.abspath(__file__))),
    "internal",
    "connectors",
    "catalog.json",
)


# Dexaflow-native connection types absent from the Airflow providers introspection.
# duckdb is an embedded, file-based warehouse — the zero-server backend for local dbt
# development (ADR 0042/0043). Kept here so a catalog regeneration preserves it.
_NATIVE_CONNECTIONS: list[dict] = [
    {
        "connection_type": "duckdb",
        "hook_name": "DuckDB (local file)",
        "hook_class_name": None,
        "default_conn_name": "duckdb_default",
        # dbt-duckdb is installed via the DAG's dependencies:, not the connection catalog
        "pip_package": None,
        "standard_fields": {
            "description": None,
            "host": {"hidden": True, "placeholder": None, "title": None},
            "login": {"hidden": True, "placeholder": None, "title": None},
            "password": {"hidden": True, "placeholder": None, "title": None},
            "port": {"hidden": True, "placeholder": None, "title": None},
            "schema": {"hidden": True, "placeholder": None, "title": None},
        },
        "extra_fields": {
            "path": {
                "description": "Path to the DuckDB database file (empty = in-memory).",
                "schema": {"title": "Path", "type": ["string", "null"]},
                "source": None,
                "value": None,
            }
        },
    }
]


def build() -> list[dict]:
    from airflow.api_fastapi.core_api.services.ui.connections import HookMetaService
    from airflow.providers_manager import ProvidersManager

    pm = ProvidersManager()
    conn_type_to_package: dict[str, str] = {}
    for conn_type, hook_info in pm.hooks.items():
        if hook_info and getattr(hook_info, "package_name", None):
            conn_type_to_package[conn_type] = hook_info.package_name

    rows: list[dict] = []
    for meta in HookMetaService.hook_meta_data():
        d = meta.model_dump()
        conn_type = d["connection_type"]
        rows.append(
            {
                "connection_type": conn_type,
                "hook_name": d.get("hook_name"),
                "hook_class_name": d.get("hook_class_name"),
                "default_conn_name": d.get("default_conn_name"),
                "pip_package": conn_type_to_package.get(conn_type),
                "standard_fields": d.get("standard_fields"),
                "extra_fields": d.get("extra_fields") or {},
            }
        )
    rows.extend(_NATIVE_CONNECTIONS)
    rows.sort(key=lambda r: r["connection_type"])
    return rows


def main() -> int:
    out_path = sys.argv[1] if len(sys.argv) > 1 else DEFAULT_OUT
    rows = build()
    with open(out_path, "w", encoding="utf-8") as fh:
        json.dump(rows, fh, indent=2, sort_keys=True, default=str)
        fh.write("\n")
    sys.stderr.write(f"generated {len(rows)} connector entries -> {out_path}\n")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
