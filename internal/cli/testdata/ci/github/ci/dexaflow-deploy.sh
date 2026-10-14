#!/bin/sh
# Deploys the DAGs of this workspace that changed to Dexaflow.
# Written by `dexaflow ci init` for dexaflow v0.6.0; write it again with
# `dexaflow ci init <system> --force` after upgrading the CLI.
#
# Usage: sh ci/dexaflow-deploy.sh [deploy|install]
#   deploy   (the default) install the CLI and uv when missing, then deploy
#   install  only install the pinned CLI
#
# Environment:
#   DEXAFLOW_SERVER_URL    the control plane URL (required)
#   DEXAFLOW_TOKEN         a deploy token (required), created once with
#                          `dexaflow auth create-token --scope deploy --name ci-<repo> --ttl 90d`
#   DEXAFLOW_BASE_REF      the commit the branch was on before this push: only
#                          the DAGs changed since it are deployed. Empty, all
#                          zeros (a new branch) or missing from the clone
#                          deploys every DAG.
#   DEXAFLOW_SUMMARY_FILE  a file to append a Markdown summary to (default:
#                          $GITHUB_STEP_SUMMARY, else the log)
#   DEXAFLOW_REPORT_FILE   a file to copy the `dexaflow deploy --output json`
#                          report to (optional)
#   DEXAFLOW_CLI_VERSION   the release to install instead of v0.6.0 (a tag
#                          such as v0.6.0, v0.5.0 or later)
# The pre-rename LEOFLOW_TOKEN, LEOFLOW_SERVER_URL and LEOFLOW_TARGET still work.
#
# The CLI is installed by the install.sh of its release tag, which checks the
# archive against the release's checksums.txt, and its cosign signature when
# cosign is on PATH (then required). uv comes from PyPI, checked against the
# wheel hashes below.
set -eu

cli_version="${DEXAFLOW_CLI_VERSION:-v0.6.0}"
DEXAFLOW_TOKEN="${DEXAFLOW_TOKEN:-${LEOFLOW_TOKEN:-}}"
DEXAFLOW_SERVER_URL="${DEXAFLOW_SERVER_URL:-${LEOFLOW_SERVER_URL:-}}"
export DEXAFLOW_TOKEN DEXAFLOW_SERVER_URL
uv_version="0.12.24"
workspace_dir="."

die() {
	echo "error: $*" >&2
	exit 1
}

# The version ends up in a URL and in install.sh: only a release tag is taken.
case "$cli_version" in
v[0-9]*.[0-9]*.[0-9]*)
	case "$cli_version" in
	*[!0-9A-Za-z.-]*) die "DEXAFLOW_CLI_VERSION=$cli_version is not a release tag such as v0.6.0" ;;
	esac
	;;
*) die "DEXAFLOW_CLI_VERSION=$cli_version is not a release tag such as v0.6.0" ;;
esac

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

# install_dexaflow installs the pinned CLI with the release's install.sh,
# unless that version is already on PATH.
install_dexaflow() {
	if command -v dexaflow >/dev/null 2>&1 &&
		[ "$(dexaflow version 2>/dev/null | awk '{print $2}')" = "${cli_version#v}" ]; then
		return 0
	fi
	dir="${DEXAFLOW_INSTALL_DIR:-$HOME/.dexaflow/bin}"
	url="https://raw.githubusercontent.com/dexadata/dexaflow/$cli_version/install.sh"
	if command -v curl >/dev/null 2>&1; then
		curl -fsSL "$url" -o "$tmp/install.sh"
	else
		wget -qO "$tmp/install.sh" "$url"
	fi
	if command -v cosign >/dev/null 2>&1; then
		DEXAFLOW_INSTALL_REQUIRE_SIGNATURE=1
		export DEXAFLOW_INSTALL_REQUIRE_SIGNATURE
	fi
	DEXAFLOW_VERSION="$cli_version" DEXAFLOW_NO_SETUP=1 DEXAFLOW_NO_PATH=1 \
		DEXAFLOW_INSTALL_DIR="$dir" sh "$tmp/install.sh"
	PATH="$dir:$PATH"
	export PATH
	if [ -n "${GITHUB_PATH:-}" ]; then
		echo "$dir" >>"$GITHUB_PATH"
	fi
}

# install_uv puts uv on PATH, so dependency images are built from wheels
# without a container daemon. Without it the remote builder or the local
# daemon builds them.
install_uv() {
	command -v uv >/dev/null 2>&1 && return 0
	echo "uv==$uv_version --hash=sha256:e47005957dca320272ad68960b591b854b6bf0c3288368efb096d9ccd06b8404 --hash=sha256:94c9ed22c1b5d6861a72639e23c5707bb07a354b67ec0462e9a0169a9665b5fd --hash=sha256:0fc14fabb78c55d140a01125d5cd038afb4fed317eb0c3868f3fba87b8cefe16 --hash=sha256:b6fe9cd01f5c576a640f17829cb465899fffb5d6d8dfdd576e38d4f1e40dd438" >"$tmp/uv.txt"
	if python3 -m pip install --quiet --disable-pip-version-check --only-binary :all: \
		--require-hashes -r "$tmp/uv.txt" >/dev/null 2>&1 &&
		command -v uv >/dev/null 2>&1; then
		return 0
	fi
	echo "note: uv could not be installed; dependency images are built by the remote builder or the container daemon" >&2
}

# base_ref prints the commit to compare with, or nothing to deploy every DAG.
base_ref() {
	base="${DEXAFLOW_BASE_REF:-}"
	case "$base" in
	*[!0]*) ;;
	*) return 0 ;;
	esac
	if ! git rev-parse --git-dir >/dev/null 2>&1; then
		echo "note: not a git checkout, so every DAG is deployed" >&2
		return 0
	fi
	if ! git cat-file -e "$base^{commit}" 2>/dev/null; then
		echo "note: $base is not in this clone (a force push, or a shallow clone), so every DAG is deployed" >&2
		return 0
	fi
	echo "$base"
}

# summarize prints a Markdown summary of the deploy report $1.
summarize() {
	python3 - "$1" <<'PY'
import json
import sys

try:
    with open(sys.argv[1]) as f:
        report = json.load(f)
except (OSError, ValueError):
    print("Dexaflow deploy wrote no report; see the job log.")
    sys.exit(0)


def cell(value):
    return str(value or "").replace("|", "\\|").replace("\n", " ")


where = report.get("server") or ""
if report.get("target"):
    where += " (target %s)" % report["target"]
print("### Dexaflow deploy to %s" % cell(where))
print()
dags = report.get("dags") or []
if dags:
    print("| DAG | Status | Version | Image | Note |")
    print("| --- | --- | --- | --- | --- |")
    for d in dags:
        note = d.get("error") or d.get("reason") or d.get("next")
        print("| %s | %s | %s | %s | %s |" % tuple(cell(v) for v in (
            d.get("dag_id"), d.get("status"), d.get("version"), d.get("image"), note)))
else:
    print("No DAG changed, so nothing was deployed.")
for p in report.get("removed") or []:
    print()
    print("- %s (%s) was removed and is still registered. If its DAG should go, remove it with "
          "`dexaflow dags delete %s --deregister`." % (cell(p.get("path")), cell(p.get("dag_id")), cell(p.get("dag_id"))))
for p in report.get("moved") or []:
    print()
    print("- %s moved from %s to %s." % (cell(p.get("dag_id")), cell(p.get("from")), cell(p.get("to"))))
PY
}

deploy() {
	[ -n "${DEXAFLOW_TOKEN:-}" ] ||
		die "DEXAFLOW_TOKEN is not set: store a deploy token as a CI secret (dexaflow auth create-token --scope deploy)"
	[ -n "${DEXAFLOW_SERVER_URL:-}" ] ||
		die "DEXAFLOW_SERVER_URL is not set: set it to the control plane URL"
	base="$(base_ref)"
	report="$tmp/report.json"
	status=0
	if [ -n "$base" ]; then
		dexaflow deploy --all --changed-since "$base" --yes --output json >"$report" || status=$?
	else
		dexaflow deploy --all --yes --output json >"$report" || status=$?
	fi
	summarize "$report" >"$tmp/summary.md" || true
	out="${DEXAFLOW_SUMMARY_FILE:-${GITHUB_STEP_SUMMARY:-}}"
	if [ -n "$out" ]; then
		cat "$tmp/summary.md" >>"$out"
	else
		cat "$tmp/summary.md"
	fi
	if [ -n "${DEXAFLOW_REPORT_FILE:-}" ]; then
		cp "$report" "$DEXAFLOW_REPORT_FILE"
	fi
	return "$status"
}

cd "$(dirname "$0")/.."
cd "$workspace_dir"
case "${1:-deploy}" in
install) install_dexaflow ;;
deploy)
	install_dexaflow
	install_uv
	deploy
	;;
*) die "usage: sh ci/dexaflow-deploy.sh [deploy|install]" ;;
esac
