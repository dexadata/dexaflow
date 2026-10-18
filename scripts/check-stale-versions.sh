#!/usr/bin/env bash
# Fail when a user-facing page tells people to install a release older than the
# newest one.
#
# After v0.5.2 shipped, the README and the installation guide still installed
# 0.5.1, the chart README's examples used 0.4.0 and v0.4.0-rc.2, the published
# images page used 0.4.8 throughout and the install.sh header said v0.5.0
# (#1555). Nothing read those lines at release time, so each GA left a few more
# of them behind, and a reader copying a command got an old release.
#
# What counts as "the version to install" is deliberately narrow: a version in
# an install context, i.e. after `--version`, `.tag=`, `tag:`,
# `DEXAFLOW_VERSION=` / `LEOFLOW_VERSION=`, a `ghcr.io/dexadata/<image>:` or
# `py3.N-` image tag, or `releases/download/`. History ("since 0.5.1", "in
# v0.5.0 the project took the name") is prose about a release, not an
# instruction to install it, and is never matched. A line that must pin an old
# release on purpose carries the marker `stale-version-ok` (in Markdown, an
# HTML comment such as `<!-- stale-version-ok: the last 0.4 release -->`).
#
# A version is stale when it is older than the newest GA, or when it is a
# release candidate of the newest GA or older. A version newer than the newest
# GA (an upcoming release, or another tool's version) is never stale.
#
# The newest GA is the `latest` leg of website/scripts/ci/versions.json, which
# scripts/docs-promote.sh repoints on every GA (promote_docs_version). It is read from
# the file, not from git tags, so this gate needs no network and gives the same
# answer during a cut, in CI and offline. promote_docs_version also calls
# `--bump` below, so the docs promotion pull request carries the new pins and
# this gate stays green on it.
#
# That makes the plain check one release late at cut time: before the tag,
# versions.json still names the GA being replaced (and on release-X.Y, which
# the promotion never touches, the GA the branch was cut from or older). The
# v0.5.4 tag shipped operate pages pinning 0.5.3, and only the promotion pull
# request after the GA noticed (#1934). So cut-release.sh runs this gate as
# `--candidate <tag>`, which judges against the release being cut and leaves
# out README.md, whose pins the promotion bumps after the release is out.
#
# Docs pages should not carry a literal version at all: the
# `{{< latest-version >}}` shortcode prints the newest GA. The Markdown files
# GitHub renders (README.md) cannot use it, which is what `--bump` is for.
#
# Scanned: README.md, install.sh, helm/dexaflow/README.md and its template, and
# website/content/ except website/content/project/ (ADRs and other records,
# which describe the release they were written for).
#
# Usage:
#   scripts/check-stale-versions.sh [root]          # check (default: this repo)
#   scripts/check-stale-versions.sh --candidate <version> [root]
#                                                   # judge against the release
#                                                   # being cut (cut-release.sh)
#   scripts/check-stale-versions.sh --bump <tag> [root]
#                                                   # rewrite README.md's pins
#   scripts/check-stale-versions.sh --self-test
set -euo pipefail

VER_RE='v?[0-9]+\.[0-9]+\.[0-9]+(-rc\.[0-9]+)?'
CTX_RE='(--version[ =]|\.tag=|tag:[[:space:]]*"?|(DEXAFLOW|LEOFLOW)_VERSION=|ghcr\.io/dexadata/[a-z0-9-]+:(py3\.[0-9]+-)?|py3\.[0-9]+-|releases/download/)'
MARKER="stale-version-ok"
# Files whose pins --bump rewrites: the ones that cannot use the shortcode.
BUMP_FILES=(README.md)

# latest_ga <root>: the newest GA, without a leading v, from versions.json.
latest_ga() {
	local f="$1/website/scripts/ci/versions.json" ref
	[ -f "$f" ] || { echo "cannot read $f" >&2; return 1; }
	ref=$(jq -r '[.versions[]? | select(.id == "latest") | .ref] | if length == 1 then .[0] else empty end' "$f" 2>/dev/null) || ref=""
	ref="${ref#v}"
	[[ "$ref" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || { echo "versions.json has no single GA \`latest\` leg (got '${ref}')" >&2; return 1; }
	echo "$ref"
}

# is_stale <version> <latest>: true when <version> is older than <latest>, or a
# release candidate of <latest> or older.
is_stale() {
	local v="${1#v}" latest="${2#v}" core
	core="${v%%-*}"
	if [ "$core" = "$latest" ]; then
		[ "$core" != "$v" ] # a candidate of the newest GA
		return
	fi
	[ "$(printf '%s\n%s\n' "$core" "$latest" | sort -V | head -n1)" = "$core" ]
}

# scanned_files <root>: every file this gate reads, relative to <root>.
scanned_files() {
	local root="$1" f
	for f in README.md install.sh helm/dexaflow/README.md helm/dexaflow/README.md.gotmpl; do
		[ -f "$root/$f" ] && echo "$f"
	done
	if [ -d "$root/website/content" ]; then
		(cd "$root" && find website/content -path website/content/project -prune -o -type f -name '*.md' -print | sort)
	fi
}

# findings <root> <latest> [skip-bump-files]: "<file>:<line>: <version>" for
# each stale pin. With a third argument the BUMP_FILES are left out.
findings() {
	local root="$1" latest="$2" skip="${3:-}" f hit line ver
	while IFS= read -r f; do
		if [ -n "$skip" ] && [[ " ${BUMP_FILES[*]} " == *" $f "* ]]; then continue; fi
		while IFS= read -r hit; do
			line="${hit%%:*}"
			ver=$(printf '%s' "${hit#*:}" | grep -oE "${VER_RE}\$")
			ver="${ver#v}"
			if is_stale "$ver" "$latest"; then
				sed -n "${line}p" "$root/$f" | grep -qF "$MARKER" && continue
				echo "$f:$line: $ver"
			fi
		done < <(grep -noE "${CTX_RE}${VER_RE}" "$root/$f" || true)
	done < <(scanned_files "$root")
}

check() { # [root]
	local root="${1:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}" latest
	latest=$(latest_ga "$root") || { echo "FAIL: cannot tell the newest release" >&2; return 1; }
	report "$latest" "the newest GA" "$(findings "$root" "$latest")"
}

# check_candidate <version> [root]: judge the tree as if <version>'s GA were the
# newest release. Before a cut tags it, versions.json still names the GA before
# it (on release-X.Y, the GA of whenever the branch was cut, since the docs
# promotion lands on main only), so the plain check passes a page pinning the
# release being replaced, and that page ships in the tag: v0.5.4 shipped
# operate pages pinning 0.5.3 that way, found only by the post-GA promotion
# pull request (#1934). README.md is left out: it pins the previous GA until
# the promotion bumps it, after the release is published.
check_candidate() { # <version> [root]
	local v="${1#v}" root="${2:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
	[[ "$v" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-rc\.[0-9]+)?$ ]] || { echo "--candidate needs a version like v1.2.3 or v1.2.3-rc.1, got '$1'" >&2; return 2; }
	v="${v%%-*}"
	report "$v" "the release being cut" "$(findings "$root" "$v" skip-bump-files)"
}

# report <latest> <what latest is> <findings>: print the verdict, 1 on findings.
report() {
	local latest="$1" what="$2" out="$3"
	if [ -z "$out" ]; then
		echo "OK: no page pins a release older than v${latest}."
		return 0
	fi
	{
		echo "FAIL: these lines install a release older than ${what}, v${latest}:"
		printf '%s\n' "$out" | sed 's/^/  /'
		echo
		echo "In website/content/, write {{< latest-version >}} (or {{< latest-version v >}} for the v-prefixed tag)"
		echo "instead of a number. In README.md, run: scripts/check-stale-versions.sh --bump v${latest}"
		echo "A line that must name an old release on purpose carries '${MARKER}' (an HTML comment in Markdown)."
	} >&2
	return 1
}

bump() { # <tag> [root]
	local tag="$1" root="${2:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}" v f
	v="${tag#v}"
	[[ "$v" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || { echo "--bump needs a GA tag like v1.2.3, got '$tag'" >&2; return 2; }
	for f in "${BUMP_FILES[@]}"; do
		[ -f "$root/$f" ] || continue
		# The context keeps its own spelling and only the number changes, so a
		# `v`-prefixed tag stays prefixed and `--version` stays bare. Marked
		# lines are left alone.
		perl -pi -e '
			next if /\Q'"$MARKER"'\E/;
			s{(--version[ =]|\.tag=|(?:DEXAFLOW|LEOFLOW)_VERSION=|ghcr\.io/dexadata/[a-z0-9-]+:(?:py3\.[0-9]+-)?)(v?)[0-9]+\.[0-9]+\.[0-9]+(?:-rc\.[0-9]+)?}{${1}${2}'"$v"'}g;
		' "$root/$f"
		echo "bumped the install pins in $f to $v"
	done
}

# shellcheck disable=SC1003,SC2016 # literal backslashes and backticks in fixtures
self_test() {
	local tmp fail=0 out rc
	tmp=$(mktemp -d); trap 'rm -rf "$tmp"' RETURN
	_eq() { if [ "$1" = "$2" ]; then printf '  ok   %s\n' "$3"; else printf '  FAIL %s\n    got:  %q\n    want: %q\n' "$3" "$1" "$2"; fail=1; fi; }
	_rc() { rc=0; "$@" >/dev/null 2>&1 || rc=$?; echo "$rc"; }

	# The version comparison.
	_eq "$(_rc is_stale 0.5.1 0.5.3)" 0 "an older patch is stale"
	_eq "$(_rc is_stale 0.4.8 0.5.3)" 0 "an older minor is stale"
	_eq "$(_rc is_stale 0.5.3 0.5.3)" 1 "the newest GA is not stale"
	_eq "$(_rc is_stale 0.5.4 0.5.3)" 1 "a newer release is not stale"
	_eq "$(_rc is_stale 0.5.4-rc.1 0.5.3)" 1 "a candidate of the next release is not stale"
	_eq "$(_rc is_stale 0.5.3-rc.1 0.5.3)" 0 "a candidate of the newest GA is stale"
	_eq "$(_rc is_stale 0.4.0-rc.2 0.5.3)" 0 "an old candidate is stale"
	_eq "$(_rc is_stale 0.5.10 0.5.9)" 1 "versions compare numerically, not as text"
	_eq "$(_rc is_stale 1.16.2 0.5.3)" 1 "another tool's higher version is not stale"

	# A fixture repository.
	local r="$tmp/repo"
	mkdir -p "$r/website/scripts/ci" "$r/website/content/get-started" "$r/website/content/project/adrs" "$r/helm/dexaflow"
	printf '{"root_url":"https://x/","versions":[{"id":"latest","ref":"v0.5.3","subpath":"","label":"v0.5.3 (latest)","archived":false},{"id":"v0.5.2","ref":"v0.5.2","subpath":"v0.5.2","label":"v0.5.2","archived":true}]}\n' \
		>"$r/website/scripts/ci/versions.json"
	_eq "$(latest_ga "$r")" "0.5.3" "the newest GA comes from the latest leg of versions.json"

	printf '%s\n' 'helm install x oci://ghcr.io/dexadata/charts/dexaflow --version 0.5.3 \' '  --set image.tag=v0.5.3' >"$r/README.md"
	printf '%s\n' '# DEXAFLOW_VERSION=vX.Y.Z pin a release' >"$r/install.sh"
	printf '%s\n' 'Since 0.5.1 the clear resets retries.' 'Use `--version {{< latest-version >}}`.' >"$r/website/content/get-started/installation.md"
	printf '%s\n' 'This ADR shipped with --version 0.3.0.' >"$r/website/content/project/adrs/0001.md"
	_eq "$(_rc check "$r")" 0 "a tree pinning the newest GA passes, history and ADRs included"

	printf '%s\n' 'cosign verify ghcr.io/dexadata/dexaflow-server:v0.5.1 \' >>"$r/website/content/get-started/installation.md"
	out=$(check "$r" 2>&1) || true
	_eq "$(_rc check "$r")" 1 "an old image tag in a docs page fails"
	case "$out" in *"installation.md:3: 0.5.1"*) echo "  ok   the finding names the file, line and version" ;;
		*) printf '  FAIL the finding lacks file:line: version\n%s\n' "$out"; fail=1 ;; esac
	sed -i '$d' "$r/website/content/get-started/installation.md"

	for line in '--set migrations.image.tag=v0.5.2' 'DEXAFLOW_VERSION=v0.5.0 curl' 'LEOFLOW_VERSION=0.5.0' \
		'  tag: "v0.4.0"' 'ghcr.io/dexadata/dexaflow-runtime:py3.11-v0.4.8' 'image py3.11-v0.4.8 is pinned' \
		'https://github.com/dexadata/dexaflow/releases/download/v0.5.0/x.tar.gz' 'helm install --version=0.4.0'; do
		printf '%s\n' "$line" >"$r/helm/dexaflow/README.md"
		_eq "$(_rc check "$r")" 1 "stale in an install context: $line"
	done
	printf '%s\n' '--set image.tag=v0.4.0-rc.2  <!-- stale-version-ok: shows the rc tag format -->' >"$r/helm/dexaflow/README.md"
	_eq "$(_rc check "$r")" 0 "the marker allows an old pin on its line"
	printf '%s\n' 'helm install cert-manager jetstack/cert-manager --version v1.16.2' >"$r/helm/dexaflow/README.md"
	_eq "$(_rc check "$r")" 0 "a higher version of another chart passes"
	: >"$r/helm/dexaflow/README.md"

	# Without versions.json the gate cannot know the newest GA: fail, never pass.
	mv "$r/website/scripts/ci/versions.json" "$tmp/v.json"
	_eq "$(_rc check "$r")" 1 "a missing versions.json fails"
	mv "$tmp/v.json" "$r/website/scripts/ci/versions.json"

	# --bump rewrites the README pins to the tag and nothing else.
	printf '%s\n' 'helm install x oci://ghcr.io/dexadata/charts/dexaflow --version 0.5.1 -n x \' \
		'  --set image.tag=v0.5.1 \' '  --set migrations.image.tag=v0.5.1' 'In v0.5.0 the project was renamed.' >"$r/README.md"
	bump v0.5.3 "$r" >/dev/null
	_eq "$(sed -n 1p "$r/README.md")" 'helm install x oci://ghcr.io/dexadata/charts/dexaflow --version 0.5.3 -n x \' "--bump rewrites --version without the v"
	_eq "$(sed -n 2p "$r/README.md")" '  --set image.tag=v0.5.3 \' "--bump rewrites an image tag with the v"
	_eq "$(sed -n 4p "$r/README.md")" 'In v0.5.0 the project was renamed.' "--bump leaves history alone"
	_eq "$(_rc check "$r")" 0 "the tree passes after --bump"
	_eq "$(_rc bump not-a-tag "$r")" 2 "--bump refuses a malformed tag"
	_eq "$(_rc bump v0.5.4-rc.1 "$r")" 2 "--bump refuses a release candidate"

	# The entrypoint, as cut-release.sh's run_gates() calls every check-*.sh:
	# no arguments, from anywhere, against this repository.
	_eq "$(_rc bash "${BASH_SOURCE[0]}" "$r")" 0 "the entrypoint checks the root it is given"

	# --candidate judges the tree a cut is about to tag against the release
	# being cut, not against versions.json, which still names the GA before it
	# (and on release-X.Y an even older one). The v0.5.4 tag shipped pages
	# pinning 0.5.3 because the cut judged them against 0.5.3 (#1934).
	printf '%s\n' 'source_mode_image: ghcr.io/dexadata/dexaflow-runtime:0.5.3@sha256:<digest>' >"$r/website/content/get-started/source.md"
	_eq "$(_rc check "$r")" 0 "a pin of the GA versions.json names passes the plain check"
	_eq "$(_rc check_candidate v0.5.4 "$r")" 1 "the same pin fails when v0.5.4 is the release being cut"
	_eq "$(_rc check_candidate 0.5.4-rc.1 "$r")" 1 "a candidate of v0.5.4 is judged against v0.5.4"
	printf '%s\n' 'DEXAFLOW_VERSION=0.5.4' >"$r/website/content/get-started/source2.md"
	rm -f "$r/website/content/get-started/source.md"
	_eq "$(_rc check_candidate 0.5.4-rc.1 "$r")" 0 "under an rc, a pin of its own GA is not stale"
	rm -f "$r/website/content/get-started/source2.md"
	printf '%s\n' 'source_mode_image: ghcr.io/dexadata/dexaflow-runtime:0.5.3@sha256:<digest>' >"$r/website/content/get-started/source.md"
	out=$(check_candidate v0.5.4 "$r" 2>&1) || true
	case "$out" in *"source.md:1: 0.5.3"*) echo "  ok   the candidate finding names the file, line and version" ;;
		*) printf '  FAIL the candidate finding lacks file:line: version\n%s\n' "$out"; fail=1 ;; esac
	printf '%s\n' 'source_mode_image: ghcr.io/dexadata/dexaflow-runtime:py3.11-{{< latest-version v >}}@sha256:<digest>' >"$r/website/content/get-started/source.md"
	_eq "$(_rc check_candidate v0.5.4 "$r")" 0 "the shortcode passes the candidate check"
	# README.md pins the previous GA until the docs promotion bumps it, after
	# the release is published, so the candidate check must not refuse a cut
	# over a line the cut is not allowed to change yet.
	_eq "$(sed -n 1p "$r/README.md" | grep -c 'version 0.5.3')" 1 "the fixture README pins the previous GA"
	_eq "$(_rc check_candidate v0.5.4 "$r")" 0 "the README pins the promotion bumps are not judged"
	printf '%s\n' 'helm install x --version 0.5.3' >"$r/helm/dexaflow/README.md"
	_eq "$(_rc check_candidate v0.5.4 "$r")" 1 "the chart README is still judged"
	: >"$r/helm/dexaflow/README.md"
	_eq "$(_rc check_candidate not-a-version "$r")" 2 "--candidate refuses a malformed version"
	_eq "$(_rc bash "${BASH_SOURCE[0]}" --candidate v0.5.4 "$r")" 0 "the --candidate entrypoint checks the root it is given"
	printf '%s\n' 'DEXAFLOW_VERSION=0.5.3' >"$r/website/content/get-started/source.md"
	_eq "$(_rc bash "${BASH_SOURCE[0]}" --candidate v0.5.4 "$r")" 1 "the --candidate entrypoint fails a stale pin"
	rm -f "$r/website/content/get-started/source.md"

	if [ "$fail" -eq 0 ]; then echo "check-stale-versions self-test: ok"; return 0; fi
	echo "check-stale-versions self-test: FAIL"; return 1
}

case "${1:-}" in
	--self-test) self_test; exit $? ;;
	--candidate) shift; [ $# -ge 1 ] || { echo "usage: $0 --candidate <version> [root]" >&2; exit 2; }; check_candidate "$@"; exit $? ;;
	--bump) shift; [ $# -ge 1 ] || { echo "usage: $0 --bump <tag> [root]" >&2; exit 2; }; bump "$@"; exit $? ;;
	*) check "$@"; exit $? ;;
esac
