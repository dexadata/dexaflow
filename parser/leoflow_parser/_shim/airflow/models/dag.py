"""Shim of ``airflow.models.dag``, the pre-3 home of ``DAG``."""
from airflow._core import DAG

__all__ = ["DAG"]
