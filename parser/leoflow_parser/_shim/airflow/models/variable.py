"""Shim of ``airflow.models.variable``: the module spelling of ``Variable``."""
from airflow._core import Variable

__all__ = ["Variable"]
