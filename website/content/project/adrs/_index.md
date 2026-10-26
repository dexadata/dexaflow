---
# --- AUTO redirect aliases (build_redirects.py) — do not edit by hand ---
aliases:
  - /adrs.html
# --- end AUTO redirect aliases ---
title: Architecture Decision Records
linkTitle: ADRs
weight: 10
description: The why behind Leoflow design decisions — one record per decision, immutable once accepted.
cascade: { type: docs }
---

The *why* behind Leoflow's design. ADRs are immutable once accepted. To
record a new decision, start from the [template](#template) at the end of
this page.

- [ADR 0001: Why Leoflow and Not Apache Airflow's KubernetesExecutor](/project/adrs/0001-why-leoflow/)
- [ADR 0002: Pod-per-Task Execution Model](/project/adrs/0002-pod-per-task/)
- [ADR 0003: DAG-as-Image with `leoflow.yaml` Abstraction Layer](/project/adrs/0003-dag-as-image/)
- [ADR 0004: Thin Static Go Agent in the Worker Container](/project/adrs/0004-thin-agent/)
- [ADR 0005: Hybrid DAG Authoring with Compile-Time Parsing](/project/adrs/0005-hybrid-dag-authoring/)
- [ADR 0006: XCom with Redis Backend](/project/adrs/0006-xcom-redis/)
- [ADR 0007: Airflow UI Compatibility for the MVP](/project/adrs/0007-airflow-ui-compatibility/)
- [ADR 0008: JWT Authentication with OIDC-Ready Interface](/project/adrs/0008-jwt-auth/)
- [ADR 0009: Leader Election via Postgres Advisory Locks](/project/adrs/0009-leader-election/)
- [ADR 0010: Observability Stack from Day One](/project/adrs/0010-observability/)
- [ADR 0011: Test-Driven Development (Strict)](/project/adrs/0011-tdd-strict/)
- [ADR 0012: Code Quality Standards (Go Report Card A+ as Floor)](/project/adrs/0012-code-quality-standards/)
- [ADR 0013: API Documentation via Scalar, Embedded in the Server Binary](/project/adrs/0013-scalar-api-docs/)
- [ADR 0014: Supply Chain Security Stack](/project/adrs/0014-supply-chain-security/)
- [ADR 0015: Kubernetes as the Sole Container Execution Path (No Docker SDK)](/project/adrs/0015-kubernetes-only-execution/)
- [ADR 0016: Deferrable Tasks (Deferred to v0.3)](/project/adrs/0016-deferrable-tasks/)
- [ADR 0017: UI Static Asset Serving Strategy](/project/adrs/0017-ui-asset-serving/)
- [ADR 0018: UI Custom as Strategic North Star](/project/adrs/0018-ui-custom-north-star/)
- [ADR 0019: Secret Encryption at Rest (Connections)](/project/adrs/0019-secret-encryption-at-rest/)
- [ADR 0020: "Delete DAG" Clears History; Deregister Is Separate](/project/adrs/0020-delete-vs-clear-dag/)
- [ADR 0021: Exposing Variables and Connections to Task Pods](/project/adrs/0021-exposing-variables-connections-to-pods/)
- [ADR 0022: Ephemeral Per-DAG-Run Staging Volume](/project/adrs/0022-ephemeral-per-run-staging-volume/)
- [ADR 0023: DAG Authoring — Config Binding and Override Layers](/project/adrs/0023-dag-authoring-config-binding/)
- [ADR 0024: DAG Parsing via a Structural Shim (No Airflow SDK Dependency)](/project/adrs/0024-dag-parsing-structural-shim/)
- [ADR 0025: Embedded Monaco Web Editor for Leoflow Lite](/project/adrs/0025-lite-embedded-web-editor/)
- [ADR 0026: Lite Datastore — XCom on Postgres, No Redis](/project/adrs/0026-lite-datastore-no-redis/)
- [ADR 0027: Product Editions — Executors and Delivery](/project/adrs/0027-product-editions-executors-delivery/)
- [ADR 0028: Release & Versioning for the Two Editions (One Tag, Two Co-Versioned Artifacts)](/project/adrs/0028-release-versioning-two-editions/)
- [ADR 0029: Lite Datastore Default — Docker Postgres (Managed PG is the Opt-In)](/project/adrs/0029-lite-datastore-default-docker/)
- [ADR 0030: Lite Datastore Auto-Selects — Docker Postgres, or a Managed PG When Docker Is Absent](/project/adrs/0030-lite-datastore-auto-select/)
- [ADR 0031: Scheduler Architecture — Reconciliation Loop, Two-Phase Dispatch, Two-Layer Reaping](/project/adrs/0031-scheduler-architecture/)
- [ADR 0032: Task Return Values Are Not Logged — Only Their Metadata Is](/project/adrs/0032-return-values-not-logged/)
- [ADR 0033: Release Flow — RC Tags, E2E Gates, and Immutable Versions](/project/adrs/0033-release-flow-rc-tags-and-e2e-gates/)
- [ADR 0034: Fan-in / map-reduce — list-of-upstream parameter binding](/project/adrs/0034-fan-in-map-reduce-binding/)
- [ADR 0035: Cloud connector auth — keyless-first; Leoflow is not a key manager](/project/adrs/0035-cloud-connector-auth-keyless-first/)
- [ADR 0036: Airflow 3.X runtime compatibility shim — one model, one policy seam](/project/adrs/0036-airflow-runtime-compat-shim/)
- [ADR 0037: Release version scheme — skip alpha/beta, RC discipline from `v0.0.1`](/project/adrs/0037-release-version-scheme/)
- [ADR 0038: Connector dependency ergonomics — `connectors:` sugar + `dependencies:` escape hatch](/project/adrs/0038-connector-dependency-ergonomics/)
- [ADR 0039: Generated connector catalog with full form fidelity](/project/adrs/0039-generated-connector-catalog/)
- [ADR 0040: Airflow operator + sensor execution — native fast path + generic executor](/project/adrs/0040-airflow-operator-support/)
- [ADR 0041: `leoflow deploy` — pipeline-less promotion from Lite to Pro](/project/adrs/0041-leoflow-deploy-pipelineless/)
- [ADR 0042: dbt support via native-Go manifest rendering](/project/adrs/0042-dbt-support-native-rendering/)
- [ADR 0043: TaskGroup as a first-class construct with split/fused execution](/project/adrs/0043-taskgroup-split-fused-execution/)
- [ADR 0044: dbt multi-project — one project per business domain](/project/adrs/0044-dbt-multi-project-by-domain/)
- [ADR 0045: Secrets reach a task because it declared them](/project/adrs/0045-declared-secret-delivery/)
- [ADR 0046: Coverage — one rule, per package, counting the tests we already wrote](/project/adrs/0046-coverage-policy/)
- [ADR 0047: Deprecate the native inline http_api; run HTTP through the generic pod executor](/project/adrs/0047-deprecate-native-inline-http/)
- [ADR 0048: The control plane executes no user-influenced code or network requests](/project/adrs/0048-no-user-code-in-control-plane/)
- [ADR 0049: Split the API/UI and scheduler into separate roles of one binary](/project/adrs/0049-split-api-and-scheduler-roles/)
- [ADR 0050: Model Context Protocol (MCP) server](/project/adrs/0050-mcp-server/)
- [ADR 0051: Separate the orchestration and execution state machines](/project/adrs/0051-separate-orchestration-and-execution-state-machines/)
- [ADR 0052: Durable task outcome — decouple the task result from report delivery](/project/adrs/0052-durable-task-outcome/)
- [ADR 0053: Admission + placement — one scheduler-side layer for task concurrency and pod assignment](/project/adrs/0053-admission-and-placement/)
- [ADR 0054: Coexistence in a shared, multi-team Kubernetes cluster](/project/adrs/0054-shared-cluster-coexistence/)
- [ADR 0055: Secret scoping and token liveness — scope by declaration, exchange the token, bind it to task liveness](/project/adrs/0055-secret-scoping-and-token-liveness/)
- [ADR 0056: Task-log object sink — native dual-SDK (S3 + GCS), keyless-first](/project/adrs/0056-task-log-object-sink/)
- [ADR 0057: OIDC/SSO authentication with fail-closed tenant pinning](/project/adrs/0057-oidc-sso/)
- [ADR 0058: Warm worker pools — pod-reuse semantics (N:1)](/project/adrs/0058-warm-worker-pools/)
- [ADR 0059: OpenLineage emission from the Go control plane → OpenMetadata](/project/adrs/0059-openlineage-emission/)
- [ADR 0060: External secrets resolution — pod-side, provider-neutral SecretResolver](/project/adrs/0060-external-secrets-resolution/)
- [ADR 0061: Secret locality — private scratch, masked on read](/project/adrs/0061-secret-locality/)
- [ADR 0062: Release branches per minor, main always open](/project/adrs/0062-release-branches-and-open-main/)
- [ADR 0064: Branching and skip: in-pod decision, scheduler-side skip cascade](/project/adrs/0064-branching-and-skip/)
- [ADR 0065: Lite key migration off the published key](/project/adrs/0065-lite-key-migration/)
- [ADR 0066: Weighted pool slots and an operator resource unit](/project/adrs/0066-weighted-pool-slots-and-resource-unit/)
- [ADR 0067: Run control on the MCP, scoped issuer tokens, and Pro source mode](/project/adrs/0067-mcp-run-control-scopes-source-mode/)
- [ADR 0068: Patch content in 0.x is gated by safety, not by kind](/project/adrs/0068-patch-content-gated-by-safety/)
- [ADR 0069: Contributor context is public and tool-agnostic; tool files stay local](/project/adrs/0069-contributor-context/)
- [ADR 0070: Secrets reach a process by environment reference, never rendered into a command string](/project/adrs/0070-secrets-by-env-reference/)
- [ADR 0071: Releases stage, verify, then promote; only a promote job moves public pointers](/project/adrs/0071-stage-verify-promote-releases/)
- [ADR 0072: CodeQL blocks a pull request only for the high or critical alerts it introduces](/project/adrs/0072-codeql-gate-on-introduced-alerts/)
- [ADR 0073: DAG images are built without a container daemon first; a local daemon is the fallback](/project/adrs/0073-daemonless-image-builds/)
- [ADR 0074: CI deploys with named, scoped, revocable API tokens and a built-in deployer role](/project/adrs/0074-scoped-api-tokens/)
- [ADR 0075: The daemonless build of ADR 0073 ships in 0.5.4, and pushed images are read back through the registry](/project/adrs/0075-daemonless-builds-in-0-5-4/)
- [ADR 0076: deploy reuses the image already in the registry under the content label](/project/adrs/0076-reuse-image-by-content-label/)
- [ADR 0077: a pushed build splits into a dependency image and a code layer](/project/adrs/0077-dependency-image-and-code-layer/)
- [ADR 0078: the dependency image is built from wheels without a daemon](/project/adrs/0078-dependency-image-from-wheels/)
- [ADR 0079: the CLI signs in with single sign-on through the server, which stays the only OIDC client](/project/adrs/0079-cli-sso-through-the-server/)
- [ADR 0080: registry credentials from the cloud CLI session, after the docker config](/project/adrs/0080-registry-credentials-from-cloud-cli-sessions/)
- [ADR 0081: the CLI signs in from a machine with no browser through a device sign-in served by Dexaflow](/project/adrs/0081-cli-device-sign-in/)
- [ADR 0082: a warm pool follows the DAG's current version, started when it becomes current and drained when it stops being current](/project/adrs/0082-warm-pool-follows-the-current-version/)
- [ADR 0083: the remote builder runs on Cloud Build first, through the gcloud CLI](/project/adrs/0083-remote-builder-on-cloud-build/)
- [ADR 0084: a template that fails to render fails the task attempt](/project/adrs/0084-template-render-errors-fail-the-task/)
- [ADR 0085: the control plane reads task ServiceAccounts for the register dry run](/project/adrs/0085-control-plane-reads-task-service-accounts/)
- [ADR 0086: a clear on the latest version re-binds a finished run with its task set, and leaves an unfinished run on its own version](/project/adrs/0086-clear-rebinds-a-finished-run-with-its-task-set/)
- [ADR 0087: the local daemon takes the digest from its own push](/project/adrs/0087-local-daemon-digest-from-the-push/)
- [ADR 0088: build args, build secrets and a private package index](/project/adrs/0088-build-args-secrets-and-package-index/)
- [ADR 0089: a warm pool is keyed by the DAG's environment, so versions that share a dependency image and runtime settings share workers](/project/adrs/0089-warm-pools-keyed-by-environment/)
- [ADR 0090: the control plane waits for its schema, and names a database that was emptied, replaced, restored or repointed under it](/project/adrs/0090-database-replaced-under-a-running-release/)
- [ADR 0091: registry login without docker](/project/adrs/0091-registry-login-without-docker/)
- [ADR 0092: CI pipelines deploy with their provider's OIDC token, exchanged for a short session](/project/adrs/0092-ci-deploys-with-oidc-tokens/)
- [ADR 0093: an unavailable Kubernetes API is ridden out for a bounded time, off the dispatch-attempt budget](/project/adrs/0093-unavailable-api-dispatch-window/)
- [ADR 0094: Lite's auto datastore follows the datastore the install is pinned to](/project/adrs/0094-lite-auto-follows-the-pinned-datastore/)

## Template

An ADR records one decision and why it was made; what a large change will
build goes in a [spec](/project/specs/)
([ADR 0069](/project/adrs/0069-contributor-context/)).

**File it** as `NNNN-short-title.md` in this directory, with the next free
number: check open pull requests as well as `main`, because two drafts can
take the same number. Open it as its own pull request, which also adds it to
the list above.

**Status** starts as `Proposed` and becomes `Accepted` when the decision is
taken. From then on the record is not rewritten: a later decision gets a new
ADR that names the one it replaces in its **Relates** line.

```markdown
---
title: "ADR NNNN: <the decision, as a sentence>"
linkTitle: "NNNN · <short title>"
weight: NNNN0
description: "ADR NNNN: <one sentence>"
---

**Status:** Proposed
**Date:** YYYY-MM-DD
**Relates:** ADR <n> (<how it relates>), ...

## Context
The problem and the forces at play, with evidence: issues, incidents,
measurements, the code as it is today.

## Decision
What is decided, stated so a reviewer can check a change against it.

## Alternatives considered
Each option that was rejected, and why.

## Consequences
What becomes easier, what becomes harder, and what follows from it
(specs, migrations, settings, docs).
```
