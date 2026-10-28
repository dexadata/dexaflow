#!/usr/bin/env bash
# changelog-sync.sh: bring a patch release's changelog back to main.
#
#   scripts/changelog-sync.sh 0.5.4            # edit this checkout (CHANGELOG.md + .changes/unreleased)
#   scripts/changelog-sync.sh 0.5.4 --check    # would syncing change origin/main? exit 1 if so
#   scripts/changelog-sync.sh 0.5.4 --pr       # sync origin/main in a scratch worktree and open the PR
#   scripts/changelog-sync.sh --check-all      # --check for the newest patch tag of every minor
#   scripts/changelog-sync.sh --self-test      # fixture repository, no network
#
# Add --fetch to refresh origin/main and the tags first.
#
# Patches are cut from release-X.Y (ADR 0062). Their changes reach that branch
# as cherry-picks that carry the changie fragment, and the cut folds the
# fragments into CHANGELOG.md there and deletes them, then dates the section.
# Nothing brought that back to main: by v0.5.3, main's CHANGELOG went from
# [Unreleased] straight to [0.5.0], and 97 of main's 101 pending fragments had
# already shipped in 0.5.1 to 0.5.3. The 0.6.0 cut folds main's fragments, so
# it would have published all 97 a second time as new.
#
# What a sync does, against the tag vX.Y.Z (the changelog is true once the tag
# exists, whatever the release workflows do afterwards):
#
#   1. Every dated GA section of minor X.Y up to X.Y.Z that the tag's
#      CHANGELOG has and the target lacks is copied verbatim, in version order,
#      so syncing 0.5.3 also repairs a skipped 0.5.1 and 0.5.2.
#   2. A fragment in the target's .changes/unreleased is deleted when the fold
#      on the release line consumed it (a file of that name was deleted in
#      vX.Y.0..vX.Y.Z) AND its text is in one of those released sections.
#      A fragment whose text is in a released section under another name (it
#      was renamed on one side) is deleted too, and reported as such.
#   3. A fragment consumed by name whose text is NOT in the released sections
#      is kept and reported, and the exit status is 1: deleting it could lose
#      an entry, keeping it could publish one twice, and only a person can tell.
#
# Re-running is safe: a section already present is never touched and a deleted
# fragment is gone, so a second run changes nothing.
#
# Exit status: 0 when the target is (or now is) in sync, 1 when --check found
# drift or a fragment needs a person, 2 on a usage or git error.
#
# cut-release.sh runs `--pr` after it pushes a patch GA tag, and `--check-all`
# before every cut. check-changelog-structure.sh sources the helpers below.
set -uo pipefail
export LC_ALL=C

CHANGELOG_SYNC_SLUG="${CHANGELOG_SYNC_SLUG:-dexadata/dexaflow}"
# The repository the tags and history are read from. cut-release.sh runs a copy
# of this script taken from origin/main (the checkout may be on a release
# branch that predates it), so the path of the script is not always the repo.
CHANGELOG_SYNC_REPO="${CHANGELOG_SYNC_REPO:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
FRAGMENT_SUBDIR=".changes/unreleased"

cs_log()  { printf 'changelog-sync: %s\n' "$*" >&2; }
cs_die()  { printf 'changelog-sync: %s\n' "$*" >&2; exit 2; }

# ---- helpers (also sourced by check-changelog-structure.sh) ------------------

# semver_key <X.Y.Z[-pre.N]>: a string that sorts like the version under
# LC_ALL=C. A GA sorts after every prerelease of the same X.Y.Z ("~" is above
# every letter), and the numbers are padded so 0.5.10 sorts after 0.5.9.
semver_key() {
	local v="${1#v}" core pre="" a b c lab n
	core="${v%%-*}"
	[ "$core" != "$v" ] && pre="${v#*-}"
	IFS=. read -r a b c <<<"$core"
	if [ -z "$pre" ]; then
		printf '%08d%08d%08d~' "$((10#$a))" "$((10#$b))" "$((10#$c))"
	else
		lab="${pre%%.*}"; n="${pre##*.}"
		[[ "$n" =~ ^[0-9]+$ ]] || n=0
		printf '%08d%08d%08d%s%08d' "$((10#$a))" "$((10#$b))" "$((10#$c))" "$lab" "$((10#$n))"
	fi
}

# version_lt <a> <b>: true when version a is older than version b.
version_lt() { [[ "$(semver_key "$1")" < "$(semver_key "$2")" ]]; }

# dated_versions <changelog>: the version of every `## [..]` heading except
# [Unreleased], in file order.
dated_versions() {
	sed -nE 's/^## \[([0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.]+)?)\].*/\1/p' "$1"
}

# section_of <changelog> <version>: the section, heading included, up to the
# next `## ` heading or the link reference block at the bottom. Trailing blank
# lines are dropped. Matched on `## [<version>]` exactly, so 0.5.1 never
# matches 0.5.10.
section_of() {
	awk -v h="## [$2]" '
		!p && index($0, h) == 1 && (length($0) == length(h) || substr($0, length(h) + 1, 1) == " ") { p = 1; print; next }
		p && (/^## / || /^\[[^]]+\]: /) { exit }
		p { print }
	' "$1" | awk '{ l[NR] = $0 } NF { last = NR } END { for (i = 1; i <= last; i++) print l[i] }'
}

# normalize: every run of whitespace becomes one space, so text compares the
# same whether it was wrapped in a YAML block or indented by the fold.
normalize() { tr -s '[:space:]' ' ' | sed -e 's/^ //' -e 's/ $//'; }

# fragment_text <file>: the normalized body of a changie fragment. Handles the
# shapes changie writes (a `|-` block, a single-quoted string with '' escapes,
# a double-quoted or a plain scalar) without a YAML library, so it runs on a
# bare runner. Empty when there is no body.
fragment_text() {
	local raw first rest
	raw="$(awk '
		/^body:/ { inb = 1; v = $0; sub(/^body:[ \t]*/, "", v); print v; next }
		inb && /^[^ \t]/ { inb = 0 }
		inb { print }
	' "$1")"
	first="${raw%%$'\n'*}"
	rest=""
	[ "$first" != "$raw" ] && rest="${raw#*$'\n'}"
	case "$first" in
		'|'* | '>'*) printf '%s' "$rest" | normalize ;;
		"'"*)
			printf '%s\n%s' "$first" "$rest" | normalize |
				sed -e "s/^'//" -e "s/'\$//" -e "s/''/'/g"
			;;
		'"'*)
			printf '%s\n%s' "$first" "$rest" | normalize |
				sed -e 's/^"//' -e 's/"$//' -e 's/\\"/"/g' -e 's/\\\\/\\/g'
			;;
		*) printf '%s\n%s' "$first" "$rest" | normalize ;;
	esac
}

# released_text <changelog>: every dated section, normalized to one line and
# padded with a space at each end, for entry_in.
released_text() {
	printf ' %s ' "$(awk '/^## \[/ && !/^## \[Unreleased\]/ { d = 1 } d' "$1" | normalize)"
}

# entry_in <padded-text> <fragment-text>: true when some entry starts with the
# fragment's text and ends where it ends. The fold renders a fragment as
# `- <body>`, so the text is searched as " - <body> ": a short body can match
# neither the middle of another entry nor a longer entry that starts with it
# mid-word.
entry_in() {
	[ -n "$2" ] || return 1
	printf '%s' "$1" | grep -qF -- " - $2 "
}

# ---- the sync -----------------------------------------------------------------

# insert_section <changelog> <section-file> <version>: put the section above
# the first dated section older than <version>, or at the end of the sections
# (before the link reference block) when every one is newer.
insert_section() {
	local cl="$1" sf="$2" ver="$3" v anchor="" tmp
	while IFS= read -r v; do
		if version_lt "$v" "$ver"; then
			anchor="$(grep -nF -m1 -- "## [$v]" "$cl" | cut -d: -f1)"
			break
		fi
	done < <(dated_versions "$cl")
	if [ -z "$anchor" ]; then
		# After the last section: before the first link reference that follows it.
		anchor="$(awk '/^## / { h = NR } /^\[[^]]+\]: / && h && !a { a = NR } END { print a }' "$cl")"
	fi
	tmp="$(mktemp)"
	if [ -n "$anchor" ]; then
		awk -v at="$anchor" -v sf="$sf" '
			NR == at { while ((getline l < sf) > 0) print l; print "" }
			{ print }
		' "$cl" >"$tmp"
	else
		{ cat "$cl"; [ -n "$(tail -c1 "$cl")" ] && echo; echo; cat "$sf"; } >"$tmp"
	fi
	mv "$tmp" "$cl"
}

# sync_tree <dir> <version>: apply the sync to the checkout at <dir>, reading
# the tag and history from CHANGELOG_SYNC_REPO. Prints what it did on stderr.
# Returns 1 when a fragment needs a person, 2 on a git error.
sync_tree() {
	local dir="$1" ver="${2#v}" repo="$CHANGELOG_SYNC_REPO" minor tagcl v inserted=0 rc=0
	[[ "$ver" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || { cs_log "not a GA version: $ver"; return 2; }
	minor="${ver%.*}"
	[ -f "$dir/CHANGELOG.md" ] || { cs_log "no CHANGELOG.md in $dir"; return 2; }
	git -C "$repo" rev-parse -q --verify "refs/tags/v$ver^{commit}" >/dev/null ||
		{ cs_log "tag v$ver not found (the sync runs once the tag exists; git fetch --tags)"; return 2; }
	git -C "$repo" rev-parse -q --verify "refs/tags/v$minor.0^{commit}" >/dev/null ||
		{ cs_log "tag v$minor.0 not found"; return 2; }

	local work; work="$(mktemp -d)"
	tagcl="$work/tag.md"
	git -C "$repo" show "v$ver:CHANGELOG.md" >"$tagcl" 2>/dev/null || { cs_log "v$ver has no CHANGELOG.md"; rm -rf "$work"; return 2; }

	# 1. Sections. Oldest first, so each insert sees the previous one; the
	#    position comes from the version, not from the order of insertion.
	local -a want=()
	while IFS= read -r v; do
		[[ "$v" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || continue
		[ "${v%.*}" = "$minor" ] || continue
		version_lt "$ver" "$v" && continue
		want+=("$v")
	done < <(dated_versions "$tagcl")
	: >"$work/released.md"
	local i
	for ((i = ${#want[@]} - 1; i >= 0; i--)); do
		v="${want[$i]}"
		section_of "$tagcl" "$v" >"$work/s.md"
		{ cat "$work/s.md"; echo; } >>"$work/released.md"
		if dated_versions "$dir/CHANGELOG.md" | grep -qxF -- "$v"; then continue; fi
		insert_section "$dir/CHANGELOG.md" "$work/s.md" "$v"
		cs_log "inserted [$v] from v$ver"
		inserted=$((inserted + 1))
	done

	# 2. Fragments.
	local consumed released f name text removed=0 by_content=0 kept=0
	consumed="$(git -C "$repo" log --diff-filter=D --name-only --format= "v$minor.0..v$ver" -- "$FRAGMENT_SUBDIR" 2>/dev/null |
		sed 's|.*/||' | sort -u)"
	released="$(released_text "$work/released.md")"
	shopt -s nullglob
	for f in "$dir/$FRAGMENT_SUBDIR"/*.yaml "$dir/$FRAGMENT_SUBDIR"/*.yml; do
		name="$(basename "$f")"
		text="$(fragment_text "$f")"
		if printf '%s\n' "$consumed" | grep -qxF -- "$name"; then
			if entry_in "$released" "$text"; then
				rm -f "$f"; removed=$((removed + 1))
			else
				cs_log "KEPT $name: v$minor.0..v$ver consumed a fragment of this name, but its text is in none of the released sections; check it by hand"
				kept=$((kept + 1)); rc=1
			fi
		elif entry_in "$released" "$text"; then
			rm -f "$f"; removed=$((removed + 1)); by_content=$((by_content + 1))
			cs_log "removed $name: its text shipped in v$minor.x under another fragment name"
		fi
	done
	shopt -u nullglob
	rm -rf "$work"
	cs_log "v$ver: $inserted section(s) inserted, $removed fragment(s) removed ($by_content by content), $kept kept for review"
	return "$rc"
}

# materialize <ref> <dir>: CHANGELOG.md and the pending fragments of <ref>,
# written to <dir>, so --check can run the sync on a copy of origin/main
# without a worktree and without touching the operator's checkout.
materialize() {
	local ref="$1" dir="$2" repo="$CHANGELOG_SYNC_REPO" p
	mkdir -p "$dir/$FRAGMENT_SUBDIR"
	git -C "$repo" show "$ref:CHANGELOG.md" >"$dir/CHANGELOG.md" 2>/dev/null || return 2
	while IFS= read -r p; do
		[ -n "$p" ] || continue
		git -C "$repo" show "$ref:$p" >"$dir/$p" 2>/dev/null || return 2
	done < <(git -C "$repo" ls-tree --name-only "$ref" "$FRAGMENT_SUBDIR/" 2>/dev/null)
}

# check_one <version> <ref>: 0 when <ref> is in sync with v<version>, 1 when
# the sync would change it (the change is printed), 2 when it cannot tell.
check_one() {
	local ver="${1#v}" ref="$2" t rc=0
	t="$(mktemp -d)"
	materialize "$ref" "$t/before" || { rm -rf "$t"; cs_log "cannot read $ref"; return 2; }
	cp -R "$t/before" "$t/after"
	sync_tree "$t/after" "$ver" 2>"$t/log" || rc=$?
	if [ "$rc" = 2 ]; then cat "$t/log" >&2; rm -rf "$t"; return 2; fi
	if ! diff -rq "$t/before" "$t/after" >/dev/null 2>&1 || [ "$rc" != 0 ]; then
		printf '%s is not in sync with v%s:\n' "$ref" "$ver"
		sed 's/^changelog-sync: /  /' "$t/log"
		(cd "$t" && diff -rq before after 2>/dev/null | sed -e 's|^Only in before/\.changes/unreleased: |  pending fragment already shipped: |' -e 's|^Files before/CHANGELOG.md and after/CHANGELOG.md differ|  CHANGELOG.md lacks released section(s)|')
		rm -rf "$t"; return 1
	fi
	rm -rf "$t"
	return 0
}

# newest_patch_tags: the newest GA tag of every minor that has a patch
# (X.Y.Z with Z > 0) and an X.Y.0 tag to count from, one per line, oldest
# minor first. v0.0.1 and v0.0.2 predate this changelog and have no v0.0.0.
newest_patch_tags() {
	git -C "$CHANGELOG_SYNC_REPO" tag -l 'v[0-9]*' | sed 's/^v//' | grep -E '^[0-9]+\.[0-9]+\.[0-9]+$' |
		awk -F. '{ k = $1 "." $2; if ($3 == "0") z[k] = 1; if (!(k in m) || $3 + 0 > m[k] + 0) m[k] = $3 }
			END { for (k in m) if (m[k] + 0 > 0 && (k in z)) print k "." m[k] }' |
		while IFS= read -r v; do printf '%s %s\n' "$(semver_key "$v")" "$v"; done | sort | cut -d' ' -f2
}

# check_all <ref>: check_one for every newest_patch_tags entry.
check_all() {
	local ref="$1" v rc=0 r
	for v in $(newest_patch_tags); do
		r=0; check_one "$v" "$ref" || r=$?
		[ "$r" -gt "$rc" ] && rc="$r"
	done
	[ "$rc" = 0 ] && cs_log "$ref carries the changelog of every patch release"
	return "$rc"
}

# sync_pr <version>: run the sync on origin/main in a scratch worktree and open
# (or refresh) the pull request. The operator's checkout is never switched.
sync_pr() {
	local ver="${1#v}" repo="$CHANGELOG_SYNC_REPO" t wt branch rc=0 pr body
	branch="changelog/sync-v$ver"
	git -C "$repo" rev-parse -q --verify "origin/main^{commit}" >/dev/null || cs_die "origin/main not found"
	t="$(mktemp -d)"; wt="$t/wt"
	git -C "$repo" worktree add -q --detach "$wt" origin/main >/dev/null 2>&1 || { rm -rf "$t"; cs_die "could not create a worktree of origin/main"; }
	_cs_cleanup() {
		git -C "$repo" worktree remove --force "$wt" >/dev/null 2>&1 || true
		git -C "$repo" branch -D "$branch" >/dev/null 2>&1 || true
		rm -rf "$t"
	}
	sync_tree "$wt" "$ver" || rc=$?
	if [ "$rc" = 2 ]; then _cs_cleanup; return 2; fi
	if [ -z "$(git -C "$wt" status --porcelain)" ]; then
		cs_log "origin/main already carries the changelog of v$ver; no pull request needed"
		_cs_cleanup; return "$rc"
	fi
	if ! { git -C "$wt" checkout -q -B "$branch" &&
		git -C "$wt" add -A CHANGELOG.md "$FRAGMENT_SUBDIR" &&
		git -C "$wt" commit -q --signoff -m "changelog: sync v$ver to main"; }; then
		cs_log "could not commit the sync"; _cs_cleanup; return 2
	fi
	# The branch belongs to this script alone, so a re-run may replace it.
	git -C "$wt" push -q -f origin "$branch" >/dev/null 2>&1 || { cs_log "could not push $branch"; _cs_cleanup; return 2; }
	pr="$(gh pr list --repo "$CHANGELOG_SYNC_SLUG" --head "$branch" --state open --json number -q '.[0].number' 2>/dev/null || true)"
	if [ -n "$pr" ]; then
		cs_log "updated the open pull request #$pr ($branch)"
	else
		body="$(printf '%s\n\n%s\n\n%s\n' \
			"Brings the changelog of v$ver from its tag back to main: the dated sections of the v${ver%.*}.x patches that main lacks, and the deletion of the fragments those releases already folded on release-${ver%.*}." \
			"Without it the next minor cut from main would fold those fragments again and publish them as new. Opened by \`scripts/changelog-sync.sh $ver --pr\`; re-running it changes nothing once this is merged." \
			"Release mechanics: no changelog entry of its own (skip-changelog) and no milestone (the milestone guard exempts \`changelog/sync-v*\`).")"
		gh pr create --repo "$CHANGELOG_SYNC_SLUG" --base main --head "$branch" --label skip-changelog \
			--title "changelog: sync v$ver to main" --body "$body" >/dev/null 2>&1 ||
			{ cs_log "pushed $branch but could not open the pull request; open it by hand: gh pr create --base main --head $branch --label skip-changelog"; _cs_cleanup; return 2; }
		pr="$(gh pr list --repo "$CHANGELOG_SYNC_SLUG" --head "$branch" --state open --json number -q '.[0].number' 2>/dev/null || true)"
		cs_log "opened pull request #${pr:-?} ($branch); merge it once CI is green"
	fi
	_cs_cleanup
	return "$rc"
}

# ---- self-test ------------------------------------------------------------------

self_test() {
	# A git hook exports these, and the fixture repositories below would follow
	# them to the repository being committed to.
	unset GIT_DIR GIT_WORK_TREE GIT_INDEX_FILE GIT_OBJECT_DIRECTORY GIT_ALTERNATE_OBJECT_DIRECTORIES GIT_COMMON_DIR GIT_PREFIX
	local fail=0 tmp got rc
	_eq() { if [ "$1" = "$2" ]; then printf '  ok   %s\n' "$3"; else printf '  FAIL %s\n    got:  %q\n    want: %q\n' "$3" "$1" "$2"; fail=1; fi; }
	tmp="$(mktemp -d)"
	export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_SYSTEM=/dev/null
	# With no global config there is no identity, and sync_pr commits in its own
	# worktree: a runner has no fallback identity (a laptop often does).
	export GIT_AUTHOR_NAME=t GIT_AUTHOR_EMAIL=t@example.invalid GIT_COMMITTER_NAME=t GIT_COMMITTER_EMAIL=t@example.invalid

	# Version order, the one comparison every insert depends on.
	_lt() { if version_lt "$1" "$2"; then echo yes; else echo no; fi; }
	_eq "$(_lt 0.5.9 0.5.10)" "yes" "0.5.9 is older than 0.5.10 (numbers, not text)"
	_eq "$(_lt 0.3.0-rc.4 0.3.0)" "yes" "an rc is older than its GA"
	_eq "$(_lt 0.3.0-rc.2 0.3.0-rc.10)" "yes" "rc numbers compare as numbers"
	_eq "$(_lt 0.6.0 0.5.3)" "no" "a newer minor is not older"

	# fragment_text: the four scalar shapes changie and people write.
	mkdir -p "$tmp/f"
	printf 'kind: Fixed\nbody: |-\n  **Bold** first line\n  second line.\n\n  Second paragraph.\ntime: 2026-10-01T00:00:00Z\n' >"$tmp/f/block.yaml"
	printf "kind: Fixed\nbody: '**A pool''s anchor** no longer\n  leaks.'\ntime: x\n" >"$tmp/f/single.yaml"
	printf 'kind: Fixed\nbody: "say \\"hi\\" now"\ntime: x\n' >"$tmp/f/double.yaml"
	printf 'kind: Fixed\nbody: plain text here\n' >"$tmp/f/plain.yaml"
	printf 'kind: Fixed\ntime: x\n' >"$tmp/f/none.yaml"
	_eq "$(fragment_text "$tmp/f/block.yaml")" "**Bold** first line second line. Second paragraph." "a block body, paragraphs flattened"
	_eq "$(fragment_text "$tmp/f/single.yaml")" "**A pool's anchor** no longer leaks." "a single-quoted body, '' unescaped, folded"
	_eq "$(fragment_text "$tmp/f/double.yaml")" 'say "hi" now' "a double-quoted body"
	_eq "$(fragment_text "$tmp/f/plain.yaml")" "plain text here" "a plain body"
	_eq "$(fragment_text "$tmp/f/none.yaml")" "" "no body is empty"

	# entry_in: matches a whole entry, never a piece of one.
	local hay; hay=" ### Fixed - xcom reads are faster. - A thing (#1). With more. ## [0.1.0] - 2026-01-01 "
	_e() { if entry_in "$hay" "$1"; then echo yes; else echo no; fi; }
	_eq "$(_e 'A thing (#1). With more.')" "yes" "a whole entry matches"
	_eq "$(_e 'x')" "no" "a short body does not match the start of a longer word"
	_eq "$(_e 'reads are faster.')" "no" "the middle of an entry does not match"
	_eq "$(_e '')" "no" "an empty body never matches"

	# section_of stops at the next section and at the link references.
	printf '%s\n' '# C' '' '## [Unreleased]' '' '## [0.1.10] - 2026-01-02' '' '- ten' '' '## [0.1.1] - 2026-01-01' '' '- one' '' '[0.1.1]: https://example.invalid' >"$tmp/s.md"
	_eq "$(section_of "$tmp/s.md" 0.1.1)" "$(printf '%s\n' '## [0.1.1] - 2026-01-01' '' '- one')" "section_of stops at the link references"
	_eq "$(section_of "$tmp/s.md" 0.1.10 | head -1)" "## [0.1.10] - 2026-01-02" "0.1.1 and 0.1.10 are different sections"

	# The fixture: main and release-0.9 as ADR 0062 shapes them.
	local r="$tmp/repo"
	_g() { git -C "$r" -c user.name=t -c user.email=t@example.invalid -c commit.gpgsign=false "$@" >/dev/null 2>&1; }
	_frag() { mkdir -p "$r/.changes/unreleased"; printf 'kind: %s\nbody: |-\n  %s\n' "$2" "$3" >"$r/.changes/unreleased/$1"; }
	mkdir -p "$r"; _g init -q -b main
	printf '%s\n' '# Changelog' '' '## [Unreleased]' '' '## [0.9.0] - 2026-01-01' '' '### Added' '' '- the start' >"$r/CHANGELOG.md"
	_g add -A; _g commit -qm "release: promote v0.9.0 GA"; _g tag v0.9.0
	_frag a.yaml Fixed 'Fix a (#1).'; _g add -A; _g commit -qm "fix: a (#1)"
	_frag b.yaml Added 'Add b (#2), held for 1.0.'; _g add -A; _g commit -qm "feat: b (#2)"
	_frag c.yaml Fixed 'Fix c (#3).'; _g add -A; _g commit -qm "fix: c (#3)"
	_frag d.yaml Fixed 'Fix d (#4).'; _g add -A; _g commit -qm "fix: d (#4)"
	# release-0.9: a, c and d are picked; the 0.9.1 cut folds them.
	_g checkout -q -b release-0.9 v0.9.0
	_frag a.yaml Fixed 'Fix a (#1).'; _frag c.yaml Fixed 'Fix c (#3).'; _frag d.yaml Fixed 'Fix d (#4).'
	_g add -A; _g commit -qm "picks"
	printf '%s\n' '# Changelog' '' '## [Unreleased]' '' '## [0.9.1] - 2026-01-05' '' '### Fixed' '' '- Fix a (#1).' '- Fix c (#3).' '- Fix d (#4).' '' '## [0.9.0] - 2026-01-01' '' '### Added' '' '- the start' >"$r/CHANGELOG.md"
	rm -f "$r/.changes/unreleased/"*.yaml; _g add -A; _g commit -qm "release: promote v0.9.1 GA"; _g tag v0.9.1
	_g checkout -q main
	# main renamed c, and edited d after the pick (so it is not what shipped).
	_g mv .changes/unreleased/c.yaml .changes/unreleased/c-renamed.yaml
	_frag d.yaml Fixed 'Fix d (#4), reworded on main.'
	printf '%s\n' '# Changelog' '' '## [Unreleased]' '' 'A hand note.' '' '## [0.9.0] - 2026-01-01' '' '### Added' '' '- the start' >"$r/CHANGELOG.md"
	_g add -A; _g commit -qm "main moves on"; _g update-ref refs/remotes/origin/main HEAD
	export CHANGELOG_SYNC_REPO="$r"

	# --check sees the drift and touches nothing.
	rc=0; got="$(check_one 0.9.1 origin/main 2>&1)" || rc=$?
	_eq "$rc" "1" "check: main out of sync with v0.9.1 is drift"
	_eq "$(printf '%s' "$got" | grep -c 'already shipped: a.yaml')" "1" "check names the shipped fragment"
	_eq "$(git -C "$r" status --porcelain)" "" "check writes nothing"

	# The sync itself, on the checkout.
	rc=0; sync_tree "$r" 0.9.1 2>"$tmp/log" || rc=$?
	_eq "$rc" "1" "a fragment consumed by name with other text needs a person (exit 1)"
	_eq "$(grep -n '^## ' "$r/CHANGELOG.md" | tr '\n' ' ')" "3:## [Unreleased] 7:## [0.9.1] - 2026-01-05 15:## [0.9.0] - 2026-01-01 " "[0.9.1] lands between [Unreleased] and [0.9.0]"
	_eq "$(grep -c '^A hand note.$' "$r/CHANGELOG.md")" "1" "[Unreleased] content on main is kept"
	_eq "$(diff <(section_of "$r/CHANGELOG.md" 0.9.1) <(git -C "$r" show v0.9.1:CHANGELOG.md | section_of /dev/stdin 0.9.1) && echo same)" "same" "the section is verbatim from the tag"
	_eq "$(cd "$r/.changes/unreleased" && ls | tr '\n' ' ')" "b.yaml d.yaml " "a and the renamed c are removed; b and d stay"
	_eq "$(grep -c 'KEPT d.yaml' "$tmp/log")" "1" "the kept fragment is reported"
	_eq "$(grep -c 'removed c-renamed.yaml' "$tmp/log")" "1" "the content match is reported"

	# Idempotent: d resolved by hand, then a second run changes nothing.
	rm -f "$r/.changes/unreleased/d.yaml"
	cp "$r/CHANGELOG.md" "$tmp/before.md"
	rc=0; sync_tree "$r" 0.9.1 2>/dev/null || rc=$?
	_eq "$rc" "0" "a re-run exits 0"
	_eq "$(cmp -s "$tmp/before.md" "$r/CHANGELOG.md" && echo same)" "same" "a re-run leaves the CHANGELOG byte for byte"
	_g add -A; _g commit -qm "changelog: sync v0.9.1 to main"; _g update-ref refs/remotes/origin/main HEAD
	rc=0; check_one 0.9.1 origin/main >/dev/null 2>&1 || rc=$?
	_eq "$rc" "0" "check: a synced main is clean"

	# A later patch, a skipped sync, and a newer minor already on main.
	_g checkout -q release-0.9
	_frag e.yaml Fixed 'Fix e (#5).'; _g add -A; _g commit -qm "pick e"
	printf '%s\n' '# Changelog' '' '## [Unreleased]' '' '## [0.9.2] - 2026-01-07' '' '### Fixed' '' '- Fix e (#5).' '' '## [0.9.1] - 2026-01-05' '' '### Fixed' '' '- Fix a (#1).' '- Fix c (#3).' '- Fix d (#4).' '' '## [0.9.0] - 2026-01-01' '' '### Added' '' '- the start' >"$r/CHANGELOG.md"
	rm -f "$r/.changes/unreleased/"*.yaml; _g add -A; _g commit -qm "release: promote v0.9.2 GA"; _g tag v0.9.2
	_frag f.yaml Fixed 'Fix f (#6).'; _g add -A; _g commit -qm "pick f"
	printf '%s\n' '# Changelog' '' '## [Unreleased]' '' '## [0.9.3] - 2026-01-09' '' '### Fixed' '' '- Fix f (#6).' '' '## [0.9.2] - 2026-01-07' '' '### Fixed' '' '- Fix e (#5).' '' '## [0.9.1] - 2026-01-05' '' '### Fixed' '' '- Fix a (#1).' '- Fix c (#3).' '- Fix d (#4).' '' '## [0.9.0] - 2026-01-01' '' '### Added' '' '- the start' >"$r/CHANGELOG.md"
	rm -f "$r/.changes/unreleased/"*.yaml; _g add -A; _g commit -qm "release: promote v0.9.3 GA"; _g tag v0.9.3
	_g checkout -q main
	_frag e.yaml Fixed 'Fix e (#5).'; _frag f.yaml Fixed 'Fix f (#6).'
	printf '%s\n' '# Changelog' '' '## [Unreleased]' '' '## [1.0.0] - 2026-01-08' '' '### Added' '' '- Add b (#2), held for 1.0.' '' '## [0.9.1] - 2026-01-05' '' '### Fixed' '' '- Fix a (#1).' '- Fix c (#3).' '- Fix d (#4).' '' '## [0.9.0] - 2026-01-01' '' '### Added' '' '- the start' '' '[0.9.0]: https://example.invalid/v0.9.0' >"$r/CHANGELOG.md"
	rm -f "$r/.changes/unreleased/b.yaml"
	_g add -A; _g commit -qm "release: promote v1.0.0 GA"; _g tag v1.0.0; _g update-ref refs/remotes/origin/main HEAD
	_g tag v0.0.2 v0.9.0
	_eq "$(newest_patch_tags | tr '\n' ' ')" "0.9.3 " "check-all looks at the newest patch of each minor that has an X.Y.0 tag"
	rc=0; check_all origin/main >/dev/null 2>&1 || rc=$?
	_eq "$rc" "1" "check-all: a skipped 0.9.2 sync and a pending 0.9.3 one are drift"
	rc=0; sync_tree "$r" 0.9.3 2>/dev/null || rc=$?
	_eq "$rc" "0" "syncing 0.9.3 exits 0"
	_eq "$(grep '^## ' "$r/CHANGELOG.md" | tr '\n' '|')" "## [Unreleased]|## [1.0.0] - 2026-01-08|## [0.9.3] - 2026-01-09|## [0.9.2] - 2026-01-07|## [0.9.1] - 2026-01-05|## [0.9.0] - 2026-01-01|" \
		"syncing 0.9.3 also repairs 0.9.2, both below the newer minor and in version order"
	_eq "$(tail -1 "$r/CHANGELOG.md")" "[0.9.0]: https://example.invalid/v0.9.0" "the link references stay at the bottom"
	_eq "$(ls "$r/.changes/unreleased" 2>/dev/null | wc -l | tr -d ' ')" "0" "both patches' fragments are removed"

	# Nothing to sync from: no tag yet.
	rc=0; sync_tree "$r" 0.9.4 2>/dev/null || rc=$?
	_eq "$rc" "2" "a tag that does not exist is an error, not a silent pass"

	# --pr against a bare origin and a gh stub: the operator's branch and tree
	# are left alone, the branch is pushed, the PR carries skip-changelog, and
	# a second run with nothing left to sync opens nothing.
	_g checkout -q -- . ; _g checkout -q main
	git -C "$r" reset -q --hard >/dev/null 2>&1
	git init -q --bare "$tmp/origin.git"
	_g remote add origin "$tmp/origin.git"
	_g push -q origin main
	_g fetch -q origin
	_g checkout -q -b operator-branch
	mkdir -p "$tmp/bin"
	printf '#!/bin/sh\nprintf "%%s\\n" "$*" >>"$GH_LOG"\ncase "$*" in *"pr list"*) [ -f "$GH_OPEN" ] && echo 42 ;; *"pr create"*) touch "$GH_OPEN" ;; esac\nexit 0\n' >"$tmp/bin/gh"
	chmod +x "$tmp/bin/gh"
	rc=0
	(export PATH="$tmp/bin:$PATH" GH_LOG="$tmp/gh.log" GH_OPEN="$tmp/gh.open"; sync_pr 0.9.3) >/dev/null 2>&1 || rc=$?
	_eq "$rc" "0" "--pr exits 0"
	_eq "$(git -C "$r" rev-parse --abbrev-ref HEAD)|$(git -C "$r" status --porcelain)" "operator-branch|" "--pr leaves the operator's checkout alone"
	_eq "$(git -C "$tmp/origin.git" show changelog/sync-v0.9.3:CHANGELOG.md 2>/dev/null | grep -c '^## \[0.9.3\]')" "1" "--pr pushes the synced CHANGELOG"
	_eq "$(git -C "$tmp/origin.git" log -1 --format=%s changelog/sync-v0.9.3 2>/dev/null)" "changelog: sync v0.9.3 to main" "--pr commits with the subject release-gap.sh treats as mechanics"
	_eq "$(git -C "$tmp/origin.git" log -1 --format='%ae|%(trailers:key=Signed-off-by,valueonly)' changelog/sync-v0.9.3 2>/dev/null | sed '/^$/d')" \
		"$(git -C "$tmp/origin.git" log -1 --format='%ae|%an <%ae>' changelog/sync-v0.9.3 2>/dev/null)" "--pr signs the sync off as its author (DCO, #2103)"
	_eq "$(grep -c -- 'pr create .*--label skip-changelog' "$tmp/gh.log")" "1" "--pr opens the PR with skip-changelog"
	_eq "$(git -C "$r" worktree list | wc -l | tr -d ' ')" "1" "--pr removes its scratch worktree"
	# Merge it, then re-run.
	_g push -q origin "refs/remotes/origin/changelog/sync-v0.9.3:refs/heads/main" 2>/dev/null || git -C "$tmp/origin.git" update-ref refs/heads/main refs/heads/changelog/sync-v0.9.3
	_g fetch -q origin
	: >"$tmp/gh.log"
	rc=0
	(export PATH="$tmp/bin:$PATH" GH_LOG="$tmp/gh.log" GH_OPEN="$tmp/gh.open"; sync_pr 0.9.3) >/dev/null 2>&1 || rc=$?
	_eq "$rc|$(grep -c 'pr create' "$tmp/gh.log")" "0|0" "--pr on a synced main opens nothing"

	unset CHANGELOG_SYNC_REPO GIT_CONFIG_GLOBAL GIT_CONFIG_SYSTEM
	rm -rf "$tmp"
	if [ "$fail" = 0 ]; then echo "self-test: PASS"; return 0; else echo "self-test: FAIL"; return 1; fi
}

# ---- entry point ----------------------------------------------------------------

main() {
	local mode=apply version="" fetch=0 arg rc=0
	for arg in "$@"; do
		case "$arg" in
			--self-test) self_test; exit $? ;;
			--check) mode=check ;;
			--check-all) mode=check-all ;;
			--pr) mode='pr' ;;
			--fetch) fetch=1 ;;
			-*) cs_die "unknown flag: $arg" ;;
			*) version="${arg#v}" ;;
		esac
	done
	if [ "$fetch" = 1 ]; then
		git -C "$CHANGELOG_SYNC_REPO" fetch -q origin --tags "+refs/heads/main:refs/remotes/origin/main" || cs_die "cannot reach origin"
	fi
	if [ "$mode" = check-all ]; then
		check_all origin/main; exit $?
	fi
	[[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] ||
		cs_die "usage: changelog-sync.sh <X.Y.Z> [--check | --pr] [--fetch] | --check-all [--fetch] | --self-test"
	case "$mode" in
		check) check_one "$version" origin/main; exit $? ;;
		pr) sync_pr "$version"; exit $? ;;
		apply) sync_tree "$CHANGELOG_SYNC_REPO" "$version" || rc=$?; exit "$rc" ;;
	esac
}

# Sourced by check-changelog-structure.sh for the helpers above; run otherwise.
if [ "${BASH_SOURCE[0]}" = "$0" ]; then main "$@"; fi
