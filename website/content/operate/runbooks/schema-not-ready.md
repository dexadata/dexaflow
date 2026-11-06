---
title: DexaflowSchemaNotReady
weight: 10
description: What DexaflowSchemaNotReady means and what to do when it fires.
---

**Symptom.** A control plane pod stays up and not ready because the database
schema is not one its binary can serve: absent, behind, dirty, unreadable, or
the database was replaced (ADR 0090).

**Impact.** The pod takes no traffic and, if it would lead, schedules nothing.
`database replaced` is critical: the control plane points at a database that is
not the one it ran on.

**First commands.**

```bash
kubectl -n <namespace> get pods -l app.kubernetes.io/name=dexaflow
kubectl -n <namespace> logs deploy/<release> --tail=20 | grep 'database schema'
kubectl -n <namespace> logs -l app.kubernetes.io/component=migrate --tail=50
```

**Decide.**

- `schema absent` or `schema behind`: the migration Job did not run or failed.
  Read its log and rerun the upgrade.
- `schema dirty`: a migration stopped halfway. Follow
  [The migration Job](/operate/troubleshooting/#the-migration-job).
- `database replaced`: follow
  [Database lost, restored or repointed](/operate/database-lost-restored-repointed/).
