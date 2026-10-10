#!/usr/bin/env bash
# Fail a PR that changes user-facing surface without touching the docs site.
#
# The CHANGELOG gate exists because entries "repeatedly merged with none and had
# to be back-filled by separate PRs (at least eight)". The docs are in exactly
# that state, and it is not hypothetical: taskPodSecurity.readOnlyRootFilesystem
# — the security hardening of the v0.4.5 tranche — shipped in rc.3 with ZERO
# mentions anywhere in website/content/. It was validated on a real cluster the
# same day and nobody noticed, because nothing looks.
#
# "User-facing surface" is deliberately narrow, so the gate is about capability,
# not churn: a chart value an operator sets, the authoring schema a user writes,
# and the CLI commands they run. Internal refactors do not trip it.
#
# The surface grew after v0.5.1, whose PRs added server settings, migrations
# and API behaviour that reached the CHANGELOG but not the docs site, because
# none of those paths was on the list. It now also covers the server settings
# (internal/config), the schema migrations, the OpenAPI document, and any change
# whose changelog fragment is Added, Changed, Deprecated or Removed: a fragment
# of those kinds is the author saying users will see something different.
# Fixed and Security fragments alone do not count; a fix restores documented
# behaviour, and one that changes it is expected to say so with Changed.
#
# Escape hatch: the `skip-docs` label, for PRs that genuinely change nothing a
# user could discover — release prep, dependabot, pure internals. Same shape as
# skip-changelog, and the workflow re-runs on label so it takes effect at once.
# The label needs a reason: a line `Skip-docs: <why>` in the PR description, so
# the decision can be read back at release time (scripts/docs-gap.sh reads it).
#
# Usage: scripts/check-docs-updated.sh <base-ref> [changed-files-file]
#        SKIP_DOCS_LABEL=1 PR_BODY="..." scripts/check-docs-updated.sh <base-ref> <file>
#        scripts/check-docs-updated.sh --self-test
#
# scripts/docs-gap.sh sources this file for user_facing() and
# skip_docs_reason(), so the per-PR guard and the release cut judge a change
# by the same rule.
set -euo pipefail

DOCS_DIR="website/content"
# Paths whose change implies something an operator or author can see.
SURFACE_RE='^(helm/dexaflow/values\.yaml|docs/api/leoflow-yaml-schema\.json|internal/domain/schemas/.+|internal/cli/[a-z0-9_]+\.go|internal/config/[a-z0-9_]+\.go|internal/api/openapi\.yaml|migrations/[0-9]+_[a-z0-9_]+\.up\.sql)$'
# Changelog fragments (changie) and the kinds that announce a visible change.
FRAGMENT_RE='^\.changes/unreleased/[^/]+\.ya?ml$'
FRAGMENT_KINDS_RE='^(Added|Changed|Deprecated|Removed)$'
# A Go TEST file is not user-facing surface. `[a-z_]+\.go` matched
# compile_baseimage_integration_test.go, so this gate blocked a test-only PR and
# sent its author looking for a `skip-docs` label that did not exist. Digits are
# allowed in the name now too; `internal/domain/schemas/` gained `.+` because the
# alternation is `$`-anchored and a bare directory prefix matched nothing.
NOT_SURFACE_RE='(_test\.go|/testdata/)$'

# fragment_kind <path> [rev]: the `kind:` of a changelog fragment, read from
# <rev> when one is given and from the working tree otherwise. Empty when the
# file is gone (a PR that deletes a fragment announces nothing).
fragment_kind() {
	local path="$1" rev="${2:-}" text
	if [ -n "$rev" ]; then
		text=$(git show "$rev:$path" 2>/dev/null) || return 0
	else
		[ -f "$path" ] || return 0
		text=$(cat "$path")
	fi
	printf '%s\n' "$text" | sed -nE 's/^kind:[[:space:]]*"?([A-Za-z]+)"?[[:space:]]*$/\1/p' | head -1
}

# user_facing <changed-files> [rev]: prints the changed paths that make a
# change user-facing, one per line: surface paths that are not tests, and
# changelog fragments of a visible kind (shown as "<path> (<kind>)").
user_facing() {
	local changed="$1" rev="${2:-}" f kind
	printf '%s\n' "$changed" | grep -E "$SURFACE_RE" | grep -vE "$NOT_SURFACE_RE" || true
	while IFS= read -r f; do
		[ -n "$f" ] || continue
		kind=$(fragment_kind "$f" "$rev")
		if printf '%s\n' "$kind" | grep -qE "$FRAGMENT_KINDS_RE"; then
			printf '%s (%s)\n' "$f" "$kind"
		fi
	done < <(printf '%s\n' "$changed" | grep -E "$FRAGMENT_RE" || true)
}

# touches_docs <changed-files>: true when the change edits the docs site.
# A here-string, not a pipe: grep -q exits at the first match, and under
# pipefail the writer's SIGPIPE on a long file list would read as "no docs".
touches_docs() { grep -qE "^${DOCS_DIR}/" <<<"$1"; }

# skip_docs_reason <pr-body>: the reason a `Skip-docs:` line gives, empty when
# there is none. Case-insensitive on the key, and the reason must say
# something: "Skip-docs:" alone, or followed only by spaces, is no reason.
skip_docs_reason() {
	printf '%s\n' "$1" | tr -d '\r' | sed -nE 's/^[[:space:]]*[Ss][Kk][Ii][Pp]-[Dd][Oo][Cc][Ss]:[[:space:]]*(.*[^[:space:]])[[:space:]]*$/\1/p' | head -1
}

check() { # <base-ref> [changed-files-file]
	local base="$1" listfile="${2:-}" changed
	if [ -n "$listfile" ]; then
		changed=$(cat "$listfile")
	else
		changed=$(git diff --name-only "$base"...HEAD)
	fi
	if [ "${SKIP_DOCS_LABEL:-0}" = 1 ]; then
		local why
		why=$(skip_docs_reason "${PR_BODY:-}")
		if [ -n "$why" ]; then
			echo "skip-docs: $why"
			return 0
		fi
		{
			echo "This PR carries the 'skip-docs' label but its description gives no reason."
			echo "Add a line like this to the PR description and this guard re-runs:"
			echo
			echo "  Skip-docs: internal refactor, nothing an operator or author can see"
			echo
			echo "The release cut reads it back (scripts/docs-gap.sh)."
		} >&2
		return 1
	fi
	local surface
	surface=$(user_facing "$changed")
	if [ -z "$surface" ]; then
		echo "no user-facing surface changed; docs not required"
		return 0
	fi
	if touches_docs "$changed"; then
		echo "user-facing surface changed and ${DOCS_DIR}/ was updated"
		return 0
	fi
	{
		echo "This PR changes user-facing surface but updates no docs:"
		printf '%s\n' "$surface" | sed 's/^/  /'
		echo
		echo "Update ${DOCS_DIR}/ in THIS PR — a capability an operator cannot find"
		echo "in the docs did not really ship. If nothing here is user-discoverable,"
		echo "apply the 'skip-docs' label and add a 'Skip-docs: <reason>' line to the"
		echo "PR description; this guard re-runs on both."
	} >&2
	return 1
}

self_test() {
	local tmp rc; tmp=$(mktemp -d); trap 'rm -rf "$tmp"' RETURN

	printf 'helm/dexaflow/values.yaml\nwebsite/content/operate/x.md\n' > "$tmp/a"
	check X "$tmp/a" >/dev/null || { echo "self-test FAIL: surface+docs rejected" >&2; return 1; }

	printf 'internal/executor/kubernetes.go\ninternal/storage/repo.go\n' > "$tmp/b"
	check X "$tmp/b" >/dev/null || { echo "self-test FAIL: internal-only rejected" >&2; return 1; }

	# The real regression: a chart value with no docs.
	printf 'helm/dexaflow/values.yaml\n' > "$tmp/c"
	rc=0; check X "$tmp/c" >/dev/null 2>&1 || rc=$?
	[ "$rc" -ne 0 ] || { echo "self-test FAIL: chart value with no docs accepted" >&2; return 1; }

	# The authoring schema is surface too.
	printf 'docs/api/leoflow-yaml-schema.json\n' > "$tmp/d"
	rc=0; check X "$tmp/d" >/dev/null 2>&1 || rc=$?
	[ "$rc" -ne 0 ] || { echo "self-test FAIL: schema change with no docs accepted" >&2; return 1; }

	# A CLI command is surface; an internal helper in the same package is not.
	printf 'internal/cli/compile.go\n' > "$tmp/e"
	rc=0; check X "$tmp/e" >/dev/null 2>&1 || rc=$?
	[ "$rc" -ne 0 ] || { echo "self-test FAIL: cli change with no docs accepted" >&2; return 1; }

	# A docs-only PR must pass, not trip on itself.
	printf 'website/content/operate/x.md\n' > "$tmp/f"
	check X "$tmp/f" >/dev/null || { echo "self-test FAIL: docs-only PR rejected" >&2; return 1; }

	# Exercise the CLI ENTRYPOINT, not just check(). The bug this catches:
	# the entrypoint forwarded only $1, dropping the file list, so every real
	# invocation diffed <base>...HEAD (empty) and passed. check() was fine.
	rc=0; bash "$0" X "$tmp/c" >/dev/null 2>&1 || rc=$?
	[ "$rc" -ne 0 ] || { echo "self-test FAIL: entrypoint ignored the file list" >&2; return 1; }
	rc=0; bash "$0" X "$tmp/f" >/dev/null 2>&1 || rc=$?
	[ "$rc" -eq 0 ] || { echo "self-test FAIL: entrypoint rejected a docs-only PR" >&2; return 1; }

	# No arguments is how cut-release.sh's run_gates() invokes every check-*.sh.
	# Exiting non-zero here broke the cut; this locks the skip.
	rc=0; bash "$0" >/dev/null 2>&1 || rc=$?
	[ "$rc" -eq 0 ] || { echo "self-test FAIL: a no-arg invocation is not a skip — this breaks every release cut" >&2; return 1; }

	# A Go TEST file is not user-facing surface. This blocked a test-only PR.
	rm -f "$tmp"/*.yaml
	printf 'internal/cli/compile_baseimage_integration_test.go\n' >"$tmp/tst"
	bash "$0" X "$tmp/tst" >/dev/null 2>&1 || { echo "self-test FAIL: a test-only PR was told to write docs" >&2; return 1; }

	# The schemas directory is real surface; a `$`-anchored bare prefix matched nothing.
	printf 'internal/domain/schemas/leoflow-yaml-schema.json\n' >"$tmp/sch"
	rc=0; bash "$0" X "$tmp/sch" >/dev/null 2>&1 || rc=$?
	[ "$rc" -ne 0 ] || { echo "self-test FAIL: an authoring-schema change did not require docs" >&2; return 1; }

	# A server setting, a migration and the OpenAPI document are surface too.
	for p in internal/config/server.go migrations/041_x.up.sql internal/api/openapi.yaml; do
		printf '%s\n' "$p" >"$tmp/s"
		rc=0; check X "$tmp/s" >/dev/null 2>&1 || rc=$?
		[ "$rc" -ne 0 ] || { echo "self-test FAIL: $p did not require docs" >&2; return 1; }
	done
	# A down migration alone is not: it only undoes the up file's change.
	printf 'migrations/041_x.down.sql\n' >"$tmp/s"
	check X "$tmp/s" >/dev/null 2>&1 || { echo "self-test FAIL: a down migration required docs" >&2; return 1; }

	# A changelog fragment of a visible kind requires docs; a Fixed one does not.
	( cd "$tmp" && mkdir -p .changes/unreleased &&
		printf 'kind: Changed\nbody: x\n' >.changes/unreleased/a.yaml &&
		printf 'kind: Fixed\nbody: x\n' >.changes/unreleased/b.yaml )
	printf '.changes/unreleased/a.yaml\n' >"$tmp/fa"
	printf '.changes/unreleased/b.yaml\n' >"$tmp/fb"
	rc=0; ( cd "$tmp" && check X "$tmp/fa" ) >/dev/null 2>&1 || rc=$?
	[ "$rc" -ne 0 ] || { echo "self-test FAIL: a Changed fragment did not require docs" >&2; return 1; }
	( cd "$tmp" && check X "$tmp/fb" ) >/dev/null 2>&1 || { echo "self-test FAIL: a Fixed fragment required docs" >&2; return 1; }

	# The label needs a reason in the description.
	rc=0; SKIP_DOCS_LABEL=1 PR_BODY="" check X "$tmp/c" >/dev/null 2>&1 || rc=$?
	[ "$rc" -ne 0 ] || { echo "self-test FAIL: skip-docs without a reason accepted" >&2; return 1; }
	rc=0; SKIP_DOCS_LABEL=1 PR_BODY=$'Summary\r\nSkip-docs:   \r\n' check X "$tmp/c" >/dev/null 2>&1 || rc=$?
	[ "$rc" -ne 0 ] || { echo "self-test FAIL: an empty Skip-docs line accepted" >&2; return 1; }
	SKIP_DOCS_LABEL=1 PR_BODY=$'Summary\r\nskip-docs: internal only\r\n' check X "$tmp/c" >/dev/null 2>&1 ||
		{ echo "self-test FAIL: skip-docs with a reason rejected" >&2; return 1; }
	[ "$(skip_docs_reason $'a\n  Skip-docs: covered by #12  \n')" = "covered by #12" ] ||
		{ echo "self-test FAIL: skip_docs_reason did not read the reason" >&2; return 1; }

	echo "check-docs-updated self-test: ok"
}

# Sourced by scripts/docs-gap.sh for the definitions above: stop here.
if [ "${BASH_SOURCE[0]}" != "$0" ]; then return 0; fi
if [ "${1:-}" = "--self-test" ]; then self_test; exit $?; fi
# No arguments is a SKIP, not an error. cut-release.sh's run_gates() globs
# scripts/check-*.sh and runs each with NO arguments, treating any non-zero as
# "gate FAIL" and dying with "mechanical gates failed". Exiting 2 here would
# have broken every release cut -- the exact failure check-script-selftests.sh's
# header records for an earlier gate that "failed every rc cut it was globbed
# into". This gate needs a PR to have an opinion about; a cut has none.
if [ $# -eq 0 ]; then
	echo "gate skipped: needs a PR context (<base-ref> [changed-files-file])"
	exit 0
fi
# Forward BOTH arguments. This used to be `check "$1"`, which silently dropped
# the file list and diffed <base>...HEAD instead — empty in CI, so the gate
# passed everything. The self-test never caught it because it calls check()
# directly and bypasses this line, so the only broken path was the only one
# CI uses. A gate whose entrypoint is inert is worse than no gate.
check "$@"
