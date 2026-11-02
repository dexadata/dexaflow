#!/usr/bin/env bash
# Mutation testing (ADR 0101): runs gremlins on a package, checks its efficacy
# against the floor in .github/mutation/targets.txt, and lists the surviving
# mutants that .github/mutation/accepted.txt does not accept.
#
# Usage:
#   scripts/mutation.sh run <package dir>...   full run of each package; fails
#                                              when one is below its floor
#   scripts/mutation.sh diff <base ref>        only the lines changed since the
#                                              merge base with <base ref>, in
#                                              the tier 1 packages the change
#                                              touches; lists the survivors
#   scripts/mutation.sh packages <tier>        the package dirs of a tier
#   scripts/mutation.sh canary                 checks that gremlins tells a
#                                              test that asserts from one that
#                                              does not (run and diff do this
#                                              first)
#   scripts/mutation.sh integration-coverage <coverprofile>
#                                              the coverage an integration run
#                                              gives the files the unit floor
#                                              excludes
#   scripts/mutation.sh --self-test
#
# The report goes to stdout and, in CI, to the job summary. The gremlins JSON
# of each package is kept under $MUTATION_OUT (default: .mutation/).
#
# MUTATION_STRICT=1 makes `diff` fail when a changed line has an unaccepted
# survivor. Without it `diff` only reports: the pull request job is advisory.
set -euo pipefail
# comm needs both sides sorted the same way.
export LC_ALL=C

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TARGETS="${MUTATION_TARGETS:-$ROOT/.github/mutation/targets.txt}"
ACCEPTED="${MUTATION_ACCEPTED:-$ROOT/.github/mutation/accepted.txt}"
OUT="${MUTATION_OUT:-$ROOT/.mutation}"
GREMLINS="${GREMLINS:-gremlins}"

# The packages scripts/coverage-gate.sh excludes from the floor are out of
# scope here too: generated code, main packages, version, and the harnesses.
# Its file-level exclusions (the database and process bindings) are passed to
# gremlins as --exclude-files below.
OUT_OF_SCOPE='^(cmd|test)/|^internal/version$|^internal/storage/queries$|/testdata(/|$)'

# targets_lines: the package lines of the targets file, comments and blanks
# dropped, as "<dir> <tier> <floor>".
targets_lines() {
	sed -e 's/#.*//' "$TARGETS" | awk 'NF >= 3 { print $1, $2, $3 }'
}

# floor_of <dir>: the floor of a package, "-" when it has none.
floor_of() {
	targets_lines | awk -v d="$1" '$1 == d { print $3; found = 1 } END { if (!found) print "-" }'
}

tier1_packages() {
	targets_lines | awk '$2 == 1 { print $1 }'
}

# tier2_packages: every top-level dir under internal/ and pkg/ with non-test
# Go code, minus tier 1 and what is out of scope. gremlins walks a dir with
# its subdirs, so a subpackage is run with its top-level parent.
tier2_packages() {
	local t1
	t1=$(tier1_packages)
	(cd "$ROOT" && git ls-files 'internal/*.go' 'pkg/*.go') |
		grep -v '_test\.go$' | grep -vE '\.(pb|gen)\.go$' |
		sed -E 's|^((internal\|pkg)/[^/]+)/.*|\1|' | sort -u |
		grep -vE "$OUT_OF_SCOPE" |
		while read -r d; do
			printf '%s\n' "$t1" | grep -qxF "$d" && continue
			printf '%s\n' "$d"
		done
}

# exclude_args <dir>: a gremlins --exclude-files pair for each file of the
# package that scripts/coverage-gate.sh leaves out of the floor (generated
# code, the database and process bindings only integration can exercise; ADR
# 0101 section 6). The list is read from that script, so the two never drift.
exclude_args() {
	local dir="$1" re f
	re=$(sed -n "s/^EXCLUDE='\(.*\)'\$/\1/p" "$ROOT/scripts/coverage-gate.sh")
	[ -n "$re" ] || { echo "mutation.sh: no EXCLUDE in scripts/coverage-gate.sh" >&2; return 1; }
	(cd "$ROOT" && git ls-files "$dir/*.go") | grep -v '_test\.go$' |
		while read -r f; do
			if printf '/%s:\n' "$f" | grep -qE "$re"; then
				printf -- '--exclude-files\n^%s$\n' "$(printf '%s' "${f#"$dir"/}" | sed 's/\./\\./g')"
			fi
		done
}

# survivors <dir> <json>: one "<dir>/<file>:<line>:<column> <MUTATOR>" line per
# lived mutant of a gremlins report.
survivors() {
	jq -r --arg d "$1" '.files[]? | .file_name as $f | .mutations[]? | select(.status == "LIVED") | "\($d)/\($f):\(.line):\(.column) \(.type)"' "$2" | sort
}

# accepted_keys: the "<file>:<line>:<column> <MUTATOR>" of every accepted mutant.
accepted_keys() {
	[ -f "$ACCEPTED" ] || return 0
	sed -e 's/#.*//' "$ACCEPTED" | awk 'NF >= 2 { print $1, $2 }' | sort -u
}

# evaluate <dir> <json>: prints "<killed> <lived> <accepted> <efficacy> <mcover>"
# where efficacy leaves the accepted survivors out.
evaluate() {
	local killed lived total notcov acc eff mcov
	killed=$(jq -r '.mutants_killed // 0' "$2")
	lived=$(jq -r '.mutants_lived // 0' "$2")
	total=$(jq -r '.mutants_total // 0' "$2")
	notcov=$(jq -r '.mutants_not_covered // 0' "$2")
	acc=$(comm -12 <(survivors "$1" "$2") <(accepted_keys) | wc -l | tr -d ' ')
	eff=$(awk -v k="$killed" -v l="$lived" -v a="$acc" 'BEGIN { d = k + l - a; printf "%.2f", (d > 0 ? 100 * k / d : 100) }')
	mcov=$(awk -v k="$killed" -v l="$lived" -v n="$notcov" 'BEGIN { d = k + l + n; printf "%.2f", (d > 0 ? 100 * (k + l) / d : 100) }')
	: "$total"
	printf '%s %s %s %s %s\n' "$killed" "$lived" "$acc" "$eff" "$mcov"
}

# below_floor <efficacy> <floor>: true when the floor is a number above it.
below_floor() {
	[ "$2" != "-" ] && awk -v e="$1" -v f="$2" 'BEGIN { exit !(e < f) }'
}

# report_package <dir> <json>: one Markdown section; returns 1 below the floor.
report_package() {
	local dir="$1" json="$2" floor k l a eff mcov verdict rc=0 unaccepted stale
	floor=$(floor_of "$dir")
	read -r k l a eff mcov <<<"$(evaluate "$dir" "$json")"
	if [ "$floor" = "-" ]; then
		verdict="no floor yet"
	elif below_floor "$eff" "$floor"; then
		verdict="**below the floor of ${floor}%**"
		rc=1
	else
		verdict="floor ${floor}%"
		awk -v e="$eff" -v f="$floor" 'BEGIN { exit !(e >= f + 1) }' &&
			verdict="$verdict, raise it to $(awk -v e="$eff" 'BEGIN { printf "%d", e }')"
	fi
	printf '### `%s`\n\n' "$dir"
	printf 'Efficacy **%s%%** (%s), mutant coverage %s%%: %s killed, %s lived, %s of them accepted.\n\n' \
		"$eff" "$verdict" "$mcov" "$k" "$l" "$a"
	unaccepted=$(comm -23 <(survivors "$dir" "$json") <(accepted_keys))
	if [ -n "$unaccepted" ]; then
		printf '<details><summary>Survivors not accepted (%s)</summary>\n\n```\n%s\n```\n\n</details>\n\n' \
			"$(printf '%s\n' "$unaccepted" | wc -l | tr -d ' ')" "$unaccepted"
	fi
	stale=$(comm -13 <(survivors "$dir" "$json") <(accepted_keys | awk -v d="$dir/" 'index($1, d) == 1'))
	if [ -n "$stale" ]; then
		printf 'Accepted in `.github/mutation/accepted.txt` but no longer a survivor here (the line moved, or a test kills it now); delete or update:\n\n```\n%s\n```\n\n' "$stale"
	fi
	return "$rc"
}

# run_gremlins <dir> <json> [extra args]: gremlins from inside the package
# dir. Its file names are relative to the package, and so is the diff it
# reads with diff.relative: run from the root, the two never match and a
# --diff run skips every mutant.
run_gremlins() {
	local dir="$1" json="$2"
	shift 2
	local excl=()
	mapfile -t excl < <(exclude_args "$dir")
	(cd "$ROOT/$dir" &&
		GOFLAGS="${GOFLAGS:+$GOFLAGS }-count=1" \
			GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0=diff.relative GIT_CONFIG_VALUE_0=true \
			"$GREMLINS" unleash --config "$ROOT/.gremlins.yaml" --silent \
			"${excl[@]}" -o "$json" "$@" "$ROOT/$dir")
}

# canary: proves the tool and its configuration can tell a test that checks
# from one that does not, before any number is trusted. A throwaway module
# holds two packages with the same function: one test asserts nothing (its
# mutants must live), the other asserts the boundary (its mutants must die).
# A misconfiguration that fails every test run reads as 100% everywhere
# otherwise: gremlins v0.6.0 with test-cpu set did exactly that.
canary() {
	local tmp weak strong rc=0
	tmp=$(mktemp -d)
	printf 'module canary\n\ngo 1.22\n' >"$tmp/go.mod"
	mkdir -p "$tmp/weak" "$tmp/strong"
	printf 'package weak\n\nfunc Positive(a int) bool { return a > 0 }\n' >"$tmp/weak/f.go"
	printf 'package weak\n\nimport "testing"\n\nfunc TestPositive(t *testing.T) { _ = Positive(5) }\n' >"$tmp/weak/f_test.go"
	printf 'package strong\n\nfunc Positive(a int) bool { return a > 0 }\n' >"$tmp/strong/f.go"
	printf 'package strong\n\nimport "testing"\n\nfunc TestPositive(t *testing.T) {\n\tif !Positive(1) || Positive(0) {\n\t\tt.Fatal("boundary")\n\t}\n}\n' >"$tmp/strong/f_test.go"
	(cd "$tmp" && git init -q . 2>/dev/null) || true
	for p in weak strong; do
		(cd "$tmp/$p" && GOFLAGS="${GOFLAGS:+$GOFLAGS }-count=1" "$GREMLINS" unleash --config "$ROOT/.gremlins.yaml" --silent -o "$tmp/$p.json" "$tmp/$p") >/dev/null 2>&1 || true
	done
	weak=$(jq -r '"\(.mutants_killed // 0) \(.mutants_lived // 0)"' "$tmp/weak.json" 2>/dev/null || echo "? ?")
	strong=$(jq -r '"\(.mutants_killed // 0) \(.mutants_lived // 0)"' "$tmp/strong.json" 2>/dev/null || echo "? ?")
	case "$weak" in "0 "[1-9]*) ;; *) rc=1 ;; esac
	case "$strong" in [1-9]*" 0") ;; *) rc=1 ;; esac
	rm -rf "$tmp"
	if [ "$rc" -ne 0 ]; then
		echo "::error::mutation canary failed (killed lived): a test that asserts nothing got '$weak', one that asserts got '$strong'. gremlins or .gremlins.yaml cannot tell them apart, so no result of this run would mean anything." >&2
		return 1
	fi
	echo "mutation canary: ok (weak test: $weak killed/lived, strong test: $strong)"
}

summary_out() {
	if [ -n "${GITHUB_STEP_SUMMARY:-}" ]; then tee -a "$GITHUB_STEP_SUMMARY"; else cat; fi
}

cmd_run() {
	local dir json rc=0
	canary
	mkdir -p "$OUT"
	{ printf '## Mutation testing\n\n'; } | summary_out
	for dir in "$@"; do
		dir="${dir#./}"
		dir="${dir%/}"
		json="$OUT/$(printf '%s' "$dir" | tr '/' '_').json"
		if ! run_gremlins "$dir" "$json"; then
			printf '### `%s`\n\ngremlins failed; see the log above.\n\n' "$dir" | summary_out
			rc=1
			continue
		fi
		if ! report_package "$dir" "$json" | summary_out; then rc=1; fi
	done
	return "$rc"
}

# changed_tier1 <base ref>: the tier 1 packages with non-test Go files changed
# since the merge base.
changed_tier1() {
	local files
	files=$(cd "$ROOT" && git diff --name-only --merge-base "$1" -- '*.go' | grep -v '_test\.go$' || true)
	[ -n "$files" ] || return 0
	tier1_packages | while read -r p; do
		printf '%s\n' "$files" | grep -q "^$p/" && printf '%s\n' "$p"
	done
	return 0
}

cmd_diff() {
	local base="$1" pkgs dir json found=0 unaccepted
	pkgs=$(changed_tier1 "$base")
	[ -z "$pkgs" ] || canary
	mkdir -p "$OUT"
	{
		printf '## Mutation testing of the changed lines\n\n'
		if [ -z "$pkgs" ]; then
			printf 'No tier 1 package changed against `%s`.\n' "$base"
		fi
	} | summary_out
	[ -n "$pkgs" ] || return 0
	for dir in $pkgs; do
		json="$OUT/diff_$(printf '%s' "$dir" | tr '/' '_').json"
		run_gremlins "$dir" "$json" --diff "$base" || {
			printf '### `%s`\n\ngremlins failed; see the log above.\n\n' "$dir" | summary_out
			found=1
			continue
		}
		unaccepted=$(comm -23 <(survivors "$dir" "$json") <(accepted_keys))
		{
			printf '### `%s`\n\n%s killed, %s lived on the changed lines.\n\n' "$dir" \
				"$(jq -r '.mutants_killed // 0' "$json")" "$(jq -r '.mutants_lived // 0' "$json")"
			if [ -n "$unaccepted" ]; then
				printf 'Each survivor is a changed line no test checks: kill it with an assertion, or accept it in `.github/mutation/accepted.txt` with the reason.\n\n```\n%s\n```\n\n' "$unaccepted"
			fi
		} | summary_out
		[ -z "$unaccepted" ] || found=1
	done
	if [ "$found" -ne 0 ] && [ "${MUTATION_STRICT:-0}" = 1 ]; then
		return 1
	fi
	return 0
}

# cmd_integration_coverage <coverprofile>: the statement coverage the
# integration suite gives each file the unit floor excludes as a database or
# process binding (ADR 0101 section 6), lowest first. Generated code, main
# packages and harnesses are left out: nothing is expected to cover them.
cmd_integration_coverage() {
	local re rows
	re=$(sed -n "s/^EXCLUDE='\(.*\)'\$/\1/p" "$ROOT/scripts/coverage-gate.sh")
	rows=$(binding_stats "$1" "$re")
	{
		printf '## Integration coverage of the database and process bindings

'
		if [ -z "$rows" ]; then
			printf 'The profile has none of the files the unit floor excludes.
'
		else
			printf '| File | Coverage | Statements |\n|---|---:|---:|\n%s\n' "$rows"
		fi
	} | summary_out
}

# binding_stats <coverprofile> <exclude regexp>: one Markdown row per binding
# file, lowest coverage first. A block listed more than once counts once.
binding_stats() {
	awk 'NR == 1 && /^mode:/ { next } { print }' "$1" |
		awk -v re="$2" '
			{
				split($1, loc, ":"); file = loc[1]
				short = file; sub(/^[^\/]*\/[^\/]*\/[^\/]*\//, "", short)
				if (("/" short ":") !~ re) next
				if (short ~ /(\.pb\.go|\.gen\.go)$|^cmd\/|^test\/|^internal\/version\/|^internal\/storage\/queries\//) next
				if (!($1 in seen)) { seen[$1] = 1; stmts[short] += $2 }
				if ($3 > 0 && !($1 in hit)) { hit[$1] = 1; covered[short] += $2 }
			}
			END { for (f in stmts) printf "%.1f\t| `%s` | %.1f%% | %d / %d |\n", 100 * covered[f] / stmts[f], f, 100 * covered[f] / stmts[f], covered[f], stmts[f] }
		' | sort -n -k1,1 | cut -f2-
}

self_test() {
	local tmp fail=0 out
	tmp=$(mktemp -d)
	trap 'rm -rf "$tmp"' RETURN
	cat >"$tmp/targets.txt" <<'EOF'
# comment
internal/a  1  80   # trailing comment
internal/b  1  -
internal/c  2  65
EOF
	cat >"$tmp/accepted.txt" <<'EOF'
# header
internal/a/a.go:10:4 CONDITIONALS_BOUNDARY never equal
internal/a/gone.go:1:1 ARITHMETIC_BASE stale entry
EOF
	cat >"$tmp/a.json" <<'EOF'
{"mutants_killed":8,"mutants_lived":2,"mutants_not_covered":0,"mutants_total":10,
 "files":[{"file_name":"a.go","mutations":[
  {"type":"CONDITIONALS_BOUNDARY","status":"LIVED","line":10,"column":4},
  {"type":"CONDITIONALS_NEGATION","status":"LIVED","line":12,"column":7},
  {"type":"CONDITIONALS_NEGATION","status":"KILLED","line":13,"column":7}]}]}
EOF
	cat >"$tmp/low.json" <<'EOF'
{"mutants_killed":6,"mutants_lived":4,"mutants_not_covered":10,"mutants_total":20,"files":[]}
EOF
	TARGETS="$tmp/targets.txt" ACCEPTED="$tmp/accepted.txt"
	_is() { # <name> <got> <want>
		if [ "$2" = "$3" ]; then printf '  ok   %s\n' "$1"; else
			printf '  FAIL %s: got %q, want %q\n' "$1" "$2" "$3"
			fail=1
		fi
	}
	_has() { # <name> <haystack> <needle>
		case "$2" in *"$3"*) printf '  ok   %s\n' "$1" ;; *)
			printf '  FAIL %s: want %q in:\n%s\n' "$1" "$3" "$2"
			fail=1
			;;
		esac
	}
	_is "a floor is read past a trailing comment" "$(floor_of internal/a)" "80"
	_is "a dash is no floor" "$(floor_of internal/b)" "-"
	_is "an unlisted package has no floor" "$(floor_of internal/z)" "-"
	_is "tier 1 lists its packages" "$(tier1_packages | tr '\n' ' ')" "internal/a internal/b "
	_is "survivors are named from the root" "$(survivors internal/a "$tmp/a.json" | tr '\n' ',')" \
		"internal/a/a.go:10:4 CONDITIONALS_BOUNDARY,internal/a/a.go:12:7 CONDITIONALS_NEGATION,"
	# 8 killed of 10 reached, one survivor accepted: 8 / 9.
	_is "an accepted survivor leaves the efficacy" "$(evaluate internal/a "$tmp/a.json")" "8 2 1 88.89 100.00"
	_is "mutant coverage counts the uncovered" "$(evaluate internal/b "$tmp/low.json")" "6 4 0 60.00 50.00"
	out=$(report_package internal/a "$tmp/a.json") && rc=0 || rc=$?
	_is "above the floor passes" "$rc" "0"
	_has "the report names the efficacy and floor" "$out" 'Efficacy **88.89%** (floor 80%, raise it to 88)'
	_has "the report lists the unaccepted survivor" "$out" 'internal/a/a.go:12:7 CONDITIONALS_NEGATION'
	case "$out" in *"a.go:10:4 "*) echo "  FAIL an accepted survivor is listed"; fail=1 ;; *) echo "  ok   an accepted survivor is not listed" ;; esac
	_has "an accepted entry that no longer survives is reported" "$out" "$(printf 'delete or update:\n\n```\ninternal/a/gone.go:1:1 ARITHMETIC_BASE')"
	cp "$tmp/low.json" "$tmp/c.json"
	out=$(report_package internal/c "$tmp/c.json") && rc=0 || rc=$?
	_is "below the floor fails" "$rc" "1"
	_has "below the floor is said" "$out" 'below the floor of 65%'
	out=$(report_package internal/b "$tmp/low.json") && rc=0 || rc=$?
	_is "no floor never fails" "$rc" "0"
	_has "no floor is said" "$out" '(no floor yet)'
	if below_floor 79.99 80; then echo "  ok   79.99 is below 80"; else echo "  FAIL 79.99 is below 80"; fail=1; fi
	if below_floor 80.00 80; then echo "  FAIL 80.00 is not below 80"; fail=1; else echo "  ok   80.00 is not below 80"; fi
	out=$(exclude_args internal/storage | tr '\n' ' ')
	_has "a file the coverage floor excludes is excluded" "$out" '--exclude-files ^repository\.go$ '
	case "$out" in *"^dag_versions\.go$"*) echo "  FAIL a file the floor counts is excluded"; fail=1 ;; *) echo "  ok   a file the floor counts is not excluded" ;; esac
	out=$(exclude_args internal/scheduler | tr '\n' ' ')
	_is "only the excluded files of a package" "$out" '--exclude-files ^leader\.go$ '
	printf '%s\n' 'mode: atomic' \
		'github.com/x/y/internal/storage/repository.go:1.1,2.2 3 1' \
		'github.com/x/y/internal/storage/repository.go:3.1,4.2 1 0' \
		'github.com/x/y/internal/storage/repository.go:3.1,4.2 1 2' \
		'github.com/x/y/internal/storage/dag_versions.go:1.1,2.2 5 0' \
		'github.com/x/y/internal/scheduler/leader.go:1.1,2.2 2 0' \
		'github.com/x/y/internal/storage/queries/q.go:1.1,2.2 2 0' >"$tmp/int.out"
	out=$(binding_stats "$tmp/int.out" '(/internal/storage/repository\.go:|/internal/scheduler/leader\.go:|/internal/storage/queries/)')
	_is "bindings are listed lowest first, generated code left out" "$out" \
		"$(printf '%s\n' '| `internal/scheduler/leader.go` | 0.0% | 0 / 2 |' '| `internal/storage/repository.go` | 100.0% | 4 / 4 |')"
	if [ "$fail" -eq 0 ]; then
		echo "mutation self-test: ok"
		return 0
	fi
	echo "mutation self-test: FAIL"
	return 1
}

case "${1:-}" in
--self-test) self_test ;;
run)
	shift
	[ $# -gt 0 ] || { echo "usage: $0 run <package dir>..." >&2; exit 2; }
	cmd_run "$@"
	;;
diff)
	[ $# -eq 2 ] || { echo "usage: $0 diff <base ref>" >&2; exit 2; }
	cmd_diff "$2"
	;;
canary) canary ;;
integration-coverage)
	[ $# -eq 2 ] || { echo "usage: $0 integration-coverage <coverprofile>" >&2; exit 2; }
	cmd_integration_coverage "$2"
	;;
packages)
	case "${2:-}" in
	1) tier1_packages ;;
	2) tier2_packages ;;
	*) echo "usage: $0 packages <1|2>" >&2; exit 2 ;;
	esac
	;;
*)
	sed -n '2,20p' "$0" >&2
	exit 2
	;;
esac
