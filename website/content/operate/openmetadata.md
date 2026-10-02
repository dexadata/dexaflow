---
title: "Cataloguing Dexaflow in OpenMetadata"
linkTitle: "OpenMetadata"
weight: 75
description: >
  Point OpenMetadata's Airflow connector at Dexaflow over REST, and understand
  what it does and does not give you.
---

Dexaflow serves Airflow's API shape, so OpenMetadata's Airflow connector can
catalogue Dexaflow pipelines over REST with no plugin and no database access.

## What you get, and what you do not

You get the pipeline, its task graph, run history with per-task status, owners
and tags, and working "view in source" links back into the Dexaflow UI.

You do **not** get table lineage. OpenMetadata's REST-based Airflow connector
returns no lineage by construction: its `yield_pipeline_lineage_details` returns
an empty list with no setting that changes it. Table lineage from OM's Airflow
connector exists only on the path that reads Airflow's metadata database
directly, and Dexaflow has no such database. Table lineage for Dexaflow comes from
OpenLineage emission, tracked in
[ADR 0059](/project/adrs/0059-openlineage-emission/).

You also do not get the schedule. On the v2 API OpenMetadata reads a pipeline's
`scheduleInterval` from `timetable_summary`, which Dexaflow does not emit yet, so
the field lands empty. The schedule is visible in the Dexaflow UI the catalogued
`sourceUrl` links back to.

## Configuration

Create a service account first. The connector needs exactly four permissions:
`read:dag`, `read:dag_run`, `read:task_instance` and `read:task`. The built-in
`viewer` role covers them, and grants five more reads on top (`xcom`, `pool`,
`connection`, `variable`, `config`); see `migrations/025_role_ladder.up.sql`. If
that is wider than you want a catalog to hold, grant the four permissions
directly rather than the role.

In OpenMetadata, add an **Airflow** pipeline service with the **REST API**
connection:

| Field | Value |
|---|---|
| `hostPort` | `http://leoflow:8080` |
| `apiVersion` | `auto` |
| Auth | Basic, with the service account's username and password |

`hostPort` must be the **bare root**. OpenMetadata appends `api` and the version
itself, so it requests `http://leoflow:8080/api/v2/version`. Adding `/api`
yourself produces `/api/api/v2/...`, which 404s; with `apiVersion: auto` that
404 makes version detection fall through to `v1` and every later call 404s too.
The symptom is a service that looks entirely unreachable.

Basic auth works because Dexaflow serves `POST /auth/token` with the same request
and response shape OpenMetadata expects, and accepts the resulting bearer token
on every subsequent call.

## Checking that it worked

A green **Test Connection** is not enough. That probe checks reachability, and
it deliberately accepts any body from the tasks endpoint, so it passes even when
every DAG is being rejected during ingestion.

Run the ingestion workflow and check that the number of pipelines created equals
the number of DAGs, and that the run reports **zero failed records**. A workflow
that reports success with every DAG in its failure list is the shape to look for.

## Deploying and running Dexaflow from the OpenMetadata UI

Not supported. OpenMetadata's "deploy" model writes a generated Python file into
an Airflow DAGs folder and imports it in-process; a Dexaflow DAG is a compiled,
published image, so there is no folder to write to and nothing to import. If your
OpenMetadata is not orchestrating its own ingestions through Dexaflow, set
`pipelineServiceClientConfiguration.enabled: false` so OM stops health-checking a
component it is not using.
