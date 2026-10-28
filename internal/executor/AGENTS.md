# internal/executor/

Rules for changing the executor: pod creation, the pod lifecycle, the
reconciler and reapers, and warm worker pools. The root
[AGENTS.md](../../AGENTS.md) applies too. This directory is security sensitive:
CONTRIBUTING.md ("Security-Sensitive Changes") does not accept changes here from
first-time contributors without a discussion issue first.

- **Pod-per-task is the default, and warm pools are off.**
  `execution.warm_pools_enabled` defaults to false, which keeps one dedicated
  pod per attempt ([ADR 0002](https://dexaflow.dexadata.ai/project/adrs/0002-pod-per-task/),
  `internal/config/execution_test.go`). Turning warm pools on is refused at
  boot unless `auth.agent_token_transport=exchange` and
  `auth.secret_liveness_mode=enforce`, because a warm pod reuses one
  credential across attempts
  ([ADR 0058](https://dexaflow.dexadata.ai/project/adrs/0058-warm-worker-pools/) D2,
  `TestValidateExecutionWarmPoolsRequires*`).
- **A warm pool belongs to one DAG version.** It is keyed by
  `dag_version_id`, so a pod on an old image never runs new code, and an
  inactive version's pods are removed (ADR 0058 D1, `warmpool_test.go`).
  The versions kept are the current version of each unpaused DAG that
  declares `min_idle_workers` and the versions of active runs that are
  still current (ADR 0082,
  `warmCandidates` in `internal/storage`), except a version none of whose
  tasks dispatch would place on a warm worker (#2065), so a deploy, a promote or a
  rollback starts the new pool and drains the old one; the drain never
  deletes a busy worker (`warm_prewarm_integration_test.go`).
- **The termination message is a contract.** `BuildPod` pins
  `TerminationMessagePolicy` and `TerminationMessagePath` because the durable
  outcome record travels there
  ([ADR 0052](https://dexaflow.dexadata.ai/project/adrs/0052-durable-task-outcome/),
  `TestBuildPodPinsTerminationMessagePolicy`).
- **Settles and reapers are fenced on the attempt.** They match
  `(try_number, attempt_epoch)`, so a settle read from a superseded pod is a
  no-op on its replacement
  (`internal/storage/attempt_settle_fence_integration_test.go`).
- **Every Kubernetes call needs a Role rule.** `scripts/rbac-covers-executor.sh`
  reads the `CoreV1().<Resource>(...)` calls in this directory and fails when
  the chart's Role does not permit one. It is a grep: write calls in that shape
  so it can see them.
- **The warm pool tick is measured, not assumed.** The leader runs it every
  warm pool interval. `warmpool_bench_test.go` has its benchmarks,
  `test/load/BASELINE.md` the command and
  `test/release/perf-reliability-checklist.md` (row B1) the bar against the
  previous release. A steady tick allocates nothing per worker, and the
  prewarm pass nothing while the per-tenant cap covers every prewarm floor
  (`warmpool_allocs_test.go`); keep it that way or say why in the PR. A performance PR is reviewed by a specialist before
  it merges (root section 2).
- **Hardening applies to every pod.** The settings that are not configurable
  are pinned by `pod_security_test.go`; a change that relaxes one is a
  security change (review 6 in the root AGENTS.md).

## What only a real cluster proves

Unit tests here assert the pod spec the executor builds; they cannot show what
Kubernetes does with it. Say in the PR which of these a change depends on, and
how it was checked.

- **Kubelet behavior** (a pod's start time, `activeDeadlineSeconds`, the
  termination message a container leaves): the k3d end-to-end suite
  (`make e2e`, `make e2e-timeout`, see `test/e2e/README.md`).
- **NetworkPolicy enforcement and ReadWriteMany volumes:** k3d with kindnet
  cannot enforce either. `.github/workflows/pro-netpol-rwx.yaml` runs them on
  kind with Calico and an NFS StorageClass.
- **Cloud identity, real StorageClasses, node pools, eviction and scale:**
  only the release candidate runbook `test/release/rc-cluster-validation.md`
  covers them, on EKS or GKE.
