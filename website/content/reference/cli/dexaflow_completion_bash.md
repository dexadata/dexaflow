---
aliases:
  - /cli/leoflow_completion_bash.html
  - /reference/cli/leoflow_completion_bash/
title: "dexaflow completion bash"
linkTitle: "completion bash"
weight: 19
---

Generate the autocompletion script for bash

### Synopsis

Generate the autocompletion script for the bash shell.

This script depends on the 'bash-completion' package.
If it is not installed already, you can install it via your OS's package manager.

To load completions in your current shell session:

	source <(dexaflow completion bash)

To load completions for every new session, execute once:

#### Linux:

	dexaflow completion bash > /etc/bash_completion.d/dexaflow

#### macOS:

	dexaflow completion bash > $(brew --prefix)/etc/bash_completion.d/dexaflow

You will need to start a new shell for this setup to take effect.


```
dexaflow completion bash
```

### Options

```
  -h, --help              help for bash
      --no-descriptions   disable completion descriptions
```

### Options inherited from parent commands

```
      --config string       config file path (default ~/.leoflow/config.yaml)
      --log-level string    log level: debug, info, warn, error
      --server-url string   control plane API base URL
```

### SEE ALSO

* [dexaflow completion](/reference/cli/dexaflow_completion/)	 - Generate the autocompletion script for the specified shell

