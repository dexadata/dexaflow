"""The ``macros`` template namespace for images without the Airflow Task SDK.

The runtime hands templates the Task SDK's own ``airflow.sdk.execution_time.macros``
module whenever it is importable, which it is in the Dexaflow base image. This
module is the fallback for an image that has the runtime but not the SDK: it keeps
the names templates use most (``macros.ds_add``, ``macros.ds_format`` and the
standard modules the SDK re-exports) so ``{{ macros.ds_add(ds, 1) }}`` renders the
same either way.
"""

from __future__ import annotations

import json
import random as _random
import time
import uuid
from datetime import datetime, timedelta

# The standard modules are re-exported, as in the Task SDK's macros, so templates
# can call macros.json, macros.time and macros.uuid.
__all__ = ["datetime", "ds_add", "ds_format", "json", "random", "time", "timedelta", "uuid"]

random = _random.random


def ds_add(ds: str, days: int) -> str:
    """Add ``days`` (negative to subtract) to a ``YYYY-MM-DD`` date string."""
    if not days:
        return str(ds)
    dt = datetime.strptime(str(ds), "%Y-%m-%d") + timedelta(days=days)
    return dt.strftime("%Y-%m-%d")


def ds_format(ds: str, input_format: str, output_format: str) -> str:
    """Reformat the date string ``ds`` from ``input_format`` to ``output_format``."""
    return datetime.strptime(str(ds), input_format).strftime(output_format)
