---
# --- AUTO redirect aliases (build_redirects.py) — do not edit by hand ---
aliases:
  - /connections/azure.html
# --- end AUTO redirect aliases ---
title: Azure connection
linkTitle: Azure
weight: 30
description: Azure connection
---

Connect a task to Azure (Blob Storage, Data Lake, Data Factory, Synapse, …) via a
managed Leoflow Connection and the Microsoft Azure provider hooks. The Azure
provider exposes **many** conn types — all from
`apache-airflow-providers-microsoft-azure`. The common ones:

| conn_type | Service | Primary hook |
|---|---|---|
| `wasb` | Blob Storage | `WasbHook` |
| `adls` / `azure_data_lake` | Data Lake Storage | `AzureDataLakeStorageV2Hook` |
| `azure_data_factory` | Data Factory | `AzureDataFactoryHook` |
| `azure_synapse` | Synapse | `AzureSynapseHook` |
| `azure_cosmos` | CosmosDB | `AzureCosmosDBHook` |

## Declare the provider

One line covers the whole family:

```yaml
# dexaflow.yaml
dag_id: blob_load
connectors:
  - wasb            # or adls / azure_data_factory / azure_synapse / ...
```

`wasb`, `adls`, etc. all resolve to `apache-airflow-providers-microsoft-azure`.

## Credentials

Azure connectors are **keyless-first** (ADR 0035): prefer a **managed identity**
(`managed_identity_client_id` / `workload_identity_tenant_id` in Extra) so no key
is stored. The form renders these as labeled fields. The key-based fallback:

| Field | Where it goes (wasb) |
|---|---|
| Login | storage account name |
| Password | account key (encrypted at rest, ADR 0019) |
| Extra | `connection_string`, `sas_token`, `shared_access_key`, `tenant_id` |

The account-key + tenant Extra round-trip is pinned by
`TestWasbConnectionURIShapeIntegration`.

## Example DAG (copy-paste)

Docs-only recipe (needs a real Azure account). The hook is imported **inside** the
task.

```python
# dag.py
from __future__ import annotations

from airflow.sdk import DAG, task


@task
def upload() -> None:
    from airflow.providers.microsoft.azure.hooks.wasb import WasbHook

    hook = WasbHook(wasb_conn_id="wasb_default")
    print("upload: putting blob via WasbHook(wasb_default)")
    hook.load_string("hello from leoflow", container_name="data", blob_name="hello.txt", overwrite=True)
    print("upload: ok")


with DAG("blob_load", schedule=None, catchup=False, tags=["example"]):
    upload()
```

```yaml
# dexaflow.yaml
schema_version: "1.0"
dag_id: blob_load
description: Upload a blob to Azure Storage via WasbHook.
owner: examples
tags:
  - example
python_version: "3.11"
connectors:
  - wasb
```

### Run it

1. **Admin → Connections → +**, type `wasb`. Use a managed identity (leave key
   blank) or set the storage account + account key.
2. `leoflow lite path/to/this/dag` → trigger `blob_load`.

## Security notes

- **Managed identity beats keys** (ADR 0035): nothing lives in the Leoflow DB.
- **Never `print()` the URI** — it carries the account key / SAS token.

## Related

- `docs/connections/index.md` — *Installing a connector's provider*.
- ADR 0035 — cloud connectors keyless-first.
- ADR 0019 / 0021 — secret encryption + agent delivery.
