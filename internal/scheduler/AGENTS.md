# internal/scheduler/

Rules for changing the scheduler: planning, trigger rules, the state machines,
retries, pools and admission, and leader election. The root
[AGENTS.md](../../AGENTS.md) applies too. Read
[ADR 0051](https://dexaflow.dexadata.ai/project/adrs/0051-separate-orchestration-and-execution-state-machines/)
(orchestration and execution state machines),
[ADR 0053](https://dexaflow.dexadata.ai/project/adrs/0053-admission-and-placement/)
(admission and placement) and
[ADR 0066](https://dexaflow.dexadata.ai/project/adrs/0066-weighted-pool-slots-and-resource-unit/)
(weighted pool slots) before changing the area they cover.

- **The transition tables are the authority.** `taskTransitions` and
  `dagRunTransitions` in `state_machine.go` list every legal state change.
  `state_machine_test.go` keeps its own copy and checks every pair
  (`TestCanTransitionExhaustive`, `TestCanTransitionDagRunExhaustive`), so a
  new transition changes both files in the same PR, on purpose.
- **A task failure and a lost attempt are counted apart.** The task's own
  failure spends `try_number`; a fault of the substrate (eviction, a lost pod,
  a lost dispatch) is re-placed on a bounded budget of its own and does not
  (ADR 0051). `infra_replace_ceiling_test.go` pins the longest re-place delay,
  which the executor's orphan-run reaper must stay above.
- **Every per-DAG decision is keyed by tenant and `dag_id`.** Two tenants may
  own the same `dag_id`; run creation, `max_active_runs` and `max_active_tasks`
  are counted per tenant (`tenant_scope_test.go`, #209).
- **Pools count slots, not tasks.** Occupancy is the sum of `pool_slots` of the
  queued and running task instances, an unset `pool_slots` weighs 1, a task
  that does not fit stays `scheduled`, and `max_active_tasks` still counts
  tasks (`pool_weight_test.go`). A large task that waited past the starvation
  threshold reserves its pool (`pool_reservation_test.go`), and the re-plan
  after eager promotion never charges a task twice
  (`eager_promotion_charges_test.go`).
- **What the fake store cannot show, an integration test does.** The unit
  tests here run against fakes that model the state machine but not time or
  SQL. A guard that lives in a query's `WHERE` clause (a backoff, an attempt
  fence, a tenant predicate) is proven by a `//go:build integration` test in
  `internal/storage` (`make test-integration`).
- **Decision logic gets an algorithm review.** A change to planning, trigger
  rules, transitions, retries, pools, admission or leader election is read
  against its ADR and its tests, edge cases included (review 2 in the root
  AGENTS.md).
- **Performance is measured, not assumed.** The tick benchmarks are in
  `perf_bench_test.go`; `test/load/BASELINE.md` has the command and
  `test/release/perf-reliability-checklist.md` (row B1) the bar against the
  previous release. A change to the tick also needs a soak run for its
  release candidate (`make soak`, review 8), and a performance PR is reviewed
  by a specialist before it merges (root section 2).
