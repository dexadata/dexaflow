---
# --- AUTO redirect aliases (build_redirects.py) — do not edit by hand ---
aliases:
  - /adr/0046-coverage-policy.html
# --- end AUTO redirect aliases ---
title: "ADR 0046: Coverage — one rule, per package, counting the tests we already wrote"
linkTitle: "0046 · Coverage — one rule, per package, counting the tests we already wrote"
weight: 460
description: "ADR 0046: Coverage — one rule, per package, counting the tests we already wrote"
---

**Status:** Accepted
**Date:** 2026-07-30
**Supersedes (partial):** the coverage clauses of ADR 0011 (§"Coverage is computed per-package…" and the per-phase table). The rest of ADR 0011 — TDD discipline, what TDD does not mean, mandatory state-machine tests — stands unchanged.

## Context

ADR 0011 says:

> Coverage is computed per-package. Packages below threshold block CI. Excluded from coverage: `cmd/*/main.go`, generated code (sqlc, protobuf), `internal/version`.

CI does something else. It computes **one filtered aggregate** and compares it to a floor of 80, with an exclusion regex naming fourteen production files the ADR never mentions (`repository.go`, `postgres.go`, `scheduler_store.go`, `agent_store.go`, `leader.go`, `tail.go`, `managed_postgres.go`, and others). Two rules coexist: the one written down and the one enforced.

There is a third number in circulation. Measured on 2026-07-30 against `main`:

| Denominator | Value |
|---|---|
| Filtered aggregate, no `-tags integration` — **what CI gates on** | 80.8% |
| Filtered aggregate, with `-tags integration` | 81.5% |
| Raw, no filter, no integration | 60.2% |
| Raw, with integration | 65.9% |
| Promised by ADR 0011 / README | 85% |

Twenty points separate the same code depending on what one chooses not to count, and no document says which number "our coverage" refers to.

## The defect that matters most is not the number

**The coverage job does not run the integration suite.** `ci.yaml` runs `go test -race -coverprofile=… ./...` with no `-tags integration`, so the 189 integration tests — written specifically to exercise the Postgres and Redis glue — contribute **nothing** to the gate. And the same glue files are then removed by the exclusion regex.

They are excluded twice. Counting them moves `internal/storage` from 20.4% to **67.4%** and `internal/xcom` from 30.4% to **58.1%**.

The consequence is worse than an inaccurate number: someone who writes an integration test to cover `storage` watches the metric not move. The gate fails to reward precisely the tests the glue needs, which is a strong incentive pointed the wrong way.

## Decision

**1. Count the integration suite.** The coverage job runs with `-tags integration`. No new tests; stop discarding the ones that exist. Everything below assumes this.

**2. One rule: per package.** The aggregate stops being the gate. An aggregate is gameable by averaging — 80.8% today hides `xcom` at 30% behind `alerts` at 96% — and hiding is the opposite of what a gate is for. The aggregate remains useful as a reported trend.

**3. Three tiers, membership justified per entry.**

| Tier | Rule | Members (2026-07-30) |
|---|---|---|
| **Excluded** | not counted, reason recorded per file | generated code (`proto` 201 fns, `storage/queries` 107 fns), `cmd/*` wiring, `internal/version` |
| **Function-locked** | no package percentage; named functions carrying real logic must have tests | glue: `internal/storage`, `internal/xcom`, `internal/observability` |
| **Floored at 85%** | CI fails below | everything else — the pure-logic packages |

The opaque regex is replaced by a list where each entry states why it is there. A file nobody can justify does not get excluded.

**4. 85% is reachable, not aspirational.** Most of the floored tier is already there: `alerts` 95.7, `connectors` 95.0, `auth` 94.2, `agent` 91.6, `executor` 90.8, `api` 88.9, `dispatch` 87.6, `ui` 85.0. The gap is small and specific: `agentrpc` 84.3, `scheduler` 84.4, `domain` 83.4, `config` 83.1, `secrets` 82.6 are within three points; `logs` 78.9, `setup` 77.8, `workspace` 75.4 need real but bounded work.

**5. `internal/cli` gets a declared exception.** 306 functions at 77.9%, 14k lines, `dev.go` alone at 1,706. It does not fit any onda and will not reach 85% as a side effect of other work. It carries an explicit floor of 78% — a regression gate, not a target — until it is split or given its own effort. Stating that is better than a silent miss.

**6. Coverage rides the work.** A wave that opens a package raises it, rather than coverage being a separate campaign. Tests written by whoever just understood the code are better tests, and a standalone coverage push produces the mock theatre ADR 0011 already warns against.

**7. The floor is 85%, one number, with a dated exception list.**

Per-package floors negotiated individually would make every package's bar a
separate conversation, which is the opposite of consistent. So 85% is the rule.

Eight packages sit below it today, so a flat 85% enforced immediately would land
red. They get named exceptions rather than a softer rule:

| Package | Today | Gap |
|---|---|---|
| `internal/scheduler` | 84.4% | 0.6 |
| `internal/agentrpc` | 84.3% | 0.7 |
| `internal/domain` | 83.4% | 1.6 |
| `internal/config` | 83.1% | 1.9 |
| `internal/secrets` | 82.6% | 2.4 |
| `internal/logs` | 78.9% | 6.1 |
| `internal/setup` | 77.8% | 7.2 |
| `internal/workspace` | 75.4% | 9.6 |
| `internal/cli` | 77.9% | see §5 — separate, larger, and not closing as a side effect |

Each exception is pinned at its measured value, so the package cannot regress
while it waits. Each needs an issue. The list is expected to shrink to `cli`
alone, and an entry that has not moved in two releases is a decision to revisit,
not a permanent floor.

The first five are within 2.5 points and should close as their onda touches them.
The last three are real but bounded work.

**8. Percentage is not applied below 10 functions.** `migrations` is 75% of one
function; a single statement moves it 100 points. Small packages get the
function-lock treatment instead, for the same reason glue does: the number
carries no signal.

## Consequences

**CI will fail on packages that pass today**, since per-package is stricter than an aggregate. The floors above are set from measured values so the change is a small step, not a cliff — but it must land with the floors, not before.

**Glue coverage becomes invisible as a percentage**, deliberately. Chasing a number there produces tests that assert a mock was called. Function-level locks say which behaviour must not regress, which is the honest form of the question.

**The exclusion list becomes a review surface.** Adding a file to it now requires a justification someone reads. That is the point; it is also friction.

**README and ADR 0011's 85% become true**, for the first time, because the denominator is finally stated.

## Alternatives rejected

**Keep the aggregate, raise the floor.** Cheapest, and it preserves exactly the property that makes the current number misleading — one weak package hidden behind several strong ones.

**Enforce 85% everywhere including glue.** Would force mock-heavy tests over Postgres and Kubernetes clients to satisfy an arithmetic target, which ADR 0011 explicitly rejects and which produces tests nobody trusts.

**Chase the raw number (65.9%).** Includes 308 generated functions nobody should test. Optimising it means writing tests for protoc output.

## Open

Nothing blocking. The exception table is the one thing that dates quickly — it is
a snapshot of 2026-07-30 and should be re-measured when the gate lands, not
copied forward on trust.
