---
# --- AUTO redirect aliases (build_redirects.py) — do not edit by hand ---
aliases:
  - /ui-compatibility.html
# --- end AUTO redirect aliases ---
title: UI compatibility
weight: 40
description: "What the Airflow-compatible UI supports, and where it intentionally differs."
---

{{% alert title="Status update — superseded by the implementation" color="success" %}}
This began as a **Phase 5 investigation (2026-05-22)** arguing the *naive* goal
(unmodified Airflow UI on `/api/v2` alone) was not viable. **That realistic path
was taken and it works:** Dexaflow serves the unmodified Apache Airflow 3.3.2
React SPA **and** implements the internal `/ui/*` API (ADR 0017 / ADR 0018). The
grid, graph, dashboard, and run views run against Dexaflow today (Demo + Lite,
browser-verified). The standing risk below is real and accepted: `/ui/*` is
internal/unstable (AIP-84), so a new Airflow UI version can break a screen — we
pin to 3.3.2 (3.2.1 until v0.5.4) and guard with a browser contract sweep. See
[Upgrading the bundle](#upgrading-the-bundle) for what the 3.3 move changed.
{{% /alert %}}

**Original investigation (historical):** why the *naive* goal — run the unmodified
Apache Airflow 3.2.x UI against `/api/v2` only — was not viable as stated, and the
realistic path (now implemented).

## Finding: Airflow 3.x decoupled the UI onto an internal API

Airflow **2.x** served a Flask/FAB server-rendered UI; the stable REST API
(`/api/v1/`) was a separate, public surface. "Be API-compatible" largely meant
matching `/api/v1/`.

Airflow **3.x** (3.0 GA, Apr 2025) **rewrote the UI as a React SPA** served by a
FastAPI **API server**, and split the backend into *two* API surfaces:

| Surface | Purpose | Stability |
|---|---|---|
| **Public REST API** `/api/v2/` | external clients, OpenAPI-documented | stable, backward-compatible |
| **UI API** `/ui/*` (AIP-84) | powers the React UI (grid, graph/structure, dashboard, calendar) with aggregated/denormalized shapes | **internal, explicitly NOT backward-compatible, "should not be relied upon by external consumers"** |

The React UI is built against the **`/ui/*` API**, not the public `/api/v2/`.
The grid view, graph/structure view, and dashboard stats all come from `/ui/*`
endpoints whose shapes are tuned for the frontend and change between Airflow
releases by design (AIP-84 explicitly trades backward-compatibility away so the
UI can iterate).

### Consequence for Dexaflow

Dexaflow implements a subset of the **public `/api/v2/`**. It does **not**
implement `/ui/*`. Therefore:

- Pointing the **unmodified** Airflow 3.2.x UI at Dexaflow's `/api/v2/` **will not
  work** — the UI calls `/ui/*` endpoints Dexaflow doesn't serve.
- Implementing `/ui/*` to satisfy the UI means matching an **internal, unstable**
  API and re-chasing it on every Airflow minor release. That is a brittle,
  perpetual maintenance burden — the opposite of a stable compatibility target.

The original premise ("the `/api/v2/` API matches Airflow 3.2.x, so the UI just
works") reflects the **2.x** architecture. It does not hold for 3.x.

## Realistic paths

1. **Custom minimal UI (recommended).** Build a small React app against Dexaflow's
   own stable `/api/v2/`. This is already on the post-MVP roadmap ("Custom UI
   (replacing the Airflow UI)"). It avoids chasing an internal API and gives us a
   stable contract we control. Best long-term fit with the GitOps/immutable
   thesis.
2. **Implement a pinned `/ui/*` subset.** Serve the Airflow 3.2.1 UI assets and
   implement exactly the `/ui/*` endpoints that version calls, pinned to 3.2.1.
   Delivers the familiar Airflow UI now, but is brittle and version-locked.
3. **Defer the UI.** For the MVP, the operator surface is the embedded **Scalar
   API reference** (`/docs`) plus the `dexaflow runs` / `dexaflow` CLI. The visual
   UI lands later via path 1 or 2.

## Public `/api/v2/` compatibility audit

Independent of the UI, the **public** API should stay Airflow-3-shaped for
external clients (Airflow operators, scripts, the `airflow` CLI's API mode). What
Dexaflow exposes today aligns well on shape:

- ✅ Airflow-3 field naming: `logical_date` (not `execution_date`), `dag_run_id`,
  `task_instances`, `total_entries` pagination, the `__type` schedule field.
- ✅ Core resources: `GET /dags`, `GET|PATCH /dags/{id}`,
  `GET|POST /dags/{id}/dagRuns`, `.../taskInstances`, `.../logs/{try}`,
  `clearTaskInstances`, `xcoms`.
- ⚠️ **Breadth gap** vs Airflow 3.2 `/api/v2/`: not implemented yet —
  `/dags/{id}/details` (timetable_description, etc.), `/dagSources`, `/eventLogs`,
  `/variables`, `/connections`, `/pools`, `/assets`, `/monitor/health`,
  `/version`. These are needed for full external-client parity but not for the
  current execution surface.
- ⚠️ `/dags/{id}/versions` is **Dexaflow-specific** (DAG-as-image versioning), not
  an Airflow endpoint.

## Decision: path 2, serve the unmodified Airflow UI and implement a pinned `/ui/*`

We pursue **path 2**: serve the **unmodified** Apache Airflow React UI assets
(pinned to **3.3.2** since v0.5.4, **3.2.1** before) and implement, in the
Dexaflow control plane, the `/ui/*` (and the few extra `/api/v2/*`) endpoints
that exact version calls. We
accept the version-lock and the re-chase-on-upgrade cost; in return we get the
familiar Airflow UI without forking it.

**Risk mitigation (non-negotiable):**

1. **Authoritative shapes, never guessed.** Every endpoint we implement is
   matched against the version-pinned spec
   `airflow-core/src/airflow/api_fastapi/core_api/openapi/_private_ui.yaml`
   at the pinned tag (the `/ui` API) and `v2-rest-api-generated.yaml` (public).
   That file is the single source of truth for field names and types.
2. **Do not fork or strip the SPA.** The UI is a compiled Vite bundle; surgically
   removing components is impractical and forking + rebuilding it is the brittle
   path we are avoiding. Instead we **shape the UI from the backend** (below).
3. **Pin everything.** The Airflow image tag, the spec, and the asset bundle are
   all pinned to one version (`AIRFLOW_UI_VERSION` in the `Makefile`). Upgrades
   are a deliberate, tested event.

## Graceful degradation — hide the uncovered, never break

Rather than leave dead buttons, we minimize the uncovered surface from the
backend, in three tiers:

1. **`/ui/auth/menus` (curated)** — the UI renders only the menu sections this
   endpoint authorizes. By returning only Dexaflow-backed capabilities we make the
   UI **hide** unsupported sections (Assets, Connections, Variables, Pools,
   Backfills, Admin, …) entirely. No dead button, no SPA change.
2. **`/ui/config` (feature flags)** — disables UI features we do not back.
3. **Graceful stubs for the rest** — any `/ui/*` we do not implement returns a
   schema-valid **empty** payload (empty list / zeroed stats) so advanced views
   render an empty state instead of erroring; unsupported **write** actions
   return `501` with a `detail` hint the UI surfaces as a toast
   ("Not available in Dexaflow yet").

## The `/ui/*` surface (from `_private_ui.yaml`)

22 operations in 3.2.1, 24 in 3.3.2. Tiers map to the degradation strategy above.

| Tier | Endpoint | Response schema (authoritative) |
|---|---|---|
| **Auth** | `POST /ui/auth/token` | `GenerateTokenBody` → token |
| **Auth** | `GET /ui/auth/me` | `AuthenticatedMeResponse` |
| **Auth** | `GET /ui/auth/menus` | `MenuItemCollectionResponse` (`authorized_menu_items`, `extra_menu_items`) |
| **Core** | `GET /ui/config` | `ConfigResponse` (`instance_name`, `auto_refresh_interval`, `hide_paused_dags_by_default`, `theme`, …) |
| **Core** | `GET /ui/dags` | inline DAG collection (UI-shaped) |
| **Core** | `GET /ui/dags/{dag_id}/latest_run` | `DAGRunLightResponse` |
| **Core** | `GET /ui/structure/structure_data` | `StructureDataResponse` (`nodes`, `edges`) — graph view |
| **Core** | `GET /ui/grid/structure/{dag_id}` | grid topology |
| **Core** | `GET /ui/grid/runs/{dag_id}` | `GridRunsResponse[]` (`dag_id`, `run_id`, `state`, `run_type`, `start_date`, `end_date`, `duration`, …) |
| **Core** | `GET /ui/grid/ti_summaries/{dag_id}` | per-run task-instance summaries |
| Degrade | `GET /ui/dashboard/historical_metrics_data` | empty stats |
| Degrade | `GET /ui/dashboard/dag_stats` | empty stats |
| Degrade | `GET /ui/calendar/{dag_id}` | empty |
| **Core** | `GET /ui/gantt/{dag_id}/{run_id}` | `GanttResponse`: every attempt of every task of the run (3.3 adds `scheduled_dttm`, `queued_dttm`) |
| Degrade | `GET /ui/dependencies` | empty graph |
| Degrade | `GET /ui/backfills` | empty list |
| Degrade | `GET /ui/next_run_assets/{dag_id}` | empty |
| Degrade | `GET /ui/partitioned_dag_runs` | empty (`partitioned_dag_runs: []`, `total: 0`) |
| Degrade | `GET /ui/pending_partitioned_dag_run/{dag_id}` | `501` (no empty form for a single run) |
| Degrade | `GET /ui/dags/{dag_id}/dagRuns/{dag_run_id}/deadlines` | empty |
| Degrade | `GET /ui/dags/{dag_id}/deadlineAlerts` (3.3) | empty |
| Degrade | `GET /ui/dags/{dag_id}/dagRuns/{dag_run_id}/stats` (3.3) | `duration: null` (the stats panel stays hidden) |
| Degrade | `GET /ui/teams` | empty |
| Degrade | `GET /ui/connections/hook_meta` | empty |

Logs, trigger, clear, and pause are served by the public `/api/v2/*` Dexaflow
already exposes; the UI calls those directly.

## Serving & auth architecture

```
browser ──▶ static SPA assets (Airflow 3.3.2, unmodified)
        ──▶ /ui/*    ─┐
        ──▶ /api/v2/* ─┼─▶ dexaflow-server   (reverse proxy serves assets + routes API)
                       ─┘
```

- A reverse proxy (or a static-file route in dexaflow-server) serves the pinned
  SPA bundle and routes `/ui/*` and `/api/v2/*` to the control plane.
- **Auth (dual-path — corrected 2026-05-22).** The earlier assumption that the UI
  logs in via `POST /ui/auth/token` was **wrong**: the spec's `GenerateTokenBody`
  carries **no credentials** (only an optional `token_type`), so `/ui/auth/token`
  re-mints a token for an **already-authenticated** principal — it is not the
  login endpoint. Credential login (username/password) is the **simple-auth-manager
  `POST /auth/token`**. Dexaflow therefore implements **both**:
  - `POST /auth/token` — credential login → JWT (the real login; already existed).
  - `POST /ui/auth/token` — re-mint for an authed bearer → `{access_token,
    token_type, expires_in_seconds}`; 401 without a bearer.
  - **OPEN — verify in browser (Phase 5.2/5.3):** open DevTools → Network and
    capture which endpoint the 3.2.1 login form actually POSTs on submit. Record
    the finding here before closing the PR. The unused path stays as a graceful
    fallback (do not remove until 5.3 or Phase 6). The JWT is sent as
    `Authorization: Bearer` on subsequent calls; Dexaflow's existing JWT issuance
    backs both, secret shared via configuration.

### Dexaflow screens inside the UI

The bundle is never edited, so a screen that only Dexaflow has reaches the UI
through Airflow 3's own extension point: `GET /api/v2/plugins` lists a built-in
plugin named `dexaflow` whose `external_views` the UI turns into menu entries.
Each view is a page served by the control plane and shown in the UI's
same-origin frame at `/plugin/<url_route>`. The page calls the API with the
session cookie, so the API's checks are the only checks, and it answers with a
CSP that allows only its own script (by hash), calls to its own origin and a
same-origin frame. The plugin lists a view only when the API behind it is
mounted; with none, the response is the empty collection the stub served.

| Menu entry | View `url_route` | Page | API |
|---|---|---|---|
| User menu, **API tokens** | `api-tokens` | `/dexaflow/api-tokens` | `GET` and `DELETE /api/v2/apiTokens` ([ADR 0074](/project/adrs/0074-scoped-api-tokens/), #1886) |

## Learnings log

- **2026-05-22:** Airflow 3.x split the backend into a stable public `/api/v2/`
  and an internal, non-backward-compatible `/ui/*` (AIP-84). The React UI targets
  `/ui/*`, so `/api/v2/` parity alone never renders the UI — the original premise
  (principle #8) reflected Airflow 2.x. Authoritative `/ui` shapes live in
  `_private_ui.yaml` per tag. The UI is backend-shaped via `/ui/auth/menus` and
  `/ui/config`, which is how we hide uncovered features without touching the SPA.
- **2026-05-22 (spec corrections during 5.1).** Reconciling the implementation
  against the authoritative `_private_ui.yaml`:
  - `AuthenticatedMeResponse` is `{id, username}` only (not `name`/`is_active`/
    `is_authenticated`).
  - `MenuItem` is a fixed string enum (`Required Actions, Assets, Audit Log,
    Config, Connections, Dags, Docs, Jobs, Plugins, Pools, Providers, Variables,
    XComs`) — there is **no** "Browse > DAG Runs / Task Instances" (that was
    Airflow 2.x). Curated set starts at `[Dags, Docs]`; widen only if a browser
    test proves a missing section breaks the UI.
  - `ConfigResponse` has 12 **required** fields and **no** `assets_enabled`/
    `plugins_enabled`/`is_db_isolation_mode`. `theme` is required-but-nullable (a
    Chakra-theme object or `null`), not the string `"default"`. Menus, not config
    flags, are the lever that hides sections.
  - `/ui/auth/token` is a re-mint, not login (see Serving & auth above).
- **Strategic note:** the pinned `/ui/*` is tactical for MVP velocity; a custom
  Dexaflow UI on the stable `/api/v2/` is the long-term destination. See ADR 0018.
- **2026-05-22 (Phase 5.2 — DAG list, grid, graph).** Implemented the read views:
  - `GET /ui/dags` (DAGWithLatestDagRunsResponse, 30+ required fields),
    `GET /ui/dags/{id}/latest_run` (DAGRunLightResponse|null — 200 null, not 404),
    `GET /ui/grid/runs/{id}` (GridRunsResponse[]),
    `GET /ui/grid/structure/{id}` (GridNodeResponse[], topo-sorted),
    `GET /ui/structure/structure_data?dag_id=` (StructureDataResponse nodes+edges),
    `GET /api/v2/dags/{id}/details` (DAGDetailsResponse, 43 fields),
    `GET /api/v2/version`.
  - **`ti_summaries` is an NDJSON stream**, not a single map: `GET /ui/grid/ti_summaries/{id}?run_ids=`
    returns `application/x-ndjson`, one `GridTISummaries` per run. (The 5.2 prompt's
    "run_id→task_id→state map" was wrong; spec wins.)
  - **Impedance gaps mapped, not faked away:** `DAGRunLightResponse.id` is an
    integer in the spec but Dexaflow keys runs by `(dag_id, run_id)` — `id` is a
    stable FNV hash of run_id, a display key only; `run_after` maps to logical
    date (no separate field); `has_missed_deadline`, task groups, dynamic mapping,
    bundle/fileloc/parse metadata are absent → null/false/defaults. Topology comes
    from `dag_versions.spec` (the JSON the Python parser emitted); reads are Go.
  - **Performance posture:** `/ui/dags` latest runs via one LATERAL window query
    (no N+1); `ti_summaries` one join grouped in Go with a weak ETag over
    (count, max timestamp) since `task_instances` has no `updated_at`.
  - **OPEN (needs live PG / browser):** integration fixtures (3×10×5), the 20k-TI
    perf budget + EXPLAIN ANALYZE, and the browser walk of grid/graph rendering
    are verification steps to run with `make dev-up` and a real browser.
- **2026-05-23 (Phase 5.3 — degradation, demo stack, live smoke).**
  - Graceful stubs for unimplemented `/ui` screens return schema-valid empties
    (zeroed counts, `[]` collections, `{edges,nodes}`); writes degrade to 501.
  - `docker compose --profile demo up --build` runs the full stack with the
    embedded UI; `deploy/Dockerfile.server` builds the single image.
  - **Live container smoke** (curl, not yet a browser) confirmed: anonymous `/`
    serves the SPA shell with `<base href="/">`, `/ui/config` is public, static
    assets are gzipped, login via `POST /auth/token` works, and `/api/v2` + `/ui`
    stay gated (401) without a token. It also caught two bugs now fixed: the
    static SPA was auth-gated (couldn't reach login), and `/ui/auth/me` returned a
    blank username (JWT lacked the email claim).
  - **RESOLVED — the login flow (browser DevTools).** The 3.2.1 UI sends an
    unauthenticated user to **`GET /api/v2/auth/login`** (not `/auth/token` or
    `/ui/auth/token`). Upstream that 307-redirects into the simple-auth-manager
    login SPA, which POSTs `{username,password}` to **`POST /auth/token`** and
    stores the returned JWT in a cookie named **`_token`** (path `/`) that the
    rest of the UI reads. Dexaflow now: serves a minimal login page at
    `/api/v2/auth/login` honoring that contract (no second SPA embedded), makes
    `/api/v2/auth/` public, and **accepts the `_token` cookie** as a fallback to
    the `Authorization` header across `/api/v2` and `/ui`. `/api/v2/auth/logout`
    clears the cookie. Dexaflow departs from upstream on one point: **the cookie
    is set by the response, not by the page.** `POST /auth/token` sets it
    server-side, `HttpOnly`, exactly as the SSO callback does, and still returns
    `access_token` in the body for API clients. A page-set cookie is one a script
    can read, and, worse, one the browser silently discards when an `HttpOnly`
    cookie of the same name is already there, which is what a break-glass login
    over a live SSO session is. So the prior dual-path question (`/auth/token` vs
    `/ui/auth/token`) is settled: the credential endpoint is `/auth/token`;
    `/ui/auth/token` remains the authed re-mint.
  - **Browser walk findings (live, against the demo stack).** Logging in and
    opening the DAG views surfaced several gaps, now fixed:
    - Login is `GET /api/v2/auth/login` → `_token` cookie (resolved above).
    - The home dashboard polls **`GET /api/v2/monitor/health`** to color the
      Scheduler/Metadatabase/Triggerer/DagProcessor widgets — implemented;
      `metadatabase` is a real Postgres ping, the rest report healthy (the single
      control plane subsumes those roles).
    - Opening DAG properties fans out to public `/api/v2` list endpoints we don't
      implement (`dagTags`, `dagWarnings`, `importErrors`, `assets`,
      `assets/events`, `plugins`, `pools`, dag-run `hitlDetails`). They 404'd and
      broke the detail view; now stubbed as schema-valid empty collections, with
      real implementations tracked in issues **#26–#32**.
    - Inline http_api tasks succeeded but their logs 404'd: the distroless
      container's nonroot user could not create `/var/log/leoflow`. Fixed by
      pointing `DEXAFLOW_LOGS_DIR` at a writable path in the demo compose. Logs now
      persist and render (e.g. `inline http_api GET … -> success`).
    - Note: **http_api** ran inline (ADR 0015; removed, ADR 0047/0048); python/bash tasks need a
      Kubernetes worker pod, which the compose demo does not provide.
  - **Write flows.** trigger / clear / pause are implemented on the public API
    (`POST /api/v2/dags/{id}/dagRuns`, `POST /api/v2/dags/{id}/clearTaskInstances`,
    `PATCH /api/v2/dags/{id}`). **OPEN — confirm in the browser** which paths the
    3.2.1 UI actually calls for these actions; add `/ui/*` aliases only if it uses
    them. The browser walk + screenshots remain the final acceptance step.

## Sources

- [Apache Airflow 3 is Generally Available](https://airflow.apache.org/blog/airflow-three-point-oh-is-here/)
- [AIP-84 UI REST API](https://cwiki.apache.org/confluence/display/AIRFLOW/AIP-84+UI+REST+API)
- [Airflow REST API endpoints & data models (DeepWiki)](https://deepwiki.com/apache/airflow/9.3-rest-api-endpoints-and-data-models)

## Upgrading the bundle

The bundle is the `airflow/ui/dist` directory of the pinned Airflow release.
`make fetch-airflow-ui` extracts it from the `apache/airflow:<version>` image
(the same files ship in the `apache-airflow-core` wheel on PyPI), writes the
`VERSION` marker and rebrands the Docs and GitHub nav links. The bundle is
unverified until the contract sweep (`test/ui-contract/run.sh`) and the UI smoke
(`test/e2e/ui-smoke.js`) pass against it.

### 3.2.1 to 3.3.2 (v0.5.4)

What the 3.3 UI reads that the 3.2 UI did not, and what Dexaflow serves for it:

- **Cursor paging of the runs and task instance tables.** The 3.3 UI sends
  `cursor` (empty on the first page) and no `offset`, and moves with the
  `next_cursor` and `previous_cursor` the body names. `GET .../dagRuns` and
  `GET .../dagRuns/{dag_run_id}/taskInstances` now name them whenever the request
  carries `cursor`; a request without it gets the 3.2 body unchanged.
- **Mark as success or failed.** The dialog previews with
  `PATCH .../taskInstances/{task_id}/dry_run` and reads `task_instances` from the
  answer. Both the preview and the PATCH now answer the
  `TaskInstanceCollectionResponse` Airflow declares (a bare task instance crashed
  the dialog).
- **Clear a whole run.** `POST .../dagRuns/{dag_run_id}/clear` is implemented
  (dry run lists the affected task instances, the clear resets them and re-queues
  the run). `only_new` is refused with `400`, since Dexaflow cannot yet tell which
  tasks a newer DAG version added.
- **Gantt.** `GET /ui/gantt/{dag_id}/{run_id}` is implemented; the catch-all `{}`
  crashed the 3.3 Gantt view.
- **Pinned version on runs.** `GET .../dagRuns` and
  `GET .../dagRuns/{dag_run_id}` name the version each run is pinned to in
  `dag_versions`. The 3.3 Clear dialogs, opened from the run page or from a row
  of the runs table, read an empty list as a run carried over from Airflow 2 and
  force "Run with latest bundle version".
- **New fields.** `has_note` on grid runs, `partition_date` (always `null`) on
  runs, `awaiting_input` in the task instance state counts, and the
  `*_counts_are_lower_bounds` flags (always `false`: the dashboard counts are
  exact).

The two upstream regressions that held the bump back were re-checked against the
3.3.2 bundle in a real browser: after Mark as failed the task instance badge
updates without a reload (apache/airflow#67883, fixed upstream by
apache/airflow#67212, which 3.3.0 carries), and the page stays responsive with
one grid re-stream per mark (apache/airflow#67884 did not reproduce). After a
Clear, the log view shows one attempt tab per try and each tab loads its own
attempt (the #241 flow).

Known gaps the 3.3 UI exposes, none of them a crash:

- The note typed in the 3.3 Clear dialog is not stored (run and task instance
  notes are not editable yet).
- A run whose version is no longer registered carries no `dag_versions`, so the
  Clear dialog shows "run on latest version" as forced for it; the request it
  sends still keeps the run's own version. Every other run names its pinned
  version, in `GET .../dagRuns/{dag_run_id}` and in the run list alike.
- "Run with latest bundle version" follows Airflow 3.2
  ([ADR 0086](/project/adrs/0086-clear-rebinds-a-finished-run-with-its-task-set/)):
  a finished run with no task queued or running moves to the current version,
  which creates the task instances of the tasks that version adds and marks
  `removed` the ones of the tasks it dropped. A run that has not finished keeps
  its version and the clear applies on it.
  - **The UI does not say when the option was not applied.** Both Clear routes
    (`clearTaskInstances` and `dagRuns/{dag_run_id}/clear`), dry run included,
    name such a run in the added field `run_on_latest_version_not_applied`,
    with the reason. The 3.3.2 Clear dialogs read only the fields Airflow
    defines, so they show the usual preview and no message; the run keeps
    showing the version it had. [Clearing a run that is still
    running](/author-dags/dag-authoring/#clearing-a-run-that-is-still-running)
    shows how to read the field and how to move the run once it has finished.
- Deadline alerts and run duration stats answer empty.
- The calls below are not served yet. Each one answers `501` with the detail
  "... is not available in Dexaflow yet", which the UI shows as an error toast,
  and a list read answers its empty collection, so no view breaks on them:
  - The bulk actions of the runs table (select runs, then "Clear Dag Runs",
    "Mark Dag Runs as..." or "Delete Dag Runs"):
    `POST /api/v2/dags/{dag_id}/clearDagRuns` (dry run preview included) and
    the bulk `PATCH /api/v2/dags/{dag_id}/dagRuns`. Clear, mark or delete the
    runs one at a time from each run.
  - Marking a whole task group:
    `PATCH .../dagRuns/{dag_run_id}/taskGroupInstances/{group_id}` and its
    `/dry_run`. Mark the task instances one at a time.
  - Clearing partitions: `POST /api/v2/dags/{dag_id}/clearPartitions`.
  - The asset and task state stores: `/api/v2/assets/{asset_id}/state-store`
    and `.../taskInstances/{task_id}/state-store`, each with `/{key}`. The two
    list reads answer an empty collection; reading one key, writing
    (`PUT`, `PATCH`) and deleting answer `501`.
  - Partitioned runs: `GET /ui/partitioned_dag_runs` answers an empty list and
    `GET /ui/pending_partitioned_dag_run/{dag_id}` answers `501`.
  - Bulk actions on task instances (`PATCH .../dagRuns/{dag_run_id}/taskInstances`)
    and deleting a task instance (`DELETE .../taskInstances/{task_id}`). Clear
    or mark the task instances one at a time.
  - Bulk edits of variables and connections (`PATCH /api/v2/variables`,
    `PATCH /api/v2/connections`). Edit or delete them one at a time.
  - Creating, updating and deleting XCom entries under
    `.../taskInstances/{task_id}/xcomEntries`. Reading them works.
  - Reparsing a DAG file (`PUT /api/v2/parseDagFile/{file_token}`). Dexaflow
    registers DAGs with `dexaflow deploy` or `dexaflow push`, not by parsing a
    DAGs folder.
  - Asset events: `GET .../dagRuns/{dag_run_id}/upstreamAssetEvents` and the
    queued event reads (`/api/v2/assets/{asset_id}/queuedEvents`,
    `/api/v2/dags/{dag_id}/assets/queuedEvents` and
    `/api/v2/dags/{dag_id}/assets/{asset_id}/queuedEvents`) answer empty
    collections; clearing queued events (`DELETE` on those paths) answers `501`.

