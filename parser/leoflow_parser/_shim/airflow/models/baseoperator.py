"""Shim of ``airflow.models.baseoperator``: the pre-3 home of ``chain`` and
``cross_downstream``, which Airflow 3 still accepts with a deprecation warning
(#2337). ``airflow.sdk`` is the canonical spelling."""
from airflow._core import BaseOperator, chain, cross_downstream

__all__ = ["BaseOperator", "chain", "cross_downstream"]
