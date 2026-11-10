"""Shim of ``airflow.exceptions``: the exception classes a DAG file imports at
module level (#2451). Task bodies never run while the file is parsed, so
nothing raises them here; the task pod imports the real ones."""
from __future__ import annotations


class AirflowException(Exception):
    """Base of Airflow's exceptions."""


class AirflowFailException(AirflowException):
    """Fail the task without retrying."""


class AirflowSkipException(AirflowException):
    """End the task as skipped."""


class AirflowSensorTimeout(AirflowException):
    """A sensor ran past its timeout."""


class AirflowNotFoundException(AirflowException):
    """A resource (a Connection, a Variable) was not found."""


class AirflowTaskTimeout(AirflowException):
    """A task ran past its execution_timeout."""


class AirflowRescheduleException(AirflowException):
    """A reschedule-mode sensor asks to be poked again later."""


class AirflowConfigException(AirflowException):
    """Invalid Airflow configuration."""


class AirflowTaskTerminated(AirflowException):
    """The task was terminated."""


class ParamValidationError(AirflowException):
    """A run's params failed their schema."""


class TaskDeferred(AirflowException):
    """An operator defers to a trigger."""


class DownstreamTasksSkipped(AirflowException):
    """A task skips some of its downstream tasks."""


__all__ = [
    "AirflowConfigException",
    "AirflowException",
    "AirflowFailException",
    "AirflowNotFoundException",
    "AirflowRescheduleException",
    "AirflowSensorTimeout",
    "AirflowSkipException",
    "AirflowTaskTerminated",
    "AirflowTaskTimeout",
    "DownstreamTasksSkipped",
    "ParamValidationError",
    "TaskDeferred",
]
