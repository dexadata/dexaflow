#!/usr/bin/env bash
# Fail if the Helm chart's version/appVersion lags the release tag.
#
# ADR 0028: the chart `version` and `appVersion` move in lockstep with the
# release tag — a `vX.Y.Z` tag ships a chart pinned to `X.Y.Z`. The image tags
# default to `.Values.image.tag | default .Chart.AppVersion`
# (helm/dexaflow/templates/_helpers.tpl, migration-job.yaml), so if a cut tags
# `v0.4.0-rc.3` while Chart.yaml still says `0.4.0-rc.2`, a DEFAULT
# `helm install ./helm/dexaflow` pulls the WRONG (previous) images and the
# operator has to `--set image.tag=… --set migrations.image.tag=…` by hand.
# The published OCI chart stamps version/appVersion from the tag at package
# time, so this gate protects the source tree (the from-source install path and
# the RC cluster-validation runbook) and enforces the lockstep invariant so a
# future cut cannot tag with a stale Chart.yaml.
#
# Usage:
#   scripts/check-chart-version-matches-tag.sh [TAG]
#       TAG defaults to $GITHUB_REF_NAME (the pushed tag in CI). A leading `v`
#       is stripped: a Helm chart version must be SemVer 2 (no `v` prefix),
#       matching how GoReleaser's semver image tag strips it too.
#   scripts/check-chart-version-matches-tag.sh --self-test
#       Run the built-in pass/fail cases and exit; no repo state is touched.
#
# Overridable for the self-test only:
#   CHART_FILE   path to the Chart.yaml to inspect (default: helm/dexaflow/Chart.yaml)
set -euo pipefail

CHART_FILE="${CHART_FILE:-helm/dexaflow/Chart.yaml}"

# Read a top-level scalar key from Chart.yaml without a yq dependency (the helm
# lint job has helm but not yq). Strips surrounding quotes and inline comments.
chart_key() {
	local key="$1" file="$2"
	awk -v k="^${key}:" '
		$0 ~ k {
			sub(/^[^:]*:[[:space:]]*/, "")   # drop "key:" and leading space
			sub(/[[:space:]]*#.*$/, "")      # drop trailing inline comment
			gsub(/^["'\'']|["'\'']$/, "")    # drop surrounding quotes
			print
			exit
		}
	' "$file"
}

# Compare Chart.yaml version/appVersion against an expected (v-stripped) version.
# Prints a diagnostic and returns non-zero on any mismatch or missing key.
check_chart() {
	local file="$1" want="$2"
	local version appversion rc=0

	if [ ! -f "$file" ]; then
		echo "FAIL: chart file not found: $file" >&2
		return 1
	fi

	version="$(chart_key version "$file")"
	appversion="$(chart_key appVersion "$file")"

	if [ -z "$version" ]; then
		echo "FAIL: could not read 'version' from $file" >&2
		rc=1
	elif [ "$version" != "$want" ]; then
		echo "FAIL: chart version '$version' != tag '$want' (from $file)" >&2
		rc=1
	fi

	if [ -z "$appversion" ]; then
		echo "FAIL: could not read 'appVersion' from $file" >&2
		rc=1
	elif [ "$appversion" != "$want" ]; then
		echo "FAIL: chart appVersion '$appversion' != tag '$want' (from $file)" >&2
		rc=1
	fi

	if [ "$rc" -ne 0 ]; then
		echo >&2
		echo "Bump helm/dexaflow/Chart.yaml 'version' and 'appVersion' to '$want'" >&2
		echo "BEFORE cutting the tag (ADR 0028: chart moves in lockstep with the tag)." >&2
		return 1
	fi

	echo "OK: chart version + appVersion == '$want' ($file)"
}

self_test() {
	local tmp rc=0 out
	tmp="$(mktemp -d)"
	trap 'rm -rf "${tmp:-}"' EXIT

	# Case 1: matching version + appVersion → pass.
	cat > "$tmp/Chart.yaml" <<-'EOF'
		apiVersion: v2
		name: leoflow
		# version tracks the tag (ADR 0028)
		version: 0.4.0-rc.3
		appVersion: "0.4.0-rc.3"
	EOF
	if out="$(check_chart "$tmp/Chart.yaml" "0.4.0-rc.3" 2>&1)"; then
		echo "self-test 1 (match) PASS"
	else
		echo "self-test 1 (match) FAIL — expected pass, got:"; echo "$out"; rc=1
	fi

	# Case 2: stale version + appVersion vs the tag → fail (the #3 bug).
	if out="$(check_chart "$tmp/Chart.yaml" "0.4.0" 2>&1)"; then
		echo "self-test 2 (stale) FAIL — expected fail, got pass:"; echo "$out"; rc=1
	else
		echo "self-test 2 (stale) PASS"
	fi

	# Case 3: leading `v` on the tag is stripped by the caller, not here — verify
	# a bare-vs-v mismatch is caught (defends the v-strip contract at the CLI).
	if check_chart "$tmp/Chart.yaml" "v0.4.0-rc.3" >/dev/null 2>&1; then
		echo "self-test 3 (v-prefix) FAIL — 'v0.4.0-rc.3' should not equal '0.4.0-rc.3'"; rc=1
	else
		echo "self-test 3 (v-prefix) PASS"
	fi

	# Case 4: appVersion lags while version matches → still fails (both keys checked).
	cat > "$tmp/Chart.yaml" <<-'EOF'
		version: 0.4.0-rc.3
		appVersion: "0.4.0-rc.2"
	EOF
	if check_chart "$tmp/Chart.yaml" "0.4.0-rc.3" >/dev/null 2>&1; then
		echo "self-test 4 (appVersion lag) FAIL — expected fail, got pass"; rc=1
	else
		echo "self-test 4 (appVersion lag) PASS"
	fi

	if [ "$rc" -eq 0 ]; then
		echo "self-test: all cases passed"
	fi
	return "$rc"
}

main() {
	cd "$(dirname "$0")/.."

	if [ "${1:-}" = "--self-test" ]; then
		self_test
		return
	fi

	local tag="${1:-${GITHUB_REF_NAME:-}}"
	if [ -z "$tag" ]; then
		echo "FAIL: no tag given (pass one as \$1 or set \$GITHUB_REF_NAME)" >&2
		echo "Usage: $0 [TAG] | --self-test" >&2
		return 2
	fi

	# Strip a single leading `v`: `v0.4.0-rc.3` → `0.4.0-rc.3`.
	local want="${tag#v}"
	check_chart "$CHART_FILE" "$want"
}

main "$@"
