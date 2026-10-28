---
title: Add a configuration setting
linkTitle: Add a setting
weight: 40
description: Add a server setting that binds from DEXAFLOW_* and LEOFLOW_*, fails fast on a bad value, and is documented in the configuration reference.
---

The rules are in [`internal/config/AGENTS.md`](https://github.com/dexadata/dexaflow/blob/main/internal/config/AGENTS.md).
This recipe adds a server setting (`internal/config/server.go`); a developer
CLI setting lives in `internal/config/config.go` and follows the same steps.

1. **Write the tests first** in `internal/config/`: the default, the value read
   from `DEXAFLOW_<KEY>` and from `LEOFLOW_<KEY>`, and the values `Validate`
   refuses. `env_binding_test.go` and `execution_test.go` show the shape.
   Run them and watch them fail.
2. **Add the field** to its section struct in `server.go`, with a
   `mapstructure` tag and a GoDoc comment that says what it changes and what the
   default does.
3. **Register the default** in `serverDefaults`. Without it the environment
   variable never binds (`TestDocumentedEnvVarsBind`). Pick the default that
   keeps today's behavior, so the setting is inert until someone sets it
   ([ADR 0068](/project/adrs/0068-patch-content-gated-by-safety/)).
4. **Validate it.** Add a check to `(*ServerConfig).Validate` that refuses a bad
   value at boot, naming the key and what it accepts.
5. **Use it where it applies**, and test that path through its real entry
   point, not only the config struct.
6. **Document it.** The settings tables of the
   [configuration reference](/reference/configuration/) are generated from
   `internal/config`: give the key its purpose and edition in
   `internal/tools/configref/settings.yaml` (or let the field's GoDoc describe
   it), make sure a `BEGIN GENERATED configref settings` block on the page
   covers it, and run `make gen-config-reference`. Never edit the generated rows
   by hand; `go test ./internal/tools/configref/` fails on a stale page.
   Mention it on the page of the feature it tunes, and on
   [upgrades](/operate/upgrades/) when an operator should act. Record a
   changelog fragment of kind `Added`
   ([record a change](/contribute/recipes/record-a-change/)).
7. **Expose it in the chart** when operators set it through Helm:
   [add a Helm value](/contribute/recipes/add-a-helm-value/).
8. **Check:**

   ```sh
   go test ./internal/config/ ./internal/tools/configref/
   make lint test
   ```
