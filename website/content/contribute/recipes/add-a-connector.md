---
title: Add a connector
linkTitle: Add a connector
weight: 80
description: Add a connection type to the generated connector catalog from a pinned Airflow provider, and prove the connection reaches the task.
---

The connector catalog (`internal/connectors/catalog.json`) is generated from a
real Apache Airflow install, never written by hand
([ADR 0039](/project/adrs/0039-generated-connector-catalog/)). It feeds the
connection form in the UI, the `connectors:` shorthand in `dexaflow.yaml`, and
the dependency check at compile.

1. **Add the provider** to `scripts/connectors-providers.txt`, in the section
   it belongs to. The file pins the Airflow line the catalog is generated from.
2. **Refresh the lock.** `scripts/connectors-providers.lock.txt` holds the exact
   versions the committed catalog came from. Install the providers list into a
   fresh virtual environment and replace the lock's package lines with its
   `pip freeze | grep '^apache-airflow'` output, keeping the header, as the
   header says.
3. **Regenerate and check for drift:**

   ```sh
   make gen-connectors         # rewrites internal/connectors/catalog.json
   make gen-connectors-check   # regenerates to a temp file and diffs
   ```

   CI does not run the drift check, so run it before you push.
4. **Add only what Airflow does not emit** to
   `internal/connectors/catalog.overlay.json`, the hand-curated overlay merged
   over the generated catalog (dbt authentication fields today). Its field
   names follow `runtime/python/leoflow_runtime/dbt.py`.
5. **Test it:**
   - `go test ./internal/connectors/` checks the catalog's shape, package
     names, aliases and overlay targets; add the new type where a test pins a
     list.
   - When the type needs its own connection URI shape, add a case to the
     delivery tests in `internal/storage/connection_*_e2e_test.go`
     (`//go:build integration`, run by `make test-integration`), which follow a
     connection from the API to the environment the task sees.
   - `test/e2e/ui-smoke.js` drives the connection form in a browser; the
     catalog has broken that page before.
6. **Document it** on [Variables and connections](/author-dags/variables-connections/)
   when authors need to know about the type, and record a changelog fragment
   of kind `Added` ([record a change](/contribute/recipes/record-a-change/)).
