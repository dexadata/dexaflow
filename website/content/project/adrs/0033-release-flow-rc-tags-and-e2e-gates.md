---
# --- AUTO redirect aliases (build_redirects.py) — do not edit by hand ---
aliases:
  - /adr/0033-release-flow-rc-tags-and-e2e-gates.html
# --- end AUTO redirect aliases ---
title: "ADR 0033: Release Flow — RC Tags, E2E Gates, and Immutable Versions"
linkTitle: "0033 · Release Flow — RC Tags, E2E Gates, and Immutable Versions"
weight: 330
description: "ADR 0033: Release Flow — RC Tags, E2E Gates, and Immutable Versions"
---

**Status:** Accepted
**Date:** 2026-06-01
**Effective from:** `v0.1.0-alpha.1` (first alpha tag); pre-alpha tags keep the
simpler direct-tag flow described in [Pre-alpha exception](#pre-alpha-exception)
below.

## Context

Today (pre-alpha) the release flow is `git tag vX.Y.Z` → `release.yaml` builds
+ publishes + runs `install-smoke` on 7 distros → if smoke fails, `gate` job
retracts the release to a DRAFT. The smoke job only runs `leoflow version` +
`python --version`, which let the v0.0.1-prealpha.21 regression slip through:
multi-DAG workspaces shipped without the materialization fix, and every task
on user-installed instances failed with `ModuleNotFoundError: No module named
'dag'`.

Two gaps to close before alpha:

1. **The gate's E2E surface is too thin.** Smoke runs `--version` only; it
   does not exercise the path the user actually runs (boot lite, register a
   subdir DAG, trigger a task). PR #251 wired the multi-DAG materialization
   contract; PR #C wires the matching gate (`test/e2e/lite-multidag.sh`) that
   would have caught the prealpha.21 break.
2. **There is no opt-in pre-release channel.** A user wanting to validate
   a build BEFORE it becomes "latest" has no way to do so other than
   installing the latest pre-release and discovering bugs in production.
   For alpha, where each tag is a public commitment, this is unacceptable.

## Decision

### 1. Versions are immutable.

A tag, once published (even as a draft), MUST NOT be moved to a different
commit. Re-tagging breaks Sigstore signatures (tied to commit SHA), poisons
mirrors/caches, and violates the universal semver convention. A bad release
becomes a drafted release; the next good release gets the next version.

This mirrors Go modules' approach: `go.mod retract vX.Y.Z` keeps the version
visible-but-discouraged, and the next semver-greater tag becomes the install
default.

### 2. Two-channel release flow, from alpha onwards.

Two tag conventions, distinguished by suffix:

| Suffix | Channel | Visibility | Resolved by `install.sh` "latest"? |
|---|---|---|---|
| `vX.Y.Z-rc.N` | Release Candidate | DRAFT | No (drafts are skipped) |
| `vX.Y.Z` | Final (or `-alpha.N`, `-beta.N`, etc.) | PRE-RELEASE or RELEASE | Yes |

> **Correction — 2026-07-30.** The Visibility column describes a mechanism that
> was never built. An `-rc.N` tag publishes as a **pre-release, not a draft**:
> `.goreleaser.yaml` sets `prerelease: auto`, and every rc since `v0.1.0-rc.3`
> has shipped that way.
>
> The *property* this row exists to guarantee holds regardless — `install.sh`
> resolves through `GET /releases/latest`, which excludes pre-releases by
> definition (`install.sh:77-80` says so in a comment), so a candidate is never
> installed by someone asking for the latest version. The retraction path also
> still works: the gate job flips a published pre-release back to a draft when a
> smoke fails, which is what the workflow does today.
>
> Recorded here rather than by changing the workflow to match the table: the
> table is the stale artifact, and someone reading it might otherwise "fix" a
> release pipeline that is behaving correctly.
>
> **Pre-release is now the intended state, not a tolerated deviation.** A draft is
> visible only to maintainers, so implementing the table as written would erase
> every candidate from the public releases page. Those candidates are part of how
> the project's history reads — `v0.1.0` took four of them, `v0.1.2` needed a
> respin for one defect — and that record is worth keeping. `prune-prealpha.yaml`
> is already scoped to tags containing `-prealpha.` and never touches `-rc.N`, so
> nothing deletes them today. Anyone changing either of these should treat
> candidate visibility as a property to preserve.

**Cutting a release:**

```text
git tag vX.Y.Z-rc.1
git push origin vX.Y.Z-rc.1
  → release.yaml builds + signs artifacts
  → publishes the release as DRAFT (goreleaser draft=true for -rc.N tags)
  → install-smoke runs against the drafted artifacts
  → if any smoke fails: gate keeps the draft; report posted on the tag

Human verifies the rc.N (Lima hands-on, dogfood, whatever the release needs):
  LEOFLOW_VERSION=vX.Y.Z-rc.1 curl -fsSL https://...install.sh | sh
  → Install the explicit tag (drafts are reachable by direct tag URL).
  → Exercise whatever the rc is meant to validate.

If rc.N is green:                  If rc.N is red (Lima found a bug):
  git tag vX.Y.Z                     fix the bug
  git push origin vX.Y.Z             git tag vX.Y.Z-rc.2 (skip rc.1 forever)
  → release.yaml builds + signs      → repeat verification
  → publishes as pre-release/release
  → install-smoke runs (auto gate)
  → if green, becomes "latest"
```

The `rc.N` drafts are never deleted manually — `prune-prealpha.yaml` already
sweeps drafts older than its keep window.

### 3. E2E gates that block both PRs and releases.

Two complementary CI layers, both running `test/e2e/*.sh`:

- **PR-time** (`ci.yaml`): every PR runs the full E2E suite. A failed E2E
  blocks merge. Catches regressions before they reach main.
- **Post-tag** (`release.yaml` install-smoke + an E2E step): the published
  artifacts are exercised end-to-end in a clean container. A failed
  post-tag E2E retracts the release to draft, even if the PR-time E2E was
  green (the artifact in CI ran against `go build`, not the release tarball
  — install paths, packaging, and managed runtime layers are tested only
  here).

**Test suite, as of this ADR:**

| Script | What it gates |
|---|---|
| `test/e2e/lite-login.sh` | The Lite happy path: setup → control plane → admin login → JWT → workspace editor. |
| `test/e2e/lite-multidag.sh` | The multi-DAG materialization contract: subdir DAG → `dag.json.source` carries dag.py verbatim (the property the subprocess executor depends on to materialize per-TI work dirs). |
| `test/e2e/e2e.sh` | The pod-path E2E on k3d (build images, k3d import, agent-over-gRPC, real pod-per-task). Heavy; runs in a separate workflow today, may merge into `release.yaml` install-smoke later. |

The suite grows as new release-blockers are identified. Each test SHOULD be:
fast enough to run on every PR (target <60s), reality-anchored (no mocks at
the boundary it gates), and named to match what it gates (the bug it would
have caught — not how the test happens to be implemented).

### 4. The CHANGELOG records stable versions; the stable section owns the changes.

`CHANGELOG.md` (Keep a Changelog) documents shipped **stable** releases. When
cutting an `-rc.N`, promote `[Unreleased]` → `[X.Y.Z-rc.N]` with the full change
list (so validators have notes). When **promoting** an rc to stable, do NOT leave
a thin `[X.Y.Z]` pointer above the rc section — instead **rename the
`[X.Y.Z-rc.N]` section to `[X.Y.Z]`** (re-dated), consolidating any fixes that
landed between rcs, so the stable version is a single, self-contained record. The
release is what people install, so the release section — not the rc — carries the
changes. RCs stay discoverable via their git tag and the goreleaser-generated
GitHub pre-release notes (`changelog: use: github`), which is where per-rc
granularity belongs; `CHANGELOG.md` need not retain rc sections.

## Consequences

### Positive

- **A regression cannot reach `latest` undetected.** The prealpha.21 break
  could not happen under this flow: PR-time E2E would have failed; even if
  it had merged, install-smoke would have caught it post-tag and drafted the
  release.
- **Users opt in to release candidates.** `LEOFLOW_VERSION=vX.Y.Z-rc.1` is
  explicit; nobody touches `-rc` tags by accident.
- **Versions remain immutable, signatures remain valid.** A retracted draft
  is still verifiable (the artifact, the SHA, the cosign signature) so
  forensics on what failed are preserved.

### Negative

- **One extra step in the release ritual** when cutting a stable: tag `-rc.1`,
  verify, then tag final. This is the cost of explicit gating; the
  alternative is shipping bugs first and apologizing.
- **Drafted versions accumulate.** `prune-prealpha.yaml` already covers this
  (it sweeps drafts > 90 days old, keeping the newest N). No action required;
  documented here for context.
- **The "skip" appearance in stable history**: a bad `v1.2.4` drafted +
  a good `v1.2.5` published is visible from the outside as "v1.2.4 missing
  from latest." This is correct (semver allows non-contiguous sequences) and
  matches what Kubernetes, Node.js, and Postgres ship — but the user
  experience requires release notes on `v1.2.5` that name the drafted
  predecessor when one exists.

### Pre-alpha exception

For pre-alpha tags (`v0.0.1-prealpha.N`), the simpler direct-tag flow stays
in place: tag → build → publish as pre-release → install-smoke → gate
retracts to draft on failure. The two-channel `-rc.N` flow becomes
mandatory **starting at `v0.1.0-alpha.1`**, where the first public alpha
commits to a more careful release ritual.

This exception exists because pre-alpha tags are explicitly experimental and
their churn (several per day during active development) would make a -rc
step per tag a meaningful overhead with little additional value — the E2E
gates run on the direct tag and catch regressions either way.

## Implementation status

- [x] Pre-alpha direct-tag + install-smoke gate (already shipped pre-ADR)
- [x] `test/e2e/lite-login.sh` E2E in PR-time CI (existed before this ADR)
- [x] `test/e2e/lite-multidag.sh` E2E in PR-time CI (this PR)
- [ ] `goreleaser.yml` recognises `-rc.N` tags and publishes as DRAFT
      (deferred to PR D, lands before `v0.1.0-alpha.1`)
- [ ] `release.yaml` install-smoke runs `test/e2e/lite-multidag.sh` against
      the installed artifact (deferred — needs a `--against-installed` flag
      in the script first)
- [ ] Release-notes template that mentions the drafted predecessor when one
      exists (deferred to documentation pass)

## Related

- [ADR 0014 — Supply-chain security](/project/adrs/0014-supply-chain-security/): the
  Cosign + SBOM + Trivy + govulncheck gates that already protect the
  artifact. This ADR adds the *functional* gates on top.
- Memory note `alpha-release-policy`: "first `v0.1.0-alpha.1`
  cut only after user hands-on testing." This ADR formalises that ritual
  via the `-rc.N` convention.
- [PR #251](https://github.com/dexadata/dexaflow/pull/251) — the
  materialization fix this test guards.
