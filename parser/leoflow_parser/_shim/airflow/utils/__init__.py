"""Shim of `airflow.utils`.

It holds ``edgemodifier`` (the pre-3 home of ``Label``) and nothing else, so an
import of ``airflow.utils.task_group`` fails on that module name, which the
compiler turns into the task group advice (#2336), instead of on
``airflow.utils`` itself, which says nothing about what was imported.
"""
