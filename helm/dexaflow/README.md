# dexaflow

![Version: 0.5.0-rc.1](https://img.shields.io/badge/Version-0.5.0--rc.1-informational?style=flat-square) ![Type: application](https://img.shields.io/badge/Type-application-informational?style=flat-square) ![AppVersion: 0.5.0-rc.1](https://img.shields.io/badge/AppVersion-0.5.0--rc.1-informational?style=flat-square)

Dexaflow control plane — a GitOps-first, container-native workflow orchestrator (Airflow 3.2.x UI/API compatible).

Deploys the Dexaflow **control plane** (`dexaflow-server`) into Kubernetes for a
production-like install — distinct from the host-run `test/e2e/e2e.sh` smoke.

**Homepage:** <https://github.com/dexadata/dexaflow>

## What it installs

| Resource | Purpose |
|---|---|
| Deployment | `dexaflow-server` (HTTP `8080`, metrics `9090`, agent gRPC `9091`) |
| Service | ClusterIP exposing http / metrics / grpc |
| ServiceAccount + Role/RoleBinding | lets the control plane create/watch/delete **task pods** and read their logs in `taskNamespace` |
| ClusterRole + ClusterRoleBinding | **only** when `auth.agentTokenTransport: exchange` — `create` on `tokenreviews` (cluster-scoped API). See [Agent credential transport](#agent-credential-transport-and-warm-worker-pools) |
| Secret | holds inline DB/Redis/JWT/bootstrap credentials (skipped when you bring your own) |
| ConfigMap | **only** when `auth.oidc.enabled`, the two OIDC settings that are maps and therefore have no env-var route (`tenantClaims`, `roleMappings`), mounted as the server's `LEOFLOW_CONFIG`. See [SSO with OIDC](#sso-with-oidc) |
| Job (hook) | runs `golang-migrate` before install/upgrade |
| Ingress | optional |

## Quick start

Every release tag publishes this chart as a signed **OCI artifact** to
`oci://ghcr.io/dexadata/charts/dexaflow` (ADR 0028), so you can install a
pinned version without cloning the repo:

```bash
kubectl create namespace leoflow
helm install lf oci://ghcr.io/dexadata/charts/dexaflow --version <x.y.z> -n leoflow \
  --set database.url='postgres://user:pass@postgres:5432/leoflow?sslmode=disable' \
  --set redis.url='redis://redis:6379/0' \
  --set auth.jwtSecret='change-me' \
  --set bootstrap.password='admin'
```

Use the release tag **without** the leading `v` as `--version` (tag `v0.4.0` →
`--version 0.4.0`); the chart `version`/`appVersion` move in lockstep with the
tag. The chart is cosign-signed by digest — verify it with
`cosign verify ghcr.io/dexadata/charts/dexaflow --certificate-identity-regexp '…' --certificate-oidc-issuer https://token.actions.githubusercontent.com`.

To install from a source checkout instead (e.g. an unreleased branch), point
Helm at the chart directory:

```bash
helm install lf ./helm/dexaflow -n leoflow \
  --set database.url='postgres://user:pass@postgres:5432/leoflow?sslmode=disable' \
  --set redis.url='redis://redis:6379/0' \
  --set auth.jwtSecret='change-me' \
  --set bootstrap.password='admin'
```

Task pods are created in `taskNamespace` (default `leoflow`, which must match the
namespace the server targets). Agents dial the control plane gRPC at the
in-cluster Service DNS automatically (override with
`config.agentControlPlaneAddr`).

## Upgrading a release installed before the rename

This chart was published as `leoflow` before the project was renamed. It is
still published under that name, from the same templates, at
`oci://ghcr.io/dexadata/charts/leoflow`. Upgrade an existing release with it:

```bash
helm upgrade lf oci://ghcr.io/dexadata/charts/leoflow --version <x.y.z> -n leoflow \
  --reset-then-reuse-values
```

The chart name feeds the selector labels and every resource name, and a
Deployment's selector cannot change in place, so the release has to keep the
name it was installed with. Upgrading it with the `dexaflow` chart instead
needs `--set nameOverride=leoflow`, which renders exactly the same resources.

## Bringing your own secrets

Instead of inline values, reference existing Secrets:

```bash
--set database.existingSecret=my-db     # key: databaseUrl
--set redis.existingSecret=my-redis     # key: redisUrl
--set auth.existingSecret=my-jwt        # key: jwtSecret
--set bootstrap.existingSecret=my-boot  # key: bootstrapPassword
```

When all credentials come from existing Secrets, the chart creates no Secret of
its own.

> **Upgrades + secret rotation.** The pod template carries a `checksum/secret`
> annotation hashed over the **chart-rendered** Secret (#316), so `helm upgrade`
> rolls the pod whenever an **inline** value backing it changes
> (`database.url`, `redis.url`, `auth.jwtSecret`, `secretKey`,
> `bootstrap.password`). Credentials wired via `*.existingSecret` are **outside
> the chart's visibility**: rotating them requires a manual
> `kubectl rollout restart deployment/<release>` for the change to take effect.

## High availability

A single control-plane replica is evicted as **routine** cluster housekeeping —
autoscaler consolidation / bin-packing (Karpenter on EKS, GKE Autopilot), node
drains, node auto-upgrades — and every eviction costs a full restart (image pull
+ boot + leadership: tens of seconds during which nothing dispatches). Running
`replicaCount: 2` turns that into seconds of failover with no image pull: the
scheduler leader-elects (active-passive), the API serves from both replicas
(active-active).

The chart keeps `replicaCount: 1` as the shipped default because HA needs a log
store **every replica can write**, and the default install puts task logs on a
ReadWriteOnce PVC (one node). It therefore guards the unsafe combination instead
of defaulting into it:

| Setting | Behavior |
|---|---|
| `replicaCount > 1` (or an HPA / split with more than one mounter) with a `ReadWriteOnce` / `ReadWriteOncePod` logs PVC | **render fails** — the extra pods would hang in `ContainerCreating` on a Multi-Attach error while the install reports success |
| `split.enabled` with `logs.persistence.enabled: false` and the `disk` sink | **render fails** — the scheduler pod would write every task log into its own emptyDir and each api pod read from its own, so no log is ever readable |
| `podDisruptionBudget.enabled` unset (auto) | PDB renders **iff** the guaranteed replica floor is `> 1` (`replicaCount`, the HPA `minReplicas`, or `split.api.replicaCount`). A `minAvailable: 1` budget over a **single** replica blocks every voluntary eviction — node drains hang, auto-upgrades stall — so it is only safe-by-default for HA. Explicit `true`/`false` always wins. **On upgrade** this means every `split.enabled` install (api default 2) and every `autoscaling.enabled` install (`minReplicas` default 2) gains a PDB; NOTES say so |
| `podDisruptionBudget.unhealthyPodEvictionPolicy` | `AlwaysAllow` by default, so both replicas being unready (bad DB credentials, image-pull failure, wedged rollout) does not leave a drain hanging on an already-down control plane |
| `topologySpreadConstraints` | empty by default; the HA profile spreads the replicas across nodes (`kubernetes.io/hostname`, `ScheduleAnyway`) — two replicas bin-packed onto one node are one replica, and the PDB then blocks that node's drain. A constraint without `labelSelector` gets the Deployment's own selector |
| `terminationGracePeriodSeconds` | unset by default (Kubernetes' 30s); the HA profile sets `60`. An explicit `0` or `null` means the same as unset — the field is omitted and Kubernetes' 30s applies; the chart never renders a literal `0`, which would be SIGKILL with nothing drained. It buys headroom for the HTTP shutdown (10s) and the dispatch-pool drain; it does **not** decide leadership handoff (the lease is released within a tick of SIGTERM and frees anyway when the connection drops); with tasks running, open agent log streams are closed and flushed at SIGTERM and the gRPC stop is bounded, so the shutdown completes within the grace instead of being SIGKILLed |
| `podAnnotations: {karpenter.sh/do-not-disrupt: "true"}` | **EKS/Karpenter-only opt-in**, not set by default: exempts the node from voluntary disruption, trading maintenance friction for eviction protection |

The one-switch HA profile is
[`examples/values-ha.yaml`](https://github.com/dexadata/dexaflow/blob/main/helm/dexaflow/examples/values-ha.yaml):
two replicas spread across nodes, task logs in object storage
(`logs.persistence.enabled: false` + `logs.sink.provider: s3|gcs` — the
recommended HA log path; a `ReadWriteMany` PVC is the alternative), auto PDB,
memory sized for the object sink's per-attempt buffer, and the longer drain grace:

```bash
helm install dexaflow oci://ghcr.io/dexadata/charts/dexaflow --version <x.y.z> -n leoflow \
  -f helm/dexaflow/examples/values-ha.yaml
```

Involuntary disruptions — node failure, spot reclamation, a kernel panic — are
stopped by **no** PDB or annotation, which is why the second replica is the
posture that matters. Note that the api/scheduler **split** is not dispatch-HA:
its scheduler is pinned to one replica with no standby and no PDB, so only
non-split `replicaCount: 2` fails dispatch over today. The full reasoning,
per-platform PDB behavior, and the upgrade path from a single replica are in the
[Control-plane HA and disruption posture](https://dexaflow.dexadata.ai/operate/control-plane-ha/)
page.

## Verified TLS to managed Postgres (#315)

Managed Postgres (Cloud SQL, RDS, Azure DB) presents a server cert signed by
a provider / per-instance CA that is **not** in the container's system roots.
Without the CA bundle mounted, the strongest TLS posture you can pin is
`sslmode=require` — encrypted but the server cert is **not verified**
(MITM-vulnerable). To upgrade to `sslmode=verify-full`:

1. Create a ConfigMap holding the CA bundle as `ca.crt`:

   ```bash
   kubectl create configmap managed-pg-ca \
     -n leoflow --from-file=ca.crt=server-ca.pem
   ```

2. Reference it from `database.caConfigMap` and point the DSN at the mounted
   path:

   ```bash
   helm upgrade --install dexaflow ./helm/dexaflow -n leoflow \
     --set database.caConfigMap=managed-pg-ca \
     --set database.url='postgres://user:pass@cloudsql-private-ip:5432/leoflow?sslmode=verify-full&sslrootcert=/etc/leoflow/db-ca/ca.crt'
   ```

The chart mounts the ConfigMap readonly at `/etc/leoflow/db-ca/ca.crt`. pgx
reads `sslrootcert` from the DSN natively, so no extra Go-side configuration
is required.

> **Rotation note.** Kubernetes auto-updates the mounted file when the
> ConfigMap changes, but the pgx pool keeps its existing connections until
> they cycle. Cert rotation that invalidates the old chain may break in-flight
> connections; rolling the pod (`kubectl rollout restart deploy/<release>`)
> guarantees a clean cutover.

> **Redis sibling — coming next** (#312). The same pattern lands for Redis in
> a follow-up PR: a `redis.caConfigMap` knob mounts the bundle and the Go
> client overrides its `TLSConfig.RootCAs` from the file (go-redis does not
> read CA paths from the URL the way pgx does).

## Migrations

The pre-install/pre-upgrade Job runs `migrate -path <path> -database <url> up`
using the default image **`ghcr.io/dexadata/dexaflow-migrate`** (built from
`deploy/Dockerfile.migrate`, published by `.github/workflows/release.yaml` on
every tag), which bundles the Dexaflow `migrations/` at `migrations.path`. The
`migrate` binary in it is golang-migrate's own CLI, compiled from the version
pinned in Dexaflow's `go.mod` onto distroless static and running as UID 65532,
rather than a third-party image pulled at build time (#1039).
Override `migrations.image` to use your own, or set `migrations.enabled=false`
to migrate out of band.

## Datastore compatibility

The chart talks to external Postgres + Redis you provision. Versions
we test against in CI and the minimums we support:

| Datastore | Tested in CI | Recommended minimum | Notes |
|---|---|---|---|
| **PostgreSQL** | 16.x (`postgres:16-alpine`) | **PostgreSQL 13** (current LTS line) | Uses JSONB, `ON CONFLICT`, advisory locks, generated columns. Versions below 13 may work but are untested; CI doesn't validate them. |
| **Redis** | 7.x (`redis:7-alpine`) | **Redis 6.0** | ACL is used for the per-tenant auth model; Redis 5 and below lack ACL. Streams aren't used today but may be in the future. |

The PoC recipe pins Bitnami chart majors that bundle the tested
versions (postgres chart 16 → PG 16; redis chart 20 → Redis 7). When
pointing at managed cloud datastores (RDS, ElastiCache, CloudSQL,
Memorystore), check that the engine version is at the recommended
minimum or above.

If you need to run against an older engine, please file an issue with
the version + symptoms — we don't bench-test older majors but will
investigate specific incompatibilities.

## Verified TLS to managed Redis (#312)

Managed Redis offerings — Memorystore SERVER_AUTHENTICATION, ElastiCache
in-transit encryption, Azure Cache for Redis — sign their TLS server cert
with a provider or per-instance CA that is **not** in the container's
system roots. Without telling the client which CA to trust, the only
working postures are "plaintext `redis://`" (unacceptable across the
internet) or "skip verification" (we don't expose that knob; it strips
TLS to a noise channel).

Set `redis.caConfigMap` to a ConfigMap holding the provider CA as
`ca.crt`:

```bash
kubectl create configmap dexaflow-redis-ca \
  --from-file=ca.crt=./memorystore-server-ca.pem -n leoflow
```

```yaml
redis:
  url: rediss://10.0.0.5:6378/0
  caConfigMap: dexaflow-redis-ca
```

The chart then:

1. Mounts the ConfigMap read-only at `/etc/leoflow/redis-ca/ca.crt`.
2. Sets `LEOFLOW_REDIS_CA_FILE=/etc/leoflow/redis-ca/ca.crt` so the
   server overrides `tls.Config.RootCAs` instead of falling back to
   system roots.
3. Refuses to boot if the file is missing or malformed (clear error
   instead of a confusing "x509: certificate signed by unknown
   authority" at first Ping).

Leave `caConfigMap` empty for plaintext `redis://` or for managed
TLS that uses a public CA (rare).

## Agent credential transport, and warm worker pools

Two independent settings, coupled in one direction. Both are operator-scoped: a
DAG author cannot reach either.

### `auth.agentTokenTransport`

How the agent inside a task pod obtains its control-plane credential:

| Value | What the task pod carries | RBAC |
|---|---|---|
| `envvar` *(default)* | the task-scoped JWT, in a plaintext `LEOFLOW_AGENT_TOKEN` env var on the Pod object | the namespaced executor Role only |
| `exchange` | a projected ServiceAccount token, traded once — via a control-plane TokenReview — for the task-scoped JWT, so no bearer sits in plaintext on the pod spec | **plus** a ClusterRole granting `create` on `authentication.k8s.io/tokenreviews` |

TokenReview is a **cluster-scoped** API, so the namespaced Role cannot carry it.
The chart renders the ClusterRole + ClusterRoleBinding **only** on
`exchange` — `create` on `tokenreviews` lets its holder ask the apiserver to
validate any token it can get hold of, which is not a standing permission every
install should carry. Setting the transport through the chart is therefore
sufficient on its own:

```bash
--set auth.agentTokenTransport=exchange
```

> **Do not flip this outside the chart.** The transport is **server-wide**: with
> `exchange` selected but no TokenReview grant, the control plane logs
> `tokenreviews.authentication.k8s.io is forbidden ... at the cluster scope` and
> **every** task pod fails on an invalid projected token — the ordinary dedicated
> pod-per-task path included, not just warm pools.

The cluster-scoped objects are named `<fullname>-<namespace>-tokenreview` so two
Dexaflow releases in one cluster never collide on them.

### `execution.warmPoolsEnabled`

Warm worker pools ([ADR 0058](https://github.com/dexadata/dexaflow/blob/main/docs/adr/0058-warm-worker-pools.md))
reuse one pod across many attempts of the **same DAG version**, instead of one pod
per attempt. Off by default: every attempt gets a dedicated pod, which is today's
behavior.

Enabling is a security decision first. A reused pod holds one live credential
across attempts, so per-attempt identity is the only thing stopping a superseded
attempt from still resolving secrets — hence ADR 0058 D2:

```bash
--set execution.warmPoolsEnabled=true \
--set auth.agentTokenTransport=exchange \
--set auth.secretLivenessMode=enforce
```

The chart **refuses to render** if the flag is on without both prerequisites; the
server enforces the same coupling at boot, so the alternative would be a
`CrashLoopBackOff` explained only in container logs.

The remaining knobs (`minIdleWorkers`, `maxPoolSize`, `maxAttemptsPerWorker`,
`maxWorkerLifetime`, `workerIdleTtl`, `maxWarmPodsPerTenant`) default to exactly
the server's own defaults and are sent only while the flag is on — see the values
table below. Before enabling on a shared production cluster, read ADR 0058's
*Measurement & enable readiness*: the go/no-go bar is stated in rates measured on
a real cluster, and unit tests do not clear it.

## Settings the chart does not model

`extraEnv` appends raw env entries to the control-plane container, for any
`LEOFLOW_*` setting without a first-class value (the full surface is in
[`docs/configuration.md`](https://github.com/dexadata/dexaflow/blob/main/docs/configuration.md)):

```yaml
extraEnv:
  - name: LEOFLOW_SCHEDULER_DISPATCH_WORKERS
    value: "4"
```

It is an escape hatch, not an override: an entry redefining
`LEOFLOW_EXECUTION_WARM_POOLS_ENABLED`, `LEOFLOW_AUTH_AGENT_TOKEN_TRANSPORT` or
`LEOFLOW_AUTH_SECRET_LIVENESS_MODE` fails the render, because those three are the
values the guard above validates against each other.

## SSO with OIDC

The server has had a complete OIDC implementation for several releases; this
chart could not reach it until now, and the reason is worth knowing because it
shapes how the values are laid out.

Two of the server's OIDC settings are **maps** (`auth.oidc.tenant_claims` and
`auth.oidc.role_mappings`), and a map has no environment-variable route at all.
Viper binds env only for the scalar leaves the server registers, and those two are
deliberately unregistered: a Google Workspace `hd` key is a domain, always dotted,
and viper's `.` key delimiter would split it into nested maps and fail to decode
(#826). The server reads them out-of-band from the YAML file named by
`LEOFLOW_CONFIG`. The chart shipped no such file and no volume to put one in, so
`auth.provider: oidc` could only ever fail boot closed on the missing tenant pin
(#1143).

So the chart splits the delivery:

| What | How |
|---|---|
| `issuer`, `clientId`, `redirectUrl`, `tenantClaim`, `scopes`, `groupsClaim`, `defaultRole`, `allowedEmailDomains`, `breakGlassEmails`, `jitProvisioning`, `clockSkewSeconds` | `LEOFLOW_AUTH_OIDC_*` env vars, the way the rest of this chart delivers server settings |
| `tenantClaims`, `roleMappings` | a ConfigMap mounted read-only at `/etc/leoflow/oidc`, with `LEOFLOW_CONFIG` pointing at it |
| `clientSecret` | the chart-managed Secret (or `auth.oidc.existingSecret`), by `secretKeyRef`: never the ConfigMap |

The mounted file is **partial on purpose**. Viper ranks env above a config file,
and a key a file omits keeps the value it already had, so a file holding only the
two maps can neither override nor blank anything the Deployment's env delivers.
That is what makes mixing the two routes safe, and why nothing else is written
there.

```yaml
auth:
  jwtSecret: "…"          # still required: OIDC is the login flow, not the token signer
  oidc:
    enabled: true
    issuer: "https://accounts.google.com"
    clientId: "…apps.googleusercontent.com"
    existingSecret: dexaflow-idp          # key: oidcClientSecret
    redirectUrl: "https://leoflow.example.com/api/v2/auth/oidc/callback"
    tenantClaim: "hd"                    # `tid` on Entra
    tenantClaims:
      "example.com": "default"
    defaultRole: "viewer"
    breakGlassEmails: ["admin@example.com"]
```

A full worked example is in
[`examples/values-oidc-google.yaml`](https://github.com/dexadata/dexaflow/blob/main/helm/dexaflow/examples/values-oidc-google.yaml).

Three things the chart refuses at render time rather than letting them become a
CrashLoopBackOff whose cause is visible only in container logs: the one channel a
GitOps sync never shows, since Argo CD does not render `NOTES.txt`:

- `enabled: true` without `issuer`, `clientId`, `redirectUrl`, `tenantClaim` **and**
  a non-empty `tenantClaims`. All missing keys are named in one error, because each
  boot failure on Kubernetes costs a values edit, an upgrade and a rollout to learn
  the next one.
- a non-`https://` `issuer`.
- an `extraEnv` entry setting `LEOFLOW_CONFIG` while SSO is on, which would repoint
  the server away from the mounted file and take the tenant pin with it.

`auth.oidc.jitProvisioning` defaults to `true` here, unlike the server's own
default, because `false` means no SSO login can succeed at all: a login is matched
to a user by `(oidc_provider, oidc_subject)`, and just-in-time provisioning is the
only code path that ever writes those two columns, so there is nothing to match
and every first login is denied (audited `no_user_jit_off`) behind a generic 403.
It is not a widening of who may log in: the tenant pin, `email_verified` and
`allowedEmailDomains` decide that, and a JIT-created row carries only the roles
`roleMappings`/`defaultRole` resolve, which is none by default. One address it
cannot serve either way is one that already has a LOCAL password account in the
same tenant: it collides on the users table's unique `(tenant, email)` and the
login is denied `jit_failed`, so keep `breakGlassEmails` off the domain your
users sign in with.

Two more it only warns about, in `NOTES.txt`, because both are legitimate
configurations that are almost always mistakes: `jitProvisioning: false` (no
login can succeed), an empty `breakGlassEmails` (SSO
becomes the only way in, so an IdP outage or one wrong `tenantClaims` key locks out
everybody, including whoever has to fix it), and neither `roleMappings` nor
`defaultRole` set (role resolution is default-deny, so logins succeed into a UI
that shows nothing).

`auth.oidc.existingSecret` is separate from `auth.existingSecret` on purpose: the
JWT signing key and the IdP client secret rotate on different schedules and usually
have different owners. Like every `existingSecret` in this chart it sits outside
the `checksum/secret` annotation's visibility, so rotating it needs a manual
`kubectl rollout restart`. The maps do not: they carry their own
`checksum/oidc-config` annotation, because `LoadServer` parses the file once at
boot and would otherwise leave an edited tenant map sitting on disk unread.

## Evaluating without a managed Postgres + Redis

For a one-cluster evaluation (kind, minikube, k3d, scratch namespace), the
chart deliberately won't fall back to embedded datastores — that's Lite's
job, not Pro's (see `templates/deployment.yaml:8-13`). The supported PoC
path is to install Bitnami's Postgres + Redis charts alongside Dexaflow:

- Recipe: [`helm/dexaflow/examples/README.md`](https://github.com/dexadata/dexaflow/tree/main/helm/dexaflow/examples/README.md)
- Matching values file: [`helm/dexaflow/examples/poc.yaml`](https://github.com/dexadata/dexaflow/tree/main/helm/dexaflow/examples/poc.yaml)

Three `helm install`s in total. **Not for production** — see the recipe for
the production-shaped command.

## Validate

```bash
helm lint ./helm/dexaflow
helm template lf ./helm/dexaflow -n leoflow \
  --set database.url=postgres://x \
  --set redis.url=redis://r/0 \
  --set auth.jwtSecret=s \
  --set "secretKey=$(openssl rand -hex 32)"
bash scripts/helm-template-checks.sh   # contract assertions (env wiring, Job hardening, fixture lengths)
```

## Maintainers

| Name | Email | Url |
| ---- | ------ | --- |
| Dexaflow |  |  |

## Source Code

* <https://github.com/dexadata/dexaflow>

## Values

The table below is auto-generated by [`helm-docs`](https://github.com/norwoodj/helm-docs) from `values.yaml`. To
update: edit `values.yaml` (use `# -- <description>` comments above each key
that warrants documentation), then run `helm-docs -c helm/dexaflow` from the
repo root. CI fails (helm-ci.yaml lint job) if the regenerated README would
differ from what's committed.

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| affinity | object | `{}` | Pod affinity rules (standard K8s — co-locate or anti-affinity). |
| agentTLS.autoGenerate | bool | `true` | Auto-generate a stable self-signed CA + gRPC server cert when neither `serverCertSecret` nor `caConfigMap` is supplied (#690), so `helm install` needs no cert-manager and no pre-created Secret. The material is minted once and reused on every `helm upgrade` (via `lookup`) so the CA never rotates and running agents keep trusting it. Fine for quickstart / simple clusters. Set `false` (and supply nothing) to force the old fail-fast when no cert is provided. **Not for GitOps** (ArgoCD/Flux): a cluster-less render can't `lookup` the live Secret and would rotate the CA every sync — use BYO/cert-manager there. For production, prefer BYO below (rotation, real chain of trust). |
| agentTLS.caConfigMap | string | `""` | Name of a ConfigMap with key `ca.crt`. Mounted into task pods so the agent verifies the server cert. Set this **and** `serverCertSecret` for BYO (typically a cert-manager trust-bundle). Leave empty to let `autoGenerate` mint one. |
| agentTLS.enabled | bool | `true` | Enable TLS on the agent ↔ control plane gRPC channel (#58). Keep this `true`: the chart marks every deployment as the production edition (`LEOFLOW_UI_EDITION=pro`), and that edition refuses to boot without a gRPC cert/key — setting this to `false` yields a crash-looping control plane ("the Pro edition requires TLS on the agent gRPC channel"), not a plaintext deployment. The same edition marker also rejects `LEOFLOW_AGENT_ALLOW_INSECURE_SECRETS=true`, so secrets cannot accidentally travel a plaintext channel. For a plaintext local loop use the Lite dev server, not this chart. |
| agentTLS.serverCertSecret | string | `""` | Name of a `kubernetes.io/tls` Secret (with `tls.crt`/`tls.key`) for the gRPC server. Set this **and** `caConfigMap` to bring your own (BYO), typically a cert-manager `Certificate` — the chart then uses them verbatim and skips auto-generation. Leave empty to let `autoGenerate` mint one. |
| auth.agentTokenTransport | string | `"envvar"` | Agent credential transport: `envvar` (default) puts the task-scoped bearer in a plaintext `LEOFLOW_AGENT_TOKEN` env var on the Pod object; `exchange` mounts a projected ServiceAccount token that the agent trades once — via a control-plane TokenReview — for the task-scoped JWT, so no bearer sits in plaintext on the pod spec. `exchange` needs the cluster-scoped TokenReview grant the chart renders with it (`rbac.create`), and is a hard prerequisite of `execution.warmPoolsEnabled` (ADR 0058 D2). Server-wide: switching it changes how EVERY task pod authenticates, warm or dedicated. |
| auth.existingSecret | string | `""` | Name of a Secret with key `jwtSecret` (takes precedence over `jwtSecret`). |
| auth.externalSigninUrl | string | `""` | Send UI visitors without a session to your own sign-in instead of Dexaflow's page (#1288), for deployments served from a larger platform. The requested page travels in a `next` query parameter, and your flow is expected to return the user with a Dexaflow session. Absolute `http(s)` URL; empty (the default) keeps Dexaflow's page. `/api/v2/auth/login?local=1` still reaches Dexaflow's page, so break-glass access survives an outage of your sign-in. |
| auth.externalSignoutUrl | string | `""` | Where sign-out lands after clearing the Dexaflow session (#1288), so your platform can end its own session too. Absolute `http(s)` URL; empty (the default) returns to Dexaflow's sign-in page. |
| auth.jwtSecret | string | `""` | HMAC secret signing API + agent JWTs. Set inline OR reference an existing Secret via `existingSecret`. Generate with `openssl rand -base64 64`. |
| auth.oidc.allowedEmailDomains | list | `[]` | Login-level email-domain allowlist layered ON TOP of the tenant pin, not a replacement for it. Checked only after the pin and `email_verified` have passed, so the domain is trustworthy by then. Empty (the default) imposes no extra restriction. It gates EVERY OIDC login, not only JIT-provisioned ones. Rendered comma-joined, omitted when empty. |
| auth.oidc.autoRedirect | bool | `false` | Start the login flow on the sign-in page instead of rendering it. OFF by default, because turning it on removes the sign-in page from a deployment that relies on it. Turn it on where that page is a screen to acknowledge for nothing: an edge proxy has already authenticated the session, or SSO is the only way in and the click adds nothing. It is SUPPRESSED in two cases, and both matter. A refused sign-on returns to this page carrying `sso_error`, and redirecting that would bounce the denial straight back to the IdP: an infinite loop with no surface left to read the error on. And `?local=1` always renders the form, so a `breakGlassEmails` account can reach it when the IdP is the thing that is broken, without an operator having to edit values and roll out to get back in. |
| auth.oidc.breakGlassEmails | list | `[]` | Local password logins still accepted while SSO is on; every other password login is rejected. This is the lock-out escape hatch: with the list empty, an IdP outage or a wrong `tenantClaims` entry leaves nobody able to reach the UI, including the operator who has to fix it. Put at least one break-glass admin here before enabling SSO, and make sure it is an address with a LOCAL PASSWORD account: listing an address that has no password row (or only an SSO-provisioned one, which has no password at all) produces a hatch that does not open, because the allowlist admits it and the credential store then rejects it like a wrong password. The server checks this at boot and warns. Rendered comma-joined, omitted when empty. |
| auth.oidc.clientId | string | `""` | The registered application (client) id. It is also the expected `aud` of every ID token, so it must be the client the redirect URL below is registered against. |
| auth.oidc.clientSecret | string | `""` | The client secret, used ONLY for the authorization-code exchange (ID-token verification is keyless, against the issuer's published JWKS). Set inline OR reference an existing Secret via `oidc.existingSecret`. Never goes into the ConfigMap. |
| auth.oidc.clockSkewSeconds | int | `60` | Tolerance in seconds applied to the ID token's `exp`/`iat`/`nbf` checks, to absorb clock drift between the IdP and this server. The shipped 60 is the server's own default; raise it only if boot-time clock sync is genuinely unreliable, since it widens the window a replayed token stays valid. |
| auth.oidc.defaultRole | string | `""` | Single role granted to an authenticated user who resolves to ZERO mapped roles. Empty (the default) keeps strict default-deny, which with an empty `roleMappings` means a successful SSO login that can see nothing, correct, and a confusing first experience. Set a read-only role such as `viewer` while you work the group mapping out. It must name a role that already exists for the resolved tenant; an unknown name fails the login closed. |
| auth.oidc.enabled | bool | `false` | Turn on the OIDC/SSO login flow (sets `auth.provider: oidc`). While off, none of the keys below render at all (no env vars, no ConfigMap, no volume) so a non-SSO install is unaffected by this block existing. Turning it on makes SSO the login path for everyone except `breakGlassEmails`, so configure those before you flip it or an IdP misconfiguration locks every operator out of the UI. |
| auth.oidc.existingSecret | string | `""` | Name of a Secret with key `oidcClientSecret` (takes precedence over `clientSecret`). Separate from `auth.existingSecret` on purpose: the JWT signing key and the IdP client secret rotate on different schedules and are usually owned by different people. Like every `existingSecret` in this chart it is outside the `checksum/secret` annotation's visibility, so rotating it needs a manual `kubectl rollout restart`. |
| auth.oidc.groupsClaim | string | `"groups"` | ID-token claim carrying the user's IdP groups; its values are what `roleMappings` matches on. Entra emits `groups` (object ids unless the app registration is configured to emit names); Okta emits whatever the groups claim is named in the authorization server. |
| auth.oidc.issuer | string | `""` | The IdP's issuer URL, `https://` only (the ID token's `iss` is pinned to it, a token from any other issuer is rejected). Entra: `https://login.microsoftonline.com/<tenant-id>/v2.0`. Google Workspace: `https://accounts.google.com`. Okta: `https://<org>.okta.com`. |
| auth.oidc.jitProvisioning | bool | `true` | Create the Dexaflow user row on first successful OIDC login. ON here, unlike the server default, because OFF means no SSO login can succeed at all: a login resolves its user by `(provider, subject)` alone and the only code path that ever writes those two columns is this one, so there is no supported way to pre-provision an account an SSO login will match, and every first login is denied with a generic 403 whose reason (`no_user_jit_off`) reaches only the audit log. Turning it on is not a widening of who may log in: the tenant pin, `email_verified` and `allowedEmailDomains` decide that, and the created row carries only the roles `roleMappings`/`defaultRole` resolve, which is none by default. One case it cannot serve either way: an address that already has a LOCAL password account in the same tenant collides on the users table's unique (tenant, email) and is denied `jit_failed`, so do not create local accounts for the addresses your users sign in with. |
| auth.oidc.redirectUrl | string | `""` | This server's callback URL as registered with the IdP; it must end in `/api/v2/auth/oidc/callback` and be reachable from the browser, not from inside the cluster. `https://` is required except on loopback hosts, so this is the ingress hostname, never the Service name. |
| auth.oidc.roleMappings | object | `{}` | Maps an IdP group value → an existing Dexaflow role name. Default-DENY: a group with no entry here grants nothing. This is one of the two keys that CANNOT travel as an env var, so the chart writes it into the mounted `config.yaml`. Keys are quoted on render, which is what lets a dotted group name (`app.admins`) survive (#826). Optional, leave empty and every user falls back to `defaultRole`. |
| auth.oidc.scopes | list | `["openid","email","profile"]` | OAuth scopes requested at login. The shipped three are what the flow needs; add the IdP's groups scope (Okta `groups`, Entra exposes groups without one) when `roleMappings` is used, or the `groupsClaim` arrives empty and every user resolves to zero roles. Rendered comma-joined into one env var, viper's decode hook splits it back into a list, the same mechanism `config.trustedProxies` uses. |
| auth.oidc.tenantClaim | string | `""` | Which claim identifies the tenant: `tid` on Entra, `hd` on Google Workspace. REQUIRED with `enabled: true`, together with `tenantClaims` it is the tenant pin, and the chart refuses to render without both. |
| auth.oidc.tenantClaims | object | `{}` | Maps each accepted `tenantClaim` VALUE → a Dexaflow tenant name. REQUIRED and non-empty with `enabled: true`. A claim value that is not a key here is rejected with a generic 403 whose cause reaches only the audit log (which, in an SSO-only deployment, nobody can log in to read (#1143)) and it never falls back to the `default` tenant. This is the second key that cannot travel as an env var, so it too is written into the mounted `config.yaml` with its keys quoted: a Google `hd` value is always a dotted domain. Example: `{"example.com": "default"}`. The VALUE must name a tenant that already exists: the first migration creates `default`, and any other must be created through the service API (`PUT /api/v2/service/tenants/<name>`, enabled by `auth.serviceToken`) before it can be mapped. The server checks this at boot and warns. |
| auth.secretLivenessMode | string | `"observe"` | Secret-delivery liveness gate: `observe` (default) logs and audits a would-have-denied when the calling task instance is no longer live but still delivers the secrets; `enforce` denies. Required at `enforce` by `execution.warmPoolsEnabled` (ADR 0058 D2) — a reused pod's superseded attempt would otherwise still resolve secrets. Run `observe` first and read the audit trail before flipping. |
| auth.secretScoping | string | `"permissive"` | Scope-by-declaration policy for secret delivery: `permissive` (default) delivers the WHOLE tenant vault to every task regardless of what its `leoflow.yaml` declares, and only logs + audits a warning when a task declared a narrower non-empty set; `enforce` delivers only the declared subset; `off` disables scoping and its warning entirely. `enforce` denies secrets to a DAG that declares NONE, which — the declaration schema being new — is most DAGs, so run `permissive` first and read the scope-warning trail before flipping. A clean trail proves only that no DAG whose declarations still RESOLVE is over-served: the warning counts only declared names that actually resolve, so it says nothing about a DAG that declares nothing, nor about one whose declared names were since deleted from the vault (an all-stale declaration counts as zero) (#800). Operator-scoped, never DAG-author-settable. |
| auth.serviceToken | string | `""` | Bearer token for the operator service API under `/api/v2/service/` (#1283), which creates tenants (with the built-in roles and default pool) and links passwordless users to `trustedIssuer`. At least 32 characters; generate with `openssl rand -base64 48`. Stored in the chart Secret. It is a root-level credential: whoever holds it can create tenants and grant any role, admin included, in every tenant `trustedIssuer.allowedTenants` covers. Rotate it by changing the Secret and restarting. Empty (the default) leaves the service API off. |
| auth.serviceTokenExistingSecret | string | `""` | Name of an existing Secret with key `serviceToken`, used instead of `serviceToken`. |
| auth.tokenTtlSeconds | int | `3600` | API + agent JWT lifetime in seconds. Default 1h; raise for longer agent sessions. |
| auth.trustedIssuer.allowedOrigins | list | `[]` | Origins (`scheme://host[:port]`, no path) whose pages may post a handoff, typically your portal. Any other `Origin`, or none, is refused, so another site cannot sign a visitor in as someone else. Required when `issuer` is set. |
| auth.trustedIssuer.allowedTenants | list | `[]` | Tenants the issuer may sign in to; `["*"]` allows every tenant. Required when `issuer` is set. |
| auth.trustedIssuer.audience | string | `""` | The `aud` the tokens must carry for this Dexaflow. |
| auth.trustedIssuer.issuer | string | `""` | The exact `iss` of the issuer's tokens. Empty (the default) disables the handoff and renders none of these keys. |
| auth.trustedIssuer.jwksUrl | string | `""` | Where the issuer publishes its public signing keys. `https`, or `http` on a loopback host. |
| auth.trustedIssuer.maxLifetimeSeconds | int | `0` | Longest `exp - iat` a handoff token may have. `0` uses 120 seconds; at most 600. |
| auth.trustedIssuer.name | string | `""` | Name of the issuer, 1-40 lowercase letters, digits or `-`. Users are linked under `issuer:<name>`, so keep it stable once users exist. |
| auth.trustedIssuer.tenantClaim | string | `"tenant_id"` | The string claim naming the Dexaflow tenant. |
| autoscaling.behavior | object | `{}` | HPA scaling behavior (scale-up/scale-down policies). See K8s docs for autoscaling/v2 `behavior` schema. |
| autoscaling.enabled | bool | `false` | Enable HPA for the leoflow-server Deployment. Requires metrics-server. |
| autoscaling.maxReplicas | int | `6` | Maximum replicas. HPA never scales above this. Must be at or above `minReplicas`, and the render is refused otherwise — these are independent values with independent defaults, so overriding only this one used to produce a HorizontalPodAutoscaler the apiserver rejects at install (#947). |
| autoscaling.minReplicas | int | `2` | Minimum replicas. HPA never scales below this. Must be at least 1: the apiserver rejects a lower minimum unless the HPAScaleToZero feature gate is on, which this chart does not assume. |
| autoscaling.targetCPUUtilizationPercentage | int | `70` | Target average CPU utilization across replicas (percent). HPA scales out when exceeded. |
| autoscaling.targetMemoryUtilizationPercentage | string | `""` | Target average memory utilization (percent). Empty = not used. Add only if your workload is memory-bound (rare for a control plane). |
| bootstrap.existingSecret | string | `""` | Name of a Secret with key `bootstrapPassword` (takes precedence over `password`). |
| bootstrap.password | string | `""` | Initial admin password (first install only). Leave empty to skip the bootstrap; the operator then creates the first admin out-of-band. |
| config.agentControlPlaneAddr | string | `""` | gRPC address task pods dial back to reach the control plane. Defaults to the in-cluster Service DNS on `ports.grpc` when empty. Override for cross-cluster or external task pods. |
| config.cors.allowedOrigins | list | `[]` | CORS origins the API accepts (`server.cors.allowed_origins`). Dexaflow serves its UI same-origin with the API, so most deployments need no entry here. Empty (the default) renders nothing and leaves the server's own default in place, which is what every install has actually been running: this key was documented but read by no template until #1144, so whatever was set here was silently ignored. It now takes effect, so check the value you have. The previous documented default of `["*"]` is not restored and is now rejected at render time, because rendering it would have widened CORS to every origin on the next `helm upgrade` for anyone who only ever copied it out of this file; set `LEOFLOW_SERVER_CORS_ALLOWED_ORIGINS` through `extraEnv` if you want the wildcard deliberately. Rendered as a comma-joined `LEOFLOW_SERVER_CORS_ALLOWED_ORIGINS`, which viper splits back into a list. |
| config.logsDir | string | `"/var/log/leoflow"` | Directory inside the pod where task logs are written. Mounted from `logs.persistence` (a PVC by default) so logs survive pod restarts. Set `logs.persistence.enabled: false` to fall back to an ephemeral emptyDir (dev only). |
| config.scheduler.dispatch.bufferSize | int | `0` | Depth of the queued-dispatches channel (`scheduler.dispatch.buffer_size`). 0 = synchronous dispatch inside the tick (server default). |
| config.scheduler.dispatch.workers | int | `0` | Goroutines creating task pods from the queue (`scheduler.dispatch.workers`). Ignored while `bufferSize` is 0; 0 with a buffer means 1. |
| config.scheduler.enabled | bool | `true` | Run the scheduler loop. Disable only for read-only API-only replicas (rare). |
| config.scheduler.loopIntervalMs | int | `1000` | Scheduler loop interval in milliseconds. Lower = faster reactivity, higher CPU. 1000ms is the production-tested default. |
| config.trustedProxies | list | `[]` | Proxy IPs/CIDRs whose `X-Forwarded-For` the server honors when resolving the client IP (`server.trusted_proxies`). Empty (default) trusts NO proxy, so a spoofed XFF cannot forge the client IP. Behind an ingress this MUST be set to the ingress/pod CIDR, otherwise every request is attributed to the ingress IP and the per-IP login rate-limit locks out the whole deployment on a few bad logins (#725). Rendered as a comma-joined `LEOFLOW_SERVER_TRUSTED_PROXIES` env var, which viper splits into a list. |
| database.caConfigMap | string | `""` | Name of a ConfigMap with key `ca.crt` holding the managed-Postgres CA bundle (#315). When set, the chart mounts it at `/etc/leoflow/db-ca/ca.crt` — in the control plane **and in the pre-install migration Job** (#1052), which reads the same DSN — so the operator can pin `sslmode=verify-full&sslrootcert=/etc/leoflow/db-ca/ca.crt` in the DSN. Empty (default) means TLS still works via `sslmode=require`, but the server cert is NOT verified — the connection is encrypted but MITM-vulnerable, the standard managed-DB posture before this knob. |
| database.existingSecret | string | `""` | Name of a Secret with key `databaseUrl` (takes precedence over `url`). |
| database.maxIdleConns | int | `5` | Max idle DB connections kept in the pool. Should be ≤ `maxOpenConns`. |
| database.maxOpenConns | int | `20` | Max concurrent open DB connections (Postgres-side load gate). Increase for high-throughput Pro deployments. |
| database.url | string | `""` | External Postgres DSN. Required for Pro (the embedded datastore is Lite-only); the chart `fail`s the install if neither this nor `existingSecret` is set. Example: `postgres://user:pass@host:5432/leoflow?sslmode=disable`. |
| deployment.preStopSleepSeconds | int | `0` | Seconds the control-plane container sleeps in its `lifecycle.preStop` hook before the process is signalled. `0` (default) omits the `lifecycle` block entirely. Endpoint removal is asynchronous: a terminating replica stays in kube-proxy's and every client's endpoint set for a propagation window, so task pods keep opening NEW agent log streams against a control plane that is already shutting down — and each one creates an empty log object for an attempt whose lines then have nowhere to land. This sleep spends the propagation window before SIGTERM, so those streams land on a replica that will still be alive to flush them. It uses the native `sleep` hook action rather than `exec`, because the image is distroless and has no shell. That action is gated on `PodLifecycleSleepAction`, which is beta and on by default from Kubernetes **1.30** and alpha and **off** in 1.29 (absent before that): below 1.30 the apiserver nils the field and its own validation then rejects the resulting empty `preStop: {}` with "must specify a handler type", which fails the install rather than dropping the hook. The chart therefore renders the hook only on 1.30+ and omits it below, so a 1.27-1.29 install still works — without the sleep, and the install notes say so rather than leaving the omit silent. Two limits of a version test, both real: a **helm 3** client rendering through `helm template | kubectl apply` defaults its capabilities to v1.20.0 and so omits the hook even against a 1.31 cluster — `helm install`/`helm upgrade` and Argo CD/Flux resolve the cluster's real capabilities, so the blessed paths are correct and the direction of failure is the safe one (a missing optimization, never a rejected install). And the version test is a proxy for a feature gate, not the gate itself: a self-managed 1.30+ apiserver started with `--feature-gates=PodLifecycleSleepAction=false` renders the hook and has the Deployment rejected exactly as before this gate existed, because nothing in discovery reports gate state. It runs INSIDE `terminationGracePeriodSeconds` and ahead of the drain, so it adds to the shutdown budget — count it when sizing the grace. Kubernetes validates the sleep as `0 < seconds <= terminationGracePeriodSeconds` and rejects a sleep ABOVE the grace, so the chart refuses to render one that does not fit the effective grace — 30 when `terminationGracePeriodSeconds` is left unset, nil or `0`, because the renderer omits the field on all three and Kubernetes' own default is then what the pod gets. The chart draws its line one step tighter than upstream draws it: a sleep EQUAL to the grace is a spec the apiserver accepts, but it leaves zero budget for the shutdown that runs after the sleep, so the chart refuses that too and says which of the two rules it is applying. Off by default, because a replica the streams can move TO is what makes it worth anything, and neither shipped topology has one: `replicaCount` is 1, an RWO logs PVC auto-selects the `Recreate` strategy, and in split mode the agent gRPC lives on the scheduler side, whose Deployment is pinned to a single replica. On those the sleep buys nothing **for agent log streams** and adds its own seconds of downtime to every upgrade — the same reason `terminationGracePeriodSeconds` ships empty. That is the scheduler-side singleton case, not the whole story: the hook renders on BOTH split Deployments on purpose, and on the api side with `split.api.replicaCount` > 1 the same sleep spends the same endpoint-propagation window for HTTP and UI requests, where a second endpoint really is there to take them. Set it (5 is a sensible start, and `examples/values-ha.yaml` ships that) once a second replica is serving agent gRPC. Note what it does NOT do: an already-established gRPC connection is pinned by conntrack and gets `Unavailable` at SIGTERM regardless of any sleep — only streams not yet opened are moved — and 5s is at the low end when a service-mesh sidecar is in the path. |
| deployment.strategy | string | `""` | Update strategy: `RollingUpdate`, `Recreate`, or `""` (auto). Auto picks `Recreate` when `logs.persistence` is a ReadWriteOnce/ReadWriteOncePod PVC — a RollingUpdate surges a second pod that Multi-Attach-deadlocks on the single-writer volume and the rollout never converges (#868/#282) — and `RollingUpdate` otherwise (RWX or ephemeral emptyDir). Set explicitly to override, EXCEPT that an explicit `RollingUpdate` over a single-writer logs PVC now fails the render: an override defeated the auto-selection and walked back into the same deadlock, with no second pod to serve while the rollout was wedged. The value is an allowlist of those three, trimmed first: any other spelling fails the render, because it used to be rendered verbatim into `spec.strategy.type` and compared exactly — so a lowercase `rollingupdate` skipped both the refusal and the auto-selection and the apiserver rejected the install, while `RollingUpdate ` with a trailing space skipped the refusal and the apiserver ACCEPTED it, delivering the deadlock the refusal exists to prevent. |
| execution.maxAttemptsPerWorker | int | `50` | Attempts a warm worker serves before it drains and recycles (ADR 0058 D9). Bounds credential-leak and stale-image exposure by forcing a fresh pod periodically. Ignored while `warmPoolsEnabled` is false. |
| execution.maxPoolSize | int | `8` | Cap on the warm workers a single dag_version may hold (ADR 0058 D6). Also the ceiling a DAG author's own warmth request is clamped to. Ignored while `warmPoolsEnabled` is false. |
| execution.maxWarmPodsPerTenant | int | `100` | Cap on the TOTAL warm pods one tenant may hold across ALL its dag_versions (ADR 0058 M4). Where `maxPoolSize` bounds one version, this bounds a tenant's aggregate footprint so one team cannot pin idle pods and starve neighbours on a shared cluster. Enforced by refusing to create new warm pods, never by deleting a busy worker. Ignored while `warmPoolsEnabled` is false. |
| execution.maxWorkerLifetime | string | `"1h"` | Wall-clock lifetime of a warm worker before it drains and recycles, independent of the attempt count (ADR 0058 D9). A Go duration string (`1h`, `30m`). Recycle is a graceful drain, never a mid-attempt kill. Ignored while `warmPoolsEnabled` is false. |
| execution.minIdleWorkers | int | `0` | Warm workers kept ready per dag_version when a DAG declares no warmth of its own (ADR 0058 D6). `0` (default) is scale-to-zero: nothing idles until a DAG asks for it. Ignored while `warmPoolsEnabled` is false. |
| execution.warmPoolsEnabled | bool | `false` | Reuse one warm pod across many attempts of a dag_version (ADR 0058). OFF = dedicated pod-per-task. Requires `auth.agentTokenTransport: exchange` AND `auth.secretLivenessMode: enforce`; the chart fails the render otherwise. Read ADR 0058's "Measurement & enable readiness" before enabling on a shared cluster — the go/no-go bar is stated in rates on a real cluster, not in unit tests. |
| execution.workerIdleTtl | string | `"5m"` | How long an idle warm worker is kept before it is recycled (ADR 0058 D6). A Go duration string (`5m`, `90s`). This is also what drains the pool of a superseded dag_version after a deploy. Ignored while `warmPoolsEnabled` is false. |
| executor.defaults.resources | object | `{"cpu":"","memory":""}` | Default CPU **and** memory for a task that declares none of its own. Set BOTH: the control plane applies each value as its own request AND limit, and Kubernetes grants Guaranteed QoS (least likely to be throttled/evicted under node pressure) only when requests == limits for cpu AND memory. Setting just one lands the task in **Burstable** with NO request or limit at all on the other dimension — nothing else fills it in — and the control plane logs a boot WARN naming the missing key (#802). Empty (default) leaves tasks with no platform resource floor, i.e. BestEffort QoS unless the DAG sets its own (#725). Kubernetes quantities. |
| executor.defaults.resources.cpu | string | `""` | Default CPU request+limit (e.g. `250m`, `1`). Empty = unset. Set together with `memory`. |
| executor.defaults.resources.memory | string | `""` | Default memory request+limit (e.g. `256Mi`, `1Gi`). Empty = unset. Set together with `cpu`. |
| executor.defaults.staging | object | `{"size":"","storageClass":""}` | Default size + StorageClass for the per-run staging volume when the DAG enabled staging without pinning them (#743). Env-only override path: the chart mounts no server config file, so these render as LEOFLOW_* env. Empty leaves the PVC on the cluster's default StorageClass and an unset size. |
| executor.defaults.staging.size | string | `""` | Default staging PVC size (e.g. `10Gi`). Empty = unset. |
| executor.defaults.staging.storageClass | string | `""` | Default staging PVC StorageClass (e.g. the cluster's RWX class). Empty = cluster default. |
| extraEnv | list | `[]` | Extra environment variables appended to the control-plane container, for server settings this chart does not model as a first-class value (see `docs/configuration.md` for the full `LEOFLOW_*` surface). Standard K8s `env` entries, so `valueFrom` works, and a `value` is always rendered as a string (Kubernetes rejects a numeric one). Appended AFTER the chart-managed entries; do not use it to redefine one — the chart refuses to render an entry that shadows a variable whose value it guards (the warm-pool / agent-credential coupling). Example: `[{name: LEOFLOW_SCHEDULER_DISPATCH_WORKERS, value: "4"}]`. |
| image.pullPolicy | string | `"IfNotPresent"` |  |
| image.repository | string | `"ghcr.io/dexadata/dexaflow-server"` | Control-plane image. Published by GoReleaser on every tag, signed with cosign. |
| image.tag | string | `""` | Image tag. Defaults to `.Chart.appVersion` when empty; to pin a specific release set `--set image.tag=v0.4.0-rc.2` (the `v`-prefix and no-`v` forms are both published and resolve to the same digest, so either works). |
| imagePullSecrets | list | `[]` |  |
| ingress.annotations | object | `{}` | Ingress annotations (controller-specific: rewrites, TLS, auth, etc.). |
| ingress.className | string | `""` | Ingress class name (e.g. `nginx`). Leave empty to use the cluster default. |
| ingress.enabled | bool | `false` | Enable an Ingress resource exposing the control plane via HTTP/HTTPS. Requires an Ingress controller (nginx/traefik/etc.) in the cluster. |
| ingress.hosts | list | `[{"host":"leoflow.local","paths":[{"path":"/","pathType":"Prefix"}]}]` | Host + path rules. Each host maps to one or more path entries routed to the leoflow-server's `http` port. |
| ingress.tls | list | `[]` | TLS configuration. Each entry maps hosts to a TLS Secret (typically a cert-manager Certificate Secret). |
| logs.persistence.accessMode | string | `"ReadWriteOnce"` | PVC access mode — one of `ReadWriteOnce`, `ReadWriteMany`, `ReadWriteOncePod`; any other value (a typo, an empty string, or `ReadOnlyMany`, which the log-writing control plane cannot use) FAILS the render, because an unrecognized mode used to skip both the single-writer refusal and the `Recreate` auto-selection and fail at the apiserver instead. `ReadWriteOnce` (default) is fine for single-replica deployments. At `replicaCount > 1` (or an HPA / split with more than one mounter) the chart FAILS the render on `ReadWriteOnce` / `ReadWriteOncePod` — a single-writer volume Multi-Attach-deadlocks the extra pods silently — so HA needs either `ReadWriteMany` here or `persistence.enabled: false` + the object-store `sink`. Also drives `deployment.strategy`: an RWO/RWOP PVC auto-selects `Recreate` (a rolling upgrade would Multi-Attach-deadlock). |
| logs.persistence.enabled | bool | `true` | Persist control-plane logs in a PVC (default ON). Disable for the object-store sink (HA — the bucket owns durability) or for an ephemeral emptyDir (dev only — logs lost on pod restart, and per-pod at `replicaCount > 1`). |
| logs.persistence.size | string | `"50Gi"` | PVC size for control-plane logs. ~1 GB/day per ~1000 active task runs is a sane starting point. |
| logs.persistence.storageClass | string | `""` | StorageClass for the PVC. Empty uses the cluster default. Specify an RWX class when `accessMode: ReadWriteMany`. |
| logs.sink.bucket | string | `""` | Target bucket. Required when `provider` is `s3` or `gcs`. |
| logs.sink.endpoint | string | `""` | [s3] Endpoint override for S3-compatible stores (MinIO, Ceph RGW). Empty uses the AWS default. NOT the way to reach GCS — use `provider: gcs`. |
| logs.sink.existingSecret | string | `""` | [s3] Name of a Secret with keys `accessKeyId` and `secretAccessKey` for static credentials. Discouraged — prefer keyless (IRSA). Empty (recommended) uses the ambient credential chain. (For `gcs`, use keyless Workload Identity — the discouraged file escape hatch is a config-level `logs.sink.credentials_file`.) |
| logs.sink.forcePathStyle | bool | `false` | [s3] Use path-style addressing (bucket in the path, not the host). Required by MinIO and some S3-compatible stores. |
| logs.sink.prefix | string | `""` | Optional key prefix under which attempt objects are laid out (`{prefix}/{tenant}/{dag}/{run}/{task}/{try}.log`). |
| logs.sink.provider | string | `"disk"` | Durable task-log backend: `disk` (default — the PVC above), `s3` (AWS S3, MinIO, Ceph RGW), or `gcs` (Google Cloud Storage). Object storage is opt-in; `disk` leaves the on-disk path unchanged. |
| logs.sink.region | string | `""` | [s3] Store region (e.g. `us-east-1`). Required by AWS S3; ignored by some S3-compatible stores. |
| metrics.serviceMonitor.additionalLabels | object | `{}` | Extra labels on the ServiceMonitor. Required when the Prometheus instance has a `serviceMonitorSelector` filter (e.g. `{release: kube-prometheus-stack}`). |
| metrics.serviceMonitor.enabled | bool | `false` | Enable ServiceMonitor for Prometheus scraping. Requires kube-prometheus-stack CRDs. |
| metrics.serviceMonitor.interval | string | `"30s"` | Prometheus scrape interval. |
| metrics.serviceMonitor.namespace | string | `""` | Namespace for the ServiceMonitor resource. Defaults to the release namespace; override when Prometheus expects ServiceMonitors in a dedicated namespace. |
| metrics.serviceMonitor.scrapeTimeout | string | `"10s"` | Prometheus scrape timeout (must be ≤ interval). |
| migrations.enabled | bool | `true` |  |
| migrations.image.pullPolicy | string | `"IfNotPresent"` |  |
| migrations.image.repository | string | `"ghcr.io/dexadata/dexaflow-migrate"` | leoflow-migrate image: the golang-migrate CLI compiled from the version in our `go.mod`, plus the Dexaflow SQL migrations, on distroless static. Published per release by `release.yaml`, signed with cosign, multi-arch (amd64 + arm64). |
| migrations.image.tag | string | `""` | Migration image tag. Defaults to `.Chart.appVersion` when empty. Pin to the same tag as `image.tag` (both server and migrate publish both `v`-prefix and no-`v` forms — use whichever convention you prefer, they resolve to the same digest): `--set migrations.image.tag=v0.4.0-rc.2`. |
| migrations.path | string | `"/migrations"` | Path inside the migrate image where the SQL files live. Must match the COPY destination in `deploy/Dockerfile.migrate`. |
| migrations.podSecurityContext.fsGroup | int | `65532` |  |
| migrations.podSecurityContext.runAsGroup | int | `65532` |  |
| migrations.podSecurityContext.runAsNonRoot | bool | `true` |  |
| migrations.podSecurityContext.runAsUser | int | `65532` |  |
| migrations.podSecurityContext.seccompProfile.type | string | `"RuntimeDefault"` |  |
| migrations.resources | object | `{"limits":{"cpu":"200m","memory":"256Mi"},"requests":{"cpu":"100m","memory":"128Mi"}}` | Resources for the migration Job container. Small on purpose — the work is a few dozen DDL statements — but **not empty**: with no requests the pod is BestEffort, and a namespace `ResourceQuota` on `requests.cpu`/`requests.memory` then rejects it at admission, which fails `helm install` because this is a pre-install hook (#1053). BestEffort is also first out under node pressure, and an eviction part-way through leaves the schema dirty. Limits stay within **2:1** of requests on both axes on purpose: a namespace `LimitRange` with `maxLimitRequestRatio` is as common as a quota, and a wider ratio is rejected at admission — the same install-wedge the requests are here to prevent. Keep the limits: a quota on `limits.*` rejects a pod that declares none. Clearing the block needs explicit nulls (`--set-json 'migrations.resources=null'`, or `migrations:\n  resources: null` in a values file for Argo CD / Flux, which cannot pass `--set-json`) — Helm coalesces chart defaults back into a bare `{}`, so `{}` leaves these values in place. |
| migrations.securityContext.allowPrivilegeEscalation | bool | `false` |  |
| migrations.securityContext.capabilities.drop[0] | string | `"ALL"` |  |
| migrations.securityContext.readOnlyRootFilesystem | bool | `true` |  |
| migrations.securityContext.runAsNonRoot | bool | `true` |  |
| migrations.securityContext.runAsUser | int | `65532` |  |
| networkPolicy.egress | list | `[]` | Explicit egress rules. Empty = allow-all (DNS is ALWAYS allowed regardless). Lock down to your DB/Redis/kube-apiserver endpoints in regulated environments. |
| networkPolicy.enabled | bool | `false` | Enable NetworkPolicy gating ingress + egress on the control-plane pods. Requires a CNI that enforces policies (Calico/Cilium/etc.). |
| networkPolicy.ingressFrom | list | `[]` | NetworkPolicy `from` rules for HTTP + gRPC ingress (task pods dial back). Empty = allow from any pod in any namespace. Tighten with e.g. `[{namespaceSelector: {}}]` for same-namespace only. |
| networkPolicy.metricsFrom | list | `[{"namespaceSelector":{}}]` | NetworkPolicy `from` rules for the metrics port (Prometheus scrape). **Narrow this**: the default below allows any namespace, and the metrics listener is unauthenticated and labels its series with `dag_id` and `task_id`. Replace it with your Prometheus namespace, e.g. `[{namespaceSelector: {matchLabels: {kubernetes.io/metadata.name: monitoring}}}]`. An empty list means no rule at all, which an Ingress-typed policy denies — that was the old default, and it made `networkPolicy.enabled` plus `metrics.serviceMonitor.enabled` a scrape target that never answered and failed nothing at install (#1067). Stated as a real value rather than left to a comment about what empty means, because a magic empty is how #1067 happened. |
| nodeSelector | object | `{}` | Pod nodeSelector (standard K8s scheduling label match). |
| observability.logFormat | string | `"json"` | Log format: `json` (production / log aggregators) or `console` (dev / human-readable). |
| observability.logLevel | string | `"info"` | Log level: `debug`, `info`, `warn`, `error`. Production default is `info`. |
| observability.otel.enabled | bool | `false` | Export OpenTelemetry traces. When false, internal spans are no-ops. |
| observability.otel.endpoint | string | `""` | OTLP/gRPC endpoint URL, e.g. `otel-collector:4317`. Required when `otel.enabled: true`. |
| podAnnotations | object | `{}` | Extra annotations on the control-plane pod template. EKS/Karpenter opt-in: `karpenter.sh/do-not-disrupt: "true"` exempts the pod's node from Karpenter's voluntary disruption (consolidation, drift, expiration), trading maintenance friction — that node is never consolidated or upgraded by Karpenter while the pod runs — for eviction protection. Not set by default; a second replica plus the auto PDB is the platform-neutral answer. |
| podDisruptionBudget.enabled | bool | `nil` | Tri-state. Unset/empty (default) = auto: the PDB renders iff the guaranteed replica floor is `> 1`. `true` forces it on (on a single replica this blocks node drains and auto-upgrades — an informed choice; NOTES warns). `false` forces it off. The quoted string spellings `"true"` / `"false"` are accepted too, because Argo CD's `helm.parameters` and helm `--set-string` pass every override as a string — reading those as "not a boolean" used to fall through to auto, so a forced-on budget silently never rendered. Any OTHER non-empty value fails the render rather than falling back to auto. |
| podDisruptionBudget.maxUnavailable | string | `""` | Maximum replicas allowed unavailable during voluntary disruption. Set only ONE of minAvailable / maxUnavailable — setting this one means clearing `minAvailable` (`""`), since the chart fails the render when both are set. |
| podDisruptionBudget.minAvailable | int | `1` | Minimum replicas that must remain up during voluntary disruption. Set only ONE of minAvailable / maxUnavailable — a PDB spec carries exactly one, the apiserver rejects both together, and the chart fails the render on the pair rather than letting the install fail after a clean render. |
| podDisruptionBudget.unhealthyPodEvictionPolicy | string | `"AlwaysAllow"` | `spec.unhealthyPodEvictionPolicy` (policy/v1, GA since Kubernetes 1.27; older apiservers prune the field — set `""` to omit it). `AlwaysAllow` (default) lets unhealthy pods be evicted even when the budget is unmet: with the Kubernetes default `IfHealthyBudget`, both replicas unready (bad DB credentials after a rotation, image-pull failure, wedged rollout) means even the broken pods cannot be evicted and node drains hang on an already-down control plane. |
| podSecurityContext.fsGroup | int | `65532` |  |
| podSecurityContext.runAsGroup | int | `65532` |  |
| podSecurityContext.runAsNonRoot | bool | `true` |  |
| podSecurityContext.runAsUser | int | `65532` |  |
| podSecurityContext.seccompProfile.type | string | `"RuntimeDefault"` |  |
| ports | object | `{"grpc":9091,"http":8080,"metrics":9090}` | Ports the leoflow-server listens on. http: API + UI, metrics: Prometheus /metrics, grpc: agent ↔ control plane channel (task pods dial back here). |
| probes | object | `{"liveness":{"failureThreshold":3,"initialDelaySeconds":10,"periodSeconds":20,"timeoutSeconds":5},"readiness":{"failureThreshold":3,"initialDelaySeconds":5,"periodSeconds":10,"timeoutSeconds":3},"startup":{"enabled":true,"failureThreshold":36,"periodSeconds":5}}` | Liveness/readiness probes for the leoflow-server container. Exposed so an operator can loosen the timeout under load: a busy scheduler can miss a 1s `/healthz` during a task-pod burst and be kubelet-killed mid-run, which cascades in-flight tasks to agent_lost (#860). Defaults are deliberately forgiving (5s liveness / 3s readiness timeout, 3 failures) rather than Kubernetes' 1s/3. `readiness.timeoutSeconds` has a **floor of 3** and the render fails below it: /readyz bounds its own work at 2s so it answers before the kubelet gives up, and a shorter kubelet timeout inverts that and you get a bare timeout instead of a 503 naming the failing dependency (#1041). To react faster, lower `readiness.periodSeconds` or `failureThreshold` instead. |
| probes.startup | object | `{"enabled":true,"failureThreshold":36,"periodSeconds":5}` | Startup gate for the control-plane boot. Kubernetes runs neither the liveness nor the readiness probe until this one succeeds, so a slow boot reads as "not ready yet" instead of being killed and restarted. Without it liveness points at the same API listener the boot has not bound yet, and any boot-path dependency that is slow or stuck becomes a restart loop whose every cycle is recorded as `Completed exit=0`, because SIGTERM is handled cleanly (#1083). The budget is `periodSeconds * failureThreshold` = 180s, and it is sized from the server's own boot bounds rather than picked: the Postgres connect retry is 30s and runs TWICE (the request pool and the dedicated probe pool), the pod-informer warm-up is bounded at 10s (#1141), and on top of those sit credential detection for an object-store log sink and OIDC discovery, neither of which carries a bound of its own. That is 70s of code-defined budget before anything cloud-shaped, and a gate tighter than the boot would kill a pod that was about to come up, which is the loop again. Generous on purpose, because the two directions are not symmetric: a boot that FAILS returns an error and the process exits 1, so CrashLoopBackOff reports it promptly no matter what this says, and the gate only bounds a boot that HANGS, where a restart is a lottery ticket rather than a fix. The render refuses a budget at or below the liveness budget it replaces (10 + 3*20 = 70s), since that only moves the kill from one probe to the other. It probes `/healthz`, never `/readyz`: a failing startup probe RESTARTS the container, so gating it on dependency health would turn a database outage into a crash loop, which is the exact thing `/healthz` is deliberately static to avoid. Nothing is lost by that, because readiness still gates endpoint membership on `/readyz` once the gate opens. Set `enabled: false` to go back to liveness policing the boot. |
| rbac.create | bool | `true` | Create the Role + RoleBinding the pod-per-task executor needs in `taskNamespace`: create/get/list/watch/delete on pods, get on pods/log, create/delete on persistentvolumeclaims for the per-run staging volume (ADR 0022), and create/get/delete on configmaps for the warm-pool GC anchor (ADR 0058 D11). Additionally, when `auth.agentTokenTransport` is `exchange`, a ClusterRole + ClusterRoleBinding granting `create` on `authentication.k8s.io/tokenreviews` — cluster-scoped because the TokenReview API is, and the only cluster-scoped objects this chart creates. The grant is checked against the code's actual API calls by `scripts/rbac-covers-executor.sh` in CI, so this list cannot silently fall behind the code again. Set `false` only if you bring your own Role — and then that check does not protect you. |
| redis.caConfigMap | string | `""` | Name of a ConfigMap with a `ca.crt` key containing the PEM CA bundle the client trusts when negotiating TLS to a `rediss://` URL (#312). Required when the managed-Redis server cert is signed by a provider / per-instance CA that is not in the system roots — Memorystore SERVER_AUTHENTICATION, ElastiCache in-transit encryption, Azure Cache for Redis. Mounted read-only at `/etc/leoflow/redis-ca` and exposed to the server via `LEOFLOW_REDIS_CA_FILE`. Leave empty when Redis uses a public CA or no TLS. |
| redis.existingSecret | string | `""` | Name of a Secret with key `redisUrl` (takes precedence over `url`). |
| redis.url | string | `""` | External Redis URI. Required for Pro (the embedded XCom is Lite-only). Example: `redis://host:6379/0`, or `rediss://host:6380/0` for TLS. |
| replicaCount | int | `1` | Number of control-plane replicas. The scheduler leader-elects (ADR 0009), so `>1` is HA-safe (active-passive scheduler, active-active API) and is the recommended production posture: a single replica evicted by consolidation, a drain, or a node upgrade costs a full restart (image pull + boot + leadership, tens of seconds) during which nothing dispatches; a standby replica turns that into seconds of failover. The shipped default stays `1` because HA needs a log store every replica can write — see `logs.persistence.accessMode` / `logs.sink` — and the chart refuses to render `>1` onto the default single-writer PVC. `examples/values-ha.yaml` is the one-file HA profile. |
| resources | object | `{"limits":{"cpu":"1","memory":"512Mi"},"requests":{"cpu":"250m","memory":"256Mi"}}` | Resource requests + limits for the leoflow-server container. `250m`/`256Mi` requests are a floor for a small Pro (50–500 DAGs) — bump CPU+memory for larger deployments. The request guarantees the control plane its CPU shares under node contention (so a task-pod burst can't starve it); the actual probe-timeout fix is `probes.liveness.timeoutSeconds` below (CFS throttling is bounded by the CPU *limit*, not the request). See #860. |
| secretKey | string | `""` | AES-256 key encrypting Connection passwords + Extra at rest (ADR 0019). MUST be exactly 32 raw bytes OR 64-char hex OR base64-of-32-bytes. Without it, Connection management is disabled (Variables still work). |
| secretKeyExistingSecret | string | `""` | Name of a Secret with key `secretKey` (takes precedence over `secretKey`). |
| secrets.backend | string | `""` | Provider secrets-backend class the in-pod resolver drives (e.g. `airflow.providers.amazon.aws.secrets.secrets_manager.SecretsManagerBackend`). Empty disables external secrets. |
| secrets.backendKwargs | string | `""` | Backend kwargs as a JSON object string (e.g. `'{"connections_prefix":"airflow/connections","variables_prefix":"airflow/variables","region_name":"us-east-1"}'`). A kind is served iff its `*_prefix` is present. Keyless auth (IRSA / Workload Identity) uses the task pod's ServiceAccount — set `taskServiceAccount` accordingly. |
| securityContext.allowPrivilegeEscalation | bool | `false` |  |
| securityContext.capabilities.drop[0] | string | `"ALL"` |  |
| securityContext.readOnlyRootFilesystem | bool | `false` |  |
| securityContext.runAsNonRoot | bool | `true` |  |
| securityContext.runAsUser | int | `65532` |  |
| service.annotations | object | `{}` | Service annotations (e.g. cloud LB controller hints, ExternalDNS). |
| service.type | string | `"ClusterIP"` | Service type. `ClusterIP` for internal-only; `LoadBalancer` to expose externally; `NodePort` for k3d/kind. |
| serviceAccount.annotations | object | `{}` | ServiceAccount annotations, the usual home of a cloud-identity binding for the object log sink: IRSA on EKS (`eks.amazonaws.com/role-arn`), Workload Identity on GKE (`iam.gke.io/gcp-service-account`). EKS **Pod Identity** — AWS's current recommendation — needs no annotation at all: leave this empty and create a Pod Identity association against the ServiceAccount instead. In split mode this one value is rendered onto BOTH ServiceAccounts, and both need the identity (the scheduler writes task logs, any api replica reads them back). |
| serviceAccount.create | bool | `true` | Create a dedicated ServiceAccount for the leoflow-server. Set `false` only if you bring your own via `name`. |
| serviceAccount.name | string | `""` | Override the ServiceAccount name. Defaults to the chart fullname when empty. |
| split.api.replicaCount | int | `2` | API Deployment replicas (active-active; HPA-friendly). Ignored unless split.enabled. |
| split.enabled | bool | `false` | Render separate `api` and `scheduler` Deployments instead of one `all` Deployment. Pro-only (Lite is single-host and never splits). |
| split.scheduler.replicaCount | int | `1` | Scheduler Deployment replicas. Pinned to 1 by the chart regardless of this value — the scheduler is a single leader (ADR 0009); a field is kept only for clarity. Ignored unless split.enabled. |
| taskNamespace | string | `"leoflow"` | Namespace where the control plane creates task pods + per-run staging PVCs. The chart wires this into both the executor RBAC (Role/RoleBinding) AND the server (LEOFLOW_EXECUTOR_TASK_NAMESPACE), so the server acts on exactly the namespace it is granted — override it freely (#480). |
| taskNetworkPolicy.allowMetadataEgress | list | `[]` | Host CIDRs within the always-blocked cloud-metadata range to RE-ALLOW, scoped to a single host each. Needed for keyless external-secrets auth that reaches a metadata/link-local endpoint: GKE Workload Identity (`169.254.169.254/32`) and EKS Pod Identity (`169.254.170.23/32`). AWS IRSA needs nothing here (it uses the public STS endpoint). Empty keeps the full metadata block. **One host per entry is enforced, not merely documented: the render FAILS on anything wider than an IPv4 `/32` or an IPv6 `/128`** — and on a bare address, a malformed CIDR or a scalar instead of a list. A wider entry would not widen the hatch, it would remove the block: NetworkPolicy egress rules are additive, so a rule allowing `169.254.0.0/16` overrides the `except` in the allow-all rule and the manifest still shows a block that no longer exists. To reach a wider range that is NOT the metadata range, use `extraEgress`. |
| taskNetworkPolicy.blockPrivateNetworks | bool | `false` | ALSO deny RFC1918 private ranges, which on most clusters takes the apiserver with them — but not always: GKE's newer default Service range (34.118.224.0/20) is outside RFC1918, so print your own Service CIDR before assuming. IPv4 only, so on dual-stack an IPv6 metadata or apiserver endpoint is unaffected. OFF by default: a DAG calling an internal service is legitimate, and the policy cannot tell that from the apiserver by IP (ADR 0047). Turn on for high-security clusters; the control-plane gRPC is re-allowed explicitly. |
| taskNetworkPolicy.enabled | bool | `false` | Enable the task-pod egress NetworkPolicy in `taskNamespace`. Denies ingress, allows DNS + the control-plane gRPC, then all other egress except the blocked ranges below. |
| taskNetworkPolicy.extraEgress | list | `[]` | Extra raw egress rules appended verbatim (e.g. allow a specific data store when blockPrivateNetworks is on). |
| taskNetworkPolicy.extraExcept | list | `[]` | Extra CIDRs to add to the egress `except` list (e.g. a specific internal admin subnet). |
| taskPodSecurity.readOnlyRootFilesystem | bool | `false` | Mount every task container's root filesystem read-only. Off by default deliberately: `restricted` does not require it, and it breaks ordinary Python tasks that write to `/tmp`, the pip cache, or a matplotlib config dir. Turn it on for a fleet of tasks known to write only to their mounted volumes. |
| taskPodSecurity.runAsNonRoot | bool | `true` | Refuse to start a task container whose image runs as UID 0. This completes Pod Security Admission's `restricted` set, so a `restricted` namespace will admit your task pods. On by default: the runtime base image runs as the numeric non-root UID 65532 (`USER 65532:65532`, the distroless `nonroot` UID) and the `examples/` images inherit it, and the executor pairs it with a pod-level `fsGroup` so the per-run staging PVC stays writable. **This default changed from `false` to `true`** — a fleet whose task images legitimately run as root must set this to `false` (or rebuild those images non-root), else every such task pod fails to start with `CreateContainerConfigError` ("container has runAsNonRoot and image will run as root"). This is a cluster-wide switch by design: pod security is not a per-DAG setting, so a DAG author cannot elevate their own task. |
| taskSecret.mountPath | string | `"/etc/leoflow/secrets"` | Read-only mount path in the task pod. A connection references files here, e.g. `/etc/leoflow/secrets/key.json`. |
| taskSecret.name | string | `""` | Name of an existing Kubernetes Secret to mount into task pods. Empty = none. |
| taskServiceAccount.annotations | object | `{}` | Annotations. GKE Workload Identity: `iam.gke.io/gcp-service-account: GSA@PROJECT.iam.gserviceaccount.com`. EKS IRSA: `eks.amazonaws.com/role-arn: ...`. |
| taskServiceAccount.create | bool | `false` | Create a ServiceAccount in taskNamespace for task pods to run as. When true, task pods DEFAULT to this ServiceAccount (no per-DAG `execution.service_account` needed) — so keyless secret access works out of the box; a DAG may still set `execution.service_account` to override per task. NOTE: the auto-default is wired only when `create: true`. If you bring your own pre-existing SA (`create: false` + `name:`), task pods do NOT auto-default to it — reference it per-DAG via `execution.service_account: <name>` (which always works), or let the chart both create and default to it with `create: true`. |
| taskServiceAccount.imagePullSecrets | list | `[]` | Pull secrets for private DAG images. Kubernetes auto-injects these into every task pod that runs as this SA, so a `dexaflow deploy` to a private registry can be pulled (ADR 0041). Reference an existing docker-registry secret, e.g. `[{name: regcred}]`. |
| taskServiceAccount.name | string | `"leoflow-task"` | Name of the task ServiceAccount. With `create: true` it becomes the default task-pod SA; you can also reference it explicitly as `execution.service_account`. |
| terminationGracePeriodSeconds | string | `""` | Pod `terminationGracePeriodSeconds` for the control plane. Empty (default) omits the field: Kubernetes applies its own 30s, a default install's pod spec is unchanged, and no downtime is added to a single-replica `Recreate` upgrade (a chart default would). An explicit `0` and an explicit `null` are treated the same as empty — the field is omitted and **Kubernetes' 30s applies**. The chart deliberately does NOT render a literal `0`: that means SIGKILL with no grace at all, so nothing drains — in-flight HTTP requests are cut, the dispatch pool never settles (task instances are left stuck `queued`), open agent log streams are never flushed, and any `deployment.preStopSleepSeconds` becomes unsatisfiable. If you truly want no grace, set it on the pod spec yourself; the chart's `0` means "the Kubernetes default applies". A voluntary eviction sends SIGTERM and waits this long before SIGKILL. What the grace buys is headroom for the HTTP shutdown (in-flight requests get up to 10s) and the dispatch-pool drain (in-flight dispatches settle instead of leaving task instances stuck `queued`). It does NOT decide leadership handoff — the scheduler lease is released within a tick of SIGTERM and frees anyway when the connection drops. With tasks running, open agent log streams are closed and flushed at SIGTERM and the gRPC stop is bounded (5s, then forced), so the shutdown completes within the default 30s instead of being SIGKILLed. `deployment.preStopSleepSeconds` runs INSIDE this grace and ahead of everything above, so count it in the budget. The HA profile sets `60` for drain headroom under load. |
| tolerations | list | `[]` | Pod tolerations (standard K8s — allow scheduling on tainted nodes). |
| topologySpreadConstraints | list | `[]` | Pod `topologySpreadConstraints` for the control plane (standard K8s schema), rendered on every control-plane Deployment. A constraint that omits `labelSelector` gets the Deployment's own selector labels, so a values file need not know the release name or role. Empty by default. Set for HA: consolidation bin-packs both replicas onto one node unless told otherwise, and two replicas on one node are one replica — a node failure takes the whole control plane, and the PDB then blocks that node's drain. Use `whenUnsatisfiable: ScheduleAnyway`, not `DoNotSchedule`: the latter leaves the second replica Pending on a single-node cluster, where the PDB then blocks every drain. |
| ui.autoRefreshIntervalSeconds | string | `""` | How often the bundled SPA re-polls DAG, run and task-instance state, in seconds. Empty uses the server's production-safe default of 30s. It is POLLING, so this multiplies request and query load by the number of open browser tabs, not by the number of users: 5s is thirty tabs' worth of traffic at 30s, and 1s is what `dexaflow lite` uses precisely because Lite is one person against a local database. Lower it when operators watch runs progress and the metadata database has room; the honest way to choose is to measure one refresh cycle against your own DAG count rather than to copy Lite (#1196). A zero or negative value is not an error and not a fast refresh: the server falls back to 30s, deliberately, so a misconfigured variable cannot drop the interval to 0 and hammer the database. |
| ui.faviconUrl | string | `""` | Favicon URL for the UI, http(s) or root-relative. Empty keeps the stock icon. |
| ui.homeLink.label | string | `""` | Text of an optional link from the UI back to the platform you serve it from, such as an internal portal (#1290). Shown on every page at the bottom-left, opens in the same tab. Set it together with `url`. |
| ui.homeLink.url | string | `""` | Absolute `http://` or `https://` URL the home link opens. Empty (the default) shows no link and renders no variable. The server refuses to boot on a URL with another scheme or with a label missing. |
| ui.stylesheetUrls | list | `[]` | Extra stylesheets every UI page loads, typically the web fonts a `theme` names (for example a Google Fonts CSS URL). Each must be http(s) or root-relative and contain no comma, because the list travels comma-joined in `LEOFLOW_UI_STYLESHEET_URLS`. |
| ui.theme | object | `{}` | Theme for the bundled UI, in the shape of Airflow's `[api] theme` (#1289): `tokens` (Chakra design tokens, for example `colors.brand` with shades 50 to 950 and `fonts.heading`/`fonts.body`/`fonts.mono`), `globalCss`, `icon` and `icon_dark_mode`. Rendered as compact JSON in `LEOFLOW_UI_THEME`. Empty (the default) keeps the stock look. The server refuses to boot on an unknown top-level key, or an icon that is not an http(s) URL or a root-relative path. |
