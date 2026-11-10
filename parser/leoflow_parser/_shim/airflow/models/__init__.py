"""Shim of ``airflow.models``: only the surface Dexaflow's parser reads.

``Variable`` is here because ``from airflow.models import Variable`` is the
pre-3 spelling that Airflow 3 still accepts (#2337).
"""
from airflow._core import Variable

__all__ = ["Variable"]
