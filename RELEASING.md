# Releasing Dexaflow

Cutting a release is one command: `scripts/cut-release.sh <version>`. The script
owns the mechanical flow so it is not re-derived (and re-broken) by hand each time
(#879). You own the *decisions*: whether to cut, the version, and rc-vs-GA.

## Branches: main is always open, one release-X.Y per minor

Releases follow [ADR 0062](https://dexaflow.dexadata.ai/project/adrs/0062-release-branches-and-open-main/),
the model Kubernetes uses:

- **`main` never freezes.** Every change merges to `main` first, including a
  fix meant for a patch release.
- **`X.Y.0` and its candidates are cut from `main`.** `scripts/cut-release.sh
  0.6.0-rc.1` and `scripts/cut-release.sh 0.6.0` work as they always have.
- **Right after `vX.Y.0` ships, create `release-X.Y` from that tag.** The GA cut
  prints the command:

      git push origin vX.Y.0^{commit}:refs/heads/release-X.Y

- **Every later patch of the minor is cut from `release-X.Y`.** For
  `X.Y.Z-rc.N` and `X.Y.Z` with `Z > 0` the script prepares, merges and tags on
  `release-X.Y`, never on `main`, and stops with the command above when the
  branch does not exist yet. The base branch comes from the version, not from
  your checkout, so a patch cannot be cut from `main` by accident.
- **Fixes reach a release branch only as cherry-picks.** Once the change is
  merged to `main`, open a PR against `release-X.Y` with
  `git cherry-pick -x <sha>` (the `-x` records the original commit), the title
  prefixed `[release-X.Y]`, and a link to the original PR. Carry its changelog
  fragment with it: patch notes are folded from the fragments on the release
  branch. While the project is 0.x, any change that meets the safety bar of
  [ADR 0068](https://dexaflow.dexadata.ai/project/adrs/0068-patch-content-gated-by-safety/)
  may be cherry-picked, features and migrations included; picks keep the
  migration order of `main`.
- **Every PR to `main` names its release as a milestone** (`v0.5.1`,
  `v0.5.2`, ...). The milestone guard check fails a PR without one. A PR
  milestoned for the patch being cut needs its cherry-pick; one milestoned for
  a later release stays on `main` until then.
- **Nothing on `main` is left behind by accident.** `scripts/release-gap.sh
  X.Y.Z` lists every commit on `main` since `vX.Y.0` that `release-X.Y` does
  not carry, ignoring PRs milestoned for a later release and the lines of
  `.github/release-skip.txt` on the release branch (`#N reason` or
  `<sha> reason`, for what will never ship in this minor, such as an ADR).
  The cut runs it and refuses a patch while the list is not empty. The
  commits the cut itself lands on `main` (release prep, docs promotion) never
  count; a Dependabot bump has no milestone, so it shows in the list until it
  is cherry-picked or skipped.
- **Nothing user-facing ships undocumented.** `scripts/docs-gap.sh X.Y.Z`
  lists every commit the release ships since the previous GA (on
  `release-X.Y` for a patch, on `main` for a minor) that changes user-facing
  surface without editing `website/content/`: a chart value, the authoring
  schema, a CLI command, a server setting (`internal/config`), a migration,
  the OpenAPI document, or a changelog fragment of kind Added, Changed,
  Deprecated or Removed. Such a commit passes when its PR (or, for a
  cherry-pick, the original PR on `main`) carries the `skip-docs` label and a
  `Skip-docs: <reason>` line in its description, or when
  `.github/docs-skip.txt` on the branch being cut lists it (`#N reason` or
  `<sha> reason`, for example `#1352 documented in #1477`). The cut runs it
  for every rc and GA and refuses while the list is not empty. The docs guard
  applies the same rule to each PR when it is opened, so the list is normally
  empty; it catches what the per-PR check could not, such as a skip with no
  reason or a docs PR that was promised and never merged.
- **Only the newest release branch takes patches.** An older one gets a
  security fix only when the owner decides so for that fix.

`release-X.Y` is a different name from the short-lived `release/<tag>` prepare
branches the script opens, so the two never collide. Nothing ever merges a
release branch back into `main`.

CI, the changelog and docs guards and the security scans run on pull requests
to `release-*` as they do on `main`, and CI and the security scans also run on
pushes to `release-*`, so the merge commit a patch is tagged from has a run to
gate on.

## How the changelog flows

`CHANGELOG.md` is assembled, not edited. Each pull request records its change
as one fragment file, the cut folds the fragments into the file, and after a
patch the result is brought back to `main`. The contributor side (how to write
a fragment, what not to edit) is in
[Record a change](https://dexaflow.dexadata.ai/contribute/recipes/record-a-change/);
this section is the whole lifecycle.

1. **A pull request to `main` writes a fragment.** `make changelog` (changie)
   writes `.changes/unreleased/<timestamp>-<slug>.yaml` with a `kind` (one of
   the kinds in `.changie.yaml`) and a `body`. A pull request with no
   user-facing change carries the `skip-changelog` label instead.
2. **A cherry-pick carries it to `release-X.Y`.** `git cherry-pick -x` copies
   the fragment with the same file name and the same text. Keep both: the sync
   in step 5 matches the fragment on `main` to what shipped by that name, and
   by its text.
3. **The cut folds the fragments, on the branch it cuts from.**
   `scripts/changelog-fold.sh` renders every pending fragment into
   `## [Unreleased]` and deletes the files, in the prepare commit. An rc stops
   there; its entries wait in `[Unreleased]` for the GA.
4. **A GA dates the section.** `[Unreleased]` becomes `## [X.Y.Z] - <date>`
   and a fresh, empty `[Unreleased]` opens above it.
5. **A patch GA syncs back to `main`.** For `X.Y.Z` with `Z > 0`, steps 3 and
   4 happened on `release-X.Y`, so `main` still has every fragment the patch
   shipped and no `[X.Y.Z]` section. Right after pushing the tag, the cut runs
   `scripts/changelog-sync.sh X.Y.Z --pr`, which opens
   `changelog: sync vX.Y.Z to main` from a branch `changelog/sync-vX.Y.Z`:
   the dated sections of the minor's patches that `main` lacks, copied
   verbatim from the tag in version order, and the deletion of the fragments
   the release consumed. The cut does not wait for it, and its failure never
   fails the cut; merge it once it is green. It depends only on the tag, not
   on the release workflows reaching PUBLISHED: the changelog is true once
   the tag exists. The milestone guard exempts the branch, and
   `release-gap.sh` and `docs-gap.sh` treat its commit as release mechanics.
   When the tag's Release workflow completes green, the Docs promote
   workflow's `changelog-sync` job runs the same command unless the sync pull
   request is already open, so a patch tagged without the cut, as v0.5.4 was,
   gets its pull request too (#1937). Its commit is signed off by
   `github-actions[bot]`, and with the `DOCS_PROMOTE_TOKEN` secret its checks
   start on their own (see [Publishing the docs root](#publishing-the-docs-root-ga)).
6. **`X.Y.0` is cut from `main`.** Steps 3 and 4 happen on `main` itself, so
   there is nothing to sync. This is the cut that would publish an unsynced
   patch's fragments a second time, which is why it checks first (below).

```
 main             release-X.Y
  |  PR + fragment   |
  |---cherry-pick -x-->  same fragment, same file name
  |                  |
  |                  |  cut X.Y.Z (Z > 0): fold, delete fragments, date [X.Y.Z]
  |                  o  tag vX.Y.Z
  |<--sync PR--------'  changelog: sync vX.Y.Z to main
  |   + [X.Y.Z] section, - consumed fragments
  |
  o  cut X.Y+1.0 on main: fold (only what never shipped), date, tag
```

What checks it, and when:

| Check | What it checks | When it runs |
|---|---|---|
| `check-changelog-entry.sh` (CHANGELOG guard) | The pull request adds a fragment or edits `[Unreleased]`, or carries `skip-changelog` | Every pull request to `main` and `release-*` |
| `check-changelog-structure.sh` (CHANGELOG structure) | One `[Unreleased]`, first; every other heading `## [X.Y.Z] - YYYY-MM-DD`, strictly descending; only declared kinds, once each, in order, none empty (from 0.4.7 on); every GA tag of a minor the file records has its section, except the newest tag of each minor while its sync pull request is open; every fragment parses, has a declared `kind` and a non-empty `body`, and is not already an entry of a dated section | Pull requests to `main` and `release-*` that touch the changelog, the fragments or these scripts; and every cut, as one of the `check-*.sh` gates |
| `changelog-sync.sh --check-all` | `main` carries the changelog of the newest patch of every minor: no missing section, no fragment the release consumed | Preflight of every cut. A minor cut (from `main`) refuses; a patch cut warns, because its own sync repairs the earlier ones. Also daily on `main`, and on pushes to `main` that change `CHANGELOG.md` (the Changelog sync workflow, `changelog-sync-check.yaml`): it fails while a patch lacks its sync and no sync pull request is open, and reports an open one as a notice |
| `changelog-fold.sh` | The fold drops no line of the file | Every cut |

### When the changelog drifts

- **The sync pull request failed to open, or was closed unmerged.** Nothing
  about the release is affected. From any checkout:
  `scripts/changelog-sync.sh X.Y.Z --pr --fetch`. It works from the tag in a
  scratch worktree of `origin/main`, never switches your checkout, and
  re-running it updates the same branch and pull request. Or run the Docs
  promote workflow with the tag (`gh workflow run docs-promote.yaml -f
  tag=vX.Y.Z`), which opens it when none is open. The Changelog sync workflow
  fails every day until one is.
- **A patch was synced late or never.** Syncing the newest patch of the minor
  also inserts every earlier section the minor is missing, so
  `scripts/changelog-sync.sh <newest X.Y.Z> --pr --fetch` repairs them all.
  `scripts/changelog-sync.sh --check-all --fetch` says which minor is behind.
- **CHANGELOG structure is red on a pull request.** It names the rule and the
  line. "vX.Y.Z is tagged but has no section" is a missed sync (above).
  "already an entry of a dated section" is a fragment that shipped: delete it
  in the same pull request. A malformed heading or a fragment that does not
  parse is fixed in the pull request that is red.
- **A fragment was renamed on one side.** The sync still removes it when its
  text matches a released entry, and says it matched by content. When a
  fragment of the consumed name is on `main` with different text (edited after
  the pick), the sync keeps it, reports it, and exits 1: decide by hand whether
  it is the shipped entry (delete it) or a new one (keep it, give it a new
  name).
- **Preview without changing anything:** `scripts/changelog-sync.sh X.Y.Z
  --check --fetch` prints what a sync would insert and delete.
  `scripts/changelog-sync.sh X.Y.Z` (no flag) applies the sync to the
  checkout you are in, for a pull request you open yourself.

## The flow the script runs

1. **Preflight**: required tools, clean tree, version validated, rc-vs-GA
   detected from the `-rc.N` suffix, and the base branch picked from the
   version: `main` for `X.Y.0` and its candidates, `release-X.Y` for later
   patches (it must exist on `origin`). Being checked out on another branch
   only warns; the cut builds on `origin/<base>` either way. It also runs
   `scripts/changelog-sync.sh --check-all`: a cut from `main` refuses while
   `main` lacks the changelog of a patch already released, and a patch cut
   warns.
2. **Prepare**: a `release/<tag>` branch off the base. Bump
   `helm/dexaflow/Chart.yaml` `version`+`appVersion` in lockstep (ADR 0028), regenerate the chart README with
   `helm-docs`, fold the pending changelog fragments into `[Unreleased]`, and
   for a **GA** move `CHANGELOG [Unreleased]` to `[X.Y.Z] - <date>`
   with a fresh empty `[Unreleased]` (an **rc** keeps `[Unreleased]`). See
   [How the changelog flows](#how-the-changelog-flows). Run every
   `scripts/check-*.sh` gate against the tag. One of them,
   `check-changelog-entry.sh`, is a pull-request gate with no question to ask
   when there is no pull request, so it reports `gate SKIP`; the cut log
   distinguishes SKIP from PASS to keep the count honest. Another,
   `check-stale-versions.sh`, runs as `--candidate <tag>`: it judges the pages
   against the release being cut, because `versions.json` still names the GA
   that release replaces (on `release-X.Y`, an older one still), and v0.5.4
   was tagged with pages that pinned 0.5.3 (#1934).
3. **PR → wait green → merge**: opens the prepare PR against the base and waits
   for CI, re-running **only known-transient flakes** (registry rate-limits, Go
   module-proxy resets, the shallow-fetch merge-base gate, the cold-start
   `/readyz` timeout) and never hard-failing on them; then squash-merges.
4. **Guard → tag**: verifies the Chart at the merge commit matches the version,
   waits for the base branch's CI on that commit, then, behind an explicit
   **confirmation gate**, tags and pushes.
5. **Sync the changelog to main** (**patch GA** only): right after the tag
   push and the `.release-<tag>.log` line, opens
   `changelog: sync vX.Y.Z to main` (`scripts/changelog-sync.sh X.Y.Z --pr`)
   and moves on without waiting for it. A failure warns and prints the command
   to run by hand; it never fails the cut.
6. **Watch**: writes `.release-<tag>.log` the moment the tag is pushed, then
   follows the tag's release workflows to **PUBLISHED**, un-drafting +
   re-running if the gate retracts on a flake (#862). PUBLISHED means the
   release run is green, which includes its `promote` job (see below).
7. **Publish the docs root** (**GA** only, and only if step 6 reached
   PUBLISHED): runs `scripts/docs-promote.sh <tag> --pr`, which opens a
   `docs/promote-<tag>` PR repointing `website/scripts/ci/versions.json`, or
   finds the one the Docs promote workflow already opened, then waits for it
   green and merges it. It runs
   last on purpose: the Pages deploy checks the tag out, so it cannot ride in
   the prepare commit; and the site root must never advertise a release whose
   artifacts are red or still draft. A failure here costs the docs root and
   nothing else, since the release is already out and logged. The promotion always
   targets `main`, also for a patch tagged on `release-X.Y`, and it leaves the
   root alone when the GA being cut is older than the one the root serves. See
   below.

## What becomes public, and when

A tag stages, the gate verifies, and one job promotes
([ADR 0071](https://dexaflow.dexadata.ai/project/adrs/0071-stage-verify-promote-releases/)).
Everything `release.yaml` pushes before its `gate` job is immutable and
versioned; only the `promote` job, which needs a green gate, moves a pointer
that people follow without naming a version.

| What | Who follows it | Moved by | rc | GA, newest minor | GA, older minor |
|---|---|---|---|---|---|
| Binaries, `dexaflow-server`, `dexaflow-mcp`, `dexaflow-cli`, `dexaflow-migrate` at `vX.Y.Z` and `X.Y.Z`, `dexaflow-runtime:py3.N-vX.Y.Z` | pinned installs, the `setup-dexaflow` action, pipelines | `release`, `migrate-image`, `runtime-image` | yes | yes | yes |
| GitHub release, public, not Latest | pinned `install.sh`, the smokes | GoReleaser (`make_latest: false`) | prerelease | yes | yes |
| Helm chart `X.Y.Z` (`charts/dexaflow`, `charts/leoflow`) | `helm install`, `helm upgrade` | `promote` | yes | yes | yes |
| `dexaflow-runtime:py3.N`, `leoflow-runtime:py3.N` | development builds, the docs | `promote` | no | yes | no |
| GitHub release marked Latest | `install.sh` without a pinned version, Renovate, Dependabot | `promote` | no | yes | no |
| Docs root | the docs site | `docs-promote.yaml` and `cut-release.sh`, after PUBLISHED | no | yes | no |

The `setup-dexaflow` action (`.github/actions/setup-dexaflow`) is used at a
release tag of this repository (`dexadata/dexaflow/.github/actions/setup-dexaflow@vX.Y.Z`),
so the tag publishes it and it needs no job of its own.
`verify-server-signature` checks the `dexaflow-cli` signature, runs the image
(`dexaflow version`, `python3`, `git`, `uv`) and checks that it pulls without
credentials (`scripts/release-check-public.sh`: a warning on an rc, a failure
on a GA) before the gate.

### A new image

GHCR can create a package private on its first push, and every check in
`release.yaml` runs logged in. After the first release that publishes a new
image (`dexaflow-cli` from `v0.5.4-rc.1` on), make its package public before
the GA: the package's settings on GitHub, "Change visibility", Public. The
GA's `verify-server-signature` fails while `dexaflow-cli` is not public.

After an rc, as a follow-up and not a gate, check that the action resolves
its version from its ref: dispatch a workflow that uses
`dexadata/dexaflow/.github/actions/setup-dexaflow@vX.Y.Z-rc.N` with no
`version` input and runs `dexaflow version`. The docs pass `version:`
explicitly because `github.action_ref` is empty in some composite actions
(actions/runner#2473).

"Newest minor" means the tag is not older than any published GA
(`scripts/release-promote-plan.sh`, the same rule the docs root uses).
`promote` first checks with cosign that every image the chart and the moving
tags reference exists and carries this repository's release signature
(`scripts/release-verify-images.sh`), then pushes, signs and verifies the charts, moves
the `py3.N` tags and marks Latest last. Every step is idempotent: a failed
`promote` is recovered by re-running that job alone.

Every un-draft, the gate's and the one `cut-release.sh` does on a flake, passes
`--latest=false`: GitHub marks a release Latest by default when it leaves draft,
which would move the pointer before `promote` ran.

`helm-release.yaml` no longer runs on tags. Dispatch it from a release tag only
when the release run can no longer be re-run and a chart has to be published
again; it runs the same image check first.

## If the cut dies after the prepare PR merged

The cut is a transaction with an irreversible middle. Once the prepare PR is
merged, `main` carries the bump and the `release/<tag>` branch is gone — so a
failure at the merge-commit gate (which is how a cut fails: the tag gate
refuses a run that did not finish) cannot be recovered by re-invoking, because
the re-cut guard sees `main` already carrying the version and cannot tell an
interrupted cut from an accidental re-cut of a released one.

    scripts/cut-release.sh <version> --resume

picks up at the merge-commit gate, on the same base branch the cut used. It
refuses unless the base's `Chart.yaml`
carries exactly the version being cut, which is what proves the prepare half
completed, and it tags the commit that **introduced** that version rather than
`main`'s tip — a chart version is a plateau, so anything merged since the
prepare would otherwise be swept into the tag. When they differ it prints what
it is excluding. There is deliberately no override for the version check: fix
the reason the gate refused, then resume.

Compose it with `--dry-run` first. It prints the sha it would tag and every
commit it is excluding, so you can check both by eye before anything is pushed:

    scripts/cut-release.sh <version> --resume --dry-run

It is a preview of the *target*, not a full preflight — it exits before the
working-tree and on-`main` checks, so a dirty tree still passes it and fails
the real run.

`--resume` refuses a shallow clone outright: `git rev-list` is truncated there,
and at depth 1 it would silently return `main`'s tip. Run `git fetch --unshallow`.

## Publishing the docs root (GA)

`website/scripts/ci/versions.json`'s `latest` entry is the ref the published
site root is built from. A GA repoints it and archives the one it replaces, in
its own PR to `main` **after the tag exists**, because the deploy checks the
tag out. `scripts/docs-promote.sh <tag> --pr` makes that PR, titled
`docs: publish <tag> at the documentation root` from the branch
`docs/promote-<tag>`: `versions.json` repointed (label `<tag> (latest)`), the
`[[params.versions]]` menu of `website/hugo.toml` rendered from it, and the
README install pins bumped. It never takes the root for an rc or for a GA
older than the one the root serves, it works in a scratch worktree of
`origin/main`, and re-running it finds the same branch and PR. If the PR does
not land, the release is still published; the docs root just still serves
the previous GA until it does.

Two things run it (#1935):

- **The Docs promote workflow** (`.github/workflows/docs-promote.yaml`), when
  the Release workflow of a tag push completes green, which includes its
  `promote` job. It enables auto-merge on the PR. This is what moves the root
  when the tag was pushed without the cut, as v0.5.4 was. For a patch GA the
  same run also opens the changelog sync pull request
  ([How the changelog flows](#how-the-changelog-flows), step 5).
- **`cut-release.sh`**, after it watched the release to PUBLISHED. It opens the
  PR or finds the workflow's, waits for it green and merges it.

A PR opened or pushed with the workflow's default `GITHUB_TOKEN` starts no
workflows, so its checks never run. Set the `DOCS_PROMOTE_TOKEN` repository
secret (a fine-grained token with Contents and Pull requests read and write
on this repository) and the workflow uses it instead. Without it the workflow
warns, and a maintainer closes and reopens the PR to start its checks.

To promote by hand, for example when the release workflows were re-run after
the cut gave up: run the Docs promote workflow with the tag
(`gh workflow run docs-promote.yaml -f tag=vX.Y.Z`), or from any checkout
`scripts/docs-promote.sh vX.Y.Z --pr --fetch`. Do not edit `versions.json` by hand: the promotion also renders
`hugo.toml` and bumps the README, and a PR without them fails
`check-docs-version-menu.sh` and `check-stale-versions.sh`.

## Release authorization

The tag + push (the irreversible publish) never happens without an explicit
confirmation: the interactive prompt (type the tag back), or `--yes` passed
deliberately for automation. Everything before the tag — prepare, PR, merge — is
recoverable; nothing is published until you confirm.

## Release freeze and the Actions queue

GitHub Actions has no job priority: release jobs wait behind every pull request
run already queued. The v0.5.3 GA was tagged during a wave of 245 PR runs and
its jobs waited about 50 minutes for a runner (#1685).

- **Freeze PR pushes while a release publishes.** From the go-ahead to publish
  until the release run is green, nobody pushes to pull request branches or
  opens new ones. Pushes to `main` and `release-X.Y` that the cut itself makes
  are the exception.
- **The cut checks the queue before the tag.** Right before the confirmation
  prompt it counts the queued and running workflow runs of the repository and
  refuses above `CUT_MAX_QUEUED` (default 25). Wait for the queue to drain and
  re-run with `--resume`, or pass `--force-queue` when the release cannot wait.
  `--dry-run` reports the same counts.
- **The watch clock starts when a job starts.** `CUT_WAIT_DEADLINE` (default
  5400 s) counts from the first release job that got a runner, not from the tag
  push, so a long queue no longer skips the docs root promotion. The time spent
  queued is logged as `queue_wait_s` in `.release-<tag>.log` and has its own cap,
  `CUT_QUEUE_DEADLINE` (default 14400 s).

## Commands

```bash
# Cut a release candidate (interactive confirm before the tag)
scripts/cut-release.sh v0.4.4-rc.1

# Promote it to GA once the RC is validated in staging
scripts/cut-release.sh v0.4.4

# What main carries that the patch would miss (the cut refuses while non-empty)
scripts/release-gap.sh 0.5.1 --fetch

# User-facing changes the release would ship without docs (the cut refuses while non-empty)
scripts/docs-gap.sh 0.5.2 --fetch

# Does main carry the changelog of every patch already tagged? (a minor cut refuses while not)
scripts/changelog-sync.sh --check-all --fetch

# Publish a GA at the docs root by hand (the Docs promote workflow and the cut do this)
gh workflow run docs-promote.yaml -f tag=v0.5.4
scripts/docs-promote.sh v0.5.4 --pr --fetch

# Bring a patch's changelog back to main by hand (the cut does this after a patch GA)
scripts/changelog-sync.sh 0.5.4 --pr --fetch

# A patch of a minor that already shipped is cut from release-0.5
scripts/cut-release.sh v0.5.1-rc.1

# Preview the plan — no branch, commit, PR, or tag
scripts/cut-release.sh v0.4.4 --dry-run

# Tag even though the Actions queue is busy (the cut refuses by default)
scripts/cut-release.sh v0.4.4 --force-queue

# Non-interactive (skips the confirm prompt — use only in trusted automation)
scripts/cut-release.sh v0.4.4 --yes

# Pure-logic self-test (no network) — also runnable in CI
scripts/cut-release.sh --self-test
```

## rc → GA promotion

Cut `vX.Y.Z-rc.1` first, let the field validate it (especially the upgrade path
and any security-sensitive change), then promote to `vX.Y.Z`. Before the GA,
run the GA rows of `test/release/perf-reliability-checklist.md` and, on a real
cluster, `test/release/rc-cluster-validation.md`. Today the GA tag
rebuilds and re-runs the full gate matrix; lightening that to a build-once /
promote-artifact model is tracked in #878.

## If something goes wrong

- **PR/main red on a non-flake** — the script stops before tagging; inspect the
  run, fix, re-run the command (it refuses if the tag already exists).
- **Release retracted to a draft** — the script un-drafts **before** classifying
  the failure, then re-runs on a flake. That ordering is the fix for #979: the
  un-draft used to sit inside the flake branch, and a retracted release makes
  most smokes fail on the asset download instead — a failure that matches
  nothing in `FLAKE_RE`, so `isflake` went to 0 and the un-draft never ran. The
  deadlock defended itself. If you are watching from the Actions UI rather than
  driving `cut-release.sh`, the gate's step summary prints the two commands:
  `gh release edit <tag> --draft=false`, then re-run the failed jobs once the
  transient clears.
- **The cut stopped saying "non-flake" but the failure looks transient** — check
  whether the failed job died in `Initialize containers`. That step has no log
  for `gh run view --log-failed` to return, so the flake classifier used to read
  the empty output as "no flake pattern matched". It now falls back to the job
  logs API and treats a still-unreadable log as *unknown*, which reruns rather
  than stops (#978, #1007).
- **`promote` failed** (a registry blip, an image that is not signed) — the
  gate already passed, so nothing needs retracting: the release is public but
  not Latest, and no chart was pushed. Fix the cause and re-run the failed
  `promote` job. The job summary says which pointers it moved, and why it left
  Latest and `py3.N` alone for an rc or an older minor.
- **The docs root still serves the previous GA.** Nothing about the release
  fails when the root is left behind, so a separate job watches for it:
  `.github/workflows/docs-root-lag.yaml` runs `scripts/docs-root-lag.sh`
  daily and on every change to `versions.json` on `main`. It fails when the
  `latest` leg names a different tag from the GitHub release marked Latest
  (which only `promote` moves, by the rule of
  `scripts/release-promote-plan.sh`), once that release is more than six
  hours old, and keeps one issue open, titled "docs: the documentation root
  does not serve the newest release", until the root catches up (#1936).
  Promote the release as [Publishing the docs root](#publishing-the-docs-root-ga)
  says.
- **`no merge base`** in the heavy-E2E gate — rebase the branch onto current
  `main` (tracked: #876).

Related: #878 (light promotion gate), #862 (draft fragility), #876 (merge-base gate).
