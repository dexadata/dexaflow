#!/usr/bin/env bash
# Fail a PR that changes shipped behavior without recording it in the CHANGELOG.
#
# Dexaflow keeps a hand-written Keep-a-Changelog `CHANGELOG.md`; entries are added
# manually under `## [Unreleased]` and `cut-release.sh` dates that section at a GA.
# Nothing forced an entry per PR, so features repeatedly merged with none and had
# to be back-filled by separate "docs(changelog): record …" PRs (#529, #532, #708,
# #739, #756, #780, #781, #888 — at least eight). Each wastes a cycle and risks a
# release shipping an incomplete changelog (v0.4.4's headline #881 nearly did).
#
# This gate passes when a PR's `[Unreleased]` section differs from the base
# branch's — i.e. the PR added/changed a changelog entry. Pure-mechanics PRs that
# legitimately have no user-facing change (release-prep, chore, dependabot,
# docs-only) carry the `skip-changelog` label, which the CI job honors before
# invoking this script.
#
# Usage:
#   scripts/check-changelog-entry.sh [base-ref]   # default: origin/main
#   scripts/check-changelog-entry.sh --self-test
set -euo pipefail

CHANGELOG="CHANGELOG.md"
# Where changie writes one file per change. A git pathspec, so it stays relative.
FRAGMENT_DIR=".changes/unreleased"

# unreleased_section reads a CHANGELOG on stdin and prints the body of its
# `## [Unreleased]` section (everything up to the next `## [` header). Kept as a
# pure filter so --self-test can exercise it without git.
unreleased_section() {
	awk '
		/^## \[Unreleased\]/ { inu = 1; next }
		inu && /^## \[/       { exit }
		inu                   { print }
	'
}

self_test() {
	local fail=0
	_eq() { # <got> <want> <name>
		if [ "$1" = "$2" ]; then printf '  ok   %s\n' "$3"; else printf '  FAIL %s\n    got:  %q\n    want: %q\n' "$3" "$1" "$2"; fail=1; fi
	}
	local doc
	doc=$'# Changelog\n\n## [Unreleased]\n\n### Added\n- a new thing (#1)\n\n## [1.0.0] - 2026-01-01\n\n### Fixed\n- old (#0)\n'
	_eq "$(printf '%s' "$doc" | unreleased_section | grep -c 'new thing')" "1" "extracts the Unreleased body"
	_eq "$(printf '%s' "$doc" | unreleased_section | grep -c 'old (#0)')" "0" "stops at the next dated section"
	# An empty Unreleased (just dated below, as right after a GA cut) yields blank.
	local dated
	dated=$'## [Unreleased]\n\n## [1.0.0] - 2026-01-01\n- x\n'
	_eq "$(printf '%s' "$dated" | unreleased_section | tr -d '[:space:]')" "" "empty Unreleased extracts blank"
	# The pure filter above was the only thing under test, which is why nothing
	# caught that this gate fails when invoked with no pull request to judge —
	# cut-release.sh's run_gates() globs scripts/check-*.sh and runs this one
	# bare from a release branch whose HEAD is origin/main, so the base and the
	# comparison were the same commit and every rc cut died on "mechanical gates
	# failed". These cases drive the script itself in a throwaway repository.
	#
	# Ambient git config is neutralised rather than hoped about: a global
	# commit.gpgsign with an unavailable gpg, or a core.hooksPath whose
	# pre-commit refuses, otherwise kills this script at `git commit` under
	# set -e with every byte of the reason inside a redirect. Asserts are on the
	# MESSAGE, not just the exit code, because exit 0 cannot distinguish "passed
	# because an entry was added" from "passed because it skipped".
	local script tmp
	script="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/check-changelog-entry.sh"
	tmp="$(mktemp -d)"
	trap 'rm -rf "${tmp:-}"' RETURN

	local setup_log
	setup_log="$(
		export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_SYSTEM=/dev/null
		cd "$tmp" || exit 1
		git -c init.defaultBranch=main init -q . &&
		git config user.email t@example.invalid && git config user.name tester &&
		mkdir -p scripts && cp "$script" scripts/ &&
		printf '%s\n' '# Changelog' '' '## [Unreleased]' '' '### Fixed' '- a thing (#1)' '' '## [1.0.0] - 2026-01-01' '- old' > CHANGELOG.md &&
		git add -A && git commit -qm base &&
		git update-ref refs/remotes/origin/main refs/heads/main
	2>&1)" || { printf '  FAIL self-test setup could not build the fixture repo\n%s\n' "$setup_log"; return 1; }

	# _case <name> <want-rc> <want-substring> -- <commands run inside $tmp>
	# GITHUB_BASE_REF is part of the gate's decision, and it is SET in the
	# environment this suite runs in — every CI job for a pull_request has it.
	# Cases that simulate a local or cut invocation must therefore unset it, or
	# they inherit "a pull request is in play" from their own runner and the
	# skip never fires. Case 6 sets it back deliberately.
	_case() {
		local name="$1" want_rc="$2" want_msg="$3"; shift 4
		local out rc=0
		out="$(
			export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_SYSTEM=/dev/null
			unset GITHUB_BASE_REF
			cd "$tmp" || exit 1
			"$@" >/dev/null 2>&1
			bash scripts/check-changelog-entry.sh 2>&1
		)" || rc=$?
		_eq "$rc" "$want_rc" "$name (exit)"
		case "$out" in
			*"$want_msg"*) printf '  ok   %s (message)\n' "$name" ;;
			*) printf '  FAIL %s (message)\n    got:  %q\n    want to contain: %q\n' "$name" "$out" "$want_msg"; fail=1 ;;
		esac
	}

	# 1. Bare on the base branch: how run_gates invokes it. Must skip.
	_case "skips on the base branch" 0 "gate skipped" -- true
	# 2. The shape the cut ACTUALLY presents: a release branch off origin/main
	#    with an uncommitted CHANGELOG rewrite (bump_chart/date_the_changelog run
	#    before run_gates, and the commit comes after). Must still skip. If
	#    anyone moves run_gates after the commit, this is what goes red.
	_case "skips with the cut's dirty worktree" 0 "gate skipped" -- \
		bash -c 'git checkout -qb release/v9.9.9 main && printf "%s\n" "# Changelog" "" "## [Unreleased]" "" "## [9.9.9] - 2026-09-07" "- dated" > CHANGELOG.md'
	# 3. A real branch that changes something else: must still FAIL.
	_case "still fails a branch that adds no entry" 1 "records no changelog entry" -- \
		bash -c 'git checkout -q main && git checkout -qb feature && git checkout -- CHANGELOG.md && echo x > other.txt && git add -A && git commit -qm other'
	# 4. And pass, by comparison rather than by skipping, when it adds one.
	_case "passes a branch that adds an entry" 0 "updated relative to" -- \
		bash -c 'printf "%s\n" "# Changelog" "" "## [Unreleased]" "" "### Fixed" "- a thing (#1)" "- another (#2)" "" "## [1.0.0] - 2026-01-01" "- old" > CHANGELOG.md && git add -A && git commit -qm entry'
	# 4b. A fragment under .changes/unreleased/, and nothing else, passes. This
	#     is the shape every PR is meant to have after #1200.
	_case "passes a branch that only adds a fragment" 0 "fragment(s) added under" -- \
		bash -c 'git checkout -q main && git checkout -qb frag && mkdir -p .changes/unreleased && printf "%s\n" "kind: Fixed" "body: a thing" > .changes/unreleased/x.yaml && git add -A && git commit -qm frag'
	# 4c. An uncommitted fragment counts too, so running this locally before the
	#     commit gives the same verdict CI will.
	_case "passes on an uncommitted fragment" 0 "fragment(s) added under" -- \
		bash -c 'git checkout -q main && git checkout -qb fragdirty && echo x > other2.txt && git add other2.txt && git commit -qm other && mkdir -p .changes/unreleased && printf "%s\n" "kind: Fixed" "body: b thing" > .changes/unreleased/y.yaml'
	# 4d. Fragments the BASE already carries must not approve a PR that adds
	#     none of its own. Without this, the first merged fragment would
	#     rubber-stamp every PR that followed it, forever.
	_case "fails when the only fragments came from the base" 1 "records no changelog entry" -- \
		bash -c 'git checkout -q main && git checkout -- . && git clean -qfd &&
			mkdir -p .changes/unreleased && printf "%s\n" "kind: Fixed" "body: merged earlier" > .changes/unreleased/z.yaml &&
			git add -A && git commit -qm "fragment on base" &&
			git update-ref refs/remotes/origin/main refs/heads/main &&
			git checkout -qb after-frag && echo x > other3.txt && git add -A && git commit -qm other'
	# 5. A base ref that does not resolve must FAIL, never approve. Before this
	#    it printed OK: git show of a missing ref yields an empty base section,
	#    which differs from a non-empty head section.
	_case "fails closed when the base ref is missing" 1 "not present" -- \
		bash -c 'git update-ref -d refs/remotes/origin/main'
	# 6. The pull_request_target rubber-stamp. That event checks out the BASE
	#    branch, so HEAD is the base tip and ancestry alone would skip forever.
	#    With a pull request in play the gate must judge, never skip.
	local out6 rc6=0
	out6="$(
		export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_SYSTEM=/dev/null GITHUB_BASE_REF=main
		cd "$tmp" || exit 1
		git checkout -q main && git update-ref refs/remotes/origin/main refs/heads/main
		bash scripts/check-changelog-entry.sh 2>&1
	)" || rc6=$?
	_eq "$rc6" "1" "does not skip on the base branch when a pull request is in play (exit)"
	case "$out6" in
		*"records no changelog entry"*) echo "  ok   does not skip on the base branch when a pull request is in play (message)" ;;
		*) printf '  FAIL pull_request_target shape was rubber-stamped\n    got: %q\n' "$out6"; fail=1 ;;
	esac

	if [ "$fail" -eq 0 ]; then echo "self-test: PASS"; return 0; else echo "self-test: FAIL"; return 1; fi
}

[ "${1:-}" = "--self-test" ] && { self_test; exit $?; }

cd "$(dirname "$0")/.."
base="${1:-origin/main}"

if [ ! -f "$CHANGELOG" ]; then
	echo "FAIL: $CHANGELOG not found"; exit 1
fi

# A base ref that does not resolve must never let a PR through. `git show` of a
# missing ref yields an empty base section, which differs from any non-empty head
# section, so this gate used to print OK on a PR that added nothing — fail-open,
# which in a gate is worse than the cut being blocked. Reachable the moment a
# workflow loses its fetch step or checks out shallow.
git rev-parse --verify --quiet "$base" >/dev/null 2>&1 ||
	{ echo "FAIL: base ref '${base}' is not present — fetch it before running this gate (shallow clone?)"; exit 1; }

# With no pull request to judge, this gate has no question to ask.
# cut-release.sh's run_gates() globs scripts/check-*.sh and runs each one bare
# from a release branch created at origin/main, so the base's Unreleased section
# was compared against its own: always equal, always FAIL, and every rc cut died
# on "mechanical gates failed".
#
# Two conditions, both required. GITHUB_BASE_REF is set on pull_request and
# pull_request_target and unset locally and during a cut, so it is literally
# "there is no pull request"; without it, a workflow using pull_request_target
# without an explicit ref checks out the BASE branch, HEAD becomes the base tip,
# and this gate would rubber-stamp every PR forever. HEAD being an ancestor of
# the base means the content is already contained in the base, so there is
# nothing this gate could judge either way. Ancestry only ever under-reports
# reachability on truncated history, so a shallow clone makes it fail closed —
# the gate runs.
if [ -z "${GITHUB_BASE_REF:-}" ] &&
	git merge-base --is-ancestor HEAD "$base" >/dev/null 2>&1; then
	echo "OK: HEAD is contained in ${base} and no pull request is in play — gate skipped."
	exit 0
fi

# A fragment under .changes/unreleased/ satisfies this gate, and is the way to
# satisfy it going forward (#1200).
#
# The CHANGELOG path below is kept, not replaced, for two reasons. A release in
# flight may still carry a hand-written entry, and refusing it would fail PRs for
# a reason unrelated to their content. And a contributor who edits CHANGELOG.md
# by hand has still recorded the change, which is what the gate is actually for;
# conflicts are a cost of that, not a correctness problem.
#
# "New" is the worktree's set of fragments minus the base's, not `ls | wc -l`:
# once the first fragment lands, a non-empty directory is true for every PR
# after it and the gate would approve them all. It is also not a `git diff` of
# committed files, because the CHANGELOG path below reads the worktree, and a
# contributor running this locally before committing should get the same answer
# either way they record the change.
fragments_now="$( { git ls-files -- "$FRAGMENT_DIR"
	git ls-files --others --exclude-standard -- "$FRAGMENT_DIR"; } | sort -u)"
fragments_base="$(git ls-tree -r --name-only "$base" -- "$FRAGMENT_DIR" 2>/dev/null | sort)"
new_fragments="$(comm -23 \
	<(printf '%s\n' "$fragments_now" | awk 'NF') \
	<(printf '%s\n' "$fragments_base" | awk 'NF') | awk 'NF' | wc -l | tr -d ' ')"
if [ "${new_fragments:-0}" -gt 0 ]; then
	echo "OK: ${new_fragments} changelog fragment(s) added under ${FRAGMENT_DIR}."
	exit 0
fi

base_section="$(git show "${base}:${CHANGELOG}" 2>/dev/null | unreleased_section || true)"
head_section="$(unreleased_section <"$CHANGELOG")"

if [ "$base_section" = "$head_section" ]; then
	echo "FAIL: this PR records no changelog entry."
	echo
	echo "Preferred, and conflict-free because no two PRs touch the same file:"
	echo
	echo "    changie new"
	echo
	echo "which writes a fragment under ${FRAGMENT_DIR}/ that the release cut"
	echo "assembles into CHANGELOG.md. Editing CHANGELOG.md by hand still passes."
	echo
	echo "If the PR has no user-facing change (release-prep, chore, dependabot,"
	echo "docs-only), apply the 'skip-changelog' label to the PR instead."
	exit 1
fi

echo "OK: CHANGELOG [Unreleased] updated relative to ${base}."
