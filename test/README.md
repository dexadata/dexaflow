# The test suites, and when each one runs

Dexaflow has several test trees and they answer different questions. This page
says which is which, what each one proves, and when CI runs it, so a
contributor can tell whether the suite that stayed silent was meant to speak.

## The suites

| suite | what it proves | what it does NOT prove | when it runs |
| --- | --- | --- | --- |
| Go unit tests (`internal/`, `pkg/`) | logic, in isolation, with a coverage floor per package | that the pieces work together, or against a real database | every pull request |
| Integration (`//go:build integration`) | the SQL and the storage layer against a real Postgres | anything about Kubernetes | every pull request |
| `test/e2e/lite-*.sh` | Lite boots, serves, and runs a DAG on the subprocess executor | the Pro or Kubernetes paths | every pull request |
| `test/e2e/e2e.sh`, `split-two-process.sh`, `dbt-e2e.sh`, `deploy-e2e.sh` | the Kubernetes paths on k3d: pod-per-task, the two-process split, dbt, and build-then-deploy against a registry | NetworkPolicy enforcement, ReadWriteMany, QoS, or anything k3d's CNI cannot show | every pull request, **unless the gate below skips them** |
| `test/e2e/pro-netpol-rwx.sh` | real NetworkPolicy enforcement and a real RWX volume, on kind with Calico and NFS | cloud-specific behavior | on demand |
| `test/soak/` | that a control plane dispatching for hours stays healthy, and that the cost of a tick does not track table growth | anything about a single request, and it is not an at-most-once proof (see `test/soak/README.md`) | a short harness smoke in CI; long runs are scheduled locally |
| `test/load/` | one cost at one instant | behavior over time | on demand |
| `test/gcp/` | what only a real cloud cluster can answer: enforcing CNI, real node counts, warm pools | anything that k3d already answers, and it costs real money | on demand, never in CI |
| `test/release/` | that the published artifact installs, and installs over a previous install | anything about the code inside it | on a release tag |
| `test/om-contract/`, `test/ui-contract/` | that our API and UI keep the contracts they claim | that the behavior behind the contract is right | every pull request |

## When the heavy Kubernetes jobs are skipped

The four k3d jobs (`operator-e2e`, `split-e2e`, `e2e-dbt`, `deploy-e2e`) cost
about six to eight minutes each. A pull request that touches **only** the paths
below cannot change what they test, so `scripts/ci-heavy-gate.sh` skips them:

```skip-paths
docs/
spec/
test/load/
website/
\.changes/
.*\.md$
```

| path | what it is | why it cannot affect a cluster run |
| --- | --- | --- |
| `docs/` | generated API and CLI reference | rendered output, never executed |
| `spec/` | specification sources | not built into any image |
| `test/load/` | the load harness | a different suite, run on demand |
| `website/` | the documentation site | built and deployed by its own workflows |
| `.changes/` | changelog fragments (see `.changie.yaml`) | inert data the release cut reads |
| `*.md` | prose anywhere in the tree | prose |

Anything else runs them, including `Makefile`, `scripts/`, `.github/` and the
chart. The rule is deliberately one-sided: a file we have not thought about
runs the jobs rather than skipping them.

**This table is checked, not decorative.** The fenced `skip-paths` block above
is read by `scripts/ci-heavy-gate.sh --self-test`, which fails when it and the
gate disagree. Editing one without the other turns the build red, which is the
point: the same list living in prose and in code with nothing between them is
how `.changes/` came to be missing from the gate for a whole release cycle
(#1234).

## Asking the gate a question

```bash
printf '%s\n' README.md .changes/unreleased/x.yaml | scripts/ci-heavy-gate.sh --decide
# skip

printf '%s\n' internal/executor/reconcile.go | scripts/ci-heavy-gate.sh --decide
# run
```

## The gate runs inside each job, on purpose

Not as a `paths:` filter and not as a job-level `if:`. A skipped job reports
nothing, so a failure already recorded on that commit stays red forever:
rerunning a `pull_request` run replays the original event payload, so the
condition evaluates the same way every time and no remedy can clear the check.
`.github/workflows/changelog-guard.yaml` carries that lesson, learned from the
v0.4.7 cut (#1176).
