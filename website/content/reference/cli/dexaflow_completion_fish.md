---
aliases:
  - /cli/leoflow_completion_fish.html
  - /reference/cli/leoflow_completion_fish/
title: "dexaflow completion fish"
linkTitle: "completion fish"
weight: 20
---

Generate the autocompletion script for fish

### Synopsis

Generate the autocompletion script for the fish shell.

To load completions in your current shell session:

	dexaflow completion fish | source

To load completions for every new session, execute once:

	dexaflow completion fish > ~/.config/fish/completions/dexaflow.fish

You will need to start a new shell for this setup to take effect.


```
dexaflow completion fish [flags]
```

### Options

```
  -h, --help              help for fish
      --no-descriptions   disable completion descriptions
```

### Options inherited from parent commands

```
      --config string       config file path (default ~/.leoflow/config.yaml)
      --log-level string    log level: debug, info, warn, error
      --server-url string   control plane API base URL
```

### SEE ALSO

* [dexaflow completion](dexaflow_completion.md)	 - Generate the autocompletion script for the specified shell

