"""Shim of ``airflow.utils.edgemodifier``: the pre-3 home of ``Label`` (#2337).
``airflow.sdk`` is the canonical spelling."""
from airflow._core import Label

__all__ = ["Label"]
