---
title: "ADR 0072: CodeQL blocks a pull request only for the high or critical alerts it introduces"
linkTitle: "0072 · CodeQL introduced-alert gate"
weight: 720
description: "ADR 0072: amends ADR 0014's CodeQL policy. A dedicated job fails a pull request that introduces an open CodeQL alert of high or critical security severity; alerts already open on the base branch warn; only checks the rulesets require block a merge."
---

**Status:** Accepted
**Date:** 2026-10-13
**Amends:** ADR 0014 (supply chain security stack), its CodeQL section and the CodeQL line of its CI gate summary. Everything else in ADR 0014 stands.
**Issues:** #494 (this change), #491 (the high alert that sat unnoticed).

## Context

ADR 0014 lists "CodeQL alerts of severity high or critical" among the things
that stop a merge, and says CodeQL is configured in `.github/workflows/codeql.yaml`
with the `security-extended` suite. Neither holds today:

- CodeQL runs as the `CodeQL` job of `.github/workflows/security.yaml`, with the
  `security-extended` and `security-and-quality` suites.
- That job only uploads its results. The analysis is green whatever it finds,
  so nothing stopped a pull request that added a high alert. A `high`
  `go/path-injection` alert in `internal/logs/logs.go` stayed open from the
  repository's first day until a manual triage found it (#491).
- A check stops a merge only when the `protect-main` or
  `protect-release-branches` ruleset lists it as required. ADR 0014's gate
  summary reads as if every check it names were enforced.

Blocking on every open high alert, including those already on the base branch,
would charge an old alert to whichever pull request comes next and would get the
gate switched off within a week.

## Decision

1. **A separate gate job.** `CodeQL high-alert gate` in
   `.github/workflows/security.yaml` runs after the `CodeQL` job on pull
   requests and runs `scripts/codeql-alert-gate.sh`. It reads the open CodeQL
   alerts of the pull request's merge ref and of the base branch with the
   default `GITHUB_TOKEN` (`security-events: read`), so pull requests from forks
   are judged too.
2. **What blocks.** An open alert of high or critical
   `security_severity_level` that is open on the pull request and not on the
   base branch fails the gate. Quality queries carry no security severity and
   never block.
3. **What warns.** An alert already open on the base branch is reported as a
   warning. It is tracked in the Security tab and by the weekly scheduled
   analysis, not charged to the next pull request.
4. **No silent pass.** The gate runs under `always()` and fails when the
   analysis did not succeed, so a failed or cancelled analysis cannot become a
   skipped, and therefore passing, required check.
5. **False positives** are cleared by dismissing the alert with a reason in the
   Security tab and re-running the gate.
6. **Enforcement lives in the rulesets.** A check blocks a merge only when the
   `protect-main` and `protect-release-branches` rulesets list it as required;
   every other check is advisory until it is added there. ADR 0014's gate
   summary states intent, and the rulesets are the source of truth. The gate
   blocks once `CodeQL high-alert gate` is added to both.

ADR 0014's CI gate summary line "CodeQL alerts of severity high or critical"
reads, under this ADR, as "CodeQL alerts of severity high or critical
introduced by the pull request (`CodeQL high-alert gate`)".

## Consequences

- A pull request that introduces a high or critical CodeQL alert goes red, and
  stops merging once the job is required in the rulesets.
- Alerts that predate a pull request do not block it. They need their own
  triage, which this ADR does not schedule.
- The gate depends on the code scanning API. An API outage fails the gate
  rather than passing it.
- `scripts/codeql-alert-gate.sh` is not a `check-*.sh` script: it needs a pull
  request and the network, and `cut-release.sh` runs every `check-*.sh` with
  neither.

## Alternatives considered

- **Block on every open high or critical alert.** Rejected: it fails unrelated
  pull requests for an alert they did not add, and the usual answer to that is
  turning the gate off.
- **Edit ADR 0014 in place.** Rejected: accepted ADRs are immutable, and a new
  decision gets a new ADR.
