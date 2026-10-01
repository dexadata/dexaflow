---
# --- AUTO redirect aliases (build_redirects.py) — do not edit by hand ---
aliases:
  - /adr/0014-supply-chain-security.html
# --- end AUTO redirect aliases ---
title: "ADR 0014: Supply Chain Security Stack"
linkTitle: 0014 · Supply Chain Security Stack
weight: 140
description: "ADR 0014: Supply Chain Security Stack"
---

**Status:** Accepted
**Date:** 2026-05-21

## Context

Leoflow targets enterprise adoption. Enterprise procurement requires demonstrable supply chain security: the project must show that it (a) scans for known vulnerabilities in dependencies, (b) catches insecure patterns in its own code, (c) signs releases, and (d) follows industry-standard security disclosure practices.

Beyond procurement, this is simply good engineering. Workflow orchestrators run code with broad privileges (creating pods, accessing secrets, executing arbitrary tasks). A compromised orchestrator is a compromised data platform.

## Decision

Leoflow ships with a layered supply chain security stack, enforced in CI from the first commit:

| Layer | Tool | What it catches | When |
|---|---|---|---|
| Code-aware vuln scanning | govulncheck | Vulnerabilities in Go dependencies that the code actually reaches (no false positives) | Every PR |
| Pattern-based code SAST | gosec | Insecure patterns in our own code (SQL injection, hardcoded creds, weak crypto) | Every PR |
| Container image scanning | Trivy | OS-level CVEs and secrets in the final Docker images | On image build |
| Semantic SAST | CodeQL | Deep static analysis for security issues | Every PR (free for public repos) |
| Project posture | OpenSSF Scorecard | Branch protection, signed releases, pinned actions, etc. | Weekly cron |
| Dependency updates | Dependabot | Automated PRs for outdated dependencies | Continuously |
| Release signing | cosign | Verify provenance of binaries and container images | Every tagged release |
| Public certification | OpenSSF Best Practices Badge | Self-certified compliance with FLOSS best practices | After v0.1.0 release |

## Why Each Tool

### govulncheck (mandatory)

The Go team's official vulnerability scanner. Unlike generic dependency scanners (Trivy, Snyk, Grype), it performs **reachability analysis**: it only flags vulnerabilities in code paths your binary actually exercises. For a project with ~500 transitive dependencies (via `client-go`), this eliminates the dozens of false positives that paranoid scanners produce.

CI fails on any vulnerability rated MEDIUM or higher that is reachable. Lower-severity issues open an issue but do not block.

### gosec

Pattern-based scanner for Go-specific security antipatterns. Catches:

- `database/sql` calls with concatenated strings (SQL injection)
- Hardcoded credentials in source
- Weak crypto algorithms (`md5`, `sha1` for security purposes)
- Unsafe deserialization
- Unhandled errors in security-sensitive paths

Runs as part of `golangci-lint` (see ADR 0012). Failure blocks merge.

### Trivy

Scans the final container images for OS-level CVEs and accidentally-committed secrets. Runs only on tagged image builds, not every PR (to avoid noise from base image updates that we cannot fix).

We accept Trivy's "paranoid mode" false positives at the image layer because the alternatives (NPM scanners, OS package scanners) have similar properties and Trivy is the industry standard.

### CodeQL

GitHub's semantic analysis engine. Runs weekly on the main branch and on every PR for free (public repos). Catches issues that pattern-based scanners miss: taint analysis, control-flow vulnerabilities, deserialization chains.

Configured via `.github/workflows/codeql.yaml` with the `security-extended` query suite.

### OpenSSF Scorecard

Automated scoring of supply chain health: branch protection, signed commits, pinned GitHub Actions versions, no binary artifacts in the repo, fuzzing presence, etc. Runs weekly. The badge appears in the README. A score below 7.0 opens an issue.

### Dependabot

Standard GitHub feature. Configured to:

- Update Go modules weekly
- Update GitHub Actions workflows weekly
- Update Docker base images weekly
- Group minor/patch updates into a single PR; security updates always separate

### cosign for release signing

> **Note (2026-09, #1031).** `leoflow/python-runtime:*` below is the repository
> name as decided in 2026-05 and it no longer exists. The task base images are
> published as `ghcr.io/dexadata/leoflow-runtime:py<version>`, one leg per
> entry in the `python_version` enum of
> `internal/domain/schemas/leoflow-yaml-schema.json`, and rendered in the
> [configuration reference](/reference/configuration/#python-version-support).
> The signing decision itself is unchanged: every published leg is signed
> keylessly on every release. This ADR is deliberately not one of the copies
> `scripts/check-python-runtime-matrix.sh` reconciles, because a decision record
> states what was decided, not what is true today.

Every tagged release (`v0.1.0`, `v0.1.1`, etc.) signs:

1. All published binaries (`leoflow`, `leoflow-server`, `leoflow-agent`)
2. All published container images (`leoflow/python-runtime:*`)
3. The SBOM (CycloneDX format, generated by `syft`)

Keyless signing via Sigstore (no key management for maintainers). Verification documented in the operator guide.

### OpenSSF Best Practices Badge

Self-certified compliance with the FLOSS Best Practices criteria. Pursued **after** the v0.1.0 release, when the project has:

- A published release artifact
- Documentation hyperlinking to the badge
- A SECURITY.md disclosure policy
- A CONTRIBUTING.md
- A CODE_OF_CONDUCT.md
- All MUST criteria satisfied

Target: passing badge for v0.1.0, silver badge for v0.2.0.

## Mandatory Repository Files

These files are part of the security posture and ship with v0.1.0:

| File | Purpose |
|---|---|
| `SECURITY.md` | Vulnerability disclosure process: GitHub private vulnerability reporting (not an email address, and no GPG key — the reporting channel is authenticated and encrypted in transit by GitHub), response-time commitment, supported versions. |
| `CONTRIBUTING.md` | How to contribute, including security review expectations for PRs touching auth, executor, or storage. |
| `CODE_OF_CONDUCT.md` | A short single-maintainer policy. Previously Contributor Covenant 2.1, whose enforcement ladder described a governance structure this project does not have; the OpenSSF badge criterion asks for *a* code of conduct, not a specific text. |
| `LICENSE` | Apache 2.0 (sealed by earlier ADRs). |
| `.github/dependabot.yaml` | Dependabot configuration. |
| `.github/workflows/security.yaml` | govulncheck + gosec + Trivy + CodeQL workflow. |
| `.github/workflows/scorecard.yaml` | Weekly OpenSSF Scorecard run. |
| `.github/workflows/release.yaml` | Signed release pipeline. |

## CI Gate Summary

A PR cannot be merged if any of the following fail:

- `golangci-lint run` (includes gosec, errcheck, staticcheck, etc.)
- `govulncheck ./...` (any reachable MEDIUM+ vulnerability)
- `go test ./...` (any failing test)
- Coverage below the per-phase floor (see ADR 0011)
- Go Report Card grade below A+ (see ADR 0012)
- CodeQL alerts of severity high or critical

## What This Does NOT Cover

- **Runtime defense.** This stack is build-time and pre-deploy. Runtime defenses (admission controllers, network policies, RBAC at the K8s layer) are operator responsibilities, documented in the operator guide.
- **Audit and compliance.** SOC 2 / ISO 27001 / FedRAMP are organizational concerns. This stack provides evidence usable by such audits, but does not produce certifications.
- **Code review discipline.** Tools augment review; they do not replace it.

## Consequences

- A new dependency cannot be added without surviving govulncheck on the next CI run. Vulnerable dependencies are caught at PR time.
- Insecure patterns (SQL injection, hardcoded secrets) cannot reach `main`. AI agents producing such patterns will see CI failures and must remediate.
- Release infrastructure has higher upfront cost (signing setup, SBOM generation) but is one-time work.
- The README will display several badges. Each is backed by a real check, not a sticker.

## Alternatives Rejected

- **"Add security tooling later":** rejected because retrofitting forces ugly remediation. Easier to never introduce the vulnerability than to fix it under deadline pressure.
- **Snyk in place of govulncheck:** rejected because Snyk is commercial, has lock-in, and produces false positives at the volume that govulncheck avoids.
- **No CodeQL:** rejected because it is free and catches a class of issues other tools miss.
- **Manual signing instead of Sigstore/cosign:** rejected because key management is operationally expensive and easy to do wrong.
