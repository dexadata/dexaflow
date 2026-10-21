package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/dexadata/dexaflow/internal/storage"
)

// A replaced database is waited on like any other reason (ADR 0090 decision
// 3), but its readiness detail says how to accept it rather than to migrate,
// and its log line carries both installation ids, which /readyz does not.
func TestWaitForSchemaReplacedNamesTheAcceptance(t *testing.T) {
	var logs bytes.Buffer
	var states recordedStates
	var delays fakeAfter
	replaced := storage.SchemaWait{
		Class:  storage.SchemaReasonReplaced,
		Reason: "database replaced: another installation id",
		Detail: `this release's installation id is "a1", the database carries "b2"`,
	}
	probe := &scriptedSchema{script: []storage.SchemaWait{replaced, replaced, waitReady}}
	w := newSchemaWaiter(probe, &states, slog.New(slog.NewJSONHandler(&logs, nil)))
	w.after = delays.after
	probe.waiter = w
	if err := w.wait(t.Context()); err != nil {
		t.Fatal(err)
	}
	if states[0] != storage.SchemaReasonReplaced {
		t.Errorf("recorded %q, want %q", states[0], storage.SchemaReasonReplaced)
	}
	detail := probe.details[1]
	if !strings.HasPrefix(detail, "database replaced: another installation id: ") || !strings.Contains(detail, "installation.id") {
		t.Errorf("readiness detail = %q, want the reason and how to accept the database", detail)
	}
	if strings.Contains(detail, "helm upgrade <release>") || strings.Contains(detail, "a1") {
		t.Errorf("readiness detail = %q: a replaced database is not fixed by migrating, and /readyz carries no ids", detail)
	}
	lines := jsonLines(t, &logs)
	if len(lines) == 0 || lines[0]["detail"] != replaced.Detail {
		t.Errorf("first log line = %v, want the ids in detail", lines)
	}
}

type notifier struct{ f func() }

func (n *notifier) SetOnReplaced(f func()) { n.f = f }

// ADR 0090 decision 5: a running process whose database is replaced stops,
// and exits with a code of its own, so its restart waits under decision 1.
func TestStopOnReplacedCancelsWithItsOwnExitCode(t *testing.T) {
	n := &notifier{}
	ctx, release := stopOnReplaced(t.Context(), n)
	defer release()
	if ctx.Err() != nil {
		t.Fatal("canceled before the database changed")
	}
	n.f()
	<-ctx.Done()
	err := serveExit(ctx, nil)
	if !errors.Is(err, errDatabaseReplaced) {
		t.Fatalf("serveExit = %v, want errDatabaseReplaced", err)
	}
	if got := exitCode(fmt.Errorf("serving: %w", err)); got != exitDatabaseReplaced {
		t.Errorf("exit code = %d, want %d", got, exitDatabaseReplaced)
	}
	if got := exitCode(errors.New("anything else")); got != 1 {
		t.Errorf("exit code of another error = %d, want 1", got)
	}
}

// A clean stop stays clean, and a serve error wins over the cause.
func TestServeExit(t *testing.T) {
	if err := serveExit(context.Background(), nil); err != nil {
		t.Errorf("a clean stop = %v", err)
	}
	boom := errors.New("listener died")
	ctx, cancel := context.WithCancelCause(context.Background())
	cancel(errDatabaseReplaced)
	if err := serveExit(ctx, boom); !errors.Is(err, boom) {
		t.Errorf("serveExit = %v, want the serve error", err)
	}
	ctx, cancel = context.WithCancelCause(context.Background())
	cancel(errKeyLockLost)
	if err := serveExit(ctx, nil); !errors.Is(err, errKeyLockLost) {
		t.Errorf("serveExit = %v, want the lost key lock", err)
	}
}

// migrations.onBoot (ADR 0090 decision 4) migrates only an absent or behind
// schema: never one ahead, dirty or unreadable, and never a replaced
// database, which migrating would turn into an empty, healthy control plane.
func TestMigrateOnBootOnlyOnAbsentOrBehind(t *testing.T) {
	cases := []struct {
		verdict storage.SchemaWait
		migrate bool
	}{
		{waitAbsent, true},
		{waitBehind, true},
		{waitReady, false},
		{storage.SchemaWait{Ahead: true}, false},
		{storage.SchemaWait{Class: storage.SchemaReasonDirty, Reason: "schema dirty at v40"}, false},
		{storage.SchemaWait{Class: storage.SchemaReasonUnreadable, Reason: "schema unreadable"}, false},
		{storage.SchemaWait{Class: storage.SchemaReasonReplaced, Reason: "database replaced: no schema"}, false},
	}
	for _, c := range cases {
		t.Run(c.verdict.Class+c.verdict.Reason, func(t *testing.T) {
			var ran int
			var logs bytes.Buffer
			migrateOnBoot(t.Context(), &scriptedSchema{script: []storage.SchemaWait{c.verdict}},
				func(context.Context) error { ran++; return nil }, slog.New(slog.NewJSONHandler(&logs, nil)))
			if (ran == 1) != c.migrate {
				t.Errorf("migrated %d times on %+v, want migrate=%v", ran, c.verdict, c.migrate)
			}
		})
	}
}

// A failed self-migration is logged and boot goes on to wait, as it would
// without onBoot: the hook Job may still bring the schema current.
func TestMigrateOnBootFailureFallsBackToTheWait(t *testing.T) {
	var logs bytes.Buffer
	migrateOnBoot(t.Context(), &scriptedSchema{script: []storage.SchemaWait{waitBehind}},
		func(context.Context) error { return errors.New("permission denied for schema public") },
		slog.New(slog.NewJSONHandler(&logs, nil)))
	lines := jsonLines(t, &logs)
	last := lines[len(lines)-1]
	if last["level"] != "WARN" || !strings.Contains(fmt.Sprint(last["error"]), "permission denied") {
		t.Errorf("last log line = %v, want a WARN with the migration error", last)
	}
}
