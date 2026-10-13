#!/usr/bin/env bash
# Fail a pull request that breaks a public API without saying so.
#
# Three surfaces are public and versioned (AGENTS.md, principle 8): the agent
# gRPC protocol in proto/, the REST API described by docs/api/openapi.yaml, and
# the Go client in pkg/client that is generated from it. buf.yaml has carried
# `breaking: FILE` for a long time and nothing ever ran `buf breaking`; there
# was no diff of the OpenAPI document and none of the Go client either, so a
# removed field, path or exported symbol reached main whenever a reviewer
# missed it (#1554).
#
# This compares each surface against the base branch:
#   - proto/          buf breaking, with the rules buf.yaml selects
#   - OpenAPI         oasdiff breaking, failing on ERR level changes
#   - pkg/client      apidiff, incompatible changes only
#
# A break is allowed when it is intended and announced: the pull request
# carries the `breaking-change` label AND adds a changelog fragment of kind
# Changed or Removed (the fragment is what tells users; the label is what tells
# reviewers). Either one alone is not enough.
#
# A tool that cannot run is a failure, never a pass: a gate that did not look
# must not report that it found nothing.
#
# Not named check-*.sh on purpose: cut-release.sh runs every scripts/check-*.sh
# bare, with no base checkout and none of these tools.
#
# Usage:
#   BREAKING_CHANGE_LABEL=0|1 scripts/api-compat.sh <base-checkout-dir>
#       Run from the head checkout. <base-checkout-dir> is a checkout of the
#       base branch (CI makes one with `git worktree add`).
#   scripts/api-compat.sh --self-test
#
# Locally, against main:
#   git worktree add /tmp/base origin/main
#   scripts/api-compat.sh /tmp/base
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
LABEL="breaking-change"
OPENAPI="docs/api/openapi.yaml"
GO_PKGS=(./pkg/client)
FRAGMENT_DIR=".changes/unreleased"
ANNOUNCE_KINDS_RE='^(Changed|Removed)$'

# fragment_kind <file>: the `kind:` of a changelog fragment, empty when none.
fragment_kind() {
	sed -nE 's/^kind:[[:space:]]*"?([A-Za-z]+)"?[[:space:]]*$/\1/p' "$1" 2>/dev/null | head -1
}

# new_fragment_kinds <head-dir> <base-dir>: the kind of every changelog
# fragment the head adds over the base, one per line.
new_fragment_kinds() {
	local head="$1" base="$2" f
	[ -d "$head/$FRAGMENT_DIR" ] || return 0
	for f in "$head/$FRAGMENT_DIR"/*.yaml "$head/$FRAGMENT_DIR"/*.yml; do
		[ -f "$f" ] || continue
		[ -e "$base/$FRAGMENT_DIR/$(basename "$f")" ] && continue
		fragment_kind "$f"
	done
}

# decide <found-breaks:0|1> <label:0|1> <new fragment kinds, newline separated>
# Returns 0 when the pull request may merge.
decide() {
	local breaks="$1" label="$2" kinds="$3" announced=0
	if [ "$breaks" != 1 ]; then
		echo "OK: no breaking change to a public API."
		return 0
	fi
	if printf '%s\n' "$kinds" | grep -qE "$ANNOUNCE_KINDS_RE"; then announced=1; fi
	if [ "$label" = 1 ] && [ "$announced" = 1 ]; then
		echo "OK: the breaking change above is intended (label '$LABEL') and announced in a changelog fragment."
		return 0
	fi
	echo "FAIL: this pull request breaks a public API (see above)."
	echo "If the break is a mistake, restore compatibility. If it is intended, it needs both:"
	[ "$label" = 1 ] || echo "  - the '$LABEL' label on the pull request (a maintainer applies it), and"
	[ "$announced" = 1 ] || echo "  - a changelog fragment of kind Changed or Removed that names the break (make changelog)."
	return 1
}

# Each check prints what it found and returns 0 (nothing), 1 (a break) or
# 2 (the tool could not run).
proto_breaks() { # <base-dir>
	local out rc=0
	command -v buf >/dev/null 2>&1 || { echo "buf is not installed"; return 2; }
	[ -f "$1/buf.yaml" ] || { echo "base has no buf.yaml: no proto to compare"; return 0; }
	out=$(cd "$ROOT" && buf breaking --against "$1" 2>&1) || rc=$?
	printf '%s\n' "$out" | awk 'NF'
	# buf exits 100 when it reports annotations (breaks); anything else
	# non-zero is a tool or input error.
	case "$rc" in 0) return 0 ;; 100) return 1 ;; *) return 2 ;; esac
}

openapi_breaks() { # <base-dir>
	local out rc=0
	command -v oasdiff >/dev/null 2>&1 || { echo "oasdiff is not installed"; return 2; }
	[ -f "$1/$OPENAPI" ] || { echo "base has no $OPENAPI: nothing to compare"; return 0; }
	out=$(oasdiff breaking "$1/$OPENAPI" "$ROOT/$OPENAPI" --fail-on ERR --format text 2>&1) || rc=$?
	printf '%s\n' "$out" | awk 'NF'
	[ "$rc" -eq 0 ] && return 0
	# --fail-on ERR exits 1 on an ERR level change; an unreadable document
	# also exits non-zero, but says so without listing any change.
	if printf '%s\n' "$out" | grep -qE '^error[[:space:]]'; then return 1; fi
	return 2
}

go_breaks() { # <base-dir>
	local tmp pkg out found=0
	command -v apidiff >/dev/null 2>&1 || { echo "apidiff is not installed"; return 2; }
	tmp=$(mktemp -d)
	for pkg in "${GO_PKGS[@]}"; do
		if [ ! -d "$1/$pkg" ]; then
			echo "$pkg: not in the base, nothing to compare"; continue
		fi
		if ! (cd "$1" && apidiff -w "$tmp/old" "$pkg") >"$tmp/err" 2>&1; then
			cat "$tmp/err"; rm -rf "$tmp"; return 2
		fi
		if ! out=$(cd "$ROOT" && apidiff -incompatible "$tmp/old" "$pkg" 2>&1); then
			printf '%s\n' "$out"; rm -rf "$tmp"; return 2
		fi
		if [ -n "$(printf '%s' "$out" | awk 'NF')" ]; then
			printf '%s:\n%s\n' "$pkg" "$out"; found=1
		fi
	done
	rm -rf "$tmp"
	return "$found"
}

run() { # <base-dir>
	local base="$1" name out rc breaks=0 broken=0
	[ -d "$base" ] || { echo "FAIL: base checkout '$base' is not a directory" >&2; return 2; }
	base="$(cd "$base" && pwd)"
	for name in proto openapi go; do
		rc=0; out=$("${name}_breaks" "$base" 2>&1) || rc=$?
		case "$rc" in
			0) echo "OK   ${name}: no breaking change" ;;
			1) echo "::error::${name}: breaking change against the base branch"; printf '%s\n' "$out" | sed 's/^/     /'; breaks=1 ;;
			*) echo "::error::${name}: the check could not run"; printf '%s\n' "$out" | sed 's/^/     /'; broken=1 ;;
		esac
	done
	[ "$broken" -eq 0 ] || { echo "FAIL: at least one API check could not run; fix the job, not the pull request."; return 2; }
	decide "$breaks" "${BREAKING_CHANGE_LABEL:-0}" "$(new_fragment_kinds "$ROOT" "$base")"
}

self_test() {
	local tmp fail=0 out rc
	tmp=$(mktemp -d); trap 'rm -rf "$tmp"' RETURN
	_expect() { # <name> <want-rc> <want-substring> -- cmd...
		local name="$1" want="$2" msg="$3"; shift 4
		rc=0; out=$("$@" 2>&1) || rc=$?
		if [ "$rc" != "$want" ]; then printf '  FAIL %s: exit %s, want %s\n%s\n' "$name" "$rc" "$want" "$out"; fail=1; return; fi
		case "$out" in
			*"$msg"*) printf '  ok   %s\n' "$name" ;;
			*) printf '  FAIL %s: output lacks %q\n%s\n' "$name" "$msg" "$out"; fail=1 ;;
		esac
	}

	# The decision.
	_expect "no break passes" 0 "no breaking change" -- decide 0 0 ""
	_expect "a break with nothing fails" 1 "breaking-change" -- decide 1 0 ""
	_expect "a break with the label only fails" 1 "Changed or Removed" -- decide 1 1 ""
	_expect "a break with a fragment only fails" 1 "label" -- decide 1 0 "Removed"
	_expect "a break with the label and a Fixed fragment fails" 1 "Changed or Removed" -- decide 1 1 "Fixed"
	_expect "a break with the label and a Changed fragment passes" 0 "announced" -- decide 1 1 $'Fixed\nChanged'
	_expect "a break with the label and a Removed fragment passes" 0 "announced" -- decide 1 1 "Removed"

	# Which fragments are new.
	mkdir -p "$tmp/h/$FRAGMENT_DIR" "$tmp/b/$FRAGMENT_DIR"
	printf 'kind: Fixed\nbody: old\n' >"$tmp/h/$FRAGMENT_DIR/old.yaml"
	cp "$tmp/h/$FRAGMENT_DIR/old.yaml" "$tmp/b/$FRAGMENT_DIR/old.yaml"
	printf 'kind: Removed\nbody: x\n' >"$tmp/h/$FRAGMENT_DIR/new.yaml"
	_expect "only fragments the head adds count" 0 "Removed" -- new_fragment_kinds "$tmp/h" "$tmp/b"
	[ "$(new_fragment_kinds "$tmp/h" "$tmp/b")" = "Removed" ] ||
		{ echo "  FAIL a fragment already on the base was counted"; fail=1; }

	# The checks themselves, against fixtures, wherever the tools are installed.
	# CI installs all three in the job that runs this gate; the self-test runner
	# has none of them, and the decision above is what it covers there.
	if command -v oasdiff >/dev/null 2>&1; then
		mkdir -p "$tmp/oa/b/docs/api" "$tmp/oa/h/docs/api"
		cat >"$tmp/oa/b/$OPENAPI" <<-'YAML'
		openapi: 3.0.3
		info: {title: t, version: "1"}
		paths:
		  /a:
		    get:
		      responses: {"200": {description: ok}}
		  /b:
		    get:
		      responses: {"200": {description: ok}}
		YAML
		sed '/^  \/b:/,$d' "$tmp/oa/b/$OPENAPI" >"$tmp/oa/h/$OPENAPI"
		_expect "oasdiff: a removed path is a break" 1 "/b" -- env ROOT="$tmp/oa/h" bash -c "$(declare -f openapi_breaks); OPENAPI=$OPENAPI; openapi_breaks $tmp/oa/b"
		_expect "oasdiff: an unchanged document is not" 0 "" -- env ROOT="$tmp/oa/b" bash -c "$(declare -f openapi_breaks); OPENAPI=$OPENAPI; openapi_breaks $tmp/oa/b"
	else
		echo "  skip oasdiff fixtures: oasdiff not installed"
	fi
	if command -v apidiff >/dev/null 2>&1 && command -v go >/dev/null 2>&1; then
		local d
		for d in b h; do
			mkdir -p "$tmp/go/$d/pkg/client"
			printf 'module example.test/m\n\ngo 1.22\n' >"$tmp/go/$d/go.mod"
		done
		printf 'package client\n\nfunc Keep() {}\n\nfunc Gone() {}\n' >"$tmp/go/b/pkg/client/c.go"
		printf 'package client\n\nfunc Keep() {}\n\nfunc Added() {}\n' >"$tmp/go/h/pkg/client/c.go"
		_expect "apidiff: a removed exported func is a break" 1 "Gone" -- env ROOT="$tmp/go/h" bash -c "$(declare -f go_breaks); GO_PKGS=(./pkg/client); go_breaks $tmp/go/b"
		_expect "apidiff: an added func is not" 0 "" -- env ROOT="$tmp/go/b" bash -c "$(declare -f go_breaks); GO_PKGS=(./pkg/client); go_breaks $tmp/go/b"
	else
		echo "  skip apidiff fixtures: apidiff not installed"
	fi
	if command -v buf >/dev/null 2>&1; then
		local d
		for d in b h; do
			mkdir -p "$tmp/pb/$d/proto"
			printf 'version: v2\nmodules:\n  - path: proto\nbreaking:\n  use:\n    - FILE\n' >"$tmp/pb/$d/buf.yaml"
		done
		printf 'syntax = "proto3";\npackage t.v1;\nmessage M {\n  string a = 1;\n  string b = 2;\n}\n' >"$tmp/pb/b/proto/t.proto"
		printf 'syntax = "proto3";\npackage t.v1;\nmessage M {\n  string a = 1;\n}\n' >"$tmp/pb/h/proto/t.proto"
		_expect "buf: a removed field is a break" 1 "deleted" -- env ROOT="$tmp/pb/h" bash -c "$(declare -f proto_breaks); proto_breaks $tmp/pb/b"
		_expect "buf: an unchanged proto is not" 0 "" -- env ROOT="$tmp/pb/b" bash -c "$(declare -f proto_breaks); proto_breaks $tmp/pb/b"
	else
		echo "  skip buf fixtures: buf not installed"
	fi

	# A missing base is an error, never a pass.
	_expect "a missing base checkout fails" 2 "not a directory" -- run "$tmp/nope"

	if [ "$fail" -eq 0 ]; then echo "api-compat self-test: ok"; return 0; fi
	echo "api-compat self-test: FAIL"; return 1
}

if [ "${1:-}" = "--self-test" ]; then self_test; exit $?; fi
if [ $# -ne 1 ]; then
	sed -n '/^# Usage:/,/^set -euo/p' "${BASH_SOURCE[0]}" | sed '$d; s/^# \{0,1\}//' >&2
	exit 2
fi
run "$1"
