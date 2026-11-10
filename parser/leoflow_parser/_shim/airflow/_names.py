"""Names a DAG file commonly imports from ``airflow.sdk`` and uses at module level
only as types, constants or inert helpers (#2451). Each one is enough for the
file to compile; calling one that needs a running task or a metadata store
while the file is parsed raises a message saying where to call it instead."""
from __future__ import annotations

from enum import Enum

from airflow._core import BaseOperator
from airflow._generic import CapturedNotifier


class TriggerRule(str, Enum):
    """Airflow's trigger rules. The compiler reads ``.value`` and decides which
    ones Dexaflow honors."""

    ALL_SUCCESS = "all_success"
    ALL_FAILED = "all_failed"
    ALL_DONE = "all_done"
    ALL_DONE_MIN_ONE_SUCCESS = "all_done_min_one_success"
    ALL_DONE_SETUP_SUCCESS = "all_done_setup_success"
    ONE_SUCCESS = "one_success"
    ONE_FAILED = "one_failed"
    ONE_DONE = "one_done"
    NONE_FAILED = "none_failed"
    NONE_FAILED_MIN_ONE_SUCCESS = "none_failed_min_one_success"
    NONE_SKIPPED = "none_skipped"
    ALWAYS = "always"
    ALL_SKIPPED = "all_skipped"

    def __str__(self) -> str:
        return self.value


class WeightRule(str, Enum):
    """Airflow's priority weight rules."""

    DOWNSTREAM = "downstream"
    UPSTREAM = "upstream"
    ABSOLUTE = "absolute"

    def __str__(self) -> str:
        return self.value


def _at_parse(what: str, where: str):
    def refuse(*_args, **_kwargs):
        raise NotImplementedError(
            f"{what} called while the DAG file is parsed is not supported by Dexaflow: "
            f"{where}")

    return refuse


class BaseSensorOperator(BaseOperator):
    """Base of Airflow's sensors. A sensor class written in the project is a
    custom operator, which the compiler refuses with its own message."""


class PokeReturnValue:
    """What a sensor's poke returns: done, and an optional XCom value."""

    def __init__(self, is_done: bool, xcom_value=None):
        self.is_done = is_done
        self.xcom_value = xcom_value


class BaseHook:
    """Base of Airflow's hooks. A hook belongs inside a task body."""

    get_connection = staticmethod(_at_parse(
        "BaseHook.get_connection()", "call it inside the task body"))

    get_hook = staticmethod(_at_parse("BaseHook.get_hook()", "call it inside the task body"))


class Connection:
    """Airflow's Connection model, as a type."""

    get = staticmethod(_at_parse("Connection.get()", "call it inside the task body"))

    def __init__(self, *args, **kwargs):
        self.args = args
        self.kwargs = kwargs


class Context(dict):
    """The task context, as a type for annotations."""


class ParamsDict(dict):
    """A run's params, as a type."""


class Metadata:
    """Asset event metadata, as a type."""

    def __init__(self, *args, **kwargs):
        self.args = args
        self.kwargs = kwargs


class ObjectStoragePath:
    """Airflow's object storage path, kept as its string. Building one at module
    level and joining it with ``/`` works; reading or writing happens in the task."""

    def __init__(self, path, *parts, conn_id=None, **_kwargs):
        self._path = "/".join([str(path).rstrip("/"), *[str(p).strip("/") for p in parts]])
        self.conn_id = conn_id

    def __truediv__(self, other):
        return ObjectStoragePath(self._path, other, conn_id=self.conn_id)

    def __str__(self) -> str:
        return self._path

    def __repr__(self) -> str:
        return f"ObjectStoragePath({self._path!r})"

    def __fspath__(self) -> str:
        return self._path


class BaseNotifier(CapturedNotifier):
    """Base of Airflow's notifiers; the compiler names it when one is used as a
    callback (#2449)."""


get_current_context = _at_parse(
    "get_current_context()", "call it inside the task body, where the run context exists")


class _ParsingContext:
    dag_id = None
    task_id = None


def get_parsing_context():
    """Airflow's parsing context. While Dexaflow compiles there is no run, so
    both ids are None, as when Airflow parses a whole file."""
    return _ParsingContext()


def literal(value):
    """Airflow's ``literal``: a value that is not rendered as a template."""
    return value
