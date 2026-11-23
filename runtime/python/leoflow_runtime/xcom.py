"""Access XCom inputs injected into the task container by the agent."""

from __future__ import annotations

import json
import os
from typing import Any

XCOM_ENV_PREFIX = "LEOFLOW_XCOM_"

# An input too large for one environment variable (Linux caps each at 128 KiB)
# arrives as a file instead: LEOFLOW_XCOMFILE_<NAME> holds the path of a file
# with the JSON (#2455). The prefix is not XCOM_ENV_PREFIX, so code that walks
# the LEOFLOW_XCOM_ variables never mistakes the path for a value.
XCOM_FILE_ENV_PREFIX = "LEOFLOW_XCOMFILE_"


def read_raw(name: str) -> str | None:
    """Return the JSON text of the XCom input ``name``, from its environment
    variable or its file, or None when the agent delivered neither."""
    raw = os.environ.get(XCOM_ENV_PREFIX + name.upper())
    if raw is not None:
        return raw
    path = os.environ.get(XCOM_FILE_ENV_PREFIX + name.upper())
    if path is None:
        return None
    with open(path, encoding="utf-8") as f:
        return f.read()


def input_names() -> list[str]:
    """Return the names (lower case) of every XCom input the agent delivered,
    in either form."""
    names = set()
    for key in os.environ:
        for prefix in (XCOM_ENV_PREFIX, XCOM_FILE_ENV_PREFIX):
            if key.startswith(prefix):
                names.add(key[len(prefix):].lower())
    return sorted(names)


def xcom_pull(name: str, default: Any = None) -> Any:
    """Return the upstream XCom mapped to ``name``, or ``default`` if absent.

    The agent injects each declared input as ``LEOFLOW_XCOM_<NAME>=<json>``, or
    as a file named by ``LEOFLOW_XCOMFILE_<NAME>`` when it is too large for the
    environment; the name is matched case-insensitively.
    """
    raw = read_raw(name)
    if raw is None:
        return default
    return json.loads(raw)
