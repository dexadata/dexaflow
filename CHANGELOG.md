# Changelog

All notable changes to Leoflow are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project aims to
adhere to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.4.8] - 2026-09-21

### Added

- **The UI refresh interval is settable from the chart
  (`ui.autoRefreshIntervalSeconds`).** It was reported that the Pro UI feels far
  slower to update than Lite, and it does: Pro polls every 30s and Lite every 1s,
  a thirty-fold difference. The setting to change it already existed and was
  documented, and the chart modelled nothing, so a Helm operator could reach it
  only through `extraEnv`, which hides the behavior from anyone reading the
  chart.

  The chart omits the variable entirely when unset rather than rendering an empty
  one, so the server's default stays in charge and nobody goes looking in the
  chart for a number the chart did not choose.

  **This exposes the choice; it does not change the default.** Copying Lite's 1s
  would multiply request and query load by thirty per open tab, and Lite can
  afford that only because it is one person against a local database. Choosing a
  better default needs the cost of one refresh cycle measured, which is tracked
  in [#1196](https://github.com/neochaotic/leoflow/issues/1196) along with the
  question of whether polling is the right mechanism at all.

- **A long-running resilience soak battery (`test/soak/`).** The gates we had
  answer a different question: `test/e2e/` proves a path works once, `test/load/`
  measures one cost at one instant, and `chaos-runtime.sh` injects a fault and
  checks the recovery. None told us whether a control plane that has been
  dispatching since Friday is still dispatching on Monday, or whether the cost of
  a tick had started tracking the size of the history table rather than the
  active set.

  `make soak` runs a realistic scheduled workload (six DAG projects spanning the
  `python`, `bash` and `airflow_operator` task types, with DuckDB generating the
  data volume) against a dedicated local Postgres and asserts ten invariants on
  every sample: wedge thresholds on `queued`/`scheduled`/`running`, scheduler
  health, run-creation cadence per DAG, leader churn, retry budget, archived-
  attempt state, and terminal-run consistency. (The archived-attempt check is
  cheap and holds, but it is not an at-most-once proof: see `test/soak/README.md`
  section 1 for exactly what it can and cannot catch.) Evidence is written
  continuously
  (`samples.jsonl` fsynced per record, `summary.md` and `verdict.json` rewritten
  atomically every sample), so a harness that is killed still leaves a current
  report.

  `make soak-selftest` proves the assertions can fail: it injects a real 300 s
  Postgres outage while declaring a 45 s window for it, and the run must exit
  exactly 1 with recorded violations (exit 2, a harness that never ran, is a
  failure of the self test, not a pass). Nothing is faked and no threshold is
  relaxed.

  Everything runs locally and costs nothing: no cloud, no cluster, no paid
  service, and no public HTTP endpoint anywhere in the workload (the operator leg
  points at a loopback fixture server, and CI enforces that). Bounded by a
  wall-clock ceiling, a disk budget with a clean stop, and a watchdog. Long runs
  are scheduled locally via `test/soak/schedule/install.sh`; CI runs only a
  6-minute harness smoke, for the cost reasons documented in
  `test/soak/README.md`.

- **`auth.session_cookie_insecure`** (default `false`), the one escape hatch the
  fix above needs. `Secure` is now decided by the server rather than by the
  page's `location.protocol`, and a browser refuses a `Secure` cookie from a
  plain-http origin that is not loopback, so a deployment served over plain http
  to a real hostname would otherwise have been upgraded into a sign-in page that
  posts valid credentials, gets a `200`, and lands back on itself. It cannot be
  derived from the request: behind a TLS-terminating ingress the server sees
  plain http while the browser sees https, so request-derived `Secure` would
  strip it from the deployment that most needs it. Operator-scoped, `WARN` at
  boot while it is on, and no Helm value on purpose.

- **`auth.oidc.auto_redirect` starts the flow instead of showing the sign-in
  page.** Off by default. Where an edge proxy has already authenticated the
  session, or SSO is the only way in, that page was a screen to acknowledge for
  nothing; a comparable tool against the same identity provider lands the user
  inside with no visible login step.

  **Signing out reaches the page, not the flow.** `logoutHandler` redirected to
  the bare sign-in URL, which with auto-redirect on is itself a redirect to the
  identity provider. Our sign-out does not end the IdP session, so a user who
  signed out would be signed straight back in and the button would appear to do
  nothing, and the more reliable the SSO setup is, the more completely it fails.

  It is **suppressed on a refused sign-on**, and that guard is the feature. A
  denial answers a redirect back to the sign-in page, so redirecting it onward
  would bounce every refusal straight back to the identity provider: an infinite
  loop with no surface left to read the error on. It is also suppressed by
  `?local=1`, so a break-glass account can reach the password form when the
  identity provider is the thing that is broken, without an operator editing
  values and rolling out to get back in.

### Changed

- **Changelog entries are now one file per pull request.** `make changelog` (a wrapper around [changie](https://changie.dev)) writes `.changes/unreleased/<slug>.yaml`, and the release cut folds every pending fragment into `CHANGELOG.md` under `## [Unreleased]`. Before this, every open PR edited the same `## [Unreleased]` lines in one file: merging any one of them made the rest dirty, each rebase cost a full CI cycle of around fifty-five checks, and resolving those conflicts by keeping both sides is how the section came to hold five headings for three kinds. Two fragments are two different files and cannot conflict. Editing `CHANGELOG.md` by hand still satisfies the guard. (#1200)

- **The documentation version menu says which release you are reading.** The
  current release now appears as `v0.4.7 (latest)` rather than `latest`, and the
  unreleased leg as `dev (main, unreleased)`. Before this, the current release
  was the ONE release whose number the menu never showed: an archived tag shows
  its number only after it has been superseded, so the number a reader most
  wants was the one missing. The project's own maintainer read the menu and
  concluded `latest` meant `main`.

  A new gate reconciles the menu the published site uses with the fallback a
  local `hugo` build uses. The two had already drifted: one listed a release the
  other did not, so a local build and the published site disagreed about which
  releases exist.

- **The session cookie is now `Secure` by default**, decided by the server
  rather than by the page's `location.protocol` (see the fix below).

  **Upgrading a deployment served over plain http on a name that is not
  localhost:** set `auth.session_cookie_insecure: true` BEFORE you upgrade. A
  browser refuses a `Secure` cookie on such an origin, and it refuses the
  `Secure` deletion too. So with the default, a new login is silently discarded
  **and sign-out stops signing anybody out**: the pre-existing non-`Secure`
  cookie from the old build stays in the jar, stays a valid session, and cannot
  be cleared until its original lifetime runs out. Loopback is unaffected,
  because browsers treat it as potentially trustworthy, and so is anything
  behind TLS, which is every chart install.

### Fixed

- **A GA release page carried none of the release.** The body was generated
  from the commits since the previous tag, and a GA is cut from its own release
  candidate, so the only commit between the two is the promotion itself. The
  v0.4.7 page said one line, `release: promote v0.4.7 GA`, while `CHANGELOG.md`
  held 35 entries for that same version: everything written for a human to read
  stayed in a file, and the page most people reach from GitHub showed nothing.

  The body is now composed from the changelog section for the tag, so a release
  page says what the release did. A candidate falls back to `[Unreleased]`,
  which is where its entries are. The commits are still one click away, as a
  compare link.

  The generated list was also keeping noise it meant to drop: the filters were
  anchored as `^docs:`, `^test:` and `^chore:`, and every commit in this
  repository is scoped, as in `docs(changelog):`, so none of the three ever
  matched anything.

- **Building and deploying in separate steps could name the same image two
  different ways** (#1227). A project that sets `registry.tag_strategy:
  git_sha` had its image pushed under the DAG version by `leoflow compile
  --build --push`, while `leoflow deploy` looked for it under the commit SHA,
  because the build never consulted the strategy and the deploy did.

  This only shows up when the two commands run separately, which is the normal
  CI/CD shape: build in one job, deploy in another with `--skip-build`. A single
  `leoflow deploy`, which does both, was never affected. With the default
  strategy the two rules happen to agree, so the failure needed a non-default
  setting as well.

  The error made it worse by pointing elsewhere. It named the image it could not
  find rather than the disagreement, which reads as a failed push and sends you
  to check registry credentials that were never the problem.
- **The release cut's dry run described a changelog it was no longer going to
  produce.** On an rc it printed `changelog: unchanged`, which stopped being
  true when the cut started folding the pending `.changes/unreleased/`
  fragments into `[Unreleased]`. The plan now names how many fragments will be
  folded and removed, on an rc and on a GA alike. A plan that understates what
  a release will do is wrong in the one place someone reads before authorising
  it.

- **The first-run check said the system Python was fine and then setup
  downloaded a different one** (#1224). `leoflow doctor` and `leoflow setup
  --dry-run` reported a host Python 3.12 or 3.13 as the interpreter that would
  be used, while `leoflow setup` looks for `python3.11` specifically and fetches
  a managed CPython when it does not find one. Behind a proxy or offline that is
  a hard failure at a step the check had just called green.

  Neither half was wrong on its own, which is why it survived: the check answers
  what can parse a DAG, and any 3.11 or newer can, while setup answers what the
  runtime is pinned to. They were answering different questions in the same
  sentence. Both now say when a download is coming and which interpreter would
  avoid it, and the quickstart prerequisites name the network dependency.

- **A warm worker carried one task's leftover processes into the next** (#1216).
  A process a task left behind kept running: the process group was killed only
  when the run was cancelled, never when the task simply exited. Under one pod
  per task that is invisible, because the pod ends and takes everything with it.
  On a warm worker, which serves attempt after attempt in one container, a
  survivor reached the next attempt holding the previous one's environment,
  including its secrets and its attempt token, with read and write access to the
  next attempt's working directory. The scratch directory was already wiped
  between attempts; the processes were not.

- **An out-of-memory kill is described as one when the control plane has to
  recover the outcome itself** (#1216). When a task is killed for memory the
  pod's status says `OOMKilled`, and the reconciler preferred a bare exit code
  from the agent's own record. It now prefers the pod's description when the
  record carries no explanation of its own.

  Scope worth stating: this is the recovery path only. When the agent's report
  arrives normally, which is the common case, the task instance is already
  settled and the reconciler's description is not applied. Recognising an
  out-of-memory kill on that path is still open.

- **A single sign-on deployment whose IdP stopped answering tied up a request
  goroutine per login attempt, with nothing on our side bounding it** (#1153).
  This package makes three outbound calls to the IdP: discovery at boot, the
  JWKS fetch on any login whose signing key is not cached, and the code
  exchange on every login. All three fell back to Go's default HTTP client,
  which has no timeout.

  Only the first was protected, by a deadline on the boot context. A deadline
  cannot reach the second: the key set is built over a background context, so
  the request's own deadline never applies to it, and the server sets no write
  timeout. An IdP that accepted the connection and then went silent therefore
  held a goroutine per attempt until the browser gave up.

  Every call now goes through a client that carries a 15 second timeout, which
  is the bound that applies whatever context the caller passes. It matches the
  boot deadline so the two cannot drift into disagreeing about how patient a
  deployment is.

- **The scheduler handed back its own leadership once an hour** (#1199). The
  advisory lock that makes one replica the scheduler is session-scoped: it lives
  and dies with the connection holding it. pgxpool applies a default connection
  lifetime of one hour when the database URL does not set one, and leoflow's do
  not, so the leader pool's single connection was recycled every hour and the
  leadership went with it.

  Every layer below that behaved correctly, which is why it went unnoticed: the
  lock check reported the lock gone, because it was, and the scheduler stepped
  down, because a leader that cannot prove it holds the lock must not keep
  scheduling. The split-brain guard worked exactly as designed and the scheduler
  still gave up leadership hourly for no reason. A 15.7-hour soak recorded 15
  step-downs in a single process with nothing contending.

  On a single replica the cost is a pause in scheduling. On several it moves
  leadership, and whatever the leader was in the middle of, on a timer nobody
  chose. The leader pool now keeps its connection, which is the whole reason it
  is a dedicated single-connection pool; a connection that really dies is still
  caught within one 5-second watch tick, and the step-down that follows is a
  real one.

- **A password login could not replace a live SSO session, and a JWT-only
  deployment's session cookie was readable by any script** (#1191). The two
  login paths set the same `_token` cookie by different mechanisms: the OIDC
  callback set it server-side and `HttpOnly`, the sign-in page set it from
  JavaScript with `document.cookie`. A script cannot overwrite an `HttpOnly`
  cookie, so on top of a live SSO session the browser silently discarded the
  token a break-glass login had just been issued. The server answered `200`, the
  audit recorded a success, and the UI went on showing the SSO identity, which
  reads as "the escape hatch did not open" at the one moment break-glass exists
  for.

  Looking for it surfaced the wider half, which nobody had reported: a
  deployment on `auth.provider: jwt` never reaches the OIDC callback, so its
  session cookie was **only ever** the page-set one, not `HttpOnly`, readable by
  anything running on the page. Same cookie, same session, two security
  postures, decided by which button the user pressed.

  `POST /auth/token` now sets the session cookie itself, through the same helper
  the SSO callback uses (`HttpOnly`, `Secure`, `SameSite=Lax`, path `/`,
  `Max-Age` = the token TTL), and the page's `document.cookie` write is gone.
  The response body still carries `access_token`, so the CLI, the SPA and every
  other API client are unaffected; logout clears through the same helper, so the
  deletion cannot drift from what the login set.

  Setting a cookie there also had to be kept from becoming a login-CSRF hole,
  which `/auth/token` did not have while it answered with a body only. The
  handler binds JSON without looking at `Content-Type`, so a page on another
  origin can POST credentials it controls with no preflight and, if the response
  set a cookie unconditionally, plant its own session in the visitor's browser.
  The cookie is therefore written only for a request the browser itself reports
  as same-origin (`Sec-Fetch-Site`); a cross-origin caller gets the body and no
  cookie, and a caller that sends no such header keeps today's behavior. That
  last group is every non-browser client, which holds no cookie jar, and also a
  browser on a plain-http origin that is not loopback, which is sent no fetch
  metadata at all and so gains nothing here; that deployment already carries the
  session token in the clear, which is what `auth.session_cookie_insecure` says
  on the tin. The OIDC callback is deliberately exempt: it is a
  cross-site navigation from the IdP by construction, and its signed single-use
  state cookie is what binds it to a flow this browser started.

- **A tenant claim that is an array no longer rejects every login.** The claim
  was read as a string and nothing else, which is correct for `hd` and `tid` and
  not for `aud`, which OpenID Connect defines as a string **or** an array. An
  operator behind an identity provider that issues no domain claim reaches for
  `aud`, and against one that emits the array form every login was refused as
  `tenant_not_allowed`: a message that sends you to inspect a map that is
  correct.

  A string or an array is now accepted. An array naming two accepted tenants is
  refused as `tenant_ambiguous` rather than resolved to one, because it
  identifies neither and the choice would decide which tenant's data the session
  reaches. A claim that is neither shape is refused as `tenant_claim_shape`, so
  the audit log separates a value we cannot read from a tenant that is not
  allowed.

## [0.4.7] - 2026-09-19

### Added

- **SSO is configurable from the chart (`auth.oidc.*`).** The server has had OIDC
  since ADR 0057, but no Helm install could turn it on: the chart exposed no
  `auth.oidc` key, and `validateOIDC` requires `auth.oidc.tenant_claims`, a map
  that loads only from the YAML file named by `LEOFLOW_CONFIG`, which the chart
  never mounted and still has no `extraVolumes` to mount. SSO was not
  misconfigured in Kubernetes; it was unreachable.

  The scalars now render as `LEOFLOW_AUTH_OIDC_*` environment variables, and only
  the two maps (`tenant_claims`, `role_mappings`) go into a ConfigMap as a
  deliberately partial `config.yaml`, mounted read only. Partial is safe because
  `LoadServer` reads the file before the environment, so every other setting
  keeps arriving by env and outranks the file.

  The IdP client secret goes to the Secret, never the ConfigMap, with its own
  `auth.oidc.existingSecret`: the JWT signing key and the IdP secret rotate on
  different clocks.

  Three render guards fail the template rather than the pod (a missing tenant
  pin, a non-`https` issuer, and an `extraEnv` that hijacks `LEOFLOW_CONFIG`),
  because a chart error is visible to an Argo CD sync and `NOTES.txt` is not.

  `auth.oidc.jitProvisioning` defaults to `true` in the chart, unlike the
  server's own default, because `false` denies every first SSO login: a login is
  matched by `(oidc_provider, oidc_subject)` and just-in-time provisioning is the
  only path that ever writes those columns, so there is no account to match. The
  tenant pin, `email_verified` and `allowedEmailDomains` still decide who may be
  created, and a created row carries only the roles `roleMappings`/`defaultRole`
  resolve, which is none by default.

  **What to change:** nothing, unless you want SSO. With `auth.oidc.enabled`
  false, the render is byte-identical to before. See
  `helm/leoflow/examples/values-oidc-google.yaml` for a Google Workspace setup
  pinned on the `hd` claim.

- **BREAKING: `clear` now re-runs a task on the image that produced it, not the
  newest one (`run_on_latest_version`).** Clearing always re-bound the run to the
  DAG's current version, so "clear last week's task" executed today's code and
  there was no way to ask for anything else.

  The flag now chooses, and defaults as Apache Airflow does: `false` keeps the
  version the run was created with; `true` re-binds to the current version.

  **What to change:** clearing a task to test a fix no longer picks the fix up on
  its own. Trigger a **new run** (always the current version), or pass
  `run_on_latest_version: true`. Clearing to re-run a task *as it was* — a
  week-old failure, a flake, an infra-failed attempt — now does that, which was
  impossible before.

  It is a flag on the clear request, not a DAG setting: nothing changes in
  `dag.py` or `leoflow.yaml`. Whether a re-run should reproduce the past or pick
  up a fix is a decision at the moment of clearing, not at authoring time.

  **The UI can now ask for it.** `bundle_version` was declared on the DAG and
  DAG-run payloads and never populated, and the embedded SPA renders the clear
  dialog's "Run with latest bundle version" checkbox only when the run's
  `bundle_version` differs from the DAG's and neither is null. So the checkbox
  never appeared, and the field the SPA always sent was hardcoded to its default.
  Both are populated now — the run reports the version it is pinned to, the DAG
  reports its current one — so the control appears exactly when there is a newer
  version to choose, in all three of the SPA's clear dialogs (run, task group,
  single task).

  This also splits two decisions that were one boolean — re-opening a run and
  choosing its version are independent, and `reset_dag_runs` now means only the
  first. ADR 0020 carries the reasoning, and withdraws an earlier claim that the
  unconditional re-bind "matches Airflow" — true of Airflow 2, not of 3.x.

  `dry_run`, `include_upstream`, `include_downstream`, `include_past` and
  `include_future` were implemented but missing from the published OpenAPI, so
  the generated Go client could not express them. All five are documented now.


- **`leoflow build` builds every project in a workspace (#1115).** `leoflow
  compile <dir> --image <ref> --build` has always built one; with several DAGs
  that meant running it once per directory with the right reference each time,
  by hand, which is where the reference goes wrong. Each project's image comes
  from its own `registry:` block — the same derivation `deploy` already uses — so
  nothing about naming is new.

  A project that declares no registry is **reported by name**, not skipped
  quietly: a command that covers less than the workspace without saying so leaves
  you believing images exist. A failure stops the run naming the project, because
  a half-built workspace where some images are new and some are stale is worse
  than a clear failure — afterwards the difference is invisible.

- **`include_paths` copies the files it says it copies (#1062).** It was
  declared, defaulted, documented and read by nothing: a project could set it
  and believe a helper module was shipping, while the generated Dockerfile
  `COPY`ed only the DAG source and any dbt group directories.

  It is **additive** — paths copied alongside the DAG source, not an allowlist
  that replaces it. An allowlist would need a stated precedence against
  `exclude_paths` and `dag_source`, and would silently shrink images for anyone
  who set the field expecting the documented "files copied into the image";
  adding is the reading that cannot break a build that works today. The default
  `["."]` means *no extra paths*: every existing project carries it, so it must
  not change what their images contain.

  An entry that is absolute, or that escapes the build context, is refused at
  compile with the entry named — Docker cannot `COPY` it, and failing at build
  time would name a Docker error instead. Included paths are scanned by the
  credential warning like everything else that ships.

  `build.context` is still unimplemented and still tracked: wiring it moves the
  `.dockerignore` to the context root AND re-roots every `COPY` in the generated
  Dockerfile, and getting that wrong produces an image with no DAG in it — a
  failure that only appears when a task runs.

- **`leoflow dev --fresh`, and a banner line for the state you inherited
  (#1104).** Lite keeps its state under `~/.leoflow/dev` and nothing reset it, so
  a DAG registered during a spike weeks ago stayed registered: it kept being
  scheduled, it kept failing, and it failed inside sessions that had nothing to
  do with it. `--fresh` drops the local database first, announcing what goes with
  it (registered DAGs, runs and history).

  The banner line is the half that fixes the surprise rather than the state. The
  leftovers were invisible until one of them broke, and nobody goes looking for a
  flag to remove something they do not know is there — so the count now arrives
  unasked, in the block that already carries the URL, the login and the project
  path. It says nothing when the slate is clean, and nothing when the count could
  not be read: asserting "0" on a lookup that never ran would be worse than
  silence.


### Fixed

- **A blank name in an OIDC map is a boot failure, not a silent denial.**
  `corp.example:` with nothing after it is valid YAML that binds to the empty
  string, and the boot checks only ever asked whether the map was non-empty. A
  blank tenant name resolves a login to a tenant that cannot exist; a blank role
  name is copied straight into the resolved role set and then fails the existence
  check. Both denied the login behind the same generic answer as everything else,
  while the operator looked at a key they had filled in.

  This one fails boot where its neighbors only warn, and the difference is that
  it cannot be a transient. The tenant and role existence checks ask a live
  database, where "absent" and "could not ask" are the same answer during a blip,
  so a hard gate there would turn a lagging replica into a restart loop. A blank
  name is a string in a file: never correct, identical on every boot, and no
  deployment can be working with one.

- **A denied login no longer reports an unknown tenant as a database failure.**
  The role lookup resolves the tenant first, so a tenant that does not exist
  comes back as an error exactly like a database that is down, and both were
  audited `role_check_failed`. The audit row could not tell a one-character typo
  in `tenant_claims` from an outage. The typo is now `unknown_tenant`, carrying
  its cause, and `role_check_failed` keeps the meaning it should have had.

- **A refused single sign-on no longer answers the browser with raw JSON.** Every
  fail-closed path wrote problem+json with 403. That is the right answer to an
  API client and the wrong one to a browser, and both OIDC routes are reached
  only by a top-level browser navigation: the user clicks the sign-in control, or
  the identity provider redirects them back. So a denied login rendered a page of
  JSON with no way back to the sign-in page, which is also where the retry lives.

  The browser is now returned to the login page, which says that sign-on did not
  complete and points at the person who can look up why. **The redirect carries
  no reason.** Withholding the cause from the browser is the posture of this
  whole path, since it is the same information someone probing the deployment is
  after; the cause is in the audit row and, since the previous release, in a WARN
  on the server log. The banner names an administrator rather than telling the
  locked-out reader to go read the audit log, which under `provider: oidc` with
  an empty `break_glass_emails` is behind the very session they do not have.

  **The three 500 paths on the same two routes got the same treatment**, with
  their own marker and their own words: generating the login tokens, sealing the
  state cookie, and minting the session. The last is the worst of them, being the
  tail of a completely successful round trip through the identity provider, and
  it too ended in raw JSON. "We refused you" and "we broke" send a user to
  different places, so the page says which happened: a refusal needs an
  administrator, a failure needs a retry.

  The tests changed shape with it. A denied login is a 302 now, so `302` alone no
  longer means a login succeeded, and every assertion that read it that way would
  have passed on a rejection. Success and denial are each asserted in one shared
  place, and those two assertions are themselves tested: denial requires that no
  session was minted, that the target is exactly the login page on this origin,
  and that neither the body nor any response header carries the cause; success
  requires a session cookie and a target that is not the bounce back to the login
  page. The pair is also pinned as mutually exclusive, so one response can never
  satisfy both.

- **Every rejected SSO login now says why, in the logs.** Each fail-closed path
  recorded an audit row, wrote a 403, and logged nothing. The audit row is the
  record of truth, but it lives in a Postgres table reachable only through an API
  that needs a working session, and with `auth.provider: oidc` and an empty
  `break_glass_emails` there is no session to be had. So the one channel an
  operator could actually read carried nothing: a deployment rejecting 100% of
  logins looked, in the logs, exactly like a deployment nobody was using.

  Denials are now logged at WARN with the audit reason, the action, and the
  tenant, user and email when known.

  **`token_invalid` carries the underlying error.** It is the catch-all arm, so a
  JWKS fetch failure, a network timeout to the IdP, an audience mismatch and a
  signature surprise all collapsed into it and the real error was discarded. The
  cause is attached there, and on `group_claim_overage`, whose text is the remedy.
  Everywhere else the reason names a fixed sentinel, so repeating it would only
  write the same words twice.

  **Three recognized failures stop reporting as `token_invalid`.** `verify.go`
  declares ten sentinels and only seven had a reason, so a token with no `exp`,
  a token with no `sub`, and an Entra group-claim overage were all audited as the
  reason that means "we did not recognize this". They are now
  `token_missing_expiry`, `token_no_subject` and `group_claim_overage`. The
  overage is the one that bites: past roughly 200 group memberships Entra omits
  the `groups` claim entirely, so the most heavily grouped users are the only
  ones who cannot log in.
- **The sign-in page offers single sign-on when it is configured (#1160).** The
  server has had complete OIDC since ADR 0057, and nothing linked to the route
  that starts it. A deployment with SSO correctly configured showed a username
  and password form; users typed their IdP credentials into it, got
  `Invalid credentials`, and produced no audit record at all, because no OIDC
  request was ever made. The only way in was typing
  `/api/v2/auth/oidc/login` into the address bar.

  Indistinguishable, from the outside, from a broken configuration: every
  diagnostic instinct then points at the IdP, the tenant pin or the redirect URL,
  none of which is the problem.

  The password form stays, deliberately, below the SSO control and marked as such.
  `auth.oidc.break_glass_emails` exists precisely so a named set of local logins
  still works when the IdP is down or the tenant mapping is wrong, and hiding the
  form would hide the escape hatch at the moment it is needed.
- **Three more SSO settings that boot green and deny everything are named at
  startup.** The first two are names that pass every configuration check and
  refer to rows that do not exist, so the server now looks them up once at boot,
  after the schema check and bounded like the rest of the boot path:

  - **a `tenant_claims` value naming a tenant that does not exist.** Nothing in
    Leoflow creates a tenant: `INSERT INTO tenants` appears once, in the first
    migration, creating `default`. No API, CLI, chart setting or later migration
    adds another, so any other name denies every login carrying that claim value
    and no supported action makes it start working. The chart's Google example
    suggested exactly this, mapping a second domain to a tenant named `acme`.
  - **a `default_role` or `role_mappings` value naming a role the tenant does not
    have.** The ladder (`viewer`, `editor`, `operator`, `admin`) is seeded for
    `default` only. This one matters because "set `default_role` to `viewer`" is
    the remedy the server's own warnings recommend, so it is the name most likely
    to be typed by hand.
  - **a `break_glass_emails` whose addresses cannot sign in.** This is the worse
    half of the lock-out problem, and it was silent: a non-empty allowlist looks
    safe and is not. The gate admits the address and the credential store then
    rejects it exactly like a wrong password, so the hatch does not open while
    the operator believes it will. The only local password user that ever exists
    is the bootstrap admin; every other one comes from the admin-authenticated
    user API, which is precisely what cannot be reached during a lock-out. A user
    provisioned through SSO does not count either: it has no password for
    anything to verify against. The check runs after the bootstrap admin is
    created, so a first install with `bootstrap.password` set and
    `admin@leoflow.local` on the allowlist, which is the chart's documented
    posture, says nothing: the account it asks about is the one that install just
    made.
  - **an empty `break_glass_emails` under `provider: oidc`.** Every password
    login is rejected, which is correct, and is also the state in which any of
    the failures above leaves nobody able to reach the control plane, including
    whoever has to fix it.

  A lookup that fails to run is reported as such rather than skipped, on both
  halves of the check: a check that could not ask and a check that found nothing
  must not produce the same silence. Each name is looked up once however many
  settings point at it, and the warning names every claim value or IdP group that
  named it, so one line points at every line that has to be edited. An empty
  value counts as a name that does not exist rather than as an absent setting:
  `corp.example:` with nothing after it is valid YAML, nothing rejects it, and it
  denies every login it touches. An unset `default_role` is still the supported
  strict default-deny and is not reported here.

- **Single sign-on now says at boot why it will not work.** Four OIDC
  configurations were accepted at boot and failed at the last step of a login,
  where the callback answers a deliberately generic 403. An operator saw a
  deployment that boots green, an IdP redirect that works, a token that verifies,
  and a refusal that names nothing. Each now logs a WARN at startup naming the
  setting and what it does:

  - **`jit_provisioning: false` denies every first login.** A login matches a user
    only by `(oidc_provider, oidc_subject)`, and just-in-time provisioning is the
    only code path that writes those columns. No API, CLI or migration can
    pre-create an OIDC identity, so the documented alternative ("a pre-provisioned
    user is required") never existed. The chart already defaults
    `auth.oidc.jitProvisioning` to `true`; this covers the deployments that set it
    from elsewhere or turned it off. ADR 0057 carries an amendment on D4.
  - **`role_mappings` set with `default_role` empty clears grants.** Roles are
    reconciled to exactly the resolved set, so a login whose group claim matches
    nothing resolves to zero roles and strips the user's existing grants. Google
    Workspace emits no `groups` claim at all unless Directory API group sync is
    configured, so on that IdP every login takes this path. The warning fired
    before only when both keys were empty, which is the shape a Google deployment
    is least likely to be in. Boot cannot tell that from a deliberately strict
    deployment on an IdP that does emit the claim, so the warning names both and
    can be dismissed on purpose: `default_role` is not free, it grants that role
    to every login the tenant pin admits.
  - **An empty `client_secret` breaks the code exchange.** Google Workspace and
    Entra always register a server-side application as confidential, and a
    confidential client rejects an exchange with no secret. It stays optional at
    boot because a public client is a valid registration.
  - **An IdP that never answers parked boot forever.** Discovery runs before the
    HTTP listener binds, on a client with no timeout, so a request that waits
    instead of failing meant the probe endpoint never came up and the kubelet
    restarted the pod on a probe failure that named nothing. It is bounded at 15s,
    and the failure names the issuer, the bound, and the causes that wait rather
    than refuse: a NetworkPolicy or firewall that DROPs egress instead of
    REJECTing it, a name that never resolves, an egress proxy or intercepting TLS
    middlebox, or an issuer that accepts the connection and goes silent. The
    underlying transport error is wrapped, not replaced, so the detail that
    separates them survives. The startup-probe budget gate counts the new bound.
- **Google Workspace sign-in no longer offers accounts it will then reject.**
  Google's account chooser lists every account signed in on the browser, work and
  personal. A personal one produces a token with no `hd` claim, the tenant pin
  refuses it, and the user gets the same generic 403 as every other failure with
  nothing saying which account to use. The chooser then looks identical on the
  retry.

  With `tenant_claim: hd` and exactly one accepted domain, the authorization
  redirect now carries Google's `hd` parameter and the chooser is narrowed to
  that domain. With several accepted domains nothing is sent, because picking one
  would lock out the users of the others.

  It is a hint and not a boundary. Google's own documentation says to verify the
  `hd` **claim** on the returned token, which is what the tenant pin already
  does, and this must never be read as a reason to relax that check.

  **How this was verified, and how it was not.** The parameter's construction is
  unit-tested, including the two cases where it is deliberately not sent, and the
  authorization request's shape is checked on every push by a browser test
  against a fake identity provider. It has **not** been exercised against a real
  Google Workspace tenant: no cut so far had one to point at
  ([#1177](https://github.com/neochaotic/leoflow/issues/1177)). If it is wrong,
  the symptom is the account chooser still offering accounts the tenant pin then
  rejects, which is the problem this set out to remove rather than a new one, and
  it cannot weaken the pin, which is the verified claim on the returned token.

- **OpenMetadata can catalogue leoflow again: `class_ref.module_path` is no
  longer null.** A field team reported that they could not integrate leoflow with
  OpenMetadata and could not say why. The cause was one field. `classRefDTO`
  declared `ModulePath *string` and never assigned it, so `GET /api/v2/dags/{id}/tasks`
  emitted `"class_ref": {"module_path": null, ...}`. OpenMetadata types that pair
  as a string map, pydantic does not coerce null into a string, and the model
  raises. OM catches that per DAG and files it as a failed record, so **no**
  pipeline was ingested. Not a partial result, zero.

  What made it hard to see from outside: OM's Test Connection probe checks
  reachability and deliberately accepts any body from that endpoint, so the
  wizard reported success while ingestion produced nothing.

  Airflow itself populates `module_path`, so this was a compatibility gap as well
  as an OpenMetadata one. `GET /api/v2/dags` also now carries `fileloc`, the field
  Airflow declares as a required string on that response and the only place a
  consumer of the list route can learn where a DAG is defined; OpenMetadata reads
  its `pipelineLocation` from there and never calls the details endpoint.

  There is a new `operate/openmetadata` page with the configuration, including
  the detail that `hostPort` must be the bare root (OM appends `api` and the
  version itself, so a trailing `/api` produces `/api/api/...` and 404s
  everywhere), and an explicit statement of what this path does not give you:
  **no table lineage**, by construction on OM's side.

  CI gains a contract job that captures the six responses OM reads (the token
  exchange, the version probe, the DAG list, one DAG's tasks, its runs and one
  run's task instances) and validates them against OpenMetadata's own pydantic
  models, vendored verbatim from the 2.0.1 release and checksum-pinned so an
  edited copy fails the job. Two layers, because a Go test cannot prove that
  pydantic accepts a body, and this defect was valid JSON that was valid against
  our own OpenAPI spec.
- **The control plane's credentials are an ordinary resource, not a Helm hook
  (#1142).** Reported from production: both replicas sat in
  `CreateContainerConfigError` for 4h47m with `secret "leoflow-secrets" not
  found`, while Argo CD reported the Application `Synced` the whole time and
  nobody was paged.

  The Secret the Deployment reads was rendered as a `pre-install,pre-upgrade`
  hook. Under Argo CD a `before-hook-creation` hook is deleted and recreated in
  **separate passes of one operation**, hooks are excluded from the compared
  state, and self-heal reconciles only tracked resources. So a sync interrupted
  between those two passes removes the Secret permanently, with nothing OutOfSync
  to show for it. The reporter's audit log shows exactly that: four clean
  delete/create cycles, then a delete with no create, then 30 hours until a node
  replacement restarted the pods and the absence finally surfaced.

  Nothing paged because nothing could: Argo reports a Deployment `Degraded` only
  on `ProgressDeadlineExceeded`, and Kubernetes never sets that when the rollout
  was already complete, which is the case when pods die from node replacement
  rather than a spec change.

  The Deployment now reads `<release>-credentials`, a normal tracked resource, so
  deleting it makes the Application OutOfSync and self-heal restores it. The hook
  Secret remains for the migration Job, which runs before the normal manifest is
  applied, reduced to the one key that Job reads and deleted when the hook
  succeeds.

  **A new name, deliberately.** Helm stamps ownership metadata only on
  main-manifest resources, never in `execHook`. Measured against the released
  0.4.6 chart on a live cluster, the live Secret carries
  `app.kubernetes.io/managed-by: Helm` and no `meta.helm.sh/release-name`, so
  reusing that name in the manifest would abort every `helm upgrade` with an
  ownership conflict. That would have fixed the GitOps path by breaking the
  primary documented one.

  **What to change on upgrade.** Nothing in the default shape: the hook copy is
  rendered again under the old name, feeds the migration Job and is removed when
  the hook succeeds, taking the pre-0.4.7 Secret with it. Helm deletes succeeded
  hooks at the end of the hook phase and Argo CD at the end of the sync
  operation, so in both cases after the Job has run.

  Two shapes render no hook copy at all and therefore leave the old Secret
  behind, still holding every credential it had: `migrations.enabled=false`, and
  a `database.existingSecret` that supplies the DSN. Nothing will ever collect
  it, for the same reason the new name was needed: Helm never tracked it (it came
  from `execHook`) and Argo CD excludes hooks from the compared state. Remove it
  by hand once the upgrade is through, with
  `kubectl -n <namespace> delete secret <release>-secrets`.

  An operator with a runbook that reads `<release>-secrets` for the control
  plane's credentials should point it at `<release>-credentials`. CI now installs
  the last released chart and upgrades it to HEAD on every run, then asserts the
  durable Secret is tracked, the Deployment reads it and the hook copy is gone,
  which is the leg that would have caught the adoption break.


- **The startup gate no longer nil-pointers on a value tree that predates it.**
  `helm upgrade --reuse-values` does not merge the new chart's defaults: helm
  rebuilds the previous release's coalesced values and hands them to the new
  templates. `probes.startup` is the only key this chart gained, so an operator
  upgrading a 0.4.6 release with that flag got

  ```
  nil pointer evaluating interface {}.enabled
  ```

  and no render at all. Nothing caught it because every test and every CI install
  starts from the chart's own defaults, where the key is always present; the
  cross-version upgrade leg added alongside it is what surfaced the class.

  An absent `probes.startup` is now read as "not configured", which renders the
  same output the 0.4.6 chart did. Note what that means: a `--reuse-values`
  upgrade lands WITHOUT the gate, because the flag never sees the default that
  turns it on.

- **`--reset-then-reuse-values` is now the documented upgrade flag, and
  `--reuse-values` from 0.4.6 still will not render.** Fixing the nil pointer
  above does not make that flag work, because the same rebuilt-old-tree also
  carries 0.4.6's documented `config.cors.allowedOrigins: ["*"]`, which this
  version refuses at render time. Every `--reuse-values` upgrade from 0.4.6 hits
  that refusal, including operators who never set the key, since it was the old
  chart's own default. The refusal stands, because the alternative is rendering a
  wildcard nobody chose, but its message now names the flag and the fix.

  `--reset-then-reuse-values` starts from this chart's defaults, lays the previous
  release's user-supplied values over them, and then applies `--set`/`-f`. It
  renders, it preserves what you configured, and it delivers new defaults such as
  the startup gate. `website/content/operate/upgrades.md` documents it as the Pro
  upgrade path.

- **BREAKING: an unflagged `clearTaskInstances` now previews, and is scoped to
  failures (#1137).** Apache Airflow 3.2.1 declares `dry_run: bool = True` and
  `only_failed: bool = True` on `ClearTaskInstancesBody`. leoflow defaulted both
  the other way, so this request:

  ```
  POST /api/v2/dags/{id}/clearTaskInstances -d '{"dag_run_id":"r","task_ids":["load"]}'
  ```

  previewed on Airflow and **executed** here, over every task instance named by
  the request, including ones that had succeeded. With no `task_ids` the same
  unflagged request cleared no task instance but still re-opened the run, putting
  a finished run back to `queued` and discarding its alert bookkeeping. Anything
  written against the Airflow API, which is leoflow's declared compatibility
  target, sends exactly that request. The divergence was known and recorded in
  the OpenAPI description ("NOTE: Apache Airflow's equivalent defaults to true")
  rather than reconciled.

  A missing `dry_run` now previews and a missing `only_failed` now restricts the
  clear to failed task instances. Both explicit values still win, so
  `"dry_run": false` executes and `"only_failed": false` widens exactly as before.

  **`only_running` is now refused with 400 instead of being ignored.** It was
  declared in the request and in the spec and read by no code. That was harmless
  while `only_failed` defaulted to false, and stops being harmless now: Airflow
  rejects both flags being true and defaults `only_failed` to true, so
  `{"only_failed": false, "only_running": true}` is the only way to ask Airflow
  for the narrowest possible clear. Read here with `only_running` discarded, that
  same body meant the widest one. leoflow has no path that clears a running task
  instance, so it says so.

  **What to change:** an API client that relied on an unflagged call executing
  must send `"dry_run": false`, and one that relied on clearing successful task
  instances must send `"only_failed": false`. The embedded UI is unaffected: all
  three of its clear dialogs already send both fields explicitly. The OpenAPI
  spec and the generated `pkg/client` carry the new defaults.
- **A slow boot is no longer a restart loop: the control plane gets a
  `startupProbe`.** Liveness and readiness both target the API listener, which
  binds at the END of boot, so the kubelet's answer to a boot that was slow or
  stuck was to kill and restart the container. That is what turned #1083 into a
  loop whose every cycle was recorded as `Completed exit=0`, and it would do the
  same for any future boot-path dependency.

  Kubernetes runs neither liveness nor readiness until a startup probe succeeds,
  so that state now reads as "not ready yet" rather than "unhealthy". The budget
  is `periodSeconds * failureThreshold` = 180s, sized from the server's own boot
  bounds rather than picked: the Postgres connect retry is 30s and runs twice
  (request pool + probe pool), the pod-informer warm-up is bounded at 10s
  (#1141), and cloud credential detection and OIDC discovery sit on top of those
  with no bound of their own. A boot that overruns even that still restarts,
  which is the honest semantic, and a boot that outright fails still exits 1 and
  CrashLoopBackOffs as before, untouched by this.

  The gate probes `/healthz`, never `/readyz`: a failing startup probe restarts
  the container, so gating it on dependency health would turn a database outage
  into a crash loop. Readiness still gates endpoint membership on `/readyz` the
  moment the gate opens.

  The render refuses a startup budget at or below the liveness budget it replaces
  (10 + 3*20 = 70s), because such a gate only moves the kill from one probe to
  the other and leaves the loop where it was. Tunable under `probes.startup`, and
  `probes.startup.enabled: false` hands the boot back to liveness.

- **`config.cors.allowedOrigins` is read by the chart at all (#1144).** The key
  shipped in `values.yaml`, documented as the API's CORS policy with a default of
  `["*"]`, and no template ever referenced it. Anything set there was silently
  ignored, and the server ran its own default of `["http://localhost:8080"]`, so
  the chart documented a security-relevant default that was wrong in both
  directions.

  It now renders `LEOFLOW_SERVER_CORS_ALLOWED_ORIGINS`, comma-joined, the same
  way `config.trustedProxies` does.

  **The default is now `[]`, not `["*"]`.** Restoring the documented default
  would have widened CORS to every origin on the next `helm upgrade` for every
  existing deployment, silently. Empty renders nothing and leaves behaviour
  exactly as it is today. If you had set this key expecting it to work, it now
  does, so check the value you have.

  **`"*"` is now rejected at render time.** An install that started from
  `helm show values` carries the old documented `["*"]` verbatim in its own
  values file while the server has actually been running `http://localhost:8080`,
  so simply honouring the key would still widen that deployment to every origin
  on upgrade with nothing in its values changing to show for it. The chart fails
  the render instead, which is the one channel a GitOps sync surfaces. Set
  `LEOFLOW_SERVER_CORS_ALLOWED_ORIGINS` through `extraEnv` if you want the
  wildcard deliberately. The reference documentation claimed list-valued keys
  could not be set from a single env var, which was the belief that produced this
  bug; it now states the comma-separated form that viper actually supports.

- **A control plane that cannot watch pods now boots, says why, and serves
  `/readyz` (#1083).** The pod informer's cache warm-up sat on the boot path
  holding the process context, and `WaitForCacheSync` returns only on sync or on
  that context being canceled, which happens at shutdown. So a cache that could
  not sync held boot ahead of the HTTP and metrics listeners.

  On a cluster that is worse than a hang. gRPC serves, `8080` and `9090` never
  bind, and liveness points at `8080`, so the kubelet kills the container about
  every 70 seconds, it restarts, blocks again, and each cycle is recorded as
  `Completed exit=0`, because SIGTERM is handled cleanly. A dependency failure
  that reports itself as a success, while `/readyz` answers connection refused
  instead of naming the problem. The usual trigger is a ServiceAccount without
  `list`/`watch` on pods in the task namespace, or an unreachable API server.

  The warm cache was always an optimization: `CachedPodActive` gates on
  `HasSynced` and every consumer falls back to a live read, and `HasSynced` stays
  live so a cache that warms later (after an RBAC fix) is picked up. That
  fallback was correct and unreachable, because the process could not finish
  booting to use it. Boot now waits a bounded 10s and continues, and the timeout
  line names the namespace, the budget, the likely cause and what it means for
  the reapers.
- **An OIDC deployment that cannot complete a login now fails boot by name
  (#1143).** `Verify` resolves a tenant on every login and fails closed when
  `auth.oidc.tenant_claim` is unset or the claim value is not in
  `auth.oidc.tenant_claims`. That is correct, but the rejection reached the user
  as a generic 403 and its cause reached only the audit log, which in an
  SSO-only deployment nobody can log in to read. The boot check already existed
  to prevent exactly this ("fails boot with an actionable message rather than
  starting a login flow that cannot complete") and checked issuer, client_id and
  redirect_url, leaving out the two settings that decide whether a login can
  succeed at all.

  Both are now required when `auth.provider: oidc`. Every missing OIDC key is
  reported in ONE error, because each boot failure on Kubernetes costs a values
  edit, an upgrade and a rollout to learn the next one. When the tenant pin is
  what is missing, the error also says where the map can come from: viper cannot
  bind a map from an environment variable, so `auth.oidc.tenant_claims` loads
  only from the YAML config file named by `LEOFLOW_CONFIG`, and an error that
  named the key alone would send an env-only deployment to set a variable that
  does nothing. It also names the way back (`auth.provider: jwt`) for an operator
  who needs password login while SSO is being configured.

  Upgrade impact: a deployment already on `auth.provider: oidc` without the pin
  is rejecting 100% of single sign-on today, so no working login path breaks. The
  one shape that changes is an install that leans on
  `auth.oidc.break_glass_emails` alone, where password login for those addresses
  works while SSO does not: it now fails boot until the pin is set or the
  provider goes back to `jwt`, both of which the error names.

- **An OIDC deployment with no source of roles says so at boot (#1143).** Roles
  are IdP-authoritative: each login computes its roles from
  `auth.oidc.role_mappings`, falls back to `auth.oidc.default_role`, and
  reconciles the user's grants to EXACTLY that set. With neither configured the
  set is always empty and an empty reconcile is a full clear, so a
  pre-provisioned admin's first single sign-on strips the grants they were
  provisioned with. The login succeeds, so nothing can fail closed on it; the
  WARN at boot (on every process serving the API, including an api-only replica)
  is the only signal before it happens. It was unreachable while the tenant pin
  was unsatisfiable, because the login was rejected before reconciliation ran.

  This is not a typo an operator talked themselves into: the chart exposes no
  `auth.oidc.*` values, mounts no server config file and has no `extraVolumes`,
  so a deployment that reaches OIDC through `extraEnv` cannot satisfy the pin by
  any route the chart offers. Modelling `auth.oidc` in the chart (with a rendered
  config ConfigMap for the two maps, and a render-time refusal when the pin is
  empty) is the rest of #1143.

- **A dbt folder that cannot be a task id is refused by name (#1114).** With
  `granularity: folder` the folder name becomes the task id verbatim, so a
  folder like `my folder!` produced an unusable id. It was already caught — but
  at the end of the compile, as `schema validation: jsonschema validation
  failed with 'file:///…/dag.json#'`, leaving the author to work out that a dbt
  folder name was the cause. The compiler knows the folder and the rule, and now
  says both, along with the models the group holds and the two ways out.

- **`granularity: folder` says when two unrelated groups merged into one task
  (#1114).** The group key is the first folder segment, with a fallback to the
  resource-type plural for a model with no folder — so a project with a folder
  literally named `models` **and** a model at the root of `models/` keyed both on
  `models`, and the two sets became a single task with their dependencies
  combined. Nothing said so: the author saw one task named after a folder, with
  no sign that a root-level model had been folded into it.

  The merge is not refused, and the reason is worth knowing: dbt still orders the
  nodes inside the combined task, so the data is not wrong — what is lost is
  per-node failure isolation and Leoflow-level parallelism. Refusing would break
  a project that runs today. The compile now warns on stderr, naming the group,
  every member, and the two ways out (rename the folder, or `granularity: node`) —
  on the dbt-only path and on an embedded `dbt_group` alike, the latter prefixed
  with the group name since a `dag.py` DAG can embed several.

- **The migration Job honours `nodeSelector`, `tolerations` and `affinity`
  (#1056).** It was the one workload in the chart ignoring the pod-placement
  values every other workload respects, and it is the workload that runs
  **first**. On a cluster with a tainted or dedicated node pool — where the
  operator set `tolerations` precisely so Leoflow lands there — the pre-install
  hook was unschedulable: the pod sat `Pending`, Helm waited out the hook
  timeout, and `helm install` failed with a message that never mentioned
  scheduling.

  The values are inherited from the top level rather than given their own
  `migrations.*` knobs. Someone who set `tolerations` said "Leoflow runs on this
  pool", and the migration Job is Leoflow; a Job-specific pool is surface nobody
  has asked for. Nothing is rendered when nothing is set, so an existing release
  sees no diff.

- **`exclude_paths` says when a `!` entry was ignored (#1081).** A negation
  contributed nothing to the generated `.dockerignore` and nothing said so, which
  made `exclude_paths` a field that accepts input and discards it. The drop
  stays — order decides a `.dockerignore`, and leoflow's block is appended
  **after** your own lines, so a `!` emitted there could resurrect a path one of
  your earlier lines excluded. The build now names the ignored entries and points
  at your own `.dockerignore`, which the merge only ever appends to. A `#` entry
  is a comment rather than a request that was refused, and is not reported.

  The function's comment claimed the opposite of what it did ("passed through
  untouched"), which is how the false statement reached the configuration
  reference before the audit caught it.

- **A dbt-only DAG keeps the `connections:` and `variables:` it declares
  (#997).** `dbt.Compile` built its spec from an explicit field list that omitted
  both, so a `leoflow.yaml` declaring them produced a `dag.json` without them —
  while the `dag.py` path emitted both from the same file. Under ADR 0055 secret
  scoping that is not a missing JSON field: what a task pod may see is derived
  from what the DAG declares. Under `auth.secret_scoping: enforce` the pod received
  **nothing** and the DAG failed inside the task rather than at compile. Under the
  default permissive scoping it still received the whole tenant vault, so the
  declarations were simply not honored — except for secrets that live only in an
  external backend, which are requested by declared name and so were never fetched.

- **A dbt task with a managed `dbt.connection` also gets the connections its DAG
  declares (#997).** Rendering stamped the managed connection onto every task, and
  a non-empty task-level list is what reaches the pod — the DAG-level list is never
  consulted once it exists. So a dbt project using `dbt.connection` silently lost
  every other connection it declared (a pre-hook's, say). The two are now merged.

- **`leoflow lite --help` now says the admin password is shown once, and names
  the way back (#1105).** It described the UI as being "behind a login (the admin
  created by `leoflow setup`)" and stopped there. The password is printed once
  during setup and never again; `leoflow lite reset-password` sets a new one, and
  it was already listed as a subcommand in the same help output — just never
  connected to the sentence about the login. Present and unlinked is the same as
  absent for someone stuck on a login screen.

- **The wrong-server 401 hint names a command that exists.** The hint added
  below printed `leoflow login --server <url>`. There is no `leoflow login` —
  login lives under `auth` — so the one-line way out of the 401 could not be
  pasted. The test asserted the same wrong literal, which is why nothing caught
  it: comparing one author's string to the same author's string can only show
  they typed it twice. The hint now resolves against the real command tree in a
  test, so moving or renaming `auth login` fails there instead of in a terminal.

- **A 401 now says when your saved token belongs to a different server
  (#1102).** `~/.leoflow/config.yaml` holds a `server_url` and a `token`, written
  together; point a command at another control plane with `--server` and the
  saved token went along, got rejected, and you were told only "server returned
  401". Nothing distinguished an expired token from a token for somewhere else,
  and the way out — logging in to the server you were actually calling — was the
  one thing the message did not say.

  The CLI already knew: the two keys are written in the same call, so the pairing
  was on disk the whole time. A 401 now names both servers and prints the login
  command for the one being called. It stays quiet when nothing is wrong — the
  same server, a `--token` or `LEOFLOW_TOKEN` you supplied yourself, or no
  configured server to compare against — because a hint that fires when there is
  no mismatch sends you chasing something that is not happening.

  This reaches every command that talks to a control plane, not just the one
  where it was noticed: `push`, `deploy`, `dags`, `runs`, `connections`,
  `variables` and the admin commands all formatted that error with their own copy
  of the same line, and now share one. Holding more than one server's token at a
  time is the structural answer and is still open.

- **Lite seeds a declared connection your environment already carries (#1103).**
  Registration is fail-closed against the vault: a DAG declaring a connection the
  vault does not hold is refused, with a message naming the fix. In Lite that
  meant declaring the same value twice — once as `AIRFLOW_CONN_<ID>` for the task
  (the subprocess executor inherits the environment, which is how tasks read
  connections) and once in the vault so registration would pass. The first
  `leoflow dev` of a project now works without the second declaration.

  Four constraints, because taking a secret from ambient environment is only
  defensible under them: only connections the DAG **declares** are considered, so
  unrelated `AIRFLOW_CONN_*` exports are never copied out of your shell; a
  connection the vault already holds is **never** replaced, so a stale export
  cannot shadow a value you set deliberately; a URI that does not parse is
  reported by name and not stored, because a broken connection that exists is
  worse than one that is missing; and every seeded connection is announced by
  name — never by value — so a secret store never fills itself silently.

  If the vault cannot be read at all, nothing is seeded: "already set" is then
  unknown, and the one thing this must never do is overwrite. Seeding is best
  effort throughout and never fails a reload.

- **A database outage is reported as an outage, not as the caller's fault
  (#1087, #1071).** Two surfaces answered a dependency failure with a statement
  about the client, and both were wrong in the same way.

  **Any authenticated request during a database outage returned
  `401 invalid token`**. `Authenticate` joined
  `ErrInvalidToken` onto every store failure, so a dead database and a forged
  token were the same value, and the middleware — which inspected only
  `err == nil` — answered 401. Measured against a real stopped Postgres: 401
  after **76 seconds**, on a token minted seconds earlier. Signature
  verification is local and takes about a millisecond, so the duration alone
  says it was never about the token. It is now **503 "authentication temporarily
  unavailable"**, the same answer the login path has given for the same cause
  since #843, with the driver detail kept in the log and out of the body. A
  token that was actually judged and rejected is still 401.

  That mattered beyond the status code: a well-behaved client reads 401 as
  "re-authenticate" and retries against `/auth/token`, which needs the same
  database — turning a read outage into a retry storm against the dependency
  that is already down. And a wall of 401s reads to whoever is on call as an
  authentication incident, sending them to rotate credentials during a database
  outage.

  Separately, **an unreachable database was reported as `499 client closed
  request`**. `handleRepoError` routed on `errors.Is(err, context.DeadlineExceeded)`,
  and a `pgconn` connect timeout satisfies that while the caller is still
  perfectly connected. So the tenant was told they had hung up, the request
  logged at WARN because 499 is below 500, and **a 5xx-rate alert missed a
  database outage entirely** — the one incident it exists to catch. The 499
  branch now asks the request's own context, which is done for a caller who
  really went away and healthy for one whose database died. The branch itself
  stays: the UI supersedes in-flight grid requests constantly, and mapping those
  to 500 is the regression it was added for.

  `POST /api/v2/auth/token/renew` carried the same conflation and is fixed with
  it. Renewal re-proves the principal against the user store, so it fails for
  the same two unrelated reasons — and both mapped to `401 token cannot be
  renewed; log in again`, with no cause recorded at all, so an outage was
  invisible on that route. A store failure there is now 503 as well, and the
  driver detail reaches the log. A token that was judged and rejected, and a
  session past `max_lifetime`, are still 401. Renewal still fails closed either
  way: any error refuses the re-mint.

- **`leoflow dev` builds each venv on the interpreter the project declares, not
  always on the managed CPython 3.11 (#1092).** `python_version` selects the task
  base image, and since #1031 that image really can be 3.13 — but the dev venv
  was created from `~/.leoflow/python/bin/python3.11` regardless, because the
  managed build was trusted by **path** rather than by the version it reports. A
  project pinned to 3.13 therefore ran on 3.13 in the cluster and 3.11 locally,
  and the dev loop's whole promise — that passing here means passing there —
  quietly stopped holding. 3.12 removed `distutils` and `type X[T]` is a
  `SyntaxError` on 3.11, so the gap is reachable with ordinary code.

  The declared version is now resolved by the minor an interpreter **reports**:
  the managed build when it matches, then `python3.<minor>`, then the shared
  candidate list, then bare `python3`. Nothing is substituted silently — when no
  interpreter reports the requested minor the command stops and names both what
  was asked for and what was found, because "install 3.13" is the only useful
  next step and only the message can say it.

  A venv already built on a different minor is discarded and rebuilt, with a line
  saying so. Without that the fix would reach only venvs that never existed:
  provisioning short-circuits on the interpreter file being present, so editing
  `python_version` on a project you had already run would have changed the image
  and nothing else. A venv whose `pyvenv.cfg` cannot be read is left alone rather
  than rebuilt on every boot.

  Resolution under a declared version probes the same candidate list `leoflow
  setup` and `leoflow doctor` report on, via a new `setup.PythonCandidates`
  accessor. The lists had diverged: doctor could name a `python3.13` that dev
  never probed for by name, so the two answered "which Python will be used?"
  differently. `leoflow validate`'s syntax check still resolves the older
  managed-first/`python3` precedence and does not yet honour `python_version` —
  tracked separately.

## [0.4.6] - 2026-09-11

### Added

- **A `py3.13` task base image, and one list that decides which ones exist
  (#1031).** `ghcr.io/neochaotic/leoflow-runtime:py3.13` is now published
  alongside `py3.10`/`py3.11`/`py3.12` — multi-arch and cosign-signed like its
  siblings — so `python_version: "3.13"` works in `leoflow.yaml`. **3.13 is the
  ceiling, and it is set by the dbt adapters, not by Airflow:** `dbt-core` and
  `dbt-postgres` publish for 3.14, but `dbt-snowflake`, `dbt-bigquery`,
  `dbt-databricks` and `dbt-duckdb` all stop at 3.13. A `py3.14` base would give
  you an image where `dbt-postgres` installs and `dbt-snowflake` does not,
  discovered inside your build rather than ours, so it is not published.

  The set of published lines was stated in five places and only one of them was
  enforced, so they had already drifted in both directions — the release
  published a `py3.10` the CLI's host detector would never select, and the
  detector probed for a 3.13 nobody published. There is now one list: the
  `python_version` enum in the authoring schema, which is what
  `leoflow compile` already validates against. `scripts/check-python-runtime-matrix.sh`
  fails CI when the release matrix, `make runtime-images`, the
  `runtime/Dockerfile` comment, the runtime helper's `requires-python` floor or
  this reference disagree with it. Both directions of that drift used to surface
  in *your* build — a selectable version with no published leg is a
  `docker pull` 404 minutes into a compile, naming an image you never typed.

  The gate reconciles the deprecation *status and dates* as well as the list, so
  the support table in the configuration reference — the only place you can read
  when a line stops being published — cannot say one thing while the CLI warns
  another.

- **A weekly Python end-of-life watch.** Every other supply-chain signal in the
  repo (Dependabot, Trivy, govulncheck) reacts to a CVE that already exists. A
  base image whose Python line has gone EOL produces the opposite signal: it
  stops changing, because `docker-library/python` stops rebuilding an EOL line
  the day after, while the CVEs underneath it keep accumulating. The scheduled
  security workflow now asks endoflife.date instead, and opens a tracking issue
  when a line we still call supported comes within 270 days of EOL, or when a
  line we already deprecated is still being published past its own removal date.
  It never fails a build on a date.

- **Container image vulnerability scanning, with a policy designed to stay
  switched on.** `govulncheck` reads our Go module graph and says nothing about
  the Debian packages inside `python:3.x-slim`, or about a third-party
  Go binary baked into someone else's image — so OS-level and vendored-binary
  CVEs in the images we publish reached a human before they reached CI. Trivy
  now scans all three (`leoflow-server`, `leoflow-runtime` on every published
  Python line, `leoflow-migrate`), publishing to the Security tab under one
  code-scanning category per image.

  The policy is split by **who can fix the finding**, because a naive
  "fail on any HIGH" is unsatisfiable on a Debian base and gets disabled within
  a week. `leoflow-server` is distroless plus a binary built from our own
  `go.mod`, so every finding there is caused by a commit and closed by a
  `go get`: it is scanned on **every PR and push, and the job goes red**
  (baseline today: zero findings, so it starts green). Whether that red *blocks
  a merge* is a branch-protection setting — a new job is not a required check
  until somebody adds it. `leoflow-runtime` inherits a Debian package set we do
  not author, where a CVE lands because a distro security team published an
  advisory and not because anyone pushed anything — blocking it would fail
  whoever opens the next unrelated PR while the person who can fix it is
  elsewhere. It is scanned **daily, never blocks, and maintains a
  single self-closing tracking issue** whose body is refreshed each run, which
  comments only when the finding set actually changes, which reopens rather than
  duplicates when findings return, and which leaves the issue alone once a human
  has reopened it.

  `leoflow-migrate` started in that lane and moved to the blocking one in this
  same release: rebuilding it from our own `go.mod` onto distroless static
  (below) changed what the image is, so the criterion that put it in the
  report-only lane stopped describing it.

  The server gate scans **the artifact GoReleaser publishes** — the release
  Dockerfile, with a binary built by the release toolchain — not the
  build-from-source Dockerfile used for local and kind runs. Trivy keys `stdlib`
  findings to the toolchain recorded in the binary, so the two are different
  scans; and the from-source path builds on a floating `golang:1.x` tag, where a
  stdlib CVE is closed by a base rebuild rather than by a `go get`, which is
  precisely the unfairness this policy refuses to inflict on runtime and
  migrate.

  Both scans gate on **fixability first, severity second**: severity says how
  bad a finding is, fixability says whether anyone can act on it today, and only
  the second works as a gate criterion. On `leoflow-runtime:py3.11` that takes
  273 findings down to 14, removing all five CRITICALs — every one of which is
  `will_not_fix`, `fix_deferred`, or `affected` upstream in bookworm. Unfixed
  findings are kept out of the Security tab too: an alert nobody can ever close
  is not visibility, it is noise that teaches people to stop opening the tab.
  The full unfiltered inventory is retained as a build artifact.

  Accepted findings live in `.trivyignore.yaml` and cannot silently become
  permanent. Each entry must carry a `statement` saying why and an `expired_at`
  saying when the acceptance lapses; Trivy enforces the date itself, so neglect
  makes a finding *come back* rather than stay hidden. A new CI gate
  (`scripts/check-trivyignore-entries.sh`, with a `--self-test`) rejects a
  missing or placeholder rationale, a missing, lapsed, or beyond-180-day expiry,
  and a section name Trivy would silently ignore — and checks the other end of
  the chain too, **per scan**: every step that runs trivy must pass the file, so
  a flag dropped from one workflow cannot hide behind another workflow that
  still has it. A scan that must apply no suppressions declares itself with a
  reason the gate length-checks.
  `scripts/check-script-selftests.sh` now discovers Python self-tests alongside
  shell ones, so `scripts/trivy-report.py` is covered by the same gate.

  **No base image changed in this work** — it is detection only; which base
  `leoflow-runtime` ships is an authoring-surface decision users inherit through
  `base_image` and is argued separately. Findings, filters and triage steps are
  documented in [Image
  scanning](https://leoflow.dev/contribute/image-vulnerability-scanning/).

### Changed
- **`networkPolicy.enabled` now renders an ingress rule for the metrics port
  (9090), and its default allows any namespace.** **Existing installs get this
  on the next `helm upgrade` with no values change.** The port serves
  unauthenticated `/metrics`, `/healthz` and `/readyz`, and the metrics series
  carry `dag_id` and `task_id` — so this is not equivalent to the mostly
  JWT-gated API on 8080. **Narrow it**: set `networkPolicy.metricsFrom` to your
  Prometheus namespace, or to an explicitly empty list to keep the port closed.

  The previous default was an empty list, which rendered no rule at all — and
  an Ingress-typed policy denies what it does not match, so the port was
  reachable from nowhere while the value's own documentation said it was
  "reachable from wherever `ingressFrom` allows". `networkPolicy.enabled` plus
  `metrics.serviceMonitor.enabled` was a scrape target that was created, never
  answered, and failed nothing at install
  ([#1067](https://github.com/neochaotic/leoflow/issues/1067)). The default is
  now a real value rather than a magic empty, because a magic empty whose
  meaning lives only in a comment is how this happened.
- The chart refuses to render `probes.readiness.timeoutSeconds` below 3. The
  server gives up at 2s so it answers before the kubelet does; a shorter kubelet
  timeout silently inverted that. Lower `periodSeconds` or `failureThreshold` to
  react faster ([#1041](https://github.com/neochaotic/leoflow/issues/1041)).

- **`leoflow-migrate` is now built from our own `go.mod` instead of `FROM
  migrate/migrate`, taking it from 50 fixable CRITICAL/HIGH findings to zero
  (#1039).** The migration image shipped a third-party compiled binary, and
  that binary ran in the Helm pre-install/pre-upgrade hook Job — the first
  thing to touch a fresh cluster, holding the database DSN, before the control
  plane starts. `govulncheck` could not see it. The distinction is narrow and
  worth stating exactly: `golang-migrate` **is** in our `go.mod` and
  govulncheck does cover the copy compiled into the Lite path, but the binary
  inside `migrate/migrate` was built from upstream's dependency set with
  upstream's toolchain, and nothing here read it. Two migrate binaries, one
  covered.

  `deploy/Dockerfile.migrate` now compiles golang-migrate's own `cmd/migrate`
  package — upstream's CLI, not a reimplementation — at the version `go list -m`
  reports, onto `gcr.io/distroless/static-debian13:nonroot`. Same entrypoint,
  same flags, same subcommands, so the chart's Job is unchanged; the registered
  driver list narrows to what Leoflow actually supports (`postgres`,
  `postgresql`, `pgx5`, and the `file` source) instead of the two dozen the
  general-purpose image carried. The image runs as UID 65532 in its own right
  rather than relying on the chart to override a root default, and drops from
  84 MB to 20 MB.

  Bumping the `FROM` pin would have cleared the same 50 findings, and was
  rejected for the reason it keeps working: it restores the blind spot on the
  day the next advisory lands. Instead the binary is now inside the module
  graph CI reads. `security.yaml` runs `govulncheck` over that package by name
  (with the build tags the image is built with, or the drivers that actually
  ship would go unanalysed), the image moves from the daily reporting scan to
  the **blocking** Trivy gate alongside `leoflow-server` — it now meets the
  same criterion, that every finding is closed by a commit here — and
  `scripts/check-migrate-cli-build-tags.sh` fails CI if the build tags or the
  Go toolchain pin ever stop matching between the Dockerfile and the scan. The
  demo `docker compose` stack builds the same image rather than pulling a
  separately pinned `migrate/migrate:v4.18.1`.

- **The Go toolchain is one fact with one value again, and four more duplicated
  version facts got a gate (#1036).** Four files declared the Go toolchain and
  they said three different things: `go.mod` pinned `toolchain go1.26.6`,
  `runtime/Dockerfile` defaulted `GO_VERSION` to `1.26.3`, the Makefile's chaos
  image to `1.26.4`, and `deploy/Dockerfile.server` had been carried to
  `golang:1.27-bookworm` by a Dependabot bump to the one file with a literal
  tag. Nothing was broken — CI and release both passed their own literal, so the
  published artifacts were consistent — which is precisely why it survived: the
  cost of this class is not a red build, it is that somebody reads one copy and
  reasons correctly from a false premise. All four now resolve to **1.26.6**,
  `deploy/Dockerfile.server` derives it from an `ARG GO_VERSION` default like
  `runtime/Dockerfile` already did, and both images were rebuilt to confirm it.

  Five gates now hold the line, each in the ~40-line shape of the two that
  already worked (`check-lite-prepull-matches-compose.sh` and the Task SDK check
  in `ci.yaml`), each with a `--self-test`, all globbed into the release cut's
  pre-flight and run per-PR by the new `Duplicated version facts agree` job:
  `check-go-toolchain-pin.sh` (eleven copies, prose comments included),
  `check-python-host-interpreters.sh` (the managed CPython must be a *member* of
  the published matrix and every host probe either published or marked
  `// lite-only` — these are relationships, not equality, see #1031),
  `check-golangci-lint-pin.sh` (four copies; `make lint is clean` only predicts
  CI while they agree), `check-lite-postgres-tag.sh` (what Lite starts vs what
  Lite *tells you* it started) and `check-airflow-ui-pin.sh` (the pin the
  Makefile fetches vs the marker committed next to the bundle the binary
  serves).

- **The `Trivy (image scan)` job is renamed to `Trivy (filesystem scan)`,
  because that is what it runs (#1036).** It has run `scan-type: fs` under an
  image-scan name for many releases, behind a comment deferring image scanning
  until `deploy/Dockerfile.server` existed — it has existed the whole time. The
  name is the fix rather than the behaviour: container images are already
  scanned by the `Trivy (leoflow-server image)` gate next to it and by the
  scheduled `image-scan.yaml` workflow (#1034), and a third copy here would only
  duplicate them.

- **The task base image moves from Debian 12 (bookworm) to Debian 13 (trixie),
  which takes its OpenSSL from 3.0.x to 3.5.x.** `python:3.x-slim` links CPython's
  `ssl` module against the **system** OpenSSL, so this is the library every TLS
  call made from inside a task terminates in — every provider hitting an API,
  every `requests` call, every warehouse driver. bookworm ships OpenSSL 3.0.x,
  whose **upstream support ended 2026-09-07**; trixie ships 3.5.x, supported to
  **2030-04-08**. The suite itself is on the same trajectory: Debian 12 left
  regular security support on 2026-06-11 and is now oldstable under the LTS
  team's narrower, best-effort scope, while Debian 13 has full Security Team
  support to 2028-08-09 and LTS to 2030-06-30. Verified on all four interpreter
  variants (3.10 / 3.11 / 3.12 / 3.13): `openssl 3.5.7-1~deb13u2`, and the image
  still ends on numeric UID `65532` with `import leoflow, leoflow_runtime`
  working.

  **This changes what your `system_packages:` resolves to.** That key emits
  `apt-get install` into the generated DAG image, and apt now resolves against
  trixie rather than bookworm — so package versions move, and a name or version
  pin that only existed in bookworm has to be re-pinned. apt itself goes 2.6.1 →
  3.0.3 and repository signature verification moves from GnuPG's `gpgv` to
  Sequoia's `sqv` (the slim image now ships **no `gpgv` at all**); a
  `system_packages:` install of `curl git libpq-dev ca-certificates` was compiled
  and built end-to-end against the new base to confirm the path still works.
  Debian's own repositories were exercised and verify cleanly; a **third-party**
  apt repository added by a custom Dockerfile layer was not, and is the case to
  watch, since `sqv` is stricter than `gpgv` about legacy key material. Such a
  repository fails loudly at build time rather than silently, but it can fail.

  **Your outbound TLS handshake changes shape, and this is the one to read if
  your tasks talk to anything behind a corporate proxy.** OpenSSL 3.5 enables
  the hybrid post-quantum group `X25519MLKEM768` by default and puts its key
  share in the first flight, which takes the ClientHello from **517 to 1525
  bytes** (measured, same `ssl.create_default_context()`, bookworm vs trixie).
  Middleboxes, TLS-inspecting proxies and a few load balancers are known to
  mishandle a first flight that no longer fits one segment; the symptom is a
  handshake that hangs or resets **at task runtime in a pod, after a green
  build** — against one endpoint, from inside your network, which is the hardest
  shape of failure to attribute. If you hit it, restore the classical group list
  by pointing `OPENSSL_CONF` at a file containing
  `[system_default_sect]` / `Groups = x25519:secp256r1:x448:secp521r1:secp384r1`
  (verified: that puts the ClientHello back to 517 bytes) and tell us, so the
  base can carry the setting if it turns out to be common.

  **What did NOT change is the certificate floor**, which is worth stating
  because it is the thing people expect an OpenSSL major bump to break. Both
  suites already run at security level 2. Measured on four probes against a
  local TLS server, bookworm's 3.0.20 and trixie's 3.5.7 behave identically: an
  RSA-1024 leaf, an RSA-1024 CA and a SHA-1-signed leaf are refused on both, and
  TLS 1.1 is refused on both. If a legacy vendor certificate worked before this
  release, it still works.

  One silent behavior change is pinned rather than inherited: Debian's `passwd`
  went 1:4.13 → 1:4.17.4 and the `HOME_MODE` default tightened with it, so the
  unchanged `useradd` line produced `0755` on bookworm and `0700` on trixie.
  `0700` is kept — a task pod runs as this very UID, so the owner traverses its
  own home and no supported path breaks — but `runtime/Dockerfile` now sets the
  mode explicitly, so it no longer moves when a distro default does. The
  agent build stage stays on `golang:*-bookworm` deliberately; see the comment in
  `runtime/Dockerfile` for why (nothing from it reaches a task pod).

  **Rolling back is not symmetric across Python lines.** Pinning `base_image`
  to a `v0.4.5` tag gets you the bookworm base again — for `py3.10`, `py3.11`
  and `py3.12`. There is no `py3.13-v0.4.5`: that leg is published for the first
  time in this release, so it only ever existed on trixie. A project on
  `python_version: "3.13"` that needs bookworm has to move to `"3.12"` as well
  as pin the older base.

  **This path now has CI behind it.** Nothing in the repository built a DAG
  image with `system_packages:` set, so the apt half of this change was
  certified by a hand-run build and nothing re-certified it after merge. The
  k3d operator E2E now compiles a project through the **generated** Dockerfile
  (no hand-written one, which would have skipped the apt layer entirely),
  asserts the package landed, and makes one real outbound HTTPS request from
  inside the task pod. The same run asserts the base image's own properties —
  the Debian suite (read out of `runtime/Dockerfile` rather than hardcoded), the
  `65532:65532 0700` home dir, and the numeric final `USER` — each of which was
  previously guarded by a comment and by nothing else.

### Deprecated

- **`python_version: "3.10"` — the `py3.10` base image stops being published
  after 2026-10-31.** Python 3.10 reaches upstream end-of-life on that date, and
  `docker-library/python` stops rebuilding an EOL line the day after
  (`python:3.9-slim` was last rebuilt 2025-11-01, one day after 3.9 went EOL).
  From November, `python:3.10-slim` — and therefore
  `ghcr.io/neochaotic/leoflow-runtime:py3.10` — receives no further OS security
  updates and accumulates unfixed CVEs indefinitely.

  **Nothing breaks today.** The `py3.10` leg keeps being built and pushed on
  every release until then, and images you have already built keep running. This
  is announced rather than dropped silently because a DAG image is built `FROM`
  the base and many projects pin `base_image`, so our choice becomes yours until
  you rebuild. `leoflow validate`, `leoflow compile` and `leoflow deploy` now
  print a warning when your project resolves to a deprecated line, naming the
  removal date and the fix. The warning is at authoring time, not at task boot:
  the author choosing the interpreter is the only person who can change it, and
  a per-task-pod warning would reach the operator instead and repeat on every
  run until people filtered it out. With `--build` it is repeated in one line
  after the image is built, because minutes of builder output otherwise scroll
  it off the top of the terminal.

  **The warning names the field you actually set, and its fix works on that
  field.** Setting `python_version: "3.10"` is told to change `python_version`.
  Pinning one of our published `py3.10` tags through `base_image` — including a
  digest pin such as `…:py3.10-v0.4.5@sha256:…` — is told to repoint
  `base_image`, and is given the exact replacement tag with its suffix
  preserved. The two are separated because `base_image` is used verbatim: an
  author who pins it and is told to change `python_version` can follow that
  advice, rebuild, and get the identical warning back.

  **What to do:** set `python_version: "3.11"` (or `"3.12"` / `"3.13"`) in
  `leoflow.yaml` and rebuild — or, if you pinned `base_image`, repoint it to the
  matching `py3.11` tag and rebuild.
### Fixed
- **The chart no longer renders a HorizontalPodAutoscaler the apiserver
  rejects.** `autoscaling.minReplicas` and `maxReplicas` are independent values
  with independent defaults (2 and 6), so `--set autoscaling.maxReplicas=1`
  alone produced a maximum below the minimum — a clean render and a failed
  install, naming neither value the operator set. Refused at render time now,
  along with a minimum below 1, which Kubernetes rejects without the
  `HPAScaleToZero` gate ([#947](https://github.com/neochaotic/leoflow/issues/947)).
- **The migration hook's pod no longer answers for the control plane on
  `helm upgrade`.** A Service selector matches every pod whose labels *contain*
  it, and for the default (non-split) install the control-plane Service's
  selector is exactly the label set the migrate Job's pod template carried — so
  the pre-upgrade hook pod was selected by the Service and by the
  PodDisruptionBudget (on the HA profile, where one renders) for the whole
  migration window, `Ready` from its first
  instant because a Job pod has no readiness probe. `helm install` was never
  affected (no Service exists yet when the pre-install hook runs) and neither was
  `split.enabled` (api and scheduler carry a component label the Job lacked).

  The measurable loss was disruption accounting, not traffic. Held open on k3s
  v1.33.6, an integer `minAvailable` budget reported `currentHealthy: 2` over one
  real replica — the hook pod padded the count, so at the default
  `minAvailable: 1` the hook pod **alone satisfied the budget** and every real
  replica became evictable for the migration window, not one extra — and a
  percentage-valued budget
  failed outright (`DisruptionAllowed=False`, *"jobs.batch does not implement the
  scale subresource"*), pinning `disruptionsAllowed` at 0 and stalling every
  voluntary eviction until the hook exited. Requests were *not* black-holed, for
  a reason that is accidental rather than designed: all three Service ports use
  **named** `targetPort`s and the hook pod declares no container port, so its
  endpoint landed in a slice with `ports: null` and kube-proxy programmed nothing
  for it. Change one of those to a numeric `targetPort` and the same cluster
  black-holed 19 of 40 requests; consumers that resolve ports themselves (AWS
  Load Balancer Controller in IP-target mode, service meshes, Gateway API) were
  never covered by the accident at all.

  The hook's pod now runs under its own application name
  (`app.kubernetes.io/name: <name>-migrate`, `component: migrate`). Adding a
  distinguishing label while keeping the shared pair would have changed nothing —
  a superset still matches a selector — so the pod differs on a key the selector
  *carries*. It is also the truer label: this is a different image with a
  different lifetime, not a replica of the control plane. One consequence worth
  knowing: with `networkPolicy.enabled`, the control-plane policy used to govern
  the hook pod on upgrade but not on install; now it governs it in neither, which
  is at least symmetric. Egress is allow-all by default, so nothing changes for a
  default install — but in a namespace with a default-deny policy the migrate pod
  needs its own allowance to reach Postgres, on upgrade as well as on install.
  `scripts/check-migrate-pod-selection.sh` renders the chart in every deployment
  mode and fails if any Service or PodDisruptionBudget selector is ever again a
  subset of the hook pod's labels
  ([#1055](https://github.com/neochaotic/leoflow/issues/1055)).
- **`execution_timeout` now applies to the task, not just to its first
  process.** A task that left a grandchild behind defeated the timeout
  completely. The agent's cancel SIGKILLed the direct child alone; the surviving
  grandchild kept the stdout pipe it had inherited open; and because the agent
  wires stdout to a log writer rather than to a file, the standard library
  interposes that pipe and blocks `Wait` on its copying goroutine until every
  writer closes it. So the deadline fired, nothing died, and the agent never
  reached the branch that reports the timeout or writes the durable outcome
  record. The attempt ended minutes later on the kubelet's
  `activeDeadlineSeconds` with the generic container-terminated reason and
  nothing for the reconciler to recover — the exact state the pod-deadline
  arithmetic exists to prevent, reached through the front door
  ([#943](https://github.com/neochaotic/leoflow/issues/943)).

  This is the ordinary task shape, not an exotic one: the bash operator runs its
  entrypoint under `bash -c`, so any `&&`, pipe, `;` or background job makes the
  user's real programs grandchildren of the process the agent kills, and the
  same holds for any Python task that shells out. The agent now starts the task
  in its **own process group** and the cancel SIGKILLs the **group**, so the
  whole tree goes with the deadline. A bounded 10-second `WaitDelay` backstops
  the residue — a descendant that escapes the group by calling `setsid` — so
  nothing can hold the wait open indefinitely any more. Measured on the
  regression test: a shell backgrounding a long-running child under a 500 ms
  deadline returned after 23.5 s before the fix and half a second after it,
  with the task's process group gone.

  This **supersedes the limitation stated in 0.4.5**, which scoped the
  `execution_timeout` guarantee to a task whose process tree exits with it. The
  end-to-end scenario that deliberately avoided the shape now spawns such a
  grandchild on purpose and asserts the grandchild's own line in the shipped
  log, so a spawn that silently fails cannot let it pass while covering only the
  easy case.

  One behavior is worth knowing, and one boundary on it. A task with **no
  declared timeout** that exits successfully while leaving a background process
  holding the pipe keeps its own exit status rather than being turned into a
  failure; it loses only the tail of its output once the delay expires, and the
  agent logs a warning saying so. It also now takes an extra 10s to return,
  where before it hung indefinitely.

  Where a timeout **was** declared and fired, the attempt is reported as
  `execution_timeout` regardless of what the task's own exit status was — the
  classification reads the deadline, not the exit code. That is pre-existing
  and unchanged here; this fix narrows the window in which it can happen but
  does not close it.
- **A version floor in `dependencies` was silently dropped, and left junk in the
  image.** `RUN` in a Dockerfile is `/bin/sh -c`, and the specifiers were joined
  into that line unquoted — so `setuptools>=80.9.0` was a *redirection*: pip
  received a bare `setuptools`, the floor vanished, and a file named `=80.9.0`
  appeared in the image holding pip's stdout. The build stayed green and the
  only symptom was the wrong version inside the image. Measured with the base
  seeded at `setuptools==79.0.1`: it stayed at 79.0.1. A floor is how you
  remediate a CVE in a transitive dependency, so this failed exactly where
  someone was relying on it. Every entry is now shell-quoted, for
  `system_packages` too, which used the identical join
  ([#1064](https://github.com/neochaotic/leoflow/issues/1064)). Quoting in the
  YAML never helped — the parser consumes those quotes before Go sees the value.
  Entries are also passed after a `--`, because quoting guarantees "one argv
  element" and not "a package": `dependencies: ["--dry-run", "six"]` built green
  with `six` absent — the same silent-failure shape as the bug itself — and an
  apt `-o DPkg::Pre-Invoke::=<cmd>` ran that command as root during the build.
- **`taskNetworkPolicy.allowMetadataEgress` can no longer reopen the metadata
  range it exists to punch a single hole in.** The value was documented as an
  escape hatch for one `/32` each — the GKE metadata server or the EKS Pod
  Identity Agent — and nothing enforced it, so `169.254.0.0/16` rendered and
  installed. That is not a wider hatch, it is the removal of the block:
  NetworkPolicy egress rules are additive (traffic is allowed if it matches at
  least one rule), so the allow rule this value emits overrides the
  `except: [169.254.0.0/16]` in the allow-all rule, and the rendered manifest
  goes on showing an except-list that blocks nothing. The one containment the
  task-pod policy's security story rests on was a comment.

  The chart now **fails the render** on any entry wider than one host, naming
  the entry, its index, why it is refused and what to pass instead. An IPv4
  `/32` and an IPv6 `/128` are accepted (IPv6 because the allow-all rule is
  `0.0.0.0/0` and matches no IPv6 destination, so this list is the only route to
  an IPv6 metadata endpoint while the policy is on); surrounding whitespace is
  trimmed, since a padded string reaches the chart intact from `--set-string`
  and Argo CD's `helm.parameters` and would render a `cidr` the apiserver
  rejects. Also refused, so the guard is not one string wide: a prefix above the
  host width, a non-numeric prefix, a bare address with no prefix, a blank
  entry, anything that is not an address, and the value sent as a scalar instead
  of a list — the last one used to die with `range can't iterate over
  169.254.169.254/32`, naming neither the value nor the fix. Legitimate values
  render byte-for-byte as before. To reach a wider range that is *not* the
  metadata range, use `taskNetworkPolicy.extraEgress`
  ([#958](https://github.com/neochaotic/leoflow/issues/958)).
- **`exclude_paths` now reaches the image build.** It had been in the schema,
  defaulted, and documented as "skipped both in image build and workspace
  discovery" with zero consumers in build code, so with the default
  `project: "."` the single `COPY . /home/leoflow/` baked the entire context.
  It is now materialized as a `.dockerignore` for the duration of the build —
  merged with yours if you have one, restored afterwards
  ([#995](https://github.com/neochaotic/leoflow/issues/995)).
- **dbt's host-side parse artifacts no longer ship in DAG images.** The compile
  runs `dbt parse` on the host, which writes into the project it parsed; the
  wholesale `COPY` then baked `.user.yml` — dbt's anonymous-usage cookie, a
  stable UUID identifying the build host, read by the in-pod dbt so every pod
  reported as that user — and `logs/dbt.log`, carrying absolute host paths
  ([#1013](https://github.com/neochaotic/leoflow/issues/1013)). `target/`,
  `dbt_packages/` and `profiles.yml` are deliberately left in place: each is a
  real input in a configuration people use (the `dbt.manifest` path, host-side
  `dbt deps`, and BYO profiles with `DBT_PROFILES_DIR`).
- A build now warns when something credential-shaped is in the context and not
  excluded: a file (`.env` and its `.env.*` variants, `.netrc`, `.pypirc`,
  `credentials.json`, a service-account key, an SSH private key, a
  `kubeconfig`, a BYO `profiles.yml`) or a whole directory (`.ssh`, `.aws`,
  `.gnupg`, `.azure`, `.kube`, `secrets`). It checks each dbt project directory
  as well as the context root, fires only for paths a `COPY` actually reaches —
  a plain `dag.py` project copies one file, so warning there would be false —
  respects an exclusion you already wrote in your own `.dockerignore`, and
  repeats itself in one line after the build, where minutes of layer output
  have scrolled the first warning away. It is a warning rather than a silent exclusion because a `.env` can
  be a legitimate input, and breaking `load_dotenv()` far from its cause is the
  wrong trade in the other direction.
- The pre-install migration Job now mounts `database.caConfigMap`. It reads the
  same DSN as the control plane, so the chart's own managed-Postgres recipe —
  `sslmode=verify-full&sslrootcert=/etc/leoflow/db-ca/ca.crt` — produced an
  install where the server came up and the Job failed. Only reachable against a
  database whose CA is outside the system trust store, which is why no
  in-cluster test saw it
  ([#1052](https://github.com/neochaotic/leoflow/issues/1052)).
- The migration Job declares CPU and memory requests, so it is no longer
  BestEffort. A namespace `ResourceQuota` on `requests.*` rejected it at
  admission and failed `helm install` outright, and eviction under node pressure
  could leave the schema dirty mid-migration
  ([#1053](https://github.com/neochaotic/leoflow/issues/1053)). Limits stay
  within 2:1 of requests so a `LimitRange` with `maxLimitRequestRatio` cannot
  reintroduce the same admission failure by another route.
- Readiness no longer fails because the control plane is busy. The probe's two
  database reads now come from a dedicated one-connection pool instead of the
  pool serving API traffic, so a saturated control plane can no longer make
  every replica report itself unready in the same window and empty the Service
  ([#1042](https://github.com/neochaotic/leoflow/issues/1042)).
- `/readyz` and `/api/v2/monitor/health` bound the whole check at 2s instead of
  granting a fresh 2s per dependency with `Ping` unbounded, so a slow dependency
  produces a 503 rather than a probe that reports nothing
  ([#1040](https://github.com/neochaotic/leoflow/issues/1040)). Dependencies are
  checked in a stable order, and a 503 caused by the budget running out says so
  and names the dependency that consumed it — under one shared budget the check
  that fails is otherwise whichever one happened to be running when time ran
  out, which is usually not the one at fault.
- Node drains and cluster-autoscaler scale-down are no longer blocked during a
  load spike. `disruptionsAllowed` is computed from ready pods, so a spike that
  made every replica unready used to freeze the PodDisruptionBudget at zero for
  the duration of the incident.

- **`/readyz` no longer reports ready over a database with no schema (#1023).**
  The readiness probe pinged each dependency and nothing more, and a Postgres
  `Ping` succeeds whenever the *connection* is healthy — it says nothing about
  what is behind it. Found on the v0.4.5 RC cluster: a node recycle recreated an
  ephemeral-storage Postgres **empty**, and the running control plane answered
  `{"status":"ready"}` HTTP 200 in the same seconds the scheduler was failing
  every tick on `relation "dag_runs" does not exist` and `/auth/token` was
  answering 503. Readiness now asserts the same invariant boot does —
  `schema_migrations` present, clean, and not behind the version this binary
  embeds — through the same storage-layer code path, so the two verdicts cannot
  drift. This is the signal Kubernetes routes traffic on and calls a rollout
  successful on, so the gap let `helm upgrade --wait` report success against a
  control plane that could not serve one authenticated request, let a rolling
  update replace working pods with broken ones, and kept a 503-ing pod in
  Service rotation. It also covers the cases that reach installs with durable
  storage: a restore from a backup older than the running binary, a failover to
  a replica that has not caught up, a migration rolled back out of band, and a
  `database.url` repointed at the wrong database.

  **A migration in flight does not take the control plane out of rotation.**
  The migrate Job is a `pre-install,pre-upgrade` Helm hook, so on upgrade it runs
  while the old pods are still live and serving, and golang-migrate marks the
  schema dirty for the whole execution of each migration body. Every replica
  reads that same row, so a probe that treated dirty as not-ready would flip all
  of them at the same instant for any migration longer than 30s
  (`periodSeconds × failureThreshold`) and drop the Service to zero endpoints —
  and in the single-Deployment `all` role that Service also carries gRPC, so
  running task pods would lose the control plane mid-upgrade. Readiness is
  therefore version-aware where boot is not: a dirty schema **above** the
  version the running binary embeds is a forward migration that does not concern
  it, and the pod keeps serving (one log line per episode, not one per probe). A
  dirty schema at or below that version is a half-applied schema the binary
  actually depends on, and it goes not-ready. Boot still refuses to *start*
  against any dirty schema — a process that has not begun serving has nothing to
  lose by waiting, and that is the one state where the two verdicts are meant to
  differ.

  **`/api/v2/monitor/health` asserts the same thing.** It reads the same
  dependency map and was also Ping-only, so in the state above it reported
  `metadatabase: healthy` at HTTP 200 while `/readyz` was 503 and the pod was out
  of the Service — the Airflow-compatible surface the UI dashboard renders was
  the last place still claiming the database was fine.

  Three further properties of the fix are deliberate. The response body stays
  vague — it names the dependency and nothing else, because `/readyz` is
  unauthenticated and the underlying error can carry a DSN or an internal
  hostname; the detail goes to the log. It also distinguishes what it saw:
  `postgres schema not current` is a schema verdict, `postgres unavailable` is a
  database that could not be read at all (a timeout, a reset connection, an
  exhausted pool). Both are 503, but only one of them is a reason to go looking
  at migrations. **Liveness is unchanged**: restarting a pod does not create a
  schema, so a crash loop would trade an honestly not-ready pod for one that
  cannot even be inspected. And the check is bounded at 2s, under the chart's 3s
  `probes.readiness.timeoutSeconds`, so a wedged database makes the probe report
  not-ready rather than report nothing at all. An **ahead** schema still passes,
  as it does at boot — expand-contract migrations keep older code working, and
  failing it would break `helm rollback`.

### Security

- **Database errors no longer reach API clients (#961).** A repository failure
  used to be rendered into the problem-detail body verbatim, and Postgres
  errors render themselves as `severity: message (SQLSTATE code)` — with the
  constraint, table and column names of the schema inside the message. Only
  SQLSTATE `23505` was translated (to a 409), so every other code — a `23503`
  foreign-key violation, `23502` not-null, `42P01` on a database whose
  migration is behind, `40001`, `53300` — was disclosed to any authenticated
  tenant of a multi-tenant control plane (CWE-209). A `pgconn` connect failure
  additionally carries the database user and name, and reached the 499 branch
  the same way because it satisfies `errors.Is(err, context.DeadlineExceeded)`.

  Responses now carry a fixed phrase per condition — a 404 still reads as
  missing, a 409 as a conflict, a 400 as rejected input — and the real error
  goes to the request log under a new `cause` field, so nothing an operator
  needs is lost. The default is deny rather than allow: only a message Leoflow
  composed itself (`domain.Safef`) is echoed, so a newly wrapped driver error
  cannot leak by omission. The messages worth reading survive unchanged — an
  unknown role, an undeclared variable or connection, a `max_active_runs` cap,
  the undeletable default pool.

- **Database errors no longer reach the task pod either (#1068).** The same
  disclosure class was open on the agent gRPC transport, where twenty call
  sites put `%v` of a storage, XCom, log-sink or signing failure into the
  status message returned to the agent — so a SQLSTATE, a constraint name or a
  `pgconn` connection string reached the pod. That pod is a trust boundary: it
  runs the tenant's own image and entrypoint, so it is authenticated,
  tenant-controlled code execution that can call `GetTaskSpec`,
  `GetVariables` or `FetchXCom` in a loop. The control plane already treats it
  as one — a panic in a handler has always been collapsed to a constant for
  exactly this audience — and that rule now covers the ordinary failures too.

  Every status the agent receives still names the step that failed — `loading
  task spec`, `storing xcom`, `fetching connections`, `opening log sink` — so a
  task's own log still says something usable; only the underlying error's text
  is replaced, and it is written to the control-plane log under a `cause`
  field carrying the attempt identity (`ti`, `run`, `task`, `try`), which is
  the join key between the two. No status code changed. One message is passed
  through deliberately, as a `domain.SafeError`: `task "x" not found in run
  "y"`, which tells you the pod is running a task its `dag_version` does not
  declare — a stale image or a mismatched version — rather than that the
  control plane is broken.

  This also closes a second-order path: the agent puts a failed RPC's text into
  the state it reports, which is persisted on the task instance and rendered in
  the UI, so a driver error could reach a browser through the pod.

### Testing

- **The DAG base-image pin is now verified end to end, for both of its branches
  (#1032).** A generated DAG Dockerfile builds `FROM
  ghcr.io/neochaotic/leoflow-runtime:py<ver>-v<X.Y.Z>` when the CLI is a released
  build and from the moving `:py<ver>` line otherwise, and that immutability is
  what makes a base-image bump safe to ship in a patch. Only the pure function
  choosing the tag was tested; nothing asserted that a real, version-stamped
  binary emits it. Worse, the version stamp is exactly what selects the branch —
  `make build` stamps `git describe` — so which branch the toolchain exercised
  flipped with the distance from the last tag: pinned on the tag commit, moving
  one commit later, with nothing declaring which was under test and nothing
  failing when it changed. A new integration test (`go test -tags integration
  ./internal/cli/`, `make test-integration`) builds the CLI three times with the
  version linked in — the GoReleaser release form `9.9.9`, the tag form `v9.9.9`,
  and a describe form `v9.9.9-3-gdeadbee` — compiles a scaffolded project with no
  `base_image:`, and asserts the exact `FROM` the builder is handed. Needs no
  cluster and no registry, so it runs on every pull request. No shipped behavior
  changes.

- **A user-facing change without a docs update now fails CI (`skip-docs` to
  exempt).** `taskPodSecurity.readOnlyRootFilesystem` — this tranche's security
  hardening — shipped in rc.3 with zero mentions anywhere under
  `website/content/`, having been validated on a real cluster the same day. The
  CHANGELOG guard exists because that same failure repeated at least eight times
  with changelog entries; this applies the lesson one surface earlier. The gate
  keys on capability rather than churn — the chart values file, the authoring
  schema, and the CLI commands — so internal refactors do not trip it, and it
  mirrors the CHANGELOG guard's shape (standalone workflow, re-runs on label,
  Dependabot exempt by author). The missing hardening page is written.


## [0.4.5] - 2026-09-09

### Added

- **`auth.secretScoping` is now a chart value, and the security flips are
  reachable from the install path (#803, #800).** The policy that decides whether
  a task pod receives the whole tenant vault was the one ADR 0055 knob with no
  Helm value — settable only through `extraEnv` — so `values.yaml`, the file an
  operator reads to discover what is tunable, showed neither the knob nor its
  default. It is now a first-class value, stamped into the control-plane
  Deployment env on every render like its two siblings, and the configuration
  reference row carries the `Helm:` hint the others had. **The default is
  unchanged (`permissive`)** and no render behavior changes: the value is
  deliberately *not* in the chart's guarded-variable list, so an operator who
  already set `LEOFLOW_AUTH_SECRET_SCOPING` through `extraEnv` keeps working —
  `extraEnv` renders after the chart-managed block, and a helm-unittest case now
  pins that last-wins ordering so nobody reverses it. The installation guide's
  **Production hardening** section, which mentioned none of the four flips, now
  names the three `auth.*` ones with their permissive-by-default values and links
  the table that already compares them, and carries the caveat that
  makes the scoping flip breaking: under `enforce` a DAG that declares **nothing**
  receives **nothing**. Relatedly, the two pages that prescribe the
  observe-then-flip arc now state what a clean scope-warning trail does *not*
  prove. The warning counts only declared names that actually resolve, so two
  populations never appear in the trail: a DAG that declares nothing, and a DAG
  whose declared names no longer exist in the vault — an all-stale declaration
  counts as zero, which is what secret rotation produces. Both receive the whole
  vault today and nothing under `enforce` (#800; the code half is still open). The RC cluster
  validation runbook gains the matching assertion, so a green RC stops certifying
  past the blind spot.

- **Control-plane HA as the first-class, guarded posture (Helm chart + docs).** A
  production drill showed the single-replica control plane is evicted by
  autoscaler consolidation as routine bin-packing, and each restart costs tens
  of seconds (image pull + boot + leadership) during which nothing dispatches.
  The chart already supported `replicaCount > 1` (leader election); it now makes
  the safe HA path one switch and guards the unsafe one. `replicaCount > 1` (or
  an HPA / split with more than one mounter) on a `ReadWriteOnce` **or**
  `ReadWriteOncePod` logs PVC now **fails the render** with a message that leads
  with the recommended fix (object-store task logs, `logs.persistence.enabled:
  false` + `logs.sink`) and names the `ReadWriteMany` alternative — previously
  `ReadWriteOncePod` slipped through and the message pointed only at RWX. The
  same mounter ceiling now drives a second refusal: `split.enabled` with
  `logs.persistence.enabled: false` and the `disk` sink fails the render, because
  the scheduler pod would write every task log into its own emptyDir while each
  api pod reads from its own — no log ever readable, silently.
  `podDisruptionBudget.enabled` is now tri-state: unset (the new default) renders
  the PDB **iff** the guaranteed replica floor is above one (`replicaCount`, the
  HPA `minReplicas`, or `split.api.replicaCount`), because a `minAvailable: 1`
  budget over a single replica blocks every voluntary eviction (node drains hang,
  auto-upgrades stall); explicit `true`/`false` still wins. **Upgrade note:** this
  is not only for hand-set `replicaCount > 1` — every `split.enabled` install
  (api default 2) and every `autoscaling.enabled` install (`minReplicas` default
  2) gains a PodDisruptionBudget on `helm upgrade`; the install NOTES call it out,
  and `podDisruptionBudget.enabled: false` opts out. The PDB sets
  `unhealthyPodEvictionPolicy: AlwaysAllow` (new value, `""` omits it for
  apiservers older than 1.27) so two unready replicas never leave a node drain
  hanging on an already-down control plane. NOTES also warn when a PDB is forced
  onto one replica or when more than one pod (HPA ceiling or split included) runs
  on per-pod emptyDir logs. New `topologySpreadConstraints` value (a constraint
  without `labelSelector` gets the Deployment's own selector) and new
  `terminationGracePeriodSeconds` value, both unset by default so a default
  install's pod spec is unchanged; the grace buys headroom for the HTTP shutdown
  and the dispatch-pool drain, not leadership handoff (the lease frees within a
  tick of SIGTERM regardless). New `helm/leoflow/examples/values-ha.yaml`
  profile: two replicas spread across nodes, object-store logs with memory sized
  for the sink's per-attempt buffer, auto PDB, 60s drain grace, and a commented
  EKS/Karpenter-only `karpenter.sh/do-not-disrupt` opt-in. New docs page
  *Control-plane HA and disruption posture* covers the restart window, the
  storage precondition (including what the object sink makes durable and when),
  why the api/scheduler split is not dispatch-HA, the single-replica PDB trap and
  how each platform honors PDBs, and the involuntary disruptions no PDB prevents.
  The shipped default stays `replicaCount: 1`: flipping it would
  Multi-Attach-break every existing install on upgrade.

- **Boot `WARN` when `auth.max_attempt_credential_lifetime` is disabled.** A
  non-positive value is a documented setting the boot-time ladder check
  accepts, but it removes every wall-clock bound the ceiling carries: heartbeat
  renewal of an attempt's credential becomes unbounded, task pods with no
  declared `execution_timeout` get no `activeDeadlineSeconds` floor, and with
  warm pools enabled the per-attempt watchdog is off — a wedged task then has no
  bound of its own even with a healthy control plane. The server logs one
  `WARN` naming the key and the consequences; boot proceeds. The field's godoc
  and the agent-credential-transport and warm-pools operator pages now state
  every role of the knob.

### Changed

- **The reapers now run from a 30 s leader maintenance loop, ordered after the
  pod reconciler, behind one leader-settling gate.** The five reapers
  (orphan-run, agent-lost, dispatch-lost, pod-lost, warm-worker-lost) no longer
  run from the scheduler's 1 s tick. They run once per maintenance cycle, and
  every cycle reconciles first — recovering each finished pod's durable outcome —
  then reaps, so a reaper always judges post-reconcile state. The two clocks
  whose independence let a restart mark a succeeded task failed are now one.
  The per-reaper post-leadership graces (agent-lost, pod-lost) are replaced by a
  single **settling gate** at the entry of the reaper pass: after a
  (re-)election no reaper fires until the settling grace (180 s) has elapsed,
  the pod informer has synced, and a reconciler sweep has completed under this
  leadership. A **liveness valve** opens the gate after 2 × grace with a `WARN`
  and a `reap_settling_valve_open` decision if the sweep never completes, so a
  broken reconciler cannot silently disable reaping. New decisions
  `reap_settling_skip` / `reap_settling_valve_open`; `agent_lost_grace_skip` and
  `pod_lost_grace_skip` are gone. The boot-time ladder check now enforces
  `heartbeat < agent-lost threshold < settling grace < token TTL` and two
  maintenance cycles inside the grace. Each phase of a cycle runs under its own
  one-interval budget, so a sweep hung on a slow apiserver cannot starve the
  reap and a slow reap cannot starve the sweep it depends on (an overrun logs a
  `WARN`). **Detection latency note:** a stuck
  run/TI is now noticed up to 30 s after its threshold elapses instead of
  within 1 s — against thresholds of 60 s–5 min, and reaping being a backstop
  rather than the primary path. Lite (no pods) is unchanged: no maintenance loop,
  no reaping.

- **Dependency refresh, with the generated protobuf code regenerated to match.**
  The AWS SDK (including S3), the Google Cloud Storage client, the OpenTelemetry
  SDK, go-oidc and gRPC move to their current minor and patch releases; the S3
  and GCS clients are the object log sink's own transport, so the release
  validates them rather than shipping them unexercised. `google.golang.org/protobuf`
  moves from a pseudo-version to the `v1.36.12` release, which changes the
  generator the committed code must match, so `proto/agent/v1` is regenerated in
  the same change — the proto-sync gate pins `protoc-gen-go` to the module version
  precisely so this cannot drift silently.

### Fixed

- **A `dbt:` block alongside a `dag.py` is now refused instead of silently
  discarding the Python (#1001).** `compile` routes on the `dbt:` block before
  the parser is ever invoked, so the Python was never read and nothing said so:
  `validate`, `compile` and `deploy` all reported success while shipping a DAG
  missing every non-dbt task. This is also the state a migration passes through,
  because writing the `dag.py` before deleting `dbt:` is the order a person
  naturally works in — and the dbt authoring page now recommends that migration.
  `compile` and `validate` both refuse the pair and name which block to delete.
  Refusing rather than merging keeps "which one wins, and how would they
  compose?" out of the decision: no author means both at once. A genuine
  dbt-only project is untouched — the check keys on the DAG source existing.
  **Upgrade note:** a dbt-only project carrying a *leftover* `dag.py` now fails
  to compile until you delete the file. If you added an empty one to work around
  #769 — `deploy --build` failed on `COPY dag.py` for pure-dbt projects between
  v0.3.0 and v0.4.0 — that workaround has been unnecessary since v0.4.0 and the
  file can go.
- **A transient flake during a release no longer costs a tag (#979, #983).** The
  gate retracts a failing release to a draft, and most smoke jobs install with
  `curl … install.sh | sh`, which downloads from the public release-asset URL —
  which a draft does not serve. So every re-run of a retracted release failed on
  the download rather than on whatever failed first, the gate's own "green
  re-run un-drafts it" path was unreachable, and the only exit was a new tag
  (tags are immutable, ADR 0033). Observed live on `v0.4.5-rc.1`. The gate now
  prints the exact recovery, and a **Re-publish a retracted release** workflow
  prints the exact recovery. The real repair is one line of ordering in
  `cut-release.sh`: the un-draft existed (#862) but sat *inside* the flake
  branch, and a retracted release makes the smokes fail on the asset download —
  a failure matching nothing in `FLAKE_RE`, so `isflake` went to 0 and the
  un-draft never ran. The deadlock defended itself. It now re-publishes before
  classifying. The flake classifier also stopped reading an unretrievable log as
  "no flake": a job that dies in `Initialize containers` has no step log, so
  `gh run view --log-failed` returns empty — which silently disabled the rerun
  loop for this repo's most frequent failure (#1007, #978). It now falls back to
  the job logs API and treats an unreadable log as unknown, which reruns.
  Separately, the steps most likely to trigger the retraction gained retries: `cosign verify` (it fetches the Sigstore TUF root
  and talks to Rekor and Fulcio) and the seven distro prerequisite installs
  (only `apt-get` had retries; `dnf`, `zypper`, `pacman` and `apk` had none).
  `pacman -Sy` was also an unsupported partial upgrade that breaks on mirror
  rotation rather than on anything we changed; it is now `-Syu`.
- **CI service containers no longer pull from a registry that rate-limits by
  source IP (#1007).** GitHub-hosted runners share a small pool of egress IPs,
  so `public.ecr.aws` returned `toomanyrequests: Rate exceeded` on roughly 13%
  of main runs. The failure lands in `Initialize containers` — before
  `actions/checkout` and before any step — so no retry we write could reach it,
  and it fired even on pull requests that changed only markdown. All twenty
  references now use `mirror.gcr.io`, Google's unauthenticated pull-through
  cache for Docker Hub. The release workflow's seven distro smoke containers and
  the kind datastore manifest move too — those are pulled in the same pre-step
  phase, in the workflow where a red job costs a tag. `quay.io` stays as-is; it
  is a different registry, not the shared-IP pool.

  A new `check-service-image-registry.sh` gate keeps them from drifting back. It
  parses the workflow YAML — walking `jobs.*.container` and `jobs.*.services.*`,
  and resolving `${{ matrix.* }}` against literal matrix values — and enforces an
  **allowlist**, so a bare `image: postgres:16` pasted out of the compose file is
  caught too. A grep would not have been enough: an earlier version of this gate
  missed an env indirection, a folded scalar, a job-level `container:` and an
  uppercase key while failing on a comment that merely mentioned the registry.

  Note the trade: `mirror.gcr.io` is not covered by an SLA, and Google documents
  it for daemon-level configuration rather than direct addressing — which is not
  usable here, since the pull happens before any step we control. Twenty-plus
  call sites now depend on one host; the failure mode changes from ~13% red to
  all-or-nothing. The durable answer is a mirror we own, which needs a one-time
  manual package-visibility flip GitHub's API cannot perform.

- **`leoflow.yaml` rejects keys the schema does not define, and `leoflow validate`
  finally works on a pure-dbt project (#15, #996).** The authoring schema declares
  `additionalProperties: false` and that guard was **unreachable**: the loader used
  `yaml.Unmarshal`, which drops an unknown key into the void, and `Validate()` then
  marshals the *struct* back to JSON and validates that — so by the time the schema
  saw the document, the offending key had ceased to exist. Every typo, and every key
  written at the wrong level, was accepted in silence. This is not hypothetical: our
  own flagship dbt example taught a **top-level `schedule:`**, which is not a schema
  key (the real one is `dbt.schedule`), so a team following the docs shipped a DAG
  that never ran and took days to notice — `leoflow validate` said "is valid" the
  whole time. The three examples that taught it are corrected, and the reference
  table now says where the key belongs. The refusal names the offending keys and,
  for `schedule` specifically, points at both right answers. **This is breaking for
  a project carrying a stray key** — deliberately, and better at a GA than after
  one. Discovery is unaffected: a Lite workspace loads every subdirectory with a
  lenient parse, so a DAG with a typo keeps its `dag_id` and its `dbt:` block and is
  told off by `compile`, rather than silently vanishing from the workspace or being
  renamed after its directory. Separately, `validate` stat'd `dag.py` unconditionally,
  so a project whose DAG *is* the dbt project could never be validated at all; the
  check is scoped to a `dag.py` DAG, where a missing source still fails, and the
  dbt lane gets the equivalent check it never had: **`validate` now fails when
  `dbt.project` names a directory with no `dbt_project.yml`.** That is a new hard
  failure, breaking for anyone whose project relied on `validate` accepting it —
  deliberately, because the command exists to say "this is fine" and it was saying
  it for a project that cannot run. It does not yet cover `dbt_groups[*].project`;
  that gap is tracked separately.
- **A dbt task with no managed connection can find its project's own
  `profiles.yml` (#994).** The base image points `DBT_PROFILES_DIR` at an
  ephemeral `/tmp` dir so dbt's writes stay off the read-only project (#852) —
  and the side effect was that dbt never looked inside the project at all, so a
  project that ships its own `profiles.yml` failed in the pod with
  `Invalid value for '--profiles-dir': Path '/tmp/leoflow/dbt' does not exist`,
  while the code comment claimed it was using "the image's baked profiles.yml".
  `--profiles-dir` is now baked at the project when the project ships one and no
  managed connection is configured. Only reads go through it: measured in a real
  image against real dbt, the parse succeeds and `perf_info.json` still lands in
  `/tmp/leoflow/dbt/target`, so `readOnlyRootFilesystem` is unaffected. A managed
  connection still wins — it generates a profile from the operator's credentials
  into `DBT_PROFILES_DIR`, and letting a file checked into the repository
  override that would be a downgrade, so that case is pinned by a test. Applies
  to the top-level `dbt:` block and to `dbt_groups` alike — **and to Lite**,
  where the same path was equally dead: the subprocess executor points
  `DBT_PROFILES_DIR` at an empty per-task scratch dir, so a Lite project shipping
  its own `profiles.yml` failed the same way. The Lite dbt e2e fixture asserts it
  ships none, which is why nobody noticed.

- **`compile` and `deploy --skip-build` no longer bake the operator's own
  absolute path into dbt tasks (#993).** Whether dbt's `--project-dir` is baked
  as an absolute host path or as the relative path inside the image was derived
  from `!--build` — but "we did not build an image this invocation" is a
  different question from "this DAG will run as a subprocess on this host".
  `leoflow compile` without `--build`, and `leoflow deploy --skip-build` (whose
  whole purpose is reusing an image built elsewhere), were both classified local,
  so every dbt task in the resulting `dag.json` carried something like
  `--project-dir /Users/<someone>/work/sales/analytics`. That `dag.json` is what
  gets registered and executed in pods, so the task exits seconds after start
  with "project directory does not exist" and nothing points at the cause — the
  same class as #20, through the door a CI or prebuilt-image workflow actually
  uses. The executor is chosen by server configuration
  (`LEOFLOW_EXECUTOR_TYPE`), not by anything in the `dag.json`, so compile cannot
  infer it; it is now an explicit option that only Lite's subprocess run mode
  sets. **Behavior change:** a bare `leoflow compile` now emits the in-image
  relative path, and no longer writes a parse-time duckdb profile — so a project
  with no managed connection and no `profiles.yml` of its own has no profile for
  `dbt parse` to resolve, and fails unless one is reachable through `~/.dbt` or
  `DBT_PROFILES_DIR`, instead of compiling against a stub for the wrong warehouse. Which `dbt`
  parses the manifest is *not* part of this: that question does not depend on
  where the DAG will run, so the per-DAG venv's `dbt` is preferred whenever the
  host has one, image-bound compiles included. Lite itself is unaffected:
  `leoflow dev` sets the flag.
- **`from leoflow import dbt_group` now resolves inside the task image, so a
  hybrid DAG runs (#17).** A `dag.py` is not a compile-time-only artifact: the
  runtime re-imports the module for every `python` task, so every top-level
  import in the DAG runs again inside the task pod and inside the Lite per-DAG
  venv. `leoflow` existed only in the parser's compile-time shim, which is on
  `sys.path` for the duration of a parse and nowhere else — so the shape the
  docs teach, a `dag.py` with Python tasks around a `dbt_group()` (ADR 0043),
  compiled green and then died on its own first line with
  `ModuleNotFoundError: No module named 'leoflow'`, in Lite and in Pro alike.
  The runtime now ships a real `leoflow` package deriving from the Task SDK's
  `BaseOperator` — which is load-bearing rather than tidiness, because
  `pull >> models` dispatches to the *real* operator's `__rshift__` and would
  never consult a bare stub's. The placeholder raises rather than returning
  quietly if one ever reaches a pod. That is defensive rather than a live failure
  mode — `_operator_type` classifies it as `dbt_group` on its first branch, ahead
  of the check that would emit an operator class, so the compiler cannot produce
  one — but it is not inert either: the runtime's generic operator path
  instantiates an arbitrary dotted class and calls `.execute()` on it, so the
  raise is the last line of defence for a hand-written `dag.json`. Lite's venv
  freshness gate probes both packages, so a venv built before this existed
  reinstalls instead of looking healthy. Nothing caught this because the only
  mixed-mode e2e wires `BashOperator`s around the group, and a bash task never
  imports `dag.py`; a new CI leg installs the Task SDK at the version the base
  image pins and imports the documented example for real, and it hard-checks the
  imports before pytest so it cannot go green by skipping. Packaging is pinned
  down too: hatchling had silently dropped the new package from the wheel by
  resolving the repository's root-anchored `/leoflow` ignore against its own
  root, which the CI leg now catches because it installs the wheel rather than
  setting `PYTHONPATH`.
- **A hybrid DAG's dbt projects are baked into the image (#20).** `generatedDockerfile` branched on the top-level `dbt:` block and
  had no reference to `dbt_groups` at all: for a `dag.py` with dbt task groups —
  the authoring shape ADR 0043 defines — it COPYed only the DAG source. The
  group's tasks then ran `dbt --project-dir <project>` from WORKDIR
  `/home/leoflow` against a directory that was never in the image, so every dbt
  task exited within seconds of pod start. Compile was green and so was Lite,
  because Lite's subprocess executor reads from disk and never needs an image —
  the gap only appeared once something built. Both the DAG source and every
  group's project are COPYed now, deduplicated and **sorted**: `dbt_groups` is a
  map and Go randomizes map iteration, so emitting in range order would give a
  different image digest from an unchanged project, and a cold layer cache every
  time. `project: "."` collapses to a single `COPY . /home/leoflow/`,
  matching the fact that no `--project-dir` is emitted for that value. The
  `leoflow dev` cluster path had the identical gap and gets the identical fix.
  Paths under `dbt_groups` are validated the same way `dbt.project` already was:
  the guard for escaping and absolute paths returned early whenever the
  top-level `dbt:` block was absent — which is every hybrid DAG. Measured against
  real builds: an **absolute** `project:` is the silent one — Docker resolves the
  source against the context root, so `/opt/dbt` builds **green** against
  `<context>/opt/dbt`, a directory nobody named. An escaping `../shared` is loud
  but misleading: the classic builder refuses outright, and BuildKit clamps to
  `<context>/shared` and bakes *that* if it exists — never the sibling Lite
  resolves. Either way the message never mentions `leoflow.yaml`. Two paths still fail after this,
  and are tracked separately: `compile` without `--build` (and `deploy
  --skip-build`) bakes an absolute host path into the dbt entrypoint, and a
  group with no `connection:` cannot resolve a project-baked `profiles.yml`.
  Also corrected while in the file: the `#852` comment claimed the source COPY
  had to sit above the `USER` drop to land root-owned. Measured against a real
  build under both BuildKit and the classic builder, `COPY` lands `uid=0 gid=0`
  whatever `USER` is active; what the ordering
  actually buys is that the **final** `USER` is a **numeric** non-root UID, which
  is what the kubelet resolves at container creation when a task pod sets
  `runAsNonRoot` with no `runAsUser` — the pair `buildSecurityContext` sets.
  PodSecurity admission never reads the image; it checks the PodSpec.

- **HA chart posture: five render refusals for combinations that used to fail
  later, and the ServiceAccount / warm-pool docs the profile was missing.**
  `podDisruptionBudget.enabled` keyed on a real boolean, so the string spellings
  a GitOps tool sends — Argo CD's `helm.parameters`, `helm --set-string` — fell
  through to auto and an operator forcing the budget on a single replica got
  none, silently; `"true"` / `"false"` are now honored and any other non-empty
  value **fails the render** rather than falling back to auto a second time. The
  install notes follow the *spelling* rather than the YAML type, so a
  string-spelled budget on a single replica still prints the warning that it
  blocks every voluntary eviction, and an upgrade no longer credits
  auto-selection for a budget the operator set by hand.
  `logs.persistence.accessMode` was an exact-string denylist, so `""` or a typo
  like `readWriteMany` skipped *both* the single-writer refusal and the
  `Recreate` strategy auto-selection and failed at the apiserver instead; it is
  now an allowlist of `ReadWriteOnce` / `ReadWriteOncePod` / `ReadWriteMany`
  (`ReadOnlyMany` is refused too — the control plane writes task logs), checked
  only when a PVC is actually rendered. Setting **both** `minAvailable` and
  `maxUnavailable` rendered both keys and the apiserver rejects the pair, so it
  now fails the render — reachable now that the budget is default-on for the HA
  replica floor. And `deployment.strategy` becomes an allowlist of
  `RollingUpdate` / `Recreate` / `""`, trimmed before it is compared, with an
  explicit `RollingUpdate` over a single-writer logs PVC refused outright: the
  override defeated the `Recreate` auto-selection and surged straight back into
  the Multi-Attach deadlock, at one replica, with helm reporting success — while
  the exact comparison it used to make let a lowercase `rollingupdate` past both
  the refusal and the auto-selection into an install the apiserver rejected, and
  let `RollingUpdate ` with a trailing space past the refusal into one the
  apiserver **accepted**, delivering the deadlock through the guard. A refusal
  also names a non-string value plainly now instead of rendering it as a Go
  escape sequence or format error. On the docs side,
  `examples/values-ha.yaml` now names **EKS Pod Identity** (AWS's current
  recommendation, which needs *no* annotation) beside IRSA and states that in
  split mode `serviceAccount.annotations` lands on **both** ServiceAccounts and
  both need the identity — the scheduler writes task logs to the bucket, any api
  replica reads them back. The HA page gains a warm-pools section: the
  assignment stream is leader-gated over an in-memory, leader-local registry, so
  a failover leaves the new leader with no pool. Every assignment stream ends at
  `SIGTERM`, not only the idle ones: an idle worker treats that as a clean
  recycle and exits `0`, while a **busy** worker completes its attempt and then
  exits non-zero when its slot-free send hits the dead stream — so expect one
  `Failed` warm pod and one `ERROR` line per busy worker per control-plane
  restart, which is the signal the warm-worker-lost reaper keys on; an attempt
  the pod still held when it died is what that reaper re-places, and in this
  ordering the attempt has usually already settled, so the reaper matters for a
  worker killed MID-attempt rather than for one that failed on its way out. It
  re-places
  the attempts that pod held from the durable `warm_worker_id` binding. A worker
  routed to a follower re-dials with bounded backoff, and warm placement
  degrades to dedicated pods until workers have re-registered. Finally, `terminationGracePeriodSeconds: 0` is documented as
  meaning "the Kubernetes default (30 s) applies" — the chart omits the field
  and deliberately never renders a literal `0`, which would be SIGKILL with
  nothing drained.

- **The `execution_timeout` diagnosis is no longer lost when the report cannot
  be delivered.** Making the agent's clock fire before the kubelet's produced
  the diagnosis, but the diagnosis only travelled on the ReportState RPC: on
  the timeout path the agent reported `FAILED` with `execution_timeout: task
  exceeded Ns limit` while its **durable outcome record** — the
  termination-message document the reconciler prefers over pod phase — carried
  only the exit code. So when the control plane was unreachable across the
  timeout, or the kubelet's `SIGTERM` landed mid-report-retry, the report never
  arrived and the reconciler settled the attempt from the record, serving the
  generic `task failed (exit 255)`. (255, not 137: the agent's own cancel kills
  the child, a signal death reports exit code `-1`, and the agent clamps that to
  255 — so 255 is the value to grep a timed-out attempt for.) The durable
  channel existed and already preferred a record's reason; it just never carried
  one. The record now carries the classification **alongside** the exit code
  (new `taskoutcome.FailedBecauseWith`), so the reason survives a report that
  never lands.

  Scope: every failure the agent classifies **itself** and can name — the
  timeout it enforced, a refused external secret backend, and outputs the task
  produced but the agent could not deliver (which without a classification
  rendered as `task failed (exit 0)`, a string that reads as a success). The
  record's reason is a **classification**, never an error's text: the agent maps
  a failure to one of a closed set of constants and records **no reason at all**
  for a failure it recognizes nothing in, so an unclassified environment-build
  error — the XCom fetch's wrapped gRPC error carries the control-plane endpoint
  and TLS handshake text, and the per-attempt `TMPDIR` failure carries a path —
  cannot reach a durable field that is served on the task-instance API and
  readable by anyone with pod read access in the task namespace. An ordinary
  non-zero exit also records no reason: the record's own `task failed (exit N)`
  rendering is the better string. On the read side the reconciler now bounds a
  record's reason like it already bounds every other reason it reports, because
  the termination message is a file the task's own process can write before it
  is killed and the kubelet's ceiling there is ~4 KiB against a 240-byte cap.

  New `test/e2e/execution-timeout-e2e.sh` (`make e2e-timeout`) locks the whole
  seam on a real k3d cluster — the only place a real kubelet stamps
  `pod.Status.StartTime` — asserting that a task declaring
  `execution_timeout_seconds: 10` whose body sleeps far past it settles with
  `execution_timeout:` in the `failure_reason` the API serves and with a pod
  whose `status.reason` is *not* `DeadlineExceeded`, that its pod's durable
  record carries the same diagnosis, that a pod created through the real
  dispatch path carries `activeDeadlineSeconds` equal to the declared timeout
  plus the startup headroom plus the effective termination grace, and that an
  agent frozen after `RUNNING` is settled by the agent-lost reaper inside the
  window the ladder itself implies (70-150 s) instead of lingering to the pod
  deadline.
  ([#930](https://github.com/neochaotic/leoflow/issues/930))
- **A partial resource-defaults pair now fails loudly instead of silently
  suppressing the platform default.** `defaults.resources` in `leoflow.yaml` and
  `executor.defaults.resources_*` in the chart both expand one quantity into a
  request *and* a limit, and both are documented as landing a task that declares
  no resources of its own in Guaranteed QoS. Neither holds for a *partial* pair,
  and the partial case was worse than merely missing a QoS class: a
  `defaults.resources` with only `cpu` (or only `memory`) still converts to a
  non-nil resources object, the empty dimension is dropped from the pod spec, and
  because the object is non-nil the dispatcher's fallback to the per-cluster
  platform default never runs — so the task got one dimension pinned and the
  other with **no request or limit from anywhere**, worse configured than with no
  defaults block at all. `defaults.resources` now **requires both `cpu` and
  `memory`**: the break surfaces on the author's machine at compile time with the
  missing field named (`at '/defaults/resources': missing property 'memory'`),
  since the requests-equals-limits expansion is the block's only documented
  purpose and half of it is meaningless. On the operator side, the control plane
  now logs a boot `WARN` when exactly one of
  `executor.defaults.resources_cpu` / `_memory` is set, naming both keys, the
  Burstable class actually reached and the dimension nothing else will supply,
  with `config_key` / `missing_config_key` / `value` as fields so it can be
  alerted on; the chart comment, the rendered chart README, the configuration
  reference and the schema description no longer promise Guaranteed for a partial
  pair. **The pod-spec behaviour is unchanged in this release** — a partial pair
  still suppresses the platform default wholesale rather than merging per field,
  and the same wholesale suppression is reachable without any `defaults` block at
  all: a task declaring only an `ephemeral_storage` limit (a standalone knob
  [ADR 0054](https://dexaflow.dexadata.ai/project/adrs/0054-shared-cluster-coexistence/)
  promotes) makes the resources object non-nil and therefore drops the platform
  cpu and memory defaults entirely, leaving a pod with an ephemeral-storage limit
  and no cpu or memory request at all. Only cpu and memory are QoS compute
  resources, so that pod is **BestEffort** — schedulable anywhere, invisible to
  autoscaler capacity math, and first evicted under pressure. Field-granular merge is
  deliberately **not** done here: it belongs at dispatch, server-side, where the
  cluster default is known — and changing how the dispatcher composes a pod spec
  mid-release alters the resource footprint of every task that has a partial
  block, which can leave pods `Pending` on a cluster with no headroom. That
  needs a cluster pass, not a release patch
  ([#802](https://github.com/neochaotic/leoflow/issues/802)). (Doing it in the
  CLI at compile time is separately impossible — it cannot know the cluster's
  default, and baking one in would destroy the portability of the compiled
  artifact the chart promises.)

- **The installation guide no longer claims `networkPolicy.enabled` restricts
  task pods, or that it restricts egress at all.** Production hardening told
  operators to set `networkPolicy.enabled=true` "to restrict the control plane
  and task pods to only the flows they need", and both halves of that sentence
  were wrong. Task pods are governed by a *different* value,
  `taskNetworkPolicy.enabled`, which defaults to `false` — so an operator who
  followed the section to the letter believed the network-layer containment
  [ADR 0048](https://dexaflow.dexadata.ai/project/adrs/0048-no-user-code-in-control-plane/)
  leans on was in place and had none. And the control-plane policy restricts
  **ingress only**: its `networkPolicy.egress` is empty by default and the
  chart then renders a single empty egress *rule* (`- {}`), which matches every
  destination — deliberately, so enabling the policy cannot silently break
  Postgres / Redis / kube-apiserver access. The bullet is now
  split per value, states the task policy's `false` default and the
  `blockPrivateNetworks` / `allowMetadataEgress` escape hatches (the latter is
  why the policy is opt-in: both clouds serve keyless workload identity from the
  always-blocked link-local range), and says plainly that neither policy is a
  control without a CNI that enforces it. The install NOTES now carry a WARNING
  while `taskNetworkPolicy.enabled` is false, so the deliberately-off default is
  visible at the moment the operator can act on it. The task policy's default is
  **unchanged**: it always blocks `169.254.0.0/16`, so defaulting it on would
  break keyless external-secrets auth for anyone who does not know to add a
  single-address exception, and its enforcement is CNI-dependent and invisible to
  this project's gates — a default-on policy that silently does nothing on a
  large share of installs is a worse posture than an opt-in one that is honestly
  labelled ([#804](https://github.com/neochaotic/leoflow/issues/804)).

- **Token renewal now re-proves the user, not just the token (#801).**
  `POST /api/v2/auth/token/renew` validated the incoming bearer, checked the
  session ceiling, and then re-minted the principal purely from the token's
  claims — it never reloaded the user, unlike every other auth path. A user
  deactivated or deleted mid-session therefore kept getting `200` and a fresh
  token from the renew endpoint until `auth.jwt.max_lifetime_seconds` elapsed.
  Renewal now reloads the user and answers `401` when the account is inactive or
  its row is gone. **Severity, precisely:** this was a broken invariant and a
  false changelog claim, not an access path. The renewed token was inert — both
  consumers of a user token authenticate through the store-backed reload, so it
  was rejected on use — and no product path deactivates a user today (the users
  API exposes only list and create), so reaching the precondition required
  editing the database by hand. The two carve-outs the request path already makes
  are preserved exactly: no bound data plane (in-process `leoflow dev`) and the
  dev-token subject, which has no user row by design; any other store error
  refuses the renewal. The renew route remains without a rate limiter — the
  existing login limiter counts failures toward an IP lockout, so sharing it
  would let renewal traffic burn the password-login budget for the address (and
  for everyone behind a proxy when `server.trusted_proxies` is unset); a renewal
  limiter needs its own instance and is tracked separately.
- **Three registered settings that were written down nowhere, plus a guard for
  the next one (#801).** `auth.jwt.max_lifetime_seconds` — the ceiling that
  bounds a transparently renewed session, i.e. the control that makes renewal
  acceptable — and `secrets.backend` / `secrets.backend_kwargs` bound from the
  environment but appeared in no settings table in the configuration reference,
  so an operator had no documented way to reach them. All three now have rows
  (`secrets.*` gets its own section). `TestDocumentedEnvVarsBind` could not catch
  this: it only runs doc → binding. The new reverse guard runs binding → doc, so
  registering a key without documenting it now fails the build (same class as
  #725 / #733 / #743).
- **The pod-lost reaper no longer reaps a task whose pod is still there,
  finished.** Its liveness question returned one bool for two different states —
  "no pod for this attempt at all" and "a pod that exists in a terminal phase" —
  and it read either as authorization to reap: it marked the task instance
  `pod_lost` and then deleted the pod, destroying the termination log the
  reconciler recovers the attempt's durable outcome from (so a task that had
  SUCCEEDED read as failed, and its run with it). Pod liveness is now
  three-valued and pod-lost defers on a present-but-finished pod, metering a
  `pod_lost_terminal_pod_defer` decision, reaping only when the apiserver holds
  no pod for the attempt. This holds independently of the leader-settling gate,
  its grace and the reconciler cadence: an absence is a state no timing can
  manufacture, so the settling gate's liveness valve — which opens after 2 ×
  grace precisely so a broken reconciler sweep cannot wedge the reapers — can no
  longer surface the reap it was narrowing. Behavior on an unreadable apiserver
  is unchanged and now locked by a test: pod-lost defers with
  `pod_lost_pod_query_error`, which is also the reason the valve is safe to open
  (the reconciler's pod LIST and the reaper's own hit the same apiserver, so
  whatever stops the sweep also denies the reaper its authorization). The
  dispatch-lost reaper's behavior is unchanged in this fix, and that reaper can
  still delete a terminal pod's outcome record: a queued attempt's finished pod
  CAN carry an outcome (the settle statements admit `queued`, and a dispatch can
  stamp a row that already reached `running` back to `queued`). Closed one level
  down by the teardown fix below
  ([#928](https://github.com/neochaotic/leoflow/issues/928)), with the underlying
  unguarded transition tracked as
  [#929](https://github.com/neochaotic/leoflow/issues/929).
- **A reaper's pod teardown no longer deletes a task pod that already reached a
  terminal phase.** Deleting one stops no container — there is none — and
  destroys the durable outcome record on its termination message, the only
  evidence the reconciler can settle the attempt from (ADR 0052). Three reapers
  still reached that delete after the pod-lost *decision* was fixed: the
  agent-lost reaper fires on heartbeat staleness alone at a **90 s** threshold
  with no pod read at all (and a task that finished and stopped heartbeating is
  precisely its candidate), the orphan-run reaper deletes every pod of a run at
  5 minutes with no pod read, and the dispatch-lost reaper defers only on a
  *live* pod. The guard now sits at the one delete site instead of in four
  decision paths, so all five reapers are evidence-preserving and **no reaper's
  mark or decision changes**; the run-scoped delete applies it per pod inside the
  run, tearing down the run's still-live containers while its finished pods keep
  their records. Collecting a terminal pod is the reconciler's job — it settles
  each one and garbage-collects it past the 10-minute grace — and a skip is
  logged at INFO by pod name and phase, so an operator separates "left for the
  reconciler" from the `*_pod_delete_error` a failed delete already meters. A pod
  in phase `Unknown` is still deleted: it may yet be running a container, which
  is what teardown exists for, and the reconciler treats `Unknown` as
  non-terminal, so nothing else would collect it.
  ([#928](https://github.com/neochaotic/leoflow/issues/928))

- **A task pod is no longer killed by the kubelet before its own
  `execution_timeout` can fire, mislabelling the failure.** A task that declared
  `execution_timeout_seconds` got a pod `activeDeadlineSeconds` equal to that
  value — but the kubelet counts it from the pod's `status.startTime`, stamped
  before the image pull, while the agent starts its own timeout clock only inside
  its execute step: after the image pull, the volume attach and mount, container
  start, its token bootstrap and exchange, the gRPC dial, `Register`,
  `GetTaskSpec`, the environment build (XCom fan-in and secret resolution) and
  its `RUNNING` report, whose retry has no budget of its own. The kubelet
  therefore won by that entire startup cost — image pull first among them — on
  **every** timing-out task rather than as an occasional race, and the task
  settled with a generic "the task container terminated…" reason instead of
  `execution_timeout: task exceeded Ns limit`. The pod deadline is now the
  declared timeout **plus** a 3-minute startup headroom (the dispatch-lost
  threshold — the window in which the control plane still presumes healthy
  startup and defers reaping) **plus** up to 60 s of the pod's
  `terminationGracePeriodSeconds` (30 s when undeclared), so the agent's clock
  fires first and its diagnosis is the one recorded. One limit is worth stating
  rather than discovering: the agent kills its direct child and then waits for
  the task's output pipe to close, so a task that leaves a live grandchild
  holding that pipe — any compound shell entrypoint, or a Python task that
  spawns a subprocess — keeps the agent's wait open past its own deadline. That
  task still ends on the kubelet's deadline with the generic reason and no
  outcome record, exactly as before. The guarantee therefore holds for a task
  whose process tree exits with it; the remaining shape is tracked in
  [#943](https://github.com/neochaotic/leoflow/issues/943). The grace is added on
  top of the headroom rather than instead of it: it covers the shutdown tail, not
  the startup head. It is capped because the declaration is unvalidated — adding
  an hour of declared grace verbatim would put the deadline an hour past the
  declared timeout while the kubelet grants that same hour of `SIGTERM` grace
  again on top; the pod spec still carries the declared value verbatim. The sum
  is clamped to `math.MaxInt32`, the largest `activeDeadlineSeconds` the
  apiserver accepts, so a declared timeout near that bound is still a pod whose
  deadline is never reached rather than one rejected at `CREATE`. A pod whose
  agent is dead can now outlive its declared timeout by those few minutes — that
  is the backstop, and the reapers still settle the task instance on their own
  schedule — and a pathological image pull can still exceed any fixed headroom,
  in which case the kubelet's generic reason returns. Unchanged: a declared
  timeout still wins over the `auth.max_attempt_credential_lifetime` floor, and
  that floor (for tasks that declare no timeout) takes no headroom, because
  nothing inside the pod races it; warm-pool tasks are unaffected either way,
  since warm pods carry no `activeDeadlineSeconds` and are bounded per attempt by
  the warm worker's attempt watchdog. The *Scheduler resilience* page now spells
  out which clock owns the timeout.

- **A control-plane restart no longer marks a succeeded task failed, and no
  longer condemns the rest of its run.** A production drill (kill the
  control-plane pod mid-run) turned a dbt task that had SUCCEEDED into `failed`
  and failed the whole run. Five compounding causes, each now fixed:
  - The agent abandoned a terminal report after 6 attempts (~31s) — shorter than
    a control-plane restart — while its heartbeat loop kept going indefinitely.
    The report now retries until its context ends (SIGTERM, pod delete, execution
    timeout), with the pause between attempts capped at the heartbeat interval so
    it reconnects within about one heartbeat plus the gRPC channel's own
    reconnect backoff once the server returns. Credential rejections are still
    returned at once. The RUNNING pre-flight report takes the same path, on
    purpose: an agent that cannot reach the control plane does not start user
    code until it can. Because both reports now outlast an outage, every task
    pod also gets an `activeDeadlineSeconds` floor when its DAG declares no
    execution timeout — derived from `auth.max_attempt_credential_lifetime`,
    past which the pod cannot renew its credential — so no task pod is left
    Running forever after a total control-plane outage (unless the ceiling is
    disabled, which also disables this floor). A user-declared timeout is
    never shortened. The floor is load-bearing only when the control plane
    never returns: if it comes back after the token has lapsed, the rejected
    bearer already makes the agent exit promptly. It makes explicit a bound
    that held in practice before — the ceiling was a de-facto attempt-lifetime
    cap of about ceiling + token TTL + the agent-lost threshold + one
    maintenance cycle (30s, since the reapers run from that loop and not from
    the scheduler tick, and up to two intervals when a cycle overruns its
    phase budgets), and the settling grace on top when a leader re-election
    intervenes — up to twice that grace if the new leader never settles and
    the liveness valve has to open.
  - The pod-lost reaper (every scheduler tick) raced the reconciler (every 30s)
    for a pod that finished during the outage, and won — marking it `pod_lost`
    before the reconciler could recover the pod's durable outcome record. It now
    honors the same post-leadership grace as the agent-lost reaper, so the
    durable outcome is recovered first.
  - A downstream task was persisted `upstream_failed` — a terminal state nothing
    reverts — while its upstream was infra-failed but still re-placeable (parked
    in its re-place backoff). Downstream tasks now wait until an upstream is
    TERMINALLY failed (retries and infra re-place exhausted).
  - Reapers could mark or delete during the SIGTERM drain or a leader step-down.
    Every destructive reaper action is now gated on a live context, no step-down
    in progress, and (when wired) current leadership; the successor redoes the
    reap under its own grace.
  - The timing ladder the recovery depends on is validated at server boot:
    `heartbeat < agent-lost threshold < agent-lost grace < token TTL`, two
    `reconcile interval`s below both post-leadership graces (one is not enough
    to guarantee a completed sweep), the scheduler's longest infra re-place
    delay below the orphan-run threshold, and the token TTL below
    `auth.max_attempt_credential_lifetime`. That last rung is the only one an
    operator can move — hardening the ceiling below the TTL would silently
    disable heartbeat renewal and with it the whole recovery — so its failure
    names the config key; every other rung is a build-time constant and its
    failure asks for a bug report.

- **Connection/variable writes are now tri-state, restoring explicit-clear and
  fixing the masked write-back overwrite (#887, subsumes #874).** The safe-merge
  upsert (v0.4.4) preserved any omitted field, but because the API request body
  used plain strings it could not tell "field omitted" (preserve) from "field
  present and empty" (clear), so `connections set c --login ""` was a silent
  no-op and a masked secret (`***`) read back from a GET and re-submitted — as
  the Admin UI does — overwrote the real secret with the literal mask. The write
  body is now tri-state: an omitted field preserves the stored value, an explicit
  empty string clears it, and a secret field equal to the mask (a password, or
  any key inside `extra` whose value is exactly `***`, or a sensitive-keyed
  variable value) is treated as unchanged and preserved. `leoflow connections set`
  can now clear a field by passing it as an empty string (e.g. `--login ''`)
  instead of "delete and recreate", and re-saving a connection/variable read
  from the API no longer clobbers its unreadable secrets.
- **A control-plane eviction no longer drops the part of an attempt's log written before it.** Two
  coupled defects made the recommended HA log path (object storage) lose the
  complete log of every attempt running at the moment of an eviction. The object
  sink held a whole attempt in memory and wrote it as one object on close, and
  the shutdown path ran an unbounded gRPC graceful stop that waited on the
  agents' open log streams — held open for the whole task — so with any task
  running the pod burned its entire `terminationGracePeriodSeconds`, was
  `SIGKILL`ed, and the deferred close never ran. Now the object sink rewrites the
  stored object incrementally (whenever 1 MiB is unflushed, or every 5 s while
  anything is, damped for very large logs so a big attempt uploads about nine
  times its size in total rather than quadratically), so a hard kill loses at
  most each attempt's unflushed tail; open log streams are closed and flushed as
  soon as `SIGTERM` arrives (the agent keeps running its task — log shipping is
  best-effort); and the gRPC graceful stop is bounded at 5 s with a forced-stop
  fallback that still lets the remaining handlers finish their flushes, so a
  normal shutdown completes in well under a second instead of ending in
  `SIGKILL` (the bounded worst case, measured from `SIGTERM`, is ~35 s: raise the
  grace above that plus any `deployment.preStopSleepSeconds`, which runs inside
  the same grace but before the signal, when running the object sink at scale —
  the HA profile ships 60). The final flush runs on a context detached from the server's
  lifecycle, which `SIGTERM` cancels at exactly that moment. The disk sink (Lite,
  RWX PVC) is unchanged. Known gap, documented: the agent does not re-open a log
  stream its control plane closed, so lines a task prints after its control
  plane went away are not shipped by that task.

- **A shutdown with warm pools enabled no longer always ends in a forced gRPC
  stop, and a failed log flush is no longer invisible.** Follow-ups to the fix
  above. An idle warm worker holds its assignment stream open indefinitely by
  design, and that handler did not watch the shutdown signal, so with
  `execution.warm_pools_enabled` the bounded graceful stop exhausted its whole
  budget on *every* shutdown: the forced fallback became the normal path and the
  `agent grpc graceful stop exceeded its bound` warning fired every time, which
  is exactly how a warning stops being read. The assignment stream now ends at
  `SIGTERM` with the same `Unavailable` the log stream uses — the code the forced
  transport close already produced — and an **idle** warm worker now treats that
  as a clean recycle (exit 0) instead of a fatal receive error, so a control-plane
  restart no longer leaves one `Failed` pod and one ERROR line per warm worker
  behind. Only the idle receive path accepts it: an attempt that dies mid-flight,
  or a genuine outage, still surfaces as a failure. The
  object sink's retry warning for a failed incremental flush went to Go's default
  `slog` handler rather than the configured one, landing as plain text on stderr
  outside the server's log format and level, where nothing collecting the control
  plane's logs would see it; the sink now takes the configured logger at
  construction, and the server additionally points Go's package-level `slog` at
  the same handler, which covers the ~two dozen bare `slog` calls in the agent
  RPC layer (token-review rejections, secret-liveness denials, a failed final
  flush) that nothing injects into. The bounded stop also logs how many agent handlers it left
  running when it gives up: each log-object `Put` is bounded at 30 s while the
  wait after the forced stop is 5 s, so abandoning one is possible — and safe,
  since a single atomic `Put` leaves the stored object at its previous flush
  rather than truncated — but it was silent. And a log stream that
  *arrives* after `SIGTERM` is now refused before its writer is opened: the gRPC
  listener is the last thing the process stops, so it keeps accepting for the
  rest of the shutdown, and a writer opened there was closed immediately —
  storing an **empty** object, which in this sink means "the attempt ran and was
  silent" for an attempt that was really logging to a live replica. New chart
  value `deployment.preStopSleepSeconds` (default `0`, off) makes a terminating
  replica sleep through the endpoint-propagation window before it is signalled,
  so task pods open their *new* log streams against a replica that will still be
  there to flush them. It is off by default because it needs somewhere to move
  those streams to, and neither shipped topology has a second replica serving
  agent gRPC; `examples/values-ha.yaml`, which does, sets `5`. It uses the native
  `sleep` hook action (the image is distroless, so an `exec` hook has no shell to
  call), which is beta and on by default only from Kubernetes **1.30** — alpha
  and off in 1.29, where an apiserver rejects the empty `preStop: {}` it is left
  with rather than ignoring it — so the chart renders the hook only on 1.30+;
  below that the pod spec is unchanged and the install notes say the value had no
  effect. It runs inside
  `terminationGracePeriodSeconds`, and the chart refuses to render a sleep that
  does not fit the effective grace: the apiserver rejects a sleep above the grace
  (it validates `0 < seconds <= terminationGracePeriodSeconds`), and the chart
  refuses one equal to the grace too, which the apiserver would accept but which
  leaves nothing for the shutdown that runs after the sleep.

### Security

- **`golang.org/x/crypto` bumped to v0.56.0** (from v0.55.0), which fixes two
  advisories published on 2026-09-03 (GO-2026-6354, GO-2026-6355). `govulncheck`
  finds no reachable call path from leoflow into the affected symbols; the bump
  keeps the dependency scan clean and the fix in place regardless.

## [0.4.4] - 2026-09-03

### Security

- **Bumped `google.golang.org/grpc` to v1.83.1 (CVE-2026-84304, HIGH).** The
  control plane ↔ agent RPC layer used gRPC-Go v1.83.0, which a HIGH advisory
  covers; v1.83.1 is the upstream fix. `govulncheck` reports the vulnerable
  symbol is not reachable from Leoflow's call graph, so no released build was
  exploitable, but the dependency is patched to clear the advisory outright
  rather than carry a known-fixable HIGH in the RPC layer.

### Added

- **First-class `leoflow connections` and `leoflow variables` CLI groups (#881).**
  The Lite inner loop (and Pro) can now manage connections and variables directly
  — `set` (upsert), `list`, `get`, `delete` — instead of hand-rolling `curl`
  against `/api/v2`; the error for an undeclared connection already pointed at
  `leoflow connections set`, which now exists. Secrets are sent on write but never
  printed back (list/get omit secret columns; the server masks `extra` and
  sensitive-keyed values), and `--password-stdin` / `--extra-file` keep a secret
  off argv and out of shell history. `set` is a **safe merge**: only the fields
  you pass change, so a partial `set --host newhost` no longer wipes the omitted
  (and unreadable) password — the control-plane upsert now preserves any field a
  write omits rather than clobbering it (this also fixes the same latent overwrite
  on the Admin UI and PATCH write paths). To clear a field, delete and recreate.

### Security

- **A Lite dbt task no longer writes its generated `profiles.yml` into the project
  (#12).** In Lite the dbt profile step defaulted its output dir to the process
  CWD — the dbt project in the user's working tree — so the generated
  `profiles.yml`, which carries the managed connection's secret in clear, could
  overwrite the repo's version-controlled `profiles.yml`: one `git add` from
  committing a live credential. The Lite executor now injects the same private
  `DBT_PROFILES_DIR`/`DBT_TARGET_PATH`/`DBT_LOG_PATH` scratch the pod base image
  provides, and the runtime never falls back to the CWD — the profile lands in a
  private per-task dir, never the project.
- **Sensitive values in a connection's `extra` are masked in read API responses
  (#11).** `GET /api/v2/connections` (list and by-id) returned the free-form
  `extra` verbatim, so provider secrets that ride there — OAuth `client_secret`,
  PATs/`token`, `private_key`, BigQuery `keyfile_dict` — were echoed in clear (the
  `password` field was already withheld). Secret-bearing keys are now redacted to
  `***` on read, matching the Variable masking; non-secret keys (host, http_path,
  account, schema, method) are preserved.

### Changed

- **The control-plane Helm defaults no longer let the kubelet kill the server
  under load (#860).** The liveness/readiness probes are now exposed in
  `values.yaml` (`probes.liveness` / `probes.readiness`) and default to a
  forgiving 5s liveness / 3s readiness timeout with `failureThreshold: 3` —
  instead of Kubernetes' implicit 1s/3, which a busy scheduler could miss during a
  task-pod burst and be restarted mid-run (cascading in-flight tasks to
  agent_lost). Default CPU/memory requests are raised to `250m`/`256Mi` so the
  server keeps enough headroom to answer probes while fanning out task pods.
- **The control-plane Deployment update strategy is exposed and defaults safely
  (#868).** A rolling upgrade surges a second pod that Multi-Attach-deadlocks on a
  ReadWriteOnce logs PVC, so an upgrade never converged (operators worked around
  it with an in-cluster patch). `deployment.strategy` is now settable, and when
  unset it defaults to `Recreate` whenever `logs.persistence` is an RWO PVC (and
  `RollingUpdate` for RWX or an ephemeral emptyDir).

### Fixed

- **A control-plane restart no longer fails healthy in-flight tasks (#858).**
  The agent-lost reaper failed any task whose last heartbeat was older than the
  threshold — but a control-plane restart (rollout, node drain, kubelet kill)
  makes *every* in-flight task look stale, because the process that records
  heartbeats is the one that was down. The new leader would then mass-reap
  healthy, still-running tasks. The reaper now honors a post-leadership **grace
  window** (2× the agent-lost threshold, 180s) measured from when this instance
  acquired leadership, so the fleet has time to re-heartbeat before any reap; a
  genuinely lost agent is still reaped once the grace elapses.
- **Infra re-placement is now backed off and jittered to avoid a thundering herd
  (#859).** When a task fails for an infrastructure reason (agent/pod/dispatch
  lost) it re-places without consuming its retry budget — but it did so
  immediately, so a mass infra fault (a restart marking a whole run agent_lost at
  once) re-dispatched every sibling on the same tick, stampeding the just-
  recovered kube-apiserver and re-throttling the scheduler. Re-placement now
  waits out an exponential backoff from the failure time (the same curve as a
  synchronous dispatch failure, keyed on the infra-attempt count) plus a
  deterministic per-task jitter, so siblings spread across a window instead of
  firing together.
- **A task killed by the agent-lost reaper now ends its log with a marker
  (#861).** When the control plane fails a task whose agent went silent, it
  deletes the pod and the log stream stops — previously leaving the task log
  truncated mid-line (e.g. `Running with dbt…`), indistinguishable from a hang.
  The reaper now appends a final `killed: agent_lost (last heartbeat …)` line to
  the reaped attempt's log before tearing the pod down, so the reason is visible
  where the operator tails it. Best-effort: the marker never blocks the reap.

## [0.4.3] - 2026-09-01

### Added

- **Task pods default to the operator's task ServiceAccount (#844).** A new
  `executor.task_service_account` (Helm: `taskServiceAccount`) is applied to every
  task pod — and to warm-pool pods — when a DAG does not pin its own, so cloud
  workload identity (EKS IRSA, GKE Workload Identity, AKS) works without wiring a
  ServiceAccount into each task. A task that pins a different SA still wins and is
  never placed on an incompatible warm pod (#853).
- **`leoflow dags list` (#842).** Lists the registered DAGs from the CLI, matching
  the operability of `leoflow runs`.
- **`leoflow login --password-stdin` (#838).** Reads the password from stdin so it
  never lands in shell history or a process listing, mirroring `docker login`.

### Changed

- **The in-pod agent baked into the runtime base image is now version-stamped.**
  `leoflow-agent version` in a task pod reports the version, commit, and build date
  it was built from (the same `-ldflags -X` as the release binaries), instead of an
  empty/`none` build. This restores commit-level traceability for the task pod and
  makes a control-plane↔agent version skew diagnosable. The runtime-image release
  job already publishes an immutable per-release tag (`py<ver>-<release>`) alongside
  the moving `py<ver>` line.
- **Airflow Task SDK bumped to 3.3.x (`apache-airflow-task-sdk==1.3.1`).** DAGs
  now run under the Airflow 3.3 Task SDK in the task pod and the `leoflow lite`
  dev venv; the 3.3 SDK surface is additive over 3.2 (no removed `airflow.sdk`
  exports), and the airflow-free parser shim is unaffected. The parser's optional
  real-Airflow backend accepts `apache-airflow>=3.2,<3.4`. The embedded web UI
  stays on the Airflow 3.2.x SPA and `/api/v2/` remains 3.2.x-compatible — the SPA
  upgrade is tracked separately. Validated on the pod-path e2e against a 3.3.1
  runtime image.
- **`leoflow compile` pins the immutable per-release task base image by default
  (#851).** A build from a released CLI now `FROM`s the per-release base tag
  (`py<ver>-v<X.Y.Z>`) instead of the moving `py<ver>` line, so a DAG image built
  today rebuilds byte-for-byte later; a dev/dirty CLI still tracks the moving tag,
  and an explicit `base_image` in `leoflow.yaml` always wins.

### Fixed

- **A database outage during login returned 401 "invalid credentials" instead of a
  5xx (#843).** `IssueToken` collapsed every credential-store error into
  `ErrInvalidCredentials`, so an unreachable DB read as "wrong password" — sending
  operators to chase a password problem during an outage and masking the incident.
  A genuine not-found now stays `ErrInvalidCredentials` (401, no user enumeration);
  any other backend error propagates and the login endpoint answers **503
  "authentication temporarily unavailable"** without consuming the per-IP lockout
  budget. The 503 leaks nothing (returned regardless of whether the account exists).

- **dbt managed connection now works under enforce scoping and an external
  secrets backend.** The dbt compiler wraps each task with the profile step that
  reads `AIRFLOW_CONN_<conn>`, but did not **declare** the managed connection — so
  the agent injected it only under permissive scoping (the whole vault). Under
  enforce scoping or the external secrets resolver (which deliver declared names
  only) the connection was missing and the task failed "not delivered". The
  compiler now declares it on every dbt task, across all warehouse adapters
  (postgres, snowflake, bigquery, databricks, duckdb). **Behavior change:** a dbt
  DAG with `connection:` now validates that connection at registration (it must
  exist, or be covered by an external backend) — create the connection before
  deploying, the same order any declared connection follows.
- **External secrets resolver failed against a real provider backend (ADR 0060).**
  Importing and initialising an Airflow provider secrets backend emits log lines
  on stdout (structlog, alembic, the secrets masker). The resolver's stdout is the
  strict-JSON channel the in-pod agent parses, so that logging corrupted the
  result and the agent rejected it as malformed, failing the task closed. The
  resolver now isolates its stdout: provider logging is redirected to stderr for
  the duration of the resolve and only the JSON result reaches stdout. The backend
  ships off by default (added in 0.4.2), so no released configuration was affected.
  Locked by a subprocess regression test and a new pod-path end-to-end test that
  resolves a declared connection and variable from an emulated Secrets Manager
  (LocalStack) in a real task pod.
- **A per-task `connections:` override dropped a dbt task's managed connection
  (#848).** Overriding a dbt task's `connections:` in `leoflow.yaml` replaced the
  compiler-declared profile connection, so the profile step failed "not delivered".
  The override now keeps the managed connection and adds the extra ones.
- **dbt task failures were hard to diagnose (#838).** A failing dbt invocation now
  surfaces its stderr in the task log instead of a bare non-zero exit, so a
  compile/model error is visible from the run.
- **A synthesized dbt image could not find `dbt` and left the project writable
  (#852).** The generated Dockerfile installed dependencies as the base's non-root
  user, so dbt's console script landed off `PATH` (`dbt: command not found`) and an
  apt step failed. Dependencies now install as root (scripts on `PATH`), the task
  drops back to the non-root UID before the source `COPY` (the project stays
  read-only), and dbt writes `profiles.yml`, `target/`, and logs to `/tmp` — the
  base image points `DBT_PROFILES_DIR`/`DBT_TARGET_PATH`/`DBT_LOG_PATH` there, and
  `profiles.yml` is written `0600`.
- **A missing declared secret under an external backend was hard to diagnose
  (#842).** When the resolver is configured, an unresolved declared Connection or
  Variable now logs the specific declared names that came back empty (scoped to the
  backend's coverage), pointing at the pod identity / backend permissions instead
  of failing silently. A non-email login username also gets a one-line hint (the
  login is the email address).

### Docs

- **Cluster-validation runbook for the native secrets resolver (#841)** — EKS/GKE
  keyless (IRSA / Pod Identity / Workload Identity) validation steps.
- **The task ServiceAccount auto-default applies only with
  `taskServiceAccount.create=true` (#849)**, with a bring-your-own-SA note in the
  chart README.

## [0.4.2] - 2026-08-31

### Added

- **Typed trigger form in the UI (#798).** The "Trigger DAG w/ config" dialog
  renders a generated form from a DAG's declared `params=`, with the raw JSON
  editor still one toggle away.
- **External secrets resolver (#811, ADR 0060) — off by default.** A declared
  Connection/Variable can be resolved pod-side from a provider store (AWS Secrets
  Manager reference; GCP / Azure / Vault via config) under the pod's own keyless
  identity, instead of the Leoflow vault. Operator-configured (`secrets.backend`
  + Helm); declaration-scoped, fail-closed, no copy at rest. Keyless end-to-end is
  validated on a real cluster (EKS/GKE RC) before enabling; the vault stays the
  only source when unset.

### Changed

- **`deferrable=True` rejected at compile time (#794, ADR 0016).** A clear error
  points to `deferrable=False` / `mode="reschedule"` instead of compiling a task
  that cannot defer at runtime.
- **OpenTelemetry tracing is off by default** — opt in via `observability.otel`.

### Security

- **OIDC break-glass is SSO-only (#827).** Under `provider: oidc`, an empty
  break-glass allowlist now denies all password logins instead of leaving
  `POST /auth/token` open.
- **Author task env can no longer override reserved `LEOFLOW_` variables
  (GHSA-3r74-9w27-v32f).** Closes an in-pod agent control-plane / credential
  redirect via a DAG's `env:`.
- **OIDC hardening (#827):** dotted tenant/group config keys, returning-user
  tenant-mismatch reject, rate-limited login/callback, and fail-closed handling of
  Entra group-claim overage.

### Fixed

- **Flaky scheduler alert-dedup test (#609).**

### Docs

- **Versioned documentation site with per-release archives (#814).**
- **External secrets how-to** (`operate/external-secrets.md`).

## [0.4.1] - 2026-08-28

### Added

- **`leoflow runs logs <dag> <run> <task> [--try N] [-f]` (#768).** Read a task
  attempt's logs from the CLI — it streams the existing task-instance logs endpoint
  (the same logs the UI shows), so a failed task's output is reachable without the
  UI. `--try` defaults to the latest attempt; `-f` follows a running task.
- **Airflow-3 alias imports accepted (#786).** `from airflow.decorators import task`
  and `from airflow import DAG` now resolve to the Task SDK (`airflow.sdk`), so a
  canonical Airflow-3 DAG compiles as-is. Legacy `airflow.operators.*` imports get a
  clear error naming the `airflow.providers.standard.operators.*` replacement (they
  were removed from core in Airflow 3.0), rather than a mistranslation.
- **Trigger-time DAG parameters (#545).** `leoflow runs trigger --conf '{…}'` /
  `--conf-file` supplies a run's `conf`, which reaches tasks as `{{ params.x }}`;
  declared `params=` (Airflow's typed `Param`) are captured, defaulted, and validated
  at trigger time (a schema violation is a 400), and a `Param` with no default is
  required (a trigger that omits it is a 400).
- **DAG scheduling/metadata attributes honored (#797).** `max_active_runs`, `catchup`,
  `start_date`, and `description` set on the `DAG(...)` are now captured and honored
  instead of silently dropped.
- **How-to: trigger a run from an external system (#799).** A copy-paste REST flow
  (obtain a JWT → `POST …/dagRuns` with `conf` → poll the run) for an external
  integrator.

### Fixed

- **`leoflow deploy --build` now works for a pure-dbt DAG (#769).** The synthesized
  Dockerfile assumed a `dag.py` and failed the build (`COPY dag.py`) for a dbt-only
  project; it now copies the dbt project (and its baked manifest) instead. A DAG
  shipping its own Dockerfile is unaffected.
- **A task refused for running as root now says so (#766).** A `CreateContainerConfigError`
  from the non-root admission (`runAsNonRoot`) previously left the task polling and
  its `failure_reason` generic; it now settles `failed` and names the cause + the
  fix (end the image with a numeric `USER 65532`, or set `taskPodSecurity.runAsNonRoot=false`).
- **Warm pools no longer place a staging DAG on a warm worker (#788, ADR 0058 D5).**
  A staging attempt could be assigned to a reused warm pod that carries no per-run
  `/staging` mount, silently diverging the run's shared-volume data; staging versions
  now always take a dedicated pod and are excluded from the warm reconciler. Only
  reachable with warm pools enabled (`min_idle > 0`), off by default.
- **`@task.branch` fails with a clear error, not an opaque `AttributeError` (#809).**
  The TaskFlow branch spelling now reports that branching is not supported yet
  (ADR 0040 Phase D), matching how `BranchPythonOperator` is already refused, instead
  of a confusing import-time crash.
- **Warm-pool docs lead with the safe default and the dormant `min_idle_workers`
  seam is documented honestly (#789).** The enable guide now leads with
  `minIdleWorkers: 0` (scale-to-zero) and the not-yet-author-settable field is marked
  dormant rather than reading as usable.
- **Docs corrections (#790, #795).** Fixed broken cross-references/anchors, and the
  stale claim that reschedule-mode sensors were unsupported (they are); deferrable
  operators are recorded as a conscious non-goal (ADR 0016) with the reschedule
  alternative.

### Security

- **The `leoflow-server` control-plane image is now cosign-signed (#772),** closing
  the one published image that was unsigned (migrate + runtime already were). A
  release-gate step verifies the server signature and drafts the release if it is
  missing.
- **Release image signing retries transient OIDC flakes (#773).** Every `cosign sign`
  now retries with backoff, so a momentary Sigstore/OIDC blip no longer drafts an
  otherwise-good release.

### Docs

- **External secrets how-to (#812).** Reaching a credential that lives in AWS
  Secrets Manager / GCP Secret Manager / Azure Key Vault / HashiCorp Vault from a
  task pod — keyless (Workload Identity) first, then an ESO/CSI-synced or mounted
  Kubernetes Secret — without duplicating it in Leoflow, plus how a secret is
  scoped to the right pod.
- **The pod model — "when you pay for a pod" (#785).** Pod-per-task and the levers
  that avoid its cold-start cost (the subprocess dev executor, `dbt_group`, warm
  pools); records the generic fused-`TaskGroup` as a deliberate, deferred non-goal.
- **Deferrable operators recorded as a conscious non-goal (ADR 0016, #795),** with
  reschedule-mode sensors documented as the supported alternative.

## [0.4.0] - 2026-08-26

> Leoflow's biggest release to date. The headline work: **warm worker pools**
> (N:1 pod reuse across attempts, ADR 0058), first-class support for a
> **shared, multi-team Kubernetes cluster** (named task pools, per-DAG
> admission, a shared-informer read path, full placement/DRA passthrough, and
> quota/APF backpressure that no longer burns a task's retry budget — ADR
> 0053/0054), **RBAC + OIDC/SSO** with fail-closed tenant pinning, **per-task
> secret scoping and short-lived agent credentials** (the mechanism ADR 0045
> left unshipped, ADR 0055), a **durable task-outcome** model that stops
> pod-death-mid-report from reading as a false failure (ADR 0051/0052), and a
> native **S3/GCS object log sink** (ADR 0056). Below that sits the rc.1→rc.4
> hardening batch (Helm-reachability fixes, diagnostics, CLI polish).

### Added

#### Warm worker pools — N:1 pod reuse across attempts (ADR 0058)

- **A task pod can now be reused across attempts instead of spawned fresh for
  every one.** A warm worker's agent runs in a long-lived fork-per-attempt
  mode (#676) over a dedicated gRPC assignment transport with ack/lease
  semantics (#673), wired into scheduler dispatch (#675). Off by default —
  `execution.warm_pools_enabled` defaults to `false`, a byte-for-byte no-op —
  and the control plane **fails closed at boot** if it is turned on without
  the ADR 0058 D2 security prerequisites already in force: the projected-SA
  token-exchange transport and enforce-mode secret liveness (#671).
- **Pool lifecycle.** A min-idle reconciler provisions warm pods and never
  deletes a busy worker (#677, busy-aware fix #681, terminal-pod-skip fix
  #678); each attempt gets a durable attempt→worker binding persisted on ack
  (#679), backed by a failover reaper and double-run guard so two agents can
  never claim the same binding (#680); an idle-slot reclaim path unstrands a
  worker and fast-re-places its next attempt (#682); and every warm worker
  enforces its own lifecycle — max attempts, max lifetime, an idle TTL, a
  drain path, and an attempt watchdog (#683).
- **Guardrails.** A per-tenant aggregate cap on warm pods, reserved and
  rationed rather than first-come (#686); a per-DAG-version ConfigMap
  GC-anchor so a warm pod can't outlive the DAG version it was spawned for
  (#687); and a worker-scoped bootstrap token exchange so a warm pod never
  holds a long-lived plaintext credential across the attempts it serves
  (#689).
- **Reachable from the Helm chart, with the same fail-closed guard the server
  enforces (#695)** — see the full entry below.

#### Shared-cluster / multi-team Kubernetes (ADR 0053, ADR 0054)

- **Leoflow can now share a cluster without starving its neighbors or letting
  them starve it.** A per-DAG `max_active_tasks` admission gate caps how many
  of one DAG's tasks may be queued/running at once (#632), and named,
  cross-DAG task pools (Pro-only) add a second, budget-based admission gate on
  top of it (#642) — replacing the long-standing `/api/v2/pools` stub. A
  shared pod informer replaces the reapers' per-second `LIST` storm and the
  reconciler's 30-second `LIST` with one long-lived watch (#634).
- **Placement and scheduling passthrough.** Declared tolerations (#621) and
  the rest of the placement surface — `priorityClassName`,
  `terminationGracePeriodSeconds`, `runtimeClassName`, topology-spread
  constraints, affinity, DRA resource claims, ephemeral-storage, and custom
  labels/annotations (#630) — now actually reach the pod spec instead of
  being accepted at push and silently dropped.
- **Quota/APF backpressure is no longer billed to the task's retry budget.** A
  `ResourceQuota` 403 or an API Priority & Fairness 429 on pod create now
  retries forever without incrementing `dispatch_attempts`, so a fan-out under
  a tight quota self-throttles instead of producing a wave of false
  `dispatch_failed` terminal states (#631).

#### Identity & access

- **RBAC foundation.** A seeded viewer/editor/operator role ladder, with roles
  and permissions reloaded from the store on every token verify — so a
  deactivated or deleted user loses access immediately, not at token TTL —
  and nav menus filtered to what the caller can actually do (#654).
- **OIDC/SSO with fail-closed tenant pinning (ADR 0057), Pro-gated.**
  Authorization Code + PKCE against an external IdP (Entra ID / Google Cloud
  Identity / Okta and any standard OIDC provider), JIT-provisioned users, and
  every login reconciling the user's roles to the IdP-mapped set — a pin
  failure is a 403, never a default-identity fallback (#656).
- **User management.** `POST /api/v2/users` to create an account and
  `GET /api/v2/users` to list them, admin-gated (#627, #649); `leoflow admin
  users list` (#651); and a new `leoflow admin` operator CLI —
  `health`/`dags pause`/`drain`/`runs list` — for operating a running Pro
  control plane over the typed client (#635).

#### Secret scoping & agent credentials (ADR 0055 — the ADR 0045 follow-up)

- **The mechanism to stop every task from receiving the whole tenant vault.**
  ADR 0045 was accepted but never shipped; this is that code. A declared-secret
  schema lets a DAG or task name the variables/connections it actually needs
  (#660), narrowing is enforced by a scope-by-policy gate paired with a
  task-liveness check that stops a superseded or finished attempt's still-valid
  token from resolving secrets (#663), and a task that is narrowly declared but
  would still receive the full vault now gets a warning (#661). **Ships in
  observe/permissive mode by default — nothing is denied yet** until an
  operator flips it to enforce. Agent credentials also got shorter-lived:
  a per-attempt token is now renewed on every heartbeat instead of living for
  the whole attempt (#665), and a projected-ServiceAccount token-exchange
  transport replaces the plaintext-env-var credential, flag-gated off (#667).

#### Durable execution (ADR 0051, ADR 0052)

- **A task's true outcome is no longer conflated with whether its report
  survived.** If a pod dies mid-report (OOM, eviction) after the task itself
  succeeded, the reconciler can now recover that success from a durable
  outcome record instead of settling it as a failure (#615). Infra faults —
  a lost agent, pod, or dispatch — now re-place the task without consuming
  its own retry budget, bounded instead by a separate infra-attempt limit, so
  an exhausted infra budget with retries remaining still finalizes rather
  than hanging (#614). The four backstop reapers and the pod lifecycle moved
  behind a dedicated execution seam ahead of warm pools, with no behavior
  change (#669, #670).

#### Object log sink (ADR 0056)

- **Task logs can now be written straight to S3 or GCS instead of an
  in-cluster PVC.** A native dual-SDK backend (`aws-sdk-go-v2` for S3/MinIO,
  the native `cloud.google.com/go/storage` client for GCS — not the S3
  interop endpoint, which cannot use Workload Identity) is keyless-first via
  IRSA or GKE Workload Identity. Off by default; the on-disk sink is
  unchanged for every deployment that does not opt in (#640).

#### Deploy & MCP

- **`helm install` no longer requires cert-manager just to turn on agent
  TLS.** The chart now auto-generates a stable self-signed CA and gRPC server
  cert by default (`agentTLS.autoGenerate`), reusing the same CA across `helm
  upgrade` so it never silently rotates and breaks running agents; bring-your-
  own/cert-manager remains the opt-in production path (#690, #692).
  - The Helm chart is now published as a signed OCI artifact on every release
    and RC tag (`oci://ghcr.io/neochaotic/charts/leoflow`) (#691).
- **MCP `search_logs` can search a whole run, not just one known task.**
  `task_id` is now optional — when omitted, the tool enumerates the run's
  task instances and searches each one, tagging every match with the
  `task_id`/`try_number` it came from (#612).

#### Diagnostics & CLI

- **Transparent CLI token renewal — no more hourly re-login (#755).** The CLI now
  silently refreshes a file-persisted session token once it passes the halfway
  point of its life (rewriting `~/.leoflow/config.yaml`), so long sessions no
  longer hit `401 missing bearer token` every hour. Backed by a new
  `POST /api/v2/auth/token/renew` that re-mints the same identity with a fresh
  short TTL, bounded by `auth.jwt.max_lifetime_seconds` (default 24h). The
  access-token TTL is unchanged and revocation is enforced per request, so a
  renewed token is rejected the moment its user is deactivated. (Corrected: as
  shipped, revocation was enforced on *use* but not on *issuance* — the renew
  endpoint itself kept answering `200` for a deactivated user until the ceiling
  elapsed. Fixed later; see the token-renewal entry under Fixed above.)
- **`leoflow runs list` (#747).** The common `runs` verb now lists DAG runs
  (`--state`/`--dag`/`--older-than`) alongside `trigger` and `status`, reusing the
  same lister as `leoflow admin runs list`.
- **A failed task now says why, even when it produced no logs (#698).** When a
  task pod died before its agent ever registered with the control plane — bad
  RBAC, a TLS trust failure, a network policy, image-pull auth, or an OOM at
  startup — the API reported `"state": "failed"`, `"hostname": "unknown"` and no
  cause of any kind, and the UI showed `No logs available for this attempt.`
  (correct, since nothing ever streamed). Diagnosing it required `kubectl logs`
  against the cluster, so an operator without cluster access could not diagnose
  it at all. Three changes close that blind spot:
  - Task instances now expose a **`failure_reason`** field (a Leoflow extension;
    the `/api/v2/` Airflow surface is unchanged and purely additive). The cause
    was already recorded in `task_instances.error_message` by the reapers and the
    reconciler, but was dropped before it reached the domain model or the API.
  - The agent now classifies a failure it hits **before registering** and leaves
    it on the container termination message, which the reconciler already reads —
    so a refused bootstrap token exchange, an unreachable control plane, or an
    unreadable projected token becomes a reason on the attempt instead of a
    silently discarded server-side log line. The reason is drawn from a closed
    set of operator-facing classifications; a raw internal error or a credential
    is never echoed into this end-user-visible field.
  - Failures only Kubernetes can see are described precisely rather than as a
    bare `pod failed`: the task container's terminated reason and exit code
    (including `OOMKilled`), the pod-level reason (e.g. `Evicted`), and the
    unrecoverable waiting reasons (`ImagePullBackOff`, `CreateContainerError`, …)
    now carry the context that makes them actionable.

  The reason is also appended to the otherwise-empty log view as an error-level
  event, so the cause appears where an operator is already looking. It is
  best-effort and diagnostic only: it is null when nothing observed a cause, it
  is bounded in length, and it does not change retry accounting or any state
  transition.
- **Warm worker pools are reachable from the Helm chart (#695).** The server has
  supported them since ADR 0058, but the chart exposed none of the knobs and had
  no escape hatch, so the only way to enable the feature on a Helm-installed
  release was `kubectl set env` behind Helm's back — an operator following the
  docs could not reach it at all. New values: `execution.warmPoolsEnabled`,
  `execution.minIdleWorkers`, `execution.maxPoolSize`,
  `execution.maxAttemptsPerWorker`, `execution.maxWorkerLifetime`,
  `execution.workerIdleTtl`, `execution.maxWarmPodsPerTenant`,
  `auth.agentTokenTransport` and `auth.secretLivenessMode`, plus a general
  `extraEnv` list for `LEOFLOW_*` settings the chart does not model. Every
  default equals the server's, so an unchanged `helm upgrade` keeps today's
  behavior: warm pools off, `envvar` transport, liveness in `observe`. The chart
  now **refuses to render** when warm pools are enabled without their ADR 0058 D2
  security prerequisites (`agentTokenTransport=exchange` **and**
  `secretLivenessMode=enforce`), mirroring the server's fail-closed boot check —
  a clear render error instead of a `CrashLoopBackOff` explained only in
  container logs.

### Fixed

- **Duplicate `dag_version` push returns 409, not a raw 500 (#746).** Pushing a
  version that already exists with different content (the common
  `tag_strategy: version`→`dev` dev loop) collided on the unique constraint and
  surfaced `500 internal error` leaking the Postgres SQLSTATE/constraint name. It
  now returns **409 Conflict** with no internal details; an identical re-push is
  still an idempotent no-op.
- **`leoflow.yaml` `defaults.resources` and `defaults.node_selector` now apply to
  tasks.** Both were accepted by the schema but silently dropped, so DAG-wide
  resource/placement defaults never reached the pod (and a DAG relying on
  `defaults.resources` for Guaranteed QoS silently got BestEffort). They are now
  applied as a per-task fallback at compile time (a task's own settings still
  win), and unknown keys in the `defaults` block now fail loudly at compile
  instead of being ignored.
- **`read_only_task_root_filesystem` no longer prevents a pod from starting
  (#741).** The hardening flag left no writable `/tmp`, so a read-only-rootfs task
  — and especially a warm worker, which died before registering — could not run. A
  writable `emptyDir` is now mounted at `/tmp` on task and warm pods whenever the
  flag is set (default pods are unchanged), making the mitigation the warm-pool
  docs recommend actually usable.
- **Managed CPython is version-checked and never silently falls back to an
  unsupported interpreter (#742).** `leoflow` re-installs the managed interpreter
  when its pinned version changes (via a `.py-version` sentinel, like managed
  Postgres), prefers the checksum-verified build over a host `python3.11`, and
  rejects an unsupported interpreter with a "run `leoflow setup`" hint instead of
  proceeding on system `python3`. Archive extraction is hardened against
  interrupted and cross-version upgrades.
- **`executor.defaults.staging_size` and `executor.defaults.staging_storage_class`
  are now reachable in a Helm install (#743).** Like the earlier
  `trusted_proxies`/resource-defaults fix, these keys were absent from the config
  defaults so their `LEOFLOW_*` env vars never bound; they are now registered and
  exposed as chart values.
- **The chart version is gated against the release tag, and the chart is published
  to OCI (#748).** A pre-tag check fails the release when `Chart.yaml`
  `version`/`appVersion` lags the tag (which made a default `helm install` pull the
  previous release's images); the OCI chart publish
  (`oci://ghcr.io/neochaotic/charts/leoflow`) is now gated behind that check and
  documented.
- **Docs tooling cleaned up after the Hugo migration (#744).** The `ci-local`
  docs gate now runs `hugo --gc --minify` instead of the removed MkDocs build,
  `CONTRIBUTING` points at a template that exists again, and the CI path filters
  account for `website/`.
- **Secret-scope audit events are now recorded (#722).** `RecordSecretScopeWarning`
  and `RecordSecretLivenessDenial` resolved the tenant argument by name, but the
  agent RPC path calls them with the tenant **UUID** carried by the agent token —
  so every ADR 0055 audit row failed silently with `resource not found`, including
  the enforce-mode `secret.liveness_denied` security record. Both now resolve by
  id, so the observe-mode readiness trail and the enforce-mode denials are
  actually persisted rather than existing only as log lines.
- **A retried task instance no longer wedges in `queued`/`running` (#723).** The
  reaper liveness check (`TaskPodActive` and its cache fast-path `CachedPodActive`)
  matched a pod by `(run, task)` only, so a lingering earlier-attempt pod made the
  dispatch-lost and pod-lost reapers defer forever once a retry bumped
  `try_number`. Both checks now pin `leoflow.io/try-number`, asking liveness about
  the attempt being failed rather than any pod for the task.
- **`server.trusted_proxies` and `executor.defaults.resources_*` are now
  reachable from a Helm install (#725).** These keys were absent from
  `serverDefaults`, and viper's `AutomaticEnv` only binds an env var for a key it
  has seen via `SetDefault` — so `LEOFLOW_SERVER_TRUSTED_PROXIES` and
  `LEOFLOW_EXECUTOR_DEFAULTS_RESOURCES_CPU`/`_MEMORY` were silently dropped. The
  chart ships no server config file, so env is the only override path, which left
  two production defaults unconfigurable: with trusted proxies unset the login
  rate-limiter keyed every request on the ingress IP, so a handful of bad logins
  locked out the whole deployment; and with the resource defaults unset a task
  that relied on them ran BestEffort (first evicted under node pressure). The
  keys are now registered (`redis.ca_file` was missing from the same map and is
  fixed too), `trusted_proxies` binds from a comma-separated env var (viper
  splits it into a list), and the L0 resource default now sets **both** requests
  and limits so such tasks reach Guaranteed QoS. The chart exposes
  `config.trustedProxies` and `executor.defaults.resources`.
- **The api role no longer receives the gRPC TLS private key in split mode
  (#726).** The `grpc-tls` volume was gated only on `agentTLS.enabled`, so the
  internet-facing api Deployment mounted the whole cert Secret — including
  `tls.key` — although only the scheduler serves the agent gRPC listener. The
  volume and mount are now scoped to the scheduler role (the TLS env vars stay on
  both roles so the Pro boot guard still passes), keeping the private key off the
  api pod (ADR 0049).
- **Warm-pool attempts now get a fresh `TMPDIR` (#728).** A warm worker reused one
  pod across attempts but never redirected `TMPDIR`, so anything a task wrote under
  `/tmp` (a dbt profile, a cached credential) persisted to the next attempt on the
  same worker — a filesystem channel around the per-task secret scoping. Each
  attempt now gets a `TMPDIR` inside the per-attempt scratch that is wiped between
  runs; the warm-pools doc is corrected to state the actual isolation guarantee
  (image-level paths and `$HOME` still persist — use `read_only_task_root_filesystem`).
- **Repository validation errors now return 400, not 500 (#724).** A DAG version
  declaring an unknown variable or connection produced `500 internal error`
  (reading as a server fault) because `handleRepoError` had no
  `domain.ErrValidation` branch. It now maps validation errors to `400 invalid
  request`, so the actionable message points the user at `leoflow connections set`
  instead of the server logs.
- **The migration Job no longer mounts an unused ServiceAccount token (#727).** The
  Job talks only to Postgres but received the namespace default SA's projected
  token; it now sets `automountServiceAccountToken: false`, matching the task-pod
  path.
- **`leoflow lite --postgres managed` is now idempotent across upgrades (#729).**
  Re-extracting the managed Postgres bundle failed with `file exists` because
  symlink extraction was not idempotent; it now removes an existing target before
  creating the link. The managed-Postgres guard is also version-aware (keyed on the
  bundled Postgres version), so a newer bundle is re-installed on upgrade instead
  of silently keeping the old one.
- **Chart RBAC now grants the `tokenreviews` permission the token-exchange
  transport requires (#696).** With `auth.agentTokenTransport=exchange` the
  control plane validates a task pod's projected ServiceAccount token via the
  Kubernetes TokenReview API, but the chart granted only a namespaced Role —
  and TokenReview is cluster-scoped, so no Role could ever carry it. The review
  was refused (`tokenreviews.authentication.k8s.io is forbidden ... at the
  cluster scope`) and every task pod died on `projected token is not valid`.
  Because the transport is server-wide, this broke the ordinary dedicated
  pod-per-task path too, not only warm pools — which made it a hard blocker for
  warm pools, whose D2 prerequisites force the transport on. The chart now
  renders a ClusterRole + ClusterRoleBinding granting `create` on
  `authentication.k8s.io/tokenreviews`, named `<fullname>-<namespace>-tokenreview`
  so multiple releases in one cluster cannot collide, bound to the same
  ServiceAccount as the executor Role (the scheduler SA in split mode). It is
  rendered **only** on the `exchange` transport: `create` on `tokenreviews` is an
  authentication oracle, and an install on the default transport never issues the
  call. `scripts/rbac-covers-executor.sh` now checks this grant against the code
  the same way it checks the executor's, so the two cannot drift apart again.
- **`leoflow runs trigger`, `leoflow runs status` and `leoflow dags delete` now
  use the token saved by `leoflow auth login`.** These commands read the server
  URL from `~/.leoflow/config.yaml` but resolved the bearer token only from an
  explicit `--token` or `LEOFLOW_TOKEN`, so a user who had just logged in
  successfully got `401 missing bearer token`. They now share the same
  `--token` → `LEOFLOW_TOKEN` → config-file precedence that `push`, `deploy` and
  the `admin` subcommands already applied. (#697)
- **The control plane now fails closed at boot when the `exchange` agent-token
  transport has no Kubernetes client (#700).** It previously logged a warning and
  left the exchange unwired, so with `auth.agent_token_transport=exchange` every
  task pod's bootstrap failed `Unimplemented` while `/readyz` stayed green. It now
  refuses to start with a clear error naming the missing in-cluster/kubeconfig
  client, so the misconfiguration is visible at boot instead of silently failing
  every task.
- **`leoflow dev` no longer panics on a nil context while probing the companion
  binary version (#699).** The probe received `cmd.Context()`, which is nil when
  the command was not run through cobra's `Execute()`, and `context.WithTimeout`
  panicked `cannot create context from nil parent`. It now routes through the
  package's nil-guarding context helper and falls back to a background context —
  a best-effort probe must never crash the CLI.

### Changed

- **Task pods now run as non-root by default (behavior change; breaking for root
  task images).** The default for `taskPodSecurity.runAsNonRoot`
  (`executor.defaults.run_tasks_as_non_root`) flips `false → true`, and the task
  base image's UID is renumbered `1000 → 65532` (distroless `nonroot`, matching
  the control-plane and migration pods) — this unblocks Pod Security Admission
  `restricted`. On upgrade, any existing task image that runs as root will fail to
  start with `CreateContainerConfigError` ("container has runAsNonRoot and image
  will run as root"), because the pod sets `runAsNonRoot=true` without pinning
  `runAsUser`. **Remediation for a fleet with root task images:** set
  `taskPodSecurity.runAsNonRoot=false` (cluster-wide — pod security is
  intentionally not a per-DAG setting), or rebuild those images to run as a
  non-root user. The staging PVC stays writable for any non-root UID via the
  pod's supplementary `fsGroup`.

## [0.3.0] - 2026-08-13

> Promotes the **0.3.0** release line (`v0.3.0-rc.1` → `v0.3.0-rc.4`) to stable.
> The candidate was validated end-to-end on the published artifact: checksum +
> cosign signature verification, a binary-only install (managed Postgres, no repo
> or `PYTHONPATH`), the full UI journey (trigger, failure-path traceback,
> connection CRUD with encryption verified at the database), the `leoflow-mcp`
> server over stdio, and a dbt-duckdb run. See the `0.3.0-rc.1` … `0.3.0-rc.4`
> sections below for the full change list. The only change since `rc.4` is the
> front-door documentation fix below.

### Documentation

- **Front-door docs fixed for the v0.3.0 line.** The README's flagship example
  imported `from leoflow import DAG, task` (which does not parse — the shim only
  exports `dbt_group`); it now uses `from airflow.sdk import DAG, task`. Broken
  README links to `docs/api-reference.md` and `docs/helm-chart.md` now point to
  the published API reference and `helm/leoflow/README.md`. Install commands no
  longer pin the removed `v0.0.1*`/pre-alpha tags. Pro's readiness is now stated
  consistently across the README, index, quickstart, editions, and operating-modes
  pages ("Helm-installable and in active validation"), and the Status section
  reflects v0.3.0 (dbt, `leoflow-mcp`, `pkg/client`). (#601, #602, #603)

## [0.3.0-rc.4] - 2026-08-13

> Fourth RC of the **0.3.0** line — a one-line CLI consistency fix over `rc.3`:
> `leoflow --version` (flag) now works, matching the `version` subcommand and the
> companion binaries. No control-plane, MCP, dbt, or UI change since `rc.3`.

### Fixed

- **`leoflow --version` (flag) now works, matching the `version` subcommand and
  the companion binaries.** Previously the root CLI accepted only `leoflow
  version`; `leoflow --version` errored with `unknown flag`. The flag now prints
  the same build info and exits 0. (#598)

## [0.3.0-rc.3] - 2026-08-13

> Third RC of the **0.3.0** line — operability polish surfaced by the `rc.2`
> soak. The companion binaries (`leoflow-server`/`-agent`/`-mcp`) now answer
> `--version` and `--help` without a runnable config, and the embedded UI's
> documentation links resolve (the Airflow VersionInfo endpoint reports the
> pinned Airflow UI version, not leoflow's build version). No control-plane,
> MCP, or dbt functional change since `rc.2`. Docker-free gates green locally;
> full CI battery gated the cut.

### Fixed

- **`leoflow-server`, `leoflow-agent`, and `leoflow-mcp` now answer `--version`
  and `--help`.** Previously only the main `leoflow` CLI could report its
  version, and the companion binaries responded to `--version`/`--help` by
  trying to boot and erroring on missing config or an unreachable control plane.
  They now print their build version (`--version`) or a usage message
  (`--help`) and exit 0 before any config load or network connect, so an
  operator can identify and inspect a deployed binary. (#593)
- **The UI's "Learn more" documentation links no longer 404.** `GET
  /api/v2/version` (the Airflow VersionInfo endpoint the embedded SPA reads to
  build `https://airflow.apache.org/docs/apache-airflow/<version>/…` links)
  reported leoflow's build version, so the SPA pointed at a nonexistent Airflow
  docs release. It now reports the pinned Airflow UI version. leoflow's own
  version remains on the CLI, health endpoints, and the MCP
  `health://control-plane` resource. (#594)

## [0.3.0-rc.2] - 2026-08-13

> Second RC of the **0.3.0** line — a fix-and-polish pass over `rc.1`. Repairs two
> distribution bugs the `rc.1` soak surfaced (the **`leoflow-mcp`** binary is now
> built and shipped in the release archives; a fresh **binary-only install** —
> `leoflow compile` and `leoflow lite` — works without a repo or `PYTHONPATH`), and
> closes the discoverability gap on the new dbt cloud auth: the **connection form
> now surfaces** the Snowflake key-pair, BigQuery keyless, and Databricks OAuth M2M
> fields, with matching docs. No functional change to the MCP server or the
> control plane since `rc.1`. Docker-free gates green locally; full CI battery
> gated the cut.

### Added

- **The connection form now surfaces dbt cloud-auth fields with inline help.**
  The three modern service-account auth modes shipped in 0.3.0-rc.1 were only
  reachable by hand-typing keys into the raw `extra` JSON box, because the form
  catalog is generated from Airflow's provider introspection, which does not
  describe them. A hand-curated leoflow overlay
  (`internal/connectors/catalog.overlay.json`, merged over the generated catalog
  at load) adds them as labeled fields: Snowflake `private_key_passphrase`,
  BigQuery's keyless `method` selector, and the full Databricks form
  (`http_path`, `client_id`/`client_secret`, `auth_type`, plus a labeled
  workspace URL and access-token field — Airflow shipped it empty). The generated
  `catalog.json` is untouched; the overlay survives regeneration.

### Documentation

- **dbt: the warehouse-connection section now documents modern cloud auth.**
  `docs/dbt.md` §4 described only the legacy password/key-file modes; it now has
  a per-warehouse table (Snowflake key-pair, BigQuery keyless, Databricks OAuth
  M2M) and links to the connection reference pages that carry the full setup.

### Fixed

- **Fresh binary-only install: `leoflow compile` and `leoflow lite` now work
  without a repo or `PYTHONPATH`.** The primary "download the release and run
  it" path was broken: the binary extracts the Python parser to
  `~/.leoflow/pysrc/parser`, but spawned it as a bare `python3 -m leoflow_parser`
  without putting that directory on `PYTHONPATH`, so compile failed with
  `No module named leoflow_parser`. And the Lite boot referenced the extracted
  `pysrc/runtime/python` (to build the per-DAG venv) *before* anything extracted
  it, aborting with `does not exist`. The parser invocation now always wires the
  extracted sources onto `PYTHONPATH`, and the Lite boot self-heals the
  extraction before provisioning the venv. Every existing e2e job preset
  `PYTHONPATH=parser` (repo-relative), which masked both — a new
  binary-only-install CI gate exercises the genuine path (built binary, clean
  HOME, no repo, no `PYTHONPATH`). (#587)
- **The `leoflow-mcp` binary is now built and shipped in the release archives.**
  GoReleaser built `leoflow`/`leoflow-server`/`leoflow-agent` but not
  `leoflow-mcp`, so v0.3.0-rc.1 shipped the MCP server as source with no
  distributable binary. It now builds for the same platform matrix, carries its
  version via the `main.version` ldflag, and is included in the per-platform
  archive. (#586)

## [0.3.0-rc.1] - 2026-08-12

> First RC of the **0.3.0** line. Adds the experimental **`leoflow-mcp`** Model
> Context Protocol server (read tools + resources over stdio and Streamable HTTP,
> ADR 0050) and a typed **`pkg/client`** for `/api/v2`; brings **dbt** cloud-adapter
> auth up to modern service-account standards (Databricks OAuth M2M, Snowflake
> key-pair, BigQuery keyless), a dbt-aware `diagnose_run`, and adapter contract
> tests; plus security hardening (seccomp, trusted-proxy default, `/metrics`
> isolation). Docker-free gates green locally; full CI battery green on the tagged
> commit (`f3ded10`).

### Added

- **`diagnose_run` (MCP) is now dbt-aware and shows downstream impact.** For each
  failed task it additionally surfaces the tasks it transitively **blocks**
  (`downstream_blocked`, from the DAG's `depends_on` graph) and, for a dbt task,
  the **models** it runs (`models`, parsed from the task's `--select` — a single
  model at `node` granularity, several for a fused group). The summary now
  distinguishes root failures from the tasks they blocked. Best-effort from the
  compiled spec — a run with no failures pays nothing extra, and a spec that can't
  be read just omits the fields. No control-plane change (built on the existing
  `dag://spec`).
- **Databricks dbt profiles support OAuth M2M (service principal).** A managed
  Databricks connection whose extra carries `client_id`/`client_secret` (or an
  explicit `auth_type: oauth`) now generates a `profiles.yml` using service-principal
  OAuth M2M — Databricks' recommended auth for automation — instead of a Personal
  Access Token. PATs keep working; OAuth wins when present (the two are mutually
  exclusive). Same encrypted-at-rest, generated-in-pod delivery as before.
- **Snowflake dbt profiles support key-pair auth (service account).** A managed
  Snowflake connection whose extra carries `private_key_content` (inline PEM) or
  `private_key_file` (path) — with an optional `private_key_passphrase` — now
  generates a `profiles.yml` using key-pair auth instead of a password. Passwords
  keep working; key-pair wins when a key is present (password/token dropped).
  Prefer it for automation — Snowflake is deprecating single-factor password auth.
- **BigQuery dbt profiles support keyless auth (Workload Identity).** Setting
  `method: oauth` in a `google_cloud_platform` connection's extra now generates a
  BigQuery `profiles.yml` that authenticates via Application Default Credentials
  (GKE Workload Identity on Pro) — no service-account key file is shipped.
  `keyfile_dict` (inline key) still works for the service-account-json method.

### Testing

- **dbt adapter contract tests (Snowflake / BigQuery / Databricks).** CI now feeds
  the `profiles.yml` Leoflow emits for each cloud warehouse through that warehouse's
  *real* dbt adapter credential parsing — validating field names, alias resolution,
  required fields, and each auth mode (key-pair / keyless / OAuth M2M) without a live
  query or credentials (`dbt-adapter-contracts` matrix). This is the credential-free
  half of cloud-adapter assurance; a live-query gate remains maintainer-owned (needs
  warehouse secrets). See the new "Adapter assurance" section in docs/dbt.md.

### Documentation

- **dbt: fused-group retry trade-off** — documented that retrying a `level`/`folder`
  (fused) group re-runs the whole group, with guidance to use `granularity: node`
  where per-model retry efficiency matters.
- **dbt: baked manifest & Slim CI** — documented where the compiled `manifest.json`
  lives and how it enables `state:modified+` Slim CI, plus an honest note on the two
  capabilities still needed for a turnkey recipe.

- **Experimental MCP server skeleton (`leoflow-mcp`)** — the first slice of the
  Model Context Protocol server (ADR 0050): a read-only, stdio server built on
  the official `modelcontextprotocol/go-sdk`, exposing `list_dags`, `diagnose_run`, and `search_logs` (one call: run state + failed tasks +
  each failed task's truncated, sanitized log tail) — reaching the control plane
  only through `pkg/client` (the caller's token is
  passed through; the server holds no privilege of its own). Optional and never
  part of `leoflow-server`. It also serves addressable read resources —
  `dag://list`, `run://detail/{dag_id}/{run_id}`, `task://instances/{dag_id}/{run_id}`,
  `log://task/{dag_id}/{run_id}/{task_id}/{try_number}` (truncated + sanitized),
  `dag://source/{dag_id}` (the dag.py text), `dag://spec/{dag_id}` (the compiled
  dag.json artifact, via a new GET /api/v2/dags/{dag_id}/spec endpoint), and
  `health://control-plane` (component health + executor + version). Beyond stdio
  it also speaks **Streamable HTTP** (`--transport http`, `POST /mcp`, stateless)
  as an optional Pro service, where each request carries its own bearer and the
  server mints a per-request control-plane client from it — the token
  pass-through the Pro deployment relies on, with no ambient privilege. Authoring
  follows.
- **A generated, typed Go client for the `/api/v2` surface (`pkg/client`)** — the
  single control-plane client the CLI, the coming MCP server, and smoke tests
  share instead of hand-rolling HTTP (ADR 0050). Generated from the OpenAPI spec
  with `oapi-codegen` (`make pkg-client`); CI fails on drift. The public OpenAPI
  spec now carries an `operationId` on every operation, so any consumer's code
  generator produces stable, idiomatic method names (`ListDagRuns`, `GetTaskLogs`,
  …). `leoflow auth create-token` / `login` now go through this client.

### Security

- **Control-plane and migration-Job pods now set `seccompProfile: RuntimeDefault`**
  (audit follow-up). The chart hardened these pods with `runAsNonRoot`, dropped
  capabilities, and `allowPrivilegeEscalation: false`, and a comment claimed a
  RuntimeDefault seccomp profile — but it was never rendered, so a cluster
  enforcing the `restricted` Pod Security Standard would reject the pods. The
  profile is now set at the pod level (inherited by every container) on the api,
  scheduler, monolith, and migration-Job pods. Task pods already carried it.
- **The API now trusts no proxy by default** (`server.trusted_proxies`,
  `LEOFLOW_SERVER_TRUSTED_PROXIES`). Previously `X-Forwarded-For` was honored
  from any source, so the client IP behind the login rate-limiter and the audit
  log could be spoofed. The client IP is now the direct peer unless you list the
  reverse proxy's IP/CIDR. **Behavior change** for deployments behind an ingress:
  set `server.trusted_proxies` to the ingress CIDR to keep per-client
  rate-limiting and audit accurate. See
  [Configuration → Trusted proxies](docs/configuration.md).
- **`/metrics` is no longer served on the public API/UI listener; `/readyz`
  no longer leaks dependency errors** (audit H2). Prometheus `/metrics` was
  exposed unauthenticated on the API port in addition to the dedicated metrics
  listener; it is now served **only** on the observability listener (the metrics
  port, which every role runs), so it can be firewalled separately from the API.
  And a failing `/readyz` returned the raw dependency error (which can carry a
  DSN, credentials, or internal hostnames) to an unauthenticated caller; it now
  names only the unready dependency and logs the full error server-side. Scrapers
  must target the metrics port (`LEOFLOW_SERVER_METRICS_ADDR`, default `:9090`),
  not the API port.

## [0.2.0] - 2026-08-07

> The **0.2.0** line. The control plane can now run as **separate api and
> scheduler processes** (ADR 0049, `split.enabled`, off by default), the inline
> **`http_api` task type is removed** — closing an SSRF surface (**breaking**
> only for a hand-authored `http_api` DAG) — and **task reaping is now
> at-most-once**: a reaped task's pod is actually torn down instead of running
> user code to completion. Plus a pod-aware dispatch-lost reaper, a configurable
> task namespace, shell-quoted bash templating, and an e2e/chaos harness that
> runs on Linux/Lima, not only Docker Desktop. (Shipped through `v0.2.0-rc.1`,
> validated on a Linux VM and a hands-on UI journey, then promoted unchanged.)

### Changed

- **k3d e2e image import is now verified + retried** (test/e2e reliability).
  `k3d image import` prints "Successfully imported" even when the in-node
  containerd import fails ("tarball: no such file or directory"), so a flaky
  import left the cluster without the image and every task pod ErrImagePulled —
  a confusing downstream failure. A shared `k3d_import` helper treats the import
  as successful only when k3d exits 0 AND emits no error line, retrying up to 3×
  and failing loud otherwise. Applied across the operator, split, and dbt e2es.

- **Runtime chaos / fault-injection e2e harness** (`make chaos-runtime`, #231
  Phase 2). A destructive k3d harness that injects real faults on the two newest,
  riskiest surfaces and asserts the invariants hold: killing the scheduler
  mid-run in the api/scheduler split — the api's `/monitor/health` flips the
  scheduler unhealthy, the in-flight run resumes on restart, and each task ran
  exactly once (at-most-once, no duplicate dispatch) — and force-deleting a
  running task's pod, after which the agent-lost reaper moves the task off
  `running` with no orphaned pod left behind. Not gated in CI (slow); run on a
  Linux box.

- **The k3d e2e harness and `rc-smoke` battery now run on Linux/Lima, not only
  Docker Desktop.** Running the RC battery on a Linux host surfaced four
  environmental gaps that also affect a real Lima gate: task pods reach the host
  control plane via `host.k3d.internal` on Linux (Docker Desktop's
  `host.docker.internal` is not injected there); the DAG image is pinned to the
  host arch (the loader defaults `build.platforms` to `linux/amd64`, which fails
  `FROM` an arm64 base with `InvalidBaseImagePlatform` → `ErrImagePull`); the base
  image is built with `--provenance=false` (a buildx manifest list breaks a later
  `FROM` with "no match for platform"); and `rc-smoke`'s ui-smoke step now brings
  up the Lite control plane it needs and drives it over IPv4. Test-only; macOS
  behaviour is unchanged (the Linux branches are guarded by `uname`).

### Fixed

- **Split api role now reports pod dispatch correctly on `/api/v2/monitor/executor`**
  (ADR 0049 pre-RC review). In split mode the executor runs in the scheduler
  process, so the api role's runtime dispatch flag was always false and the
  endpoint — whose job is "why is a task stuck queued" — told operators pod
  dispatch was off when it wasn't. The api role now reports the configured
  capability (`executor.type`) instead of the in-process bool.

### Fixed

- **Reaping a task now actually stops it** (#474). The three scheduler reapers
  (`agent_lost`, `dispatch_lost`, `orphaned`) only wrote metadatabase state, so a
  reaped task's pod kept running user code to completion — breaking at-most-once
  execution when that work committed or a retry re-ran it. Each reaper now tears
  the pod down **after** the durable DB transition: the per-TI reapers delete
  exactly the reaped `(run-id, task-id, try-number)` pod (a retry's newer pod has
  a different try-number label, so it can never be the one deleted), and the
  orphan-run reaper deletes every pod of the abandoned run. As a belt-and-suspenders
  layer for pods we couldn't delete (e.g. a K8s API outage), the control plane now
  answers a **stale** agent `ReportState`/`Heartbeat` — one whose attempt no longer
  matches the live row — with `should_terminate`, so a reaped-but-alive pod cancels
  its own work. The "stale" test reuses the exact source-state + `try_number` guard
  the state write already carries (#467): a live, matching attempt always applies,
  so a live execution is never torn down or told to stop. Deletion uses only the
  `list`+`delete` pod verbs the executor Role already grants; Lite (subprocess) has
  no pods, so only the DB transition and the terminate signal apply.
- **Dispatch-lost reaper is now pod-aware, ending the cold-node false positive**
  (#461). A slow image pull on a cold node could leave a TI in `queued` past the
  3-minute threshold while its pod was actually `Pending`/`Running`, so the reaper
  failed a live dispatch as `dispatch_lost`. On Kubernetes the reaper now checks
  pod liveness first: if a pod for the TI is `Pending`/`Running` (the dispatch
  landed, the node is just slow) — or if liveness can't be read (K8s API error) —
  it defers instead of reaping. It only fails a TI when no live pod exists. Lite
  keeps the pure time-threshold behavior. New metrics: `dispatch_lost_deferred`,
  `dispatch_lost_pod_query_error`.
- **`taskNamespace` now actually moves the control plane** (#480). The chart
  granted the executor Role in `.Values.taskNamespace` while the server created
  task pods in a hardcoded `leoflow` namespace, so any override installed cleanly
  and then 403'd every dispatch. The namespace is now configuration
  (`executor.task_namespace`, wired from the chart's `taskNamespace` via
  `LEOFLOW_EXECUTOR_TASK_NAMESPACE`), so the server acts on exactly the namespace
  it is granted — the knob means what it says. A helm test asserts the server env
  and the RBAC namespace derive from the same value.

### Security

- **Bash task templating now shell-quotes interpolated values** (issue #489). A
  `bash_command` Jinja-renders the run context, and `params` is the run's `conf` —
  supplied by anyone with `execute:dag`, a lower bar than authoring the DAG. A
  value with shell metacharacters (`x; rm -rf /`, `$(...)`) was interpolated into
  the command string unquoted, so a trigger could run arbitrary commands in the
  task pod (privilege escalation from "may run this pipeline" to "may run any
  command"). Every interpolated value is now `shlex.quote`d before `bash -c`; the
  trusted template text is unchanged and safe values render byte-identically.
  Behavior change: a value can no longer expand into multiple shell words — write
  interpolations unquoted (`--name {{ params.x }}`).

### Added

- **Optional api/scheduler split for Pro (`split.enabled`, off by default; ADR
  0049).** `leoflow-server` gains a role (`LEOFLOW_SERVER_ROLE`: `all` (default,
  and Lite's only mode — behavior-identical to before), `api`, `scheduler`). When
  the chart's `split.enabled` is set, it renders a restricted-identity api
  Deployment (HTTP + UI, active-active, its ServiceAccount unbound from the
  pod-create Role) and a privileged single-leader scheduler Deployment (reconciler
  + dispatch + agent gRPC), each with its own Service/RBAC/NetworkPolicy and
  role-appropriate probes. Isolates API load from scheduling and shrinks the
  API's blast radius. Lite and existing monolith installs are unaffected.

### Changed

- **The metrics listener (`:9090`) now also serves `/healthz` and `/readyz`**, and
  is scoped to those plus `/metrics` (a request to another path returns 404,
  where the bare Prometheus handler previously answered on any path). This gives a
  scheduler-only pod a probe target (ADR 0049) and is additive for the standard
  `/metrics` scrape; adjust any scrape configured against `:9090/` (non-`/metrics`).

### Removed

- **The native inline `http_api` task type is removed** (ADR 0047/0048, issue
  #512). It ran an author-supplied HTTP request *inline in the control-plane
  process*, carrying the control plane's network position — a server-side request
  forgery surface (audit finding H5). Registering a spec with `type: http_api` is
  now **rejected** at validation (the parser already stopped emitting it — an
  `HttpOperator` compiles to a pod-run `airflow_operator`, ADR 0040 — so it could
  only arrive via a hand-written `dag.json`), and the inline executor and its
  scheduler wiring are deleted, so the guard is structural (no in-process path to
  route to, ADR 0048), not an input check. **Breaking** only for a hand-authored
  `dag.json` that declared `http_api`; migrate to an `HttpOperator` (runs in a
  task pod; declare `connectors: [http]`). The one-release deprecation window was
  collapsed into this release because it is the first Pro-facing cut and shipping
  the SSRF was not acceptable; the parser-emitted path was already gone, so no
  compiled DAG is affected.

### Added

- **A task-pod egress NetworkPolicy** (`taskNetworkPolicy`, off by default). Task
  pods run untrusted author code, so this is where the "a pod can reach cloud
  metadata / the apiserver / another service" risk is contained — at the network
  layer, for every task type — not in the control plane (ADR 0048). When enabled
  it denies ingress, allows DNS and the control-plane gRPC (the agent dials back),
  then all other egress **except the cloud metadata endpoint** (`169.254.0.0/16`),
  which is always blocked — the one unambiguous SSRF target. `blockPrivateNetworks`
  additionally denies RFC1918 + the apiserver; it is opt-in because a DAG calling
  an internal service is legitimate and the policy cannot tell that from the
  apiserver by IP (ADR 0047). This is the network-layer control ADR 0047/0048 point
  to, and the same one Argo and Kubeflow recommend.

### Added

- **A synchronous dispatch failure now backs off and eventually gives up, instead
  of retrying every tick forever** (ADR 0031 Amendment A). When `Dispatch` failed
  synchronously — kube-apiserver unreachable, RBAC denied, quota, an admission
  webhook reject — the task stayed `scheduled` and the planner re-attempted it on
  every tick, with no backoff, surfaced by no reaper (the dispatch-lost reaper
  only sees `queued`). A permanent misconfiguration became a silent tight loop.

  The task instance now records `dispatch_attempts` and `next_dispatch_at` (two new
  columns, mirroring `reschedule_at`); the planner does not re-dispatch until the
  exponential, capped backoff elapses, and after the attempt budget is spent the
  task fails as `dispatch_failed` so the run finalizes. A dispatch failure does
  **not** consume the task's `try_number` — it is infrastructure, not a task
  failure, so a `retries: 0` task is not killed by a transient blip.
  `dispatch_failed` is distinct from `dispatch_lost` (dispatched then vanished) and
  from a task's own `failed` (the code ran and failed).

- **`leoflow compile` now rejects a task graph that cannot execute.** Three
  defects shared one symptom, and it was the worst one available: the run
  started, no task in the affected region ever became ready, and the run sat in
  `running` indefinitely with nothing on screen explaining why. There was no
  error to read — from the scheduler's side nothing had gone wrong, it was
  waiting on a predecessor, correctly, forever.

  - a **cycle** (`a >> b >> c >> a`, or a self-dependency). Airflow does not
    reject this: the parser emits a perfectly well-formed `dag.json`.
  - a **`depends_on` naming a task that is not declared** — the more common typo.
  - a **duplicated `task_id`**, which is the same family for a different reason:
    the graph keys by id, so the losing definition was silently dropped and the
    DAG ran a subset of what was written.

  The error names the tasks involved (`cyclic task graph: a -> c -> b -> a`),
  because "cycle detected" alone leaves the author to find it by hand in a
  200-task DAG. Cycle detection is a three-color DFS rather than a visited set: a
  visited set reports a diamond — two paths rejoining at one task, one of the most
  ordinary shapes a real DAG has — as a cycle. Traversal follows declared task
  order, so the same DAG always reports the same cycle.

  Validated at compile and again at registration, so a hand-written or
  machine-generated `dag.json` cannot bypass the compiler.

- **A typo in an alert template is now a compile error.** An unknown placeholder
  is not a rendering failure — `Render` leaves it alone — so `{{taskk}}` used to
  survive compile and reach the operator verbatim, discovered in the alert that
  was supposed to explain an outage. `leoflow compile` now rejects it, naming the
  offending placeholder and listing the supported set. Airflow-style names
  (`{{ ds }}`) are reported by name rather than passing through as text.

- **`leoflow compile` now rejects a `dbt.project` or `dbt.manifest` path it cannot use.** The value
  is resolved with `filepath.Join(dagDir, project)` and, for a Pro image build,
  baked at that same relative path inside the image — so both an absolute path and
  one escaping upward were broken, and broken silently.

  `filepath.Join` does not treat an absolute second element specially:
  `Join("/dags/sales", "/opt/dbt/proj")` is `/dags/sales/opt/dbt/proj`. The
  leading slash was swallowed and dbt was pointed at a directory nobody named. A
  path like `../../../etc` resolved outside the DAG directory, and therefore
  outside the Docker build context, so the image could not contain it either.

  Both fields feed the same `filepath.Join` chain — `project` onto the DAG
  directory, then `manifest` onto that result — so both are checked. Validating
  only `project`, as the first version of this change did, left half the defect in
  place.

  Both surfaced only when dbt ran inside a pod, as "project directory does not
  exist", with nothing connecting it back to `leoflow.yaml`. The error now names
  the field, what it resolves to, and why that cannot work. `.`, `transform`,
  `./transform`, `dbt/transform`, `target/manifest.json` and paths that normalise
  back inside are unaffected.

### Fixed

- **The pod reconciler could garbage-collect a failed pod before its failure was
  recorded.** `Reconcile` reported a failed pod's task instance and then deleted
  the pod if it had aged out — but the delete ran whether or not the report
  succeeded. A transient metadatabase error during `FailTask` meant the pod (the
  only signal that would let the next tick retry) was deleted anyway, stranding
  the task instance in `running` until the slower heartbeat reaper caught it. The
  reconciler now defers collection of a failed pod until its failure is durably
  recorded; a succeeded pod, which has nothing to record, is still collected on
  age alone. (One component was both the state-recorder and the garbage-collector
  with no ordering between them.)

- **The pod reconciler and staging-volume GC ran on every replica, not just the
  leader.** The scheduler loop and its reapers are leader-gated (ADR 0009), but
  `startReconciler` and `startStagingGC` spawned unconditional tickers, so at
  `replicaCount > 1` every replica would list, reconcile, and delete the same task
  pods and staging PVCs — a follower racing the leader's provisioning. Both now
  gate on the same leadership signal (`scheduler.IsLeading`) the scheduler loop
  uses. No behaviour change at the default `replicaCount: 1`, where the single
  replica is always the leader; this is the correctness base that makes running
  Pro with more than one replica safe.

- **Alert messages carried a UUID where the run id belongs.** `{{run_id}}`
  rendered `RunState.RunID`, which is the `dag_runs` primary key — not the
  `run_id` an operator sees in the UI and passes to the API. The alert named a
  run nobody could look up. It now renders the user-facing id.

- **`{{logical_date}}` always rendered empty.** The placeholder was documented and
  substituted, but the dispatcher never populated the field, so every alert using
  it produced a dangling `for `. `RunState` now carries the logical date and the
  dispatcher passes it through.

- **A placeholder with no value renders `(none)`** instead of an empty string —
  `{{logical_date}}` on a manually triggered run, for example. `failed for
  logical date ` is indistinguishable from a truncated message.

### Security

- **Hardened the log sink against path escape.** `DiskSink` interpolated every
  `logs.Ref` field straight into a filesystem path
  (`{root}/{tenant}/{dag}/{run}/{task}/{try}.log`) with no containment, so any
  Ref carrying `../` or a separator read and wrote outside the log root. Both
  filesystem calls were annotated
  `//nolint:gosec // path is built from validated identity fields`, asserting a
  validation step that existed nowhere in `internal/` — which is why static
  analysis stayed quiet on those lines for the life of the repository.

  **Not reachable in any released version.** All five `logs.Ref` construction
  sites pass the database UUID as `RunID`: the scheduler builds `RunState.RunID`
  from `uuidToString(run.ID)`, dispatch mints that same value into the agent
  token, and both read paths resolve the caller's `run_id` through
  `ResolveRunRef` to `ref.DagRunID` before touching the sink. `dag_id` and
  `task_id` carry a charset pattern in the DAG schema, enforced at compile and
  again at registration. No release shipped an exploitable path. What shipped was
  a sink whose safety rested entirely on every current caller happening to pass a
  UUID, under a comment claiming a guarantee that was absent.

  The sink now performs all filesystem work through `os.Root` pinned to the log
  directory, so escape is refused by the runtime rather than by convention. That
  also closes a case no string check can see: a symlink inside the log root whose
  every path component is a legal name, where the traversal happens during kernel
  resolution. `logs.Ref` is additionally validated field by field, which names the
  offending field in the error.

  Separately, `POST /api/v2/dags/{dag_id}/dagRuns` accepted `dag_run_id` verbatim
  from the request body with no validation at all. It now rejects a value that is
  not a usable single path segment. Airflow-generated ids embed an RFC3339
  timestamp and keep working: separators are banned, punctuation is not.

## [0.1.2] - 2026-07-30

> **On-failure alerting, and a Pro install that can no longer be misconfigured into
> silence.** A failed task notifies on its own, without an Airflow callback in the
> loop; and three Pro misconfigurations that used to produce a healthy-looking but
> broken deployment now fail loudly at `helm install` or at boot.
>
> This promotes `v0.1.2-rc.2` unchanged. The candidate was exercised by hand on
> darwin/arm64 — the platform no CI job covers — including the alerting paths, both
> Pro guards, and the callback fix that prompted the respin. Per-candidate detail is
> in the `0.1.2-rc.2` and `0.1.2-rc.1` sections below.

### ⚠️ Upgrade note for Pro operators

`agentTLS.enabled: false` no longer yields a running deployment — it never yielded a
working one, and now says so at install time instead of crash-looping. Full detail and
the migration in the `0.1.2-rc.1` section below.

### Known issues carried into this release

- **A task can be marked `dispatch_lost` while its pod is still `Running` (#461).** The
  mechanism is now understood (#474): no reaper consults Kubernetes, nothing deletes the
  pod, and the `should_terminate` signal the agent honours is never sent. A slow image
  pull is enough to trigger it. Fix in progress.
- **Shutdown can hang when the Kubernetes API stops answering (#463).** Described in the
  `0.1.2-rc.2` section; the fix is written and lands next.

## [0.1.2-rc.2] - 2026-07-30

> Respin of `0.1.2-rc.1` for one defect, found by hands-on validation of that
> candidate: a task declaring `on_failure_callback` as a **list** — the shape Airflow
> itself produces — had it dropped silently. Nothing else changed; every other item
> in the 0.1.2 line is described in the `0.1.2-rc.1` section below and was verified
> against that candidate.

### Fixed

- **`on_failure_callback` written as a list now runs (#470).** Airflow 3 normalises a
  task's callback attributes to a list, so `@task(on_failure_callback=[notify])` — and
  any DAG copied from a real Airflow deployment — carries `[fn]`, not `fn`. The runtime
  already accepted both shapes; the compiler's gate tested only `callable()`, so the
  list form produced no marker in `dag.json`, no warning, and no callback at run time.
  A bare callable was unaffected.
- **An *unsupported* callback written as a list is loudly rejected again (#470).** The
  same gate guards the compile-time refusal for `on_success_callback` and
  `on_retry_callback`. In list form they slipped past it and were dropped in silence —
  the precise outcome the refusal exists to prevent, and worse than the case it was
  written for, because the author was told nothing.

### Known issues

- **Shutdown can hang when the Kubernetes API stops answering (#463).** The 0.1.2
  line wires the buffered dispatch drain into shutdown (#133) so in-flight dispatches
  settle instead of leaking — but the wait is unbounded and the Kubernetes client
  carries no per-call timeout. An apiserver that accepts the connection and never
  answers pins a worker, and with it the drain, until the runtime kills the process.
  In that specific case shutdown is worse than in 0.1.1, where the drain never ran at
  all. The fix is written and held for the next release rather than folded in here,
  to keep this candidate a targeted respin. Workaround: none needed unless your
  apiserver hangs; the pod is SIGKILLed at the end of its termination grace period.
- **A task can be marked `dispatch_lost` while its pod is still `Running` (#461)** —
  carried over from rc.1, still under investigation.

### Corrected from the rc.1 notes

The `0.1.2-rc.1` section claims, under Fixed, that `on_failure_callback` runs for
"list-normalised callbacks". Unbound DAGs did work; **lists did not**. Tags are
immutable (ADR 0033), so that section stands as published — the claim it makes is true
of this candidate, not of rc.1.

## [0.1.2-rc.1] - 2026-07-30

> First release candidate of the **0.1.2** line — **native on-failure alerting** and a
> **Pro install that can no longer be misconfigured into silence**. A failed task now
> notifies on its own, without an Airflow callback in the loop; and the three Pro
> misconfigurations that used to produce a healthy-looking-but-broken deployment now
> fail loudly at `helm install` or at boot.

### ⚠️ Upgrade note for Pro operators

**`agentTLS.enabled: false` no longer yields a running deployment.** It was never a
plaintext deployment — the control plane marks itself as the Pro edition and every
secrets RPC to a task pod was rejected, so tasks queued and hung with no visible
cause. That failure is now surfaced where you can act on it:

- `helm install`/`upgrade` **refuses to render** with `agentTLS.enabled=false`.
- The control plane **refuses to boot** without `LEOFLOW_SERVER_GRPC_TLS_CERT`/`_KEY`
  when `LEOFLOW_UI_EDITION=pro`.

**If your values override `agentTLS.enabled` to `false`,** provision a cert before
upgrading — cert-manager `Certificate` + CA trust bundle, then set
`agentTLS.serverCertSecret` and `agentTLS.caConfigMap`. Step-by-step in
[`docs/pro-tls.md`](docs/pro-tls.md). Installs on the `agentTLS.enabled: true` default
(unchanged since 0.1.1) are unaffected. For a plaintext local loop, use the Lite dev
server (`leoflow dev lite`), which is not subject to the Pro guards.

### Added

- **Native on-failure alerting (#424).** A DAG declares its alert targets and the
  control plane notifies on final task failure from a Go notifier — no Airflow
  callback in the request path. Configuration, `dag.json` fields and the notifier
  ship together; see [`docs/alerting.md`](docs/alerting.md).
- **Airflow `on_failure_callback`, in-process on final failure (#424).** The familiar
  Airflow hook runs where Leoflow already knows the task reached its terminal state,
  so existing DAG code keeps working alongside native alerting.
- **Alerts dedup per failure episode (#431).** Retries within one failure episode
  produce one notification, not one per attempt.
- **Saturation-drop metric (#435).** Drops caused by a saturated alert path are now a
  metric you can alert on, not just a log line.

### Changed

- **The Pro edition refuses to boot without TLS on the agent gRPC channel (#281).**
  Booting looked healthy while every secrets RPC failed; it now fails at boot with the
  reason. See the upgrade note above.
- **The Helm chart fails at render on the silent Pro misconfigs** — `agentTLS.enabled`
  with an empty `caConfigMap` (#280), a `ReadWriteOnce` logs PVC under more than one
  replica whether static or HPA-driven (#282), and `agentTLS.enabled=false` on a chart
  that only ever deploys Pro (#459). Each fails in about a second with an actionable
  message instead of a `CrashLoopBackOff` or a `Multi-Attach` hang.

### Fixed

- **Buffered dispatch drains on shutdown, and `Close()` is no longer racy (#133).**
  In-flight dispatches are no longer dropped when the control plane stops.
- **The parser rejects an oversized literal task argument at compile time (#149)**,
  instead of failing later in the pod.
- **The parser captures `retries`, `retry_delay` and `execution_timeout` from
  operators (#434)** — previously dropped, so a task silently ran with defaults.
- **`on_failure_callback` runs for unbound DAGs and list-normalised callbacks (#424).**

### Security

- **grpc → v1.82.1**, clearing `GHSA-hrxh-6v49-42gf`.
- **`x/net` and `x/text` bumped**, clearing `CVE-2026-46600` and `CVE-2026-56852`.
- **Trivy filesystem scan now runs on pull requests (#437)**, not only on push and
  schedule, so a vulnerable dependency is caught before merge.
- Nine further dependency bumps in the minor-and-patch group.

### Known issues

- **A task can be marked `dispatch_lost` while its pod is still `Running` (#461).**
  Observed once in CI and not reproducible on rerun; the pod is dispatched and alive,
  but its agent never reports in, and the control plane fails the task at the dispatch
  threshold. In production this risks a false failure (and its alert) on work that may
  still be executing. Under investigation — please report occurrences with the run id.

## [0.1.1] - 2026-07-10

> **dbt-native orchestration.** A dbt project becomes a Leoflow DAG — one pod per
> model, no Cosmos at runtime, no Airflow in the parser — and develops **locally with
> zero config** against an embedded duckdb. This promotes `v0.1.1-rc.1` after a clean
> Lite (arm64) end-to-end soak, plus a Go 1.26.5 toolchain bump for a newly-disclosed
> standard-library advisory (below) — no functional change from the rc.

### Security

- **Go toolchain 1.26.4 → 1.26.5**, closing `GO-2026-4970` (root escape via symlink +
  trailing slash in the standard-library `os` package), disclosed after the rc was cut
  and reachable from the installer's download path.

### The 0.1.1 line, in brief

- **dbt projects as Leoflow DAGs (ADR 0042).** A dbt project compiles straight to
  Leoflow tasks from its `manifest.json`, in Go — one task per model/seed/snapshot/test,
  wired by dbt's own dependency graph.
- **Mix dbt with operators (ADR 0043)** via `dbt_group("name")`, and **multiple dbt
  projects, one per business domain (ADR 0044)** via a namespaced `dbt_groups` map.
- **Adapters:** Postgres, Snowflake, BigQuery, Databricks, and **duckdb** — mapped from a
  managed connection at runtime, so no warehouse credential is baked into the image.
- **Zero-config local dbt (Lite):** a dbt project with no connection and no
  `profiles.yml` just runs against an embedded duckdb, never touching your global
  `~/.dbt`; model edits hot-reload.

Per-rc detail is in the `0.1.1-rc.1` section below.

## [0.1.1-rc.1] - 2026-07-05

> First release candidate of the **0.1.1** line — **dbt-native orchestration**. A dbt
> project becomes a Leoflow DAG (one pod per model, no Cosmos at runtime, no Airflow in
> the parser), and — new this line — a dbt project develops **locally with zero config**.

### Added

- **dbt projects as Leoflow DAGs (ADR 0042).** A dbt project compiles straight to
  Leoflow tasks from its `manifest.json`, in Go — one task per model/seed/snapshot/test,
  wired by dbt's own dependency graph. No Cosmos at runtime, no Airflow in the parser.
- **Mix dbt with operators (ADR 0043).** `dbt_group("name")` embeds a dbt project as a
  namespaced task group inside a normal `dag.py`, wired around your operators/sensors;
  `granularity` (node/level/folder) is a pod-packing knob.
- **Multiple dbt projects, one per business domain (ADR 0044).** A `dbt_groups` map lets
  one DAG carry several domain projects (`sales__*`, `marketing__*`), namespaced so
  identically-named models never collide.
- **Adapters:** Postgres, Snowflake, BigQuery, Databricks (the official `dbt-databricks`
  adapter), and **duckdb** — Leoflow maps a managed connection to each adapter's profile
  at runtime, so no warehouse credential is baked into the image.
- **Zero-config local dbt (Lite).** `leoflow lite` + a dbt project with no connection and
  no `profiles.yml` just runs — against an embedded **duckdb** file, generated
  transparently at compile and run time, never touching your global `~/.dbt`. Model
  edits hot-reload (the manifest re-parses with the per-DAG venv's dbt).

### Fixed

- **Lite runs dbt end-to-end (subprocess):** the per-DAG venv's `bin` is now on the
  task's PATH (so a bare `dbt` resolves), the dbt `--project-dir` is absolute for local
  builds (so the project resolves from the task workdir), and the manifest parses with
  the venv's dbt — the three gaps that stopped `leoflow lite` from running a dbt DAG.
- **`leoflow compile` self-heals the extracted parser after a binary upgrade** (#239), so
  a new build's features (like dbt) never fail against a stale `~/.leoflow/pysrc`.

## [0.1.0] - 2026-06-28

> **First stable release.** Leoflow runs standard Apache Airflow 3.2 DAGs on a Go
> control plane — no GIL, no Airflow in the scheduling path — one pod per task. This
> promotes `v0.1.0-rc.4` verbatim: the same artifacts, soaked through the rc series.

### The 0.1.0 line, in brief

- **Standard Airflow 3.2 DAGs in Python.** A dependency-free structural shim (ADR
  0024) parses `airflow.sdk` DAGs without importing Airflow; the real provider
  operator runs in the task pod (ADR 0040).
- **Provider operators & sensors.** A native fast path for `bash`/`python`/`http`,
  generic capture for the long tail, and reschedule-mode sensors.
- **86 connection types** generated from real Airflow (ADR 0038 / 0039), with the
  `connectors:` one-liner that bakes providers into each DAG's image.
- **Lite** — a Docker-free, Kubernetes-free local edition: hot-reload, the embedded
  Airflow 3.2 UI, and resilience (Docker-wedged fallback, boot self-heal, per-DAG
  venv reclaim, watcher-token refresh).

Per-rc detail is in the `0.1.0-rc.1` … `0.1.0-rc.4` sections below.

## [0.1.0-rc.4] - 2026-06-28

> Fourth release candidate of the **0.1.0** line — a single Lite fix found while
> testing rc.3.

### Fixed

- **Lite hot-reload no longer stops registering after an hour (#407).** `leoflow
  lite` minted one admin token at startup (one-hour expiry) and reused it for every
  hot-reload registration, deregister, and the boot reconcile; after an hour the
  token expired and every save silently failed to register — only a `✗ … invalid
  token` in the log, with the UI just not updating. The watcher now re-mints a fresh
  token per operation, so a Lite left running for the day keeps reloading.

## [0.1.0-rc.3] - 2026-06-28

> Third release candidate of the **0.1.0** line. On top of rc.2 it hardens the
> **install path** and makes the local **Lite** edition **resilient** — every item
> here surfaced during hands-on testing of rc.2. The boot self-heal is end-to-end
> gated in CI.

### Added

- **Lite self-heals stale state on boot (#404).** A reused metadata DB no longer
  leaves "ghost" DAGs and orphan import errors that the UI showed but could not
  remove: on startup Lite reconciles the registered DAGs against the workspace —
  deregistering what is gone on disk and clearing stale import errors — fail-safe
  (if the control plane can't be listed, it wipes nothing). A new end-to-end gate
  (`lite-selfheal`) keeps it from regressing.
- A DAG's **per-DAG venv is reclaimed when the DAG is deregistered** (and logged),
  instead of lingering on disk with the Airflow SDK; a later reload re-creates it
  if the DAG returns. The sweep of venvs orphaned while Lite was stopped is tracked
  for a scheduled GC (#406).

### Fixed

- **Install:** the pinned-version command placed `LEOFLOW_VERSION` on `curl`
  instead of `sh`, so `install.sh` resolved latest-stable and installed the
  previous release rather than the pinned rc. The variable now sits on the `sh`
  side of the pipe (#402).
- **Lite falls back to a Docker-free Postgres when Docker is wedged (#403).** The
  auto-resolvers now ping the Docker daemon; a present-but-unresponsive Docker
  (e.g. a hung Docker Desktop returning 500s) falls back to the managed Postgres
  and the subprocess executor instead of aborting on `docker compose up`.

## [0.1.0-rc.2] - 2026-06-24

> Second release candidate of the **0.1.0** line. On top of rc.1 it ships
> **reschedule-mode sensors** — the first ADR 0040 Phase B capability, which rc.1
> still rejected at compile — plus release- and CI-hardening fixes. E2E-gated on a
> real k3d cluster.

### Added

- **Reschedule-mode sensors (ADR 0040, Phase B).** A sensor declared
  `mode='reschedule'` now releases its pod between pokes instead of holding it:
  on a not-ready poke the task transitions to `up_for_reschedule` and the
  scheduler re-dispatches it once `reschedule_at` arrives — no retry budget
  consumed (mirrors the `up_for_retry` rail). The agent reports
  `up_for_reschedule` only when the task exits 75 **and** writes a parseable
  reschedule file, so a bare exit 75 stays an ordinary failure. Validated on a
  real k3d cluster via a `DateTimeSensor(mode='reschedule')` e2e guard that
  visibly passes through `up_for_reschedule` and is re-dispatched to success
  (#380, #389).

### Fixed

- Deterministic reschedule-sensor e2e guard: the wait loop asserts the sensor
  passed through `up_for_reschedule` without timing flakiness (#390).
- Release notes now install the exact release tag (`LEOFLOW_VERSION` + the
  tag's `install.sh`) instead of resolving latest-stable, and drop the stale
  `(pre-alpha)` wording (ADR 0037) (#391).

### Changed

- CI secret-scan runs a pinned `gitleaks` binary and drops the flaky Docker Hub
  pull (#392).

## [0.1.0-rc.1] - 2026-06-16

> First release candidate of the **0.1.0** line — a Go control plane that runs
> DAGs end-to-end and serves the embedded Apache Airflow 3.2.1 UI. Published as a
> pre-release after hands-on maintainer validation of the UI and the connector
> flow in a real browser.

### Added

- **Embedded Airflow 3.2.1 UI (Phase 5).** The control plane embeds the pinned
  Airflow 3.2.1 React SPA (`go:embed`, ADR 0017) and serves it at `/`, alongside
  the implemented internal UI API:
  - Auth/identity: `GET /ui/config`, `GET /ui/auth/me`, `GET /ui/auth/menus`
    (curated to the screens Leoflow backs), `POST /ui/auth/token`.
  - Read views: `GET /ui/dags` (latest runs embedded, no N+1), `/ui/dags/{id}/latest_run`,
    `/ui/grid/runs/{id}`, `/ui/grid/structure/{id}`, `/ui/structure/structure_data`,
    `/ui/grid/ti_summaries/{id}` (NDJSON stream with a conditional-GET ETag),
    `GET /api/v2/dags/{id}/details` (cron→English), `GET /api/v2/version`.
  - Graceful degradation: unimplemented `/ui` screens return schema-valid empty
    responses; writes degrade to `501`.
  - Static assets are gzipped; the SPA shell and assets load without auth so the
    login screen is reachable, while `/api/v2` and `/ui` data stay gated.
- **One-command demo.** `docker compose --profile demo up --build` brings up
  Postgres, Redis, and the control plane with the UI; bootstraps an admin user.
  `deploy/Dockerfile.server` builds the single image.
- `make fetch-airflow-ui` extracts the pinned UI bundle from `apache/airflow:3.2.1`.
- **Connectors — provider hooks/operators without `apache-airflow` in the control
  plane (ADR 0038/0039).** `connectors:` / `dependencies:` in `leoflow.yaml` install a
  provider into the DAG image; a **generated connection catalog** (86 connection
  types, derived from real Airflow) drives the UI's Add-Connection form
  (`/ui/connections/hook_meta`) and the structural connection-test probe
  (`POST /api/v2/connections/test`). Admin Variables and Connections are delivered to
  each task as `AIRFLOW_VAR_*` / `AIRFLOW_CONN_*` (encrypted at rest, ADR 0019) and
  resolved by Airflow's native env-secrets backend — in pods (via the agent over gRPC)
  and in Lite/subprocess.
- **Airflow operators & poke sensors (ADR 0040, Phase A).** Any provider operator or
  sensor runs in its own pod through a generic executor
  (`import_string(class)(**args) → render_template_fields → execute(context)`) — no
  per-operator code. Includes `ti.xcom_pull` chaining between operators; the
  standalone run context (`ds`, `ts`, `data_interval_*`, `params`, `var`, `conn`);
  operator extra-links (the UI "open in …" buttons); multi-key XCom; and native
  `@task` / `bash` parity (including Jinja-templated bash commands). Reschedule-mode
  sensors, deferrable operators, dynamic task mapping and branching are loudly
  rejected with actionable errors (tracked for later phases).

### Changed

- ADR 0007 (Airflow UI Compatibility) premise refined from Airflow 2.x-style
  `/api/v2` parity to the pinned Airflow 3.x `/ui/*` approach (see ADR 0017,
  ADR 0018, `docs/ui-compatibility.md`).

### Fixed

- The static SPA (shell + assets) is now public, so an unauthenticated first
  visit can load the app and reach the login screen.
- `/ui/auth/me` returns the authenticated user's username (the JWT now carries
  the email claim).

### Notes

- The pinned Airflow UI is a tactical MVP choice; a purpose-built Leoflow UI on
  the stable `/api/v2` is the long-term direction (ADR 0018).
- Browser end-to-end verification (rendering, write-flow paths, screenshots) is
  the remaining Phase 5 acceptance step; see `docs/ui-compatibility.md`.

[Unreleased]: https://github.com/neochaotic/leoflow/compare/v0.3.0...HEAD
[0.3.0]: https://github.com/neochaotic/leoflow/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/neochaotic/leoflow/compare/v0.1.2...v0.2.0
[0.1.2]: https://github.com/neochaotic/leoflow/compare/v0.1.2-rc.2...v0.1.2
[0.1.2-rc.2]: https://github.com/neochaotic/leoflow/compare/v0.1.2-rc.1...v0.1.2-rc.2
[0.1.2-rc.1]: https://github.com/neochaotic/leoflow/compare/v0.1.1...v0.1.2-rc.1
[0.1.1]: https://github.com/neochaotic/leoflow/compare/v0.1.1-rc.1...v0.1.1
[0.1.1-rc.1]: https://github.com/neochaotic/leoflow/compare/v0.1.0...v0.1.1-rc.1
[0.1.0]: https://github.com/neochaotic/leoflow/compare/v0.1.0-rc.4...v0.1.0
[0.1.0-rc.4]: https://github.com/neochaotic/leoflow/compare/v0.1.0-rc.3...v0.1.0-rc.4
[0.1.0-rc.3]: https://github.com/neochaotic/leoflow/compare/v0.1.0-rc.2...v0.1.0-rc.3
[0.1.0-rc.2]: https://github.com/neochaotic/leoflow/compare/v0.1.0-rc.1...v0.1.0-rc.2
[0.1.0-rc.1]: https://github.com/neochaotic/leoflow/releases/tag/v0.1.0-rc.1
