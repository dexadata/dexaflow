"""A small stand-in for ``pendulum`` while a DAG file is compiled (#2451).

Airflow 3's docs write ``start_date=pendulum.datetime(2026, 1, 1, tz="UTC")``,
and the compile runs dag.py without the DAG's dependencies, so ``import
pendulum`` failed on the first line. This covers what a DAG file uses at module
level, built on ``datetime`` and ``zoneinfo``. Task bodies never run here; the
task image has the real package."""
from __future__ import annotations

import datetime as _dt
from zoneinfo import ZoneInfo

__all__ = [
    "UTC", "DateTime", "Duration", "Timezone", "datetime", "duration", "now",
    "parse", "timezone", "today", "yesterday", "tomorrow",
]

DateTime = _dt.datetime
Duration = _dt.timedelta
Timezone = ZoneInfo
UTC = ZoneInfo("UTC")


def timezone(name) -> ZoneInfo:
    """The zone ``name`` (an IANA name such as ``America/Sao_Paulo``)."""
    return name if isinstance(name, _dt.tzinfo) else ZoneInfo(str(name))


def _tz(tz):
    return UTC if tz is None else timezone(tz)


def datetime(year, month, day, hour=0, minute=0, second=0, microsecond=0, tz="UTC",
             **_kwargs) -> _dt.datetime:
    """A timezone-aware datetime, in UTC unless ``tz`` names another zone."""
    return _dt.datetime(year, month, day, hour, minute, second, microsecond,
                        tzinfo=_tz(tz))


def duration(days=0, seconds=0, microseconds=0, milliseconds=0, minutes=0, hours=0,
             weeks=0, **_kwargs) -> _dt.timedelta:
    """A timedelta. ``years`` and ``months`` have no fixed length and are not
    supported here."""
    if _kwargs.get("years") or _kwargs.get("months"):
        raise NotImplementedError(
            "pendulum.duration(years=..., months=...) while the DAG file is compiled is "
            "not supported by Dexaflow: use days or weeks")
    return _dt.timedelta(days=days, seconds=seconds, microseconds=microseconds,
                         milliseconds=milliseconds, minutes=minutes, hours=hours,
                         weeks=weeks)


def now(tz=None) -> _dt.datetime:
    return _dt.datetime.now(_tz(tz))


def today(tz=None) -> _dt.datetime:
    return now(tz).replace(hour=0, minute=0, second=0, microsecond=0)


def yesterday(tz=None) -> _dt.datetime:
    return today(tz) - _dt.timedelta(days=1)


def tomorrow(tz=None) -> _dt.datetime:
    return today(tz) + _dt.timedelta(days=1)


def parse(text, tz="UTC", **_kwargs) -> _dt.datetime:
    """An ISO 8601 string; a value with no offset is read in ``tz``."""
    value = _dt.datetime.fromisoformat(str(text).replace("Z", "+00:00"))
    return value if value.tzinfo else value.replace(tzinfo=_tz(tz))
