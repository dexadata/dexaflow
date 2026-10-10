"""Dexaflow DAG parser.

Compiles an Airflow DAG (Python source) into the canonical Dexaflow dag.json,
without executing user task code.

The parser has no third-party runtime dependencies (ADR 0024): the Airflow
shim is bundled under ``_shim`` and the project config arrives as JSON from
the Go CLI via the ``LEOFLOW_PROJECT_CONFIG_JSON`` environment variable, so
the parser runs on a bare managed CPython — no venv, no pip, no PyYAML.
"""
__version__ = "0.1.0"
