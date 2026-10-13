---
title: "Deploy prerequisites & why shortcuts fail"
linkTitle: "Deploy prerequisites"
weight: 15
description: "Every gate dexaflow deploy/push enforces — the exact error, why it exists, and the fix."
---

`dexaflow deploy` (and its explicit-steps sibling `dexaflow push`) is **fail-closed
by design** — it registers a DAG only when the artifact and the caller both meet
every gate below. That is deliberate: a Pro control plane runs task pods with
your DAG's image and credentials, so the gates are the boundary between "code a
teammate wrote" and "code that runs with real access." When a deploy is
rejected, the error tells you which gate and how to satisfy it — this page is
the one place that collects all of them, so you are not re-discovering each one
by trial and error.

{{% alert title="`dexaflow deploy`/`push` is the only supported registration path" color="warning" %}}
There is no `kubectl apply` shortcut for a DAG. `POST /api/v2/dags/{id}/versions`
(what `deploy`/`push` call) is the **only** way a DAG version is registered, and
it re-validates the full spec server-side on every call — the same guardrails
that reject a bad DAG locally reject it again here, so a hand-crafted or
CI-bypassing request cannot sneak an invalid artifact past compile-time checks.
{{% /alert %}}

## The gates

### 1. A configured registry

Pro has no single-node image-import path — Kubernetes pulls images from a
registry, so a Pro deploy needs one, full stop (Lite runs locally and needs
none). Missing `registry:` in `dexaflow.yaml` fails immediately:

```
error: deploy requires a container registry, but none is configured.
  A Pro deploy pushes the DAG image to a registry your cluster can pull from
  (Lite runs locally and needs none). Add to dexaflow.yaml:

      registry:
        url: ghcr.io/<your-org>     # or ECR / Artifact Registry / ACR / private
        image_name: <name>

  Then authenticate your builder once:  docker login ghcr.io
```

**Fix:** add the `registry:` block, then `docker login <registry>` once (builder
auth is separate from the control-plane login — see [gate 5](#5-auth--writedag-rbac-scope)).
See [ADR 0041](/project/adrs/0041-leoflow-deploy-pipelineless/).

### 2. Digest-pinned, immutable artifact — no `:latest`

A DAG is an **immutable artifact**: `dag.json` + a container image, versioned
together ([ADR 0003](/project/adrs/0003-dag-as-image/)). `deploy` captures the
pushed image's **digest** and rewrites it into `dag.json` as `registry/image@sha256:…`
before registering — Pro never resolves a tag at pull time, so there is no
`:latest` drift and no way to point a DAG version at a moving target. This is
automatic; there is nothing to configure, but it explains why re-running the
same tag with different content produces a **new**, distinct artifact rather
than silently replacing the old one.

### 3. Supported task types only

`dexaflow compile` accepts a closed set of task types — `python` (`@task`/
`PythonOperator`), `bash` (`BashOperator`), and `airflow_operator` (any provider
operator/sensor, run through the generic executor — [ADR 0040](/project/adrs/0040-airflow-operator-support/)).
Everything else is a **loud compile-time rejection**, never a silent
mistranslation — including a message shaped like:

```
<construct>: not supported by Dexaflow (supported: Bash, Http, Python/@task; no dynamic task mapping or task groups)
```

The construct named most often in real deploys:

- **`KubernetesPodOperator` — always refused.** Every Dexaflow task already runs
  in its own pod (the pod *is* the execution unit); `KubernetesPodOperator` lets
  a DAG author specify an **arbitrary pod spec** — service account, volumes,
  host networking, capabilities — which would hand the author the exact
  privilege-escalation surface Dexaflow's pod-per-task model exists to contain.
  Wrapping a task in another pod is redundant on top of that, and the author-
  controlled pod spec is precisely the escalation surface Dexaflow retains
  control of. There is no workaround; express the work as `python`/`bash`, or
  as a provider operator via `airflow_operator`.
- **Dynamic task mapping** (`.expand`/`.partial`) — refused; static fan-in works,
  dynamic fan-**out** does not yet.
- **Task groups** (`TaskGroup`, `@task_group`) — refused outside the dbt
  integration's own group construct (`dbt_group()`); a general-purpose `dag.py`
  `TaskGroup` is not supported.

The full, current, authoritative list (including reschedule-mode sensors,
deferrable operators, branching, `PythonVirtualenvOperator`, per-task
`default_args`) lives in
[DAG authoring → Not supported](/author-dags/dag-authoring/#not-supported--dexaflow-compile-rejects-these) —
this page only calls out the ones deploy attempts hit most.

### 4. Non-root task image

A task container whose image runs as **UID 0** is refused by the control
plane's pod security defaults. On Pro, this is the executor's
`taskPodSecurity.runAsNonRoot` Helm value (`true` by default — see
`helm/dexaflow/values.yaml`). When it rejects your image, the **task pod**
fails to start with:

```
CreateContainerConfigError: container has runAsNonRoot and image will run as root
```

This is easy to miss because it surfaces as a **pod** failure, not a `deploy`
CLI error — `deploy` succeeds (the artifact registers fine), and the rejection
only shows up when the scheduler tries to run a task.

**Fix — pick one:**
- End your custom `Dockerfile` with a **numeric** non-root user, e.g.
  `USER 65532:65532` (the published Dexaflow runtime base already does this — you
  only hit this gate with a **custom** Dockerfile that overrides `USER`). A
  *name* (`USER leoflow`) is not enough: the kubelet can only verify a numeric
  UID, so a name is rejected as unresolvable even when it maps to a non-root
  user.
- Or, cluster-wide, an operator sets `taskPodSecurity.runAsNonRoot: false` in
  the Helm values — this is deliberately a cluster-operator switch, not a
  per-DAG one, so a DAG author cannot elevate their own task's privilege.

### 5. Auth + `write:dag` RBAC scope

Registering a version (`POST /api/v2/dags/{id}/versions`, what `deploy`/`push`
call) requires the **`write:dag`** permission on the caller's token:

| Symptom | Cause | Fix |
|---|---|---|
| `401` — `"unauthorized" / "no authenticated user"` | No token, or an invalid/expired one | `dexaflow auth login --server <pro>` once; `deploy` then needs no auth flags |
| `403` — `"forbidden" / "missing permission write:dag"` | A valid token whose role does not grant `write:dag` | Log in as (or ask an admin to grant) a role with `write:dag` — Admin → Users/Roles on the control plane |

Registry auth (`docker login`, step 1) and control-plane auth (`dexaflow auth
login`) are **two separate credentials** — a push failing with `denied`/
`unauthorized` is the *registry* rejecting the builder, not the control plane.

### 6. Private-registry pull (the cluster's half, not just the push)

Pushing successfully is not the same as the **cluster** being able to pull. A
private registry needs the task pod to carry pull credentials:

- **`imagePullSecrets` on the task ServiceAccount** —
  `taskServiceAccount.imagePullSecrets` in the Helm values, referencing an
  existing `docker-registry` Secret (e.g. `regcred`, created with
  `kubectl create secret docker-registry regcred --docker-server=… --docker-username=… --docker-password=…`).
  Kubernetes auto-injects a ServiceAccount's pull secrets into every pod that
  runs as it.
- **The task must actually run as that ServiceAccount** — set
  `execution.service_account: <taskServiceAccount.name>` in the DAG's
  `dexaflow.yaml`. Without it, task pods use the namespace `default` SA, which
  carries no pull secrets, and the pod fails with `ErrImagePull`/`ImagePullBackOff`
  even though `imagePullSecrets` is configured in the chart.
- **On ECR specifically**, you have the same two options infra teams already
  know: a long-lived `regcred` (`aws ecr get-login-password | kubectl create
  secret docker-registry …`, needs periodic rotation — ECR tokens expire), or
  node/pod-level IAM (IRSA / EKS Pod Identity) paired with an ECR credential
  helper so the kubelet authenticates without a static secret. Dexaflow does not
  care which — it only needs the pull to work; either path satisfies this gate.

See the chart-wiring detail in [ADR 0041 → Cluster wiring](/project/adrs/0041-leoflow-deploy-pipelineless/#cluster-wiring-helm--required-for-the-deploy-path-to-actually-pull).

### 7. Where task logs actually live — not `kubectl logs`

`kubectl logs <pod>` shows only the **agent wrapper's** own stderr — it is not
where task output goes. Task stdout/stderr streams from the agent over gRPC to
the control plane's log sink. Read it from:

- **The UI** — the task instance's log drill-down (Airflow-compatible).
- **The API** — `GET /api/v2/dags/{dag_id}/dagRuns/{dag_run_id}/taskInstances/{task_id}/logs/{try_number}`.
- **The CLI** — `dexaflow runs logs <dag_id> <run_id> <task_id> [--try N] [-f/--follow]`,
  landing in v0.4.1 (reads the same endpoint as the UI, so it works over a
  plain `--server`/token session with no cluster access needed).

See [Troubleshooting → Logs](/operate/troubleshooting/#logs) for where the sink
directory and control-plane request logs live.

### 8. The version label and its 409

DAG versions are immutable and unique per `(dag_id, version)`. Registering
the same label with the same content again is a no-op; the same label with
**different** content is a conflict, not an overwrite, and is rejected:

```
server returned 409: {"title":"conflict","status":409,"detail":"inserting version: …"}
hint: version "v1" of this DAG is already registered with different content, and versions are immutable.
```

Without `--dag-version`, the label is derived from the DAG itself:
`<git describe>-<hash>`, where `git describe --tags --always` runs in the
**project directory** (outside git the base is `dev`), and the hash covers what
the DAG is built from:

- the DAG source (`dag.py`, or the dbt project directories), the files under
  `include_paths`, and `dbt.manifest` when set, wherever it lives;
- the whole project directory when the project ships its own Dockerfile,
  which can `COPY` anything, and that Dockerfile itself;
- the project's `.dockerignore`;
- the resolved `dexaflow.yaml` (dependencies, connectors, `python_version`,
  `base_image`, task overrides) and the CLI version.

It leaves out `.git`, what `exclude_paths` and `.dockerignore` keep out of the
build, the compiled `dag.json`, and the files `dbt parse` rewrites on every run
(`target/manifest.json` and its siblings, `logs/`, `.user.yml`) unless
`dbt.manifest` points at one of them. Anything else under `target/` counts,
because it ships in the image. A symlink counts as the path it points to,
never followed. File times and modes never count. A file the label cannot read
stops the compile with its name: add it to `exclude_paths`, or pass
`--dag-version`.

So editing code and deploying again, from a dirty tree or from a folder
outside git, gets a new label, and deploying unchanged content gets the same
one. Every DAG of a monorepo gets its own. The label is also a valid image tag
(characters a tag cannot carry become `-`), since `tag_strategy: version` tags
the image with it. A release tagged `v1.2.3` is therefore pushed as
`:v1.2.3-<hash>` under the default strategy; set `tag_strategy: git_tag` to
tag the image with the git tag itself.

Before building, `dexaflow deploy` asks the control plane whether the DAG
already has a version with the default label. If it does, the content is
already deployed: it prints `already deployed as <label>` and skips the build,
the push and the register (`--trigger` still starts a run). This is what makes
a re-run CI job succeed: rebuilding the same sources would produce a new image
digest under the same label, which the server refuses. If the check cannot be
made (network, auth), the deploy proceeds as before. An explicit
`--dag-version` is never checked.

A 409 can still happen, and the hint names the cause that applies:

- **An explicit `--dag-version` was reused** for different content. Pass a new
  one, or omit it.
- **The hashed inputs are unchanged**, as after `compile --build --push`
  followed by `push` of the same sources: either the image was rebuilt to a
  new digest, or the DAG reads files outside its source and `include_paths`
  (add them to `include_paths`). Pass a new `--dag-version` to register
  anyway.

## From here

- **[Deploy your first Pro DAG](/operate/first-pro-dag/)** — the walkthrough
  these gates protect.
- **[CI/CD & deploy examples](/operate/cicd-deploy/)** — wiring `deploy`/`push`
  into a pipeline, where most of gates 5–8 get automated away.
- **[Troubleshooting & observability](/operate/troubleshooting/)** — symptoms
  once a DAG is already running.
