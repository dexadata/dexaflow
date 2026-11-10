"""Shim of ``airflow.sdk.exceptions``, the Airflow 3 home of the task
exceptions; the same classes as ``airflow.exceptions`` (#2451)."""
from airflow.exceptions import *  # noqa: F403
from airflow.exceptions import __all__  # noqa: F401
