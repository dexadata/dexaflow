---
# --- AUTO redirect aliases (build_redirects.py) — do not edit by hand ---
aliases:
  - /adr/0041-leoflow-deploy-pipelineless.html
# --- end AUTO redirect aliases ---
title: "ADR 0041: `leoflow deploy` — pipeline-less promotion from Lite to Pro"
linkTitle: "0041 · `leoflow deploy` — pipeline-less promotion from Lite to Pro"
weight: 410
description: "ADR 0041: `leoflow deploy` — pipeline-less promotion from Lite to Pro"
---

**Status:** Accepted — **shipped**. `leoflow deploy`/`push` is implemented
(`internal/cli/deploy.go`), end-to-end tested (`test/e2e/deploy-e2e.sh`), and
was the registration path used in the v0.4.0-rc.4 EKS validation. The registry
requirement, digest-pinning, `--platform` cross-build default, and the
[Cluster wiring](#cluster-wiring-helm--required-for-the-deploy-path-to-actually-pull)
Helm changes below are all live. The rc.2-era markers left in the sections
below ("target v0.0.2-rc.2", the `feat/deploy` branch-sync note) are the
historical record of the branch this shipped on, not open work; whether the
**Tier 2** (Dockerfile-free, yaml-driven build) happy path described under
"Two-tier happy path" is the current default is tracked in the DAG-authoring
docs, not restated here.
**Date:** 2026-06-04
**Companions:** ADR 0003 (DAG as immutable artifact), ADR 0019 (secret encryption), ADR 0021 (AIRFLOW_CONN delivery), the editions split (Lite/Pro)

## Context

Today, promoting a DAG to a Pro control plane is **three CLI calls** —
`leoflow compile --build --push --image …` then `leoflow push dag.json --server …`
(after a `leoflow auth create-token`). CI automates them; by hand it is awkward.
A user who built and tested a DAG in **Lite** (the local loop) and wants it in
**Pro** but has **no CI pipeline yet** has no single, ergonomic command.

This ADR defines **`leoflow deploy`**: one command that promotes a DAG (or a whole
workspace) from a developer's machine to a Pro control plane, **without a
pipeline**, reusing the existing primitives (`compile`, `--build`, `--push`,
`push`). It is the manual happy path the `first-pro-dag` walkthrough already
describes, collapsed into one verb.

## Decision

### Split by edition, not by cluster topology
- **Lite** (`leoflow lite`) runs locally and needs **no registry** — unchanged.
- **`leoflow deploy` targets Pro and REQUIRES a registry.** We do **not** add a
  single-node image-import path for Pro (it complicates our side — per-node
  containerd trust). Kubernetes pulls images from a registry; a Pro deploy needs
  one, full stop.

### Registry is mandatory for deploy — fail loudly when missing
`leoflow deploy` without a configured `registry:` is a **loud, actionable error**,
never a silent attempt:

```
error: deploy requires a container registry, but none is configured.
  A Pro deploy pushes the DAG image to a registry your cluster can pull from
  (Lite runs locally and needs none). Add to leoflow.yaml:

      registry:
        url: ghcr.io/<your-org>     # or ECR / Artifact Registry / ACR / private
        image_name: <name>

  Then authenticate your builder once:  docker login ghcr.io
```

This is **documented as a hard requirement** of the command (docs + the error
itself). It aligns with the standing loud-reject-over-silent principle.

### What lives where
- **`leoflow.yaml`** (per-DAG, part of the artifact): `registry:` (`url`,
  `image_name`, `tag_strategy`). Optional for Lite (ignored locally), required for
  deploy.
- **NOT in the yaml** (environment, not per-DAG): the target control plane
  `--server` and the auth token. The same DAG deploys to staging and prod by
  changing only `--server`. Provided via flag, `LEOFLOW_TOKEN` env, or a persisted
  session (`leoflow auth login`).

### Two auths, kept separate
1. **Registry auth** (image push) — the **builder's** credential (`docker login`,
   or cloud helpers: `aws ecr get-login`, `gcloud auth configure-docker`,
   `az acr login`). Leoflow never stores registry credentials; it shells out to
   `docker push`. Leoflow is registry-agnostic.
2. **Control-plane auth** (register `dag.json`) — a JWT bearer token. Precedence:
   `--token` (default from `LEOFLOW_TOKEN`) → `--username`/`--password` (deploy
   fetches a token via `/auth/login`) → a persisted session from `leoflow auth login`.

### `leoflow auth login` ships alongside `deploy`
`leoflow auth login --server <pro>` stores the token in `~/.leoflow/config`, so
`leoflow deploy` then needs **no auth flags**. This is what makes the pipeline-less
loop fluid (login once, deploy many). It is nested under `auth` (sibling of
`auth create-token`) to disambiguate from `docker login` (registry auth, which
Leoflow never handles); it prompts for the password interactively (hidden) when
not given, so the secret never lands in shell history.

### The CLI is runtime-independent (Lite and CI share it)

`deploy`, `login`, `compile`, and `push` are **client-side by contract**: they talk
to a remote control plane via `--server` + a token and require **no local runtime**
— no `leoflow lite`, no embedded server, no datastore. They already live in the
root command's *authoring* group (`internal/cli/root.go`), distinct from the
*runtime* group (`lite`/`server`). This is what lets the exact same commands run in
two settings with no special-casing:

- **From Lite** — a developer who has `leoflow lite` up still invokes `deploy`/
  `login` as plain client calls against the Pro `--server`.
- **From a CI pipeline, without Lite** — a runner that only has the `leoflow`
  binary runs `leoflow auth login` + `leoflow deploy` (journey 3); it never starts a
  local runtime.

**Packaging note (deferred optimization).** The single `leoflow` binary already
serves both — CI simply calls the authoring subcommands. A **slim, client-only
build** (build-tag-gated to exclude `lite`/`server`/the SPA, shrinking the CI
image) is a *packaging* optimization, not a requirement, and is deferred until
binary size is a real pain. The contract above (commands are runtime-independent)
is what keeps that option open.

### Deploy scope — single, by id, or all
- `leoflow deploy [path]` (default `.`) — the DAG project in that directory.
- `leoflow deploy <dag_id>` — resolve the id to its subdir in a multi-DAG
  workspace and deploy that one.
- `leoflow deploy --all` — every DAG in the workspace (reuses `DiscoverProjects`).
  **Best-effort**: build+push+register each, print a per-DAG summary, and exit
  non-zero if **any** failed (so CI catches it) — partial pushes are not rolled
  back (images are content-addressed; a re-deploy is idempotent).

### Behavior + UX
- **Registers only** by default (the artifact is published; scheduled DAGs run on
  their schedule). `--trigger` opts into kicking a run immediately.
- **Confirmation** when interactive and the server is non-loopback (a real Pro):
  `Deploy <dag> → <server>? [y/N]`. `--yes` skips it (CI/automation).
- **Rebuilds** the image by default (simple, deterministic); `--skip-build`
  reuses an already-built image (skips docker build/push) but still recompiles
  `dag.json` from `leoflow.yaml`/`dag.py` — the spec is always refreshed.
- **Pins by digest:** the push captures the image digest and writes
  `registry/img@sha256:…` into `dag.json` — Pro pulls **exactly** the bytes that
  were built; there is no `:latest` lookup. The first deploy *registers* the
  artifact, which is how Pro learns it; subsequent deploys register new versions,
  and "latest" in Pro means the newest registered artifact, each pinning its own
  digest.
- **Tag strategy:** `tag_strategy: version` (the config default) tags by the DAG
  version label (`git describe`); `tag_strategy: git_sha` tags by the immutable
  short commit sha (`git rev-parse --short HEAD`), falling back to the version
  label when the project is not in git. (Either way the artifact is then re-pinned
  by digest, so the tag is a convenience, not the integrity boundary.)
- **Output:** a clear per-phase summary —
  `built … @sha256 · pushed … · registered <dag> <version> → <server>/dags/<dag>`.

### Target architecture — cross-build by default (Mac-safe)

The common dev setup is **macOS/arm64**; the common Pro cluster is **Linux/amd64**.
A local `docker build` inherits the host arch (`internal/cli/compile.go:182` passes
no `--platform`), so a Mac-built image would crash the task pod with
`exec format error`. Decision:

- **`leoflow deploy` defaults to `--platform linux/amd64`** — it builds for the
  cluster, not the laptop. On Docker Desktop this "just works" (bundled QEMU/binfmt
  cross-builds with no setup); the runtime base is already published multi-arch
  (`release.yaml` → `linux/amd64,linux/arm64`), so the cross-build resolves.
- **`--platform` is overridable:** `linux/arm64` (Graviton) or a comma list
  (`linux/amd64,linux/arm64`). A multi-arch value routes through
  `<builder> buildx build --platform … --push` (a manifest list cannot be `--load`ed
  locally, so multi-arch is inherently a push).
- **Builder-agnostic, no new library surface.** The platform fix is just a CLI arg
  to the operator-chosen `--builder` (`docker`/`podman`/`nerdctl`); Leoflow shells
  out and never links the Docker Go SDK (ADR 0015). (`docker/docker` in `go.sum` is
  a test-only transitive of golang-migrate's `dktest` — `go mod why` reports it
  unneeded by any package; govulncheck finds it unreachable.)
- **Loud first-time note:** cross-building under emulation is slow (pip/wheels); the
  command prints a one-line heads-up so a long build is not mistaken for a hang.
  Non-Docker-Desktop runtimes (Colima/Lima/podman) may need a one-time
  `docker run --privileged tonistiigi/binfmt --install all`.

### What the artifact does NOT carry — connections & variables

The DAG **image + `dag.json`** travel; **connections and variables do not** — they
live encrypted in the Pro control-plane DB (ADR 0019), not in the artifact. So a
deploy can succeed and the DAG still fail at runtime because its `conn_id` does not
exist on Pro. This bites journey (2) hardest (an example DAG that needs a
connection). The deploy flow must therefore:

- **Surface the dependency, not hide it.** A deploy of a DAG that references a
  `conn_id` prints the connections it expects and reminds the user to create them
  on Pro (UI or API), the same way the registry-missing error teaches.
- A future `leoflow connections push` (seed selected connections to Pro) is a
  natural follow-up but is **out of scope** here — secrets crossing machines is a
  deliberate, separately-designed step, never an implicit side effect of deploy.

## Happy paths (documentation restructures on ship)

`deploy` reshapes the Pro happy path into **three journeys**, which the docs
(`docs/first-pro-dag.md`, `docs/deploy.md`) must present distinctly — today they
only show one by-hand sequence:

1. **Your own DAG — develop in Lite, then deploy.** Iterate locally with
   `leoflow lite` (fast, no registry, hot-reload); when it is ready,
   `leoflow deploy --server <pro>` promotes it. Lite is the dev loop; deploy is
   the promotion. This is the path for DAGs you are writing.

2. **An example DAG — deploy directly, no Lite.** The shipped examples are
   known-good, so you skip the local loop entirely: configure `registry:` (and any
   connection the example needs), then `leoflow deploy --server <pro>`. Good for a
   first taste of Pro without authoring anything.

3. **Automated — a CI pipeline (esteira).** The same `compile → build → push →
   register` (or one `leoflow deploy`) on every push (the `deploy.md` recipes),
   for teams that already have a pipeline.

When `deploy` ships, `first-pro-dag.md` is restructured around journeys (1) and
(2) — replacing the current by-hand `compile --build --push` + `push` sequence —
and `deploy.md` keeps (3) as the automated variant.

### Two-tier happy path (rc.2 reality vs. the complete vision)

The fully-ergonomic Pro path is **Dockerfile-free** — `compile --build`
synthesizes the image from `leoflow.yaml` (`FROM` the published base, install
`connectors:`/`dependencies:`, copy the DAG). But that **yaml-driven build is part
of the v0.1.0 connectors epic** (it calls `EffectiveDependencies()`), which is
**held**. `feat/deploy` (rc.2) ships `deploy` itself but **not** the yaml-driven
build. To document a happy path that is *true on the branch it ships on* without
waiting for v0.1.0, the walkthrough is written in **two tiers**:

- **Tier 1 — the simple happy path (true in rc.2).** Deploy a **shipped example**
  whose `Dockerfile` builds `FROM` the CI-published base
  (`ghcr.io/dexadata/leoflow-runtime`). The user authors nothing and writes no
  Dockerfile — it belongs to the example. `leoflow auth login` → `leoflow deploy
  examples/<dag>`. This runs today on `feat/deploy`. "A Dockerfile is for examples
  only" holds: the example carries it; the user never writes one.
- **Tier 2 — the complete happy path (your first DAG, end-to-end).** Author your
  own DAG, **yaml-driven, no Dockerfile**, with `connectors:`/`dependencies:`. The
  prose/structure is borrowed now from the connectors-branch `first-pro-dag.md`,
  marked clearly as the **complete path that becomes executable when v0.1.0 lands**
  (the yaml-driven build). No connectors code is duplicated into `feat/deploy`.

**Branch sync (deferred, not a rebase).** `feat/deploy` stays independent of the
held v0.1.0: we borrow the connectors docs' *ideas*, not its code. When v0.1.0
unfreezes, the branches are reconciled — Tier 2 loses its "lands with v0.1.0"
caveat and the yaml-driven build is wired once (no duplicate `compile_build.go`).
Rebasing `feat/deploy` onto `feat/connectors` was rejected: it would couple
rc.2's Steve-unblock to the held epic.

## Open questions (decide during implementation — tracked as issues)

- **Registry reachability asymmetry.** The dev pushes; the *cluster* pulls the same
  ref. A LAN/short-name `registry.url` that resolves differently inside the cluster
  (or needs different TLS trust) makes the dev's push succeed while the pod's pull
  fails. `imagePullSecrets` covers *auth*, not *name/reachability*. Decide whether
  deploy does a best-effort reachability hint.
- **Paused/catchup on first register** (issue #370). `versions.go` does not set
  `IsPaused`, so a fresh deploy inherits the default; if that is active-with-catchup,
  a deploy could immediately fire a backlog on Pro. Decide: register **paused** by
  default with an `--activate`, or document the current behavior loudly.
- **CLI ↔ Pro version skew** (issue #371). A newer Lite writes a `dag.json`
  `schema_version` an older Pro cannot parse (or vice versa). The register endpoint
  should reject with an explicit "upgrade Pro" message, not a confusing error.
- **Re-register of the same version returns 500, not 409** (issue #368). The unique
  violation on `dag_versions_unique` should map to a 409 with an actionable message.
- **Default task SA for private-registry pulls** (issue #369). `imagePullSecrets`
  on the task SA only apply when pods run as it; default the execution SA so they do.
- **Tenant is implicit (works, document it).** `RegisterDagVersion` uses
  `tenantOf(token)` — deploy lands in the **token's tenant**. On a multi-tenant Pro
  the user selects the tenant by which token they `login` with; the docs must say so.

## Deferred (explicitly out of scope here)
- **Rollback UX** (issue #372). The model is already versioned server-side
  (`POST /dags/{id}/versions`), so the data supports rollback, but there is no
  `leoflow deploy --rollback` / pin-previous command yet. Name now, build later.
- **Secrets baked into the image during Lite dev** get pushed to the registry on
  deploy. A lint/warn for obvious secret material in the build context is a later
  hardening, not part of this command.
- **Bundled in-cluster registry** (Pro shipping its own `registry:2`) — it is the
  part that complicates *our* side (per-node containerd trust, TLS, image GC).
  Reconsider only on real demand for multi-node-without-external-registry.
- **Named environments / contexts** (`--env prod`, kubectl-style) — start with
  `--server` + `login`; add named targets if demand appears.
- **A "Deploy" button in the Lite web editor** — viable Lite-first (it has Docker),
  but a UI concern that reuses this same `deploy` command; not part of the CLI work.

## Cluster wiring (Helm) — required for the deploy path to actually pull

The deploy story is only complete if the **task pod** can pull the DAG image from
a **private** registry. Today the chart's `imagePullSecrets` is applied **only to
the control-plane Deployment** (`deployment.yaml`); the per-task pod built by the
K8s executor (`internal/executor/kubernetes.go::BuildPod`) sets **no**
`ImagePullSecrets`, and the task ServiceAccount template carries none either. A
private-registry deploy would therefore fail the task pod with `ErrImagePull`.

**Done (rc.2):** `taskServiceAccount.imagePullSecrets` (values + the block in
`templates/task-serviceaccount.yaml`, helm-unittested) — Kubernetes auto-injects a
SA's pull secrets into every pod that uses it.

**The remaining half — task pods must *run as* that SA — is by contract, not by
default (rc.2 decision).** Today a pod runs as `taskServiceAccount` only when
`Execution.ServiceAccount` is set (`kubernetes.go:93`); otherwise it uses the
namespace `default` SA, which has no pull secrets. We **do not** change the
executor default for rc.2: the GKE/Workload-Identity pattern *already* requires
`execution.service_account: <taskServiceAccount.name>` in the DAG's `leoflow.yaml`
(Workload Identity binds to the same SA), so those users already run as it and the
pull secrets apply. The requirement is surfaced in `NOTES.txt` (printed when
`imagePullSecrets` is set) and the values comment. **Deferred (own,
cluster-tested change):** a server-config default execution SA applied in
`BuildPod` when a task names none — nice ergonomics, but a behavior change on a
path already validated on GKE, so not slipped into rc.2.

This chart change is a **prerequisite** for journeys (1) and (2) against a private
registry; public images (e.g. the shipped examples on a public GHCR) work without
it, which is why journey (2) is the gentlest first taste of Pro.

## Consequences
- The pipeline-less promotion becomes one command, reusing tested primitives —
  little new code on our side (orchestration + a registry-presence check + the
  digest capture + `login`).
- A registry is a hard, documented prerequisite for Pro deploy; the failure path
  teaches the user instead of guessing.
- The artifact stays immutable and environment-agnostic: `leoflow.yaml` describes
  the DAG + where its image lives; `--server`/`login` chooses where it runs.
