---
# --- AUTO redirect aliases (build_redirects.py) — do not edit by hand ---
aliases:
  - /connections/aws.html
# --- end AUTO redirect aliases ---
title: Amazon Web Services connection
linkTitle: Amazon Web Services
weight: 20
description: Amazon Web Services connection
---

Connect a task to AWS (S3, Athena, SQS, …) via a managed Leoflow Connection and
Airflow's Amazon provider hooks. The conn_type is `aws` (Airflow's name for the
Amazon provider). Like Snowflake it has **no host:port** — credentials and region
live in login/password + Extra.

## Declare the provider

```yaml
# dexaflow.yaml
dag_id: s3_load
connectors:
  - aws
```

## Two credential modes

### Keyless (recommended — ADR 0035)

Prefer **no stored key**: run the task pod under an IAM role (IRSA / instance
profile / web-identity) and leave the access-key fields blank. The AWS SDK
resolves credentials at runtime from the environment; Leoflow stores nothing.
This is the default posture for cloud connectors — a leaked Leoflow DB never
leaks AWS keys.

```text
Conn Id: aws_default
Conn Type: aws
(leave Access Key / Secret Key empty)
Extra: {"region_name": "eu-central-1"}
```

### Key-based (fallback)

When a role is not available, store an access key. The form renders the labeled
fields:

| Field | Where it goes | Notes |
|---|---|---|
| AWS Access Key ID | login | e.g. `AKIA…`. |
| AWS Secret Access Key | password | Encrypted at rest (ADR 0019). Routinely contains `/` and `+` — the URI builder escapes them; the round-trip is pinned by `TestAWSConnectionURIShapeIntegration`. |
| Region | extra | `region_name`, e.g. `eu-central-1`. |
| Role to assume | extra | `role_arn`, e.g. `arn:aws:iam::…:role/transformer`. |

## Example DAG (copy-paste)

Docs-only recipe (needs a real AWS account / bucket). The hook is imported
**inside** the task — a top-level provider import fails the compile.

```python
# dag.py
from __future__ import annotations

from airflow.sdk import DAG, task


@task
def upload() -> None:
    from airflow.providers.amazon.aws.hooks.s3 import S3Hook

    hook = S3Hook(aws_conn_id="aws_default")
    print("upload: putting object via S3Hook(aws_default)")
    hook.load_string(
        string_data="hello from leoflow",
        key="leoflow/hello.txt",
        bucket_name="my-bucket",
        replace=True,
    )
    print("upload: ok")


with DAG("s3_load", schedule=None, catchup=False, tags=["example"]):
    upload()
```

```yaml
# dexaflow.yaml
schema_version: "1.0"
dag_id: s3_load
description: Upload an object to S3 via S3Hook.
owner: examples
tags:
  - example
python_version: "3.11"
connectors:
  - aws
```

### Run it

1. Create the Connection in **Admin → Connections → +**, type `aws`. Either leave
   the keys blank (keyless, with a role on the pod) or fill Access Key / Secret
   Key. Set `region_name` in Extra.
2. `leoflow lite path/to/this/dag` → trigger `s3_load`.
3. The task log reports the put; verify the object in your bucket.

## Security notes

- **Keyless beats keys.** Use IRSA (EKS) / instance profiles; rotate is automatic
  and nothing lives in the Leoflow DB (ADR 0035).
- **Least privilege**: scope the role/policy to the exact bucket/prefix.
- **Never `print()` the URI** — it carries the secret key.

## Related

- `docs/connections/index.md` — *Installing a connector's provider*.
- ADR 0035 — cloud connectors keyless-first.
- ADR 0019 / 0021 — secret encryption + agent delivery.
