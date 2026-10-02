---
# --- AUTO redirect aliases (build_redirects.py) — do not edit by hand ---
aliases:
  - /connections/google_ads.html
# --- end AUTO redirect aliases ---
title: Google Ads connection
linkTitle: Google Ads
weight: 190
description: Google Ads connection
---

Pull reports from the Google Ads API from a managed Dexaflow Connection. The
OAuth material (developer token + installed-app client credentials + refresh
token) lives entirely in **Extra**; there is no host or password field.

{{% alert title="Underscore conn_type → hyphenated scheme" color="info" %}}
`google_ads` contains an underscore, which is not a legal URI scheme (RFC
3986). The Dexaflow URI builder normalizes `_`→`-` for the scheme (delivering
`google-ads://…`), exactly as Airflow's `Connection.get_uri()` does — and
Airflow reverses it in `from_uri`, so the round-trip is faithful. Pinned by
`TestGoogleAdsConnectionURIShapeIntegration` and
`TestAirflowConnURISchemeUnderscoreNormalized`.
{{% /alert %}}

## Declare the provider

```yaml
connectors: [google_ads]
```

## URI shape

Google Ads is an **Extra-only** connection. Once the scheme is normalized it is
delivered as `AIRFLOW_CONN_<ID>` with the canonical hyphenated scheme:

```
google-ads://?__extra__=<url-encoded JSON>
```

The `__extra__` query parameter carries the OAuth blob (`developer_token`,
`client_id`, `client_secret`, `refresh_token`).

## Fields

| Field | Required | Notes |
|---|---|---|
| Conn Id | yes | e.g. `google_ads_reports`. Exported as `AIRFLOW_CONN_GOOGLE_ADS_REPORTS`. |
| Conn Type | yes | `google_ads`. |
| Extra | yes | JSON: `{"developer_token":"...","client_id":"...","client_secret":"...","refresh_token":"..."}`. |

No Login/Password: the OAuth material lives entirely in Extra (encrypted at rest,
ADR 0019).

## Example DAG (doc-only)

The provider import goes **inside** the `@task` body — a top-level provider
import fails the compile.

```python
from datetime import datetime
from airflow.decorators import dag, task


@dag(schedule=None, start_date=datetime(2024, 1, 1), catchup=False)
def google_ads_demo():
    @task
    def fetch() -> int:
        from airflow.providers.google.ads.hooks.ads import GoogleAdsHook

        hook = GoogleAdsHook(gcp_conn_id="google_ads_reports", google_ads_conn_id="google_ads_reports")
        rows = hook.search(client_ids=["1234567890"], query="SELECT campaign.id FROM campaign")
        return len(list(rows))

    fetch()


google_ads_demo()
```

```yaml
# dexaflow.yaml
dag_id: google_ads_demo
python_version: "3.12"
connectors: [google_ads]
```

## Security notes

- The refresh token and client secret are long-lived credentials. They are
  encrypted at rest (ADR 0019) and delivered only over the authenticated, TLS
  agent channel (ADR 0021).
- **Never log the URI** — it carries the full OAuth material.
- Rotate the refresh token periodically; revoke it in the Google Cloud console
  if a DAG image leaks.

## Related

- ADR 0019 — secret encryption at rest.
- ADR 0021 — agent secret delivery.
- ADR 0035 — keyless-first cloud connector auth.
- [gcpbigquery.md](/connections/gcpbigquery/), [gcpcloudsql.md](/connections/gcpcloudsql/) — the other Google data services in this batch.
