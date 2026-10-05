---
aliases:
  - /cli/leoflow_lite_migrate-key.html
  - /reference/cli/leoflow_lite_migrate-key/
title: "dexaflow lite migrate-key"
linkTitle: "lite migrate-key"
weight: 40
---

Move stored connection secrets off the key published in this repository, onto a key only this install has.

### Synopsis

migrate-key re-encrypts every stored connection secret of this Lite install onto a key of its
own and records it in ~/.dexaflow/config.yaml. Run it when `dexaflow lite` warns that your secrets
are under the published key, that a key migration has not finished, or that some secrets are under
the published key and cannot be read.

Stop `dexaflow lite` first: the command refuses while a Lite server runs against any datastore of
this install. It scans every datastore the install has (the managed Postgres and the Docker one),
records the new key next to every old one before touching a row, moves the rows in one verified
transaction per datastore, and drops the old keys only after re-checking every datastore.

If it is interrupted at any point, run it again: it resumes. Afterwards config.yaml is the only copy
of the key, so back it up (`dexaflow lite backup`).

```
dexaflow lite migrate-key [flags]
```

### Options

```
      --dry-run   scan and report what would be done; write nothing
  -h, --help      help for migrate-key
      --yes       skip the confirmation prompt
```

### Options inherited from parent commands

```
      --config string       config file path (default ~/.dexaflow/config.yaml)
      --log-level string    log level: debug, info, warn, error
      --server-url string   control plane API base URL
```

### SEE ALSO

* [dexaflow lite](/reference/cli/dexaflow_lite/)	 - Run Dexaflow Lite locally with hot reload.

