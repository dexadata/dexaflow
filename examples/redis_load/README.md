# redis_load — write a hash into an external Redis via a managed Connection

This example is a **test DAG**: it exercises the connection-delivery contract
end-to-end against a real Redis, mirroring `postgres_load` and `mysql_load`.
Running it is the manual companion to the Go-side chain-of-custody test
(`TestConnectionDeliveryChainOfCustodyIntegration`, which now covers Redis as
the fifth conn_type).

## What it tests

1. Admin creates a `redis` Connection in the UI.
2. The control plane encrypts and stores it (ADR 0019).
3. The agent fetches the URI via gRPC and exports it as
   `AIRFLOW_CONN_REDIS_TARGET`.
4. The user task `load()` reads the env var, opens `redis.Redis.from_url`,
   and writes 20 hash fields under `dexaflow:example_load`.
5. You verify the keys exist with `redis-cli`.

Without a Connection, `load()` falls back to a hardcoded local URI so the
example also runs in a quick demo on a developer machine.

## How to run it (Lima / subprocess executor)

### 1. Spin up a target Redis

```sh
docker run --rm -d --name dexaflow-redis \
  -p 56379:6379 \
  redis:7
```

The DAG defaults to `redis://host.k3d.internal:56379/0` which works inside a
k3d cluster. From the host or via subprocess, use `localhost:56379`.

### 2. Create the Connection in the UI

Open `http://localhost:8088` → **Admin → Connections → +**.

| Field | Value |
|---|---|
| Conn Id | `redis_target` |
| Conn Type | `redis` |
| Host | `localhost` (host) or `host.k3d.internal` (k3d) |
| Schema | `0` (the Redis db index, 0–15 by default) |
| Login | _(blank)_ |
| Password | _(blank, unless your Redis requires AUTH)_ |
| Port | `56379` |

Save. The UI never shows the password again — it is encrypted at rest.

### 3. Trigger the DAG

```sh
dexaflow lite path/to/this/example
```

In the UI: open `redis_load` → **Trigger DAG**.

### 4. Verify

```sh
docker exec dexaflow-redis redis-cli HGETALL dexaflow:example_load | head
docker exec dexaflow-redis redis-cli HLEN dexaflow:example_load
```

Expected: 20 fields. `HGETALL` shows `cat_0`..`cat_19` keys with the computed
score values.

The task logs in the UI also report
`load: connecting via managed Connection redis_target` (vs the fallback URI
banner if the env var is missing).

## Notes that make this connector different

- **Schema is a db index**, not a name. Redis namespaces data into numeric
  databases (default 0..15). The Connection's Schema field carries this
  number; the URI builder renders it as the path component, so the final
  URI is `redis://host:port/0`.
- **No login is fine.** Pre-ACL Redis used `AUTH <password>` only; the URI
  shape is `redis://:<password>@host:port/<db>` — empty username, populated
  password. With Redis 6+ ACL, set both Login and Password.
- **No TLS in this example.** For `rediss://` (TLS), set Conn Type to
  `redis` but use Extra to carry `{"ssl": true}`. We'll add a dedicated TLS
  recipe later; out of scope here.
- **Tier 1 in CI** (#162) — redis already runs as a service in CI, so the
  chain-of-custody test for redis runs on every PR at zero extra cost.

## What can go wrong

- **AIRFLOW_CONN_REDIS_TARGET not set** → the DAG falls back to the hardcoded
  URI. If that URI does not match your target, the connect fails. The log
  line tells you which path was taken.
- **WRONGTYPE / wrong db index** — if you reuse `dexaflow:example_load` for a
  string key in db 0, the `HSET` errors with `WRONGTYPE`. The DAG deletes
  the key first to avoid this.
- **Password with reserved characters** (`@`, `:`, `/`, `?`, `#`) — handled
  by the same percent-escape path as the SQL family; the chain-of-custody
  test pins this.

## Related

- `docs/connections/redis.md` — the cookbook entry for the redis connector
  (URI shape, ACL semantics, supported drivers).
- ADR 0019 — secret encryption at rest.
- ADR 0021 — agent secret delivery (`AIRFLOW_CONN_<CONN_ID>`).
- Issue #142 — connector cookbook umbrella.
- Issue #73 — redis connector umbrella.
