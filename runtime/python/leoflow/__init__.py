"""The authoring package under its name before the rename.

DAGs written for Leoflow import ``from leoflow import dbt_group``; runner.py
re-imports them in every task pod. This module re-exports ``dexaflow``, so both
names refer to the same objects and those DAGs keep running unchanged.
"""
from __future__ import annotations

from dexaflow import _DbtGroup, dbt_group

# _DbtGroup is re-exported for code that checks the placeholder's type.
__all__ = ["_DbtGroup", "dbt_group"]
