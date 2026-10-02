# Releasing Dexaflow

Cutting a release is one command: `scripts/cut-release.sh <version>`. The script
owns the mechanical flow so it is not re-derived (and re-broken) by hand each time
(#879). You own the *decisions*: whether to cut, the version, and rc-vs-GA.

## The changelog fills itself, per PR

Every PR records its own user-facing change under `## [Unreleased]` in
`CHANGELOG.md`, enforced by the **`changelog-guard`** CI job
(`scripts/check-changelog-entry.sh`). So by the time you cut, `[Unreleased]` is
already the complete release note — no scramble to reconstruct it, and no
back-fill "docs(changelog)" PRs (the recurring pattern this gate ends). A PR with
no user-facing change (release-prep, chore, dependabot, docs-only) carries the
**`skip-changelog`** label to bypass the gate.

## The flow the script runs

1. **Preflight** — required tools, clean tree, on `main`, version validated,
   rc-vs-GA detected from the `-rc.N` suffix.
2. **Prepare** — a `release/<tag>` branch: bump `helm/dexaflow/Chart.yaml`
   `version`+`appVersion` in lockstep (ADR 0028), regenerate the chart README with
   `helm-docs`, and for a **GA** move `CHANGELOG [Unreleased]` to `[X.Y.Z] - <date>`
   with a fresh empty `[Unreleased]` (an **rc** keeps `[Unreleased]`). Run every
   `scripts/check-*.sh` gate against the tag. One of them,
   `check-changelog-entry.sh`, is a pull-request gate with no question to ask
   when there is no pull request, so it reports `gate SKIP`; the cut log
   distinguishes SKIP from PASS to keep the count honest.
3. **PR → wait green → merge** — opens the prepare PR and waits for CI, re-running
   **only known-transient flakes** (registry rate-limits, Go module-proxy resets,
   the shallow-fetch merge-base gate, the cold-start `/readyz` timeout) and never
   hard-failing on them; then squash-merges.
4. **Guard → tag** — verifies the Chart at the merge commit matches the version,
   then — behind an explicit **confirmation gate** — tags and pushes.
5. **Watch** — writes `.release-<tag>.log` the moment the tag is pushed, then
   follows the tag's release workflows to **PUBLISHED**, un-drafting +
   re-running if the gate retracts on a flake (#862).
6. **Publish the docs root** (**GA** only, and only if step 5 reached
   PUBLISHED) — opens a `docs/promote-<tag>` PR repointing
   `website/scripts/ci/versions.json`, waits for it green and merges it. It runs
   last on purpose: the Pages deploy checks the tag out, so it cannot ride in
   the prepare commit; and the site root must never advertise a release whose
   artifacts are red or still draft. A failure here costs the docs root and
   nothing else — the release is already out and logged. See below.

## If the cut dies after the prepare PR merged

The cut is a transaction with an irreversible middle. Once the prepare PR is
merged, `main` carries the bump and the `release/<tag>` branch is gone — so a
failure at the merge-commit gate (which is how a cut fails: the tag gate
refuses a run that did not finish) cannot be recovered by re-invoking, because
the re-cut guard sees `main` already carrying the version and cannot tell an
interrupted cut from an accidental re-cut of a released one.

    scripts/cut-release.sh <version> --resume

picks up at the merge-commit gate. It refuses unless `main`'s `Chart.yaml`
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
site root is built from. A GA repoints it and archives the one it replaces —
and the cut opens that as its own PR **after pushing the tag**, because the
deploy checks the tag out and it does not exist until then. If that PR does not
land, the release is still published; the docs root just still serves the
previous GA until it does.

The cut skips the promotion entirely when the release workflows never reach
PUBLISHED — the root must not advertise a tag with no artifacts behind it — and
says so. `--resume` cannot help once the tag exists, so that recovery is by
hand. Three edits to `website/scripts/ci/versions.json`, as one PR labelled
`skip-changelog`:

1. point the `latest` entry's `ref` at the new tag AND set its `label` to
   `latest (<the new tag>)` (the dropdown names the release it points at, and
   `render-version-config.py` reads `label`);
2. for the GA it replaces, set `"archived": true`, adding the entry if it has
   none yet — `id`/`ref`/`subpath`/`label` all the **outgoing** tag, inserted
   directly after the `latest` entry, since the order here is the order of the
   version dropdown;
3. leave every other leg untouched, `dev` included.

That is exactly what `promote_docs_version` does — read it if in doubt.

## Release authorization

The tag + push (the irreversible publish) never happens without an explicit
confirmation: the interactive prompt (type the tag back), or `--yes` passed
deliberately for automation. Everything before the tag — prepare, PR, merge — is
recoverable; nothing is published until you confirm.

## Commands

```bash
# Cut a release candidate (interactive confirm before the tag)
scripts/cut-release.sh v0.4.4-rc.1

# Promote it to GA once the RC is validated in staging
scripts/cut-release.sh v0.4.4

# Preview the plan — no branch, commit, PR, or tag
scripts/cut-release.sh v0.4.4 --dry-run

# Non-interactive (skips the confirm prompt — use only in trusted automation)
scripts/cut-release.sh v0.4.4 --yes

# Pure-logic self-test (no network) — also runnable in CI
scripts/cut-release.sh --self-test
```

## rc → GA promotion

Cut `vX.Y.Z-rc.1` first, let the field validate it (especially the upgrade path
and any security-sensitive change), then promote to `vX.Y.Z`. Today the GA tag
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
- **`no merge base`** in the heavy-E2E gate — rebase the branch onto current
  `main` (tracked: #876).

Related: #878 (light promotion gate), #862 (draft fragility), #876 (merge-base gate).
