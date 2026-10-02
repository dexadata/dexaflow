---
# --- AUTO redirect aliases (build_redirects.py) — do not edit by hand ---
aliases:
  - /connections/kafka.html
# --- end AUTO redirect aliases ---
title: Apache Kafka connection
linkTitle: Apache Kafka
weight: 270
description: Apache Kafka connection
---

Produce / consume Kafka from a task via a managed Dexaflow Connection and the
Apache Kafka provider hooks. The conn_type is `kafka` (from
`apache-airflow-providers-apache-kafka`).

## Declare the provider

```yaml
# dexaflow.yaml
dag_id: kafka_produce
connectors:
  - kafka
```

## Fields the UI asks for

A Kafka connection is **all Extra**: the entire confluent-kafka client config goes
into the Extra JSON. There is no host/login — the brokers and security live in the
config dict.

| Field | Value |
|---|---|
| Conn Id | e.g. `kafka_default`. |
| Extra | The client config, e.g. `{"bootstrap.servers": "broker1:9092,broker2:9092", "security.protocol": "SASL_SSL", "sasl.mechanism": "PLAIN", "sasl.username": "etl", "sasl.password": "..."}` |

The whole config (including `sasl.password`) is encrypted at rest (ADR 0019); the
Extra round-trip is pinned by `TestKafkaConnectionURIShapeIntegration`.

## Example DAG (copy-paste)

Docs-only recipe (needs a Kafka cluster). The hook is imported **inside** the
task.

```python
# dag.py
from __future__ import annotations

from airflow.sdk import DAG, task


@task
def produce() -> None:
    from airflow.providers.apache.kafka.hooks.produce import KafkaProducerHook

    hook = KafkaProducerHook(kafka_config_id="kafka_default")
    producer = hook.get_producer()
    print("produce: sending message to topic 'events'")
    producer.produce("events", value=b"hello from leoflow")
    producer.flush()
    print("produce: ok")


with DAG("kafka_produce", schedule=None, catchup=False, tags=["example"]):
    produce()
```

```yaml
# dexaflow.yaml
schema_version: "1.0"
dag_id: kafka_produce
description: Produce a message to Kafka via KafkaProducerHook.
owner: examples
tags:
  - example
python_version: "3.11"
connectors:
  - kafka
```

### Run it

1. **Admin → Connections → +**, type `kafka`. Put the client config in Extra.
2. `dexaflow lite path/to/this/dag` → trigger `kafka_produce`.

## Security notes

- The SASL password sits inside the Extra config — it is encrypted at rest. Prefer
  `SASL_SSL` over plaintext.
- **Never `print()` the Extra** — it carries the SASL credentials.

## Related

- `docs/connections/index.md` — *Installing a connector's provider*.
- ADR 0019 / 0021 — secret encryption + agent delivery.
