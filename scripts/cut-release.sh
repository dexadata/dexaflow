#!/usr/bin/env bash
# Cut a Dexaflow release — one repo-owned entrypoint for the whole flow so the
# steps are not re-invented (and re-broken) by hand each time (#879).
#
# It: preflights, prepares the chart/CHANGELOG bump on a release branch, opens the
# prepare PR, waits for CI green (re-running ONLY known-transient flakes, never
# hard-failing on them), squash-merges, waits for the base branch to go green on
# the merge commit, then (behind an explicit confirmation gate) tags and pushes,
# and watches the release workflows to PUBLISHED. A structured log is written for provenance.
#
# Usage:
#   scripts/cut-release.sh v0.4.4-rc.1            # interactive confirm before tag
#   scripts/cut-release.sh 0.4.4 --yes            # non-interactive (CI/automation)
#   scripts/cut-release.sh v0.4.4-rc.1 --dry-run  # preflight + print the plan, no mutation
#   scripts/cut-release.sh <version> --resume     # finish a cut that died after
#                                                 # the prepare PR merged
#   scripts/cut-release.sh --self-test            # pure-logic cases, no network
#
# Which branch a cut lands on follows from the version (ADR 0062): X.Y.0 and
# its candidates are prepared and tagged on main; every later patch (X.Y.Z-rc.N
# and X.Y.Z with Z > 0) on release-X.Y, which must already exist. That branch
# is created once from the vX.Y.0 tag, and the GA cut prints the command. The
# docs root promotion always targets main, whichever branch the tag came from.
#
# A `-rc.N` version keeps CHANGELOG `[Unreleased]`; a GA version (no `-rc`) moves
# `[Unreleased]` to `[X.Y.Z] - <date>` and opens a fresh `[Unreleased]`.
#
# Either way the cut first folds the pending `.changes/unreleased/*.yaml`
# fragments into `[Unreleased]` and deletes them (scripts/changelog-fold.sh).
#
# Release-auth: the tag+push (the irreversible publish) NEVER happens without an
# explicit confirmation — the interactive prompt, or `--yes` passed deliberately.
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHART="$ROOT/helm/dexaflow/Chart.yaml"
CHANGELOG="$ROOT/CHANGELOG.md"
REPO="dexadata/dexaflow"

# Transient CI failures that are safe to rerun — never a code signal. Matches the
# classes seen in practice: registry rate-limits and 5xx, Go module-proxy stream
# resets, the shallow-fetch merge-base gate, the lite cold-start /readyz timing
# flake, a package index answering 5xx mid-resolve, and a k3d cluster that fails
# to come up. Every alternative is covered by the table in self_test(); add a
# pattern there first, with the line that motivated it, or it is not coverage.
#
# Deliberately absent: anything that also matches a failure of OUR OWN code. A
# "returned error: 5xx" alternative looks like a registry pattern but is what
# curl prints for a 5xx out of our control plane, which this harness queries
# from 60 sites — it would rerun past the commonest real regression there is.
FLAKE_RE='toomanyrequests|Rate exceeded|TLS handshake|i/o timeout|no space left|Connection reset|context deadline|Client\.Timeout|INTERNAL_ERROR|proxy\.golang\.org|stream ID [0-9]|readyz never responded|go mod download|failed to solve|returned error: 404|reserve cache|no merge base|exit code 128|HTTP Error 5[0-9][0-9]|Gateway Time-out|Cluster creation FAILED|failed Cluster Creation'

log()  { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33mwarn:\033[0m %s\n' "$*" >&2; }
die()  { printf '\033[1;31merror:\033[0m %s\n' "$*" >&2; exit 1; }

# ---- pure logic (unit-testable, no network) --------------------------------

# flake_verdict <log-text> -> prints "flake" | "real" | "unknown"
#
# "unknown" is a distinct answer on purpose. A job that dies in `Initialize
# containers` — the service-container pull, this repo's most frequent failure
# (#1007) — has no step log, so `gh run view --log-failed` returns EMPTY. The
# caller used to grep that empty string, get no match, and conclude "not a
# flake", which disabled the rerun loop for the one failure it most needed to
# handle (#978). No evidence is not evidence of absence: for a cut, unknown
# means rerun, because a rerun is cheap and a stopped cut on a transient is not.
flake_verdict() {
  local lg="${1:-}"
  [ -n "$lg" ] || { echo unknown; return 0; }
  if printf '%s' "$lg" | grep -qE "$FLAKE_RE"; then echo flake; else echo real; fi
}


# normalize_tag: accept "0.4.4", "v0.4.4", "0.4.4-rc.1" -> "v0.4.4[-rc.1]".
normalize_tag() { local v="${1#v}"; printf 'v%s' "$v"; }
# chart_version: the SemVer the Helm chart carries (no leading v).
chart_version() { printf '%s' "${1#v}"; }
# is_rc: true when the version is a pre-release candidate.
is_rc() { case "$1" in *-rc.*) return 0 ;; *) return 1 ;; esac; }
# valid_version: X.Y.Z or X.Y.Z-rc.N (no leading v).
valid_version() { [[ "$1" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-rc\.[0-9]+)?$ ]]; }
# minor_of: "0.5.1-rc.2" -> "0.5".
minor_of() { local v="${1#v}"; v="${v%%-*}"; printf '%s' "${v%.*}"; }
# release_base: the branch a version is cut from (ADR 0062). X.Y.0 and its
# candidates come from main; every later patch of X.Y comes from release-X.Y,
# which is created from the vX.Y.0 tag right after that GA ships.
release_base() {
  local v="${1#v}" patch
  v="${v%%-*}"; patch="${v##*.}"
  if [ "$patch" = 0 ]; then printf 'main'; else printf 'release-%s' "$(minor_of "$v")"; fi
}

self_test() {
  local fail=0
  # The real checkout, captured before any case overrides ROOT with a fixture.
  local REPO_ROOT="$ROOT"
  _eq() { [ "$1" = "$2" ] || { echo "FAIL: $3: '$1' != '$2'"; fail=1; }; }
  # flake_verdict: the three answers, and the one that used to be missing.
  _eq "$(flake_verdict 'Error: toomanyrequests: Rate exceeded')" "flake"   "rate limit is a flake"
  _eq "$(flake_verdict 'FAIL: TestFoo assertion failed')"        "real"    "a test failure is real"
  _eq "$(flake_verdict '')"                                      "unknown" "an unreadable log is UNKNOWN, not a non-flake (#978)"
  # The regression this locks: `Initialize containers` leaves no step log, so
  # --log-failed returns empty. Treating that as "real" stopped the cut on this
  # repo's most common transient. Assert the two are not the same answer.
  _eq "$([ "$(flake_verdict '')" = "$(flake_verdict 'FAIL: TestFoo assertion failed')" ] && echo same || echo different)" \
      "different" "empty and a genuine failure must not classify alike"

  _eq "$(normalize_tag 0.4.4)"      "v0.4.4"        "normalize bare"
  _eq "$(normalize_tag v0.4.4)"     "v0.4.4"        "normalize v"
  _eq "$(normalize_tag v0.4.4-rc.1)" "v0.4.4-rc.1"  "normalize rc"
  _eq "$(chart_version v0.4.4-rc.1)" "0.4.4-rc.1"   "chart strips v"
  if is_rc "0.4.4-rc.1"; then :; else echo "FAIL: is_rc rc"; fail=1; fi
  if is_rc "0.4.4"; then echo "FAIL: is_rc ga"; fail=1; fi
  for v in 0.4.4 0.4.4-rc.1 10.20.30 1.2.3-rc.15; do valid_version "$v" || { echo "FAIL: valid_version $v"; fail=1; }; done
  for v in v0.4.4 0.4 0.4.4-rc 0.4.4rc1 1.2.3-alpha; do valid_version "$v" && { echo "FAIL: valid_version accepted bad $v"; fail=1; }; done
  # release_base decides which branch a tag is cut from (ADR 0062): every
  # X.Y.0 and its candidates come from main, every later patch of that minor
  # from release-X.Y. Cutting a patch from main would ship whatever main has
  # gathered since the minor, which is the thing release branches exist to stop.
  _eq "$(minor_of 0.5.1-rc.2)"        "0.5"          "minor_of drops patch and rc"
  _eq "$(minor_of 10.20.30)"          "10.20"        "minor_of multi-digit"
  _eq "$(release_base 0.5.0)"         "main"         "a minor GA is cut from main"
  _eq "$(release_base 0.5.0-rc.3)"    "main"         "a minor rc is cut from main"
  _eq "$(release_base 0.5.1)"         "release-0.5"  "a patch GA is cut from its release branch"
  _eq "$(release_base 0.5.1-rc.1)"    "release-0.5"  "a patch rc is cut from its release branch"
  _eq "$(release_base 10.20.3)"       "release-10.20" "multi-digit release branch"
  _eq "$(release_base 0.5.10)"        "release-0.5"  "patch 10 is still a patch, not 0.5.1"
  # FLAKE_RE decides whether a red run gets rerun or stops the cut, so both ways
  # of getting it wrong cost something real: too broad reruns past a genuine
  # regression, and a pattern that can never match does nothing while looking
  # like coverage. Both shipped in the first cut of this change; only a table
  # caught them. A positive marked [observed] is verbatim from a real failed
  # run, [ours] from our own scripts, and the rest are fixed strings from the
  # tool that emits them (Go net/http, http2, POSIX errno, curl, urllib, k3d).
  local -a POS=()
  _flake() { POS+=("$1"); printf '%s' "$1" | grep -qE "$FLAKE_RE" || { echo "FAIL: FLAKE_RE misses transient: $1"; fail=1; }; }
  _real()  { printf '%s' "$1" | grep -qE "$FLAKE_RE" && { echo "FAIL: FLAKE_RE masks real failure: $1"; fail=1; }; }

  # [observed] Docker Hub answering 500 to a manifest HEAD, 2026-09-07: five e2e
  # jobs across four PRs died inside one window with nothing of ours involved.
  _flake 'ERROR: failed to build: failed to solve: python:3.11-slim-bookworm: failed to resolve source metadata for docker.io/library/python:3.11-slim-bookworm: unexpected status from HEAD request to https://registry-1.docker.io/v2/library/python/manifests/3.11-slim-bookworm: 500 Internal Server Error'
  _flake 'toomanyrequests: You have reached your pull rate limit.'
  _flake 'net/http: TLS handshake timeout'
  _flake 'dial tcp 140.82.121.4:443: i/o timeout'
  _flake 'write /home/runner/work/_temp/build: no space left on device'
  _flake 'curl: (56) Recv failure: Connection reset by peer'
  _flake 'rpc error: code = DeadlineExceeded desc = context deadline exceeded'
  _flake 'Get "https://proxy.golang.org/github.com/@v/list": context deadline exceeded (Client.Timeout exceeded while awaiting headers)'
  _flake 'http2: server sent GOAWAY: stream ID 15; INTERNAL_ERROR'
  # [ours] scripts/lite-redeploy.sh — the lite cold-start timing flake.
  _flake '::error::/readyz never responded after 60s — boot log tail:'
  # urllib phrasing: the index or a build backend answering 5xx mid-resolve.
  _flake 'ERROR: HTTP Error 504: Gateway Time-out'
  # k3d v5.7.4: cmd/cluster/clusterCreate.go and pkg/client/cluster.go.
  _flake 'Cluster creation FAILED, all changes have been rolled back!'
  _flake 'failed Cluster Creation: Failed Cluster Preparation'

  # A 5xx out of OUR OWN control plane is the commonest shape of a real
  # regression in this harness, which has 60 curl sites pointed at it. curl
  # reports it as an exit-22 message, so any "returned error: 5xx" alternative
  # is a bare non-zero exit in disguise.
  _real 'curl: (22) The requested URL returned error: 500'
  _real 'curl: (22) The requested URL returned error: 503'
  # Our own pyproject.toml breaking under `pip install -e ./parser` prints
  # metadata-generation-failed. It is the symptom of both a network fetch and a
  # first-party defect; the network cause always arrives on its own line and is
  # matched above, so matching the symptom only adds the false positive.
  _real 'error: metadata-generation-failed'
  # [observed] main, 2026-09-07 — a real test defect that must reach a human.
  _real '--- FAIL: TestRegisterVersionDuplicateReturns409 (0.08s)'
  _real 'versions_conflict_integration_test.go:63: 409 body leaks raw pg internals ("23505")'
  _real 'Error: Process completed with exit code 1.'

  # Every alternative must earn its place by matching one of the lines above. A
  # pattern that matches nothing is not harmless: it is a transient class we
  # believe is covered and is not. This is what catches a bad escape — FLAKE_RE
  # is single-quoted, so a doubled backslash reaches grep as a literal
  # backslash and `curl: \\(22\\)` can never match the `curl: (22)` it was
  # written for. GRANDFATHERED lists the pre-existing alternatives that predate
  # this table and for which no captured sample survives (#956); nothing may be
  # added to it.
  local GRANDFATHERED='Rate exceeded|go mod download|returned error: 404|reserve cache|no merge base|exit code 128'
  local alt hit line
  while IFS= read -r alt; do
    [ -n "$alt" ] || continue
    printf '%s' "$alt" | grep -qxE "$GRANDFATHERED" && continue
    hit=0
    for line in "${POS[@]}"; do
      printf '%s' "$line" | grep -qE -- "$alt" && { hit=1; break; }
    done
    [ "$hit" = 1 ] || { echo "FAIL: FLAKE_RE alternative matches no known transient: $alt"; fail=1; }
  done < <(printf '%s' "$FLAKE_RE" | tr '|' '\n')

  # run_verdict: the classification that decides whether the cut tags. Each
  # case is a real GitHub conclusion; the cancelled ones are why this exists.
  # Fixtures are shaped like `gh run list --json databaseId,status,conclusion`,
  # NOT like the REST API: gh's Run.Conclusion is a Go string, so a run with no
  # conclusion yet carries "" and never null. Modelling the API instead is how
  # the empty-conclusion case shipped returning GREEN — every fixture used
  # null, and null never reaches the conclusion logic because those runs are
  # status-driven.
  _eq "$(run_verdict '[]')" "NONE" "no runs for the sha yet"
  _eq "$(run_verdict '[{"status":"queued","conclusion":""}]')" "PENDING" "one run still queued"
  _eq "$(run_verdict '[{"status":"in_progress","conclusion":""}]')" "PENDING" "one run in progress"
  _eq "$(run_verdict '[{"status":"completed","conclusion":""}]')" "PENDING" "completed with no conclusion yet is not GREEN"
  _eq "$(run_verdict '[{"status":"completed","conclusion":"success"},{"status":"completed","conclusion":""}]')" "PENDING" "one unconcluded run among successes"
  _eq "$(run_verdict '[{"status":"completed","conclusion":""},{"status":"completed","conclusion":"cancelled"}]')" "PENDING" "an empty conclusion never leaks into the BLOCKED list"
  # A conclusion that is not a string is not a pass. These lock the exit-status
  # guards on the two jq calls below the blank check: without them, jq errors on
  # `join` and on `any`, both results read as "nothing bad", and the payload
  # falls through to GREEN. Unreachable from gh, which marshals a Go string —
  # but it is the same shape as the "" bug, and the guards had no test.
  _eq "$(run_verdict '[{"status":"completed","conclusion":["success"]}]')" "PENDING" "an array conclusion is not a pass"
  _eq "$(run_verdict '[{"status":"completed","conclusion":{}}]')" "PENDING" "an object conclusion is not a pass"
  _eq "$(run_verdict '[{"status":"completed","conclusion":"stale"}]')" "BLOCKED stale" "a stale run is never GREEN"
  _eq "$(run_verdict '[{"status":"completed","conclusion":"action_required"}]')" "BLOCKED action_required" "action_required is never GREEN"
  _eq "$(run_verdict '[{"status":"completed","conclusion":"success"}]')" "GREEN" "all success"
  _eq "$(run_verdict '[{"status":"completed","conclusion":"success"},{"status":"completed","conclusion":"skipped"}]')" "GREEN" "success plus a skipped gate"
  _eq "$(run_verdict '[{"status":"completed","conclusion":"neutral"}]')" "GREEN" "neutral counts as a pass"
  _eq "$(run_verdict '[{"status":"completed","conclusion":"failure"}]')" "FAILED" "a plain failure reaches the flake path"
  _eq "$(run_verdict '[{"status":"completed","conclusion":"cancelled"}]')" "BLOCKED cancelled" "a cancelled run is never GREEN"
  _eq "$(run_verdict '[{"status":"completed","conclusion":"timed_out"}]')" "BLOCKED timed_out" "a timed-out run is never GREEN"
  _eq "$(run_verdict '[{"status":"completed","conclusion":"startup_failure"}]')" "BLOCKED startup_failure" "a startup failure is never GREEN"
  _eq "$(run_verdict '[{"status":"completed","conclusion":"success"},{"status":"completed","conclusion":"cancelled"}]')" "BLOCKED cancelled" "one cancelled among successes"
  _eq "$(run_verdict '[{"status":"completed","conclusion":"failure"},{"status":"completed","conclusion":"cancelled"}]')" "BLOCKED cancelled" "BLOCKED outranks FAILED, so FLAKE_RE never sees it"
  # latest_runs: a guard re-run on the same commit (a label added) replaces the
  # cancelled run it superseded; an older run of ANOTHER workflow still counts.
  _eq "$(run_verdict "$(latest_runs '[{"workflowName":"CHANGELOG guard","createdAt":"2026-10-04T10:00:00Z","status":"completed","conclusion":"cancelled"},{"workflowName":"CHANGELOG guard","createdAt":"2026-10-04T10:00:01Z","status":"completed","conclusion":"success"},{"workflowName":"CI","createdAt":"2026-10-04T09:59:59Z","status":"completed","conclusion":"success"}]')")" "GREEN" "a guard run cancelled by its own re-run on the same commit is not judged"
  _eq "$(run_verdict "$(latest_runs '[{"workflowName":"CI","createdAt":"2026-10-04T10:00:00Z","status":"completed","conclusion":"cancelled"},{"workflowName":"Security","createdAt":"2026-10-04T10:00:01Z","status":"completed","conclusion":"success"}]')")" "BLOCKED cancelled" "the newest run of each workflow is still judged"
  _eq "$(run_verdict "$(latest_runs '[{"status":"completed","conclusion":"cancelled"},{"workflowName":"CI","createdAt":"2026-10-04T10:00:01Z","status":"completed","conclusion":"success"}]')")" "BLOCKED cancelled" "a run without a workflow name keeps the whole list judged"
  # Two runs of one guard started in the same second (the prepare PR opened with
  # its label): the cancelled one was replaced by its sibling. Shape taken from
  # v0.5.2's prepare PR #1510, where max_by picked the cancelled run.
  _eq "$(run_verdict "$(latest_runs '[{"workflowName":"Docs guard","createdAt":"2026-10-08T16:30:32Z","status":"completed","conclusion":"success"},{"workflowName":"Docs guard","createdAt":"2026-10-08T16:30:32Z","status":"completed","conclusion":"cancelled"},{"workflowName":"CI","createdAt":"2026-10-08T16:30:32Z","status":"completed","conclusion":"success"}]')")" "GREEN" "a run cancelled by a sibling started in the same second is not judged"
  _eq "$(run_verdict "$(latest_runs '[{"workflowName":"Docs guard","createdAt":"2026-10-08T16:30:32Z","status":"completed","conclusion":"cancelled"},{"workflowName":"Docs guard","createdAt":"2026-10-08T16:30:32Z","status":"completed","conclusion":"cancelled"}]')")" "BLOCKED cancelled" "every tied run cancelled is still BLOCKED"
  _eq "$(run_verdict "$(latest_runs '[{"workflowName":"Docs guard","createdAt":"2026-10-08T16:30:32Z","status":"completed","conclusion":"success"},{"workflowName":"Docs guard","createdAt":"2026-10-08T16:30:32Z","status":"completed","conclusion":"failure"}]')")" "FAILED" "tied runs that both finished are both judged"
  _eq "$(run_verdict "$(latest_runs '[{"workflowName":"Docs guard","createdAt":"2026-10-08T16:30:32Z","status":"completed","conclusion":"cancelled"},{"workflowName":"Docs guard","createdAt":"2026-10-08T16:30:32Z","status":"in_progress","conclusion":""}]')")" "PENDING" "a tied sibling still running keeps the cut waiting"
  _eq "$(run_verdict '[{"status":"completed","conclusion":"cancelled"},{"status":"in_progress","conclusion":""}]')" "PENDING" "PENDING outranks BLOCKED"
  # And the REST shape must not regress if anything ever feeds it in.
  _eq "$(run_verdict '[{"status":"completed","conclusion":null}]')" "PENDING" "a null conclusion is treated like an empty one"

  # promote_docs_version rewrites the file that decides which ref the published
  # docs root is built from. Getting it wrong ships the wrong docs for a
  # release, silently.
  local vt; vt="$(mktemp -d)"
  _versions() { printf '%s' "$1" >"$vt/versions.json"; }
  # promote_docs_version now renders website/hugo.toml too, so the fixture
  # carries the renderer and a block for it to rewrite. Without them the
  # function correctly returns 1 and every case below would fail for the wrong
  # reason.
  _fixture_docs() {
    # REPO_ROOT, not ROOT: the callers override ROOT to point at the fixture
    # before calling this, so reading ROOT here copies the file from the very
    # directory being populated.
    mkdir -p "$1/website/scripts/ci"
    cp "$REPO_ROOT/website/scripts/ci/render-version-config.py" "$1/website/scripts/ci/"
    printf '%s\n' '[params]' '  [[params.versions]]' '    version = "seed"' '    url = "https://example.invalid/"' > "$1/website/hugo.toml"
  }
  _promote() { ( ROOT="$vt" && _fixture_docs "$vt" && cp "$vt/versions.json" "$vt/website/scripts/ci/versions.json" && promote_docs_version "$1" >/dev/null && cat "$vt/website/scripts/ci/versions.json" ); }

  _versions '{"root_url":"https://example.invalid/","versions":[{"id":"latest","ref":"v1.0.0","subpath":"","label":"latest","archived":false},{"id":"v1.0.0","ref":"v1.0.0","subpath":"v1.0.0","label":"v1.0.0","archived":false}]}'
  local out; out="$(_promote v2.0.0)"
  _eq "$(printf '%s' "$out" | jq -r '.versions[] | select(.id=="latest") | .ref')" "v2.0.0" "docs root repointed at the new GA"
  _eq "$(printf '%s' "$out" | jq -r '.versions[] | select(.id=="v1.0.0") | .archived')" "true" "the superseded GA is archived"
  # The label carries the tag now. It used to be deliberately left as the bare
  # string "latest", which made the current GA the one release whose number the
  # menu never showed: an archived tag shows its number only after it has been
  # superseded. Pinning the new behavior rather than deleting the old assertion,
  # because a promotion that silently stopped updating the label would put the
  # menu back to naming a version the reader cannot identify.
  _eq "$(printf '%s' "$out" | jq -r '.versions[] | select(.id=="latest") | .label')" "v2.0.0 (latest)" "the dropdown label names the release it points at"

  # The outgoing GA may have no archive leg yet — three of them do not, because
  # no cut has ever run this. One must be created rather than silently skipped.
  _versions '{"root_url":"https://example.invalid/","versions":[{"id":"latest","ref":"v1.0.0","subpath":"","label":"latest","archived":false},{"id":"dev","ref":"","subpath":"dev","label":"dev","archived":false}]}'
  out="$(_promote v2.0.0)"
  _eq "$(printf '%s' "$out" | jq -r '.versions[] | select(.id=="v1.0.0") | .subpath')" "v1.0.0" "an archive leg is created for an outgoing GA that had none"
  _eq "$(printf '%s' "$out" | jq -r '.versions[] | select(.id=="dev") | .ref')" "" "the dev leg is untouched"
  _eq "$(printf '%s' "$out" | jq -r '[.versions[].id] | join(",")')" "latest,v1.0.0,dev" "the new leg lands after latest"

  # ...and after `latest` specifically, not after whatever happens to be first.
  # The insertion used to be .[0:1] + [new] + .[1:], which is the same thing only
  # while latest sits at index 0 — and nothing enforces that; the function only
  # checks there is exactly one. With dev first, the archive leg landed BEFORE
  # latest, and array order is the version dropdown's order
  # (render-version-config.py emits one [[params.versions]] per entry, in order).
  _versions '{"root_url":"https://example.invalid/","versions":[{"id":"dev","ref":"","subpath":"dev","label":"dev","archived":false},{"id":"latest","ref":"v1.0.0","subpath":"","label":"latest","archived":false}]}'
  out="$(_promote v2.0.0)"
  _eq "$(printf '%s' "$out" | jq -r '[.versions[].id] | join(",")')" "dev,latest,v1.0.0" "the new leg lands after latest even when latest is not first"

  # Re-running a cut must not archive the version it is promoting.
  _versions '{"root_url":"https://example.invalid/","versions":[{"id":"latest","ref":"v2.0.0","subpath":"","label":"latest","archived":false}]}'
  out="$(_promote v2.0.0)"
  _eq "$(printf '%s' "$out" | jq -r '.versions[] | select(.id=="latest") | .ref')" "v2.0.0" "promoting the current root is a no-op"

  # With release branches a GA can be OLDER than the docs root: a security
  # patch on release-1.0 after v2.0.0 shipped. Promoting it would point the site
  # root back at the previous minor and archive the current one.
  _versions '{"root_url":"https://example.invalid/","versions":[{"id":"latest","ref":"v2.0.0","subpath":"","label":"v2.0.0 (latest)","archived":false}]}'
  out="$(_promote v1.0.5)"
  _eq "$(printf '%s' "$out" | jq -r '.versions[] | select(.id=="latest") | .ref')" "v2.0.0" "an older patch never takes the docs root"
  _eq "$(printf '%s' "$out" | jq -r '[.versions[] | select(.archived==true)] | length')" "0" "and archives nothing"

  # restore_docs_file has to undo a STAGED edit, not just a dirty worktree. The
  # commit at the end of promote_docs_pr runs one line after `git add`, so when
  # it fails the file is already in the index — and `git checkout -- <path>`
  # restores the worktree FROM the index, making it a no-op there. The staged
  # edit then rode the branch switch onto the operator's branch and failed the
  # next cut's working-tree guard, while the comment claimed it could not.
  local rf; rf="$(mktemp -d)"
  (
    export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_SYSTEM=/dev/null
    mkdir -p "$rf/website/scripts/ci" && printf '{"v":1}\n' >"$rf/website/scripts/ci/versions.json"
    cd "$rf" && git -c init.defaultBranch=main init -q . &&
    git config user.email t@example.invalid && git config user.name tester &&
    git add -A && git commit -qm base &&
    printf '{"v":2}\n' >website/scripts/ci/versions.json && git add website/scripts/ci/versions.json
  ) >/dev/null 2>&1 || { echo "  FAIL restore_docs_file: could not build the fixture"; fail=1; }
  local rfroot="$ROOT"; ROOT="$rf"; restore_docs_file; ROOT="$rfroot"
  _eq "$(cd "$rf" && git status --porcelain)" "" "restore_docs_file also drops a STAGED versions.json"
  rm -rf "$rf"

  # promote_docs_version must RETURN on a bad input, never die. It runs after
  # `git push origin <tag>`, so an `exit` there takes the whole script with it
  # and skips the provenance line, the entire release watch and the #862
  # un-draft — leaving a pushed tag, a possibly-draft release nobody is
  # watching, and no scripted way forward: re-invoking dies on "tag already
  # exists", and so does --resume. That is the state this PR exists to remove.
  #
  # Called here NOT in a subshell, deliberately: the subshell in _promote is
  # what makes the cases above blind to an exit, and the caller has no subshell.
  # If this regresses, --self-test aborts here; the EXIT trap below names it.
  local pdrc reached=0 oroot="$ROOT"
  _versions '{"root_url":"https://example.invalid/","versions":[{"id":"latest","ref":"v1.0.0","subpath":"","label":"latest"},{"id":"latest","ref":"v1.0.1","subpath":"","label":"latest"}]}'
  _fixture_docs "$vt" && cp "$vt/versions.json" "$vt/website/scripts/ci/versions.json"
  # >&3: the trap runs in the redirection context of the command that exited,
  # and that command carries >/dev/null 2>&1 to keep its warn out of the
  # transcript — so a plain echo here lands in /dev/null and the abort is silent
  # again, which is the one thing the trap exists to prevent. Measured: zero
  # lines without the fd, the FAIL line with it.
  exec 3>&2
  trap 'echo "  FAIL promote_docs_version exited (die) instead of returning — everything below here did not run" >&3' EXIT
  ROOT="$vt"; promote_docs_version v2.0.0 >/dev/null 2>&1 && pdrc=0 || pdrc=$?
  trap - EXIT
  reached=1; ROOT="$oroot"
  _eq "$pdrc" "1" "promote_docs_version returns 1 on a versions.json with two \`latest\` legs"
  _eq "$reached" "1" "and its caller keeps running — a die here strands a pushed tag"

  rm -f "$vt/website/scripts/ci/versions.json"
  ROOT="$vt"; promote_docs_version v2.0.0 >/dev/null 2>&1 && pdrc=0 || pdrc=$?
  reached=2; ROOT="$oroot"
  _eq "$pdrc" "1" "promote_docs_version returns 1 when versions.json is missing"
  _eq "$reached" "2" "and that path keeps running too"
  rm -rf "$vt"

  # resume_target is the only check between --resume and a tag on the wrong
  # commit, so both answers are driven against a real repository.
  # The fixture has commits AFTER the bump, because that is the shape that
  # broke: a chart version is a plateau, so the tip passes the version check
  # while being the wrong commit to tag.
  local rt; rt="$(mktemp -d)"
  (
    export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_SYSTEM=/dev/null
    cd "$rt" && git -c init.defaultBranch=main init -q . &&
    git config user.email t@example.invalid && git config user.name tester &&
    mkdir -p helm/dexaflow && printf 'version: 9.9.8\nappVersion: "9.9.8"\n' >helm/dexaflow/Chart.yaml &&
    git add -A && git commit -qm base &&
    printf 'version: 9.9.9\nappVersion: "9.9.9"\n' >helm/dexaflow/Chart.yaml &&
    git add -A && git commit -qm "release: prepare v9.9.9" &&
    echo one >a.txt && git add -A && git commit -qm "unrelated one" &&
    echo two >b.txt && git add -A && git commit -qm "unrelated two" &&
    git update-ref refs/remotes/origin/main refs/heads/main
  ) >/dev/null 2>&1 || { echo "  FAIL resume_target: could not build the fixture"; fail=1; }
  local got rc want
  want="$( cd "$rt" && git rev-parse HEAD~2 )"   # the prepare commit
  got="$( cd "$rt" && resume_target 9.9.9 v9.9.9 2>/dev/null )" && rc=0 || rc=$?
  _eq "$rc" "0" "resume_target accepts a main that carries the version being cut"
  _eq "$got" "$want" "resume_target returns the commit that INTRODUCED the version, not the tip"
  _eq "$( cd "$rt" && resume_target 9.9.9 v9.9.9 2>&1 >/dev/null | grep -c 'unrelated' )" "2" "it names the commits it is excluding"
  got="$( cd "$rt" && resume_target 1.2.3 v1.2.3 2>&1 )" && rc=0 || rc=$?
  _eq "$rc" "1" "resume_target refuses a main that carries a different version"
  case "$got" in *"does not hold a prepared"*) echo "  ok   resume_target says why it refused" ;;
    *) printf '  FAIL resume_target message\n    got: %q\n' "$got"; fail=1 ;; esac

  # On a release branch the walk has to run against THAT branch: main carries
  # the next minor's history, and the prepare commit of a patch exists only on
  # release-X.Y. The fixture forks a release branch off the 9.9.9 prepare, bumps
  # it to 9.9.10 and adds an unrelated commit after, the same plateau shape.
  (
    export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_SYSTEM=/dev/null
    cd "$rt" && git checkout -q -b rel HEAD~2 &&
    printf 'version: 9.9.10\nappVersion: "9.9.10"\n' >helm/dexaflow/Chart.yaml &&
    git add -A && git commit -qm "release: prepare v9.9.10" &&
    echo three >c.txt && git add -A && git commit -qm "unrelated three" &&
    git update-ref refs/remotes/origin/release-9.9 refs/heads/rel && git checkout -q main
  ) >/dev/null 2>&1 || { echo "  FAIL resume_target: could not build the release-branch fixture"; fail=1; }
  want="$( cd "$rt" && git rev-parse rel~1 )"
  got="$( cd "$rt" && resume_target 9.9.10 v9.9.10 release-9.9 2>/dev/null )" && rc=0 || rc=$?
  _eq "$rc" "0" "resume_target accepts a release branch that carries the patch being cut"
  _eq "$got" "$want" "resume_target walks the release branch, not main"
  got="$( cd "$rt" && resume_target 9.9.10 v9.9.10 2>&1 )" && rc=0 || rc=$?
  _eq "$rc" "1" "resume_target with the default base still judges main"

  # A shallow clone truncates rev-list, and the truncation is invisible. At
  # depth 1 the shallow root has no parents, so it is not TREESAME to anything
  # and gets listed even though it never touched Chart.yaml: intro lands on the
  # tip, the "main advanced" warn never fires, and the exact bug the walk above
  # exists to kill is back and silent. Deeper is worse, not better — a wrong sha
  # delivered with a confident exclusion list. This is CI's own clone shape:
  # actions/checkout defaults to fetch-depth 1 and the script-selftests job does
  # not override it, and --resume is advertised for automation.
  local sh; sh="$(mktemp -d)"
  if ( export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_SYSTEM=/dev/null
       git clone --depth=1 -q "file://$rt" "$sh/c" ) >/dev/null 2>&1; then
    got="$( cd "$sh/c" && resume_target 9.9.9 v9.9.9 2>&1 )" && rc=0 || rc=$?
    _eq "$rc" "1" "resume_target refuses a shallow clone instead of tagging the tip"
    case "$got" in *shallow*) echo "  ok   it names the shallow clone as the reason" ;;
      *) printf '  FAIL shallow-clone message\n    got: %q\n' "$got"; fail=1 ;; esac
  else
    echo "  FAIL resume_target: could not build the shallow fixture"; fail=1
  fi
  rm -rf "$sh" "$rt"

  # The release-prep PR must carry skip-changelog. Without it the changelog
  # guard fails the prepare PR itself — on an rc it deliberately leaves
  # [Unreleased] alone — and wait_sha_green correctly refuses to call that a
  # flake, so the cut dies one step past the gates. Asserted against a stub
  # because the real call opens a pull request.
  local stubdir argv
  stubdir="$(mktemp -d)"
  printf '#!/bin/sh\nprintf "%%s\\n" "$@" > "$STUB_ARGV"\n' > "$stubdir/gh"
  chmod +x "$stubdir/gh"
  argv="$(
    export PATH="$stubdir:$PATH" STUB_ARGV="$stubdir/argv" REPO=o/r
    create_prepare_pr release/v9.9.9-rc.1 "release: prepare v9.9.9-rc.1" body >/dev/null 2>&1
    cat "$stubdir/argv" 2>/dev/null
  )"
  if printf '%s\n' "$argv" | grep -qx -- '--label' && printf '%s\n' "$argv" | grep -qx 'skip-changelog'; then
    echo "  ok   prepare PR carries --label skip-changelog"
  else
    echo "  FAIL prepare PR is missing --label skip-changelog; the changelog guard will fail it"; fail=1
  fi
  _eq "$(printf '%s\n' "$argv" | grep -cx 'release/v9.9.9-rc.1')" "1" "prepare PR heads the release branch"
  _eq "$(printf '%s\n' "$argv" | grep -A1 -x -- '--base' | tail -n1)" "main" "prepare PR targets main by default"
  argv="$(
    export PATH="$stubdir:$PATH" STUB_ARGV="$stubdir/argv" REPO=o/r
    create_prepare_pr release/v9.9.10 "release: prepare v9.9.10" body release-9.9 >/dev/null 2>&1
    cat "$stubdir/argv" 2>/dev/null
  )"
  _eq "$(printf '%s\n' "$argv" | grep -A1 -x -- '--base' | tail -n1)" "release-9.9" "a patch prepare PR targets its release branch"
  rm -rf "$stubdir"

  # promote_docs_pr had no coverage at all — every case above targets
  # promote_docs_version or restore_docs_file. That is how a seven-call-site
  # function shipped with one call site missing its argument and the whole gate
  # green: the one that lost it strands the operator on docs/promote-<tag>, and
  # nothing anywhere said so. Two invariants are locked, on BOTH the no-op and
  # the happy path: the function returns you to the ref you were on with a clean
  # tree, AND it did the work — because asserting only the restore passes when
  # promote_docs_pr does nothing at all. Measured: make `git checkout -b` fail so
  # the function returns at its first line, and a restore-only assertion still
  # reports "operator-branch||0". So the gh stub records its argv and the origin
  # is inspected: the no-op path must push nothing and open no PR, the happy path
  # must push the repointed root and merge it.
  #
  # Driven against a bare repo as origin, a gh stub, and a wait_sha_green
  # override, so it needs no network. Run in a subshell for the cd/PATH, and the
  # rc is asserted so a die inside cannot pass for a pass.
  local pp; pp="$(mktemp -d)"
  _pp_fixture() { # <latest-ref>  -> builds origin + a clone, operator on their own branch
    rm -rf "$pp/origin" "$pp/wt"; mkdir -p "$pp/origin"
    (
      export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_SYSTEM=/dev/null
      cd "$pp/origin" && git -c init.defaultBranch=main init -q --bare . &&
      git clone -q "$pp/origin" "$pp/wt" && cd "$pp/wt" &&
      git config user.email t@example.invalid && git config user.name tester &&
      mkdir -p website/scripts/ci &&
      cp "$REPO_ROOT/website/scripts/ci/render-version-config.py" website/scripts/ci/ &&
      printf '%s\n' '[params]' '  [[params.versions]]' '    version = "seed"' '    url = "https://example.invalid/"' > website/hugo.toml &&
      printf '{"root_url":"https://example.invalid/","versions":[{"id":"latest","ref":"%s","subpath":"","label":"latest","archived":false}]}\n' "$1" \
        >website/scripts/ci/versions.json &&
      git add -A && git commit -qm base && git push -q origin main &&
      git checkout -q -b operator-branch
    ) >/dev/null 2>&1
  }
  _pp_run() { # -> echoes "<branch after>|<porcelain after>|<rc>"
    local rc
    (
      export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_SYSTEM=/dev/null
      export PATH="$pp/stub:$PATH" REPO=o/r ROOT="$pp/wt" GH_ARGV="$pp/ghargv"
      cd "$pp/wt" && wait_sha_green() { echo GREEN; } && promote_docs_pr v2.0.0
    ) >/dev/null 2>&1
    rc=$?
    printf '%s|%s|%s' \
      "$(cd "$pp/wt" && git rev-parse --abbrev-ref HEAD)" \
      "$(cd "$pp/wt" && git status --porcelain)" "$rc"
  }
  mkdir -p "$pp/stub"
  # The stub records its argv: without that, the cases below pass when
  # promote_docs_pr does NOTHING. Measured — make `git checkout -b` fail and the
  # function returns at its first line, yet both cases still report
  # "operator-branch||0". The restore invariant is real but it is not the whole
  # claim the comments make, so assert the work too.
  printf '#!/bin/sh\nprintf "%%s " "$@" >>"$GH_ARGV"; printf "\\n" >>"$GH_ARGV"\ncase "$*" in *"--json number"*) echo 7 ;; esac\nexit 0\n' >"$pp/stub/gh"
  chmod +x "$pp/stub/gh"

  # The no-op path: main already serves the tag, so promote_docs_version writes
  # nothing and the function returns early. This is the call site that lost its
  # argument, and it is reachable exactly after the manual recovery RELEASING.md
  # now documents.
  _pp_fixture v2.0.0 || { echo "  FAIL promote_docs_pr: could not build the no-op fixture"; fail=1; }
  rm -f "$pp/ghargv"
  _eq "$(_pp_run)" "operator-branch||0" "promote_docs_pr returns you to your branch when the root already serves the tag"
  _eq "$(cat "$pp/ghargv" 2>/dev/null)" "" "the no-op path opens no PR"
  _eq "$(git -C "$pp/origin" rev-parse -q --verify refs/heads/docs/promote-v2.0.0 >/dev/null 2>&1 && echo pushed || echo no)" "no" \
    "the no-op path pushes nothing"

  # The happy path: the file changes, the PR is opened and merged through the
  # stub, and gh's own branch switch never happens — so the restore is the only
  # thing bringing HEAD back.
  _pp_fixture v1.0.0 || { echo "  FAIL promote_docs_pr: could not build the happy-path fixture"; fail=1; }
  rm -f "$pp/ghargv"
  _eq "$(_pp_run)" "operator-branch||0" "promote_docs_pr returns you to your branch after promoting"
  _eq "$(grep -c '^pr merge 7 ' "$pp/ghargv" 2>/dev/null)" "1" "the happy path actually merges the docs PR"
  _eq "$(git -C "$pp/origin" rev-parse -q --verify refs/heads/docs/promote-v2.0.0 >/dev/null 2>&1 && echo pushed || echo no)" "pushed" \
    "the happy path actually pushes the branch"
  _eq "$(git -C "$pp/origin" show refs/heads/docs/promote-v2.0.0:website/scripts/ci/versions.json 2>/dev/null | jq -r '.versions[] | select(.id=="latest") | .ref')" \
    "v2.0.0" "and what it pushed is the repointed root"
  rm -rf "$pp"

  # promote_docs_version must leave BOTH version menus agreeing, which is the
  # only thing that matters about it: it rewrote versions.json alone and the
  # gate failed the docs promotion PR of every GA, so the release shipped with
  # its documentation root on the previous version. Asserting on the GATE, not
  # on file contents, is deliberate: the generator and the checker are then
  # bound to each other and neither can drift alone.
  local dv; dv="$(mktemp -d)"
  mkdir -p "$dv/website/scripts/ci"
  cp "$ROOT/website/scripts/ci/render-version-config.py" "$dv/website/scripts/ci/"
  cat > "$dv/website/scripts/ci/versions.json" <<'JSON'
{
  "root_url": "https://example.invalid/leoflow/",
  "versions": [
    {"id": "latest", "ref": "v9.9.8", "subpath": "", "label": "v9.9.8 (latest)", "archived": false},
    {"id": "dev", "ref": "", "subpath": "dev", "label": "dev (main, unreleased)", "archived": false}
  ]
}
JSON
  printf '%s\n' '[params]' '  [[params.versions]]' '    version = "v9.9.8 (latest)"' '    url = "https://example.invalid/leoflow/"' \
    '  [[params.versions]]' '    version = "dev (main, unreleased)"' '    url = "https://example.invalid/leoflow/dev/"' > "$dv/website/hugo.toml"
  ( ROOT="$dv"; promote_docs_version v9.9.9 >/dev/null 2>&1 )
  local menu_rc=0
  bash "$ROOT/scripts/check-docs-version-menu.sh" "$dv/website/scripts/ci/versions.json" "$dv/website/hugo.toml" >/dev/null 2>&1 || menu_rc=$?
  _eq "$menu_rc" "0" "promote_docs_version leaves both version menus agreeing"
  _eq "$(grep -c 'v9.9.9 (latest)' "$dv/website/hugo.toml")" "1" "the hugo fallback names the new release"
  _eq "$(grep -c '^    version = "v9.9.8"$' "$dv/website/hugo.toml")" "1" "and keeps the superseded one as an archived leg"
  rm -rf "$dv"

  if [ "$fail" = 0 ]; then echo "self-test: PASS"; else echo "self-test: FAIL"; return 1; fi
}

# ---- CI waiter -------------------------------------------------------------

# run_verdict <runs-json>: classify a sha's runs. Pure (jq only, no network)
# so --self-test can table it. Echoes exactly one of:
#
#   NONE                    no runs for this sha yet — keep waiting
#   PENDING                 at least one run has not completed
#   GREEN                   every run completed in {success, skipped, neutral}
#   FAILED                  at least one `failure`, and nothing worse
#   BLOCKED <conclusions>   a completed run whose conclusion is neither a pass
#                           nor a plain failure
#
# BLOCKED is the whole point. A cancelled run has status "completed" and a
# conclusion that is not "failure", so it is neither pending nor failed — and
# the old form here returned GREEN for it. On the merge commit that is the last
# gate before `git tag`, so the cut would tag a sha whose CI never finished and
# the "main red — NOT tagging" guard would never fire. timed_out,
# startup_failure, stale and action_required have the same shape.
#
# BLOCKED outranks FAILED, and the reason is the rerun loop rather than
# FLAKE_RE: the old `failed=` list only ever collected conclusion=="failure",
# so a cancelled run was never a rerun candidate to begin with. What it did
# instead was survive. One transient failure plus one cancelled run meant
# FLAKE_RE matched the failure, the rerun ground it to success, and the next
# iteration found an empty `failed` list with the cancelled run still sitting
# there — so the loop laundered the sha and echoed GREEN. Ranking BLOCKED above
# FAILED is what stops that. It also would not converge if we tried: `gh run
# rerun --failed` has nothing to act on when a run's jobs concluded cancelled.
#
# PENDING outranks both — a sha still in flight is not judged at all.
run_verdict() { # <runs-json>
  local j="$1" n pend blank bad anyfail
  n=$(printf '%s' "$j" | jq 'length' 2>/dev/null); [[ "$n" =~ ^[0-9]+$ ]] || n=0
  [ "$n" -eq 0 ] && { echo NONE; return; }
  pend=$(printf '%s' "$j" | jq '[.[] | select(.status!="completed")] | length' 2>/dev/null)
  [[ "$pend" =~ ^[0-9]+$ ]] || pend=1
  # An unpopulated conclusion is a read-window artifact, not a verdict, so it
  # counts as PENDING: the loop re-polls, and the deadline still fails it
  # closed. It must be caught HERE and not by the `bad` line below, because
  # gh's Run.Conclusion is a Go string — an absent conclusion marshals to ""
  # and never to null, so jq's `//` never fires on it, and `[""] | join(",")`
  # is "" so a non-empty test on the result cannot see it either. That is the
  # shape that returned GREEN for a completed run with no conclusion.
  blank=$(printf '%s' "$j" | jq '[.[] | select(.status=="completed") | select((.conclusion // "") == "")] | length' 2>/dev/null)
  [[ "$blank" =~ ^[0-9]+$ ]] || blank=1
  if [ "$pend" -gt 0 ] || [ "$blank" -gt 0 ]; then echo PENDING; return; fi
  # jq's own failure must not read as "nothing bad": check the exit status
  # rather than overloading emptiness of the result, or a malformed payload
  # falls through to GREEN.
  bad=$(printf '%s' "$j" | jq -r '[.[] | .conclusion | select(. != null and . != "" and . != "success" and . != "skipped" and . != "neutral" and . != "failure")] | unique | join(",")' 2>/dev/null) || { echo PENDING; return; }
  [ -n "$bad" ] && { echo "BLOCKED $bad"; return; }
  anyfail=$(printf '%s' "$j" | jq -r 'any(.[]; .conclusion == "failure")' 2>/dev/null) || { echo PENDING; return; }
  [ "$anyfail" = "true" ] && { echo FAILED; return; }
  echo GREEN
}

# latest_runs <runs-json>: keeps the newest run of each workflow. A pull
# request's guards run again on the same commit when a label changes, and the
# concurrency group of a pull request cancels the older run, so the commit
# carries a cancelled run next to the one that replaced it. Judging both would
# read that commit as BLOCKED forever. A run without a workflow name leaves the
# list as it is, so a payload of an unexpected shape is still judged in full.
#
# createdAt has one-second resolution, and the prepare PR is opened with its
# label in the same call, so `opened` and `labeled` start two runs of a guard in
# the same second and one cancels the other. max_by alone then picks either
# (v0.5.2 picked the cancelled one and died on a green PR). Among the runs tied
# at the newest second, a cancelled run is the one its sibling replaced, so the
# tied runs that were not cancelled are judged, all of them; only when every
# tied run was cancelled is the cancellation the verdict.
latest_runs() { # <runs-json>
  printf '%s' "$1" | jq 'if all(.[]; (.workflowName // "") != "" and (.createdAt // "") != "")
    then [group_by(.workflowName)[]
          | (map(.createdAt) | max) as $newest
          | [.[] | select(.createdAt == $newest)]
          | ([.[] | select(.conclusion != "cancelled")]) as $live
          | if ($live | length) > 0 then $live[] else .[] end]
    else . end' 2>/dev/null || printf '%s' "$1"
}

# wait_sha_green <sha>: block until every run for <sha> is completed; rerun only
# transient flakes (bounded); echo GREEN or RED. Robust to the post-rerun window
# where gh briefly reports the prior conclusion (it waits for pending==0).
wait_sha_green() {
  local sha="$1" reruns=0 j verdict failed rid isflake start=$SECONDS
  local deadline="${CUT_WAIT_DEADLINE:-5400}" # 90 min; a stuck-queued run must not hang the cut forever
  while :; do
    if [ $((SECONDS - start)) -gt "$deadline" ]; then
      warn "timed out after ${deadline}s waiting on CI for ${sha:0:8} — inspect: gh run list --commit $sha"
      echo RED; return 2
    fi
    # --commit filters SERVER-side. The old form listed the 40 most recent runs
    # repo-wide and filtered headSha here, but one push fans out to ~7 runs and
    # the changelog guard alone appears 4x per sha (its labeled/unlabeled
    # triggers), so 40 runs spans about 5 shas. Measured on this repo: the
    # release sha's 4 runs were outside the window and the query returned 0,
    # which run_verdict reports as NONE, spinning to the deadline before dying
    # RED on a green sha.
    j=$(gh run list --commit "$sha" --limit 100 --json databaseId,status,conclusion,workflowName,createdAt 2>/dev/null \
          | jq '[.[]]' 2>/dev/null)
    j="$(latest_runs "$j")"
    # run_verdict coerces jq's output itself, so a partial or `null` read keeps
    # the loop waiting rather than declaring a verdict.
    verdict="$(run_verdict "$j")"
    case "$verdict" in
      NONE)     sleep 25; continue ;;
      PENDING)  sleep 45; continue ;;
      GREEN)    echo GREEN; return 0 ;;
      BLOCKED*) warn "CI for ${sha:0:8} has a run that did not finish (${verdict#BLOCKED }) — a run that was cancelled or timed out says nothing about the code, so it is not rerun-eligible; inspect: gh run list --commit $sha"
                echo RED; return 1 ;;
      FAILED)   ;; # falls through to the flake path below
      *)        warn "run_verdict returned an unhandled verdict '$verdict' — failing closed"
                echo RED; return 1 ;;
    esac
    failed=$(echo "$j" | jq -r '.[] | select(.conclusion=="failure") | .databaseId')
    isflake=1
    for rid in $failed; do
      gh run view "$rid" --log-failed 2>/dev/null | grep -qE "$FLAKE_RE" || isflake=0
    done
    if [ "$isflake" = 1 ] && [ "$reruns" -lt 8 ]; then
      reruns=$((reruns+1)); warn "transient flake -> rerun #$reruns ($failed)"
      for rid in $failed; do gh run rerun "$rid" --failed >/dev/null 2>&1; done
      sleep 90; continue
    fi
    echo RED; return 1
  done
}

# ---- prepare edits ---------------------------------------------------------

# resume_target <chart-version> <tag> [base]: echo the sha --resume should pick
# up at, or die. base is the branch the cut targets (release_base), main by
# default. Split out so --self-test can drive both answers against a throwaway
# repository; it is the only thing standing between --resume and a tag on the
# wrong commit.
resume_target() { # <chart-version> <tag> [base]
  local cv="$1" tag="$2" base="${3:-main}" tip on_tip c v intro=""
  # Every answer below comes out of `git rev-list`, which a shallow clone
  # truncates without saying so. At depth 1 the shallow root has no parents, so
  # it is not TREESAME to anything and is listed even though it never touched
  # Chart.yaml: intro lands on the tip, the "main advanced" warn never fires,
  # and the plateau bug this function exists to kill comes back silent. Deeper
  # is worse — a wrong sha with a confident exclusion list. Refuse instead:
  # actions/checkout defaults to fetch-depth 1, and --resume is advertised for
  # automation, so this is the clone shape CI would hand us.
  [ "$(git rev-parse --is-shallow-repository 2>/dev/null)" = false ] ||
    die "--resume: this is a shallow clone — rev-list is truncated here, so the walk would tag the wrong commit (at depth 1, silently the tip). Run: git fetch --unshallow"
  tip="$(git rev-parse "origin/$base" 2>/dev/null)" || die "--resume: cannot resolve origin/$base"
  on_tip="$(git show "${tip}:helm/dexaflow/Chart.yaml" 2>/dev/null | awk '/^version:/{print $2; exit}')"
  [ -n "$on_tip" ] || die "--resume: no Chart.yaml version at ${tip:0:8}"
  [ "$on_tip" = "$cv" ] || die "--resume: $base carries chart $on_tip, not $cv; $base does not hold a prepared $tag."

  # The tip is the WRONG answer, and it looks right. A chart version is a
  # plateau, not an edge: it stays equal to cv for every commit from the prepare
  # merge until the next cut, so anything merged in between passes the check.
  # Measured on this repo: three commits carried 0.4.5-rc.2, and the tip was two
  # unrelated PRs ahead of the prepare commit the tag belongs on. Walk back to
  # the commit that INTRODUCED cv — the same sha the non-resume path produces.
  for c in $(git rev-list "origin/$base" -- helm/dexaflow/Chart.yaml); do
    v="$(git show "${c}:helm/dexaflow/Chart.yaml" | awk '/^version:/{print $2; exit}')"
    [ "$v" = "$cv" ] || break
    intro="$c"
  done
  [ -n "$intro" ] || die "--resume: no commit on $base introduces chart $cv"
  if [ "$intro" != "$tip" ]; then
    warn "resume: $base advanced since $tag was prepared; tagging ${intro:0:8} and excluding:"
    git log --oneline "$intro..origin/$base" >&2
  fi
  printf '%s' "$intro"
}

# promote_docs_pr <tag>: land the docs promotion, AFTER the tag exists.
#
# This cannot ride in the prepare commit. versions.json lives under website/,
# which is in website-deploy.yml's push path filter, so the prepare merge would
# trigger a deploy whose `latest` leg checks out `ref: <tag>` — a tag the cut
# has not created yet, because it tags only after main is green on that very
# commit. The deploy fails, wait_sha_green sees red, and the cut dies at the
# merge-commit gate with no scripted way out: --resume cannot help either,
# since the sha stays red until the tag exists and the tag is what the red run
# is blocking. The GA would be unpublishable.
#
# Post-tag also happens to be the only ordering that WORKS: the deploy reads
# versions.json from the triggering ref (main), not from the tag, and a tag
# push does not trigger that workflow at all. So the promotion must arrive as
# its own push to main, after the tag is real.
#
# Failures here warn rather than die: the release is already published, and a
# stale docs root is recoverable by re-running this by hand. Dying would tell
# the operator the cut failed when it did not.
# restore_docs_file: put versions.json back to HEAD, staged edit included.
#
# `git checkout -- <path>` restores the worktree from the INDEX, so the moment
# `git add` has run it is a no-op: a failed commit right after it left the file
# STAGED, the branch switch carried it to the operator's branch, and the next
# cut died on "working tree not clean". Split out from promote_docs_pr so the
# self-test can drive it — the hole was in a closure no test could reach.
restore_docs_file() {
  git -C "$ROOT" restore --staged --worktree -- website/scripts/ci/versions.json 2>/dev/null || true
}

promote_docs_pr() { # <tag>
  local tag="$1" pr orig
  local branch="docs/promote-$tag"
  # Restore whatever ref we found, not `main`: from a git worktree — which is
  # how this script is developed, and the cut only WARNS when HEAD is not main —
  # `git checkout main` fails rc=128 ("already used by worktree at ..."), and
  # `2>/dev/null` swallows the reason. Every early return goes through _restore,
  # which also drops an uncommitted versions.json: leaving that behind fails the
  # NEXT cut at its working-tree guard.
  orig="$(git symbolic-ref -q --short HEAD || git rev-parse HEAD)"
  # Takes the ref as an argument: bash has no local functions, so _restore
  # outlives this one, while `orig` (local) does not. Reading $orig from the
  # leaked copy would be an unbound-variable exit under `set -u` — a shell-
  # killing exit living past the tag push, which is the one thing this file
  # may not have.
  _restore() { # <ref-to-return-to>
    local o="${1:-}"
    restore_docs_file
    # Never silent: a missing argument used to strand the operator on the docs
    # branch with no trace. That is how the call site below went unconverted
    # through a green gate — six of seven passed "$orig" and nothing said so.
    [ -n "$o" ] || { warn "docs promotion: _restore got no ref — still on $(git rev-parse --abbrev-ref HEAD 2>/dev/null)"; return 0; }
    git checkout -q "$o" 2>/dev/null || git checkout -q --detach "$o" 2>/dev/null || true
  }
  git checkout -q -b "$branch" origin/main 2>/dev/null || { warn "docs promotion: could not create $branch — publish the docs root by hand"; return 0; }
  promote_docs_version "$tag" || { warn "docs promotion skipped"; _restore "$orig"; return 0; }
  # -C "$ROOT": promote_docs_version writes an absolute path, so a bare relative
  # pathspec stages nothing whenever cwd is not the repo root — and the
  # --cached --quiet below is then TRUE, so the failure came back as the log
  # line "docs: root already serves <tag>". A flat lie, return 0, and a dirty
  # tree left behind. Check the exit.
  # Both menus, or the promotion PR ships one and leaves the other dirty in the
  # operator's tree, which is how the gate failed every GA (#1242).
  git -C "$ROOT" add website/scripts/ci/versions.json website/hugo.toml || { warn "docs promotion: could not stage the version menus"; _restore "$orig"; return 0; }
  if git -C "$ROOT" diff --cached --quiet -- website/scripts/ci/versions.json; then log "docs: root already serves $tag"; _restore "$orig"; return 0; fi
  git -C "$ROOT" commit -q -m "docs: publish $tag at the documentation root" -- website/scripts/ci/versions.json website/hugo.toml || { warn "docs promotion: commit failed"; _restore "$orig"; return 0; }
  git push -u origin "$branch" -q || { warn "docs promotion: push failed — open the PR by hand from $branch"; _restore "$orig"; return 0; }
  gh pr create --repo "$REPO" --base main --head "$branch" --label skip-changelog \
    --title "docs: publish $tag at the documentation root" \
    --body "Repoints website/scripts/ci/versions.json's \`latest\` leg at $tag, archives the one it replaces, and renders the matching \`[[params.versions]]\` block into website/hugo.toml so check-docs-version-menu.sh agrees. Opened by cut-release.sh after the tag was pushed, because the deploy checks the tag out, so this cannot ride in the prepare commit." >/dev/null || { warn "docs promotion: gh pr create failed"; _restore "$orig"; return 0; }
  pr="$(gh pr view "$branch" --json number -q .number 2>/dev/null)"
  log "docs promotion PR #$pr — waiting for CI"
  # Its own budget: wait_sha_green restarts the clock per call, so the default
  # 5400s would pin the operator's terminal for 90 minutes on a docs PR after
  # the release is already out. ci.yaml has no path filter, so this one-file PR
  # still drags the full suite — 30 minutes is generous for it. An operator who
  # exports CUT_WAIT_DEADLINE overrides this default and gets their own value,
  # whether it is larger or smaller.
  if [ "$(CUT_WAIT_DEADLINE="${CUT_DOCS_WAIT_DEADLINE:-${CUT_WAIT_DEADLINE:-1800}}" wait_sha_green "$(git rev-parse "$branch")")" = GREEN ]; then
    gh pr merge "$pr" --repo "$REPO" --squash --delete-branch >/dev/null 2>&1 &&
      log "docs root now serves $tag (PR #$pr)" || warn "docs promotion PR #$pr is green but did not merge — merge it by hand"
  else
    warn "docs promotion PR #$pr is not green — the release is published, but the docs root still serves the previous GA. Fix and merge #$pr."
  fi
  _restore "$orig"
}

# promote_docs_version <tag>: point the published docs root at the GA being cut
# and archive the one it replaces.
#
# website/scripts/ci/versions.json decides which ref each leg of the Pages tree
# is built from, and its `latest` entry with subpath "" is the SITE ROOT. Its
# own header says a GA "touches ONLY this file" — and no cut has ever touched
# it: it has one commit in its history (#817), so v0.4.2, v0.4.3 and v0.4.4 all
# shipped with the root still built from v0.4.1. Anyone reading the docs gets
# the wrong version, and a link to an ADR added after v0.4.1 is a live 404 at
# the root while resolving fine under /dev/ — which is how #953's dead links
# would have recurred one ADR number later.
#
# A procedure written in a comment that no script performs is a procedure that
# does not happen, so the cut performs it. rc cuts do not: a prerelease is not
# what the docs root should serve.
#
# It RETURNS on every failure and never dies, because of WHERE it runs: past
# `git push origin <tag>`. `die` is `exit 1`, and an exit inside a function
# called in the current shell takes the whole script with it — the caller's
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

  log "docs: root now built from $tag (archived $prev), both menus rendered"
}

read_chart_version() { awk '/^version:/{print $2; exit}' "$CHART"; }

bump_chart() { # <chart_version>
  local cv="$1"
  # version + appVersion move in lockstep (ADR 0028).
  perl -0pi -e "s/^version: .*/version: $cv/m; s/^appVersion: .*/appVersion: \"$cv\"/m" "$CHART"
}

# date_the_changelog <chart_version>: GA only — move [Unreleased] to a dated
# section and open a fresh [Unreleased]. rc keeps [Unreleased] untouched.
date_the_changelog() {
  local cv="$1" today; today="$(date -u +%F)"
  grep -q '^## \[Unreleased\]' "$CHANGELOG" || die "CHANGELOG has no [Unreleased] section"
  perl -0pi -e "s/^## \\[Unreleased\\]/## [Unreleased]\n\n## [$cv] - $today/m" "$CHANGELOG"
}

# create_prepare_pr: opens the release-prep PR. Extracted so --self-test can
# assert the argv against a gh stub.
#
# --label skip-changelog is load-bearing, not tidiness. changelog-guard.yaml
# triggers on every pull_request to main and exempts only that label, and on an
# rc the prepare PR touches Chart.yaml but deliberately leaves [Unreleased]
# alone, and it adds no fragment either: the fold consumes the pending ones
# rather than creating any. So the guard finds nothing recorded and fails the
# PR. FLAKE_RE does not match "records no changelog entry" (correctly: it is not
# a flake), so wait_sha_green returns RED and the cut dies. Release-prep is
# exactly the case the label documents. The guard runs on pull requests to
# release-* too, so a patch prepare PR needs the label just the same.
create_prepare_pr() { # <branch> <title> <body> [base]
  gh pr create --repo "$REPO" --base "${4:-main}" --head "$1" --title "$2" --body "$3" --label skip-changelog
}

run_gates() { # <tag> <base>
  local tag="$1" base="$2" s ok=0 out rc
  # nullglob: without it an empty glob leaves the literal pattern, bash exits
  # 127 on it, and the cut dies reporting "gate FAIL check-*.sh".
  shopt -s nullglob
  local -a gates=("$ROOT"/scripts/check-*.sh)
  shopt -u nullglob
  # A gate renamed out of check-*.sh silently leaves the cut's gate set with no
  # signal at all, so assert the floor.
  [ "${#gates[@]}" -ge "${MIN_GATES:-20}" ] || {
    echo "gate set shrank to ${#gates[@]} (expected >= ${MIN_GATES:-20}) — a check-*.sh was renamed or removed" >&2
    return 1
  }
  for s in "${gates[@]}"; do
    # Capture rather than discard: a bare "gate FAIL <name>" during a cut is
    # unrecoverable, since $logf does not exist until after the tag.
    # check-changelog-entry.sh compares [Unreleased] with its base, origin/main
    # by default. A patch cut builds on release-X.Y, whose [Unreleased] holds the
    # entries an earlier rc folded, while main's stays empty since fragments
    # replaced hand edits. Against main, a GA that dates [Unreleased] reads as
    # "no entry" (empty before, empty after), so the gate gets the cut's base.
    if [[ "$s" == *chart-version-matches-tag* ]]; then out="$(bash "$s" "$tag" 2>&1)"
    elif [[ "$s" == *check-changelog-entry* ]]; then out="$(bash "$s" "origin/$base" 2>&1)"
    else out="$(bash "$s" 2>&1)"; fi
    rc=$?
    if [ "$rc" -ne 0 ]; then
      printf 'gate FAIL %s\n%s\n' "$(basename "$s")" "$out" >&2; ok=1
    elif [[ "$out" == *"gate skipped"* ]]; then
      # A gate that has no question to ask at cut time must not be reported as
      # a PASS: 6/6 PASS for 5 gates that ran is false confidence.
      log "gate SKIP $(basename "$s") — not applicable at cut time"
    else
      log "gate PASS $(basename "$s")"
    fi
  done
  return $ok
}

confirm_tag() { # <tag>
  [ "${ASSUME_YES:-0}" = 1 ] && { log "confirmation: --yes"; return 0; }
  local ans
  printf '\033[1;33mTag and publish %s? This is irreversible. Type the tag to confirm: \033[0m' "$1"
  read -r ans
  [ "$ans" = "$1" ] || die "confirmation did not match (got '$ans') — aborting before tag"
}

# ---- main flow -------------------------------------------------------------

main() {
  # Initialize the confirmation flag HERE so an exported ASSUME_YES already in the
  # operator's shell or CI env can never silently bypass confirm_tag (M1). Only
  # --yes, parsed below, may set it.
  ASSUME_YES=0
  local arg version="" dry=0 resume=0
  for arg in "$@"; do
    case "$arg" in
      --self-test) self_test; exit $? ;;
      --dry-run)   dry=1 ;;
      --yes)       ASSUME_YES=1 ;;
      --resume)    resume=1 ;;
      -*)          die "unknown flag: $arg" ;;
      *)           version="$arg" ;;
    esac
  done
  [ -n "$version" ] || die "usage: cut-release.sh <version> [--dry-run] [--yes] [--resume]"

  version="${version#v}"
  valid_version "$version" || die "invalid version '$version' (want X.Y.Z or X.Y.Z-rc.N)"
  local tag cv logf base; tag="$(normalize_tag "$version")"; cv="$(chart_version "$version")"
  logf="$ROOT/.release-$tag.log"
  # Where this cut lands: main for X.Y.0 and its candidates, release-X.Y for
  # every later patch (ADR 0062). Derived from the version rather than taken
  # from the checkout, so a patch can never be cut from main by being on it.
  base="$(release_base "$version")"

  # helm and PyYAML are here because run_gates globs scripts/check-*.sh, and
  # check-migrate-pod-selection.sh renders the chart to compare selectors
  # against the migrate hook's pod labels. Without them the cut fails
  # mid-flight as "gate FAIL check-migrate-pod-selection.sh" rather than as a
  # named missing tool, which is the wrong place to learn it.
  for t in gh jq git helm-docs helm; do command -v "$t" >/dev/null || die "missing tool: $t"; done
  python3 -c 'import yaml' 2>/dev/null || die "missing python module: PyYAML (pip install pyyaml) — several scripts/check-*.sh parse YAML"

  log "cutting $tag (chart $cv, $(is_rc "$version" && echo prerelease || echo GA)) from $base"
  # --tags explicitly: `git fetch origin main` does not follow tags, so a local
  # check alone passes in a clone that never saw a tag pushed from elsewhere —
  # which is exactly the --resume situation.
  git fetch origin --tags -q >/dev/null 2>&1 || warn "could not refresh tags — the 'tag already exists' check below is local-only"
  git rev-parse -q --verify "refs/tags/$tag" >/dev/null 2>&1 && die "tag $tag already exists"

  # A patch needs its release branch to exist. It is created once, from the
  # vX.Y.0 tag, by hand: creating it is a one-time decision about the minor,
  # not a step of every cut. Dry runs report rather than die, so the plan can
  # be previewed before the branch is made.
  local base_ok=1
  if [ "$base" != main ]; then
    git ls-remote --exit-code --heads origin "$base" >/dev/null 2>&1 || base_ok=0
    if [ "$base_ok" = 0 ] && [ "$dry" != 1 ]; then
      die "$base does not exist on origin. Create it from the minor's GA tag first: git push origin v$(minor_of "$version").0^{commit}:refs/heads/$base"
    fi
  fi

  # A patch ships everything main gained since vX.Y.0 unless it was skipped on
  # purpose (scripts/release-gap.sh). Nothing carries a change across on its
  # own, so without this check a fix merged on main can miss the patch
  # silently. --resume skips it: the prepare half already passed it.
  if [ "$base" != main ] && [ "$base_ok" = 1 ] && [ "$resume" != 1 ]; then
    local gap_out gap_rc=0
    gap_out="$("$ROOT/scripts/release-gap.sh" "$version" --fetch 2>&1)" || gap_rc=$?
    if [ "$gap_rc" != 0 ]; then
      printf '%s\n' "$gap_out" >&2
      # 1 is a list of missing commits; anything else means the check did not
      # run, and nothing was verified, so even a dry run stops.
      [ "$gap_rc" = 1 ] || die "release-gap.sh could not run (exit $gap_rc, above), so nothing was checked against main"
      if [ "$dry" = 1 ]; then
        warn "$base is missing commits from main (above); the real cut will refuse until each is cherry-picked or listed in .github/release-skip.txt"
      else
        die "$base is missing commits from main (above): cherry-pick each one, or list it with a reason in .github/release-skip.txt on $base"
      fi
    fi
  fi

  # A release ships docs for every user-facing change it carries, or a
  # recorded reason why not (scripts/docs-gap.sh). The per pull request guard
  # judges each change once, when it is opened; this looks again over the whole
  # release, so a skip nobody justified, or a docs PR that never came, stops
  # the cut instead of reaching users. --resume skips it: the prepare half
  # already passed it.
  if [ "$base_ok" = 1 ] && [ "$resume" != 1 ]; then
    local docs_out docs_rc=0
    docs_out="$("$ROOT/scripts/docs-gap.sh" "$version" --fetch 2>&1)" || docs_rc=$?
    if [ "$docs_rc" != 0 ]; then
      printf '%s\n' "$docs_out" >&2
      [ "$docs_rc" = 1 ] || die "docs-gap.sh could not run (exit $docs_rc, above), so nothing was checked for docs"
      if [ "$dry" = 1 ]; then
        warn "user-facing changes on $base ship without docs (above); the real cut will refuse until each is documented, labelled skip-docs with a 'Skip-docs: <reason>' line, or listed in .github/docs-skip.txt on $base"
      else
        die "user-facing changes on $base ship without docs (above): document each one, label its pull request skip-docs with a 'Skip-docs: <reason>' line, or list it with a reason in .github/docs-skip.txt on $base"
      fi
    fi
  fi

  if [ "$dry" = 1 ] && [ "$resume" = 1 ]; then
    log "DRY RUN (--resume) — no tag, no push:"
    [ "$base_ok" = 1 ] || die "$base does not exist on origin, so there is nothing to resume"
    git fetch origin "$base" -q || die "cannot reach origin"
    local rsha; rsha="$(resume_target "$cv" "$tag" "$base")" || exit 1
    printf '  would tag:     %s at %s\n  chart there:   %s\n  skipped:       prepare, gates, PR, merge (already on %s)\n' \
      "$tag" "${rsha:0:8}" "$cv" "$base"
    exit 0
  fi

  if [ "$dry" = 1 ]; then
    log "DRY RUN — plan only, no branch/commit/PR/tag:"
    # The fragment count is part of the plan, not a detail. The cut CONSUMES
    # .changes/unreleased/, on an rc as well as a GA, and a plan that says
    # "unchanged" while two entries are about to move into the changelog is
    # wrong in the one place someone reads before authorising a release.
    local pending
    pending="$(ls "$ROOT"/.changes/unreleased/*.yaml "$ROOT"/.changes/unreleased/*.yml 2>/dev/null | wc -l | tr -d ' ')"
    # The fragments that matter are the ones on the base, not in this checkout.
    if [ "$base_ok" = 1 ] && git fetch origin "$base" -q 2>/dev/null; then
      pending="$(git ls-tree --name-only "origin/$base" .changes/unreleased/ 2>/dev/null | grep -cE '\.ya?ml$')"
    fi
    local base_note="" from_chart
    [ "$base_ok" = 1 ] || base_note=" (MISSING on origin: create it from v$(minor_of "$version").0 before the real cut)"
    from_chart="$(git show "origin/$base:helm/dexaflow/Chart.yaml" 2>/dev/null | awk '/^version:/{print $2; exit}')"
    printf '  base:          %s%s\n  branch:        release/%s\n  tag:           %s\n  chart version: %s -> %s\n  kind:          %s\n  fragments:     %s\n  changelog:     %s\n' \
      "$base" "$base_note" "$tag" "$tag" "${from_chart:-$(read_chart_version)}" "$cv" \
      "$(is_rc "$version" && echo 'rc, keeps [Unreleased]' || echo 'GA, dates [Unreleased]')" \
      "$pending pending, folded into [Unreleased] and removed" \
      "$(is_rc "$version" && echo "[Unreleased] gains the $pending folded entry(ies)" || echo "[Unreleased] + $pending folded -> [$cv] - $(date -u +%F)")"
    exit 0
  fi

  if ! git diff --quiet || ! git diff --cached --quiet; then die "working tree not clean"; fi
  [ "$(git rev-parse --abbrev-ref HEAD)" = "$base" ] || warn "not on $base (on $(git rev-parse --abbrev-ref HEAD)); the cut builds on origin/$base regardless"
  # Unchecked, a stale origin/<base> feeds both the re-cut guard and the branch
  # this cut is built on. The --resume path already dies here; so should this.
  git fetch origin "$base" -q || die "cannot reach origin"

  local sha
  if [ "$resume" = 1 ]; then
    # A cut is a transaction with an irreversible middle: by the time it waits
    # on the merge commit, the prepare PR is merged, main carries the bump, and
    # gh deleted release/<tag>. If it dies there — #975 refusing to tag on a run
    # that did not finish is the way it dies — re-invoking hits the re-cut guard
    # below ("chart on main is already ..."), which cannot tell an interrupted
    # cut from an accidental re-cut of a released version. That left tagging by
    # hand as the only way forward, which is the manual step this script exists
    # to remove. It happened on v0.4.5-rc.2.
    #
    # The safety property that makes resuming safe already exists in the normal
    # path: main's Chart.yaml at the sha must carry exactly the version being
    # cut. If it does, the prepare half genuinely completed and only the tag is
    # missing. If it does not, this is not an interrupted cut and --resume is
    # the wrong tool. There is deliberately no override for that check.
    git fetch origin "$base" -q || die "--resume: cannot reach origin; refusing to judge $base from a stale clone"
    sha="$(resume_target "$cv" "$tag" "$base")" || exit 1
    log "resume: $base already carries $cv at ${sha:0:8}; picking up at the merge-commit gate"
  else
  # Re-cut guard: the target version must differ from what the base already
  # carries, so `--yes` with a fat-fingered or already-released version can't
  # silently re-cut the current line.
  local cur; cur="$(git show "origin/$base:helm/dexaflow/Chart.yaml" | awk '/^version:/{print $2; exit}')"
  [ "$cv" != "$cur" ] || die "chart on $base is already $cur, nothing to cut (re-cut of the same version?)"
  # A release branch carries one minor for its whole life. A chart of another
  # minor there means the branch was made from the wrong tag.
  if [ "$base" != main ] && [ "$(minor_of "$cur")" != "$(minor_of "$cv")" ]; then
    die "$base carries chart $cur, not a $(minor_of "$cv").x version. Was it created from the wrong tag?"
  fi

  local branch="release/$tag"
  # Safe re-run (M3): a prior failed cut can leave release/<tag> behind (local or
  # remote) + an open PR. Refuse with an explicit cleanup rather than mutating the
  # wrong tree or opening a duplicate PR.
  if git show-ref --verify --quiet "refs/heads/$branch" || git ls-remote --exit-code --heads origin "$branch" >/dev/null 2>&1; then
    die "branch $branch already exists (a prior cut?) — clean it and re-run: git branch -D $branch 2>/dev/null; git push origin :$branch 2>/dev/null; and close/reopen its PR if any"
  fi
  log "prepare on $branch"
  git checkout -b "$branch" -q "origin/$base" || die "could not create $branch off origin/$base"
  bump_chart "$cv"
  # Fold the pending per-PR fragments into [Unreleased] BEFORE dating it (#1200).
  # Contributors write .changes/unreleased/<slug>.yaml instead of editing
  # CHANGELOG.md, so two PRs never touch the same bytes; the cut is where those
  # files become the changelog. On an rc they land in [Unreleased] and stay
  # there; on a GA the dating below carries them into the version section.
  bash "$ROOT/scripts/changelog-fold.sh" "$cv" || die "folding changelog fragments failed"
  is_rc "$version" || date_the_changelog "$cv"
  helm-docs --chart-search-root="$ROOT/helm" >/dev/null 2>&1 || die "helm-docs failed"
  run_gates "$tag" "$base" || die "mechanical gates failed — fix before cutting"

  # -A on .changes because the fold deletes the fragments it consumed, and a
  # plain `git add <dir>` stages additions but not removals: the prepare PR
  # would merge the entries into the CHANGELOG and leave every fragment behind,
  # to be folded in a second time at the next cut.
  git add helm/dexaflow/Chart.yaml helm/dexaflow/README.md CHANGELOG.md
  git add -A .changes 2>/dev/null || true
  git commit -q -m "release: prepare $tag" || die "nothing to commit (already prepared?)"
  git push -u origin "$branch" -q || die "pushing $branch failed"

  local title body
  if is_rc "$version"; then title="release: prepare $tag"; body="Chart version/appVersion -> $cv (ADR 0028 lockstep). rc keeps [Unreleased]."; \
  else title="release: promote $tag GA"; body="CHANGELOG [Unreleased] -> [$cv] - $(date -u +%F); Chart version/appVersion -> $cv (ADR 0028 lockstep)."; fi
  create_prepare_pr "$branch" "$title" "$body" "$base" >/dev/null
  local pr; pr="$(gh pr view "$branch" --json number -q .number)"
  log "prepare PR #$pr — waiting for CI"
  [ "$(wait_sha_green "$(git rev-parse "$branch")")" = GREEN ] || die "PR #$pr CI red (non-flake) — inspect and retry"

  gh pr merge "$pr" --repo "$REPO" --squash --delete-branch --subject "$title" --body "$body" >/dev/null || die "merge failed"
  log "PR #$pr merged"
  sleep 8; git fetch origin "$base" -q
  sha="$(git rev-parse "origin/$base")"
  [ "$(git show "$sha:helm/dexaflow/Chart.yaml" | awk '/^version:/{print $2;exit}')" = "$cv" ] || die "guard: Chart at $sha is not $cv"
  fi

  log "$base CI on merge commit ${sha:0:8}"
  [ "$(wait_sha_green "$sha")" = GREEN ] || die "$base red on the merge commit: NOT tagging"

  confirm_tag "$tag"
  git tag -a "$tag" "$sha" -m "leoflow $tag" || die "creating tag $tag failed"
  git push origin "$tag" || die "pushing tag $tag failed — the tag is local only; 'git push origin $tag' when ready"
  log "tagged $tag @ ${sha:0:8}"
  # Provenance goes down the instant the push returns, never behind a fallible
  # step. The docs promotion used to run here and can spend half an hour on a
  # PR, so a failure there cost the .release-<tag>.log line for an irreversible
  # act AND starved the release watch below — including #862's un-draft, which
  # is what lets a retracted draft release recover on a rerun.
  { echo "tag=$tag sha=$sha date=$(date -u +%FT%TZ) kind=$(is_rc "$version" && echo rc || echo ga)"; } >>"$logf"

  log "release workflows"
  sleep 15
  local reruns=0 j verdict failed rid isflake published=0 rstart=$SECONDS
  local rdeadline="${CUT_WAIT_DEADLINE:-5400}"
  while :; do
    if [ $((SECONDS - rstart)) -gt "$rdeadline" ]; then
      warn "timed out after ${rdeadline}s waiting on the $tag release workflows — inspect: gh run list --branch $tag"
      break
    fi
    # --branch filters server-side, same window problem as wait_sha_green.
    j=$(gh run list --branch "$tag" --limit 100 --json databaseId,status,conclusion 2>/dev/null \
          | jq '[.[]]' 2>/dev/null)
    # Same classification as wait_sha_green — see run_verdict.
    verdict="$(run_verdict "$j")"
    case "$verdict" in
      NONE)     sleep 20; continue ;;
      PENDING)  sleep 45; continue ;;
      GREEN)    log "$tag PUBLISHED"; published=1; break ;;
      BLOCKED*) warn "a $tag release run did not finish (${verdict#BLOCKED }) — the tag is already pushed, so inspect before announcing: gh run list --branch $tag"
                break ;;
      FAILED)   ;; # falls through to the flake path below
      *)        warn "run_verdict returned an unhandled verdict '$verdict' — stopping the watch"
                break ;;
    esac
    failed=$(echo "$j" | jq -r '.[] | select(.conclusion=="failure") | .databaseId')
    # Un-draft BEFORE deciding whether this was a flake, not inside the flake
    # branch (#862 put it there; #979 is why that is not enough).
    #
    # Once the gate retracts, most smokes fail on install.sh's asset download
    # instead of on whatever failed first — and `curl -fsSL` prints nothing on a
    # 404, so install.sh only says "downloading <archive> failed", which matches
    # nothing in FLAKE_RE. isflake flips to 0, the watch stops at "non-flake",
    # and the un-draft below it never runs. The deadlock defends itself: the
    # symptom it creates is exactly what stops the recovery. Re-publishing first
    # costs nothing when the release is already published, and it means the next
    # verdict is computed against a release the smokes can actually download.
    gh release edit "$tag" --repo "$REPO" --draft=false >/dev/null 2>&1 || true
    isflake=1
    for rid in $failed; do
      # An unreadable log is NOT evidence of a non-flake. A job that dies in
      # `Initialize containers` — the service-container pull, this repo's most
      # frequent flake (#1007) — has no step log, so `--log-failed` returns EMPTY
      # and the grep fails. That read "not a flake" and disabled this whole
      # rerun loop for the single failure it most needed to handle (#978).
      lg=$(gh run view "$rid" --log-failed 2>/dev/null || true)
      if [ -z "$lg" ]; then
        # The `|| true` on the GROUP is load-bearing under `set -o pipefail`:
        # the pipeline takes the status of the first failing command, so a `gh
        # api` that 404s makes the assignment nonzero and errexit kills the cut.
        # Measured: without it, a total API failure aborts the whole script.
        lg=$( { gh api "repos/$REPO/actions/runs/$rid/jobs" --jq '.jobs[]|select(.conclusion=="failure")|.id' 2>/dev/null \
                | while read -r jid; do gh api "repos/$REPO/actions/jobs/$jid/logs" 2>/dev/null || true; done; } || true )
      fi
      # Still nothing to read: treat it as UNKNOWN, which for a cut means rerun
      # rather than stop. A rerun is cheap; a stopped cut on a transient is not.
      [ "$(flake_verdict "$lg")" = "real" ] && isflake=0
    done
    if [ "$isflake" = 1 ] && [ "$reruns" -lt 8 ]; then
      reruns=$((reruns+1)); warn "release flake -> rerun #$reruns"
      for rid in $failed; do gh run rerun "$rid" --failed >/dev/null 2>&1; done
      sleep 60; continue
    fi
    warn "release workflows red (non-flake) — inspect the run + gate"; break
  done
  gh release view "$tag" --repo "$REPO" --json tagName,isDraft,isPrerelease,url \
    -q '"release \(.tagName) draft=\(.isDraft) prerelease=\(.isPrerelease) \(.url)"' 2>/dev/null | tee -a "$logf"

  # The docs root is repointed LAST, and only at a release that actually
  # published: the site root must never advertise a tag whose artifacts are red
  # or still draft. Everything above this line is already recorded, so a failure
  # here costs the docs root and nothing else.
  #
  # `published` tracks the workflow verdict, not `isDraft`. Those coincide only
  # because release.yaml's retract step ends in `exit 1`, so a drafted release
  # is always a red run, and its green arm un-drafts. That invariant lives in
  # another file — if that `exit 1` ever goes, check this.
  if ! is_rc "$version"; then
    if [ "${published:-0}" = 1 ]; then
      promote_docs_pr "$tag"
    else
      warn "docs root NOT promoted: the $tag release workflows never reached PUBLISHED, and the root must not point at a release with no artifacts. The tag is pushed — once the release is good, promote by hand (RELEASING.md)."
    fi
  fi
  # ADR 0062: every later patch of this minor is cut from release-X.Y, made
  # from this tag. Printed, not done: it is a one-time decision about the
  # minor, and the operator may want to wait for the release to settle.
  if ! is_rc "$version" && [ "$base" = main ]; then
    log "next: create release-$(minor_of "$version") for this minor's patches: git push origin $tag^{commit}:refs/heads/release-$(minor_of "$version")"
  fi
  log "done — log at $logf"
}

main "$@"
