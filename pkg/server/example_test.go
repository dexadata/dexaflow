package server_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/dexadata/dexaflow/pkg/admission"
	"github.com/dexadata/dexaflow/pkg/authn"
	"github.com/dexadata/dexaflow/pkg/logsink"
	"github.com/dexadata/dexaflow/pkg/server"
	"github.com/dexadata/dexaflow/pkg/xcomstore"
)

// A program that runs the control plane as dexaflow-server does: configured
// by DEXAFLOW_CONFIG and the DEXAFLOW_* environment, stopped by SIGINT or
// SIGTERM. It needs a database, so it has no Output line.
func ExampleRun() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := server.Run(ctx)
	stop()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(server.ExitCode(err))
	}
}

// A control plane that refuses every attempt of one tenant during a freeze,
// with an admission hook compiled into the binary instead of a webhook.
func ExampleWithAdmission() {
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

// A control plane that stores task logs with a sink of its own instead of
// the disk or object store backends.
func ExampleWithLogSink() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := server.Run(ctx, server.WithLogSink(fileSink{dir: "/var/lib/task-logs"}))
	stop()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(server.ExitCode(err))
	}
}

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

// A control plane that keeps XCom values in a store of its own instead of
// Redis or the embedded Postgres store.
func ExampleWithXComBackend() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := server.Run(ctx, server.WithXComBackend(&mapBackend{m: map[string]mapEntry{}}))
	stop()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(server.ExitCode(err))
	}
}

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

// A control plane that serves a UI of its own in place of the embedded one.
func ExampleWithUIServer() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := server.Run(ctx, server.WithUIServer(shellUI{dist: os.DirFS("dist")}))
	stop()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(server.ExitCode(err))
	}
}

// directory signs users in against a company directory, here a fixed table,
// and recognizes the tokens it hands out. tenantID is the tenant's name.
type directory struct {
	tenantID string
	users    map[string]string // email -> password
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

// A control plane whose password sign-in goes to a directory of its own. The
// sessions the server mints itself (SSO, handoff, renewal) keep working.
func ExampleWithAuthenticator() {
	dir := directory{tenantID: "default", users: map[string]string{"ada@example.com": "s3cret"}}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := server.Run(ctx, server.WithAuthenticator(dir))
	stop()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(server.ExitCode(err))
	}
}

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

// A control plane that opens UI sessions from the operator's portal, posted
// from https://portal.example.com.
func ExampleWithTrustedIssuer() {
	p := portal{codes: map[string]authn.Identity{"c-1": {Subject: "u-42", Email: "ada@example.com", Tenant: "default"}}}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := server.Run(ctx, server.WithTrustedIssuer(p, "https://portal.example.com"))
	stop()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(server.ExitCode(err))
	}
}

// A control plane configured by a file of the program's own choosing, with
// the DEXAFLOW_* environment still on top.
func ExampleWithConfigFile() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := server.Run(ctx, server.WithConfigFile("/etc/acme/dexaflow-server.yaml"))
	stop()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(server.ExitCode(err))
	}
}
