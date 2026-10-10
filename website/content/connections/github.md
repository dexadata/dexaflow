---
# --- AUTO redirect aliases (build_redirects.py) — do not edit by hand ---
aliases:
  - /connections/github.html
# --- end AUTO redirect aliases ---
title: GitHub connection
linkTitle: GitHub
weight: 180
description: GitHub connection
---

Connect a task to the GitHub API to read repos, manage issues/PRs, or
trigger workflows over a managed Dexaflow Connection. `GithubHook`
authenticates with a personal access token (PAT).

## Declare the provider

```yaml
# dexaflow.yaml
dag_id: github_repo
connectors:
  - github
```

## URI shape

```
github://:<personal_access_token>@
```

There is **no host** — the PAT (token-only shape, like Slack) lives in
Password and is percent-escaped. GitHub Enterprise hosts can be supplied
via `Extra`.

## Fields the UI asks for

| Field | Required | Notes |
|---|---|---|
| Conn Id | yes | e.g. `github_default`. Exported as `AIRFLOW_CONN_GITHUB_DEFAULT`. |
| Conn Type | yes | `github`. |
| Password | yes | The personal access token, e.g. `ghp_...`. Encrypted at rest (ADR 0019). |
| Host | no | Leave blank for github.com. |
| Extra | optional | JSON: `{"base_url":"https://ghe.example.com/api/v3"}` for GitHub Enterprise. |

## Example DAG

```python
# dag.py
from airflow.sdk import DAG, task


@task
def stars():
    from airflow.providers.github.hooks.github import GithubHook

    hook = GithubHook(github_conn_id="github_default")
    client = hook.get_conn()
    repo = client.get_repo("apache/airflow")
    print("stars:", repo.stargazers_count)


with DAG("github_repo", schedule=None, catchup=False, tags=["example"]):
    stars()
```

```yaml
# dexaflow.yaml
schema_version: "1.0"
dag_id: github_repo
python_version: "3.12"
connectors:
  - github
connections:
  - github_default
```

## Security notes

- **Fine-grained PATs**: prefer fine-grained tokens scoped to specific
  repos and permissions over classic PATs.
- **Short expiry + rotation**: set a token expiry and rotate via the
  Connection; revoke immediately on leak.
- Never log `AIRFLOW_CONN_GITHUB_DEFAULT`; it carries the PAT.

## Related

- ADR 0019 — secret encryption at rest.
- ADR 0021 — agent secret delivery (`AIRFLOW_CONN_<CONN_ID>`).
- `TestGithubConnectionURIShapeIntegration` — chain-of-custody delivery test.
- [Slack connection](/connections/slack/) — the same token-in-password shape.
