#!/usr/bin/env bash
#
# The daemonless deploy journey on a real cluster (#1888): `dexaflow deploy
# examples/taskflow_sales` from a machine with no container daemon, on the
# dependency layer built from wheels with uv (ADR 0073, ADR 0078), against a
# k3d cluster that then runs the DAG.
#
# Every other deploy e2e (deploy-e2e.sh, source-mode-e2e.sh) deploys from the
# runner itself, where Docker is installed and running, and deploy-e2e.sh
# ships a hand-written Dockerfile, so the builder it takes is the local daemon.
# The path a CI job with no daemon takes (the dexaflow-cli image of #1708,
# GitLab CI, Cloud Build, a Kubernetes Job) was covered by unit tests of each
# builder and by no run of the whole journey. This script runs it:
#
#   1. Setup, on the host, where Docker is allowed: a k3d registry and cluster
#      (deploy-e2e.sh's layout), the task runtime base built from this commit
#      (runtime/Dockerfile) and pushed to that registry, the control plane with
#      cluster deploy defaults naming the registry, the platform and that base
#      image, so the example deploys unmodified, as the docs describe.
#   2. The dexaflow-cli image built from deploy/Dockerfile.cli.release with the
#      CLI of this commit: python3, git, uv and the CLI, and no Docker CLI.
#   3. The deploy, inside that image, with no Docker socket mounted. Before it
#      deploys, the container proves no daemon is reachable (no container tool
#      on PATH, no daemon socket, no DOCKER_HOST); the script then asserts the
#      builder line names the dependency layer from wheels with uv, followed by
#      the code layer, and that the registry holds the dependency image.
#   4. A run of the deployed version on the cluster: every task succeeds and
#      the task logs carry the example's own output.
#
# Requirements: Linux (the container shares the host network, so the
# control plane and the k3d registry are on its loopback), k3d, kubectl,
# docker, go, jq, curl, the binaries (`make build`) and a migrated dev
# database (`make dev-up`). Developer and CI tool, run on release candidates by
# .github/workflows/e2e-daemonless-deploy.yaml and by `make rc-smoke`.
#
# Usage:
#   test/e2e/daemonless-deploy-e2e.sh [cluster-name]
#   test/e2e/daemonless-deploy-e2e.sh --assert-no-daemon   (run in the container)
#   test/e2e/daemonless-deploy-e2e.sh --self-test
set -euo pipefail

# assert_no_daemon fails unless no container daemon is reachable from here: no
# container tool on PATH, no daemon socket under root (empty for the real
# filesystem), and no DOCKER_HOST or CONTAINER_HOST pointing elsewhere. It is
# what the container runs before `dexaflow deploy`, so a green run cannot come
# from a daemon that happened to be there.
assert_no_daemon() { # <root>
	local root="$1" tool sock found=0
	for tool in docker podman nerdctl buildah buildctl; do
		if command -v "$tool" >/dev/null 2>&1; then
			printf 'a container tool is on PATH: %s\n' "$(command -v "$tool")" >&2
			found=1
		fi
	done
	for sock in /var/run/docker.sock /run/docker.sock /run/containerd/containerd.sock \
		/run/podman/podman.sock /run/buildkit/buildkitd.sock; do
		if [ -e "${root}${sock}" ]; then
			printf 'a daemon socket is present: %s\n' "${root}${sock}" >&2
			found=1
		fi
	done
	if [ -n "${DOCKER_HOST:-}" ] || [ -n "${CONTAINER_HOST:-}" ]; then
		printf 'a daemon address is set: DOCKER_HOST=%s CONTAINER_HOST=%s\n' "${DOCKER_HOST:-}" "${CONTAINER_HOST:-}" >&2
		found=1
	fi
	[ "$found" -eq 0 ]
}

# check_builder_line fails unless the deploy output in <log> has exactly one
# builder line and it names the daemonless dependency layer built from wheels
# with uv, then the code layer. A dependency image reused from the registry
# (plain code-layer), one built by the daemon or by the remote builder, or a
# whole build, all fail: they are not the journey this script exists for.
check_builder_line() { # <log>
	local lines line
	lines="$(grep -E '^builder: ' "$1" || true)"
	if [ -z "$lines" ]; then
		echo "the deploy printed no builder line" >&2
		return 1
	fi
	if [ "$(printf '%s\n' "$lines" | wc -l)" -ne 1 ]; then
		printf 'the deploy printed more than one builder line:\n%s\n' "$lines" >&2
		return 1
	fi
	line="$lines"
	if ! printf '%s\n' "$line" | grep -qE '^builder: +dependency-layer \(uv [0-9][^)]*\), then code-layer, '; then
		printf 'the builder is not the dependency layer from wheels with uv:\n%s\n' "$line" >&2
		return 1
	fi
	if ! printf '%s\n' "$line" | grep -qF 'it is built from wheels without a daemon and pushed'; then
		printf 'the builder line does not say the dependency image is built from wheels without a daemon:\n%s\n' "$line" >&2
		return 1
	fi
}

self_test() {
	local tmp rc=0
	tmp="$(mktemp -d)"
	# shellcheck disable=SC2064 # expand now: tmp is local
	trap "rm -rf '$tmp'" RETURN
	expect() { # <want 0|1> <name> <cmd...>
		local want="$1" name="$2" got=0
		shift 2
		"$@" >/dev/null 2>&1 || got=1
		if [ "$got" -ne "$want" ]; then
			printf 'self-test FAIL: %s (want %s, got %s)\n' "$name" "$want" "$got" >&2
			rc=1
		fi
	}

	local wheels='builder:    dependency-layer (uv 0.12.24), then code-layer, no dependency image k3d-registry.localhost:5111/taskflow_sales:deps-9b07 in the registry (MANIFEST_UNKNOWN): it is built from wheels without a daemon and pushed, then the DAG code is appended to it'
	printf 'server:     http://127.0.0.1:18080 (--server)\n%s\nDeployed taskflow_sales\n' "$wheels" >"$tmp/ok.log"
	expect 0 "dependency layer from wheels passes" check_builder_line "$tmp/ok.log"

	printf 'builder:    code-layer, the registry holds the dependency image x:deps-1 for this build, so only the DAG code is pushed\n' >"$tmp/reused.log"
	expect 1 "a reused dependency image fails" check_builder_line "$tmp/reused.log"
	printf 'builder:    local-daemon (docker) for the dependency image, then code-layer, no dependency image x:deps-1 in the registry (...): the daemon builds and pushes it (no daemonless dependency layer: uv is not on PATH), then the DAG code is appended to it\n' >"$tmp/daemon-deps.log"
	expect 1 "a daemon-built dependency image fails" check_builder_line "$tmp/daemon-deps.log"
	printf 'builder:    local-daemon (docker), fallback: the project ships its own Dockerfile, which needs a real build\n' >"$tmp/whole.log"
	expect 1 "a whole daemon build fails" check_builder_line "$tmp/whole.log"
	printf 'builder:    remote (cloud-build, project acme-ci) for the dependency image, then code-layer, ...\n' >"$tmp/remote.log"
	expect 1 "a remote-built dependency image fails" check_builder_line "$tmp/remote.log"
	printf 'Deployed taskflow_sales\n' >"$tmp/none.log"
	expect 1 "no builder line fails" check_builder_line "$tmp/none.log"
	printf '%s\n%s\n' "$wheels" "$wheels" >"$tmp/two.log"
	expect 1 "two builder lines fail" check_builder_line "$tmp/two.log"
	printf 'builder:    dependency-layer (uv 0.12.24), then code-layer, --rebuild builds the dependency image again\n' >"$tmp/nowheels.log"
	expect 1 "a dependency layer line without the wheels reason fails" check_builder_line "$tmp/nowheels.log"
	printf 'builder:    local-daemon (docker), it is built from wheels without a daemon and pushed\n' >"$tmp/otherpath.log"
	expect 1 "another builder carrying the wheels reason fails" check_builder_line "$tmp/otherpath.log"
	printf '  %s\n' "$wheels" >"$tmp/indented.log"
	expect 1 "a builder line that is not at the start of a line fails" check_builder_line "$tmp/indented.log"

	# assert_no_daemon: a PATH with only coreutils-like tools and an empty root
	# passes; a fake docker on PATH, a socket under the root, or DOCKER_HOST
	# each fail on its own.
	mkdir -p "$tmp/bin" "$tmp/fakebin" "$tmp/root/var/run"
	ln -s "$(command -v true)" "$tmp/bin/true"
	printf '#!/bin/sh\nexit 0\n' >"$tmp/fakebin/docker"
	chmod +x "$tmp/fakebin/docker"
	expect 0 "no tool, no socket, no address passes" \
		env -u DOCKER_HOST -u CONTAINER_HOST PATH="$tmp/bin" "$BASH" -c "$(declare -f assert_no_daemon); assert_no_daemon '$tmp/root'"
	expect 1 "a docker on PATH fails" \
		env -u DOCKER_HOST -u CONTAINER_HOST PATH="$tmp/fakebin:$tmp/bin" "$BASH" -c "$(declare -f assert_no_daemon); assert_no_daemon '$tmp/root'"
	expect 1 "DOCKER_HOST fails" \
		env -u CONTAINER_HOST DOCKER_HOST=tcp://127.0.0.1:2375 PATH="$tmp/bin" "$BASH" -c "$(declare -f assert_no_daemon); assert_no_daemon '$tmp/root'"
	expect 1 "CONTAINER_HOST fails" \
		env -u DOCKER_HOST CONTAINER_HOST=unix:///run/podman/podman.sock PATH="$tmp/bin" "$BASH" -c "$(declare -f assert_no_daemon); assert_no_daemon '$tmp/root'"
	: >"$tmp/root/var/run/docker.sock"
	expect 1 "a docker socket fails" \
		env -u DOCKER_HOST -u CONTAINER_HOST PATH="$tmp/bin" "$BASH" -c "$(declare -f assert_no_daemon); assert_no_daemon '$tmp/root'"

	[ "$rc" -eq 0 ] && echo "self-test OK"
	return "$rc"
}

case "${1:-}" in
--self-test)
	self_test
	exit $?
	;;
--assert-no-daemon)
	assert_no_daemon ""
	exit $?
	;;
esac

# shellcheck source=test/e2e/lib.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

CLUSTER="${1:-dexaflow-daemonless-e2e}"
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
WORKDIR="$(mktemp -d)"
EXAMPLE="taskflow_sales"
DAG_ID="taskflow_sales"
PY_VERSION="3.11" # python_version of examples/taskflow_sales/dexaflow.yaml
REG_NAME="daemonless-registry.localhost" # k3d prefixes 'k3d-'; its own name, so it never meets deploy-e2e.sh's
REG_PORT="${DEXAFLOW_E2E_REGISTRY_PORT:-5112}"
REG_FULLNAME="k3d-${REG_NAME}"
REG_HOST="${REG_FULLNAME}:${REG_PORT}" # the same ref on the host (loopback) and in the cluster
BASE_REF="${REG_HOST}/dexaflow-runtime:py${PY_VERSION}-e2e"
CLI_IMAGE="dexaflow-cli:daemonless-e2e"
HTTP_PORT="${LEOFLOW_E2E_HTTP_PORT:-18080}"
API="http://127.0.0.1:${HTTP_PORT}"
GRPC_PORT="9091"
HOST_ADDR="${LEOFLOW_E2E_HOST_ADDR:-host.k3d.internal}"
SERVER_PID=""
SERVER_LOG="${WORKDIR}/server.log"
DEPLOY_LOG="${WORKDIR}/deploy.log"
case "$(uname -m)" in
arm64 | aarch64) GOARCH_HOST="arm64" ;;
*) GOARCH_HOST="amd64" ;;
esac
PLATFORM="linux/${GOARCH_HOST}"

log() { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
fail() {
	printf '\033[1;31mFAIL:\033[0m %s\n' "$*" >&2
	if [ -s "$SERVER_LOG" ]; then
		printf '\033[1;33m--- control plane log (last 60 lines) ---\033[0m\n' >&2
		tail -60 "$SERVER_LOG" >&2 || true
	fi
	printf '\033[1;33m--- task pods (namespace leoflow) ---\033[0m\n' >&2
	kubectl get pods -n leoflow -o wide >&2 2>&1 || true
	for p in $(kubectl get pods -n leoflow -o name 2>/dev/null); do
		kubectl describe -n leoflow "$p" 2>&1 | tail -20 >&2 || true
		kubectl logs -n leoflow "$p" --all-containers --tail=60 >&2 2>&1 || true
	done
	exit 1
}
cleanup() {
	[ -n "$SERVER_PID" ] && kill "$SERVER_PID" 2>/dev/null || true
	k3d cluster delete "$CLUSTER" >/dev/null 2>&1 || true
	k3d registry delete "$REG_FULLNAME" >/dev/null 2>&1 || true
	rm -rf "$WORKDIR"
}
trap cleanup EXIT

[ "$(uname -s)" = Linux ] || fail "Linux only: the CLI container shares the host network to reach the control plane and the registry"
for tool in k3d kubectl docker go jq curl; do
	command -v "$tool" >/dev/null || fail "missing required tool: $tool"
done
if [ ! -x "$ROOT/bin/dexaflow-server" ] || [ ! -x "$ROOT/bin/dexaflow" ]; then fail "run make build first"; fi
[ -f "$ROOT/examples/$EXAMPLE/dexaflow.yaml" ] || fail "examples/$EXAMPLE has no dexaflow.yaml"
# The documented path: the compiler generates the Dockerfile (AGENTS.md,
# principle 12). A Dockerfile in the example would send the build to a daemon.
[ ! -e "$ROOT/examples/$EXAMPLE/Dockerfile" ] || fail "examples/$EXAMPLE ships a Dockerfile; the daemonless journey deploys the generated one"

log "Building the task runtime base of this commit and the dexaflow-cli image (host Docker, setup only)"
docker build --provenance=false -f "$ROOT/runtime/Dockerfile" --build-arg "PYTHON_VERSION=${PY_VERSION}" -t "$BASE_REF" "$ROOT"
mkdir -p "$WORKDIR/cli-image-context"
VERSION="$(git -C "$ROOT" describe --tags --always --dirty 2>/dev/null || echo dev)"
(cd "$ROOT" && CGO_ENABLED=0 GOOS=linux GOARCH="$GOARCH_HOST" go build -trimpath \
	-ldflags "-s -w -X github.com/dexadata/dexaflow/internal/version.version=${VERSION}" \
	-o "$WORKDIR/cli-image-context/dexaflow" ./cmd/dexaflow)
docker build -f "$ROOT/deploy/Dockerfile.cli.release" -t "$CLI_IMAGE" "$WORKDIR/cli-image-context"

log "Creating the k3d registry ($REG_HOST) and cluster '$CLUSTER'"
k3d registry create "$REG_NAME" --port "$REG_PORT" >/dev/null
k3d cluster create "$CLUSTER" --registry-use "$REG_HOST" --wait
kubectl create namespace leoflow
for _ in $(seq 1 60); do
	kubectl get serviceaccount default -n leoflow >/dev/null 2>&1 && break
	sleep 1
done
kubectl get serviceaccount default -n leoflow >/dev/null || fail "the default ServiceAccount of namespace leoflow did not appear within 60s"
docker push "$BASE_REF" >/dev/null || fail "could not push the runtime base $BASE_REF"

log "Starting the control plane with cluster deploy defaults (registry, platform, base image)"
export LEOFLOW_AUTH_JWT_SECRET="e2e-secret"
export LEOFLOW_BOOTSTRAP_PASSWORD="admin"
export LEOFLOW_SERVER_HTTP_ADDR="0.0.0.0:${HTTP_PORT}"
export LEOFLOW_EXECUTOR_AGENT_CONTROL_PLANE_ADDR="${HOST_ADDR}:${GRPC_PORT}"
export LEOFLOW_LOGS_DIR="${WORKDIR}/logs"
export LEOFLOW_AGENT_ALLOW_INSECURE_SECRETS=true
export DEXAFLOW_DEPLOY_DEFAULTS_REGISTRY="$REG_HOST"
export DEXAFLOW_DEPLOY_DEFAULTS_PLATFORMS="$PLATFORM"
export DEXAFLOW_DEPLOY_DEFAULTS_BASE_IMAGE="$BASE_REF"
"$ROOT/bin/dexaflow-server" >"$SERVER_LOG" 2>&1 &
SERVER_PID=$!
for _ in $(seq 1 90); do
	curl -fsS "$API/readyz" >/dev/null 2>&1 && break
	kill -0 "$SERVER_PID" 2>/dev/null || fail "the control plane exited during boot"
	sleep 1
done
curl -fsS "$API/readyz" >/dev/null 2>&1 || fail "the control plane did not become ready"

ADMIN_USER="$(admin_user "$ROOT/bin/dexaflow" "$API" admin)"
TOKEN="$(admin_token "$ROOT/bin/dexaflow" "$API" admin)"
STAMP="$(date +%s)"
# A deploy token, as `dexaflow ci init` tells a pipeline to create.
DEPLOY_TOKEN="$("$ROOT/bin/dexaflow" auth create-token --server "$API" --username "$ADMIN_USER" \
	--password admin --scope deploy --name "daemonless-e2e-${STAMP}" --ttl 1h)" || fail "could not create a deploy token"

# The example as committed, copied so the compile does not write dag.json into
# the checkout. Nothing in it is changed.
cp -R "$ROOT/examples/$EXAMPLE" "$WORKDIR/$EXAMPLE"
diff -r "$ROOT/examples/$EXAMPLE" "$WORKDIR/$EXAMPLE" >/dev/null || fail "the copy of examples/$EXAMPLE differs from the checkout"

log "dexaflow deploy examples/$EXAMPLE from the dexaflow-cli image, with no container daemon"
# No Docker socket is mounted and the image has no Docker CLI. The container
# shares the host network only to reach the control plane and the k3d registry
# on the loopback; it runs as the invoking user so the files it writes in the
# work directory stay removable. A unique --dag-version keeps a rerun against
# the shared dev database from stopping at "already deployed".
set +e
docker run --rm --network host \
	--user "$(id -u):$(id -g)" \
	-e HOME=/tmp/dexaflow-home \
	-e DEXAFLOW_TOKEN="$DEPLOY_TOKEN" \
	-v "$ROOT/test/e2e:/e2e:ro" \
	-v "$WORKDIR/$EXAMPLE:/work/$EXAMPLE" \
	-w /work \
	--entrypoint bash \
	"$CLI_IMAGE" -c '
set -euo pipefail
bash /e2e/daemonless-deploy-e2e.sh --assert-no-daemon
echo "no container daemon: no container tool on PATH, no daemon socket, no DOCKER_HOST"
uv --version
dexaflow version
dexaflow deploy '"$EXAMPLE"' --server '"$API"' --yes --dag-version "e2e-daemonless-'"$STAMP"'"
' >"$DEPLOY_LOG" 2>&1
rc=$?
set -e
sed 's/^/    /' "$DEPLOY_LOG"
[ "$rc" -eq 0 ] || fail "the daemonless deploy failed (exit $rc, its output is above)"

log "Asserting the deploy took the daemonless dependency layer from wheels"
grep -qx 'no container daemon: no container tool on PATH, no daemon socket, no DOCKER_HOST' "$DEPLOY_LOG" \
	|| fail "the container did not prove that no daemon was reachable"
check_builder_line "$DEPLOY_LOG" || fail "the deploy did not take the daemonless dependency layer"
grep -qE "^registry: +${REG_HOST}/${DAG_ID} \\(from the deploy defaults of " "$DEPLOY_LOG" \
	|| fail "the registry did not come from the cluster deploy defaults"
grep -qE "^base image: ${BASE_REF} \\(from the deploy defaults of " "$DEPLOY_LOG" \
	|| fail "the base image did not come from the cluster deploy defaults"
IMG="$(jq -r '.image' "$WORKDIR/$EXAMPLE/dag.json" || true)"
printf '%s\n' "$IMG" | grep -q "^${REG_HOST}/${DAG_ID}@sha256:" || fail "dag.json is not pinned by digest to the registry: $IMG"
TAGS="$(curl -fsS "http://127.0.0.1:${REG_PORT}/v2/${DAG_ID}/tags/list" | jq -r '.tags[]?' || true)"
printf '%s\n' "$TAGS" | grep -q '^deps-' || fail "the registry holds no dependency image (deps-*) for $DAG_ID; tags: $(printf '%s' "$TAGS" | tr '\n' ' ')"
log "registered image: $IMG"

log "Triggering a run of $DAG_ID and waiting for it to succeed"
unpause_dag "$API" "$DAG_ID" "$TOKEN" || fail "could not unpause $DAG_ID"
RUN_ID="$(curl -fsS -X POST -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
	-d '{}' "$API/api/v2/dags/$DAG_ID/dagRuns" | jq -r '.dag_run_id')"
if [ -z "$RUN_ID" ] || [ "$RUN_ID" = null ]; then fail "no dag_run_id returned"; fi
api() { curl -fsS -H "Authorization: Bearer $TOKEN" "$API$1"; }
deadline=$(($(date +%s) + 600))
while :; do
	# A transient API error is retried until the deadline, not fatal.
	states="$(api "/api/v2/dags/$DAG_ID/dagRuns/$RUN_ID/taskInstances" | jq -r '.task_instances[] | "\(.task_id)=\(.state)"' || true)"
	log "run $RUN_ID: [$(printf '%s' "$states" | tr '\n' ' ')]"
	printf '%s\n' "$states" | grep -qE '=(failed|upstream_failed)$' && fail "a task of run $RUN_ID failed: $states"
	if [ "$(printf '%s\n' "$states" | grep -c '=success$')" -eq 3 ]; then
		break
	fi
	[ "$(date +%s)" -gt "$deadline" ] && fail "run $RUN_ID did not succeed within 600s; last states: ${states:-<none>}"
	sleep 3
done
run_state="$(api "/api/v2/dags/$DAG_ID/dagRuns/$RUN_ID" | jq -r '.state' || true)"
deadline=$(($(date +%s) + 60))
while [ "$run_state" != success ] && [ "$(date +%s)" -lt "$deadline" ]; do
	sleep 2
	run_state="$(api "/api/v2/dags/$DAG_ID/dagRuns/$RUN_ID" | jq -r '.state' || true)"
done
[ "$run_state" = success ] || fail "run $RUN_ID is $run_state, not success, although its tasks succeeded"

# task_log TASK PATTERN fails unless a try of TASK logged PATTERN: the example's
# own print, so the image that ran is the one deploy built from the example.
task_log() {
	local try body
	for try in 1 0 2; do
		body="$(api "/api/v2/dags/$DAG_ID/dagRuns/$RUN_ID/taskInstances/$1/logs/$try" 2>/dev/null || true)"
		if grep -qF "$2" <<<"$body"; then return 0; fi
	done
	fail "no log of task $1 carries '$2'"
}
task_log transform "transform: 1000 rows -> 4 regions"
task_log load "load: totals"

log "DAEMONLESS DEPLOY E2E passed: examples/$EXAMPLE deployed with no container daemon (dependency-layer from wheels with uv, then code-layer), pinned by digest, and ran green on k3d"
