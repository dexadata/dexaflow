#!/usr/bin/env bash
# check-changelog-structure.sh: CHANGELOG.md and the pending fragments are
# well formed and agree with each other and with the tags.
#
#   scripts/check-changelog-structure.sh                # the repository root
#   scripts/check-changelog-structure.sh --root <dir>   # another tree (fixtures)
#   scripts/check-changelog-structure.sh --self-test
#
# The tags are read with `git tag -l`, or from CHANGELOG_TAGS (one tag per line)
# when it is set, which is how CI passes them without fetching tag history.
#
# Why. Patches folded their fragments on release-0.5 and nothing brought the
# result back, so main reached v0.5.3 missing three dated sections and holding
# 97 fragments that had already shipped (see scripts/changelog-sync.sh). Before
# that, the hand-edited [Unreleased] ended up with five headings for three kinds
# (#1200). Both were visible in the file and nothing read it. This does:
#
#   1. Exactly one `## [Unreleased]`, and it is the first `## ` heading.
#   2. Every other `## ` heading is `## [X.Y.Z] - YYYY-MM-DD`.
#   3. Those versions strictly descend, with no duplicates.
#   4. In [Unreleased] and every section from LEGACY_BELOW on, each `### `
#      heading is a kind .changie.yaml declares, at most once, in the declared
#      order, and none is empty.
#   5. Every GA tag vX.Y.Z of a minor this file records (it has a section of
#      that X.Y) has its section. The newest tag of each minor may be missing:
#      that is the window between pushing a patch tag and merging the sync pull
#      request cut-release.sh opens right after, and blocking every pull request
#      to main during it would punish the wrong people. One release later the
#      gap is an error, and the cut's own preflight (changelog-sync.sh
#      --check-all) does not grant the window at all.
#   6. Every fragment in .changes/unreleased parses as YAML and has the two
#      fields .changie.yaml makes it need: a `kind` it declares (it declares
#      `kinds:`) and a non-empty `body` (it declares `body:`). changie also
#      writes `time:`, but only sorts by it, and two fragments that shipped in
#      0.5.1 have none, so it is not required. Its text is also not already an
#      entry of a dated section. and its text is not already an entry of a dated section.
#
# Sections older than LEGACY_BELOW are released history written by hand before
# the fold existed: rc sections, headings such as "Docs" or "Known issues", a
# repeated "Security". They are checked for 1 to 3 and 5 only, because
# rewriting published notes to satisfy a newer rule is worse than the rule.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SCRIPTS="$ROOT/scripts"
# The first section after the last hand-written one that breaks rule 4
# ([0.4.6] has a "Testing" heading). Never raise it to silence a new failure.
LEGACY_BELOW="0.4.7"

# shellcheck source=SCRIPTDIR/changelog-sync.sh
. "$SCRIPTS/changelog-sync.sh"
set -euo pipefail

# changie_kinds <changie.yaml>: the declared kinds, comma separated, read the
# same way changelog-fold.sh's self-test reads them.
changie_kinds() {
	awk '/^kinds:/{k=1;next} k&&/^  - label:/{printf "%s%s", sep, $3; sep=","} k&&/^[^ -]/{exit}' "$1"
}

# check_headings <changelog> <kinds>: rules 1 to 4, one problem per line. One
# awk pass, because macOS still ships bash 3.2 and its lack of associative
# arrays is the wrong reason for a release gate to fail on an operator's Mac.
check_headings() {
	awk -v kinds="$1" -v legacy="$LEGACY_BELOW" '
		# key: a string that sorts like the version (see semver_key in
		# changelog-sync.sh); a GA sorts after its own prereleases.
		function key(v,   core, pre, p, n, lab) {
			core = v; pre = ""
			if (index(v, "-")) { core = substr(v, 1, index(v, "-") - 1); pre = substr(v, index(v, "-") + 1) }
			split(core, p, ".")
			if (pre == "") return sprintf("%08d%08d%08d~", p[1], p[2], p[3])
			lab = pre; sub(/\..*/, "", lab); n = pre; sub(/^[^.]*\./, "", n)
			return sprintf("%08d%08d%08d%s%08d", p[1], p[2], p[3], lab, n + 0)
		}
		function close_kind() { if (strict && kind != "" && !body) printf "line %d: %s ### %s is empty\n", kline, sect, kind }
		BEGIN { n = split(kinds, order, ","); for (i = 1; i <= n; i++) rank[order[i]] = i; lkey = key(legacy) }
		/^## / {
			close_kind(); kind = ""; last = 0; split("", seen)
			heads++
			if ($0 == "## [Unreleased]") {
				unrel++
				if (heads != 1) printf "line %d: ## [Unreleased] is not the first section\n", NR
				if (unrel > 1) printf "line %d: a second ## [Unreleased]\n", NR
				sect = "[Unreleased]"; strict = 1; next
			}
			if ($0 ~ /^## \[[0-9]+\.[0-9]+\.[0-9]+(-rc\.[0-9]+)?\] - [0-9][0-9][0-9][0-9]-(0[1-9]|1[0-2])-(0[1-9]|[12][0-9]|3[01])$/) {
				v = $0; sub(/^## \[/, "", v); sub(/\].*/, "", v)
				sect = "[" v "]"; k = key(v)
				strict = (k >= lkey)
				if (strict && index(v, "-")) printf "line %d: %s is a prerelease section; an rc keeps [Unreleased] and only a GA is dated\n", NR, sect
				if (pkey != "" && k == pkey) printf "line %d: %s appears twice\n", NR, sect
				else if (pkey != "" && k > pkey) printf "line %d: %s is not older than %s above it\n", NR, sect, psect
				pkey = k; psect = sect
				next
			}
			printf "line %d: not a section heading of the form ## [X.Y.Z] - YYYY-MM-DD: %s\n", NR, $0
			sect = $0; strict = 0; next
		}
		/^### / {
			close_kind()
			kd = substr($0, 5); sub(/[ \t]+$/, "", kd)
			kind = kd; kline = NR; body = 0
			if (!strict) next
			if (!(kd in rank)) { printf "line %d: %s ### %s is not a kind .changie.yaml declares (%s)\n", NR, sect, kd, kinds; next }
			if (kd in seen) { printf "line %d: %s ### %s appears twice\n", NR, sect, kd; next }
			seen[kd] = 1
			if (rank[kd] < last) printf "line %d: %s ### %s is out of the declared order (%s)\n", NR, sect, kd, kinds
			last = rank[kd]
			next
		}
		/^\[[^]]+\]: / { close_kind(); kind = ""; next }
		NF { body = 1 }
		END { close_kind(); if (!unrel) print "no ## [Unreleased] section" }
	' "$2"
}

# check_tags <changelog>: rule 5.
check_tags() {
	local cl="$1" tags minors t v m newest
	if [ -n "${CHANGELOG_TAGS+x}" ]; then tags="$CHANGELOG_TAGS"
	else tags="$(git -C "$ROOT" tag -l 'v*' 2>/dev/null || true)"; fi
	tags="$(printf '%s\n' "$tags" | sed 's/^v//' | grep -E '^[0-9]+\.[0-9]+\.[0-9]+$' || true)"
	if [ -z "$tags" ]; then
		echo "note: no GA tags visible, so rule 5 (every tag has its section) checked nothing" >&2
		return 0
	fi
	minors="$(dated_versions "$cl" | grep -E '^[0-9]+\.[0-9]+\.[0-9]+$' | sed 's/\.[0-9]*$//' | sort -u)"
	for m in $minors; do
		newest="$(printf '%s\n' "$tags" | awk -F. -v m="$m" '$1"."$2==m' | while IFS= read -r v; do printf '%s %s\n' "$(semver_key "$v")" "$v"; done | sort | tail -1 | cut -d' ' -f2)"
		for t in $(printf '%s\n' "$tags" | awk -F. -v m="$m" '$1"."$2==m'); do
			dated_versions "$cl" | grep -qxF -- "$t" && continue
			if [ "$t" = "$newest" ]; then
				echo "note: v$t has no section yet; allowed while its sync pull request (changelog: sync v$t to main) is open" >&2
			else
				echo "v$t is tagged but has no section; run scripts/changelog-sync.sh $newest --pr"
			fi
		done
	done
}

# check_fragments <root> <kinds>: rule 6.
check_fragments() {
	local root="$1" kinds="$2" f released text
	local dir="$root/.changes/unreleased"
	[ -d "$dir" ] || return 0
	shopt -s nullglob
	local -a frags=("$dir"/*.yaml "$dir"/*.yml)
	shopt -u nullglob
	[ "${#frags[@]}" -gt 0 ] || return 0
	python3 - "$kinds" "${frags[@]}" <<-'PY' || echo "fragment validation could not run (python3 with PyYAML is required)"
		import os, sys
		try:
		    import yaml
		except ImportError:
		    sys.exit(1)
		kinds = sys.argv[1].split(",")
		for path in sys.argv[2:]:
		    name = os.path.basename(path)
		    try:
		        with open(path, encoding="utf-8") as fh:
		            doc = yaml.safe_load(fh)
		    except Exception as exc:  # any parse error is the finding
		        print(f"{name}: does not parse as YAML: {str(exc).splitlines()[0]}")
		        continue
		    if not isinstance(doc, dict):
		        print(f"{name}: is not a mapping of changie fields")
		        continue
		    for field in ("kind", "body"):
		        if field not in doc:
		            print(f"{name}: has no {field}: field")
		    kind = doc.get("kind")
		    if "kind" in doc and kind not in kinds:
		        print(f"{name}: kind {kind!r} is not one .changie.yaml declares ({','.join(kinds)})")
		    body = doc.get("body")
		    if "body" in doc and (not isinstance(body, str) or not body.strip()):
		        print(f"{name}: body is empty")
	PY
	released="$(released_text "$root/CHANGELOG.md")"
	for f in "${frags[@]}"; do
		text="$(fragment_text "$f")"
		[ -n "$text" ] || continue
		if entry_in "$released" "$text"; then
			echo "$(basename "$f"): already an entry of a dated section; it shipped, so delete it (scripts/changelog-sync.sh)"
		fi
	done
}

# check_root <root>: every rule; prints the problems, returns 1 when any.
check_root() {
	local root="$1" kinds out
	[ -f "$root/CHANGELOG.md" ] || { echo "no CHANGELOG.md in $root"; return 1; }
	[ -f "$root/.changie.yaml" ] || { echo "no .changie.yaml in $root"; return 1; }
	kinds="$(changie_kinds "$root/.changie.yaml")"
	[ -n "$kinds" ] || { echo "no kinds declared in $root/.changie.yaml"; return 1; }
	out="$(
		check_headings "$kinds" "$root/CHANGELOG.md"
		check_tags "$root/CHANGELOG.md"
		check_fragments "$root" "$kinds"
	)"
	if [ -n "$out" ]; then printf '%s\n' "$out"; return 1; fi
	return 0
}

self_test() {
	local fail=0 tmp
	_eq() { if [ "$1" = "$2" ]; then printf '  ok   %s\n' "$3"; else printf '  FAIL %s\n    got:  %q\n    want: %q\n' "$3" "$1" "$2"; fail=1; fi; }
	tmp="$(mktemp -d)"
	# _fx <name> <changelog lines...>: a fixture root with the real .changie.yaml.
	_fx() {
		local d="$tmp/$1"; shift
		mkdir -p "$d/.changes/unreleased"
		cp "$ROOT/.changie.yaml" "$d/"
		printf '%s\n' "$@" >"$d/CHANGELOG.md"
	}
	_frag() { printf '%b' "$3" >"$tmp/$1/.changes/unreleased/$2"; }
	# _case <fixture> <want: ok | substring of the expected problem> <name>
	_case() {
		local out rc=0
		out="$(CHANGELOG_TAGS="${TAGS:-}" check_root "$tmp/$1" 2>/dev/null)" || rc=$?
		if [ "$2" = ok ]; then _eq "$rc:$out" "0:" "$3"
		else
			case "$out" in *"$2"*) _eq "$rc" "1" "$3" ;;
				*) _eq "$out" "*$2*" "$3" ;; esac
		fi
	}
	local good=('# Changelog' '' '## [Unreleased]' '' '### Added' '' '- new (#9)' '' '## [1.0.1] - 2026-01-03' '' '### Fixed' '' '- one (#2)' '' '## [1.0.0] - 2026-01-01' '' '### Added' '' '- start (#1)' '' '### Fixed' '' '- first (#0)' '' '## [0.4.0-rc.1] - 2025-01-01' '' '### Docs' '' '### Docs')

	_fx good "${good[@]}"
	_frag good a.yaml 'kind: Fixed\nbody: |-\n  pending (#3)\ntime: 2026-01-04T00:00:00Z\n'
	TAGS=$'v1.0.0\nv1.0.1\nv1.0.0-rc.1\nv0.0.1' _case good ok "a well-formed changelog passes (legacy rc section and headings allowed below the floor)"

	# Rule 1.
	_fx r1a '# Changelog' '' '## [1.0.0] - 2026-01-01' '' '## [Unreleased]'
	_case r1a "is not the first section" "rule 1: [Unreleased] must come first"
	_fx r1b '# Changelog' '' '## [Unreleased]' '' '## [Unreleased]' '' '## [1.0.0] - 2026-01-01'
	_case r1b "a second ## [Unreleased]" "rule 1: one [Unreleased] only"
	_fx r1c '# Changelog' '' '## [1.0.0] - 2026-01-01'
	_case r1c "no ## [Unreleased] section" "rule 1: [Unreleased] must exist"
	# Rule 2.
	_fx r2a '# Changelog' '' '## [Unreleased]' '' '## [1.0.0]'
	_case r2a "not a section heading" "rule 2: a section without a date"
	_fx r2b '# Changelog' '' '## [Unreleased]' '' '## 1.0.0 - 2026-01-01'
	_case r2b "not a section heading" "rule 2: a version without brackets"
	_fx r2c '# Changelog' '' '## [Unreleased]' '' '## [1.0.0] - 2026-13-01'
	_case r2c "not a section heading" "rule 2: a month that does not exist"
	_fx r2d '# Changelog' '' '## [Unreleased]' '' '## [1.0.0-rc.1] - 2026-01-01'
	_case r2d "is a prerelease section" "rule 2: a new rc section is refused"
	# Rule 3.
	_fx r3a '# Changelog' '' '## [Unreleased]' '' '## [1.0.0] - 2026-01-01' '' '## [1.0.1] - 2026-01-03'
	_case r3a "is not older than [1.0.0]" "rule 3: ascending sections are refused"
	_fx r3b '# Changelog' '' '## [Unreleased]' '' '## [1.0.0] - 2026-01-01' '' '## [1.0.0] - 2026-01-01'
	_case r3b "[1.0.0] appears twice" "rule 3: a duplicated section is refused"
	_fx r3c '# Changelog' '' '## [Unreleased]' '' '## [0.5.10] - 2026-01-02' '' '## [0.5.9] - 2026-01-01'
	_case r3c ok "rule 3: versions compare as numbers"
	# Rule 4.
	_fx r4a '# Changelog' '' '## [Unreleased]' '' '### Notes' '' '- n' '' '## [1.0.0] - 2026-01-01'
	_case r4a "### Notes is not a kind" "rule 4: an unknown kind in [Unreleased]"
	_fx r4b '# Changelog' '' '## [Unreleased]' '' '## [1.0.0] - 2026-01-01' '' '### Fixed' '' '- a' '' '### Fixed' '' '- b'
	_case r4b "### Fixed appears twice" "rule 4: a repeated kind"
	_fx r4c '# Changelog' '' '## [Unreleased]' '' '## [1.0.0] - 2026-01-01' '' '### Fixed' '' '- a' '' '### Added' '' '- b'
	_case r4c "### Added is out of the declared order" "rule 4: kinds out of order"
	_fx r4d '# Changelog' '' '## [Unreleased]' '' '### Added' '' '## [1.0.0] - 2026-01-01' '' '### Fixed' '' '- a'
	_case r4d "[Unreleased] ### Added is empty" "rule 4: an empty kind"
	_fx r4e '# Changelog' '' '## [Unreleased]' '' '## [1.0.0] - 2026-01-01' '' '### Fixed' '' '[1.0.0]: https://example.invalid'
	_case r4e "[1.0.0] ### Fixed is empty" "rule 4: a kind followed only by link references is empty"
	# Rule 5.
	_fx r5 "${good[@]}"
	TAGS=$'v1.0.0\nv1.0.1\nv1.0.2' _case r5 ok "rule 5: the newest tag of a minor may wait for its sync PR"
	TAGS=$'v1.0.0\nv1.0.1\nv1.0.2\nv1.0.3' _case r5 "v1.0.2 is tagged but has no section" "rule 5: an older tag without its section is refused"
	TAGS=$'v1.0.0\nv1.0.1\nv2.0.0\nv2.0.1' _case r5 ok "rule 5: tags of a minor this file does not record are ignored (a release branch)"
	# Rule 6: fragment YAML.
	_fx r6 "${good[@]}"
	_frag r6 bad.yaml 'kind: Fixed\nbody: "unterminated\ntime: x\n'
	_case r6 "bad.yaml: does not parse as YAML" "rule 6: a fragment that does not parse"
	_fx r6b "${good[@]}"; _frag r6b k.yaml 'kind: Bugfix\nbody: x\ntime: 2026-01-01T00:00:00Z\n'
	_case r6b "kind 'Bugfix' is not one .changie.yaml declares" "rule 6: an undeclared kind"
	_fx r6c "${good[@]}"; _frag r6c e.yaml 'kind: Fixed\nbody: "   "\ntime: 2026-01-01T00:00:00Z\n'
	_case r6c "e.yaml: body is empty" "rule 6: an empty body"
	_fx r6d "${good[@]}"; _frag r6d t.yaml 'body: x\ntime: 2026-01-01T00:00:00Z\n'
	_case r6d "t.yaml: has no kind: field" "rule 6: a missing kind"
	_fx r6h "${good[@]}"; _frag r6h nb.yaml 'kind: Fixed\ntime: 2026-01-01T00:00:00Z\n'
	_case r6h "nb.yaml: has no body: field" "rule 6: a missing body"
	_fx r6i "${good[@]}"; _frag r6i nt.yaml 'kind: Fixed\nbody: no time is fine\n'
	_case r6i ok "rule 6: time is optional"
	_fx r6e "${good[@]}"; _frag r6e l.yaml -- '- kind: Fixed\n'
	_case r6e "l.yaml: is not a mapping" "rule 6: a fragment that is not a mapping"
	# Rule 6: a shipped fragment.
	_fx r6f "${good[@]}"; _frag r6f s.yaml 'kind: Fixed\nbody: |-\n  one\n  (#2)\ntime: 2026-01-01T00:00:00Z\n'
	_case r6f "s.yaml: already an entry of a dated section" "rule 6: a fragment that already shipped"
	_fx r6g "${good[@]}"; _frag r6g n.yaml 'kind: Added\nbody: new (#9)\ntime: 2026-01-01T00:00:00Z\n'
	_case r6g ok "rule 6: text in [Unreleased] is not a shipped entry"

	# The entry point, with its exit status, on a fixture root.
	local rc=0
	CHANGELOG_TAGS="" bash "$ROOT/scripts/check-changelog-structure.sh" --root "$tmp/r3b" >/dev/null 2>&1 || rc=$?
	_eq "$rc" "1" "the script exits 1 on a bad changelog"
	rc=0
	CHANGELOG_TAGS="" bash "$ROOT/scripts/check-changelog-structure.sh" --root "$tmp/good" >/dev/null 2>&1 || rc=$?
	_eq "$rc" "0" "and 0 on a good one"

	rm -rf "$tmp"
	if [ "$fail" = 0 ]; then echo "self-test: PASS"; return 0; else echo "self-test: FAIL"; return 1; fi
}

main() {
	local root="$ROOT"
	case "${1:-}" in
		--self-test) self_test; exit $? ;;
		--root) root="${2:?--root needs a directory}" ;;
		"") ;;
		*) echo "usage: check-changelog-structure.sh [--root <dir>] | --self-test" >&2; exit 2 ;;
	esac
	local out rc=0
	out="$(check_root "$root")" || rc=$?
	if [ "$rc" != 0 ]; then
		echo "CHANGELOG structure: FAIL" >&2
		printf '%s\n' "$out" | sed 's/^/  /' >&2
		exit 1
	fi
	echo "CHANGELOG structure: OK"
}

main "$@"
