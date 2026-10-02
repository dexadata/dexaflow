# lifecycle example

A three-task pipeline (`extract → transform → load`) that passes data between
tasks via XCom. Each task prints, sleeps, and prints again, so a run shows real
durations and multi-line logs in the UI — a good smoke test for the full
pod-per-task path (parser → image → dispatch → agent → logs/XCom).

## What it exercises

- **TaskFlow value passing (XCom):** `extract` returns `{"rows": 100}`,
  `transform` doubles it, `load` consumes the result.
- **Logs:** each task emits several lines across ~4s of work.
- **Sequencing:** tasks run in dependency order, each in its own pod.

## Run it

From this directory, with a control plane running (see the repo `README.md` /
`docker compose --profile demo up`) and the base image available:

```sh
# 1. Compile the project into dag.json (and build the image).
dexaflow compile . --image dexaflow-lifecycle:dev --build -o dag.json

# 2. (Local k3d only) import the image into the cluster.
k3d image import dexaflow-lifecycle:dev --cluster <cluster>

# 3. Push the compiled DAG to the control plane.
TOKEN=$(leoflow auth create-token --username admin@leoflow.local --password admin)
dexaflow push dag.json --token "$TOKEN"

# 4. Trigger a run from the UI, or:
curl -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{}' http://localhost:8080/api/v2/dags/lifecycle/dagRuns
```

The compiled `dag.json` is a build artifact and is not checked in.
