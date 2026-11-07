# Governance

How decisions about Dexaflow are made, who makes them, and how that changes as
the project grows.

## Current model

Dexaflow has a single lead maintainer, listed in [MAINTAINERS.md](MAINTAINERS.md),
who has the final say on what is merged and released. This is the usual model
for a project of this size; it is stated here so that nobody has to guess.

## How decisions are made

- **Day to day:** by pull request. Every change to `main` goes through a pull
  request that passes the required checks; the code owners in
  [`.github/CODEOWNERS`](.github/CODEOWNERS) are asked for review.
- **Design decisions:** by Architecture Decision Record. A decision that
  changes a public contract, the data model, the release process or the scope
  of an edition is written as an ADR under
  `website/content/project/adrs/`, proposed in a pull request, and merged once
  accepted. An accepted ADR is not edited; a later ADR supersedes it.
- **Disagreement:** in the pull request or the issue, in public. When it does
  not converge, the lead maintainer decides and records why.

## Becoming a reviewer or maintainer

- A **reviewer** is someone whose review the maintainers ask for in an area.
  Anyone who has had several non-trivial pull requests merged in an area can be
  invited, or can ask in an issue.
- A **maintainer** can merge. A reviewer who has reviewed consistently in an
  area for a few months and knows its ADRs can be nominated by a maintainer;
  the existing maintainers agree, and the new maintainer is added to
  MAINTAINERS.md and CODEOWNERS in a pull request.
- A maintainer who steps back moves to an emeritus line in MAINTAINERS.md.

## When there are three or more maintainers

The single lead model ends. Decisions that today rest with the lead
maintainer (accepting an ADR, changing this file, adding a maintainer) then
need the agreement of a majority of the maintainers, recorded on the pull
request, and the code owner review required by the `main` branch protection is
turned on.

## Dexaflow and Dexaflow Cloud

Dexaflow is open core. This repository is the open source product, under the
Apache License 2.0, and is complete on its own: everything needed to run it in
production, multi-tenancy included, lives here. Dexaflow Cloud, the managed
service offered by Dexadata, is built on top of it in a separate codebase and
adds the hosted service, billing and plans. Nothing in this repository depends
on Dexaflow Cloud.

## Changing this document

By pull request, like any other change. While the single lead model holds, the
lead maintainer accepts it; afterwards, as described above.
