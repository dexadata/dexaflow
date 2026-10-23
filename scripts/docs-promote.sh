#!/usr/bin/env bash
# docs-promote.sh: publish a GA release at the documentation root.
#
#   scripts/docs-promote.sh v0.5.5                 # repoint this checkout
#   scripts/docs-promote.sh v0.5.5 --pr            # on origin/main, in a scratch
#                                                  # worktree: push docs/promote-v0.5.5
#                                                  # and open (or find) its pull request
#   scripts/docs-promote.sh v0.5.5 --pr --fetch --auto-merge
#   scripts/docs-promote.sh --self-test            # fixture repository, no network
#
# The site root is built from the `latest` leg of
# website/scripts/ci/versions.json, and a GA moves it with one pull request to
# main: versions.json repointed, the hugo.toml menu rendered from it, and the
# README install pins bumped (promote_docs_version below). That pull request
# used to be opened only by scripts/cut-release.sh, at its very last step, so a
# tag pushed by hand left the root on the previous GA: v0.5.4 was tagged from a
# terminal and the root kept serving v0.5.3 until it was promoted by hand
# (#1935). Now .github/workflows/docs-promote.yaml runs `--pr` when the release
# workflow of a GA tag succeeds, cut-release.sh runs the same `--pr`, and
# whichever comes second finds the pull request the first one opened.
#
# `--pr` never switches the caller's checkout, and re-running it is safe: it
# pushes only when the branch on origin does not already carry the same tree,
# and opens a pull request only when none is open for the branch. It prints
# "<number> <head sha>" of that pull request on stdout, and nothing when the
# root already serves the tag or a newer GA ("newest GA wins": a patch of an
# older minor never takes the root).
#
# Exit status: 0 when the pull request is open (or nothing was needed), 1 when
# the promotion could not be computed, 2 on a usage, git or gh error.
#
# Sourced by cut-release.sh for promote_docs_version and docs_promote_pr.

DOCS_PROMOTE_SLUG="${DOCS_PROMOTE_SLUG:-dexadata/dexaflow}"
DOCS_PROMOTE_REPO="${DOCS_PROMOTE_REPO:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"

# cut-release.sh brings its own; standalone, these write to stderr.
declare -F log >/dev/null || log() { printf 'docs-promote: %s\n' "$*" >&2; }
declare -F warn >/dev/null || warn() { printf 'docs-promote: warn: %s\n' "$*" >&2; }

# The files promote_docs_version writes, all in one commit.
DOCS_PROMOTE_FILES=(website/scripts/ci/versions.json website/hugo.toml README.md)

# promote_docs_version <tag>: point the published docs root at the GA being cut
# and archive the one it replaces.
#
# website/scripts/ci/versions.json decides which ref each leg of the Pages tree
# is built from, and its `latest` entry with subpath "" is the SITE ROOT. Its
# own header says a GA "touches ONLY this file", and no cut has ever touched
# it: it has one commit in its history (#817), so v0.4.2, v0.4.3 and v0.4.4 all
# shipped with the root still built from v0.4.1. Anyone reading the docs gets
# the wrong version, and a link to an ADR added after v0.4.1 is a live 404 at
# the root while resolving fine under /dev/, which is how #953's dead links
# would have recurred one ADR number later.
#
# A procedure written in a comment that no script performs is a procedure that
# does not happen, so the cut performs it. rc cuts do not: a prerelease is not
# what the docs root should serve.
#
# It RETURNS on every failure and never dies, because of WHERE it runs: past
# `git push origin <tag>`. `die` is `exit 1`, and an exit inside a function
# called in the current shell takes the whole script with it: the caller's
# `|| warn` guard is not reached, it is dead code. That would skip the release
# watch and #862's un-draft, and strand the operator on a pushed tag that
# neither a re-invocation nor --resume can get past ("tag already exists"):
# exactly the no-scripted-way-out state this file was changed to remove.
promote_docs_version() { # <tag>
  local tag="$1" f="$ROOT/website/scripts/ci/versions.json" prev tmp
  [ -f "$f" ] || { warn "versions.json not found at $f"; return 1; }
  jq -e '[.versions[]? | select(.id=="latest")] | length == 1' "$f" >/dev/null 2>&1 ||
    { warn "versions.json must have exactly one \`latest\` entry"; return 1; }
  prev="$(jq -r '(.versions[] | select(.id=="latest") | .ref) // empty' "$f")"
  [ -n "$prev" ] || { warn "versions.json has no \`latest\` entry to repoint"; return 1; }
  if [ "$prev" = "$tag" ]; then log "docs: latest already $tag"; return 0; fi
  # A patch cut from an older release branch (ADR 0062) must not take the root
  # from a newer minor: the root serves the newest GA, whichever branch it
  # came from.
  if [ "$(printf '%s\n%s\n' "${prev#v}" "${tag#v}" | sort -V | tail -n1)" = "${prev#v}" ]; then
    log "docs: latest stays $prev, which is newer than $tag"; return 0
  fi
  tmp="$(mktemp)"
  # The label leads with the version, so the dropdown says which release
  # "latest" IS. Number-first is what Kubernetes, Istio and Docsy's own
  # versioning guide do; leading with the generic word was this project's
  # invention. It used to say only "latest", and the number of the CURRENT GA
  # was the one number the menu never showed: a version appeared by name only
  # after it was superseded and archived. The project's own maintainer read the
  # menu and concluded latest was main, which is the clearest evidence the old
  # label was not doing its job.
  jq --arg tag "$tag" --arg prev "$prev" '
    .versions |= (
      map(if .id == "latest" then .ref = $tag | .label = $tag + " (latest)" else . end)
      | if any(.[]; .id == $prev)
        then map(if .id == $prev then .archived = true else . end)
        else ( ([.[] | .id] | index("latest")) + 1 ) as $at
             | .[0:$at] + [{id: $prev, ref: $prev, subpath: $prev, label: $prev, archived: true}] + .[$at:]
        end
    )' "$f" >"$tmp" || { rm -f "$tmp"; warn "rewriting versions.json failed"; return 1; }
  mv "$tmp" "$f" || { rm -f "$tmp"; warn "could not replace versions.json"; return 1; }
  chmod 644 "$f" 2>/dev/null || true

  # The menu lives in TWO files, and this used to rewrite one. versions.json
  # drives the published Pages tree; the [[params.versions]] block in
  # website/hugo.toml is the fallback a plain `hugo` build uses.
  # check-docs-version-menu.sh reconciles them, so leaving hugo.toml behind
  # failed the docs promotion PR of every GA, and while that PR sat unmerged
  # the release was published with its documentation root still serving the
  # PREVIOUS GA. v0.4.8 shipped into exactly that state (#1242).
  #
  # Rendered by the same script that renders the Pages overlay, from the same
  # manifest, so the fallback cannot drift from what is published.
  python3 "$ROOT/website/scripts/ci/render-version-config.py" \
    --versions-file "$f" --sync-hugo-config "$ROOT/website/hugo.toml" >/dev/null ||
    { warn "could not sync website/hugo.toml from versions.json"; return 1; }

  # README.md is rendered by GitHub, not Hugo, so it cannot use the
  # latest-version shortcode and pins the release by number. Bump it here, or
  # the docs guard's check-stale-versions.sh, which judges against the latest
  # leg just repointed above, fails this promotion's own pull request (#1555).
  bash "$ROOT/scripts/check-stale-versions.sh" --bump "$tag" "$ROOT" >/dev/null ||
    { warn "could not bump the README install pins to $tag"; return 1; }

  log "docs: root now built from $tag (archived $prev), both menus rendered, README pins bumped"
}

# docs_promote_pr <tag> [fetch] [auto-merge]: promote on origin/main in a
# scratch worktree, push docs/promote-<tag>, and open its pull request unless
# one is open already. Prints "<number> <head sha>", or nothing when the root
# needs no change. Every message goes to stderr, so a caller can capture the
# answer with $(...).
docs_promote_pr() {
	local tag="$1" fetch="${2:-}" auto="${3:-}" repo="$DOCS_PROMOTE_REPO" t wt branch sha remote_tree pr body
	[[ "$tag" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || { warn "not a GA tag: $tag"; return 2; }
	branch="docs/promote-$tag"
	if [ -n "$fetch" ]; then
		git -C "$repo" fetch -q origin "+refs/heads/main:refs/remotes/origin/main" || { warn "cannot fetch origin/main"; return 2; }
	fi
	git -C "$repo" rev-parse -q --verify "origin/main^{commit}" >/dev/null || { warn "origin/main not found"; return 2; }
	t="$(mktemp -d)"; wt="$t/wt"
	git -C "$repo" worktree add -q --detach "$wt" origin/main >/dev/null 2>&1 || { rm -rf "$t"; warn "could not create a worktree of origin/main"; return 2; }
	_dp_cleanup() {
		git -C "$repo" worktree remove --force "$wt" >/dev/null 2>&1 || true
		rm -rf "$t"
	}
	if ! (ROOT="$wt"; promote_docs_version "$tag") >&2; then
		warn "the promotion of $tag could not be computed (above)"; _dp_cleanup; return 1
	fi
	if [ -z "$(git -C "$wt" status --porcelain -- "${DOCS_PROMOTE_FILES[@]}")" ]; then
		log "origin/main needs no docs promotion for $tag" >&2; _dp_cleanup; return 0
	fi
	# The subject is the one release-gap.sh and docs-gap.sh treat as release
	# mechanics. The sign-off is the DCO check's (#2103): the Docs promote
	# workflow commits as github-actions[bot], which signs off as itself.
	if ! { git -C "$wt" add -- "${DOCS_PROMOTE_FILES[@]}" &&
		git -C "$wt" commit -q --signoff -m "docs: publish $tag at the documentation root" -- "${DOCS_PROMOTE_FILES[@]}"; }; then
		warn "could not commit the docs promotion"; _dp_cleanup; return 2
	fi
	sha="$(git -C "$wt" rev-parse HEAD)"
	# A branch that already carries this tree was pushed by the other caller
	# (the workflow or the cut). Pushing again would only restart its CI.
	remote_tree=""
	if git -C "$wt" fetch -q origin "+refs/heads/$branch:refs/remotes/origin/$branch" 2>/dev/null; then
		remote_tree="$(git -C "$wt" rev-parse -q --verify "refs/remotes/origin/$branch^{tree}" 2>/dev/null || true)"
	fi
	if [ -n "$remote_tree" ] && [ "$remote_tree" = "$(git -C "$wt" rev-parse "HEAD^{tree}")" ]; then
		sha="$(git -C "$wt" rev-parse "refs/remotes/origin/$branch")"
		log "$branch on origin already carries this promotion" >&2
	elif ! git -C "$wt" push -q -f origin "HEAD:refs/heads/$branch" >/dev/null 2>&1; then
		warn "could not push $branch"; _dp_cleanup; return 2
	fi
	_dp_cleanup
	pr="$(gh pr list --repo "$DOCS_PROMOTE_SLUG" --head "$branch" --state open --json number -q '.[0].number' 2>/dev/null || true)"
	if [ -z "$pr" ]; then
		body="$(printf '%s\n\n%s\n\n%s\n' \
			"Repoints the \`latest\` leg of website/scripts/ci/versions.json at $tag, archives the GA it replaces, renders the matching \`[[params.versions]]\` block into website/hugo.toml so check-docs-version-menu.sh agrees, and bumps the README install pins to $tag so check-stale-versions.sh agrees." \
			"Opened by \`scripts/docs-promote.sh $tag --pr\` (the Docs promote workflow or cut-release.sh) after the release was published. It cannot ride in the release's prepare commit: the deploy checks the tag out, and the tag does not exist until after that commit." \
			"Release mechanics: no changelog entry of its own (skip-changelog) and no milestone (the milestone guard exempts \`docs/promote-v*\`).")"
		# A create that fails because the other caller just opened the same
		# pull request is not an error: look again before giving up.
		gh pr create --repo "$DOCS_PROMOTE_SLUG" --base main --head "$branch" --label skip-changelog \
			--title "docs: publish $tag at the documentation root" --body "$body" >/dev/null 2>&1 || true
		pr="$(gh pr list --repo "$DOCS_PROMOTE_SLUG" --head "$branch" --state open --json number -q '.[0].number' 2>/dev/null || true)"
		[ -n "$pr" ] || { warn "pushed $branch but could not open its pull request; open it by hand: gh pr create --base main --head $branch --label skip-changelog"; return 2; }
		log "opened pull request #$pr ($branch)" >&2
	else
		log "pull request #$pr ($branch) is already open" >&2
	fi
	if [ -n "$auto" ]; then
		gh pr merge "$pr" --repo "$DOCS_PROMOTE_SLUG" --auto --squash >/dev/null 2>&1 ||
			warn "could not enable auto-merge on #$pr; merge it once CI is green"
	fi
	printf '%s %s\n' "$pr" "$sha"
}

self_test() {
	local fail=0 tmp r rc out
	# A git hook exports these, and the fixture repositories below would follow
	# them to the repository being committed to.
	unset GIT_DIR GIT_WORK_TREE GIT_INDEX_FILE GIT_OBJECT_DIRECTORY GIT_ALTERNATE_OBJECT_DIRECTORIES GIT_COMMON_DIR GIT_PREFIX
	_eq() { if [ "$1" = "$2" ]; then printf '  ok   %s\n' "$3"; else printf '  FAIL %s\n    got:  %q\n    want: %q\n' "$3" "$1" "$2"; fail=1; fi; }
	tmp="$(mktemp -d)"
	export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_SYSTEM=/dev/null
	export GIT_AUTHOR_NAME=t GIT_AUTHOR_EMAIL=t@example.invalid GIT_COMMITTER_NAME=t GIT_COMMITTER_EMAIL=t@example.invalid
	local here; here="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

	# An origin whose main serves v1.0.0, a clone with the operator on their
	# own branch, and a gh stub that records its argv and answers `pr list`
	# with a number once `pr create` ran.
	git -c init.defaultBranch=main init -q --bare "$tmp/origin.git"
	r="$tmp/clone"
	git clone -q "$tmp/origin.git" "$r" 2>/dev/null
	mkdir -p "$r/website/scripts/ci" "$r/scripts"
	cp "$here/website/scripts/ci/render-version-config.py" "$r/website/scripts/ci/"
	cp "$here/scripts/check-stale-versions.sh" "$r/scripts/"
	printf '%s\n' 'helm install x --version 1.0.0' >"$r/README.md"
	printf '%s\n' '[params]' '  [[params.versions]]' '    version = "seed"' '    url = "https://example.invalid/"' >"$r/website/hugo.toml"
	printf '{"root_url":"https://example.invalid/","versions":[{"id":"latest","ref":"v1.0.0","subpath":"","label":"v1.0.0 (latest)","archived":false}]}\n' \
		>"$r/website/scripts/ci/versions.json"
	git -C "$r" add -A && git -C "$r" commit -qm base && git -C "$r" push -q origin HEAD:main 2>/dev/null
	git -C "$r" fetch -q origin && git -C "$r" checkout -q -b operator-branch
	mkdir -p "$tmp/bin"
	# shellcheck disable=SC2016 # the stub's own variables, expanded when it runs
	printf '#!/bin/sh\nprintf "%%s\\n" "$*" >>"$GH_LOG"\ncase "$*" in *"pr list"*) [ -f "$GH_OPEN" ] && echo 42 ;; *"pr create"*) touch "$GH_OPEN" ;; esac\nexit 0\n' >"$tmp/bin/gh"
	chmod +x "$tmp/bin/gh"
	# log writes to stdout here, as cut-release.sh's does: the answer on stdout
	# must stay clean of it, or the cut reads a log line as the PR number.
	# shellcheck disable=SC2030 # the subshell keeps the fixture's environment to itself
	_pr() { (export PATH="$tmp/bin:$PATH" GH_LOG="$tmp/gh.log" GH_OPEN="$tmp/gh.open" DOCS_PROMOTE_REPO="$r"
		log() { printf 'log: %s\n' "$*"; }; docs_promote_pr "$@") 2>/dev/null; }

	rc=0; out="$(_pr v2.0.0 fetch auto)" || rc=$?
	_eq "$rc" 0 "--pr exits 0"
	_eq "${out%% *}" 42 "--pr prints the pull request number"
	_eq "${out#* }" "$(git -C "$tmp/origin.git" rev-parse docs/promote-v2.0.0 2>/dev/null)" "and the head it pushed"
	_eq "$(git -C "$tmp/origin.git" show docs/promote-v2.0.0:website/scripts/ci/versions.json 2>/dev/null | jq -r '.versions[] | select(.id=="latest") | .ref')" v2.0.0 "--pr pushes the repointed root"
	_eq "$(git -C "$tmp/origin.git" show docs/promote-v2.0.0:README.md 2>/dev/null)" 'helm install x --version 2.0.0' "and the bumped README pins in the same commit"
	_eq "$(git -C "$tmp/origin.git" show docs/promote-v2.0.0:website/hugo.toml 2>/dev/null | grep -c 'v2.0.0 (latest)')" 1 "and the rendered hugo.toml menu"
	_eq "$(git -C "$tmp/origin.git" log -1 --format=%s docs/promote-v2.0.0 2>/dev/null)" "docs: publish v2.0.0 at the documentation root" "with the subject release-gap.sh treats as mechanics"
	_eq "$(git -C "$tmp/origin.git" log -1 --format='%ae|%(trailers:key=Signed-off-by,valueonly)' docs/promote-v2.0.0 2>/dev/null | sed '/^$/d')" \
		"$(git -C "$tmp/origin.git" log -1 --format='%ae|%an <%ae>' docs/promote-v2.0.0 2>/dev/null)" "signed off by its author (DCO, #2103)"
	_eq "$(grep -c -- 'pr create .*--base main .*--label skip-changelog' "$tmp/gh.log")" 1 "--pr opens one pull request to main with skip-changelog"
	_eq "$(grep -c -- 'pr merge 42 .*--auto' "$tmp/gh.log")" 1 "--auto-merge enables auto-merge on it"
	_eq "$(git -C "$r" rev-parse --abbrev-ref HEAD)|$(git -C "$r" status --porcelain)" "operator-branch|" "--pr leaves the caller's checkout alone"
	_eq "$(git -C "$r" worktree list | wc -l | tr -d ' ')" 1 "--pr removes its scratch worktree"

	# The second caller (the cut after the workflow, or the other way round)
	# finds the branch and the pull request: no push, no second pull request.
	local before; before="$(git -C "$tmp/origin.git" rev-parse docs/promote-v2.0.0)"
	: >"$tmp/gh.log"
	# Another date, so the second commit differs from the first and a push of
	# it would show.
	rc=0; out="$(GIT_AUTHOR_DATE=2001-01-01T00:00:00Z GIT_COMMITTER_DATE=2001-01-01T00:00:00Z _pr v2.0.0 fetch)" || rc=$?
	_eq "$rc|$out" "0|42 $before" "a second --pr reports the same pull request and head"
	_eq "$(git -C "$tmp/origin.git" rev-parse docs/promote-v2.0.0)" "$before" "and does not push again, which would restart its CI"
	_eq "$(grep -c 'pr create' "$tmp/gh.log")" 0 "and opens no second pull request"
	_eq "$(grep -c 'pr merge' "$tmp/gh.log")" 0 "and leaves the merge alone without --auto-merge"

	# A patch of an older minor never takes the root, and the root already
	# serving the tag needs nothing: no push, no pull request, empty answer.
	: >"$tmp/gh.log"
	rc=0; out="$(_pr v0.9.9 fetch)" || rc=$?
	_eq "$rc|$out" "0|" "an older GA leaves the root alone"
	_eq "$(git -C "$tmp/origin.git" rev-parse -q --verify refs/heads/docs/promote-v0.9.9 >/dev/null && echo pushed || echo no)" no "and pushes nothing"
	_eq "$(cat "$tmp/gh.log")" "" "and calls no gh"
	git -C "$r" push -q -f origin "refs/remotes/origin/docs/promote-v2.0.0:refs/heads/main" 2>/dev/null ||
		git -C "$tmp/origin.git" update-ref refs/heads/main refs/heads/docs/promote-v2.0.0
	rc=0; out="$(_pr v2.0.0 fetch)" || rc=$?
	_eq "$rc|$out" "0|" "a root that already serves the tag needs nothing"

	# Inputs that must never reach a push.
	rc=0; _pr v2.0.1-rc.1 >/dev/null || rc=$?
	_eq "$rc" 2 "an rc is refused"
	rc=0; _pr 2.0.1 >/dev/null || rc=$?
	_eq "$rc" 2 "a tag without the v is refused"
	printf '{"versions":[]}\n' >"$tmp/broken.json"
	git -C "$r" checkout -q main 2>/dev/null || git -C "$r" checkout -q -b main origin/main
	git -C "$r" reset -q --hard origin/main
	cp "$tmp/broken.json" "$r/website/scripts/ci/versions.json"
	git -C "$r" commit -qam broken && git -C "$r" push -q origin HEAD:main 2>/dev/null
	rc=0; _pr v3.0.0 fetch >/dev/null || rc=$?
	_eq "$rc" 1 "a versions.json with no latest leg fails before any push"
	_eq "$(git -C "$tmp/origin.git" rev-parse -q --verify refs/heads/docs/promote-v3.0.0 >/dev/null && echo pushed || echo no)" no "and pushes nothing"

	# The entry point.
	rc=0; bash "${BASH_SOURCE[0]}" >/dev/null 2>&1 || rc=$?
	_eq "$rc" 2 "no tag is a usage error"
	rc=0; DOCS_PROMOTE_REPO="$tmp/none" bash "${BASH_SOURCE[0]}" --frobnicate v1.0.0 >/dev/null 2>&1 || rc=$?
	_eq "$rc" 2 "an unknown flag is a usage error"

	unset GIT_CONFIG_GLOBAL GIT_CONFIG_SYSTEM
	rm -rf "$tmp"
	if [ "$fail" = 0 ]; then echo "docs-promote self-test: ok"; return 0; fi
	echo "docs-promote self-test: FAIL"; return 1
}

main() {
	local tag="" pr="" fetch="" auto="" arg
	for arg in "$@"; do
		case "$arg" in
			--self-test) self_test; exit $? ;;
			--pr) pr=1 ;;
			--fetch) fetch=1 ;;
			--auto-merge) auto=1 ;;
			-*) warn "unknown flag: $arg"; exit 2 ;;
			*) tag="$arg" ;;
		esac
	done
	[[ "$tag" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || { warn "usage: docs-promote.sh vX.Y.Z [--pr [--fetch] [--auto-merge]] | --self-test"; exit 2; }
	if [ -n "$pr" ]; then docs_promote_pr "$tag" "$fetch" "$auto"; exit $?; fi
	[ -z "$fetch$auto" ] || { warn "--fetch and --auto-merge go with --pr"; exit 2; }
	# shellcheck disable=SC2031 # only the self-test sets it in a subshell
	ROOT="$DOCS_PROMOTE_REPO"
	promote_docs_version "$tag"
}

# Sourced by cut-release.sh for the functions above; run otherwise.
if [ "${BASH_SOURCE[0]}" = "$0" ]; then
	set -uo pipefail
	main "$@"
fi
