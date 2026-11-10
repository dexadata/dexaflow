"""Shim of ``airflow.providers.standard.operators.python``.

Only PythonOperator compiles. The other operators of the module are exported
so a DAG that imports one gets its own refusal from the compiler (#2451), not
a bare ImportError: branching and short-circuit need skip support Dexaflow
does not have yet, and the virtualenv and external interpreter operators are
replaced by the DAG image's dependencies."""
from airflow._core import BaseOperator


class PythonOperator(BaseOperator):
    """Name carries 'Python' -> Dexaflow 'python'."""


class BranchPythonOperator(PythonOperator):
    """Refused by the compiler (the 'Branch' marker)."""


class ShortCircuitOperator(PythonOperator):
    """Refused by the compiler (the 'ShortCircuit' marker)."""


class PythonVirtualenvOperator(PythonOperator):
    """Refused by the compiler (the 'Virtualenv' marker)."""


class BranchPythonVirtualenvOperator(PythonOperator):
    """Refused by the compiler (the 'Branch' marker)."""


class ExternalPythonOperator(PythonOperator):
    """Refused by the compiler (the 'ExternalPython' marker)."""


class BranchExternalPythonOperator(PythonOperator):
    """Refused by the compiler (the 'Branch' marker)."""


__all__ = [
    "BranchExternalPythonOperator",
    "BranchPythonOperator",
    "BranchPythonVirtualenvOperator",
    "ExternalPythonOperator",
    "PythonOperator",
    "PythonVirtualenvOperator",
    "ShortCircuitOperator",
]
