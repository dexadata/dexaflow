---
title: Connections
weight: 30
description: "The connector matrix: install a provider, then wire a Connection. 54 recipes grouped by category, each with its conn_id and conn_type."
cascade: { type: docs }
menu: { main: { weight: 30 } }
---

A Connection is a piece of credentialised configuration the control plane
encrypts at rest (ADR 0019) and delivers to a running task as
`AIRFLOW_CONN_<CONN_ID>` (ADR 0021). User code (Python `psycopg2`,
`requests`, your operator of choice) reads the env var and connects.

This page is the index. Each linked entry below is a focused recipe with the
URI shape, an example DAG, and how to test it.

{{% alert title="Coverage" color="info" %}}
Only a subset of Airflow's standard connection types are documented +
tested so far. The list below grows as we land them.
{{% /alert %}}

## Installing a connector's provider

A Connection only carries credentials. To *use* a connector — whether through
its Airflow hook (`PostgresHook`) or a raw driver (`psycopg2`) — the matching
Python package has to be in the image / venv. Dexaflow gives you two ways to
declare that in `dexaflow.yaml`, and you pick whichever fits:

{{< tabpane text=true >}}
{{% tab header="connectors: (the sugar)" %}}

```yaml
dag_id: my_pipeline
connectors:
  - postgres
  - http
```

Short names you don't have to memorise. At compile, each expands to its
`apache-airflow-providers-*` package (ADR 0038), which pulls the hook **and**
its driver transitively — so `connectors: [postgres]` is enough to use either
`PostgresHook` or raw `psycopg2`. One line per connector, no version to recall.
{{% /tab %}}
{{% tab header="dependencies: (the escape hatch)" %}}

```yaml
dag_id: my_pipeline
dependencies:
  - apache-airflow-providers-postgres==6.0.0   # pin the provider, or…
  - psycopg2-binary==2.9.10                    # …just the driver, your call
```

Full control: pin an exact version, install only the driver, or add a package
the catalog doesn't know about. Advanced users who already think in pip
specifiers stay here.
{{% /tab %}}
{{< /tabpane >}}

Both lists are additive — the effective install is `expand(connectors) +
dependencies`. A name in `connectors:` that isn't in the catalog **fails the
compile** with the offender, the known list, and a pointer to `dependencies:`,
so a typo never slips through to a runtime `ModuleNotFoundError` in the task pod.

{{% alert title="Import provider hooks inside the task function" color="success" %}}
Dexaflow parses your DAG without providers installed (the parser only needs the
DAG's *shape*). So put hook/operator imports **inside** the `@task` body, not at
the module top level:

```python
@task
def load():
    from airflow.providers.postgres.hooks.postgres import PostgresHook  # here
    hook = PostgresHook(postgres_conn_id="pg_target")
    ...
```

A provider import at the module top level fails the compile with an actionable
message telling you to move it into the task and declare it via `connectors:`.
The import works at runtime because `connectors:`/`dependencies:` installed the
provider into the task image / venv.
{{% /alert %}}

### Connector → provider package

The catalog is **generated** from a real Airflow install (`scripts/gen_connectors.py`
→ `internal/connectors/catalog.json`, ADR 0039), so ~86 connector types ship with
a dropdown entry, the correct standard-field behavior, and the provider-specific
**custom fields** served to the form (e.g. Snowflake's `account`/`warehouse`, the
GCP keyfile) in the exact param-spec shape the Airflow 3.2 `FlexibleForm` consumes.
It is the single source of truth shared by the admin form, the sugar expansion, and
compile validation.

{{% alert title="What's verified" color="info" %}}
The field *metadata* is verified end-to-end: it is generated from Airflow's own
serializer and pinned by a Go test (`TestConnectionHookMetaExtraFieldsFlexibleFormShape`),
and the delivered `AIRFLOW_CONN_*` URI is accepted by real Airflow's
`Connection.from_uri`. The one piece confirmed only by manual UI check is the
SPA *visually rendering* those custom inputs. For a connector whose creds live
in Extra, you can always fall back to the raw **Extra (JSON)** field.
{{% /alert %}}

The common head:

| `connectors:` name | pip package |
|---|---|
| `postgres` | `apache-airflow-providers-postgres` |
| `mysql` | `apache-airflow-providers-mysql` |
| `sqlite` | `apache-airflow-providers-sqlite` |
| `mssql` | `apache-airflow-providers-microsoft-mssql` |
| `oracle` | `apache-airflow-providers-oracle` |
| `redis` | `apache-airflow-providers-redis` |
| `mongo` | `apache-airflow-providers-mongo` |
| `http` | `apache-airflow-providers-http` |
| `aws` | `apache-airflow-providers-amazon` |
| `google_cloud_platform` (alias `gcp`, `google`) | `apache-airflow-providers-google` |
| `snowflake` | `apache-airflow-providers-snowflake` |
| `ssh` | `apache-airflow-providers-ssh` |
| `ftp` | `apache-airflow-providers-ftp` |
| `sftp` | `apache-airflow-providers-sftp` |
| `kafka` | `apache-airflow-providers-apache-kafka` |

The package boundary is **not** a mechanical join of the dotted hook path
(`amazon.aws` → `amazon`; `microsoft.mssql` → `microsoft-mssql`), which is
exactly why the mapping is curated rather than derived.

When you press **Test** on a connection of a known type, the result line also
reminds you to declare the provider (`connectors: [<type>]`) — a Connection in the
UI carries credentials, but the DAG still has to install the hook to use them.

## Connectors by category

Every documented connector grouped by domain — its `conn_type`, the provider package the `connectors:` sugar expands to, and its category. Click a name for the full recipe (URI shape, example DAG, how to test).

### Databases

| Connector | `conn_type` | Provider package | Category |
|---|---|---|---|
| [cassandra](/connections/cassandra/) | `cassandra` | `apache-airflow-providers-apache-cassandra` | Databases |
| [druid](/connections/druid/) | `druid` | `apache-airflow-providers-apache-druid` | Databases |
| [elasticsearch](/connections/elasticsearch/) | `elasticsearch` | `apache-airflow-providers-elasticsearch` | Databases |
| [hive_cli](/connections/hive_cli/) | `hive_cli` | `apache-airflow-providers-apache-hive` | Databases |
| [hiveserver2](/connections/hiveserver2/) | `hiveserver2` | `apache-airflow-providers-apache-hive` | Databases |
| [influxdb](/connections/influxdb/) | `influxdb` | `apache-airflow-providers-influxdb` | Databases |
| [jdbc](/connections/jdbc/) | `jdbc` | `apache-airflow-providers-jdbc` | Databases |
| [mongo](/connections/mongo/) | `mongo` | `apache-airflow-providers-mongo` | Databases |
| [mssql](/connections/mssql/) | `mssql` | `apache-airflow-providers-microsoft-mssql` | Databases |
| [mysql](/connections/mysql/) | `mysql` / `mariadb` | `apache-airflow-providers-mysql` | Databases |
| [neo4j](/connections/neo4j/) | `neo4j` | `apache-airflow-providers-neo4j` | Databases |
| [oracle](/connections/oracle/) | `oracle` | `apache-airflow-providers-oracle` | Databases |
| [pinot](/connections/pinot/) | `pinot` | `apache-airflow-providers-apache-pinot` | Databases |
| [postgres](/connections/postgres/) | `postgres` | `apache-airflow-providers-postgres` | Databases |
| [presto](/connections/presto/) | `presto` | `apache-airflow-providers-presto` | Databases |
| [redis](/connections/redis/) | `redis` | `apache-airflow-providers-redis` | Databases |
| [sqlite](/connections/sqlite/) | `sqlite` | `apache-airflow-providers-sqlite` | Databases |
| [trino](/connections/trino/) | `trino` | `apache-airflow-providers-trino` | Databases |
| [vertica](/connections/vertica/) | `vertica` | `apache-airflow-providers-vertica` | Databases |

### Cloud

| Connector | `conn_type` | Provider package | Category |
|---|---|---|---|
| [athena](/connections/athena/) | `athena` | `apache-airflow-providers-amazon` | Cloud |
| [aws](/connections/aws/) | `aws` | `apache-airflow-providers-amazon` | Cloud |
| [azure](/connections/azure/) | `wasb` / `adls` / `azure_*` | `apache-airflow-providers-microsoft-azure` | Cloud |
| [databricks](/connections/databricks/) | `databricks` | `apache-airflow-providers-databricks` | Cloud |
| [docker](/connections/docker/) | `docker` | `apache-airflow-providers-docker` | Cloud |
| [emr](/connections/emr/) | `emr` | `apache-airflow-providers-amazon` | Cloud |
| [gcpbigquery](/connections/gcpbigquery/) | `gcpbigquery` | `apache-airflow-providers-google` | Cloud |
| [gcpcloudsql](/connections/gcpcloudsql/) | `gcpcloudsql` | `apache-airflow-providers-google` | Cloud |
| [gcpssh](/connections/gcpssh/) | `gcpssh` | `apache-airflow-providers-google` | Cloud |
| [google_cloud_platform](/connections/google_cloud_platform/) | `google_cloud_platform` | `apache-airflow-providers-google` | Cloud |
| [http](/connections/http/) | `http` / `https` | `apache-airflow-providers-http` | Cloud |
| [livy](/connections/livy/) | `livy` | `apache-airflow-providers-apache-livy` | Cloud |
| [redshift](/connections/redshift/) | `redshift` | `apache-airflow-providers-amazon` | Cloud |
| [snowflake](/connections/snowflake/) | `snowflake` | `apache-airflow-providers-snowflake` | Cloud |
| [spark](/connections/spark/) | `spark` / `spark_sql` / `spark_connect` / `spark_jdbc` | `apache-airflow-providers-apache-spark` | Cloud |

### Messaging

| Connector | `conn_type` | Provider package | Category |
|---|---|---|---|
| [discord](/connections/discord/) | `discord` | `apache-airflow-providers-discord` | Messaging |
| [imap](/connections/imap/) | `imap` | `apache-airflow-providers-imap` | Messaging |
| [kafka](/connections/kafka/) | `kafka` | `apache-airflow-providers-apache-kafka` | Messaging |
| [opsgenie](/connections/opsgenie/) | `opsgenie` | `apache-airflow-providers-opsgenie` | Messaging |
| [pagerduty](/connections/pagerduty/) | `pagerduty` | `apache-airflow-providers-pagerduty` | Messaging |
| [slack](/connections/slack/) | `slack` / `slackwebhook` | `apache-airflow-providers-slack` | Messaging |
| [smtp](/connections/smtp/) | `smtp` | `apache-airflow-providers-smtp` | Messaging |
| [telegram](/connections/telegram/) | `telegram` | `apache-airflow-providers-telegram` | Messaging |

### BI & SaaS

| Connector | `conn_type` | Provider package | Category |
|---|---|---|---|
| [datadog](/connections/datadog/) | `datadog` | `apache-airflow-providers-datadog` | BI & SaaS |
| [dbt_cloud](/connections/dbt_cloud/) | `dbt_cloud` | `apache-airflow-providers-dbt-cloud` | BI & SaaS |
| [gcp_looker](/connections/gcp_looker/) | `gcp_looker` | `apache-airflow-providers-google` | BI & SaaS |
| [github](/connections/github/) | `github` | `apache-airflow-providers-github` | BI & SaaS |
| [google_ads](/connections/google_ads/) | `google_ads` | `apache-airflow-providers-google` | BI & SaaS |
| [msgraph](/connections/msgraph/) | `msgraph` | `apache-airflow-providers-microsoft-azure` | BI & SaaS |
| [powerbi](/connections/powerbi/) | `powerbi` | `apache-airflow-providers-microsoft-azure` | BI & SaaS |
| [salesforce](/connections/salesforce/) | `salesforce` | `apache-airflow-providers-salesforce` | BI & SaaS |
| [tableau](/connections/tableau/) | `tableau` | `apache-airflow-providers-tableau` | BI & SaaS |
| [zendesk](/connections/zendesk/) | `zendesk` | `apache-airflow-providers-zendesk` | BI & SaaS |

### Files

| Connector | `conn_type` | Provider package | Category |
|---|---|---|---|
| [file-transfer](/connections/file-transfer/) | `ssh` / `sftp` / `ftp` | `apache-airflow-providers-ssh` / `-sftp` / `-ftp` | Files |
| [samba](/connections/samba/) | `samba` | `apache-airflow-providers-samba` | Files |

## Connector matrix

Every documented connector, its `conn_type`, the provider package the
`connectors:` sugar expands to (ADR 0038), the **auth shape** (where the
credential lives), the **local-testable tier**, and a runnable example. Each ships
a chain-of-custody delivery test; the tier-1 rows also ship an automated example
test (see [the contract below](#contract-every-entry-honours)). Cloud families
share a provider package (e.g. `redshift`/`athena`/`emr` →
`apache-airflow-providers-amazon`; `gcpbigquery`/`gcpcloudsql`/`gcp_looker` →
`…-google`); the boundary is curated, not a mechanical join of the hook path.

| Connector | `conn_type` | Provider package | Auth shape | Local tier | Example |
|---|---|---|---|---|---|
| [postgres](/connections/postgres/) | `postgres` | `apache-airflow-providers-postgres` | host + login/password | local (Docker) | [postgres_load](https://github.com/dexadata/dexaflow/tree/main/examples/postgres_load) |
| [mysql](/connections/mysql/) | `mysql` / `mariadb` | `apache-airflow-providers-mysql` | host + login/password | local (Docker) | [mysql_load](https://github.com/dexadata/dexaflow/tree/main/examples/mysql_load) |
| [mssql](/connections/mssql/) | `mssql` | `apache-airflow-providers-microsoft-mssql` | host + login/password | local (Docker) | [mssql_load](https://github.com/dexadata/dexaflow/tree/main/examples/mssql_load) |
| [sqlite](/connections/sqlite/) | `sqlite` | `apache-airflow-providers-sqlite` | file path | local (tier 1) | [sqlite_load](https://github.com/dexadata/dexaflow/tree/main/examples/sqlite_load) |
| [redis](/connections/redis/) | `redis` | `apache-airflow-providers-redis` | host + password | local (tier 1) | [redis_load](https://github.com/dexadata/dexaflow/tree/main/examples/redis_load) |
| [http](/connections/http/) | `http` / `https` | `apache-airflow-providers-http` | base URL + Extra | local (tier 1) | [http_load](https://github.com/dexadata/dexaflow/tree/main/examples/http_load) |
| [oracle](/connections/oracle/) | `oracle` | `apache-airflow-providers-oracle` | host + login/password (service in Schema) | doc-only | recipe |
| [mongo](/connections/mongo/) | `mongo` | `apache-airflow-providers-mongo` | host + login/password (db in Schema) | doc-only | recipe |
| [file-transfer](/connections/file-transfer/) | `ssh` / `sftp` / `ftp` | `apache-airflow-providers-ssh` / `-sftp` / `-ftp` | host + login/password (key in Extra) | doc-only | recipe |
| [google_cloud_platform](/connections/google_cloud_platform/) | `google_cloud_platform` | `apache-airflow-providers-google` | keyless (Workload Identity / ADC) or key in Extra | doc-only | [gcp_gcs_load](https://github.com/dexadata/dexaflow/tree/main/examples/gcp_gcs_load) |
| [snowflake](/connections/snowflake/) | `snowflake` | `apache-airflow-providers-snowflake` | login/password + account in Extra (or key-pair) | doc-only | recipe |
| [aws](/connections/aws/) | `aws` | `apache-airflow-providers-amazon` | keyless (IAM role) or access key | doc-only | recipe |
| [slack](/connections/slack/) | `slack` / `slackwebhook` | `apache-airflow-providers-slack` | token in password | doc-only | recipe |
| [databricks](/connections/databricks/) | `databricks` | `apache-airflow-providers-databricks` | host + PAT (or OAuth M2M) | doc-only | recipe |
| [azure](/connections/azure/) | `wasb` / `adls` / `azure_*` | `apache-airflow-providers-microsoft-azure` | keyless (managed identity) or key | doc-only | recipe |
| [spark](/connections/spark/) | `spark` / `spark_sql` / `spark_connect` / `spark_jdbc` | `apache-airflow-providers-apache-spark` | host:port + tuning Extra | doc-only | recipe |
| [kafka](/connections/kafka/) | `kafka` | `apache-airflow-providers-apache-kafka` | all-Extra client config (SASL) | doc-only | recipe |
| [redshift](/connections/redshift/) | `redshift` | `apache-airflow-providers-amazon` | host + login/password (or keyless IAM) | doc-only | recipe |
| [athena](/connections/athena/) | `athena` | `apache-airflow-providers-amazon` | keyless IAM (region in Extra) | doc-only | recipe |
| [emr](/connections/emr/) | `emr` | `apache-airflow-providers-amazon` | keyless IAM (region in Extra) | doc-only | recipe |
| [gcpbigquery](/connections/gcpbigquery/) | `gcpbigquery` | `apache-airflow-providers-google` | keyless (WI / ADC), project in Extra | doc-only | recipe |
| [gcpcloudsql](/connections/gcpcloudsql/) | `gcpcloudsql` | `apache-airflow-providers-google` | keyless (WI / ADC), instance in Extra | doc-only | recipe |
| [google_ads](/connections/google_ads/) | `google_ads` | `apache-airflow-providers-google` | OAuth material in Extra | doc-only | recipe |
| [gcp_looker](/connections/gcp_looker/) | `gcp_looker` | `apache-airflow-providers-google` | API3 client_id/secret in Extra | doc-only | recipe |
| [gcpssh](/connections/gcpssh/) | `gcpssh` | `apache-airflow-providers-google` | host + login/password | doc-only | recipe |
| [cassandra](/connections/cassandra/) | `cassandra` | `apache-airflow-providers-apache-cassandra` | host + login/password (keyspace in Schema) | doc-only | recipe |
| [neo4j](/connections/neo4j/) | `neo4j` | `apache-airflow-providers-neo4j` | host + login/password (db in Schema) | doc-only | recipe |
| [vertica](/connections/vertica/) | `vertica` | `apache-airflow-providers-vertica` | host + login/password | doc-only | recipe |
| [influxdb](/connections/influxdb/) | `influxdb` | `apache-airflow-providers-influxdb` | org + token in Extra | doc-only | recipe |
| [druid](/connections/druid/) | `druid` | `apache-airflow-providers-apache-druid` | host + login/password | doc-only | recipe |
| [pinot](/connections/pinot/) | `pinot` | `apache-airflow-providers-apache-pinot` | host + login/password (optional) | doc-only | recipe |
| [trino](/connections/trino/) | `trino` | `apache-airflow-providers-trino` | host + login/password (optional) | doc-only | recipe |
| [presto](/connections/presto/) | `presto` | `apache-airflow-providers-presto` | host + login/password (optional) | doc-only | recipe |
| [jdbc](/connections/jdbc/) | `jdbc` | `apache-airflow-providers-jdbc` | host + login/password + driver in Extra | doc-only | recipe |
| [docker](/connections/docker/) | `docker` | `apache-airflow-providers-docker` | host + login/password | doc-only | recipe |
| [salesforce](/connections/salesforce/) | `salesforce` | `apache-airflow-providers-salesforce` | login/password + token in Extra | doc-only | recipe |
| [telegram](/connections/telegram/) | `telegram` | `apache-airflow-providers-telegram` | token in password | doc-only | recipe |
| [discord](/connections/discord/) | `discord` | `apache-airflow-providers-discord` | token in password + endpoint in Extra | doc-only | recipe |
| [pagerduty](/connections/pagerduty/) | `pagerduty` | `apache-airflow-providers-pagerduty` | token in password + routing key in Extra | doc-only | recipe |
| [datadog](/connections/datadog/) | `datadog` | `apache-airflow-providers-datadog` | API + app keys in Extra | doc-only | recipe |
| [tableau](/connections/tableau/) | `tableau` | `apache-airflow-providers-tableau` | host + login/password (or PAT) | doc-only | recipe |
| [github](/connections/github/) | `github` | `apache-airflow-providers-github` | PAT in password | doc-only | recipe |
| [elasticsearch](/connections/elasticsearch/) | `elasticsearch` | `apache-airflow-providers-elasticsearch` | host + login/password | doc-only | recipe |
| [smtp](/connections/smtp/) | `smtp` | `apache-airflow-providers-smtp` | host + login/password | doc-only | recipe |
| [imap](/connections/imap/) | `imap` | `apache-airflow-providers-imap` | host + login/password | doc-only | recipe |
| [opsgenie](/connections/opsgenie/) | `opsgenie` | `apache-airflow-providers-opsgenie` | API key in password | doc-only | recipe |
| [zendesk](/connections/zendesk/) | `zendesk` | `apache-airflow-providers-zendesk` | agent email + API token | doc-only | recipe |
| [samba](/connections/samba/) | `samba` | `apache-airflow-providers-samba` | host + login/password | doc-only | recipe |
| [dbt_cloud](/connections/dbt_cloud/) | `dbt_cloud` | `apache-airflow-providers-dbt-cloud` | account id (login) + API token | doc-only | recipe |
| [hiveserver2](/connections/hiveserver2/) | `hiveserver2` | `apache-airflow-providers-apache-hive` | host + login/password (db in Schema) | doc-only | recipe |
| [hive_cli](/connections/hive_cli/) | `hive_cli` | `apache-airflow-providers-apache-hive` | host + login/password + beeline/kerberos in Extra | doc-only | recipe |
| [powerbi](/connections/powerbi/) | `powerbi` | `apache-airflow-providers-microsoft-azure` | client id/secret + tenant in Extra | doc-only | recipe |
| [msgraph](/connections/msgraph/) | `msgraph` | `apache-airflow-providers-microsoft-azure` | client id/secret + tenant in Extra | doc-only | recipe |
| [livy](/connections/livy/) | `livy` | `apache-airflow-providers-apache-livy` | host + login/password | doc-only | recipe |

**Local tier** — *local (Docker)*: spin the service up with Docker/Lima and run the
example end-to-end; *local (tier 1)*: no external service needed (or already in CI);
*doc-only*: a real run needs the external account/service, so the page is a recipe with
a delivery test rather than a runnable example. **Example** — *recipe* links back to the
connector's own page (the copy-paste DAG lives there).

## The long tail

Every curated connector above is first-class (impl + delivery test + cookbook).
Beyond them, **all ~86 connector types in the generated catalog** (ADR 0039) are
usable today via `connectors:` (dropdown + sugar) — they just don't each have a
dedicated cookbook recipe yet. Any other Airflow provider works via
`dependencies:`. Recipes are added connector-by-connector as they are promoted.

## Contract every entry honours

Every entry in this cookbook ships with all three of:

1. **A doc page** (this dir) covering: URI shape, default port, Lite-vs-Pro
   caveats, security notes (TLS, auth modes).
2. **An example DAG** under `examples/<type>_*/` with its own
   `README.md` showing how to spin up the dependency (Docker / Lima), how
   to create the Connection in the UI, and the expected end-state of the
   target system.
3. **An automated test** that proves the delivery chain — the integration
   test under `internal/storage/` (companion to the example) gated by the
   `integration` build tag, runs in CI against a real Postgres.

If a layer is mocked or the e2e test is manual-only, the doc says so and a
follow-up issue covers the gap.

## Related

- ADR 0019 — secret encryption at rest.
- ADR 0021 — agent secret delivery.
- #67 — connectors umbrella.
- #142 — the cookbook umbrella (this page).
