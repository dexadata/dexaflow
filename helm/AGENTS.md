# helm/

Rules for changing the Helm chart. The root [AGENTS.md](../AGENTS.md) applies
too.

- **Every value is documented in place.** Each key in `values.yaml` carries a
  `# --` comment that helm-docs turns into the README table; regenerate the
  README from `README.md.gotmpl`, never edit the table by hand.
  `scripts/check-values-doc-comments.sh` catches what helm-docs cannot see.
- **Template logic has a unit test** under `dexaflow/tests/`
  (`helm unittest helm/dexaflow`).
- **Upgrades from the previous release keep working.** Helm CI installs the
  previous released chart and upgrades it in place; a value rename keeps the old
  key working and documents both.
- **Names from before the rename keep working** (`charts/leoflow`, old value
  names). Generated names use `dexaflow`.
- **A patch** may add values but never changes the default behavior of an
  existing install
  ([ADR 0068](https://dexaflow.dexadata.ai/project/adrs/0068-patch-content-gated-by-safety/)).
