#!/usr/bin/env bash
# Shared helpers for the k3d end-to-end scripts.

# k3d_import imports images into a k3d cluster, working around a k3d bug where
# `k3d image import` prints "Successfully imported" even when the in-node
# containerd import actually failed ("ctr: open /k3d/images/...tar: no such file
# or directory") and exits 0. A bare import then silently leaves the cluster
# WITHOUT the image, so every task pod ErrImagePulls and the e2e fails far
# downstream with a confusing symptom. This retries and treats the import as
# successful only when k3d BOTH exits 0 AND wrote no error line (the ERRO /
# "failed to import" it emits to stderr despite the misleading success line),
# failing loud instead of proceeding into ErrImagePull.
#
# Usage: k3d_import <cluster> <image> [image...]
k3d_import() {
  local cluster="$1"; shift
  local attempt out rc
  for attempt in 1 2 3; do
    out="$(k3d image import "$@" --cluster "$cluster" 2>&1)" && rc=0 || rc=$?
    if [ "$rc" -eq 0 ] && ! printf '%s\n' "$out" | grep -qiE 'failed to import|no such file or directory|ERRO\['; then
      return 0
    fi
    printf '%s\n' "$out" >&2
    printf 'k3d_import: attempt %d hit the import flake (k3d misreported success, rc=%s); retrying...\n' "$attempt" "$rc" >&2
    sleep 3
  done
  printf 'FATAL: k3d image import failed to land images in cluster %q after 3 attempts (the tarball-not-found flake)\n' "$cluster" >&2
  return 1
}

# unpause_dag unpauses a registered DAG the way an Airflow user does before a
# manual run: PATCH /api/v2/dags/{dag_id} with {"is_paused": false}. A DAG
# registered for the first time starts paused (dags_are_paused_at_creation,
# default true), and a run triggered while its DAG is paused stays queued until
# the DAG is unpaused, so a script that triggers a run and waits on it calls
# this first. It prints nothing on stdout (callers run it inside $(...)) and
# fails unless the server answers with is_paused false.
#
# Usage: unpause_dag <api-base-url> <dag_id> [bearer-token]
unpause_dag() {
  local api="$1" dag="$2" token="${3:-}" body paused
  local auth=()
  [ -n "$token" ] && auth=(-H "Authorization: Bearer $token")
  body="$(curl -fsS --max-time 30 -X PATCH ${auth[@]+"${auth[@]}"} \
    -H 'Content-Type: application/json' -d '{"is_paused": false}' \
    "$api/api/v2/dags/$dag")" || {
    printf 'unpause_dag: PATCH %s/api/v2/dags/%s failed\n' "$api" "$dag" >&2
    return 1
  }
  paused="$(printf '%s' "$body" | jq -r '.is_paused')"
  [ "$paused" = "false" ] || {
    printf 'unpause_dag: %s still reports is_paused=%s after the PATCH\n' "$dag" "$paused" >&2
    return 1
  }
}
