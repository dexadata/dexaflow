"""Shim of ``airflow.sdk.bases.sensor``."""
from airflow._names import BaseSensorOperator, PokeReturnValue

__all__ = ["BaseSensorOperator", "PokeReturnValue"]
