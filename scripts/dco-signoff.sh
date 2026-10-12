#!/usr/bin/env bash
# Checks that every commit of a pull request carries a Developer Certificate of
# Origin sign-off from its author (#1575).
#
# Usage: scripts/dco-signoff.sh <base> <head>
#        scripts/dco-signoff.sh --self-test
#
# <base> is the CURRENT tip of the pull request's base branch (dco.yaml fetches
# it), not the base sha in the event payload: a pull request that merged `main`
# in carries main's own commits, which are only excluded when <base> already
# contains them.
#
# Every commit in <base>..<head> must have a `Signed-off-by:` trailer whose
# email is the commit author's email (compared without regard to case), which
# is what `git commit -s` writes. Two kinds of commit are exempt:
#
# - merge commits, such as the ones the "Update branch" button creates, which
#   carry no contribution of their own;
# - commits authored by a GitHub App bot (`dependabot[bot]` and the like),
#   which cannot certify anything.
#
# Why it exists: the provenance of every contribution matters, and asking for a sign-off after
# outside contributions have landed would mean asking people to rewrite their
# history. The check runs on the commits of a pull request (dco.yaml), never on
# `main`: a squash merge writes a new commit there, and that commit is the
# maintainer's, made from commits this check already passed.
#
# It is not named check-*.sh on purpose. cut-release.sh runs every
# scripts/check-*.sh with no arguments before a cut, and this check has no
# meaning without the range of a pull request.
set -euo pipefail

# lower <text>: prints <text> in lower case.
lower() {
	printf '%s' "$1" | tr '[:upper:]' '[:lower:]'
}

# is_bot <author email>: succeeds for a GitHub App bot author. GitHub writes
# the App's noreply address (`<id>+<slug>[bot]@users.noreply.github.com`) on
# the commits the App makes. The author name is not checked: anyone can call
# themselves `something[bot]`.
is_bot() {
	case "$1" in *'[bot]@users.noreply.github.com') return 0 ;; esac
	return 1
}

# has_signoff <author email> <commit message>: succeeds when the trailer block
# of the message has a Signed-off-by whose email is <author email>. Only the
# trailer block counts (git interpret-trailers --parse), the same block
# `git commit -s` appends to, so a sign-off quoted in the body does not.
has_signoff() {
	local want line email
	want="$(lower "$1")"
	while IFS= read -r line; do
		case "$(lower "$line")" in signed-off-by:*) ;; *) continue ;; esac
		case "$line" in *'<'*'>'*) ;; *) continue ;; esac
		email="${line#*<}"
		email="${email%%>*}"
		[ "$(lower "$email")" = "$want" ] && return 0
	done < <(printf '%s\n' "$2" | git interpret-trailers --parse)
	return 1
}

# check_range <base> <head>: checks every non-merge commit in <base>..<head>,
# printing one line per commit, and fails when any of them is not signed off.
check_range() {
	local shas sha short an ae msg checked=0 failed=0
	# Checked explicitly: a range git cannot resolve must fail, never pass as an
	# empty range, and set -e does not apply when the caller tests the result.
	shas="$(git rev-list --no-merges --reverse "$1..$2")" || {
		echo "::error::cannot list the commits of $1..$2"
		return 2
	}
	for sha in $shas; do
		short="$(git rev-parse --short "$sha")"
		an="$(git log -1 --format=%an "$sha")"
		ae="$(git log -1 --format=%ae "$sha")"
		msg="$(git log -1 --format=%B "$sha")"
		if is_bot "$ae"; then
			echo "skip: $short authored by $an"
			continue
		fi
		checked=$((checked + 1))
		if has_signoff "$ae" "$msg"; then
			echo "ok:   $short signed off by $ae"
		else
			echo "::error::commit $short has no 'Signed-off-by: $an <$ae>' trailer"
			failed=$((failed + 1))
		fi
	done
	if [ "$failed" -gt 0 ]; then
		cat <<-EOF
			$failed of $checked commit(s) are not signed off by their author.
			Sign off every commit of the branch and force-push it:
			  git rebase --signoff <base-branch>
			For the last commit alone: git commit --amend --no-edit -s
			See CONTRIBUTING.md, "Sign off your commits".
		EOF
		return 1
	fi
	echo "dco-signoff: $checked commit(s) signed off"
}

_eq() { # <got> <want> <label>
	if [ "$1" != "$2" ]; then
		printf 'FAIL: %s\n  got:  %s\n  want: %s\n' "$3" "$1" "$2" >&2
		return 1
	fi
}

_ok() { # <label> <command...>: the command must succeed.
	local label="$1"
	shift
	"$@" || { echo "FAIL: $label (expected success)" >&2; return 1; }
}

_no() { # <label> <command...>: the command must fail.
	local label="$1"
	shift
	if "$@"; then
		echo "FAIL: $label (expected failure)" >&2
		return 1
	fi
}

self_test() {
	local out rc
	_ok "dependabot" is_bot "49699333+dependabot[bot]@users.noreply.github.com"
	_ok "another App" is_bot "1+some-app[bot]@users.noreply.github.com"
	_no "a person is not a bot" is_bot "ada@example.com"
	_no "a person's noreply address" is_bot "1+ada@users.noreply.github.com"
	_no "a bot-looking address elsewhere" is_bot "x[bot]@example.com"

	_ok "plain sign-off" has_signoff "ada@example.com" $'fix: x\n\nSigned-off-by: Ada Lovelace <ada@example.com>'
	_ok "email case ignored" has_signoff "Ada@Example.com" $'fix: x\n\nsigned-off-by: Ada <ada@EXAMPLE.com>'
	_ok "among other trailers" has_signoff "ada@example.com" $'fix: x\n\nbody\n\nRefs: #1\nSigned-off-by: Ada <ada@example.com>'
	_no "no trailer" has_signoff "ada@example.com" $'fix: x\n\nbody'
	_no "someone else signed off" has_signoff "ada@example.com" $'fix: x\n\nSigned-off-by: Bob <bob@example.com>'
	_no "sign-off quoted in the body" has_signoff "ada@example.com" $'fix: x\n\nSigned-off-by: Ada <ada@example.com>\nwas missing, so here it is.\n\nRefs: #1'
	_no "an email that only contains the author's" has_signoff "ada@example.com" $'fix: x\n\nSigned-off-by: Ada <xada@example.com>'
	_no "no email at all" has_signoff "ada@example.com" $'fix: x\n\nSigned-off-by: ada@example.com'

	# The wiring: a real repository, the range a pull request would give.
	_selftest_tmp="$(mktemp -d)"
	# Global, not local: the EXIT trap runs after this function has returned.
	trap 'rm -rf "$_selftest_tmp"' EXIT
	(
		export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1
		export GIT_COMMITTER_NAME=Ada GIT_COMMITTER_EMAIL=ada@example.com
		export GIT_AUTHOR_NAME=Ada GIT_AUTHOR_EMAIL=ada@example.com
		cd "$_selftest_tmp"
		_commit() { # <author name> <author email> <message>
			GIT_AUTHOR_NAME="$1" GIT_AUTHOR_EMAIL="$2" git -c commit.gpgsign=false commit -q --allow-empty -m "$3"
		}
		git init -q -b main .
		_commit Ada ada@example.com "chore: base"
		git switch -q -c feature
		_commit Ada ada@example.com $'feat: signed\n\nSigned-off-by: Ada <Ada@example.com>'
		_commit "dependabot[bot]" "49699333+dependabot[bot]@users.noreply.github.com" "build(deps): bump x"
		git switch -q main
		_commit Bob bob@example.com "fix: on main after the branch point, never checked"
		git switch -q feature
		git -c commit.gpgsign=false merge -q --no-ff -m "Merge branch 'main' into feature" main
		out="$(check_range main HEAD 2>&1)" && rc=0 || rc=$?
		_eq "$rc" 0 "a signed range with a bot commit and a merge passes, output: $out"
		_eq "$(printf '%s\n' "$out" | grep -c '^ok:')" 1 "only the signed commit is checked"
		_eq "$(printf '%s\n' "$out" | grep -c '^skip:')" 1 "the bot commit is skipped"
		case "$out" in *bob@*) _eq "$out" "" "a commit of main after the branch point is outside the range" ;; esac

		_commit Ada ada@example.com $'feat: unsigned\n\nbody'
		_commit Ada ada@example.com $'feat: signed by someone else\n\nSigned-off-by: Bob <bob@example.com>'
		_commit "ada[bot]" ada@example.com "feat: a bot name on a person's address"
		out="$(check_range main HEAD 2>&1)" && rc=0 || rc=$?
		_eq "$rc" 1 "an unsigned commit fails the range"
		_eq "$(printf '%s\n' "$out" | grep -c '^::error::')" 3 "each unsigned commit is reported, output: $out"
		_eq "$(printf '%s\n' "$out" | grep -c '^3 of 4 commit(s) are not signed off')" 1 "the summary counts checked commits"

		_no "an unknown ref fails instead of checking nothing" check_range main no-such-ref >/dev/null 2>&1
	)
	echo "dco-signoff self-test: ok"
}

main() {
	case "${1:-}" in
	--self-test) self_test ;;
	*)
		if [ "$#" -ne 2 ]; then
			echo "usage: $0 <base> <head> | --self-test" >&2
			exit 2
		fi
		check_range "$1" "$2"
		;;
	esac
}

main "$@"
