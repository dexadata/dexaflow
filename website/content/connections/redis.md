---
# --- AUTO redirect aliases (build_redirects.py) — do not edit by hand ---
aliases:
  - /connections/redis.html
# --- end AUTO redirect aliases ---
title: Redis connection
linkTitle: Redis
weight: 410
description: Redis connection
---

Connect a task to a Redis instance for caching, queues, distributed locks,
or small key-value side state. Unlike the SQL-family entries, Redis has no
schemas / tables — data lives in numeric **databases** (0..15 by default)
and the value model is key + (string | hash | list | set | stream | ...).

## URI shape

```
redis://[<user>:<password>@]<host>:<port>/<db_index>
```

The Schema field carries the **db index** (a number), not a schema name.
Examples:

| Connection fields | URI |
|---|---|
| host=`redis.internal` port=`6379` schema=`0` | `redis://redis.internal:6379/0` |
| host=`r` port=`6379` schema=`3` password=`s3cret` | `redis://:s3cret@r:6379/3` |
| host=`r` user=`etl` password=`s3cret` schema=`0` | `redis://etl:s3cret@r:6379/0` |

Legacy Redis (≤ 5) only had a single password (the `AUTH <password>`
command), which the URI carries as `redis://:<password>@...` — an empty
username with a populated password is valid. Redis 6+ has ACL (a real
username), which fills both Login and Password.

## Fields the UI asks for

| Field | Required | Notes |
|---|---|---|
| Conn Id | yes | e.g. `redis_target`. Exported as `AIRFLOW_CONN_REDIS_TARGET`. |
| Conn Type | yes | `redis`. |
| Host | yes | Hostname or IP. |
| Port | usually `6379` | Default for the redis protocol; `6380` for the TLS port on some managed services. |
| Schema | yes | The **db index** as a string, e.g. `0`. Default is `0`. |
| Login | optional | The ACL username (Redis 6+). Leave blank for legacy AUTH. |
| Password | optional | The AUTH password / ACL password. Encrypted at rest. |
| Extra | optional | JSON for connect-time options, e.g. `{"ssl": true, "socket_timeout": 5}`. The DAG passes these to `redis.Redis.from_url`. |

## How user code reads it

`redis.Redis.from_url` accepts the full URI directly:

```python
import os, redis

uri = os.environ["AIRFLOW_CONN_REDIS_TARGET"]
client = redis.Redis.from_url(uri, decode_responses=True)
client.set("hello", "world")
print(client.get("hello"))
```

`from_url` parses the user/password, picks the db index from the path, and
opens the connection. No path-extraction step like sqlite needs.

## TLS (`rediss://`)

Most managed Redis services (AWS ElastiCache with in-transit encryption,
Upstash, etc.) require TLS. The standard URI scheme for that is `rediss://`
(double-s), but the Connection's Conn Type field still uses `redis` —
carry the TLS flag in Extra instead:

```json
{"ssl": true, "ssl_cert_reqs": "required"}
```

`redis.Redis.from_url` honours these. A dedicated `rediss` recipe is a
planned follow-up; for now, `redis` + Extra is the supported path.

## Example DAG

[`examples/redis_load`](https://github.com/dexadata/dexaflow/tree/main/examples/redis_load) writes 20 hash fields under
`leoflow:example_load` using `redis-py`. The example's
[README](https://github.com/dexadata/dexaflow/tree/main/examples/redis_load/README.md)
walks through Connection setup and verification with `redis-cli HGETALL`.

## Lite vs Pro caveats

- **Lite (subprocess)** runs the task on the host. Point the Connection at
  any reachable Redis (a local docker run, a remote instance).
- **Lite (k3d)** runs the task in a pod. Use `host.k3d.internal` to reach
  a host-network Redis, or deploy Redis inside the cluster.
- **Pro (Kubernetes)** typical pattern is the redis-operator or a managed
  service (ElastiCache, Memorystore). For at-rest persistence, make sure
  Redis is configured with AOF or RDB; the Dexaflow Connection itself is
  metadata only.

## Tier 1 integration test

Redis is included in `TestConnectionDeliveryChainOfCustodyIntegration`
(the table-driven SQL-family test in `internal/storage/`). It runs on
every PR with no extra service container — Redis is already a CI service
for the Dexaflow control plane (see `.github/workflows/ci.yaml`), so the
Tier 1 cost is zero (see [#162](https://github.com/dexadata/dexaflow/issues/162)).

## Troubleshooting

| Symptom | Cause | Fix |
|---|---|---|
| `ConnectionError: Error 111 connecting to ...` | Host/port wrong, or Redis bound to localhost only | Check `bind` in `redis.conf`; from inside k3d use `host.k3d.internal`. |
| `NOAUTH Authentication required` | Connection has no password but Redis has `requirepass` | Set the password in the Connection. |
| `WRONGPASS invalid username-password pair` | ACL user/password mismatch (Redis 6+) | Confirm the ACL with `ACL WHOAMI` on the server. |
| `WRONGTYPE Operation against a key holding the wrong kind of value` | Reusing a key with a different type | Delete the key first or pick a new key namespace. |
| Wrong db index | Schema field empty or wrong number | Set Schema to the intended db index (default `0`). |

## Related

- ADR 0019 — secret encryption at rest.
- ADR 0021 — agent secret delivery (`AIRFLOW_CONN_<CONN_ID>`).
- #73 — redis connector umbrella.
- #138 — chain-of-custody contract test (redis is the fifth conn_type covered).
- #142 — the connector cookbook umbrella.
- #162 — tiered integration-test pipeline (redis is Tier 1).
