---
title: "pkg/server"
linkTitle: "pkg/server"
weight: 16
---

```go
import "github.com/dexadata/dexaflow/pkg/server"
```

Package server runs the Dexaflow control plane from another Go program \(ADR 0099\). Run with no options behaves exactly as the dexaflow\-server binary, which is itself a thin caller of this package.

Compatibility: a patch release never breaks an exported identifier of this package. A minor may, after the identifier was marked Deprecated in the previous minor, with a changelog entry that says what to use instead.

## Index

- [Variables](<#variables>)
- [func ExitCode\(err error\) int](<#ExitCode>)
- [func Run\(ctx context.Context, opts ...Option\) error](<#Run>)
- [type Option](<#Option>)
  - [func WithAdmission\(h admission.Hook\) Option](<#WithAdmission>)
  - [func WithLogSink\(sink logsink.Sink\) Option](<#WithLogSink>)


## Variables

<a name="ErrDatabaseReplaced"></a>ErrDatabaseReplaced is returned by Run when the readiness probe found another installation's database, or none, under the running server. The binary exits with ExitCode\(ErrDatabaseReplaced\), 3, so a restart count tells it from a crash.

```go
var ErrDatabaseReplaced = serverapp.ErrDatabaseReplaced
```

<a name="ExitCode"></a>
## func [ExitCode](<https://github.com/dexadata/dexaflow/blob/main/pkg/server/server.go#L85>)

```go
func ExitCode(err error) int
```

ExitCode is the process exit code for an error Run returned: 0 for nil, 3 for ErrDatabaseReplaced, 1 for anything else.

<a name="Run"></a>
## func [Run](<https://github.com/dexadata/dexaflow/blob/main/pkg/server/server.go#L74>)

```go
func Run(ctx context.Context, opts ...Option) error
```

Run builds the control plane and serves until ctx is done, then shuts down and returns. Call it once per process.

Before anything reads the environment it mirrors the DEXAFLOW\_\* and LEOFLOW\_\* names onto each other, as the binary does, so either spelling configures the server. It reads the config file named by DEXAFLOW\_CONFIG, then the environment. It installs no signal handler: cancel ctx to stop the server, for example with signal.NotifyContext.

<details><summary>Example</summary>
<p>

A program that runs the control plane as dexaflow\-server does: configured by DEXAFLOW\_CONFIG and the DEXAFLOW\_\* environment, stopped by SIGINT or SIGTERM. It needs a database, so it has no Output line.

```go
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/dexadata/dexaflow/pkg/server"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := server.Run(ctx)
	stop()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(server.ExitCode(err))
	}
}
```

</p>
</details>

<a name="Option"></a>
## type [Option](<https://github.com/dexadata/dexaflow/blob/main/pkg/server/server.go#L30>)

Option changes how Run builds the server. More options come in later releases \(ADR 0099 §3\); adding one is not a breaking change.

```go
type Option func(*settings)
```

<a name="WithAdmission"></a>
### func [WithAdmission](<https://github.com/dexadata/dexaflow/blob/main/pkg/server/server.go#L43>)

```go
func WithAdmission(h admission.Hook) Option
```

WithAdmission registers a dispatch admission hook \(ADR 0100\). Every task attempt is offered to it after Dexaflow's own checks and before the attempt's agent token is minted. Hooks run in the order they are registered, before the webhook execution.admission.webhook.url configures; the first refusal wins and each later hook sees the earlier changes. execution.admission.timeout and fail\_open apply to every hook. A nil hook is ignored.

<details><summary>Example</summary>
<p>

A control plane that refuses every attempt of one tenant during a freeze, with an admission hook compiled into the binary instead of a webhook.

```go
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/dexadata/dexaflow/pkg/admission"
	"github.com/dexadata/dexaflow/pkg/server"
)

func main() {
	freeze := admission.HookFunc(func(_ context.Context, req *admission.Request) (admission.Decision, error) {
		if req.TenantID == "acme" {
			return admission.Refuse("acme is frozen until 06:00 UTC"), nil
		}
		return admission.Admit(), nil
	})
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := server.Run(ctx, server.WithAdmission(freeze))
	stop()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(server.ExitCode(err))
	}
}
```

</p>
</details>

<a name="WithLogSink"></a>
### func [WithLogSink](<https://github.com/dexadata/dexaflow/blob/main/pkg/server/server.go#L58>)

```go
func WithLogSink(sink logsink.Sink) Option
```

WithLogSink stores task logs in sink instead of the backend logs.backend configures, whose keys are then ignored; boot logs that the sink came from an option. The sink serves every read of a task log too, so the API, the UI and the CLI show what it stored. The caller owns it: Run never closes it. The optional interfaces of package logsink apply, and a sink without logsink.Resumer refuses an agent's reconnect of a log stream. A nil sink keeps the configured one; with several options the last one wins.

<details><summary>Example</summary>
<p>

A control plane that stores task logs with a sink of its own instead of the disk or object store backends.

```go
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/dexadata/dexaflow/pkg/logsink"
	"github.com/dexadata/dexaflow/pkg/server"
)

// fileSink stores each attempt's log as one file under dir, its name built
// from the Ref with every field escaped, so no value can leave dir.
type fileSink struct{ dir string }

func (s fileSink) path(ref logsink.Ref) string {
	name := strings.Join([]string{
		url.PathEscape(ref.TenantID), url.PathEscape(ref.DagID), url.PathEscape(ref.RunID),
		url.PathEscape(ref.TaskID), strconv.Itoa(ref.TryNumber), strconv.Itoa(ref.AttemptEpoch),
	}, "_")
	return filepath.Join(s.dir, name+".jsonl")
}

func (s fileSink) Open(ref logsink.Ref) (logsink.LogWriter, error) {
	f, err := os.Create(s.path(ref))
	if err != nil {
		return nil, err
	}
	return &fileWriter{f: f, enc: json.NewEncoder(f)}, nil
}

// Read returns the stored file; os.Open wraps fs.ErrNotExist for a missing
// one, which the API reports as no log yet.
func (s fileSink) Read(_ context.Context, ref logsink.Ref) (io.ReadCloser, error) {
	return os.Open(s.path(ref))
}

type fileWriter struct {
	f   *os.File
	enc *json.Encoder
}

func (w *fileWriter) WriteEvent(ev logsink.Event) error { return w.enc.Encode(ev) }
func (w *fileWriter) Close() error                      { return w.f.Close() }

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := server.Run(ctx, server.WithLogSink(fileSink{dir: "/var/lib/task-logs"}))
	stop()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(server.ExitCode(err))
	}
}
```

</p>
</details>

Generated by [gomarkdoc](<https://github.com/princjef/gomarkdoc>)
