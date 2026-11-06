---
title: DexaflowMixedVersions
weight: 170
description: What DexaflowMixedVersions means and what to do when it fires.
---

**Symptom.** `dexaflow_build_info` reports more than one `version` across the
release's processes for longer than `thresholds.mixedVersionsFor` (30 m).

**Impact.** A rollout stopped halfway. Old and new replicas disagree on
behavior; a rollback or the next upgrade starts from an unknown state.

**First commands.**

```promql
count by (version, role) (dexaflow_build_info)
```

```bash
kubectl -n <namespace> rollout status deploy/<release>
kubectl -n <namespace> get pods -l app.kubernetes.io/name=dexaflow -o custom-columns=NAME:.metadata.name,IMAGE:.spec.containers[0].image,READY:.status.containerStatuses[0].ready
```

**Decide.** New pods that are not ready usually wait on the schema (see
[DexaflowSchemaNotReady](../schema-not-ready/)) or a failed image pull. Finish
or roll back the rollout; see [Upgrades](/operate/upgrades/).
