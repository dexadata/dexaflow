---
title: Pools
linkTitle: Pools
weight: 81
description: Share a bounded number of task slots across DAGs, size tasks in slots, and read how full each pool is.
---

A **pool** is a named budget of task slots that tasks from every DAG of a
tenant share. A task in a pool is admitted to `queued` only while its pool has
room for it, so a pool caps how many tasks of a kind run at once across all
DAGs: the warehouse queries that share one database, the jobs that hold a GPU,
the calls to a rate-limited API. Pools are Airflow's pools, with the same
names and fields
([ADR 0053](/project/adrs/0053-admission-and-placement/)), and a task can take
more than one slot
([ADR 0066](/project/adrs/0066-weighted-pool-slots-and-resource-unit/)).

Pools add to the per-DAG limits, they do not replace them. A task is admitted
only when its DAG is under `max_active_tasks` and its pool has room.

## When pools are on

Pools are on by default in the cluster edition and off in Lite.
`scheduler.pools.enabled` (`DEXAFLOW_SCHEDULER_POOLS_ENABLED`) turns them on or
off on any edition; unset, it follows `ui.edition`
([ADR 0097](/project/adrs/0097-cluster-edition-name-and-pools-switch/)). The
Helm chart sets the cluster edition, so a chart install has pools on; set
`DEXAFLOW_SCHEDULER_POOLS_ENABLED=false` through `extraEnv` to run it without
them.

With pools off, the scheduler admits tasks without looking at any pool, and
`/api/v2/pools` answers an empty list, so the Pools screen renders with no
rows. A DAG that names pools runs the same with pools on or off apart from the
gate: nothing in the DAG has to change.

## The default pool

Every tenant has a pool named `default_pool`. A task that names no pool, or
names `default_pool`, is admitted against it. The `default` tenant's
`default_pool` has 128 slots, Airflow's default, so an install that never
touches pools is not throttled by it. A tenant created through the
[operator service API](/reference/configuration/#operator-service-api) gets a
copy of it, or the size given as `default_pool_slots`.

`default_pool` can be resized like any pool but not deleted: a delete answers
`409`.

## Put a task in a pool

Name the pool on the operator or on `@task`, as in Airflow. `default_args`
sets it for every task of the DAG that does not set its own.

```python
from airflow.providers.standard.operators.bash import BashOperator
from airflow.sdk import DAG, task

@task(pool="gpu", pool_slots=2)
def train() -> None: ...

with DAG("nightly", default_args={"pool": "warehouse"}):
    load = BashOperator(task_id="load", bash_command="./load.sh")  # warehouse
    load >> train()                                                  # gpu, 2 slots
```

The pool and the size are recorded on the task instance when its run starts,
and the task instance in the API and the UI reports them as `pool` and
`pool_slots`. A clear takes both from the version the re-run executes.

A task that names a pool its tenant has not defined is not gated by any pool,
so a typo or a pool created later never blocks a DAG. With
`server.pools_read_only` on, such a task is admitted against the tenant's
`default_pool` instead (see [Pools a platform sizes](#pools-a-platform-sizes)).

## Task size: `pool_slots`

`pool_slots` is how many slots of its pool a task takes while it is queued or
running. It defaults to 1, so a pool of 8 slots runs eight tasks of size 1, or
two of size 4. In `dexaflow.yaml` the same value is `size`, per task or as a
DAG default; the most specific one wins:
`tasks.<id>.size` > `pool_slots` in `dag.py` > `defaults.size` > 1. See
[Task size](/author-dags/dag-authoring/#task-size-size-adr-0066) for the YAML.

A task of more than one slot that does not fit waits; it never fails for it.
Smaller tasks cannot keep it out forever: once it has waited longer than
`scheduler.pool_starvation_threshold` (60 seconds by default), the pool admits
no other task until it fits. The oldest waiter wins, one per pool, and tasks of
size 1 never reserve a pool. The scheduler logs
`pool reserved for a task waiting longer than the starvation threshold` with
the pool, run, task and slots when it reserves one.

A task larger than its whole pool waits until the pool grows. The scheduler
logs `task is larger than its whole pool and will wait until the pool grows`
once for it. To refuse such a DAG when it is pushed instead, the platform sets
the tenant's `max_task_pool_slots` through the operator service API: a version
with a task above it is refused at registration.

## A slot as a unit of compute

By default a slot is a count: a pool sized 8 runs 8 slots of tasks, however
much CPU and memory each one asks for. An operator can make a slot a fixed
amount of compute with `executor.unit.cpu` and `executor.unit.memory` (Helm
`executor.unit.cpu` and `executor.unit.memory`), for example `250m` and
`512Mi`. Then:

- A task that declares no CPU or memory gets `pool_slots x unit` as its pod's
  requests and limits, so a task of size 4 gets 1 CPU and 2Gi.
- A task that declares more than `pool_slots x unit` is refused at
  registration, with the size it needs, and at dispatch. With
  `executor.unit.enforce: warn` it is accepted, logged and counted in
  `dexaflow_unit_misfit_total{stage}` instead; use it to find such DAGs before
  you switch to `refuse`.
- No task may be larger than `executor.unit.max_size` (64 by default).

A pool then reads as compute: a pool of 16 slots at 250m and 512Mi holds at
most 4 CPU and 8Gi of task pods. The unit is a cluster feature; the
[configuration reference](/reference/configuration/) lists every key.

## Create and resize pools

The Pools screen is at `/pools` under the UI address. The menu does not list it
yet ([#1930](https://github.com/dexadata/dexaflow/issues/1930)), so open the
address directly. Reading pools needs `read:pool`, which every built-in
role has; creating, resizing and deleting them needs `write:pool`, which
`operator` and `admin` have.

The same operations through the API:

```sh
# Create a pool of 4 slots
curl -X POST "$DEXAFLOW_URL/api/v2/pools" -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"name": "warehouse", "slots": 4, "description": "Shared warehouse"}'

# Resize it; a PATCH without slots keeps the size
curl -X PATCH "$DEXAFLOW_URL/api/v2/pools/warehouse" -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' -d '{"slots": 8}'

# Delete it
curl -X DELETE "$DEXAFLOW_URL/api/v2/pools/warehouse" -H "Authorization: Bearer $TOKEN"
```

`POST` needs `name` and `slots` and answers `201`. Posting a name that exists
updates it. A `PATCH` overwrites the description with the one in the body, as
Airflow does, so send it again to keep it. A pool with 0 slots does not gate
its tasks. Resizing takes effect on the next scheduler tick; tasks already
queued or running are never stopped because a pool shrank, the pool just
admits nothing new until its occupancy is under the new size. Deleting a pool
leaves its tasks ungated, as for a pool that was never defined.

Pools are per tenant: a tenant sees and changes only its own, and two tenants
may each have a pool named `warehouse`.

## Read how full a pool is

`GET /api/v2/pools` and `GET /api/v2/pools/{name}` report each pool in slots,
with each task counted at its `pool_slots`:

| Field | What it counts |
|---|---|
| `slots` | The pool's size. |
| `occupied_slots` | Slots held by queued and running tasks: what the gate counts. |
| `running_slots`, `queued_slots` | Those two parts of `occupied_slots`. |
| `scheduled_slots` | Slots of tasks waiting to be admitted. They hold nothing yet. |
| `open_slots` | `slots - occupied_slots`, never below 0. |
| `deferred_slots` | Slots of deferred tasks, reported for the UI; they hold none. |

Two tasks of size 4 running in a pool of 8 show 8 occupied slots and 0 open.
`scheduled_slots` above 0 with `open_slots` at 0 is a full pool with a queue
behind it. Within one run,
`GET /api/v2/dags/{dag_id}/dagRuns/{dag_run_id}/taskInstances?pool=warehouse`
lists that run's tasks in one pool.

## Pools a platform sizes

When one engine serves many tenants, a tenant's pools are a share of the
cluster, and the tenant must not be able to raise them.
`server.pools_read_only` (Helm `config.poolsReadOnly`) makes the pools API
read-only for every role, tenant `admin` included: create, resize and delete
answer `403` with the detail
`pools are read-only on this server: their slots are managed by the platform operator`.
The platform sizes each tenant's `default_pool` through the operator service
API (`default_pool_slots`) and sets `max_task_pool_slots` to the same number,
so a task that could never fit is refused when it is pushed.

With the option on, a task that names a pool its tenant has not defined is
admitted against the tenant's `default_pool`, and the Pools screen counts it
there, so no pool name can step around the budget. Set it on every role of a
split deployment: the API enforces the lock and the scheduler the fallback. The
chart sets both.

## Related

- [Task size](/author-dags/dag-authoring/#task-size-size-adr-0066) in the DAG
  authoring guide.
- [ADR 0053](/project/adrs/0053-admission-and-placement/): admission and named
  pools.
- [ADR 0066](/project/adrs/0066-weighted-pool-slots-and-resource-unit/):
  weighted slots and the resource unit.
- [ADR 0097](/project/adrs/0097-cluster-edition-name-and-pools-switch/): the
  pools switch.
