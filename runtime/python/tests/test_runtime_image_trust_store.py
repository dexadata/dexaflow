"""The task image trusts no CA that certifi has since removed (#2067).

Every HTTPS call a task makes through ``requests`` or ``httpx`` verifies the
server against certifi's bundle, not the system trust store. certifi reaches
the image twice: as a site-packages install (a transitive dependency of the
Task SDK, through ``requests`` and ``httpx``) and as the copy pip vendors in
``pip/_vendor/certifi``. GHSA-6q48-6c7w-9gw2 drops emSign ECC Root CA - G3,
whose intermediates were compromised, in certifi 2026.10.19; every older
bundle still trusts it.

Left to the resolver, the site-packages copy is whatever certifi was newest
when the layer was built, and the vendored copy moves only with pip. So both
are pinned in ``runtime/Dockerfile``, and these tests read that file: they fail
when a pin falls below the release that removes the root, or when the certifi
install runs before the Task SDK install that pulls certifi in.
"""
from __future__ import annotations

import pathlib
import re

_DOCKERFILE = pathlib.Path(__file__).resolve().parents[3] / "runtime" / "Dockerfile"

# The first certifi without emSign ECC Root CA - G3 (GHSA-6q48-6c7w-9gw2).
_CERTIFI_FLOOR = (2026, 10, 19)
# The first pip that vendors that certifi (pip/_vendor/vendor.txt).
_PIP_FLOOR = (26, 2, 2)


def _dockerfile() -> str:
    return _DOCKERFILE.read_text(encoding="utf-8")


def _arg(text: str, name: str) -> tuple[int, ...]:
    match = re.search(rf"^ARG {name}=(\S+)$", text, re.MULTILINE)
    assert match, f"runtime/Dockerfile declares no ARG {name}"
    return tuple(int(part) for part in match.group(1).split("."))


def _first_line(text: str, needle: str) -> int:
    for number, line in enumerate(text.splitlines(), start=1):
        if line.startswith("RUN ") and needle in line:
            return number
    raise AssertionError(f"runtime/Dockerfile has no RUN line installing {needle!r}")


def test_certifi_pin_drops_the_emsign_ecc_g3_root():
    """The site-packages certifi every task's HTTPS client reads."""
    assert _arg(_dockerfile(), "CERTIFI_VERSION") >= _CERTIFI_FLOOR


def test_certifi_is_installed_after_the_task_sdk_pulls_it_in():
    """Pinned after the Task SDK, so the pin is the version left in the image."""
    text = _dockerfile()
    sdk = _first_line(text, '"apache-airflow-task-sdk==${AIRFLOW_TASK_SDK_VERSION}"')
    certifi = _first_line(text, '"certifi==${CERTIFI_VERSION}"')
    assert certifi > sdk


def test_pip_pin_vendors_a_certifi_without_the_emsign_ecc_g3_root():
    """pip/_vendor/certifi, which pip uses when a DAG image installs its requirements."""
    assert _arg(_dockerfile(), "PIP_VERSION") >= _PIP_FLOOR
