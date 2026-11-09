"""Shim of ``airflow.sdk.bases.operator``."""
from airflow._core import BaseOperator, chain, cross_downstream

__all__ = ["BaseOperator", "chain", "cross_downstream"]
