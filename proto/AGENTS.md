# proto/

Rules for changing the agent protocol: `agent.proto`, the gRPC contract
between the control plane and the agent in every task pod. The root
[AGENTS.md](../AGENTS.md) applies too. This directory is security sensitive:
CONTRIBUTING.md ("Security-Sensitive Changes") does not accept changes here from
first-time contributors without a discussion issue first.

## Agents and servers of different versions talk to each other

The agent is built into the task base image (`runtime/Dockerfile`), and a DAG
image keeps the agent it was built with. After an upgrade, a new control plane
serves agents from older images, and during a rolling upgrade an agent can
reach a replica older than itself. Both directions have to work.

- **The protocol only gains things.** Add fields and RPCs; never renumber,
  retype or remove a field, and never rename an RPC. The agent protocol is a
  public surface that a patch may only extend
  ([ADR 0068](https://dexaflow.dexadata.ai/project/adrs/0068-patch-content-gated-by-safety/),
  safety bar point 4). `buf.yaml` declares the `FILE` breaking rules; CI does
  not run them, so check locally with
  `buf breaking --against '.git#branch=main'` against an up-to-date `main`.
- **A new agent handles an old control plane.** An RPC the server does not know
  answers `Unimplemented`, and the agent falls back to the older call: see
  `FetchXComBatch` in `agent.proto` and `internal/agent/xcom_prefetch.go`, with
  the fallback tested in `internal/agent/xcom_batch_test.go`.
- **A new control plane handles an old agent.** An old agent never sends a new
  field, so the server reads its absence safely: a token minted before the
  attempt epoch is read as epoch 0
  (`attemptEpochArg` in `internal/storage/agent_store.go`).
- **The generated code is committed and must match.** Edit `agent.proto`, run
  `make proto` (`buf generate`), and commit `proto/agent/v1/`. The
  `proto-drift` job in `ci.yaml` regenerates it with pinned plugins and fails
  on any difference.
- **Every RPC is authenticated and scoped.** The server authenticates each call
  by the agent's per-attempt token and scopes it to that token's tenant and task
  instance (`internal/agentrpc`, `internal/auth/AGENTS.md`).
