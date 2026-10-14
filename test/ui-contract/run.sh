#!/usr/bin/env bash
#
# Run the UI contract sweep against a running control plane, using a Dockerized
# Playwright so no local browser/Python setup is needed.
#
# It drives every major Airflow-SPA view in a headless browser and fails if any
# view makes a non-2xx /api or /ui call or logs a console error — the contract
# guard for when the embedded Airflow SPA is upgraded (see sweep.py).
#
# Requirements: docker, and a control plane reachable from the container.
# Usage:
#   test/ui-contract/run.sh                 # against http://host.docker.internal:8080
#   LEOFLOW_BASE_URL=http://host.docker.internal:8080 test/ui-contract/run.sh
#   UICONTRACT_OUT=/tmp/uic test/ui-contract/run.sh   # also save screenshots
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
IMAGE="mcr.microsoft.com/playwright/python:v1.49.0-jammy"
BASE_URL="${LEOFLOW_BASE_URL:-http://host.docker.internal:8080}"
OUT="${UICONTRACT_OUT:-}"

command -v docker >/dev/null || { echo "docker is required" >&2; exit 2; }

mounts=(-v "$HERE:/work:ro")
out_env=()
if [ -n "$OUT" ]; then
  mkdir -p "$OUT"
  mounts+=(-v "$OUT:/out")
  out_env=(-e "UICONTRACT_OUT=/out")
fi

exec docker run --rm \
  --add-host=host.docker.internal:host-gateway \
  "${mounts[@]}" \
  -e "LEOFLOW_BASE_URL=$BASE_URL" \
  -e "LEOFLOW_USER=${LEOFLOW_USER:-}" \
  -e "LEOFLOW_PASSWORD=${LEOFLOW_PASSWORD:-admin}" \
  -e "LEOFLOW_DAG_ID=${LEOFLOW_DAG_ID:-}" \
  -e "LEOFLOW_RUN_ID=${LEOFLOW_RUN_ID:-}" \
  -e "LEOFLOW_TASK_ID=${LEOFLOW_TASK_ID:-}" \
  ${out_env[@]+"${out_env[@]}"} \
  -w /work \
  "$IMAGE" \
  bash -c "pip install -q playwright==1.49.0 >/dev/null 2>&1 && python /work/sweep.py"
