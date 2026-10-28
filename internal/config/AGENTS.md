# internal/config/

Rules for changing configuration: the server settings (`server.go`) and the
developer CLI settings (`config.go`). The root [AGENTS.md](../../AGENTS.md)
applies too; the step by step is in the
[add a configuration setting recipe](https://dexaflow.dexadata.ai/contribute/recipes/add-a-configuration-setting/).

- **`DEXAFLOW_*` first, `LEOFLOW_*` as the fallback.** `bindBothPrefixes`
  (`envnames.go`) binds every key to its `DEXAFLOW_` variable and then to its
  `LEOFLOW_` variable, so the new name wins when both are set
  (`TestLoadServerNewPrefixWinsOverLegacy`). Binaries also call
  `envcompat.MirrorProcess` at start, so both spellings hold the same value for
  child processes and older agents. Never read one prefix only.
- **A server key binds only if it has a default.** viper binds environment
  variables for the keys it has seen, so every key is registered in
  `serverDefaults`, even when its default is the zero value
  (`TestDocumentedEnvVarsBind`, #725).
- **A registered key is documented.** Each key in `serverDefaults` has a row in
  `website/content/reference/configuration.md`, under its `DEXAFLOW_` name or,
  for a file-only key, in dotted form (`TestRegisteredKeysAreDocumented`, with
  no exception list). Those rows are generated: write the purpose and edition
  in `internal/tools/configref/settings.yaml` (or the field's GoDoc), then run
  `make gen-config-reference`; `go test ./internal/tools/configref/` fails on a
  stale page and on a `DEXAFLOW_*` variable the code reads but the page does
  not list. `internal/config/*.go` is user-facing surface for the docs guard
  (`scripts/check-docs-updated.sh`).
- **The default keeps today's behavior.** A new setting is optional and inert
  until an operator sets it, so an upgrade changes nothing
  ([ADR 0068](https://dexaflow.dexadata.ai/project/adrs/0068-patch-content-gated-by-safety/),
  safety bar points 1 and 2).
- **Invalid values fail at boot.** `(*ServerConfig).Validate` refuses a bad
  value or an unsafe combination with a message that names the key and what it
  accepts (see `validateExecution`, which refuses warm pools without token
  exchange and liveness enforcement). Add the check to the list it runs.
- **Security decisions belong to the operator.** A setting that decides
  isolation, such as whether a pod may be reused across attempts, is a server
  setting and never a `dexaflow.yaml` key a DAG author controls
  (`ExecutionSection` in `server.go`).
- **Configuration is one of the two allowed kinds of global state,** with
  metrics registries (root section 3, "No global state").
