---
title: DexaflowResilienceBackstopDisabled
weight: 90
description: What DexaflowResilienceBackstopDisabled means and what to do when it fires.
---

**Symptom.** `dexaflow_resilience_backstop_disabled{config_key} == 1`: the
control plane booted with that backstop off. The boot log has a matching WARN.

**Impact.** A failure the backstop would catch (a lost agent, a pod that never
started, an orphaned run) now leaves work stuck `running` or `queued`.

**First commands.**

```bash
kubectl -n <namespace> logs deploy/<release> | grep -i 'backstop'
helm -n <namespace> get values <release> -a | grep -n '<config_key>'
```

**Decide.** Turn the setting back on unless the change was deliberate and
temporary. If it was, silence this alert for that `config_key` with an
expiry, not forever. See
[Scheduler resilience](/operate/scheduler-resilience/).
