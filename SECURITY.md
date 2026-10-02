# Security Policy

## Reporting a Vulnerability

The Dexaflow team takes security issues seriously. We appreciate your efforts to disclose vulnerabilities responsibly.

**Please do NOT open public GitHub issues for security vulnerabilities.**

### How to Report

**Use GitHub's private vulnerability reporting:**
[**Report a vulnerability**](https://github.com/dexadata/dexaflow/security/advisories/new)

The report is visible only to the maintainer until an advisory is published. No
account beyond GitHub is needed, and it threads the discussion, the fix and the
eventual advisory in one place.

There is deliberately no email address here. This file used to publish
`security@leoflow.io` alongside the word "placeholder" — the domain does not
exist and never received anything, so a reporter following the policy reached
nobody. A channel that does not work is worse than no channel: it looks like
diligence and absorbs the report. The same went for the Keybase link.

If GitHub is not an option for you, open a public issue saying only that you
have a security report and asking for a contact — no details — and you will get
a private channel.

### What to Include

Please provide as much of the following as possible:

- Type of vulnerability (e.g., privilege escalation, RCE, authentication bypass)
- Affected component (control plane, agent, executor, CLI)
- Affected version(s)
- Step-by-step reproduction
- Proof-of-concept code or commands
- Impact assessment (what an attacker could achieve)
- Suggested remediation, if any

### Our Commitments

| Action | Timeline |
|---|---|
| Acknowledge receipt of your report | Within 2 business days |
| Initial assessment and severity rating | Within 5 business days |
| Regular status updates | Every 7 days until resolution |
| Public disclosure coordination | After fix is released, typically 30-90 days |

### Severity Ratings

We use CVSS 3.1 to score vulnerabilities:

| Severity | CVSS Score | Response |
|---|---|---|
| Critical | 9.0-10.0 | Fix merged to `main` within 7 days; release cut as soon as it is verified |
| High | 7.0-8.9 | Fix merged to `main` within 14 days; shipped in the next release |
| Medium | 4.0-6.9 | Fixed in the next planned release |
| Low | 0.1-3.9 | Fixed when convenient, noted in the release notes |

The timelines are on **merging the fix**, not on cutting a release, because those
are different decisions here. A release is cut when it has been verified, and
rushing one to satisfy a clock has its own risk. When a fix is on `main` but not
yet released, the advisory says so and you can build from source in the
meantime — the commit is public either way, which is itself a reason not to sit
on the disclosure.

### Supported Versions

**Only the latest release.** Dexaflow is pre-1.0 and ships patches on a single
line (v0.1.0 → v0.1.1 → v0.1.2); there is no previous minor to support, and
under SemVer's 0.x contract a breaking change may land between any two releases
(ADR 0037). Backporting to an older tag is not offered.

Upgrading to the latest release is the remediation. If that is not possible for
you, say so in the report — a targeted patch may be possible, but it is a
case-by-case answer rather than a policy.

This section previously promised support for "the previous minor version, for 90
days", which described a versioning shape the project does not have.

## Recognition

We maintain a Security Hall of Fame acknowledging researchers who have responsibly disclosed vulnerabilities. With your permission, we credit you publicly when we publish the fix.

## Out of Scope

The following are NOT considered security vulnerabilities:

- Findings from automated scanners without proof of exploitability
- Reports requiring physical access to the host machine
- Reports requiring the attacker to already have administrative privileges
- Vulnerabilities in third-party dependencies that we cannot fix (we will route these upstream)
- Issues in development/example DAGs included for documentation purposes

## Public Disclosure

We follow coordinated disclosure. We will publish a GitHub Security Advisory and a CVE (when applicable) only after a fix is available, unless evidence of active exploitation requires earlier disclosure to protect users.
