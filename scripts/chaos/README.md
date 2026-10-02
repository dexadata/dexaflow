# Chaos dogfood (#231)

The pre-Lima alpha gate. Runs the canonical test suites under host-contract
validation and emits a green/red report. **All green is the alpha bar**; any
red blocks the Lima stress-test build and any tag cut.

## Usage

```bash
# Phase 1 — run on the host (catches contamination, doesn't fix it)
make chaos-dogfood            # default report at /tmp/chaos-report.md
REPORT_FILE=/path/to/out.md \
  make chaos-dogfood          # custom report path

# Phase 2a — run inside a clean python:3.12-slim container
# (clean by construction; no host contamination can leak in)
make chaos-dogfood-docker     # report at ./chaos-report.md (gitignored)
```

Exit codes: `0` all green, `1` any red.

The Dockerized run is the alpha-gate variant — every CI/release-gate
invocation should use it because the host run can produce false greens if
the host happens to be configured the same way the contract expects.

## What's in the harness today

| # | Section | What it checks | Phase |
|---|---|---|---|
| 1 | Fresh-runner contract | `~/.dexaflow/` absent + `leoflow_parser` NOT pip-installed (catches contributor-machine state — F5/#96) | 1 |
| 2 | Go unit tests | `go test ./...` | 1 |
| 2b | Chaos integration (failure injection) | `go test -tags integration -run TestChaos ./internal/storage/` — fast-forwards reaper thresholds and asserts the mid-tick-crash recovery contract end-to-end. Skipped when `DATABASE_URL` is absent. | 2b |
| 3 | Go lint | `golangci-lint run ./...` at the CI-pinned version | 1 |
| 4 | Parser pytest | `cd parser && pytest` | 1 |
| 5 | Runtime pytest | `cd runtime/python && pytest` (skipped if absent) | 1 |

## What's in Phase 2a (here today)

- **Docker isolation** — `scripts/chaos/Dockerfile` builds a
  `python:3.12-slim` image with the pinned Go toolchain + golangci-lint +
  the parser-test runtime deps. The harness runs inside it as the
  non-root `chaos` user; the host repo bind-mounts to `/workspace`.
- Image tag includes the Go + golangci-lint versions so a toolchain bump
  busts the cache deterministically.

## What's in Phase 2b (here today)

- **Failure-injection integration test** — `internal/storage/chaos_integration_test.go`
  exercises the mid-tick-crash recovery contract end-to-end: stages a
  "scheduler died after marking TI queued" state, fast-forwards the
  dispatch-lost + orphan-run reaper thresholds, and asserts both fire in
  sequence (TI → `failed/dispatch_lost`, run → `failed/orphaned`). Runs
  in ~1.5 s against a migrated test PG.
- Harness `run.sh` now invokes `go test -tags integration -run TestChaos
  ./internal/storage/` as a dedicated section, skipping it cleanly when
  `DATABASE_URL` is absent so a host run still produces a useful report.

Scope choice: full bash-driven kill-the-process orchestration is brittle
(signal timing, file locking, port reuse). A Go integration test that
drives the state machine through the same failure shape is deterministic,
fast, and catches the contract regressions the bash version would. The
real-process variant remains an option for 2c/2d if a scenario can't be
expressed at the integration level.

## What's NOT in Phase 2b (next sub-phases)

- **2c — Connector cookbook DAGs** — postgres / mysql / mssql / sqlite /
  redis / http round-trips end-to-end inside the harness.
- **2d — Recurring DAGs** — a `*/3 * * * *` print DAG + a DB-writer DAG
  running for ≥30 min so the scheduler tick is exercised under steady load.
- **Additional chaos scenarios** — agent-lost recovery, PG-down recovery,
  leader failover. The current single scenario is the one PR #215 (#202)
  motivated; the others get their own `TestChaosX` tests as we identify
  bugs they'd have caught.

## Why this exists

The risk Phase 1 catches: a contributor runs the full suite locally, every
test passes, but the build CI runner finds the bug. The maintainer machine
had pip-installed `leoflow_parser` so `leoflow compile` "just worked"
without `dexaflow setup` — the gap was only caught by reading code, not by
running tests (PR #221 self-review). The harness makes "did you actually
run on a clean env?" a checkable question, not a vibe.

The risk Phase 2 will catch: silent regressions in the reaper SLAs and the
recovery contract — easy to introduce by accident, hard to detect without
intentional failure injection.

References: #231 (this issue), #96 (CI install-smoke fresh-runner contract),
PR #221 (the gap that motivated this), `docs/scheduler-resilience.md`.
