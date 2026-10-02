#!/usr/bin/env bash
# Keep the migrate CLI we SHIP and the migrate CLI we SCAN the same binary.
#
# `deploy/Dockerfile.migrate` compiles golang-migrate's own `cmd/migrate` from
# the version in our go.mod (#1039), and `.github/workflows/security.yaml` runs
# `govulncheck` over that same package so the binary is inside the module graph
# CI actually reads. Both ends of that chain are needed, and neither proves the
# other:
#
#   - golang-migrate registers database drivers behind BUILD TAGS. A binary
#     built with `-tags 'postgres pgx5'` and analysed with no tags is analysed
#     without the drivers it ships — govulncheck reports clean on a package that
#     is not the one in the image, and reports it in a green check. The reverse
#     drift (a tag added to the scan and not to the build) is quieter still.
#   - Trivy keys `stdlib` findings to the Go toolchain recorded in the binary's
#     build info. If the Dockerfile's GO_VERSION and the workflow's diverge, the
#     blocking image gate scans a toolchain the release never builds with.
#
# The failure mode both share is silence: everything stays green while the
# coverage stops meaning anything. That is the exact shape of the gap #1039
# closed — a migrate binary nobody was watching — so it gets a gate rather than
# a comment asking the next reader to keep two files in sync.
#
# Usage:
#   scripts/check-migrate-cli-build-tags.sh
#   scripts/check-migrate-cli-build-tags.sh --self-test
set -euo pipefail

DOCKERFILE="deploy/Dockerfile.migrate"
WORKFLOW=".github/workflows/security.yaml"
PKG="github.com/golang-migrate/migrate/v4/cmd/migrate"

# ── Pure parsers, so --self-test can drive them without the real files ───────

# dockerfile_build_line reads a Dockerfile on stdin and prints the LOGICAL line
# that runs `go build`: comment lines dropped, backslash continuations joined.
#
# Everything below parses that string rather than the file, and the difference
# is the whole point. An earlier version grepped the file, which this same
# Dockerfile's prose then satisfied on its own: the header explains the build in
# English, quoting `-tags 'postgres pgx5'`, `go list -m` and the package path, so
# a reviewer's mutation to the actual RUN line left every assertion green. A gate
# that its own documentation satisfies is worse than no gate — it is a green
# check that means nothing, which is the exact failure #1039 was about.
dockerfile_build_line() {
	sed '/^[[:space:]]*#/d' | awk '
		{ if (buf != "") { buf = buf " " $0 } else { buf = $0 } }
		buf ~ /\\[[:space:]]*$/ { sub(/\\[[:space:]]*$/, "", buf); next }
		{ print buf; buf = "" }
		END { if (buf != "") print buf }
	' | grep -F 'go build' | head -1
}

# dockerfile_tags prints the build tags of the `go build` line, normalised to a
# sorted, comma-separated set. Accepts the space-separated `-tags 'a b'`
# spelling the Dockerfile uses.
dockerfile_tags() {
	dockerfile_build_line | sed -n "s/.*-tags[ =]*'\([^']*\)'.*/\1/p" | tr ' ,' '\n' |
		sed '/^$/d' | LC_ALL=C sort -u | paste -sd, -
}

# dockerfile_builds_pkg exits 0 when the build line's output package is $1.
# Scoped to the build line for the same reason as the tags.
dockerfile_builds_pkg() { # <pkg>
	dockerfile_build_line | grep -qF -- "$1"
}

# dockerfile_reads_version exits 0 when the build line derives the stamped
# version from the module graph rather than carrying a literal.
dockerfile_reads_version() {
	dockerfile_build_line | grep -qF 'go list -m'
}

# workflow_tags reads a workflow on stdin and prints the tags passed to
# govulncheck, normalised the same way. Accepts `-tags a,b` and `-tags=a,b`.
workflow_tags() {
	sed '/^[[:space:]]*#/d' | grep -o 'govulncheck[^|]*-tags[ =][^ ]*' |
		sed -n 's/.*-tags[ =]\([^ ]*\).*/\1/p' |
		head -1 | tr ' ,' '\n' | sed '/^$/d' | LC_ALL=C sort -u | paste -sd, -
}

# workflow_scans_migrate_image exits 0 when the workflow still contains a Trivy
# step whose image-ref is the migrate image, built from the migrate Dockerfile.
#
# This is the assertion with the most at stake and it was the one missing.
# image-scan.yaml no longer covers leoflow-migrate at all — #1039 moved it out of
# the report-only lane — so the job in security.yaml is the ONLY scan of this
# image anywhere. Deleting it leaves every other gate here green and the image
# that holds the database DSN completely unscanned: the same "coverage deleted
# while claiming to strengthen it" shape the move was meant to avoid.
workflow_scans_migrate_image() {
	local body; body=$(sed '/^[[:space:]]*#/d')
	printf '%s' "$body" | grep -qE 'image-ref:[[:space:]]*leoflow-migrate' &&
		printf '%s' "$body" | grep -qF 'deploy/Dockerfile.migrate'
}

# dockerfile_go_version reads a Dockerfile on stdin and prints the default of
# its `ARG GO_VERSION=`.
dockerfile_go_version() {
	sed -n 's/^ARG GO_VERSION=\(.*\)$/\1/p' | head -1 | tr -d "\"'"
}

# workflow_go_version reads a workflow on stdin and prints its top-level
# `GO_VERSION:` env value.
workflow_go_version() {
	sed -n 's/^  GO_VERSION: *"\{0,1\}\([0-9.]*\)"\{0,1\} *$/\1/p' | head -1
}

self_test() {
	local fail=0
	_eq() { # <got> <want> <name>
		if [ "$1" = "$2" ]; then printf '  ok   %s\n' "$3"; else
			printf '  FAIL %s\n    got:  %q\n    want: %q\n' "$3" "$1" "$2"
			fail=1
		fi
	}

	local df
	df=$'ARG GO_VERSION=1.26.6\nFROM golang:${GO_VERSION}-bookworm AS build\nRUN go build -trimpath \\\n\t\t-tags '"'"'postgres pgx5'"'"' \\\n\t\t-o /out/migrate '"$PKG"'\n'
	_eq "$(printf '%s' "$df" | dockerfile_tags)" "pgx5,postgres" "Dockerfile tags parse and sort"
	_eq "$(printf '%s' "$df" | dockerfile_go_version)" "1.26.6" "Dockerfile GO_VERSION parses"

	local wf
	wf=$'env:\n  GO_VERSION: "1.26.6"\njobs:\n  x:\n    steps:\n      - run: govulncheck -tags postgres,pgx5 -show verbose '"$PKG"'\n'
	_eq "$(printf '%s' "$wf" | workflow_tags)" "pgx5,postgres" "workflow tags parse and sort"
	_eq "$(printf '%s' "$wf" | workflow_go_version)" "1.26.6" "workflow GO_VERSION parses"

	# `-tags=a,b` is the other spelling govulncheck accepts; a gate that only
	# understood one would pass by reading nothing.
	local wf2
	wf2=$'      - run: govulncheck -tags=pgx5,postgres '"$PKG"'\n'
	_eq "$(printf '%s' "$wf2" | workflow_tags)" "pgx5,postgres" "workflow -tags=a,b spelling"

	# Order must not matter: the sets are compared, not the strings.
	local df2
	df2=$'RUN go build -tags '"'"'pgx5 postgres'"'"' -o /out/migrate x\n'
	_eq "$(printf '%s' "$df2" | dockerfile_tags)" "$(printf '%s' "$df" | dockerfile_tags)" "tag order is irrelevant"

	# A Dockerfile with no -tags at all must yield the empty string rather than
	# silently matching an empty workflow parse — the caller treats empty as a
	# hard failure, and this pins that it can tell the difference.
	_eq "$(printf 'FROM scratch\n' | dockerfile_tags)" "" "no -tags yields empty"

	# ── The reviewer's mutations, as fixtures ────────────────────────────
	# Every case below was green against the first version of this gate, which
	# grepped whole files. The Dockerfile's own header quotes `-tags 'postgres
	# pgx5'`, `go list -m` and the package path in prose, so the documentation
	# satisfied the checks and the mutations could not fail.

	# A comment line quoting the tags must not out-vote the real build line.
	local decoy
	decoy=$'# example: -tags \'postgres pgx5\'\nRUN set -eux; \\\n\tgo build -tags \'pgx5\' -o /out/migrate '"$PKG"'\n'
	_eq "$(printf '%s' "$decoy" | dockerfile_tags)" "pgx5" "a decoy -tags comment does not out-vote the build line"

	# Prose naming the package must not satisfy the build-target assertion.
	local prose
	prose=$'# builds '"$PKG"$'\nRUN go build -o /out/migrate github.com/golang-migrate/migrate/v4/internal/cli\n'
	if printf '%s' "$prose" | dockerfile_builds_pkg "$PKG"; then
		printf '  FAIL %s\n' "a comment naming the package satisfies the build-target check"; fail=1
	else
		printf '  ok   %s\n' "prose naming the package does not satisfy the build-target check"
	fi

	# Prose quoting `go list -m` must not satisfy the version-derivation check.
	local hardcoded
	hardcoded=$'# the version is read with go list -m\nRUN version="v4.19.1"; go build -o /out/migrate x\n'
	if printf '%s' "$hardcoded" | dockerfile_reads_version; then
		printf '  FAIL %s\n' "a comment quoting 'go list -m' satisfies the version check"; fail=1
	else
		printf '  ok   %s\n' "prose quoting 'go list -m' does not satisfy the version check"
	fi
	# shellcheck disable=SC2016 # the fixture is a literal Dockerfile line; $( ) must NOT expand here.
	_eq "$(printf 'RUN v="$(go list -m x)"; go build -o /o/m x\n' | dockerfile_build_line | grep -c 'go list -m')" "1" "a real 'go list -m' on the build line is found"

	# The migrate image scan must be asserted to EXIST, not merely to agree.
	local wf_ok wf_gone
	wf_ok=$'      - uses: aquasecurity/trivy-action@v0\n        with:\n          image-ref: leoflow-migrate:ci\n      - run: docker build -f deploy/Dockerfile.migrate -t leoflow-migrate:ci .\n'
	wf_gone=$'      - run: echo nothing scans the migrate image any more\n'
	if printf '%s' "$wf_ok" | workflow_scans_migrate_image; then
		printf '  ok   %s\n' "a present migrate image scan is recognised"
	else
		printf '  FAIL %s\n' "a present migrate image scan is not recognised"; fail=1
	fi
	if printf '%s' "$wf_gone" | workflow_scans_migrate_image; then
		printf '  FAIL %s\n' "a deleted migrate image scan passes — the image would ship unscanned"; fail=1
	else
		printf '  ok   %s\n' "a deleted migrate image scan is caught"
	fi

	[ "$fail" -eq 0 ] && echo "self-test: all passed"
	return "$fail"
}

if [ "${1:-}" = "--self-test" ]; then
	self_test
	exit $?
fi

cd "$(dirname "$0")/.."

fail=0
note() {
	echo "FAIL: $*" >&2
	fail=1
}

[ -f "$DOCKERFILE" ] || { echo "FAIL: $DOCKERFILE not found" >&2; exit 1; }
[ -f "$WORKFLOW" ] || { echo "FAIL: $WORKFLOW not found" >&2; exit 1; }

# 1. The image must still build OUR binary. A `FROM migrate/migrate` would put
#    a third-party compiled binary back in the pod that holds the DSN, and every
#    other assertion here would go on passing while meaning nothing.
if grep -qE '^\s*FROM\s+migrate/migrate' "$DOCKERFILE"; then
	note "$DOCKERFILE is back on the third-party migrate/migrate image (#1039). The migrate binary must be compiled from the version in our go.mod so govulncheck can see it."
fi
if ! dockerfile_builds_pkg "$PKG" <"$DOCKERFILE"; then
	note "$DOCKERFILE's go build line does not build $PKG."
fi
if ! grep -qF "$PKG" "$WORKFLOW"; then
	note "$WORKFLOW does not run govulncheck over $PKG, so the shipped migrate binary is outside CI's module graph again (#1039)."
fi
if ! workflow_scans_migrate_image <"$WORKFLOW"; then
	note "$WORKFLOW no longer builds deploy/Dockerfile.migrate and scans it as leoflow-migrate. image-scan.yaml does not cover this image any more, so that job is the only scan of the container that holds the database DSN — without it the image ships unscanned and every other check here still passes."
fi

# 2. The version stamped into the binary must be READ from the module graph, not
#    written by hand — a hardcoded `-X main.Version=v4.19.1` survives a go.mod
#    bump and makes `migrate -version` lie about what is actually compiled.
if ! dockerfile_reads_version <"$DOCKERFILE"; then
	note "$DOCKERFILE's go build line no longer derives the stamped version with 'go list -m'; a hardcoded version drifts from go.mod silently."
fi

# 3. Build tags: same set on both ends.
df_tags="$(dockerfile_tags <"$DOCKERFILE")"
wf_tags="$(workflow_tags <"$WORKFLOW")"
if [ -z "$df_tags" ]; then
	note "could not read build tags from $DOCKERFILE. golang-migrate registers no database driver without one, so a tagless build fails at RUNTIME with 'unknown driver'."
elif [ -z "$wf_tags" ]; then
	note "could not read -tags from the govulncheck invocation in $WORKFLOW. Without them govulncheck analyses a driverless package, not the binary we ship."
elif [ "$df_tags" != "$wf_tags" ]; then
	note "build tags disagree: $DOCKERFILE builds with [$df_tags], $WORKFLOW scans with [$wf_tags]. govulncheck would be analysing a different binary than the one in the image."
fi

# 3b. Consistency is not correctness. Dropping `postgres` from BOTH ends
#     satisfies the comparison above and produces a binary with no driver for
#     the only database Dexaflow supports — `unknown driver postgres (forgotten
#     import?)` at Job runtime. helm-ci's kind install would catch it, several
#     minutes and one cluster later; this catches it in seconds. Dexaflow is
#     Postgres-only, so this is a fact about the product, not a preference.
case ",$df_tags," in
*,postgres,*) ;;
*) note "the build tags [$df_tags] do not include 'postgres'. golang-migrate registers its drivers behind build tags, so this binary would fail at runtime with \"unknown driver postgres\" — the migration Job would not start, and the tag comparison above cannot see it because both ends agree." ;;
esac

# 4. Go toolchain: same pin on both ends.
df_go="$(dockerfile_go_version <"$DOCKERFILE")"
wf_go="$(workflow_go_version <"$WORKFLOW")"
if [ -z "$df_go" ]; then
	note "$DOCKERFILE has no 'ARG GO_VERSION=' default. An unpinned toolchain moves this image's reported stdlib findings with no commit of ours."
elif [ -z "$wf_go" ]; then
	note "could not read GO_VERSION from $WORKFLOW."
elif [ "$df_go" != "$wf_go" ]; then
	note "Go toolchain pins disagree: $DOCKERFILE builds with $df_go, $WORKFLOW uses $wf_go. Trivy keys stdlib findings to the toolchain in the binary, so the gate would scan a build the release never produces."
fi

if [ "$fail" -ne 0 ]; then
	exit 1
fi

echo "OK: the shipped migrate CLI ($PKG, tags [$df_tags], go $df_go) is the one govulncheck and the image gate scan"
