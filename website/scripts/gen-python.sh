#!/usr/bin/env bash
# Generate the Python runtime API reference for the Hugo site with pdoc.
#
# The leoflow_runtime docstrings (the module keeps its pre-rename name) are
# rendered with pdoc into a SELF-CONTAINED static subsite. pdoc's default HTML does not
# share Docsy's Bootstrap theming, so rather than fight it we publish the pdoc
# output verbatim under website/static/python-api/ (served at /python-api/) and
# LINK to it from the content page reference/python-api.md. pdoc's internal
# links are relative, so the subsite works under any baseURL path.
#
# A dedicated, git-ignored venv (website/.pdoc-venv) keeps pdoc + the runtime
# package off the host Python. Override the interpreter with PYTHON=... .
#
# Idempotent: reruns rebuild the venv-installed package and overwrite the
# static/python-api/ output. The generated tree IS committed (parallel-track
# preview convenience); CI reruns this before `hugo`.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
VENV="${REPO_ROOT}/website/.pdoc-venv"
OUT_DIR="${REPO_ROOT}/website/static/python-api"
PYTHON="${PYTHON:-python3}"

# Modules to document: the package plus its public submodules. __main__ (the
# CLI entry point) is intentionally omitted — it is not part of the API.
MODULES=(
  leoflow_runtime
  leoflow_runtime.runner
  leoflow_runtime.xcom
  leoflow_runtime.dbt
)

echo "gen-python: preparing venv at ${VENV}"
if [ ! -x "${VENV}/bin/pdoc" ]; then
  "${PYTHON}" -m venv "${VENV}"
fi
"${VENV}/bin/pip" install -q --upgrade pip
"${VENV}/bin/pip" install -q pdoc "${REPO_ROOT}/runtime/python"

echo "gen-python: rendering ${#MODULES[@]} modules into ${OUT_DIR}"
rm -rf "${OUT_DIR}"
"${VENV}/bin/pdoc" "${MODULES[@]}" \
  --docformat google \
  --no-show-source \
  -o "${OUT_DIR}"

echo "gen-python: done ($(find "${OUT_DIR}" -type f | wc -l | tr -d ' ') files)"
