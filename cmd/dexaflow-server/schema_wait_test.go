package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/config"
	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/observability"
	"github.com/dexadata/dexaflow/internal/storage"
	"github.com/prometheus/client_golang/prometheus"
)

// scriptedSchema answers SchemaWaitState from a script, repeating the last
// entry, and records what the gate said before each read.
type scriptedSchema struct {
	script  []storage.SchemaWait
	fatal   error
	calls   int
	waiter  *schemaWaiter
	details []string
}

func (s *scriptedSchema) SchemaWaitState(context.Context) (storage.SchemaWait, error) {
	if s.waiter != nil {
		s.details = append(s.details, s.waiter.Detail())
	}
	if s.fatal != nil {
		return storage.SchemaWait{}, s.fatal
	}
	i := min(s.calls, len(s.script)-1)
	s.calls++
	return s.script[i], nil
}

type recordedStates []string

func (r *recordedStates) RecordSchemaState(reason string) { *r = append(*r, reason) }

// fakeAfter fires at once and remembers each delay asked for.
type fakeAfter []time.Duration

func (f *fakeAfter) after(d time.Duration) <-chan time.Time {
	*f = append(*f, d)
	c := make(chan time.Time, 1)
	c <- time.Time{}
	return c
}

var (
	waitAbsent = storage.SchemaWait{Class: storage.SchemaReasonAbsent, Reason: "schema absent"}
	waitBehind = storage.SchemaWait{Class: storage.SchemaReasonBehind, Reason: "schema behind v40, requires v46"}
	waitReady  = storage.SchemaWait{}
)

func jsonLines(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("log line %q: %v", line, err)
		}
		out = append(out, m)
	}
	return out
}

// ADR 0090 decision 1: an absent or behind schema is waited through, with one
// log line per state change (not per attempt), the reason on /readyz and in
// the metric, and a backoff of 1s doubling to 30s.
func TestWaitForSchemaWaitsThroughAbsentAndBehind(t *testing.T) {
	var logs bytes.Buffer
	var states recordedStates
	var delays fakeAfter
	probe := &scriptedSchema{script: []storage.SchemaWait{waitAbsent, waitAbsent, waitAbsent, waitBehind, waitBehind, waitReady}}
	w := newSchemaWaiter(probe, &states, slog.New(slog.NewJSONHandler(&logs, nil)))
	w.after = delays.after
	probe.waiter = w

	if err := w.wait(t.Context()); err != nil {
		t.Fatalf("wait: %v", err)
	}

	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second}
	if fmt.Sprint(delays) != fmt.Sprint(want) {
		t.Errorf("delays = %v, want %v", delays, want)
	}
	wantStates := []string{"schema absent", "schema absent", "schema absent", "schema behind", "schema behind", ""}
	if fmt.Sprint(states) != fmt.Sprint(wantStates) {
		t.Errorf("recorded states = %q, want %q", states, wantStates)
	}
	if !strings.HasPrefix(probe.details[1], "schema absent: ") || !strings.HasPrefix(probe.details[4], "schema behind v40, requires v46: ") {
		t.Errorf("the gate's readiness detail did not follow the verdict: %q", probe.details)
	}
	if !strings.Contains(probe.details[1], "helm upgrade") || !strings.Contains(probe.details[1], "dexaflow db migrate") {
		t.Errorf("the readiness detail does not say what to run: %q", probe.details[1])
	}

	lines := jsonLines(t, &logs)
	if len(lines) != 3 {
		t.Fatalf("logged %d lines, want one per state change (absent, behind, ready): %s", len(lines), logs.String())
	}
	for i, wantReason := range []string{"schema absent", "schema behind v40, requires v46"} {
		if lines[i]["reason"] != wantReason || lines[i]["action"] == nil || lines[i]["level"] != "WARN" {
			t.Errorf("line %d = %v, want a WARN with reason %q and an action", i, lines[i], wantReason)
		}
	}
	if lines[2]["level"] != "INFO" || !strings.Contains(fmt.Sprint(lines[2]["msg"]), "current") {
		t.Errorf("last line = %v, want the INFO that the schema is current", lines[2])
	}
}

func TestWaitForSchemaBackoffCapsAtThirtySeconds(t *testing.T) {
	var delays fakeAfter
	script := make([]storage.SchemaWait, 9)
	for i := range 8 {
		script[i] = waitAbsent
	}
	var states recordedStates
	w := newSchemaWaiter(&scriptedSchema{script: script}, &states, discardLogger())
	w.after = delays.after
	if err := w.wait(t.Context()); err != nil {
		t.Fatal(err)
	}
	want := "[1s 2s 4s 8s 16s 30s 30s 30s]"
	if fmt.Sprint(delays) != want {
		t.Errorf("delays = %v, want %s", delays, want)
	}
}

// A current schema on the first read boots without logging a wait.
func TestWaitForSchemaCurrentAtOnceLogsNothing(t *testing.T) {
	var logs bytes.Buffer
	var states recordedStates
	var delays fakeAfter
	w := newSchemaWaiter(&scriptedSchema{script: []storage.SchemaWait{waitReady}}, &states, slog.New(slog.NewJSONHandler(&logs, nil)))
	w.after = delays.after
	if err := w.wait(t.Context()); err != nil {
		t.Fatal(err)
	}
	if logs.Len() != 0 || len(delays) != 0 || len(states) != 1 || states[0] != "" {
		t.Errorf("a current schema logged %q, waited %v, recorded %q", logs.String(), delays, states)
	}
}

// A schema dirty at this binary's latest still stops boot (ADR 0090
// decision 1): no wait finishes a half-applied migration.
func TestWaitForSchemaDirtyAtLatestStopsBoot(t *testing.T) {
	fatal := fmt.Errorf("%w: schema is dirty at v46", domain.ErrSchemaNotCurrent)
	var states recordedStates
	var delays fakeAfter
	w := newSchemaWaiter(&scriptedSchema{fatal: fatal}, &states, discardLogger())
	w.after = delays.after
	if err := w.wait(t.Context()); !errors.Is(err, domain.ErrSchemaNotCurrent) {
		t.Fatalf("wait = %v, want the dirty-schema error", err)
	}
	if len(delays) != 0 {
		t.Errorf("waited %v before refusing a dirty schema", delays)
	}
}

// A read that fails keeps waiting and logs its cause once.
func TestWaitForSchemaUnreadableKeepsWaiting(t *testing.T) {
	var logs bytes.Buffer
	unreadable := storage.SchemaWait{Class: storage.SchemaReasonUnreadable, Reason: "schema unreadable", Err: errors.New("connection reset")}
	var states recordedStates
	w := newSchemaWaiter(&scriptedSchema{script: []storage.SchemaWait{unreadable, unreadable, waitReady}}, &states, slog.New(slog.NewJSONHandler(&logs, nil)))
	var delays fakeAfter
	w.after = delays.after
	if err := w.wait(t.Context()); err != nil {
		t.Fatal(err)
	}
	lines := jsonLines(t, &logs)
	if len(lines) != 2 || lines[0]["error"] != "connection reset" {
		t.Errorf("logs = %s, want one wait line carrying the read error, then ready", logs.String())
	}
}

// SIGTERM while waiting is a clean stop, not a boot failure.
func TestWaitForSchemaStopsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	var states recordedStates
	w := newSchemaWaiter(&scriptedSchema{script: []storage.SchemaWait{waitAbsent}}, &states, discardLogger())
	w.after = func(time.Duration) <-chan time.Time { cancel(); return make(chan time.Time) }
	if err := w.wait(ctx); !errors.Is(err, errStoppedWaitingForSchema) {
		t.Fatalf("wait = %v, want errStoppedWaitingForSchema", err)
	}
}

// The gate binds the metrics port in every role and the API port only where
// the role serves the API, before the schema is read, and frees both on close
// so the real listeners can bind them.
func TestSchemaGateBindsTheProbePorts(t *testing.T) {
	for _, role := range []string{"all", "scheduler"} {
		t.Run(role, func(t *testing.T) {
			cfg := &config.ServerConfig{}
			cfg.Server.Role = role
			cfg.Server.HTTPAddr = "127.0.0.1:0"
			cfg.Server.MetricsAddr = "127.0.0.1:0"
			tel := &observability.Telemetry{Logger: discardLogger(), Registry: prometheus.NewRegistry()}
			tel.Metrics = observability.NewMetrics(tel.Registry)
			gate, err := startSchemaGate(t.Context(), cfg, tel, func() string { return "schema absent: apply" })
			if err != nil {
				t.Fatal(err)
			}
			wantListeners := map[string]int{"all": 2, "scheduler": 1}[role]
			if len(gate.addrs()) != wantListeners {
				t.Fatalf("listeners = %v, want %d", gate.addrs(), wantListeners)
			}
			for _, addr := range gate.addrs() {
				for path, want := range map[string]int{"/healthz": 200, "/readyz": 503} {
					req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+addr+path, http.NoBody)
					resp, err := http.DefaultClient.Do(req)
					if err != nil {
						t.Fatalf("%s%s: %v", addr, path, err)
					}
					_, _ = io.Copy(io.Discard, resp.Body)
					_ = resp.Body.Close()
					if resp.StatusCode != want {
						t.Errorf("%s%s = %d, want %d", addr, path, resp.StatusCode, want)
					}
				}
			}
			addrs := gate.addrs()
			gate.Close()
			for _, addr := range addrs {
				req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+addr+"/healthz", http.NoBody)
				if resp, err := http.DefaultClient.Do(req); err == nil {
					_ = resp.Body.Close()
					t.Errorf("%s still answers after the gate closed", addr)
				}
			}
		})
	}
}

// run returns nil, a clean exit, for a shutdown during the wait, and every
// other boot error as it was.
func TestStoppedWhileWaitingIsACleanStop(t *testing.T) {
	if err := stoppedWhileWaiting(fmt.Errorf("postgres: %w", errStoppedWaitingForSchema), discardLogger()); err != nil {
		t.Errorf("a shutdown while waiting = %v, want nil", err)
	}
	boom := errors.New("boom")
	if err := stoppedWhileWaiting(boom, discardLogger()); !errors.Is(err, boom) {
		t.Errorf("another boot error = %v, want it passed through", err)
	}
}
