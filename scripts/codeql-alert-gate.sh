#!/usr/bin/env bash
# Fail a pull request that introduces an open CodeQL alert of high or critical
# security severity.
#
# ADR 0014 listed "CodeQL alerts of severity high or critical" among the things
# that stop a merge, and nothing enforced it (#494). The CodeQL job only uploads
# its results: the analysis itself is green whatever it finds, so a pull request
# could merge carrying a new high alert, and `internal/logs/logs.go` carried a
# `high` go/path-injection alert from the repository's first day until a manual
# triage found it (#491).
# ADR 0072 records this gate as an amendment to ADR 0014's CodeQL policy.
#
# What counts. An alert is a repository-level object with one stable number;
# each analysed ref holds instances of it. The pull request's merge ref
# (refs/pull/<n>/merge) and the base branch are each listed, and an alert open
# on the pull request but not on the base is one this pull request introduces.
# Only those fail the gate. An alert already open on the base is reported as a
# warning and does not block: it is not this pull request's to fix, and blocking
# every pull request on it would turn the gate off within a week. The weekly
# schedule and the Security tab are where those are tracked.
#
# Severity is `rule.security_severity_level`, the CVSS-derived level CodeQL
# assigns to security queries. Quality queries (security-and-quality) carry
# none and never block.
#
# Not named check-*.sh on purpose: cut-release.sh runs every scripts/check-*.sh
# with no arguments and no network, and this gate needs a pull request and the
# code scanning API.
#
# Usage:
#   scripts/codeql-alert-gate.sh --pr <number> --base <branch>
#       Lists both refs through `gh api` (GH_TOKEN with security-events: read,
#       GITHUB_REPOSITORY set) and judges them.
#   scripts/codeql-alert-gate.sh --judge <pr-alerts.tsv> <base-alerts.tsv>
#       Judges two listings already on disk.
#   scripts/codeql-alert-gate.sh --self-test
#
# A listing is one alert per line, tab separated:
#   <number> <security_severity_level> <rule id> <path>:<line> <html_url>
set -euo pipefail

# The jq filter that turns the alerts endpoint's JSON into the listing above.
ALERT_TSV_JQ='.[] | [(.number|tostring), (.rule.security_severity_level // "none"), .rule.id, ((.most_recent_instance.location.path // "?") + ":" + ((.most_recent_instance.location.start_line // 0)|tostring)), .html_url] | @tsv'

BLOCKING_RE='^(high|critical)$'

# judge <pr-tsv> <base-tsv>: prints a report and returns 1 when the pull
# request introduces a blocking alert.
judge() {
	local pr_tsv="$1" base_tsv="$2" f num sev rule loc url new=0 old=0
	for f in "$pr_tsv" "$base_tsv"; do
		[ -r "$f" ] || { echo "FAIL: cannot read alert listing '$f'" >&2; return 2; }
	done
	local base_nums
	base_nums=$(cut -f1 "$base_tsv")
	while IFS=$'\t' read -r num sev rule loc url; do
		[ -n "$num" ] || continue
		sev=$(printf '%s' "$sev" | tr '[:upper:]' '[:lower:]')
		[[ "$sev" =~ $BLOCKING_RE ]] || continue
		if grep -qxF "$num" <<<"$base_nums"; then
			echo "::warning::CodeQL alert #${num} (${sev}, ${rule}) at ${loc} is already open on the base branch; not introduced here, not blocking. ${url}"
			old=$((old + 1))
		else
			echo "::error::CodeQL alert #${num} (${sev}, ${rule}) at ${loc} is introduced by this pull request. ${url}"
			new=$((new + 1))
		fi
	done <"$pr_tsv"
	if [ "$new" -gt 0 ]; then
		echo "FAIL: this pull request introduces ${new} open CodeQL alert(s) of high or critical severity."
		echo "Fix them, or, for a false positive, dismiss the alert in the Security tab with a reason and re-run this job."
		return 1
	fi
	echo "OK: no new high or critical CodeQL alerts (${old} already open on the base branch)."
}

# list_alerts <ref>: every open CodeQL alert with an instance on <ref>, as a
# listing. Fails (and so fails the gate) when the API refuses: a gate that
# cannot see the alerts must not report that there are none.
list_alerts() {
	local ref="$1"
	gh api --paginate \
		"repos/${GITHUB_REPOSITORY:?GITHUB_REPOSITORY is not set}/code-scanning/alerts?state=open&tool_name=CodeQL&per_page=100&ref=${ref}" \
		--jq "$ALERT_TSV_JQ"
}

run() { # <pr-number> <base-branch>
	local pr="$1" base="$2" tmp rc=0
	[[ "$pr" =~ ^[0-9]+$ ]] || { echo "FAIL: --pr must be a pull request number, got '$pr'" >&2; return 2; }
	tmp=$(mktemp -d)
	list_alerts "refs/pull/${pr}/merge" >"$tmp/pr.tsv" ||
		{ echo "FAIL: could not list the CodeQL alerts of refs/pull/${pr}/merge" >&2; rm -rf "$tmp"; return 1; }
	list_alerts "refs/heads/${base}" >"$tmp/base.tsv" ||
		{ echo "FAIL: could not list the CodeQL alerts of refs/heads/${base}" >&2; rm -rf "$tmp"; return 1; }
	judge "$tmp/pr.tsv" "$tmp/base.tsv" || rc=$?
	rm -rf "$tmp"
	return "$rc"
}

self_test() {
	local tmp fail=0 out rc
	tmp=$(mktemp -d); trap 'rm -rf "$tmp"' RETURN
	t() { printf '%s\t%s\t%s\t%s\t%s\n' "$@"; }
	_expect() { # <name> <want-rc> <want-substring> <pr-tsv> <base-tsv>
		rc=0; out=$(judge "$4" "$5" 2>&1) || rc=$?
		if [ "$rc" != "$2" ]; then
			printf '  FAIL %s: exit %s, want %s\n%s\n' "$1" "$rc" "$2" "$out"; fail=1; return
		fi
		case "$out" in
			*"$3"*) printf '  ok   %s\n' "$1" ;;
			*) printf '  FAIL %s: output lacks %q\n%s\n' "$1" "$3" "$out"; fail=1 ;;
		esac
	}

	: >"$tmp/empty"
	t 7 high go/path-injection internal/logs/logs.go:42 https://x/7 >"$tmp/high-new"
	t 8 critical go/sql-injection internal/storage/q.go:9 https://x/8 >"$tmp/crit-new"
	t 9 medium go/log-injection internal/a.go:1 https://x/9 >"$tmp/medium"
	t 10 none go/unused-variable internal/b.go:3 https://x/10 >"$tmp/quality"

	_expect "no alerts passes" 0 "no new high or critical" "$tmp/empty" "$tmp/empty"
	_expect "a new high alert fails" 1 "go/path-injection" "$tmp/high-new" "$tmp/empty"
	_expect "a new critical alert fails" 1 "go/sql-injection" "$tmp/crit-new" "$tmp/empty"
	_expect "a new medium alert passes" 0 "no new high or critical" "$tmp/medium" "$tmp/empty"
	_expect "a quality alert passes" 0 "no new high or critical" "$tmp/quality" "$tmp/empty"
	# The same alert number open on the base is pre-existing: warn, do not block.
	_expect "a high alert already on the base warns" 0 "already open on the base" "$tmp/high-new" "$tmp/high-new"
	# A pre-existing alert must not hide a new one next to it.
	cat "$tmp/high-new" "$tmp/crit-new" >"$tmp/both"
	_expect "a new alert beside a pre-existing one fails" 1 "go/sql-injection" "$tmp/both" "$tmp/high-new"
	# A severity in another case is still that severity.
	t 11 HIGH go/x internal/c.go:1 https://x/11 >"$tmp/upper"
	_expect "severity is case-insensitive" 1 "go/x" "$tmp/upper" "$tmp/empty"
	# A listing that does not exist is an error, never an empty pass.
	_expect "a missing listing fails closed" 2 "cannot read" "$tmp/nope" "$tmp/empty"

	# The jq filter and the judge must agree on the listing format.
	if command -v jq >/dev/null 2>&1; then
		printf '%s' '[{"number":5,"html_url":"https://x/5","rule":{"id":"go/path-injection","security_severity_level":"high"},"most_recent_instance":{"location":{"path":"a.go","start_line":3}}},{"number":6,"html_url":"https://x/6","rule":{"id":"go/q"},"most_recent_instance":{"location":{}}}]' |
			jq -r "$ALERT_TSV_JQ" >"$tmp/from-json"
		_expect "the API filter feeds the judge" 1 "a.go:3" "$tmp/from-json" "$tmp/empty"
		grep -q $'^6\tnone\tgo/q\t?:0\t' "$tmp/from-json" ||
			{ echo "  FAIL the API filter mangled an alert with no severity or location"; fail=1; }
	fi

	# No arguments is a usage error, not a pass.
	rc=0; bash "${BASH_SOURCE[0]}" >/dev/null 2>&1 || rc=$?
	if [ "$rc" -eq 2 ]; then echo "  ok   no arguments is a usage error"
	else echo "  FAIL no arguments exited $rc, want 2"; fail=1; fi

	if [ "$fail" -eq 0 ]; then echo "codeql-alert-gate self-test: ok"; return 0; fi
	echo "codeql-alert-gate self-test: FAIL"; return 1
}

usage() {
	sed -n '/^# Usage:/,/^# A listing/p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//' >&2
	exit 2
}

case "${1:-}" in
	--self-test) self_test; exit $? ;;
	--judge) [ $# -eq 3 ] || usage; judge "$2" "$3"; exit $? ;;
	--pr)
		if [ $# -ne 4 ] || [ "$3" != "--base" ]; then usage; fi
		run "$2" "$4"; exit $? ;;
	*) usage ;;
esac
