---
aliases:
  - /cli/leoflow_runs_trigger.html
  - /reference/cli/leoflow_runs_trigger/
title: "dexaflow runs trigger"
linkTitle: "runs trigger"
weight: 61
---

Trigger a new run of a DAG, and optionally follow it to the end.

### Synopsis

Trigger a new run of a DAG. With --follow, print each task state change and the logs of each
task as it finishes, then exit with the run's result: 0 when it succeeded, 2 when it
failed (naming the failed tasks), 3 when --timeout elapsed first, 130 after Ctrl-C.
Ctrl-C and --timeout stop following; neither cancels the run.

```
dexaflow runs trigger <dag_id> [flags]
```

### Options

```
      --conf string        run configuration as an inline JSON object, exposed to tasks as params (e.g. --conf '{"date":"2026-01-01"}')
      --conf-file string   path to a JSON file whose object contents become the run configuration; mutually exclusive with --conf
      --follow             follow the run: print task state changes and logs, the UI link, and exit with the run's result
  -h, --help               help for trigger
      --server string      control plane base URL (default: config server_url)
      --timeout duration   with --follow, stop following after this long (e.g. 30m) and exit 3; the run keeps going (default: no limit)
      --token string       JWT bearer token (default: config token)
```

### Options inherited from parent commands

```
      --config string       config file path (default ~/.dexaflow/config.yaml)
      --log-level string    log level: debug, info, warn, error
      --server-url string   control plane API base URL
      --target name         name of the control plane to use (default: DEXAFLOW_TARGET, then the target selected for the workspace; see dexaflow target list)
```

### SEE ALSO

* [dexaflow runs](/reference/cli/dexaflow_runs/)	 - Trigger and inspect DAG runs.

