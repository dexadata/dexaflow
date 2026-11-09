"""Shim of ``airflow.utils.trigger_rule``, the pre-3 home of ``TriggerRule``."""
from airflow._names import TriggerRule

__all__ = ["TriggerRule"]
