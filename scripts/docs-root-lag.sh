#!/usr/bin/env bash
# Fail when the documentation root lags the newest GA release.
#
# The `latest` leg of website/scripts/ci/versions.json is the ref the site root
# is built from. Only the docs promotion moves it, and that runs at the end of
# scripts/cut-release.sh, after the release is published. When the cut does not
# get there (v0.5.4 was tagged from a terminal and the cut never reached its
# last step), the root keeps serving the previous GA and nothing says so: after
# v0.5.4 it was found by hand, reviewing the release (#1936).
#
# The newest GA is the GitHub release marked Latest, which only the `promote`
# job of release.yaml moves, by the rule of scripts/release-promote-plan.sh
# (not older than any published GA). The root and that release must name the
# same tag. Right after a GA the promotion pull request still needs its CI, so
# a lag younger than DOCS_LAG_GRACE_HOURS (default 6) since the release was
# published is reported and passes.
#
# Not a check-*.sh gate: it needs the network, and the release cut runs every
# check-*.sh offline. .github/workflows/docs-root-lag.yaml runs it daily and
# when versions.json changes on main, with --issue.
#
# Usage:
#   scripts/docs-root-lag.sh [root]           # exit 1 when the root lags
#   scripts/docs-root-lag.sh --issue [root]   # also open, refresh or close the
#                                             # one tracking issue
#   scripts/docs-root-lag.sh --self-test
#
# Environment: GITHUB_REPOSITORY (owner/name) and GH_TOKEN for the API.
# DOCS_LAG_RELEASE_JSON, when set, replaces the API answer for the release
# marked Latest ({"tag_name": ..., "published_at": ...}), for tests and dry
# runs. RUN_URL, when set, is linked from the issue.
set -euo pipefail

ISSUE_TITLE="docs: the documentation root does not serve the newest release"

# docs_ref <root>: the ref of the `latest` leg of versions.json.
docs_ref() {
	local f="$1/website/scripts/ci/versions.json"
	[ -f "$f" ] || { echo "cannot read $f" >&2; return 1; }
	jq -er '[.versions[]? | select(.id == "latest") | .ref] | if length == 1 then .[0] else error("no single latest leg") end' "$f" 2>/dev/null ||
		{ echo "versions.json has no single \`latest\` leg" >&2; return 1; }
}

is_ga() { [[ "$1" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]]; }

# lag_verdict <docs-ref> <latest-tag> <age-seconds> <grace-seconds>: one of
#   ok       the root serves the release marked Latest
#   pending  the root lags, but the release is younger than the grace period
#   lag      the root serves an older release than the one marked Latest
#   ahead    the root serves a newer release than the one marked Latest
#   unknown  the release marked Latest or the root's ref is not a GA tag
lag_verdict() {
	local docs="$1" latest="$2" age="$3" grace="$4"
	if ! is_ga "$latest" || ! is_ga "$docs" || ! [[ "$age" =~ ^-?[0-9]+$ ]]; then echo unknown; return; fi
	if [ "$docs" = "$latest" ]; then echo ok; return; fi
	if [ "$(printf '%s\n%s\n' "${docs#v}" "${latest#v}" | sort -V | tail -n1)" = "${docs#v}" ]; then echo ahead; return; fi
	if [ "$age" -lt "$grace" ]; then echo pending; return; fi
	echo lag
}

# issue_action <verdict> <open-issue-number>: what to do with the tracking
# issue. create, update, close or none.
issue_action() {
	local verdict="$1" number="$2"
	case "$verdict" in
		lag | ahead) if [ -n "$number" ]; then echo update; else echo create; fi ;;
		ok) if [ -n "$number" ]; then echo close; else echo none; fi ;;
		*) echo none ;;
	esac
}

# latest_release: the release marked Latest, as {"tag_name", "published_at"}.
latest_release() {
	if [ -n "${DOCS_LAG_RELEASE_JSON:-}" ]; then printf '%s\n' "$DOCS_LAG_RELEASE_JSON"; return; fi
	: "${GITHUB_REPOSITORY:?set GITHUB_REPOSITORY or DOCS_LAG_RELEASE_JSON}"
	gh api "repos/${GITHUB_REPOSITORY}/releases/latest" --jq '{tag_name, published_at}'
}

# open_issue_number: the open tracking issue, by its exact title among the
# open issues this workflow's token opened, or nothing.
open_issue_number() {
	gh api --paginate -X GET "repos/${GITHUB_REPOSITORY}/issues" -f state=open -f creator='github-actions[bot]' -f per_page=100 \
		--jq ".[] | select(.pull_request == null and .title == \"${ISSUE_TITLE}\") | .number" | sed -n 1p
}

issue_body() { # <verdict> <docs-ref> <latest-tag>
	local relation="an older release than"
	[ "$1" = ahead ] && relation="a newer release than"
	cat <<EOF
The documentation root at https://dexaflow.dexadata.ai/ is built from \`$2\`, $relation \`$3\`, the GitHub release marked Latest.

The \`latest\` leg of \`website/scripts/ci/versions.json\` on \`main\` names \`$2\`. The docs promotion that repoints it did not land. To promote by hand, follow "Publishing the docs root (GA)" in RELEASING.md.

This issue is kept by \`.github/workflows/docs-root-lag.yaml\` (\`scripts/docs-root-lag.sh\`): its body is refreshed on every run and it closes itself once the root serves \`$3\`.${RUN_URL:+ Last checked by [this run]($RUN_URL).}
EOF
}

# sync_issue <verdict> <docs-ref> <latest-tag>: open, refresh or close the one
# tracking issue, by issue_action.
sync_issue() {
	local verdict="$1" docs="$2" latest="$3" number action
	: "${GITHUB_REPOSITORY:?--issue needs GITHUB_REPOSITORY}"
	number="$(open_issue_number)"
	action="$(issue_action "$verdict" "$number")"
	case "$action" in
		create)
			number="$(issue_body "$verdict" "$docs" "$latest" | gh api "repos/${GITHUB_REPOSITORY}/issues" -f title="$ISSUE_TITLE" -F body=@- --jq .number)"
			echo "opened #$number" ;;
		update)
			issue_body "$verdict" "$docs" "$latest" | gh api -X PATCH "repos/${GITHUB_REPOSITORY}/issues/$number" -F body=@- --silent
			echo "refreshed #$number" ;;
		close)
			gh api "repos/${GITHUB_REPOSITORY}/issues/$number/comments" -f body="The documentation root serves \`$latest\` again.${RUN_URL:+ Checked by [this run]($RUN_URL).} Closing." --silent
			gh api -X PATCH "repos/${GITHUB_REPOSITORY}/issues/$number" -f state=closed -f state_reason=completed --silent
			echo "closed #$number" ;;
		none) ;;
	esac
}

main() { # [--issue] [root]
	local issue="" root rel latest published age docs verdict grace
	if [ "${1:-}" = "--issue" ]; then issue=1; shift; fi
	root="${1:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
	grace=$(( ${DOCS_LAG_GRACE_HOURS:-6} * 3600 ))
	docs="$(docs_ref "$root")" || return 1
	rel="$(latest_release)" || { echo "FAIL: cannot read the release marked Latest" >&2; return 1; }
	latest="$(printf '%s' "$rel" | jq -r '.tag_name // empty')"
	published="$(printf '%s' "$rel" | jq -r '.published_at // empty')"
	age=$(( $(date -u +%s) - $(date -u -d "${published:-1970-01-01T00:00:00Z}" +%s) ))
	verdict="$(lag_verdict "$docs" "$latest" "$age" "$grace")"
	case "$verdict" in
		ok) echo "OK: the docs root serves $latest, the release marked Latest." ;;
		pending) echo "OK for now: the docs root serves $docs and $latest was published $((age / 60)) minutes ago; its docs promotion has until $((grace / 3600)) hours." ;;
		lag) echo "FAIL: the docs root serves $docs, older than $latest, the release marked Latest. Promote it (RELEASING.md, \"Publishing the docs root (GA)\")." >&2 ;;
		ahead) echo "FAIL: the docs root serves $docs, newer than $latest, the release marked Latest. Check release.yaml's promote job for $docs." >&2 ;;
		unknown) echo "FAIL: cannot compare the docs root '$docs' with the release marked Latest '$latest'." >&2 ;;
	esac
	if [ -n "$issue" ]; then sync_issue "$verdict" "$docs" "$latest"; fi
	case "$verdict" in ok | pending) return 0 ;; *) return 1 ;; esac
}

self_test() {
	local fail=0 tmp rc
	tmp="$(mktemp -d)"; trap 'rm -rf "$tmp"' RETURN
	_eq() { if [ "$1" = "$2" ]; then printf '  ok   %s\n' "$3"; else printf '  FAIL %s\n    got:  %s\n    want: %s\n' "$3" "$1" "$2"; fail=1; fi; }

	_eq "$(lag_verdict v0.5.4 v0.5.4 99999 21600)" ok "the root serving the Latest release is ok"
	_eq "$(lag_verdict v0.5.3 v0.5.4 99999 21600)" lag "the root serving the GA before Latest lags (#1936)"
	_eq "$(lag_verdict v0.5.3 v0.5.4 600 21600)" pending "a lag inside the grace period is pending"
	_eq "$(lag_verdict v0.5.3 v0.5.4 21600 21600)" lag "the grace period ends on the hour"
	_eq "$(lag_verdict v0.5.9 v0.5.10 99999 21600)" lag "versions compare numerically, not as text"
	_eq "$(lag_verdict v0.5.5 v0.5.4 99999 21600)" ahead "a root newer than Latest is reported"
	_eq "$(lag_verdict v0.5.5 v0.5.4 0 21600)" ahead "and is never excused by the grace period"
	_eq "$(lag_verdict v0.5.4 '' 0 21600)" unknown "no release marked Latest is unknown, not ok"
	_eq "$(lag_verdict v0.5.4 v0.6.0-rc.1 0 21600)" unknown "an rc marked Latest is unknown"
	_eq "$(lag_verdict main v0.5.4 0 21600)" unknown "a root built from a branch is unknown"

	_eq "$(issue_action lag '')" create "a lag with no open issue opens one"
	_eq "$(issue_action lag 12)" update "a lag with an open issue refreshes it"
	_eq "$(issue_action ahead '')" create "a root ahead of Latest opens one too"
	_eq "$(issue_action ok 12)" close "a root back in step closes the open issue"
	_eq "$(issue_action ok '')" none "a root in step with no issue does nothing"
	_eq "$(issue_action pending 12)" none "a pending lag leaves the issue alone"
	_eq "$(issue_action unknown '')" none "an unknown verdict opens nothing"

	# The entrypoint, against a fixture root, with the API answer injected.
	mkdir -p "$tmp/website/scripts/ci"
	printf '{"versions":[{"id":"latest","ref":"v0.5.3"},{"id":"dev","ref":""}]}\n' >"$tmp/website/scripts/ci/versions.json"
	_eq "$(docs_ref "$tmp")" v0.5.3 "the root's ref comes from the latest leg"
	_run() { rc=0; DOCS_LAG_RELEASE_JSON="$1" bash "${BASH_SOURCE[0]}" "$tmp" >/dev/null 2>&1 || rc=$?; echo "$rc"; }
	_eq "$(_run '{"tag_name":"v0.5.4","published_at":"2020-01-01T00:00:00Z"}')" 1 "the entrypoint fails a root that lags"
	_eq "$(_run '{"tag_name":"v0.5.3","published_at":"2020-01-01T00:00:00Z"}')" 0 "and passes one in step"
	_eq "$(_run "{\"tag_name\":\"v0.5.4\",\"published_at\":\"$(date -u +%Y-%m-%dT%H:%M:%SZ)\"}")" 0 "and passes a lag of a release published just now"
	_eq "$(_run '{}')" 1 "and fails when the Latest release cannot be read"
	printf '{"versions":[{"id":"latest","ref":"v0.5.4"},{"id":"latest","ref":"v0.5.3"}]}\n' >"$tmp/website/scripts/ci/versions.json"
	_eq "$(_run '{"tag_name":"v0.5.4","published_at":"2020-01-01T00:00:00Z"}')" 1 "and fails a versions.json with two latest legs"

	if [ "$fail" -eq 0 ]; then echo "docs-root-lag self-test: ok"; return 0; fi
	echo "docs-root-lag self-test: FAIL"; return 1
}

case "${1:-}" in
	--self-test) self_test; exit $? ;;
	*) main "$@"; exit $? ;;
esac
