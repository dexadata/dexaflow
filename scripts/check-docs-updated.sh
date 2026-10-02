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
# Escape hatch: the `skip-docs` label, for PRs that genuinely change nothing a
# user could discover — release prep, dependabot, pure internals. Same shape as
# skip-changelog, and the workflow re-runs on label so it takes effect at once.
#
# Usage: scripts/check-docs-updated.sh <base-ref>   e.g. origin/main
#        scripts/check-docs-updated.sh --self-test
set -euo pipefail

DOCS_DIR="website/content"
# Paths whose change implies something an operator or author can see.
SURFACE_RE='^(helm/dexaflow/values\.yaml|docs/api/leoflow-yaml-schema\.json|internal/domain/schemas/.+|internal/cli/[a-z0-9_]+\.go)$'
# A Go TEST file is not user-facing surface. `[a-z_]+\.go` matched
# compile_baseimage_integration_test.go, so this gate blocked a test-only PR and
# sent its author looking for a `skip-docs` label that did not exist. Digits are
# allowed in the name now too; `internal/domain/schemas/` gained `.+` because the
# alternation is `$`-anchored and a bare directory prefix matched nothing.
NOT_SURFACE_RE='(_test\.go|/testdata/)$'

check() { # <base-ref> [changed-files-file]
	local base="$1" listfile="${2:-}" changed
	if [ -n "$listfile" ]; then
		changed=$(cat "$listfile")
	else
		changed=$(git diff --name-only "$base"...HEAD)
	fi
	local surface docs
	surface=$(printf '%s\n' "$changed" | grep -E "$SURFACE_RE" | grep -vE "$NOT_SURFACE_RE" || true)
	docs=$(printf '%s\n' "$changed" | grep -E "^${DOCS_DIR}/" || true)
	if [ -z "$surface" ]; then
		echo "no user-facing surface changed; docs not required"
		return 0
	fi
	if [ -n "$docs" ]; then
		echo "user-facing surface changed and ${DOCS_DIR}/ was updated"
		return 0
	fi
	{
		echo "This PR changes user-facing surface but updates no docs:"
		printf '  %s\n' $surface
		echo
		echo "Update ${DOCS_DIR}/ in THIS PR — a capability an operator cannot find"
		echo "in the docs did not really ship. If nothing here is user-discoverable,"
		echo "apply the 'skip-docs' label and this guard re-runs."
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

	echo "check-docs-updated self-test: ok"
}

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
