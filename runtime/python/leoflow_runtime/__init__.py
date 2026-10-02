"""Dexaflow task runtime: the helper that runs inside every task container.

It runs the user's Python callable, captures its return value as an XCom, and
exposes upstream XCom inputs. The leoflow-agent invokes ``python -m
leoflow_runtime <module:callable>`` and ships the captured return value back to
the control plane.
"""

from leoflow_runtime.runner import run
from leoflow_runtime.xcom import xcom_pull

__all__ = ["run", "xcom_pull"]
