#!/usr/bin/env bash
# Shared helpers for the k3d end-to-end scripts.

# DBT_CONSTRAINTS is the pip constraints file that pins dbt-core for every dbt
# install of the tests (#957).
DBT_CONSTRAINTS="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/constraints-dbt.txt"

# dbt_core_pin prints the dbt-core requirement of DBT_CONSTRAINTS, for an
# install that cannot take a constraints file, such as a DAG's dependencies.
dbt_core_pin() {
  grep -E '^dbt-core[=<>!~]' "$DBT_CONSTRAINTS"
}

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
# Even a clean run has let a job ErrImagePull (#2105), so the import also
# counts only once every image is listed in containerd on every node of the
# cluster. A node whose image list cannot be read is reported and skipped, so
# the check never fails an import it cannot see.
#
# Usage: k3d_import <cluster> <image> [image...]
k3d_import() {
  local cluster="$1"; shift
  local attempt out rc
  for attempt in 1 2 3; do
    out="$(k3d image import "$@" --cluster "$cluster" 2>&1)" && rc=0 || rc=$?
    if [ "$rc" -eq 0 ] && ! printf '%s\n' "$out" | grep -qiE 'failed to import|no such file or directory|ERRO\['; then
      k3d_images_landed "$cluster" "$@" && return 0
      out="k3d reported success, but an image is missing from a node"
    fi
    printf '%s\n' "$out" >&2
    printf 'k3d_import: attempt %d hit the import flake (k3d misreported success, rc=%s); retrying...\n' "$attempt" "$rc" >&2
    sleep 3
  done
  printf 'FATAL: k3d image import failed to land images in cluster %q after 3 attempts (the tarball-not-found flake)\n' "$cluster" >&2
  return 1
}

# k3d_image_ref prints the reference containerd stores for an image name:
# an image without a registry lives under docker.io, and one without a
# namespace under docker.io/library, and one without a tag is :latest, as
# `docker pull` resolves them.
k3d_image_ref() {
  local img="$1" first="${1%%/*}"
  case "$img" in
    */*) case "$first" in *.*|*:*|localhost) ;; *) img="docker.io/$img" ;; esac ;;
    *) img="docker.io/library/$img" ;;
  esac
  case "${img##*/}" in *:*|*@*) ;; *) img="$img:latest" ;; esac
  printf '%s\n' "$img"
}

# k3d_images_landed <cluster> <image> [image...]: every image is listed in the
# k8s.io containerd namespace of every server and agent node of the cluster.
k3d_images_landed() {
  local cluster="$1"; shift
  local nodes node listed img ref missing=0
  nodes="$( { docker ps --filter "label=k3d.cluster=$cluster" --filter label=k3d.role=server --format '{{.Names}}'
    docker ps --filter "label=k3d.cluster=$cluster" --filter label=k3d.role=agent --format '{{.Names}}'; } 2>/dev/null)"
  if [ -z "$nodes" ]; then
    printf 'k3d_import: could not list the nodes of cluster %q; not verifying the import\n' "$cluster" >&2
    return 0
  fi
  for node in $nodes; do
    if ! listed="$(docker exec "$node" ctr -n k8s.io images ls -q 2>/dev/null)"; then
      printf 'k3d_import: could not read the images on node %s; not verifying it\n' "$node" >&2
      continue
    fi
    for img in "$@"; do
      ref="$(k3d_image_ref "$img")"
      if ! printf '%s\n' "$listed" | grep -qxF "$ref"; then
        printf 'k3d_import: %s is missing from node %s\n' "$ref" "$node" >&2
        missing=1
      fi
    done
  done
  [ "$missing" -eq 0 ]
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

# admin_user prints the bootstrap admin address that logs in on the server. A
# database created by this release bootstraps admin@dexaflow.local; one created
# before the rename keeps admin@leoflow.local (the server never adds a second
# admin, see auth.ResolveAdminEmail), and these scripts run against both, so the
# new address is tried first and the old one second. DEXAFLOW_E2E_ADMIN_USER
# pins the address instead.
#
# Usage: admin_user <dexaflow-binary> <api-base-url> <password>
admin_user() {
  local bin="$1" api="$2" pw="$3" user
  local users="${DEXAFLOW_E2E_ADMIN_USER:-admin@dexaflow.local admin@leoflow.local}"
  for user in $users; do
    if "$bin" auth create-token --server "$api" --username "$user" --password "$pw" >/dev/null 2>&1; then
      printf '%s\n' "$user"
      return 0
    fi
  done
  printf 'admin_user: no bootstrap admin (%s) logs in on %s\n' "$users" "$api" >&2
  return 1
}

# admin_token mints an admin token for the bootstrap admin (see admin_user)
# with `dexaflow auth create-token` and prints it.
#
# Usage: admin_token <dexaflow-binary> <api-base-url> <password>
admin_token() {
  local user
  user="$(admin_user "$@")" || return 1
  "$1" auth create-token --server "$2" --username "$user" --password "$3"
}

# k3d_wait_host_entry waits until the cluster's CoreDNS answers for
# host.k3d.internal, the name task pods use to reach services on the runner.
# k3d adds that entry to the coredns ConfigMap after the cluster reports ready,
# so a run triggered right after cluster creation can start a pod that cannot
# resolve it and fails within seconds (#919). It returns 1 after timeout
# seconds (default 60) without the entry.
#
# Usage: k3d_wait_host_entry [timeout]
k3d_wait_host_entry() {
  local timeout="${1:-60}" deadline
  deadline=$(( $(date +%s) + timeout ))
  until kubectl -n kube-system get configmap coredns -o jsonpath='{.data.NodeHosts}' 2>/dev/null |
    grep -qw 'host.k3d.internal'; do
    if [ "$(date +%s)" -ge "$deadline" ]; then
      printf 'k3d_wait_host_entry: CoreDNS has no host.k3d.internal entry after %ss\n' "$timeout" >&2
      return 1
    fi
    sleep 2
  done
}

# dump_task_instances prints every task instance of a run with its state, try
# and failure reason, so a failed e2e says which task failed and why before
# the cluster is deleted (#919). It never fails the caller.
#
# Usage: dump_task_instances <api> <dag_id> <run_id> <token>
dump_task_instances() {
  local api="$1" dag="$2" run="$3" token="$4"
  [ -n "$run" ] || return 0
  printf '\033[1;33m--- task instances of %s/%s ---\033[0m\n' "$dag" "$run" >&2
  curl -fsS -H "Authorization: Bearer $token" "$api/api/v2/dags/$dag/dagRuns/$run/taskInstances" 2>/dev/null |
    jq -r '.task_instances[] | "\(.task_id)\t\(.state)\ttry \(.try_number)\t\(.failure_reason // "")"' >&2 || true
}
