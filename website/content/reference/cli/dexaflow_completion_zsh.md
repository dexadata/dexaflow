---
aliases:
  - /cli/leoflow_completion_zsh.html
  - /reference/cli/leoflow_completion_zsh/
title: "dexaflow completion zsh"
linkTitle: "completion zsh"
weight: 22
---

Generate the autocompletion script for zsh

### Synopsis

Generate the autocompletion script for the zsh shell.

If shell completion is not already enabled in your environment you will need
to enable it.  You can execute the following once:

	echo "autoload -U compinit; compinit" >> ~/.zshrc

To load completions in your current shell session:

	source <(dexaflow completion zsh)

To load completions for every new session, execute once:

#### Linux:

	dexaflow completion zsh > "${fpath[1]}/_dexaflow"

#### macOS:

	dexaflow completion zsh > $(brew --prefix)/share/zsh/site-functions/_dexaflow

You will need to start a new shell for this setup to take effect.


```
dexaflow completion zsh [flags]
```

### Options

```
  -h, --help              help for zsh
      --no-descriptions   disable completion descriptions
```

### Options inherited from parent commands

```
      --config string       config file path (default ~/.dexaflow/config.yaml)
      --log-level string    log level: debug, info, warn, error
      --server-url string   control plane API base URL
```

### SEE ALSO

* [dexaflow completion](/reference/cli/dexaflow_completion/)	 - Generate the autocompletion script for the specified shell

