---
aliases:
  - /operate/lite-to-pro/
title: From Lite to Cluster
weight: 14
description: What stays the same and what changes when a DAG you ran with dexaflow lite moves to a Cluster control plane.
---

A DAG you ran with `dexaflow lite` moves to a Cluster control plane without a
rewrite. The project is the same two files, `dag.py` and `dexaflow.yaml`, and
the same workspace; what changes is where the image lives, who holds the
connections and how the DAG starts. The steps are in
[Deploy your first Cluster DAG](/operate/first-cluster-dag/); this page lists the
differences so none of them surprises you.

## What stays the same

- **The project.** `dag.py`, `dexaflow.yaml`, the workspace file and the
  `dag_id` do not change, and `python_version`, `dependencies` and `connectors`
  mean the same thing on both.
- **The API and the UI.** Both serve the Airflow-compatible `/api/v2/` and the
  Airflow UI, so what you checked in Lite's UI reads the same on Cluster.
- **The CLI.** `dexaflow runs`, `dexaflow dags`, `dexaflow connections` and the
  other server commands take `--target <name>` and work against either.

## What changes

| | Lite | Cluster |
|---|---|---|
| Start | `dexaflow lite`, which watches the workspace and re-registers on save | `dexaflow deploy`, once per change, by hand or from CI ([Redeploy on save](/operate/first-cluster-dag/#redeploy-on-save) with `--watch`) |
| Server | on your machine, chosen for you | a target you add once: `dexaflow target add prod <url> --registry <prefix>` |
| Sign-in | the admin `dexaflow setup` created (its email from the config file, `admin@dexaflow.local` by default); with no setup, Lite runs without sign-in, bound to this machine | `dexaflow auth login --target prod`: the cluster's single sign-on, or a password |
| Image | built into the local cluster (none with `--executor=subprocess`), never pushed | built for the cluster's platform (`linux/amd64` by default), pushed to your registry and registered by digest |
| Registry | none | one your cluster can pull from, with a credential for the CLI: a `gcloud`, `aws` or `az` session for Artifact Registry, ECR and ACR, `dexaflow registry login` for others, and the container tool's own login when it builds ([Registry credentials](/operate/builders/#registry-credentials)) |
| A new DAG | registered active, so its schedule runs from the first save | registered **paused**, as in Airflow, until someone unpauses it; `deploy --active` does it on the first deploy ([New DAGs start paused](/concepts/core-concepts/#new-dags-start-paused)) |
| Connections and variables | on the local server, seeded from `AIRFLOW_CONN_<ID>` in your environment when the DAG declares them | stored on the control plane: create them there before the first run, in the UI or with `dexaflow connections set --target prod` and `dexaflow variables set --target prod` ([Variables & Connections](/author-dags/variables-connections/)) |

## Before the first deploy

1. **Delete a Dockerfile that Lite wrote.** The k3d mode of `dexaflow lite`
   writes a `Dockerfile` next to `dag.py`, `FROM` the `leoflow-base` image it
   builds on your machine, and leaves it there. A `Dockerfile` in the project
   is built verbatim by `deploy`, so `dexaflow.yaml`'s dependencies stop
   reaching the image, and its base exists on no other machine. Remove it, or
   see [your own Dockerfile](/operate/first-cluster-dag/#advanced-your-own-dockerfile)
   if you need one.
2. **Create the connections the DAG uses on Cluster.** They are never copied from
   Lite or baked into the image. `deploy` prints
   `note: this DAG expects connection(s): …` for the ones the DAG declares.
3. **Check the platform.** A laptop on arm64 still builds `linux/amd64` by
   default. For a cluster on arm64 nodes, set `--platform linux/arm64` on
   `dexaflow target add` or `build.platforms` in `dexaflow.yaml`, or let the
   platform team set it in the cluster's deploy defaults
   ([Choosing the platform](/operate/builders/#choosing-the-platform)).

Then deploy, start the DAG and watch its first run:

```bash
dexaflow deploy <dag_id> --active --trigger --follow
```
