#!/usr/bin/env bash
# Compose the GitHub Release body for a tag, from CHANGELOG.md.
#
# WHY THIS EXISTS. The release body used to be GoReleaser's commit list, and on
# a GA that list is empty by construction: a GA is cut from the rc, so the only
# commit between the two tags is the promotion itself. v0.4.7's release page
# says exactly one line, `release: promote v0.4.7 GA`, while CHANGELOG.md holds
# 35 entries for the same version. The page most people land on carried none of
# the release, and the prose written for humans stayed in a file.
#
# It also silently kept noise it meant to drop: the exclude filters were
# `^docs:`, `^test:`, `^chore:`, and every commit in this repository is scoped
# (`docs(changelog):`), so the anchored patterns never matched one of them.
# Removing the generated list removes that whole class rather than patching the
# regexes.
#
# The commits are not lost: the footer links the compare view, which is the
# complete log, one click away, instead of a filtered copy of it.
#
# Usage:
#   scripts/release-notes.sh v0.4.8           # GA: the [0.4.8] section
#   scripts/release-notes.sh v0.4.8-rc.1      # rc: the [Unreleased] section
#   scripts/release-notes.sh --self-test
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHANGELOG="${LEOFLOW_CHANGELOG:-$ROOT/CHANGELOG.md}"
REPO_URL="https://github.com/dexadata/dexaflow"
UPGRADES_URL="https://dexaflow.dexadata.ai/operate/upgrades/"

# section <changelog-file> <heading>: print the body under `## <heading>`, up to
# the next `## ` heading. A pure filter, so the self-test needs no git and no
# release.
section() { # <file> <heading>
	awk -v want="$2" '
		$0 == "## " want { inside = 1; next }
		inside && /^## / { exit }
		inside           { print }
	' "$1"
}

# trim_blank_edges: drop leading and trailing blank lines, keeping the inner
# ones. Entries here run to several paragraphs and those blanks are the
# paragraphs.
trim_blank_edges() {
	awk '
		{ lines[NR] = $0 }
		END {
			s = 1; e = NR
			while (s <= e && lines[s] ~ /^[ \t]*$/) s++
			while (e >= s && lines[e] ~ /^[ \t]*$/) e--
			for (i = s; i <= e; i++) print lines[i]
		}
	'
}

# notes_body <changelog> <tag>: the human-written part of the release body.
#
# A GA has its own dated section, because cut-release.sh renames [Unreleased]
# to [X.Y.Z] in the prepare PR. An rc does not: it keeps [Unreleased], so that
# is where its entries are. Falling back rather than failing is what makes the
# same script right for both.
notes_body() { # <changelog> <tag>
	local file="$1" tag="$2" ver body
	ver="${tag#v}"
	body="$(section "$file" "[$ver]" | trim_blank_edges)"
	if [ -z "$body" ]; then
		# A dated heading carries the date: `## [0.4.8] - 2026-09-21`.
		body="$(awk -v pfx="## [$ver] " '
			index($0, pfx) == 1 { inside = 1; next }
			inside && /^## /    { exit }
			inside              { print }
		' "$file" | trim_blank_edges)"
	fi
	[ -n "$body" ] || body="$(section "$file" "[Unreleased]" | trim_blank_edges)"
	printf '%s\n' "$body"
}

# kind_block <body> <kind>: the entries under `### <kind>` in a section body,
# up to the next `### ` heading, edges trimmed. Empty when the kind is absent.
kind_block() { # <body> <kind>
	printf '%s\n' "$1" | awk -v want="### $2" '
		$0 == want   { inside = 1; next }
		inside && /^### / { exit }
		inside       { print }
	' | trim_blank_edges
}

# without_kinds <body> <kind>...: the section body with those `### <kind>`
# blocks removed, so an entry is printed once, in the opening.
without_kinds() { # <body> <kind>...
	local body="$1"; shift
	printf '%s\n' "$body" | awk -v drop="$(IFS=,; echo "$*")" '
		BEGIN { n = split(drop, d, ","); for (i = 1; i <= n; i++) skip["### " d[i]] = 1 }
		/^### / { dropping = ($0 in skip) }
		!dropping { print }
	' | trim_blank_edges
}

# opening <body>: what an operator reads before upgrading (#1557), in front of
# the per kind lists. Highlights when the release has them; Breaking changes
# and Action required always, with "None." when there is no entry, so that a
# release without them says so instead of leaving the reader to search.
opening() { # <body>
	local hl br up
	hl="$(kind_block "$1" Highlights)"
	br="$(kind_block "$1" Breaking)"
	up="$(kind_block "$1" Upgrade)"
	if [ -n "$hl" ]; then printf '### Highlights\n\n%s\n\n' "$hl"; fi
	printf '### Breaking changes\n\n%s\n\n' "${br:-None.}"
	printf '### Action required\n\n%s\n\n' "${up:-None.}"
	printf 'Read the [upgrades guide](%s) for the version you upgrade to before you upgrade.\n' "$UPGRADES_URL"
}

# previous_tag <tag>: the tag before this one, for the compare link. Silent when
# there is none (the first release) rather than printing a broken link.
previous_tag() { # <tag>
	git -C "$ROOT" describe --tags --abbrev=0 "${1}^" 2>/dev/null || true
}

self_test() {
	local fail=0
	_eq() { if [ "$1" = "$2" ]; then printf '  ok   %s\n' "$3"
		else printf '  FAIL %s\n    got:  %q\n    want: %q\n' "$3" "$1" "$2"; fail=1; fi; }
	local tmp; tmp="$(mktemp -d)"; trap 'rm -rf "${tmp:-}"' RETURN
	printf '%s\n' '# Changelog' '' '## [Unreleased]' '' '### Added' '' '- pending thing' '' \
		'## [0.4.8] - 2026-09-21' '' '### Fixed' '' '- the ga thing' '' '  second paragraph' '' \
		'## [0.4.7] - 2026-09-19' '' '### Fixed' '' '- the old thing' > "$tmp/cl.md"

	# A GA takes its own dated section, never [Unreleased] and never the one below.
	local ga; ga="$(notes_body "$tmp/cl.md" v0.4.8)"
	_eq "$(printf '%s' "$ga" | grep -c 'the ga thing')"   "1" "GA takes its dated section"
	_eq "$(printf '%s' "$ga" | grep -c 'pending thing')"  "0" "GA does not take [Unreleased]"
	_eq "$(printf '%s' "$ga" | grep -c 'the old thing')"  "0" "GA stops at the previous version"
	_eq "$(printf '%s' "$ga" | grep -c 'second paragraph')" "1" "a multi-paragraph entry survives"

	# An rc has no section of its own, so it reports what is pending.
	local rc; rc="$(notes_body "$tmp/cl.md" v0.4.9-rc.1)"
	_eq "$(printf '%s' "$rc" | grep -c 'pending thing')"  "1" "an rc falls back to [Unreleased]"
	_eq "$(printf '%s' "$rc" | grep -c 'the ga thing')"   "0" "an rc does not take a released section"

	# The whole point of the change: a body that is not empty. An empty one is
	# what shipped before, and it looked like a successful release.
	_eq "$([ -n "$ga" ] && echo nonempty)" "nonempty" "the GA body is not empty"

	# Edges trimmed, paragraphs kept.
	_eq "$(printf '%s' "$ga" | head -1)" "### Fixed" "no leading blank line"
	_eq "$(printf '%s' "$ga" | tail -1 | tr -d ' ')" "secondparagraph" "no trailing blank line"

	# The opening (#1557): the three kinds an operator reads first.
	local b2 op rest
	b2="$(printf '%s\n' '### Highlights' '' '- why this release' '' '### Upgrade' '' '- run the migration' '' '### Fixed' '' '- a fix')"
	op="$(opening "$b2")"
	_eq "$(printf '%s' "$op" | grep -c '^### ')" "3" "the opening has Highlights, Breaking changes and Action required"
	_eq "$(printf '%s' "$op" | grep -A2 '^### Breaking changes' | tail -1)" "None." "no Breaking entry says None."
	_eq "$(printf '%s' "$op" | grep -A2 '^### Action required' | tail -1)" "- run the migration" "Upgrade entries are the Action required section"
	_eq "$(printf '%s' "$op" | grep -c 'a fix')" "0" "the opening carries no other kind"
	rest="$(without_kinds "$b2" Highlights Breaking Upgrade)"
	_eq "$(printf '%s' "$rest" | grep -c 'why this release\|run the migration')" "0" "an opening entry is not printed twice"
	_eq "$(printf '%s' "$rest" | head -1)" "### Fixed" "the other kinds follow, edges trimmed"
	op="$(opening "$ga")"
	_eq "$(printf '%s' "$op" | grep -c '^### Highlights')" "0" "no Highlights heading when none were written"
	_eq "$(printf '%s' "$op" | grep -c '^None\.$')" "2" "a release with no Breaking or Upgrade entry says None. twice"

	if [ "$fail" -eq 0 ]; then echo "self-test: PASS"; return 0; else echo "self-test: FAIL"; return 1; fi
}

[ "${1:-}" = "--self-test" ] && { self_test; exit $?; }

tag="${1:-}"
[ -n "$tag" ] || { echo "usage: $(basename "$0") <tag> | --self-test" >&2; exit 2; }
[ -f "$CHANGELOG" ] || { echo "FATAL: $CHANGELOG not found" >&2; exit 1; }

body="$(notes_body "$CHANGELOG" "$tag")"
if [ -z "$body" ]; then
	# Loud, not empty. A release page with no notes is the defect this script
	# exists to remove, and it is indistinguishable from a successful one.
	echo "FATAL: no changelog section for ${tag} and no [Unreleased] to fall back to" >&2
	exit 1
fi

cat <<HEADER
## Dexaflow ${tag}

A \`0.x\` (pre-1.0) build — SemVer carries the maturity, there is no separate
alpha/beta, and the pre-alpha series ended at \`v0.0.1\` (ADR 0037). APIs and
on-disk shape may still evolve between minor versions; \`-rc.N\` tags are release
candidates gated by the E2E suite. Install **this exact release** with:

\`\`\`bash
curl -fsSL https://raw.githubusercontent.com/dexadata/dexaflow/${tag}/install.sh | LEOFLOW_VERSION=${tag} sh
\`\`\`

> A bare \`curl … | sh\` installs the latest **stable** release (\`/releases/latest\`
> excludes pre-releases), so on a pre-release page it would NOT give you \`${tag}\`.
> \`LEOFLOW_VERSION\` must sit on the **\`sh\`** side of the pipe (not \`curl\`) — a
> \`VAR=x curl … | sh\` prefix sets the var for \`curl\` only, so \`install.sh\` would
> not see it and would fall back to latest-stable.

---

HEADER

opening "$body"
printf '\n---\n\n'
rest="$(without_kinds "$body" Highlights Breaking Upgrade)"
[ -z "$rest" ] || printf '%s\n\n' "$rest"

prev="$(previous_tag "$tag")"
echo '---'
if [ -n "$prev" ]; then
	printf '**Full commit log:** %s/compare/%s...%s\n\n' "$REPO_URL" "$prev" "$tag"
fi
cat <<'FOOTER'
Artifacts are checksummed (SHA-256) and the checksums file is cosign-signed
(keyless). Verify with `cosign verify-blob`.
FOOTER
