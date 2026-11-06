---
title: DexaflowAgentLostReapingHeld
weight: 95
description: What DexaflowAgentLostReapingHeld means and what to do when it fires.
---

**Symptom.** The scheduler decision `agent_lost_mass_silence_hold` or
`agent_lost_mass_silence_valve_open` was recorded: at least 10 running
attempts, and at least half of the attempts that heartbeat, went silent past
the agent-lost threshold (90 seconds) in the same window. A real agent loss is
local (a pod, a node), so the agent-lost reaper reads this as heartbeat writes
failing on the control plane and holds instead of failing every running task
([#1624](https://github.com/dexadata/dexaflow/issues/1624)).

**Impact.** While the hold lasts, no attempt is failed as `agent_lost`, so a
task whose agent really died stays `running` until the hold ends. After 10
minutes of continuous mass silence the valve opens
(`agent_lost_mass_silence_valve_open`) and the silent attempts are reaped and
re-placed as infra losses, as they would have been without the guard. Tasks
whose pods are gone are still settled by the pod-lost reaper and the
reconciler.

**First commands.**

```bash
kubectl -n <namespace> logs deploy/<release> --tail=500 | grep -E 'agent-lost|heartbeat|pool'
# Is the pool the heartbeats write through exhausted?
curl -s http://<pod>:<metrics-port>/metrics | grep -E 'dexaflow_db_pool_(acquired|max|empty_acquires|canceled_acquires)'
```

**Decide.**

- The main pool (`pool="main"`) is fully acquired and its canceled acquires
  grow: API traffic holds every connection and heartbeats queue behind it. Set
  `database.agent_max_conns` (Helm `database.agentMaxConns`) so the agent
  handlers get a pool of their own, next to `database.scheduler_max_conns`,
  and look for the API caller that saturates the pool
  ([DexaflowDatabasePoolCanceledAcquires](../database-pool-canceled-acquires/)).
- The agent pool (`pool="agent"`) is exhausted: raise
  `database.agent_max_conns` within what Postgres allows.
- The database is slow or failing over: see the Postgres provider's status;
  the hold clears once heartbeats land again.
- The task pods are really gone (a lost node pool): nothing to do, the valve
  reaps them after 10 minutes. `kubectl get pods -n <tasks-namespace>` shows
  it.

See [Scheduler resilience](/operate/scheduler-resilience/#observability).
