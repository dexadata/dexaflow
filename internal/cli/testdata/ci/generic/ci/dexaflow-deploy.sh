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
set -eu

cli_version="${DEXAFLOW_CLI_VERSION:-v0.6.0}"
uv_version="0.12.24"
workspace_dir="."

die() {
	echo "error: $*" >&2
	exit 1
}

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
	if python3 -m pip install --quiet --disable-pip-version-check "uv==$uv_version" >/dev/null 2>&1 &&
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
    print("- %s was removed from %s and is still registered: pause it with `dexaflow admin dags pause %s`."
          % (cell(p.get("dag_id")), cell(p.get("path")), cell(p.get("dag_id"))))
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
