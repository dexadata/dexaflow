"""Shim of ``airflow.hooks.base``, the pre-3 home of ``BaseHook``."""
from airflow._names import BaseHook

__all__ = ["BaseHook"]
