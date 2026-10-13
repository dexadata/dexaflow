---
title: "Review checklist: Kubernetes behavior"
linkTitle: Kubernetes behavior
weight: 20
description: Separate what the code and its tests confirm from what only a real cluster can show, and say which one each claim rests on.
---

A unit test can prove the pod spec the executor builds or the manifest the
chart renders. It cannot prove what the API server, the kubelet, the CNI or a
cloud does with them. A review sorts every Kubernetes claim of a change into
one of the groups below and asks for the evidence that group allows.

## Confirmed in code

- [ ] **The pod spec.** `BuildPod` and `BuildWarmPod` in `internal/executor`
  are tested field by field: hardening (`pod_security_test.go`), the
  termination message (`TestBuildPodPinsTerminationMessagePolicy`), labels and
  tokens. A claim about what a task pod carries points at one of these tests.
- [ ] **The rendered chart.** `helm unittest helm/dexaflow` asserts what the
  templates render, and `scripts/helm-template-checks.sh` checks the env vars
  and Secret keys the control plane needs. Neither shows that the cluster
  accepts or honors them.
- [ ] **RBAC covers the calls.** `scripts/rbac-covers-executor.sh` compares the
  executor's `CoreV1()` calls with the chart's Role. A call built another way
  is invisible to it, so check new calls by hand.
- [ ] **The decision logic.** Reconciler, reaper and placement decisions run
  against fakes and informer caches (`reconcile_test.go`, `*_reap_test.go`,
  `warm_placement_test.go`). They show the decision for a given pod state, not
  that a cluster produces that state.

## Needs a cluster

- [ ] **Kubelet and pod lifecycle:** start time, `activeDeadlineSeconds`, the
  termination message a container leaves, a pod killed mid-run. These run in
  the k3d suites (`make e2e`, `make e2e-timeout`, `make chaos-runtime`; see
  `test/e2e/README.md` for what each one asserts).
- [ ] **NetworkPolicy enforcement and ReadWriteMany volumes.** k3d with
  kindnet enforces neither. `.github/workflows/pro-netpol-rwx.yaml` runs them
  on kind with Calico and an NFS StorageClass when the chart changes.
- [ ] **Upgrades in place.** Helm CI installs the previous released chart and
  upgrades it to the change (`.github/workflows/helm-ci.yaml`).
- [ ] **Cloud behavior:** IAM for pods (IRSA, Workload Identity), real
  StorageClasses, node pools, eviction, Autopilot mutating pod specs, and watch
  load at scale. Only the release candidate runbook
  `test/release/rc-cluster-validation.md` covers these, on EKS or GKE.

## What the review records

- [ ] Each claim in the PR names its group and its evidence: a test name, a CI
  job, or a cluster run with its date and cluster.
- [ ] A claim that needs a cluster and has not been run on one is stated as
  unverified, so the release candidate run can pick it up: section 3a of
  `rc-cluster-validation.md` lists the features each candidate shipped.
