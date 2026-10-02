---
aliases:
  - /cli/leoflow_completion_powershell.html
  - /reference/cli/leoflow_completion_powershell/
title: "dexaflow completion powershell"
linkTitle: "completion powershell"
weight: 21
---

Generate the autocompletion script for powershell

### Synopsis

Generate the autocompletion script for powershell.

To load completions in your current shell session:

	dexaflow completion powershell | Out-String | Invoke-Expression

To load completions for every new session, add the output of the above command
to your powershell profile.


```
dexaflow completion powershell [flags]
```

### Options

```
  -h, --help              help for powershell
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

