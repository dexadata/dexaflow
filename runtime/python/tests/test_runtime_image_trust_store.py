"""The certifi bundles in the task image drop emSign ECC Root CA - G3 (#2067).

Every HTTPS call a task makes through ``requests`` or ``httpx`` verifies the
server against certifi's bundle, not the system trust store. certifi reaches
the image twice: as a site-packages install (a transitive dependency of the
Task SDK, through ``requests`` and ``httpx``) and as the copy pip vendors in
``pip/_vendor/certifi``, which pip loads next to the system store. GHSA-6q48-6c7w-9gw2
drops emSign ECC Root CA - G3, whose intermediates were compromised, in certifi
2026.10.19; every older bundle still trusts it. The system trust store
(``/etc/ssl/certs``) drops it in Debian's ca-certificates 20250419+deb13u1,
which the image upgrades itself rather than waiting on the base image.

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
    """The numeric release of ``ARG name=X.Y.Z``, ignoring any PEP 440 suffix."""
    match = re.search(rf"^ARG {name}=(\d+(?:\.\d+)*)", text, re.MULTILINE)
    assert match, f"runtime/Dockerfile declares no numeric ARG {name}"
    return tuple(int(part) for part in match.group(1).split("."))


def _run_instructions(text: str) -> list[str]:
    """Every RUN instruction, with its backslash continuation lines joined."""
    joined = re.sub(r"\\\n", " ", text)
    return [line for line in joined.splitlines() if line.startswith("RUN ")]


def _first_run(runs: list[str], pattern: str) -> int:
    for index, run in enumerate(runs):
        if re.search(pattern, run):
            return index
    raise AssertionError(f"runtime/Dockerfile has no RUN instruction matching {pattern!r}")


def test_certifi_pin_drops_the_emsign_ecc_g3_root():
    """The site-packages certifi every task's HTTPS client reads."""
    assert _arg(_dockerfile(), "CERTIFI_VERSION") >= _CERTIFI_FLOOR


def test_certifi_is_installed_after_the_task_sdk_pulls_it_in():
    """Pinned after the Task SDK, so the pin is the version left in the image."""
    runs = _run_instructions(_dockerfile())
    sdk = _first_run(runs, r"apache-airflow-task-sdk==\$\{AIRFLOW_TASK_SDK_VERSION\}")
    certifi = _first_run(runs, r"certifi==\$\{CERTIFI_VERSION\}")
    # The same RUN is fine too: one resolver pass honors the pin.
    assert certifi >= sdk


def test_pip_pin_vendors_a_certifi_without_the_emsign_ecc_g3_root():
    """pip/_vendor/certifi, trusted (with the system store) for a DAG's requirements."""
    assert _arg(_dockerfile(), "PIP_VERSION") >= _PIP_FLOOR


def test_system_trust_store_is_upgraded_past_the_emsign_ecc_g3_root():
    """/etc/ssl/certs, read by ``ssl``, ``urllib`` and pip's truststore.

    Debian dropped the root in ca-certificates 20250419+deb13u1 (DSA-6572-1),
    but the slim base image of a Python line keeps the ca-certificates it was
    built with, and a line past its end of life (3.10 from October 2026) is not
    rebuilt. So the image upgrades the package itself, on every line.
    """
    runs = _run_instructions(_dockerfile())
    upgrade = _first_run(runs, r"apt-get install\b.*--only-upgrade\b.*\bca-certificates\b")
    assert re.search(r"apt-get update\b", runs[upgrade]), "no fresh package index"
    assert re.search(r"rm -rf /var/lib/apt/lists", runs[upgrade]), "the index stays in the layer"
