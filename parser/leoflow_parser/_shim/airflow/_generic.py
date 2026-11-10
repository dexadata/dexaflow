"""Generic provider-operator capture (ADR 0040, Phase A).

A meta-path finder that synthesizes any `airflow.providers.<x>.{operators,sensors,
transfers}.<...>` module on demand, returning capture classes that register into
the active DAG (like a real operator) and record their **real dotted class path**
plus the **constructor kwargs**. This lets the parser record an arbitrary provider
operator/sensor structurally — without the provider installed in the parser — so
the runtime can later `import_string(class)(**kwargs).execute(context)`.

`.hooks` modules are deliberately NOT captured: a hook belongs inside a `@task`
body (it is not a task), and a top-level hook import keeps its actionable
"import inside the task" error.

The finder is appended to `sys.meta_path` so the real bundled shim operators
(standard bash/python, http) — found by the normal path finder — always win; this
only handles the long tail.
"""
from __future__ import annotations

import importlib.abc
import importlib.machinery
import sys
import types


def _is_capturable(fullname: str) -> bool:
    return fullname.startswith("airflow.providers.") and ".hooks" not in fullname


class _CaptureModule(types.ModuleType):
    """A synthesized provider module: any attribute access yields a capture class
    whose `__leoflow_operator_class__` is its real dotted path."""

    def __getattr__(self, name: str):
        if name.startswith("__"):
            raise AttributeError(name)
        if ".notifications" in self.__name__:
            # A Notifier (SlackNotifier, SmtpNotifier) is a callback, not a task:
            # built inside a `with DAG` block it must not register as one (#2449).
            bases: tuple = (CapturedNotifier,)
        else:
            from airflow._core import BaseOperator

            bases = (_GenericCaptured, BaseOperator)
        cls = type(
            name,
            bases,
            {
                "__leoflow_operator_class__": f"{self.__name__}.{name}",
                "__module__": self.__name__,
            },
        )
        setattr(self, name, cls)
        return cls


class CapturedNotifier:
    """Stand-in for an Airflow Notifier: callable like the real one, so a
    callback check sees it, and marked so the compiler can name it (#2449)."""

    __leoflow_notifier__ = True

    def __init__(self, *args, **kwargs):
        self.args = args
        self.kwargs = kwargs

    def __call__(self, *_args, **_kwargs):
        return None


class _GenericCaptured:
    """Mixin that records the constructor kwargs before the shim BaseOperator
    registers the task into the DAG."""

    def __init__(self, *args, **kwargs):
        task_id = kwargs.pop("task_id", None)
        if task_id is None and args:
            task_id, args = args[0], args[1:]
        # Record the operator's own kwargs (drop the DAG handle, never serialized).
        self.__leoflow_args__ = {k: v for k, v in kwargs.items() if k != "dag"}
        super().__init__(task_id, **kwargs)


class _Finder(importlib.abc.MetaPathFinder, importlib.abc.Loader):
    def find_spec(self, fullname, path=None, target=None):
        if _is_capturable(fullname):
            return importlib.machinery.ModuleSpec(fullname, self, is_package=True)
        return None

    def create_module(self, spec):
        module = _CaptureModule(spec.name)
        module.__path__ = []  # a package, so deeper submodule imports keep resolving
        return module

    def exec_module(self, module):
        pass


_FINDER = _Finder()


def install() -> None:
    """Register the catch-all finder (idempotent), after the real path finder."""
    if _FINDER not in sys.meta_path:
        sys.meta_path.append(_FINDER)
