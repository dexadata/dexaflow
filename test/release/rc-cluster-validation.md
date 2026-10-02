# RC cluster-validation runbook

**Reusable per RC and per cloud.** Hand this file to whoever (or whatever agent) has cluster access; fill in the run header and follow it top to bottom.
**Why this exists:** CI runs the pod-path E2E on **k3d + kindnet**, which cannot
enforce NetworkPolicy, provide ReadWriteMany, model cloud IAM (IRSA / Workload
Identity), real StorageClasses, node pools, eviction, or apiserver-scale watch. A
whole class of behavior is therefore only provable on a real cloud cluster. ADR
0033 leaves the human RC verification open ("whatever the rc is meant to
validate") — this file is that checklist.

**How to use:** fill in the run header, follow §0→§6, record PASS/FAIL in §6, and
open one issue per FAIL (root-caused, file:line if known — same bar as the
#722–#729 batch). For the **next** RC, keep §0/§2/§4/§5 as-is and refresh only
**§3a** (the "features this RC shipped") from that release's CHANGELOG
`[Unreleased]` / release notes. **§3b carries forward** — those rows stand until
the issue they name closes.

---

## Run header (fill in)

| Field | Value |
|---|---|
| RC under test | `<tag>` |
| Cloud / cluster | `<AWS EKS / GCP GKE>` (context: `<kubectl-context>`) |
| CNI | `<AWS VPC CNI / GKE Dataplane V2 / Calico>` — note it, §5 depends on it |
| Node provisioning | (managed node group / Karpenter / GKE node pool?) |
| GKE mode | `<Standard / Autopilot>` — Autopilot mutates pod specs; see §5 |
| Server image | `ghcr.io/dexadata/dexaflow-server:<tag>` |
| Date / operator | `<date>` / `<who>` |

Links: release <https://github.com/dexadata/dexaflow/releases/tag/TAG> ·
Helm guide <https://dexaflow.dexadata.ai/operate/helm-chart/> ·
chart README (full values) <https://github.com/dexadata/dexaflow/blob/main/helm/dexaflow/README.md> ·
install page <https://dexaflow.dexadata.ai/get-started/installation/>.

---

## §0 Prerequisites

- `kubectl` pointed at the target cluster (EKS / GKE Standard / other); `helm` v3.
- **External Postgres + Redis** reachable from the cluster (the chart runs on any
  cluster with external datastores — see the chart README "datastore
  compatibility" for the exact secret/URL value keys; do not guess them).
- A container registry the cluster can pull from for **your DAG images**
  (ECR / Artifact Registry).
  The control-plane image comes from `ghcr.io/dexadata/dexaflow-server:<rc>`.
- **Cloud identity** for keyless auth, to annotate the ServiceAccounts: EKS
  **IRSA** (`eks.amazonaws.com/role-arn`) or GKE **Workload Identity**
  (`iam.gke.io/gcp-service-account`) — see §4.2/#728 and §5.
- The `leoflow` CLI locally (client):
  `LEOFLOW_VERSION=<tag> curl -fsSL https://raw.githubusercontent.com/dexadata/dexaflow/main/install.sh | sh`
  (explicit tag — "latest" skips pre-releases).

---

## §1 Install (Helm / Pro)

> **Before the tag is cut (maintainer preflight).** The chart `version` and
> `appVersion` in `helm/dexaflow/Chart.yaml` must equal the release tag (minus the
> leading `v`) **before** you `git tag` — ADR 0028 keeps them in lockstep, and the
> image tags default to `.Chart.AppVersion`, so a stale Chart.yaml makes a default
> `helm install ./helm/dexaflow` pull the **previous** release's images (this bit
> `v0.4.0-rc.3`, which shipped with Chart.yaml still on `rc.2` — arestas #3).
> Bump both keys, then verify with:
>
> ```bash
> scripts/check-chart-version-matches-tag.sh vX.Y.Z   # or $GITHUB_REF_NAME in CI
> ```
>
> The same gate runs automatically on every `v*` tag in
> `.github/workflows/helm-release.yaml`, so a cut with a stale chart fails the
> Helm chart release job rather than shipping wrong image defaults. Once tagged,
> install the **published OCI chart** (`--version` = tag without the `v`) rather
> than a source checkout — see the [chart README](https://github.com/dexadata/dexaflow/blob/main/helm/dexaflow/README.md#quick-start).

Confirmed chart value keys used below (defaults in parens) — see the chart README
for the datastore/secret keys this runbook intentionally does not spell out:

- `split.enabled` (false) — `true` splits into `role=api` + `role=scheduler` (ADR 0049).
- `replicaCount` (1).
- `serviceAccount.annotations` / `taskServiceAccount.annotations` — IRSA role-arn.
- `rbac.create` (true) — renders the executor Role (+ the cluster-scoped
  `tokenreviews` grant when `auth.agentTokenTransport=exchange`).
- `auth.agentTokenTransport` (`envvar`) / `auth.secretLivenessMode` (`observe`) /
  `auth.secretScoping` (`permissive`) — the last one became a chart value in #803;
  before that it was reachable only through `extraEnv`.
- `execution.warmPoolsEnabled` (false) — requires `agentTokenTransport=exchange`
  **and** `secretLivenessMode=enforce` (chart refuses to render otherwise).
- `config.trustedProxies` (`[]`) — **#725**, must be reachable now.
- `executor.defaults.resources.cpu` / `.memory` (`""`) — **#725**, QoS defaults;
  set **both or neither** (**#802**: one alone is Burstable and suppresses the
  other dimension entirely).
- `networkPolicy.enabled` (`false`) / `taskNetworkPolicy.enabled` (`false`) —
  **#804**, two independent policies; the second is the task-pod containment.
- `logs.persistence.enabled` / `.accessMode` (`ReadWriteOnce`) / `.storageClass`.

Baseline install (single-replica, dedicated pod-per-task, defaults):

```bash
helm install leoflow oci-or-repo/leoflow \
  --version <chart-version-for-rc> \
  -n leoflow --create-namespace \
  -f <your-values>.yaml   # e.g. values.eks.yaml / values.gke.yaml
```

Record: does the control plane reach `Ready`? Is `/api/v2/` + the UI reachable
(via port-forward or the ingress)? Can you `dexaflow auth login` and get a JWT?

---

## §2 Smoke — the path a user actually runs

1. `dexaflow auth login` → JWT.
2. Register + trigger a DAG (author per
   <https://dexaflow.dexadata.ai/author-dags/dag-authoring/>; `dexaflow push` / `dexaflow deploy`).
3. **PASS:** every task instance reaches `success` — i.e. a real pod-per-task ran,
   its agent reported over gRPC, XCom chained. `kubectl get pods -n <taskNamespace>`
   shows one pod per task, completed.

---

## §3 Feature validation

**§3a is refreshed per RC** from that release's CHANGELOG `[Unreleased]`.
**§3b is not** — those rows stand until the issue they name closes, and carry
forward across RCs. Deleting a §3b row is a decision, not a refresh.

### §3a: the post-0.4.6 tranche

✔ = also unit/e2e-verified; ★ = **only a real cluster proves it well**.

Refreshed from `[Unreleased]`. The previous tranche's rows are gone by design,
with one exception, noted below and carried into §3b: rows that were never
measured do not get to disappear just because the release they belonged to
shipped.

This tranche has a third shape again. The v0.4.5 tranche was four defects in one
untested fixture. The v0.4.6 tranche was mostly documentation that had gone false.
This one is **failures that report themselves as success**: a control plane whose
every restart exits 0, an SSO deployment that boots green and rejects every login,
a destructive API default that looks like a preview, a chart key that accepted a
value and read it nowhere. None of them produce a red signal on their own, which
is why most rows below are about what an operator can *see*, not about whether
the code runs.

Single sign-on became the bulk of it, so rows 13 to 18 are one journey rather
than six unrelated checks: a deployment that cannot work says so at boot, a login
that is refused reaches the user as a page and the operator as a reason, and the
sign-in page offers only the way in that exists. Run them in that order against
one deployment; each row leaves the cluster in the state the next one needs.

| # | What it proves | How to run it, and what decides it | ★/✔ |
|---|---|---|---|
| 1 | Boot survives a cache it cannot sync (#1083) | Remove `list`/`watch` on pods from the control plane's Role, in `executor.taskNamespace`. **Decides:** the pod reaches Ready, `/readyz` **answers** (503 or 200, never connection refused), the warn line names the namespace and the budget, and `restartCount` stays 0 across five minutes. Pre-fix it climbed by one every ~70s. | ★ |
| 2 | The same failure under a CNI that enforces (#1083) | The trigger the local gates structurally cannot produce: kindnet accepts NetworkPolicy and ignores it. On EKS with the VPC CNI policy agent **actually enabled** (assert the agent is running first, or this passes vacuously), set `networkPolicy.egress` to Postgres and Redis only, omitting 443 to the apiserver. **Decides:** same as row 1. | ★ |
| 3 | A slow boot is not a restart loop (#1150) | With row 1's broken RBAC still in place. **Decides:** `startupProbe` is present on the rendered pod, the container is not killed while the gate is failing, and the restart count stays 0 for longer than `10 + 3*20 = 70s`, which is where the pre-fix kill landed. | ★ |
| 4 | An unsatisfiable SSO config fails boot, by name (#1143) | Set `auth.provider: oidc` with issuer, client id and redirect URL but **no** `tenant_claim`. **Decides:** boot fails with a message naming `auth.oidc.tenant_claim` and `auth.oidc.tenant_claims`, and saying the map loads only from the YAML file named by `LEOFLOW_CONFIG`. A green boot here is the bug: it then rejects 100% of logins, and the cause reaches only the audit row and the server log (#1162) while the browser is returned to the sign-in page with a marker that carries no reason (#1167). In an SSO-only deployment nobody can log in to read the audit log, so the boot message is the only channel left. | ★ |
| 5 | A deployment whose logins resolve to no roles says so (#1143, #1163) | Two shapes, both of which WARN now. (a) `role_mappings` and `default_role` both empty. (b) `role_mappings` set and `default_role` empty, which is where a Google deployment lands because Workspace emits no `groups` claim without Directory API sync. **Decides:** the boot WARN appears for BOTH, names `auth.oidc.default_role`, and (b) is the one the previous RC did not warn on. Then the consequence, which needs a cluster: pre-provision a user with `admin`, log in through the IdP, and confirm the grants are CLEARED rather than left alone. | ★ |
| 6 | The migration Job goes where the control plane goes (#1056) | Taint a node so only the control plane's `nodeSelector`/`tolerations` can land on it, then `helm upgrade`. **Decides:** the migrate pod is scheduled, not Pending. Pre-fix it ignored placement entirely and could land anywhere, or nowhere. | ★ |
| 7 | A database outage reads as an outage, not the caller's fault (#1087, #1071) | Break the DB mid-run. **Decides, for an authenticated request (#1087):** the API answers 503, detail `authentication temporarily unavailable`, never 401, and the driver detail reaches only the server log, never the response body (the body is fixed text; do not expect it to name Postgres). Same for `POST /api/v2/auth/token/renew`. **Decides, for a `ti_summaries`-style read under a genuine outage (#1071):** the response is 500, not 499; then cancel the client connection mid-request against a healthy DB and confirm that case still reports 499, so the fix did not just move the misclassification the other way. (The task pod's own error surface, no SQLSTATE reaching the caller, is #1068, already cluster-validated and closed; it is not part of this tranche and does not need re-proving here.) | ★ |
| 8 | `clear` re-runs the image that produced the attempt (#1138, BREAKING) | Push a new DAG version, then clear a task from an **older** run without `run_on_latest_version`. **Decides:** the dispatched pod runs the OLD image. Then send `run_on_latest_version: true` and confirm it runs the new one. The UI's checkbox should now appear, since `bundle_version` is populated on both payloads. | ★ |
| 9 | An unflagged `clear` cannot destroy state (#1149) | `POST .../clearTaskInstances -d '{"dag_run_id":"r"}'` with no other flags. **Decides:** the response is a preview, nothing is cleared, and the affected set contains no task instance in a `success` state. Then confirm all three UI clear dialogs still execute, since they send both flags explicitly. | ✔ |
| 10 | The chart's CORS key reaches the server, and `*` is refused (#1144) | `helm template` with `config.cors.allowedOrigins: ["*"]`. **Decides:** the render **fails** with a message naming `extraEnv` as the deliberate route. With real origins, confirm `LEOFLOW_SERVER_CORS_ALLOWED_ORIGINS` is present in the pod and the server honours it. | ✔ |
| 11 | A dbt-only DAG keeps its declared secrets (#997) | Run a dbt DAG that declares `connections:` and `variables:` on the cluster. **Decides:** the task receives them. Pre-fix a dbt-only DAG dropped its own declarations. | ★ |
| 12 | The DAG-image build pipeline still ships nothing secret (#1062, #1081, #1114) | This tranche changed the Dockerfile/`.dockerignore` generation itself (`include_paths`, `exclude_paths` negation handling, dbt folder grouping); this is the regression check that none of those reopened #995/#1013/#1064, which are closed and were already cluster-validated as passing on the v0.4.6 RC. On the RC image, by hand: no `=<version>` file in the workdir, no `.user.yml`, no `logs/dbt.log` carrying a build-host path, and a declared version floor is honoured against an older base. | ✔ |
| 13 | A name that cannot resolve is named, at boot, before anyone tries to log in (#1168, #1172, #1173) | One deployment, four settings wrong at once: `tenant_claims` mapping a domain to a tenant that does not exist, `default_role: viewr`, `client_secret` empty, `break_glass_emails` listing an address with no local password account. **Decides:** boot logs a WARN for each, quoting the wrong value, and the pod still becomes Ready. Then set a tenant name to blank (`corp.example:` with nothing after it) and confirm boot **fails** instead, naming `auth.oidc.tenant_claims`. The split matters: a live-database check must not turn a lagging replica into a restart loop, a blank string in a file can never be correct. | ★ |
| 14 | A refused login reaches the user as a page, and the operator as a reason (#1162, #1169) | Log in with an account whose `hd` is not in `tenant_claims`. **Decides, in the browser:** it lands on the sign-in page with the failure banner, NOT on raw JSON, and the URL and page carry no reason. **Decides, for the operator:** the audit row says `tenant_not_allowed` and the server log has one WARN naming it. Repeat with a deactivated user (`inactive`) and with `jit_provisioning: false` (`no_user_jit_off`) so the decoder table on the Google page is checked against what the deployment actually writes. | ★ |
| 15 | The sign-in page offers the way in that exists, and only that one (#1160, #1161) | Three renders. (a) `provider: jwt`: no SSO control, focus in the username field. (b) `provider: oidc` with `break_glass_emails` set: SSO control plus a working password form. (c) `provider: oidc` with the list EMPTY: the SSO control, and the password form collapsed behind a summary naming `break_glass_emails`, with no autofocus. **Decides:** (c) is the default SSO posture, and before this tranche it presented a form that accepts nobody as the primary control. | ✔ |
| 16 | An unreachable IdP is a named boot failure, not a restart loop (#1153, #1163) | Point `issuer` at a host that accepts the connection and never answers (a NetworkPolicy that DROPs egress to the IdP is the realistic trigger, and is the case the local gates cannot produce). **Decides:** boot fails within ~15s with a message naming the issuer and both possible causes, rather than hanging until the kubelet kills a pod on a probe failure that names nothing. Confirm the startup-probe budget still exceeds the code floor (`scripts/check-startup-probe-budget.sh`). | ★ |
| 17 | Google Workspace end to end, on a real tenant (#1164) | The only row here that needs a real Google client. `tenantClaim: hd` with exactly one domain. **Decides:** the authorization redirect carries `hd=<domain>` and the chooser offers only that domain; a personal account is still rejected server-side if forced (the pin is the claim, not the parameter); and a first login provisions the user with `defaultRole`. | ★ |
| 18 | The chart renders a complete SSO install from values alone (#1159) | `helm install` with the `auth.oidc.*` block, including the two maps, and nothing in `extraEnv`. **Decides:** the ConfigMap is mounted, `LEOFLOW_CONFIG` points at it, the client secret comes from `existingSecret` and is absent from the ConfigMap, and the render REFUSES when `tenantClaim`/`tenantClaims` are not both set. | ✔ |

**Execution record, v0.4.7-rc.2.** Rows 13, 14, 15, 16 and 18 were run against
the commit this RC is cut from. Row 18 was run as a real `helm install` on k3d,
and it is the one that earned its keep: the first boot of a correct install
warned that its own break-glass account could not sign in, because the check ran
before `bootstrapAdmin` created it (#1175). No unit test could see that ordering,
and neither could a local run against a database prepared by hand.

Row 14 was driven with a real rejection through the fail-closed path, which
proved the whole channel (browser page, audit row, one WARN) for one reason,
`missing_state`. The other reasons in the decoder table need a signed ID token,
so they remain unit-verified only.

**Row 17 was NOT executed.** It needs an OAuth client in a real Google Workspace
tenant, which this cut did not have. What that leaves unproven is narrow and
worth naming rather than glossing: the `hd` parameter reaching Google's real
account chooser, and a first login provisioning through it end to end. Everything
`hd` depends on is covered elsewhere (the parameter's construction by unit test,
the tenant pin by unit test, the authorization redirect's shape by the e2e
against a fake IdP), and the parameter is a hint with no security weight, since
the pin is the verified claim on the returned token. It carries forward to §3b
rather than retiring, tracked as #1177.

**Process notes, both of which have cost a cut before.**

Build the RC image from the commit under test (#1019). Rows 1, 2, 3, 4, 5, 8 and
9 say nothing at all about an image built from anything else.

`#1079` is a known flake and has already reddened an unrelated PR. A rerun is a
rerun, not a signal.

Three rows of the v0.4.6 tranche were never measured on a cloud CNI or a real
admission controller (`#1089`) and do not get to disappear just because the
release they belonged to shipped. Unlike this tranche's rows, they name an issue
that is still open, which is what earns a row the carry-forward below instead of
retirement; see §3b.

### §3b — standing assertions (carry forward until the issue closes)

**Carried from the v0.4.7 tranche, pending #1177** (row 17, never executed
because no cut so far had a Google Workspace tenant to point at):

- **#1164 the `hd` hint reaches Google's real account chooser.** With
  `tenantClaim: hd` and exactly one accepted domain, the authorization redirect
  must carry `hd=<domain>` and the chooser must offer only that domain, and a
  first login through it must provision with `defaultRole`. **PASS:** both hold
  against a real Workspace client. Not a security claim: the pin is the verified
  `hd` CLAIM on the returned token, and the parameter is attacker-editable, so a
  failure here means the chooser still offers accounts the pin will reject, which
  is the problem this was meant to remove rather than a new one. Everything the
  hint is built from is covered without a tenant (the parameter by unit test
  including the two cases where it is deliberately not sent, the pin by unit
  test, the redirect's shape by `e2e-sso-login` against a fake IdP).

**Carried from the v0.4.6 tranche, pending #1089** (never proven on a cloud CNI
or a real admission controller; not standing by original design, but they earn
the same carry-forward treatment for as long as #1089 stays open):

- **#958 `allowMetadataEgress` really is one host, on a CNI that enforces.**
  This is a **security posture claim**: if it is false, the documentation needs
  correcting whether or not a release is in flight. On EKS with the VPC CNI
  network-policy agent **enabled** (assert the agent is running first, or this
  passes vacuously), set `allowMetadataEgress: [169.254.170.23/32]`. **PASS:**
  from a task pod, `169.254.170.23` answers and `169.254.169.254` times out.
  Repeat on GKE Dataplane V2 and Calico; this is the path CNIs diverge on.
  Retire this row when #1089 closes.
- **#1067 the metrics port under a CNI that enforces ✔ (kind+Calico only).**
  `pro-netpol-rwx.sh` already asserts `:9090` with `ingressFrom` narrowed on
  kind+Calico. **PASS on a cloud cluster:** the same holds under EKS's VPC CNI,
  and a real Prometheus in another namespace can scrape it. Retire this row when
  #1089 closes.
- **#947 the rendered HPA is admitted ✔ (kind dry-run only).** kind dry-runs the
  rendered HPA already. **PASS on a cloud cluster:** no admission webhook
  (OPA/Kyverno/Gatekeeper) rejects or mutates it, and `HPAScaleToZero` is
  confirmed off on the cluster's Kubernetes version; the `min<1` refusal
  assumes it. Retire this row when #1089 closes.

**Standing checks (no expiry):**

- **#800 zero-declaration blind spot.** The #722 row below validates the population
  that *does* warn, so a green RC has been certifying past the one that does not.
  With `secretScoping=permissive` (default), run a DAG declaring **no** variables
  and **no** connections against a tenant that has several connections defined.
  **PASS (today's behavior, and the blind spot made executable):** the task
  receives **every** connection in the vault — observe this with a probe task that
  prints the `AIRFLOW_CONN_*` **names** it was handed, never their values — and
  `GET /api/v2/eventLogs` shows
  **no** `secret.scope_warning` row for that run. That silence is exactly what an
  operator would read as "safe to flip to `enforce`", while this same DAG would
  receive **nothing** after the flip. Until #800's code half lands, this row is
  the reminder that a clean trail is not evidence; when it lands, this row
  inverts — the run must then produce a warning naming zero declarations.
- **#800 stale-declaration blind spot** — the second population the trail never
  shows, and the likelier one in practice, since secret rotation produces it. The
  warning counts only declared names that actually *resolve*, so a DAG whose
  declarations all point at names since deleted from the vault collapses to zero
  declared and never warns — registration-time validation (#724) guards the
  moment of registration, not later deletion. With `secretScoping=permissive`,
  register a DAG declaring `conn_a`, run it, then delete `conn_a` from the vault
  and run it again. **PASS (today's behavior):** the second run receives **every**
  connection in the vault and `GET /api/v2/eventLogs` shows **no**
  `secret.scope_warning` row for it. Flip that same DAG to `enforce` and confirm
  it receives **nothing** — and that no audit row explains why.
- **#722 secret-audit ✔** — with `secretLivenessMode=observe`, run a DAG that
  declares fewer secrets than the vault. **PASS:** `GET /api/v2/eventLogs` shows a
  `secret.scope_warning` row (not just a log line). Flip a scenario to `enforce`
  and confirm a `secret.liveness_denied` row is written too. (This is the row the
  two #800 rows above exist to qualify: it validates the population that *does*
  warn.)
- **#724 validation 400 ✔** — register a DAG version declaring an unknown
  connection. **PASS:** API returns **400** (not 500); message points at
  `dexaflow connections set`.
- **#727 migration-job SA token ✔** — `kubectl get job <migrate> -o yaml`.
  **PASS:** `spec.template.spec.automountServiceAccountToken: false`.
- **#729 managed-PG idempotent** — Lite/local, not a cloud cluster. Run the opt-in
  E2E `test/e2e/lite-managed-pg-reextract.sh` on a networked host with managed-PG.
- **task pods non-root by default** — the rc.2 behavior change, still standing.
  `kubectl get pod <task> -o jsonpath='{.spec.securityContext}'`. Root-assuming
  DAG images fail (expected).

---
## §4 Deep cluster-only checks (k3d cannot show these)

### §4.1 — #726: api role never receives the gRPC TLS private key (split mode)
```bash
helm upgrade leoflow ... --set split.enabled=true --set agentTLS.enabled=true --set edition=pro
kubectl get deploy -n leoflow            # expect an api and a scheduler deployment
```
- **PASS:** the **api** pod's projected volumes contain **no `tls.key`**
  (`kubectl get pod <api-pod> -o yaml | grep -A3 grpc-tls` → nothing on api,
  present on scheduler), the api pod **boots** (TLS env vars still set so the Pro
  boot guard passes), and secrets RPC scheduler→pod still flows (a task succeeds).

### §4.2 — #723 (retry wedge) + #728 (warm TMPDIR)
Enable warm pools (forces the security prereqs):
```bash
helm upgrade leoflow ... \
  --set execution.warmPoolsEnabled=true \
  --set auth.agentTokenTransport=exchange \
  --set auth.secretLivenessMode=enforce
```
- **#723 PASS:** force a lingering `Pending` try-1 pod (an unschedulable
  `nodeSelector`, or a bad image → `ImagePullBackOff`), let the task retry to try 2,
  and confirm the try-2 TI **fails/recovers** rather than sticking in `queued`
  forever. *(EKS makes this more reachable — Karpenter cold nodes + ECR pull
  latency produce long-lived Pending pods.)*
- **#728 PASS:** a warm worker serving two attempts of the same dag_version: write
  a sentinel file under `$TMPDIR` in attempt N, assert it is **absent** at the
  start of attempt N+1 on the same pod. Also check IRSA-derived creds cached to
  `~/.aws/cli/cache` — those live under `$HOME` and, per the shipped decision,
  **still persist** (mitigation: `read_only_task_root_filesystem` for warm pods).

### §4.3 — #725: QoS + ingress ClientIP
```bash
helm upgrade leoflow ... \
  --set executor.defaults.resources.cpu=250m \
  --set executor.defaults.resources.memory=256Mi \
  --set 'config.trustedProxies={<AWS-ingress-CIDR>}'
```
- **QoS PASS:** a task declaring no resources shows
  `kubectl get pod <task> -o jsonpath='{.status.qosClass}'` = **`Guaranteed`**
  (not `BestEffort`); both `requests` and `limits` present. Drive a node to memory
  pressure → the task pod is **not** first evicted.
- **PARTIAL-pair PASS (#802):** this section only ever set both fields, so it
  certified past the partial case entirely. Re-run with **cpu only**:
  ```bash
  helm upgrade leoflow ... --set executor.defaults.resources.cpu=250m \
    --set executor.defaults.resources.memory=""
  ```
  **PASS** is all three: the control-plane log carries one boot `WARN` with
  `config_key=executor.defaults.resources_cpu` and
  `missing_config_key=executor.defaults.resources_memory`; a task declaring no
  resources shows `qosClass` = **`Burstable`**, *not* the `Guaranteed` the chart
  comment used to promise; and `kubectl get pod <task> -o jsonpath='{.spec.containers[0].resources}'`
  shows **no memory request or limit at all**. Also compile a `leoflow.yaml` whose
  `defaults.resources` sets only `cpu` — **PASS:** `dexaflow compile` **fails**
  naming the missing field (`missing property 'memory'`), it does not produce a
  `dag.json`.
- **ClientIP PASS:** behind the ALB/NLB, several bad logins from **different**
  clients do **not** share one lockout bucket once `config.trustedProxies` is set
  to the ingress CIDR (before the fix, all requests collapsed to the ingress IP and
  a handful of bad logins locked out everyone).

### §4.4 — #804: the two NetworkPolicy values are not one value

```bash
helm upgrade leoflow ... --set networkPolicy.enabled=true
```
- **Separation PASS:** with the control-plane policy ON and
  `taskNetworkPolicy.enabled` left at its `false` default, **no policy selects a
  task pod**. Check selection, not object count: `taskNamespace` defaults to
  `leoflow`, which is the release namespace in §1's baseline, so counting objects
  there returns the control-plane policy and reads as a FAIL on a correct
  install.

  ```bash
  kubectl -n <taskNamespace> get netpol -o json | jq '
    [ .items[]
      | select( (.spec.podSelector.matchLabels // {} | keys)
                + [ (.spec.podSelector.matchExpressions // [])[].key ]
                | any(startswith("leoflow.io/")) )
    ] | length'
  # PASS = 0
  ```

  Task pods carry only `leoflow.io/*` labels (`internal/executor/kubernetes.go`),
  while the control-plane policy selects `app.kubernetes.io/name` +
  `instance` — so it cannot select a task pod even in the same namespace. The
  task pods are uncontained, which is exactly what the hardening section used to
  deny. `kubectl -n <releaseNamespace> get networkpolicy -o yaml` shows the
  control-plane policy's `spec.egress` ending in the **empty rule (`{}`)**, i.e.
  allow-all: enabling it restricts ingress only.
- **Containment PASS:** re-run with `--set taskNetworkPolicy.enabled=true` and
  confirm one policy in `<taskNamespace>`, **and that this CNI enforces it** — a
  task pod's `curl` to `169.254.169.254` must time out. On kindnet it will not
  (kindnet enforces nothing), and the AWS VPC CNI enforces only with its
  network-policy agent enabled: a rendered object is not evidence.
- **Keyless caveat PASS:** with the task policy on, a DAG using keyless
  external-secrets auth (GKE Workload Identity / EKS Pod Identity) fails until
  `taskNetworkPolicy.allowMetadataEgress` re-allows that one `/32`. Confirm the
  failure *and* the recovery — this is why the policy stays opt-in.

---

### §4.5 — HA posture: the PDB that appears on `helm upgrade`, and restart recovery

Two `[Unreleased]` changes with no other executable statement in this file.

**PDB (changes existing installs on upgrade).** The budget now renders
automatically whenever the guaranteed replica floor is `> 1` — every
`split.enabled` install (api default 2) and every `autoscaling.enabled` install
(`minReplicas` default 2). A budget that appears unannounced is what hangs node
drains and stalls auto-upgrades.

`--set split.enabled=true` alone **will not render** on §1's baseline: the chart
refuses more than one control-plane pod against a ReadWriteOnce logs PVC. Turn
the PVC off (and ship logs to object storage), or layer the HA profile over your
own values — never apply `values-ha.yaml` alone, it carries `CHANGEME`
placeholders for the database, Redis and secrets that would repoint the control
plane at a nonexistent Postgres:

```bash
helm upgrade leoflow ... -f <your-values>.yaml -f helm/dexaflow/examples/values-ha.yaml
kubectl get pdb -n <ns> -l app.kubernetes.io/instance=leoflow
```

**PASS:** a PDB now exists that you never asked for. That object's existence is
the whole observation — it is what flips if the change is reverted.

**The drain is a separate, weaker check.** A drain completes with no PDB at all,
and faster, so "the drain completed" does not discriminate. It is worth running
only to catch the budget *blocking* one, and it needs a precondition or it reads
FAIL on a correct install — the chart ships no default anti-affinity, and
`values-ha.yaml` spreads with `whenUnsatisfiable: ScheduleAnyway`, which is
best-effort and can still bin-pack both replicas onto one node:

```bash
kubectl get pods -n <ns> -l app.kubernetes.io/instance=leoflow -o wide   # two distinct NODEs
kubectl drain <node> --ignore-daemonsets --delete-emptydir-data
```

**Not worth running: the `unhealthyPodEvictionPolicy` field check.**
`AlwaysAllow` is the chart default and only an apiserver older than 1.27 prunes
it — neither EKS nor GKE will sell you one, so the check prints `AlwaysAllow`
unconditionally. The observation that *would* flip is the behavior: break **both**
replicas (bad DB credential or a bogus image), then drain. Under `AlwaysAllow`
the eviction succeeds; under `IfHealthyBudget` it blocks on an already-down
control plane. Stage that if you want the row, and skip it otherwise.

The chart **fails the render** if `minAvailable` and `maxUnavailable` are both
set — worth one deliberate attempt, since the point of that guard is to fail at
render rather than at install.

**Restart recovery (ADR 0052) — do NOT hand-roll this.** Killing the control
plane mid-task and watching the task settle to `success` proves nothing: the
agent's terminal report retries for as long as the control plane is unreachable,
so the ordinary report path produces the same green without the recovery path
ever running. `test/e2e/chaos-runtime.sh` already covers it on k3d, and refuses
that exact false green — it bakes `LEOFLOW_CHAOS_DIE_BEFORE_REPORT` into the DAG
image (dispatch strips author-declared `LEOFLOW_`-prefixed keys, #828/#829) and
fails loudly if the pod ends `Completed`, because that means the fault seam never
fired. Run it rather than improvising:

```bash
LEOFLOW_CHAOS_ONLY=CD test/e2e/chaos-runtime.sh
```

**PASS:** scenarios C and D both pass. Two caveats worth stating, because they
bound what a green here means:

- The script is **not in any workflow** — only `make chaos-runtime`, which
  nothing invokes. An RC is the moment it actually gets run, which is why it is
  named here at all.
- It runs the control plane as **host processes** from `bin/leoflow-server` on a
  k3d cluster it creates and deletes. So it validates **the tree at the RC tag**,
  not `ghcr.io/dexadata/dexaflow-server:<rc>`. Check out the tag and build
  before running it, and record that the published image is not what was
  exercised.

Prerequisites beyond §0: a Linux docker host (a Lima VM on macOS), `k3d`, `jq`,
a migrated external Postgres, and **`bin/leoflow` + `bin/leoflow-server` already
built** — the script builds the base image but not those two.

### §4.6 — #1023: readiness fails when the schema disappears under a running pod

The v0.4.5 RC found this the expensive way: an ephemeral-storage Postgres came
back **empty** after a node recycle and the running control plane kept answering
`/readyz` 200 while the scheduler could not read `dag_runs`. A Ping proves the
connection, not the schema, so nothing in the probe noticed.

It has two halves, and **both** have to be run. The first is the fix. The second
is the outage the fix can cause if it is built naively, and it is the more
expensive of the two to discover in production.

#### §4.6a — a schema that vanishes takes the pod out of rotation

Do this against a **throwaway** database — it drops the schema.

**Port-forward to the POD, not the Service.** This is the whole point of the
check: a correct build leaves the Service's endpoints within ~30s, and a
port-forward through the Service dies with it. `curl` then prints `000` — a
connection failure, not an HTTP status — and `000` is not `503`, so the row
would read FAIL against a build that is working exactly as intended. Forwarding
to the pod keeps talking to it after it is out of rotation, which is also the
only way to see the 503 body at all.

```bash
NS=<ns>
POD=$(kubectl get pod -n "$NS" -l app.kubernetes.io/instance=leoflow -o name | head -1)
kubectl port-forward -n "$NS" "$POD" 8080:8080 &          # 9090 in the scheduler-only role
PF=$!

psql "$DATABASE_URL" -c 'DROP SCHEMA public CASCADE; CREATE SCHEMA public;'
```

Readiness is `periodSeconds: 10 × failureThreshold: 3`, so the flip takes up to
30s. Wait for the condition rather than sampling it — checked instantly, a
**correctly fixed** build still shows `READY 1/1`, and the row cannot fail:

```bash
kubectl wait --for=condition=Ready=false --timeout=60s -n "$NS" "$POD"
curl -sS -o /dev/stderr -w '%{http_code}\n' localhost:8080/readyz    # expect 503
kubectl get endpoints -n "$NS" -l app.kubernetes.io/instance=leoflow  # pod's IP gone
```

**PASS:** `kubectl wait` returns success (it did not time out), `/readyz` answers
**503**, the pod's IP is no longer in the Service's endpoints, and the 503 body
names only the dependency — **no DSN, credentials or internal hostname**, since
the endpoint is unauthenticated. The version detail is in the pod log.

Read the detail string, do not just grep for 503. `postgres schema not current`
is the schema verdict; `postgres unavailable` is a database that could not be
**read** — a timeout, a reset connection, an exhausted pool. Both are 503 and
only the first one is this check. A run that shows `postgres unavailable` here
has proved something about the pool, not about #1023.

**Liveness must NOT fire, and this is the half that needs patience.** Liveness is
`periodSeconds: 20 × failureThreshold: 3`, so a restart needs **60s of continuous
failure**. `RESTARTS` read immediately after the DROP is `0` whether liveness is
schema-blind or not, so the instant check proves nothing. Hold the broken state
for ~90s and assert `/healthz` stays 200 the whole time — a positive assertion,
not the absence of a restart:

```bash
end=$((SECONDS+90))
while [ $SECONDS -lt $end ]; do
  printf '%s ' "$(curl -sS -o /dev/null -w '%{http_code}' localhost:8080/healthz)"
  sleep 10
done; echo
kubectl get pod -n "$NS" "$POD" -o jsonpath='{.status.containerStatuses[0].restartCount}{"\n"}'
```

**PASS:** every sample is `200` and the restart count is unchanged from before
the DROP. A `CrashLoopBackOff`, or any 5xx from `/healthz`, is a FAIL — liveness
is deliberately schema-blind, because restarting creates no schema and a crash
loop would cost you the pod you need to diagnose.

**Recovery — two traps, both of which have cost time.**

*The Job is not there to re-run.* `migration-job.yaml` carries
`helm.sh/hook-delete-policy: before-hook-creation,hook-succeeded`, so after a
successful install the Job object is **deleted**. `kubectl create job
--from=job/leoflow-migrate` answers `NotFound`. What re-runs the migration is a
Helm operation that fires the `pre-upgrade` hook:

```bash
helm upgrade leoflow <chart> -n "$NS" --reuse-values --wait
```

*`CREATE SCHEMA public` does not restore who may use it.* The recreated schema is
owned by whichever role ran the `psql`, and every grant that was on the old one
went with it. If the migrate Job connects as a different role — the usual split,
and mandatory under the read-only `role:api` identity (ADR 0049) — it fails on
`permission denied for schema public` and the Job's `backoffLimit: 3` turns that
into a failed `helm upgrade` several minutes later. Restore ownership first,
substituting your own roles:

```sql
ALTER SCHEMA public OWNER TO <migrate role>;
GRANT USAGE, CREATE ON SCHEMA public TO <migrate role>;
GRANT USAGE ON SCHEMA public TO <api role>;   -- only if you run the split identity
```

**PASS:** `/readyz` returns to 200 and the pod re-enters the endpoints **without
a restart** — the check is per-probe, so recovery needs no pod churn. Note that
`--wait` now blocks on real readiness, which is the #1023 fix seen from the other
side: before it, `helm upgrade --wait` reported success against this database.

Then drop the forward, or it will outlive the pod it points at and quietly
mislead the next step: `kill $PF`.

#### §4.6b — a rolling upgrade never drops the Service to zero endpoints

The reason §4.6a is not the whole check. The migrate Job is a
`pre-install,pre-upgrade` hook, so on **upgrade** it runs while the OLD pods are
live and serving, and golang-migrate commits `dirty=true` for the duration of
each migration body. A readiness probe that treats `dirty` as not-ready flips
**every** old replica at the same instant — they all read the same one row — for
any migration longer than `3 × 10s`. The Service reaches zero endpoints, and in
the `all` role that Service also carries gRPC, so running task pods lose the
control plane mid-upgrade. Readiness is version-aware on `dirty` precisely to
avoid this, and the property is not observable in §4.6a.

Staging a genuinely slow migration is awkward and cloud-dependent, and it is not
what is being tested — the pods only ever see one row. So write that row directly
into the state golang-migrate leaves it in for the duration of a migration body,
and hold it there longer than `3 × 10s` of probing:

```bash
# terminal 1 — watch the endpoints for the whole window; never let this reach 0
kubectl get endpoints -n "$NS" leoflow -w

# terminal 2 — park the schema in the state a long migration produces:
#   dirty, at a version ABOVE what the running binaries embed
psql "$DATABASE_URL" -c \
  'UPDATE schema_migrations SET version = version + 1, dirty = true;'
sleep 60
psql "$DATABASE_URL" -c \
  'UPDATE schema_migrations SET version = version - 1, dirty = false;'
```

**PASS:** across the whole 60s the endpoints list never empties, `/readyz` on a
running pod stays **200**, and the pod log carries **one** line about a migration
being in flight — not one per probe period. If the endpoints drop to zero, or
`/readyz` goes 503 here, readiness is treating an in-flight forward migration as
a broken schema and every `helm upgrade` with a slow migration is an outage.

**The other arm, and it must FAIL closed.** The same state at or *below* the
running binary's own version is a genuinely half-applied schema it depends on,
and must take the pod out of rotation:

```bash
psql "$DATABASE_URL" -c 'UPDATE schema_migrations SET dirty = true;'   # same version
kubectl wait --for=condition=Ready=false --timeout=60s -n "$NS" "$POD"
psql "$DATABASE_URL" -c 'UPDATE schema_migrations SET dirty = false;'
```

**PASS:** the pod goes NotReady here. A build that stays ready through **both**
arms has not fixed #1023, it has disabled the dirty check.

#### §4.6c — #1055: the migration hook pod is not part of the control plane

`helm upgrade` runs the migrate Job as a `pre-upgrade` hook while the Service and
the PodDisruptionBudget are already live. Its pod used to carry the exact
control-plane selector labels, and a Job pod has no readiness probe, so it was
`Ready` — and therefore selected — from its first instant. The fix gives it its
own application name; these checks confirm that on a real cluster, in the window
where it matters.

The window is short on a fast migration, so hold it open. `pause` ignores the
Job's args and never exits, which is a hook that stays running until you delete
it — the upgrade will fail at its timeout, which is fine, this run is the
measurement, not the release:

```bash
helm upgrade leoflow ... --set migrations.image.repository=registry.k8s.io/pause \
                         --set migrations.image.tag=3.9 --timeout 150s &
```

**Selection PASS.** Ask the apiserver, not the chart — it is the component that
evaluates the selector:

```bash
kubectl -n "$NS" get pods -l app.kubernetes.io/name=leoflow,app.kubernetes.io/instance=leoflow
# PASS = only the control-plane pod(s). A leoflow-migrate-* row here is the bug.
kubectl -n "$NS" get endpointslice -l kubernetes.io/service-name=leoflow \
  -o jsonpath='{range .items[*].endpoints[*]}{.addresses[0]}{" "}{.targetRef.name}{"\n"}{end}'
# PASS = no leoflow-migrate-* target.
```

**Disruption PASS.** This was the half that actually cost something, and it needs
a budget present, so force one on (`--set podDisruptionBudget.enabled=true`) or
run the HA profile:

```bash
kubectl -n "$NS" get pdb -o custom-columns=\
'NAME:.metadata.name,ALLOWED:.status.disruptionsAllowed,CURRENT:.status.currentHealthy,REASON:.status.conditions[0].reason'
```

PASS = `currentHealthy` equals the number of *control-plane* replicas, and the
reason is never `SyncFailed`. On the pre-fix chart this reported one pod too many,
and with a percentage-valued `minAvailable` it reported
`jobs.batch does not implement the scale subresource` and pinned
`disruptionsAllowed` at 0 — a stalled drain for the length of the migration.

**The part k3d cannot settle, and EKS must.** No request was lost on k3s even
before the fix, and only by accident: every Service port here uses a *named*
`targetPort` and the hook pod declares no container port, so the endpoint landed
in a slice with `ports: null` and kube-proxy programmed nothing. Anything that
reads EndpointSlices and resolves ports for itself never had that protection. On
the EKS RC, with the **AWS Load Balancer Controller in IP-target mode** in front
of the api Service, re-run the hold-open upgrade and watch the target group:

```bash
aws elbv2 describe-target-health --target-group-arn <arn>
```

PASS = the migrate pod's IP is never registered. Record **NOT VERIFIED (cloud)**
for this row on any run that is not EKS with an IP-mode ALB/NLB — a k3d pass says
nothing about it. Same for a service mesh: an Istio/Linkerd sidecar reads the
EndpointSlice directly.

---

## §5 EKS ↔ GKE deltas (so a GKE pass doesn't give false confidence)

- **Identity:** EKS **IRSA** (`eks.amazonaws.com/role-arn`) vs GKE **Workload
  Identity** (`iam.gke.io/gcp-service-account`). Annotate `serviceAccount` (log
  sink to S3) and `taskServiceAccount` (task cloud access).
- **CNI:** AWS VPC CNI enforces NetworkPolicy differently from GKE Dataplane V2 /
  Calico; kindnet (CI) enforces nothing. Re-check any netpol-dependent path here.
- **Storage:** RWO = EBS; **RWX = EFS** (needed when `replicaCount>1` with
  `logs.persistence.accessMode=ReadWriteMany`). GKE uses PD / Filestore.
- **Ingress:** ALB (IP vs instance target mode) / NLB (proxy-protocol) change what
  `ClientIP` sees → the §4.3 `trustedProxies` CIDR is cloud-specific.
- **GKE Autopilot vs Standard:** Autopilot mutates pod specs, injects resource
  requests and constrains securityContext / ephemeral storage — which lands
  directly on §3a/#852 (read-only rootfs + an unsized emptyDir) and on §4.3's QoS
  row. This runbook assumes **Standard**; record the mode in the run header.
  *Confirm this against your own cluster before trusting it — it is the one delta
  here not verified against the code.*

---

## §5b What a GKE run cannot settle

No §3a row **depends** on a §5 delta — image content, entrypoint strings,
container security context and YAML strictness have no cloud delta. (#852 names
Autopilot only to rule it out as a justification; it does not rest on it.) The
rows that do depend on a delta are all in §4, and a GKE pass on these is not an
EKS pass:

- **§4.2 / #728 — IRSA credential cache.** "creds cached to `~/.aws/cli/cache`"
  has no Workload Identity equivalent; that half is unrunnable on GKE. The
  `$TMPDIR` sentinel half does transfer. Split the row: **NOT VERIFIED (cloud)**
  for the cache.
- **§4.3 / #725 — ClientIP.** GCLB's `X-Forwarded-For` shape differs from
  ALB/NLB, and the `trustedProxies` CIDR does not transfer. Run it for the code
  path; record **NOT VERIFIED (cloud)** for the EKS claim.
- **§4.4 / #804 — containment.** This is the **most misleading row to pass on
  GKE**, because the delta runs the wrong way: Dataplane V2 enforces
  NetworkPolicy natively, so the metadata-egress test passes almost by default,
  while the AWS VPC CNI enforces *nothing* unless its network-policy agent is on.
  A green here is the weakest evidence in the file. Record **NOT VERIFIED
  (cloud)** for EKS and re-run on the EKS RC with the addon flag in the header.

---

## §6 Results + reporting

Record **NOT VERIFIED (cloud)** rather than PASS for anything §5 says this cloud
cannot settle. A row left blank reads as "not run"; a row marked PASS on the wrong
cloud reads as proof, and that is the failure §5 exists to prevent.

| # | Check | PASS/FAIL | Notes / evidence |
|---|---|---|---|
| §2 | smoke: task→success | | |
| #17 | hybrid: python tasks succeed (§3a) | | |
| #20 | dbt_groups project present in the image, both shapes (§3a) | | |
| #993 | bare compile + /spec: no host path in any entrypoint (§3a) | | |
| #994 | no-connection group succeeds AND carries --profiles-dir (§3a) | | |
| #994 | second arm: a group **with** `connection:` still succeeds (§3a) | | |
| #852 | read-only rootfs: pod admitted, dbt tasks succeed (§3a; no cloud delta) | | |
| #1005 | `docker run … dbt --debug parse`: full or partial? (local, §3a — a question, not a gate) | | |
| #15 | stray top-level key fails validate AND compile; Lite discovery unaffected (§3a) | | |
| #800 | zero-declaration: no scope_warning, task gets every connection (§3b) | | |
| #800 | stale-declaration: deleted conn → no warning; enforce → nothing (§3b) | | |
| #722 | audit rows written | | |
| #723 | retry not wedged (§4.2) | | |
| #724 | validation → 400 | | |
| #725 | QoS Guaranteed + ClientIP (§4.3) | | |
| #802 | partial resource pair → Burstable + boot WARN + compile error (§4.3) | | |
| #804 | control-plane policy on, task policy off → no policy **selects** a task pod (§4.4) | | |
| #726 | api has no tls.key (§4.1) | | |
| #727 | migrate job no SA token | | |
| #728 | warm TMPDIR fresh (§4.2) | | |
| #729 | managed-PG re-extract (Lite host) | | |
| — | task pods non-root by default | | |
| HA | HA-profile upgrade: a PDB exists that nobody asked for (§4.5) | | |
| #1023 | schema dropped under a running pod: `/readyz` 503, pod leaves endpoints, no restart across 90s (§4.6a) | | |
| #1023 | in-flight migration above the running binary: endpoints never empty, `/readyz` stays 200; dirty at the same version still goes NotReady (§4.6b) | | |
| ADR 0052 | `LEOFLOW_CHAOS_ONLY=CD chaos-runtime.sh` — C and D pass (§4.5) | | |

For each FAIL: open an issue on `dexadata/dexaflow` with the root cause and, where
possible, the file:line (the #722–#729 batch is the quality bar). A red RC →
fix → **rc.4** (tags are immutable, ADR 0033); a green RC → the GA promotion is a
separate maintainer decision.
