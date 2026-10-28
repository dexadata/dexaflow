---
title: Add a Helm value
linkTitle: Add a Helm value
weight: 60
description: Add a chart value with its doc comment, its template, a unit test and a regenerated README, keeping upgrades from the previous chart working.
---

The rules are in [`helm/AGENTS.md`](https://github.com/dexadata/dexaflow/blob/main/helm/AGENTS.md).

1. **Write the unit test first** under `helm/dexaflow/tests/`: what the
   template renders with the value set, and what it renders without it.
   `config_bind_test.yaml` is the pattern for a value that becomes a server
   environment variable. Run `helm unittest helm/dexaflow` and watch it fail.
2. **Add the key** to `helm/dexaflow/values.yaml` with a `# --` doc comment
   above it. Its default keeps the rendered manifests of an existing install
   unchanged.
3. **Render it** in the template that uses it. A server setting reaches the
   control plane as an environment variable in
   `templates/_controlplane-deployment.tpl`; the server reads it under either
   prefix (see [add a configuration setting](/contribute/recipes/add-a-configuration-setting/)).
   When a combination of values would make the server refuse to boot, fail the
   render with a message that says what to set, as `templates/deployment.yaml`
   does for its guarded combinations.
4. **Regenerate the README** from `README.md.gotmpl`, never by editing the
   table:

   ```sh
   helm-docs -c helm/dexaflow
   bash scripts/check-values-doc-comments.sh   # what helm-docs cannot see
   ```

5. **Run the chart checks:**

   ```sh
   helm lint helm/dexaflow
   helm unittest helm/dexaflow
   bash scripts/helm-template-checks.sh
   bash scripts/rbac-covers-executor.sh        # when the Role or the executor changed
   ```

   Helm CI also installs the previous released chart and upgrades it to yours;
   a renamed value keeps the old key working.
6. **Leave `Chart.yaml` alone.** The release cut bumps `version` and
   `appVersion` together with the tag
   (`scripts/check-chart-version-matches-tag.sh`).
7. **Document it.** `values.yaml` is user-facing surface for the docs guard:
   update the [Helm chart page](/operate/helm-chart/) or the page of the
   feature, and record a changelog fragment
   ([record a change](/contribute/recipes/record-a-change/)).
