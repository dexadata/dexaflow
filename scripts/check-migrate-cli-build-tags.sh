#!/usr/bin/env bash
# Keep the migrate binary we SHIP and the migrate binary we SCAN the same one.
#
# `deploy/Dockerfile.migrate` compiles the dexaflow-migrate wrapper
# (`./cmd/dexaflow-migrate`, #1020) from this module, and
# `.github/workflows/security.yaml` runs `govulncheck ./...`, which reaches it
# because it is one of our own packages. Before #1020 the image shipped
# golang-migrate's own `cmd/migrate` (#1039), a package outside `./...`, which
# security.yaml had to name by path and with the build tags that registered its
# drivers. The chain this gate guards is shorter now, and still silent when it
# breaks:
#
#   - The image must build OUR package. A `FROM migrate/migrate`, or a build
#     line pointed back at a dependency's main package, ships a binary
#     `govulncheck ./...` does not analyse, and every scan stays green.
#   - The build line must carry no `-tags`. The wrapper imports its pgx5 driver
#     (internal/migrator), so no tag is needed; a tag would compile files that
#     the untagged `govulncheck ./...` never reads.
#   - The wrapper must import golang-migrate's pgx5 driver. Without it the
#     binary builds and fails at RUNTIME with "unknown driver pgx5", which
#     helm-ci's kind install would catch several minutes and one cluster later.
#   - Trivy keys `stdlib` findings to the Go toolchain recorded in the binary's
#     build info. If the Dockerfile's GO_VERSION and the workflow's diverge, the
#     blocking image gate scans a toolchain the release never builds with.
#
# The script keeps its name, which CI and the docs reference, from the time
# build tags were the main thing it compared.
#
# Usage:
#   scripts/check-migrate-cli-build-tags.sh
#   scripts/check-migrate-cli-build-tags.sh --self-test
set -euo pipefail

DOCKERFILE="deploy/Dockerfile.migrate"
WORKFLOW=".github/workflows/security.yaml"
PKG="./cmd/dexaflow-migrate"
DRIVER_DIR="internal/migrator"
DRIVER_IMPORT='"github.com/golang-migrate/migrate/v4/database/pgx/v5"'

# ── Pure parsers, so --self-test can drive them without the real files ───────

# dockerfile_build_line reads a Dockerfile on stdin and prints the LOGICAL line
# that runs `go build`: comment lines dropped, backslash continuations joined.
#
# Everything below parses that string rather than the file. An earlier version
# grepped the file, which the Dockerfile's prose then satisfied on its own: its
# header explains the build in English, so a reviewer's mutation to the actual
# RUN line left every assertion green.
dockerfile_build_line() {
	sed '/^[[:space:]]*#/d' | awk '
		{ if (buf != "") { buf = buf " " $0 } else { buf = $0 } }
		buf ~ /\\[[:space:]]*$/ { sub(/\\[[:space:]]*$/, "", buf); next }
		{ print buf; buf = "" }
		END { if (buf != "") print buf }
	' | grep -F 'go build' | head -1
}

# dockerfile_tags prints the build tags of the `go build` line, normalised to a
# sorted, comma-separated set; empty when the line has none. Accepts
# `-tags 'a b'`, `-tags=a,b` and `-tags a`.
dockerfile_tags() {
	local line; line=$(dockerfile_build_line)
	case "$line" in *-tags*) ;; *) return 0 ;; esac
	local rest=${line#*-tags}
	rest=${rest#=}
	rest=${rest# }
	case "$rest" in
	\'*) rest=${rest#\'}; rest=${rest%%\'*} ;;
	*) rest=${rest%% *} ;;
	esac
	printf '%s\n' "$rest" | tr ' ,' '\n' | sed '/^$/d' | LC_ALL=C sort -u | paste -sd, -
}

# dockerfile_builds_pkg exits 0 when the build line's output package is $1.
dockerfile_builds_pkg() { # <pkg>
	local line; line=" $(dockerfile_build_line) "
	case "$line" in *" $1 "*) return 0 ;; esac
	return 1
}

# dockerfile_stamps_version exits 0 when the build line stamps
# internal/version, so `-version` names the build instead of "dev".
dockerfile_stamps_version() {
	dockerfile_build_line | grep -qF 'internal/version.version='
}

# workflow_scans_module exits 0 when a non-comment line of the workflow runs
# govulncheck over `./...`, which is what reaches ./cmd/dexaflow-migrate.
workflow_scans_module() {
	sed '/^[[:space:]]*#/d' | grep -qE 'govulncheck[^|#]*[[:space:]]\./\.\.\.([[:space:]]|$)'
}

# workflow_scans_migrate_image exits 0 when the workflow still contains a Trivy
# step whose image-ref is the migrate image, built from the migrate Dockerfile.
#
# image-scan.yaml does not cover this image (#1039 moved it out of the
# report-only lane), so the job in security.yaml is the ONLY scan of the
# container that holds the database DSN. Deleting it would leave every other
# assertion here green.
workflow_scans_migrate_image() {
	local body; body=$(sed '/^[[:space:]]*#/d')
	printf '%s' "$body" | grep -qE 'image-ref:[[:space:]]*leoflow-migrate' &&
		printf '%s' "$body" | grep -qF 'deploy/Dockerfile.migrate'
}

# go_imports_driver exits 0 when Go source on stdin imports the pgx5 driver on
# a line that is not a comment. It reads all of stdin (no `grep -q`): an early
# exit would kill the writer with SIGPIPE and fail the pipeline under pipefail.
go_imports_driver() {
	grep -vE '^[[:space:]]*//' | grep -F -- "$DRIVER_IMPORT" >/dev/null
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
	_yes() { # <name> <input> <fn> [args...]
		local name=$1 input=$2; shift 2
		if printf '%s' "$input" | "$@"; then printf '  ok   %s\n' "$name"; else printf '  FAIL %s\n' "$name"; fail=1; fi
	}
	_no() { # <name> <input> <fn> [args...]
		local name=$1 input=$2; shift 2
		if printf '%s' "$input" | "$@"; then printf '  FAIL %s\n' "$name"; fail=1; else printf '  ok   %s\n' "$name"; fi
	}

	local df
	# shellcheck disable=SC2016 # the fixture is a literal Dockerfile; ${VERSION} must NOT expand here.
	df=$'ARG GO_VERSION=1.26.6\nFROM golang:${GO_VERSION}-bookworm AS build\nRUN set -eux; \\\n\tgo build -trimpath \\\n\t\t-ldflags="-s -w -X github.com/dexadata/dexaflow/internal/version.version=${VERSION}" \\\n\t\t-o /out/dexaflow-migrate '"$PKG"$'\n'
	_eq "$(printf '%s' "$df" | dockerfile_tags)" "" "a build line without -tags yields no tags"
	_eq "$(printf '%s' "$df" | dockerfile_go_version)" "1.26.6" "Dockerfile GO_VERSION parses"
	_yes "the build line builds the wrapper" "$df" dockerfile_builds_pkg "$PKG"
	_yes "the build line stamps internal/version" "$df" dockerfile_stamps_version

	# A tag reintroduced on the build line must be seen, in every spelling.
	_eq "$(printf "RUN go build -tags 'postgres pgx5' -o /o/m %s\n" "$PKG" | dockerfile_tags)" "pgx5,postgres" "a quoted -tags list is read"
	_eq "$(printf 'RUN go build -tags=pgx5,postgres -o /o/m %s\n' "$PKG" | dockerfile_tags)" "pgx5,postgres" "a -tags=a,b spelling is read"
	_eq "$(printf 'RUN go build -tags pgx5 -o /o/m %s\n' "$PKG" | dockerfile_tags)" "pgx5" "a -tags a spelling is read"
	# A comment quoting tags must not out-vote the real build line.
	_eq "$(printf "# example: -tags 'postgres'\nRUN go build -o /o/m %s\n" "$PKG" | dockerfile_tags)" "" "a decoy -tags comment is ignored"

	# Prose must not satisfy the build-line assertions: the Dockerfile's header
	# names the package and the version stamp in English.
	local prose
	prose=$'# builds '"$PKG"$' and stamps internal/version.version=\nRUN go build -o /out/migrate github.com/golang-migrate/migrate/v4/cmd/migrate\n'
	_no "a comment naming the package does not satisfy the build-target check" "$prose" dockerfile_builds_pkg "$PKG"
	_no "a comment naming the stamp does not satisfy the version check" "$prose" dockerfile_stamps_version
	# A longer package path that merely starts with the wrapper's is not it.
	_no "./cmd/dexaflow-migrate-old is not ./cmd/dexaflow-migrate" $'RUN go build -o /o/m ./cmd/dexaflow-migrate-old\n' dockerfile_builds_pkg "$PKG"

	local wf
	wf=$'env:\n  GO_VERSION: "1.26.6"\njobs:\n  x:\n    steps:\n      - run: govulncheck -show verbose ./...\n'
	_eq "$(printf '%s' "$wf" | workflow_go_version)" "1.26.6" "workflow GO_VERSION parses"
	_yes "a module-wide govulncheck is recognised" "$wf" workflow_scans_module
	_no "a commented-out module-wide govulncheck is not" $'      # run: govulncheck ./...\n      - run: govulncheck ./internal/...\n' workflow_scans_module
	_no "a govulncheck narrowed to ./internal/... is not" $'      - run: govulncheck ./internal/...\n' workflow_scans_module

	# The migrate image scan must be asserted to EXIST, not merely to agree.
	_yes "a present migrate image scan is recognised" \
		$'      - uses: aquasecurity/trivy-action@v0\n        with:\n          image-ref: leoflow-migrate:ci\n      - run: docker build -f deploy/Dockerfile.migrate -t leoflow-migrate:ci .\n' \
		workflow_scans_migrate_image
	_no "a deleted migrate image scan is caught" $'      - run: echo nothing scans the migrate image any more\n' workflow_scans_migrate_image

	# The driver import, and a commented-out one.
	_yes "an imported pgx5 driver is recognised" "$(printf '\t_ %s // registers pgx5\n' "$DRIVER_IMPORT")" go_imports_driver
	_no "a commented-out pgx5 import is caught" "$(printf '\t// _ %s\n' "$DRIVER_IMPORT")" go_imports_driver

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

# 1. The image must build OUR binary.
if grep -qE '^\s*FROM\s+migrate/migrate' "$DOCKERFILE"; then
	note "$DOCKERFILE is back on the third-party migrate/migrate image (#1039). The migrate binary must be compiled from this module so govulncheck can see it."
fi
if ! dockerfile_builds_pkg "$PKG" <"$DOCKERFILE"; then
	note "$DOCKERFILE's go build line does not build $PKG (#1020). A binary built from another package is outside the 'govulncheck ./...' that $WORKFLOW runs."
fi
if ! workflow_scans_module <"$WORKFLOW"; then
	note "$WORKFLOW no longer runs 'govulncheck ./...', which is what analyses $PKG, the binary in the migrate image."
fi
if ! workflow_scans_migrate_image <"$WORKFLOW"; then
	note "$WORKFLOW no longer builds deploy/Dockerfile.migrate and scans it as leoflow-migrate. image-scan.yaml does not cover this image, so that job is the only scan of the container that holds the database DSN."
fi

# 2. The build must stamp the version, so `-version` names the build.
if ! dockerfile_stamps_version <"$DOCKERFILE"; then
	note "$DOCKERFILE's go build line no longer stamps internal/version.version; the image's -version would print \"dev\" whatever the build."
fi

# 3. No build tags: the driver is imported, and a tag would compile files the
#    untagged govulncheck never reads.
df_tags="$(dockerfile_tags <"$DOCKERFILE")"
if [ -n "$df_tags" ]; then
	note "$DOCKERFILE builds with -tags [$df_tags]. The wrapper imports its database driver, so it needs none, and 'govulncheck ./...' in $WORKFLOW analyses the package without them: the scanned binary would not be the shipped one."
fi
if ! cat "$DRIVER_DIR"/*.go | go_imports_driver; then
	note "$DRIVER_DIR no longer imports $DRIVER_IMPORT. The binary would build and fail at runtime with \"unknown driver pgx5\", so the migration Job would not start."
fi

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

echo "OK: the shipped migrate binary ($PKG, no build tags, go $df_go) is the one govulncheck and the image gate scan"
