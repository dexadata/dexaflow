package server_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
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
