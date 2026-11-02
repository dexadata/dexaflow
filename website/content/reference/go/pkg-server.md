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
  - [func WithAuthenticator\(a authn.Authenticator\) Option](<#WithAuthenticator>)
  - [func WithConfigFile\(path string\) Option](<#WithConfigFile>)
  - [func WithLogSink\(sink logsink.Sink\) Option](<#WithLogSink>)
  - [func WithTrustedIssuer\(ti authn.TrustedIssuer, allowedOrigins ...string\) Option](<#WithTrustedIssuer>)
  - [func WithUIServer\(ui UIServer\) Option](<#WithUIServer>)
  - [func WithXComBackend\(backend xcomstore.Backend\) Option](<#WithXComBackend>)
- [type UIServer](<#UIServer>)


## Variables

<a name="ErrDatabaseReplaced"></a>ErrDatabaseReplaced is returned by Run when the readiness probe found another installation's database, or none, under the running server. The binary exits with ExitCode\(ErrDatabaseReplaced\), 3, so a restart count tells it from a crash.

```go
var ErrDatabaseReplaced = serverapp.ErrDatabaseReplaced
```

<a name="ExitCode"></a>
## func [ExitCode](<https://github.com/dexadata/dexaflow/blob/main/pkg/server/server.go#L183>)

```go
func ExitCode(err error) int
```

ExitCode is the process exit code for an error Run returned: 0 for nil, 3 for ErrDatabaseReplaced, 1 for anything else.

<a name="Run"></a>
## func [Run](<https://github.com/dexadata/dexaflow/blob/main/pkg/server/server.go#L172>)

```go
func Run(ctx context.Context, opts ...Option) error
```

Run builds the control plane and serves until ctx is done, then shuts down and returns. Call it once per process.

Before anything reads the environment it mirrors the DEXAFLOW\_\* and LEOFLOW\_\* names onto each other, as the binary does, so either spelling configures the server. It reads the config file named by DEXAFLOW\_CONFIG, then the environment. It installs no signal handler: cancel ctx to stop the server, for example with signal.NotifyContext. WithConfigFile names another config file.

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
## type [Option](<https://github.com/dexadata/dexaflow/blob/main/pkg/server/server.go#L33>)

Option changes how Run builds the server. More options come in later releases \(ADR 0099 §3\); adding one is not a breaking change.

```go
type Option func(*settings)
```

<a name="WithAdmission"></a>
### func [WithAdmission](<https://github.com/dexadata/dexaflow/blob/main/pkg/server/server.go#L58>)

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

<a name="WithAuthenticator"></a>
### func [WithAuthenticator](<https://github.com/dexadata/dexaflow/blob/main/pkg/server/server.go#L134>)

```go
func WithAuthenticator(a authn.Authenticator) Option
```

WithAuthenticator checks API requests and password sign\-in with a, an authenticator of your own, such as one backed by a company directory. A request token goes to the server's own authenticator first, so the sessions an SSO login, a trusted\-issuer handoff or a renewal opens keep working, and to a only when the server refuses it as invalid. Password sign\-in \(POST /api/v2/auth/token\) goes to a alone. API tokens and CI sessions are still checked in front of both, and agent tokens never reach a.

The server authorizes the User a returns by its roles and permissions, and clears its Scoped, Scopes and DeployAudience fields first: only the server grants those \(ADR 0099 §6\). A nil a keeps the server's own sign\-in; with several options the last one wins.

<details><summary>Example</summary>
<p>

A control plane whose password sign\-in goes to a directory of its own. The sessions the server mints itself \(SSO, handoff, renewal\) keep working.

```go
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/dexadata/dexaflow/pkg/authn"
	"github.com/dexadata/dexaflow/pkg/server"
)

// directory signs users in against a company directory, here a fixed table,
// and recognizes the tokens it hands out. tenantID is the tenant's name.
type directory struct {
	tenantID string
	users    map[string]string
}

func (d directory) IssueToken(_ context.Context, c authn.Credentials) (string, error) {
	if pw, ok := d.users[c.Username]; !ok || pw != c.Password {
		return "", authn.ErrInvalidCredentials
	}
	return "dir:" + c.Username, nil
}

func (d directory) Authenticate(_ context.Context, token string) (*authn.User, error) {
	email, ok := strings.CutPrefix(token, "dir:")
	if !ok || d.users[email] == "" {
		return nil, authn.ErrInvalidToken
	}
	return &authn.User{
		ID: email, TenantID: d.tenantID, Email: email,
		Roles:       []string{"viewer"},
		Permissions: []authn.Permission{{Action: "read", Resource: "*"}},
	}, nil
}

func main() {
	dir := directory{tenantID: "default", users: map[string]string{"ada@example.com": "s3cret"}}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := server.Run(ctx, server.WithAuthenticator(dir))
	stop()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(server.ExitCode(err))
	}
}
```

</p>
</details>

<a name="WithConfigFile"></a>
### func [WithConfigFile](<https://github.com/dexadata/dexaflow/blob/main/pkg/server/server.go#L43>)

```go
func WithConfigFile(path string) Option
```

WithConfigFile reads the configuration from path instead of the file DEXAFLOW\_CONFIG names; the DEXAFLOW\_\* environment still applies on top of it, as it does for the binary. Run fails when the file cannot be read. An empty path keeps DEXAFLOW\_CONFIG; with several options the last one wins.

<details><summary>Example</summary>
<p>

A control plane configured by a file of the program's own choosing, with the DEXAFLOW\_\* environment still on top.

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
	err := server.Run(ctx, server.WithConfigFile("/etc/acme/dexaflow-server.yaml"))
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
### func [WithLogSink](<https://github.com/dexadata/dexaflow/blob/main/pkg/server/server.go#L73>)

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

<a name="WithTrustedIssuer"></a>
### func [WithTrustedIssuer](<https://github.com/dexadata/dexaflow/blob/main/pkg/server/server.go#L154>)

```go
func WithTrustedIssuer(ti authn.TrustedIssuer, allowedOrigins ...string) Option
```

WithTrustedIssuer verifies the trusted\-issuer handoff \(POST /api/v2/auth/session\) with an issuer of your own, accepting a handoff only when the browser posts it from one of allowedOrigins \(scheme://host\[:port\], one or more\). A verified identity opens a UI session for the active user linked to the issuer under its subject, in its tenant; it never creates a user and never brings roles. Run refuses to start when allowedOrigins is empty or invalid, or when auth.trusted\_issuer is configured too.

The bearer mode, the deploy audience and the host pattern of auth.trusted\_issuer need the built\-in verifier and stay off with an issuer from options. A nil ti keeps the configured issuer; with several options the last one wins.

<details><summary>Example</summary>
<p>

A control plane that opens UI sessions from the operator's portal, posted from https://portal.example.com.

```go
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/dexadata/dexaflow/pkg/authn"
	"github.com/dexadata/dexaflow/pkg/server"
)

// portal verifies the handoff tokens of the operator's own portal, here a
// fixed table of one-time codes.
type portal struct{ codes map[string]authn.Identity }

func (portal) Provider() string { return "portal" }

func (p portal) Verify(_ context.Context, raw string) (*authn.Identity, error) {
	id, ok := p.codes[raw]
	if !ok {
		return nil, errors.New("unknown handoff code")
	}
	return &id, nil
}

func main() {
	p := portal{codes: map[string]authn.Identity{"c-1": {Subject: "u-42", Email: "ada@example.com", Tenant: "default"}}}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := server.Run(ctx, server.WithTrustedIssuer(p, "https://portal.example.com"))
	stop()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(server.ExitCode(err))
	}
}
```

</p>
</details>

<a name="WithUIServer"></a>
### func [WithUIServer](<https://github.com/dexadata/dexaflow/blob/main/pkg/server/server.go#L113>)

```go
func WithUIServer(ui UIServer) Option
```

WithUIServer serves ui in place of the embedded single page app; the ui.\* keys that configure the embedded one \(banners, instance name, home link, favicon, stylesheets\) are then ignored, and boot logs that the UI came from an option. The API, /ui/config included, is unchanged. A nil ui keeps the embedded one; with several options the last one wins.

<details><summary>Example</summary>
<p>

A control plane that serves a UI of its own in place of the embedded one.

```go
package main

import (
	"context"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/dexadata/dexaflow/pkg/server"
)

// shellUI serves a UI built into dist/ by its own toolchain: the bundle under
// /static/ and the same index.html for every client side route.
type shellUI struct{ dist fs.FS }

func (u shellUI) StaticHandler() http.Handler { return http.FileServerFS(u.dist) }

func (u shellUI) Index(w http.ResponseWriter, _ string) {
	b, err := fs.ReadFile(u.dist, "index.html")
	if err != nil {
		http.Error(w, "the UI is not built", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(b)
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := server.Run(ctx, server.WithUIServer(shellUI{dist: os.DirFS("dist")}))
	stop()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(server.ExitCode(err))
	}
}
```

</p>
</details>

<a name="WithXComBackend"></a>
### func [WithXComBackend](<https://github.com/dexadata/dexaflow/blob/main/pkg/server/server.go#L89>)

```go
func WithXComBackend(backend xcomstore.Backend) Option
```

WithXComBackend stores XCom values in backend instead of Redis or, with no Redis configured, the embedded Postgres store; boot logs that the backend came from an option. The server still checks the size and schema of each value and records its metadata in Postgres, and Redis, when configured, still serves the live log tail. A backend that is also an xcomstore.BatchBackend serves a task's bulk pulls in one call. The caller owns it: Run never closes it. A nil backend keeps the configured one; with several options the last one wins.

<details><summary>Example</summary>
<p>

A control plane that keeps XCom values in a store of its own instead of Redis or the embedded Postgres store.

```go
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path"
	"sync"
	"syscall"
	"time"

	"github.com/dexadata/dexaflow/pkg/server"
	"github.com/dexadata/dexaflow/pkg/xcomstore"
)

// mapBackend keeps XCom values in the server's memory: enough for a single
// replica that may lose them on a restart, as a test install can.
type mapBackend struct {
	mu sync.Mutex
	m  map[string]mapEntry
}

type mapEntry struct {
	e       xcomstore.Entry
	expires time.Time
}

func (b *mapBackend) Push(_ context.Context, key string, e xcomstore.Entry, ttl time.Duration) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.m[key] = mapEntry{e: e, expires: time.Now().Add(ttl)}
	return nil
}

func (b *mapBackend) Fetch(_ context.Context, key string) (xcomstore.Entry, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	v, ok := b.m[key]
	if !ok || time.Now().After(v.expires) {
		return xcomstore.Entry{}, xcomstore.ErrNotFound
	}
	return v.e, nil
}

func (b *mapBackend) Delete(_ context.Context, key string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.m, key)
	return nil
}

func (b *mapBackend) List(_ context.Context, pattern string) ([]string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	var keys []string
	for k, v := range b.m {
		if ok, _ := path.Match(pattern, k); ok && time.Now().Before(v.expires) {
			keys = append(keys, k)
		}
	}
	return keys, nil
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := server.Run(ctx, server.WithXComBackend(&mapBackend{m: map[string]mapEntry{}}))
	stop()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(server.ExitCode(err))
	}
}
```

</p>
</details>

<a name="UIServer"></a>
## type [UIServer](<https://github.com/dexadata/dexaflow/blob/main/pkg/server/server.go#L103-L106>)

UIServer serves a web UI in place of the embedded one. The server mounts StaticHandler under /static/, with that prefix stripped from the request path, and calls Index for every other GET that matches no API or /ui/ route, with the base path the UI is served under. Index is reached only with a valid session: a visitor without one is redirected to the server's own sign\-in page first, so a UI never renders for a signed\-out visitor.

```go
type UIServer interface {
    StaticHandler() http.Handler
    Index(w http.ResponseWriter, basePath string)
}
```

Generated by [gomarkdoc](<https://github.com/princjef/gomarkdoc>)
