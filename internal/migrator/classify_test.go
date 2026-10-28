package migrator

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"syscall"
	"testing"

	"github.com/golang-migrate/migrate/v4"
	"github.com/jackc/pgx/v5/pgconn"
)

// The error-mapping table is the point of the wrapper (#1020): every driver
// error the hook Job used to print raw maps to a kind, an exit code the chart's
// podFailurePolicy can act on, and a message that names the cause, the remedy
// and the chart value involved.
func TestClassify(t *testing.T) {
	target := Target{Host: "postgres", Port: "5432", Database: "leoflow_test", User: "dexaflow"}
	_, parseErr := pgconn.ParseConfig("postgres://u:s3cret@host:notaport/db")
	if parseErr == nil {
		t.Fatal("fixture: pgconn.ParseConfig accepted a non-numeric port")
	}
	refused := &net.OpError{Op: "dial", Net: "tcp", Err: &os.SyscallError{Syscall: "connect", Err: syscall.ECONNREFUSED}}

	cases := []struct {
		name     string
		err      error
		kind     Kind
		code     int
		contains []string
		absent   []string
	}{
		{
			name: "missing database",
			err:  fmt.Errorf("failed to open database: %w", &pgconn.PgError{Severity: "FATAL", Code: "3D000", Message: `database "leoflow_test" does not exist`}),
			kind: KindMissingDatabase, code: ExitMissingDatabase,
			contains: []string{
				`database "leoflow_test" does not exist on postgres:5432`,
				"Dexaflow does not create the database",
				"CREATE DATABASE leoflow_test;",
				"database.url",
				"Datastore compatibility",
			},
		},
		{
			name: "wrong password",
			err:  fmt.Errorf("failed to open database: %w", &pgconn.PgError{Severity: "FATAL", Code: "28P01", Message: `password authentication failed for user "dexaflow"`}),
			kind: KindAuth, code: ExitAuth,
			contains: []string{`postgres:5432 rejected user "dexaflow"`, "password authentication failed", "database.url", "databaseUrl"},
		},
		{
			name: "no pg_hba entry",
			err:  &pgconn.PgError{Severity: "FATAL", Code: "28000", Message: `no pg_hba.conf entry for host "10.0.0.7"`},
			kind: KindAuth, code: ExitAuth,
			contains: []string{"no pg_hba.conf entry"},
		},
		{
			name: "connection refused",
			err:  fmt.Errorf("failed to open database: %w", refused),
			kind: KindUnreachable, code: ExitTransient,
			contains: []string{"cannot reach Postgres at postgres:5432", "retried"},
		},
		{
			name: "dial timeout",
			err:  fmt.Errorf("pinging: %w", context.DeadlineExceeded),
			kind: KindUnreachable, code: ExitTransient,
			contains: []string{"cannot reach Postgres at postgres:5432"},
		},
		{
			name: "unknown host",
			err:  &net.DNSError{Err: "no such host", Name: "postgres", IsNotFound: true},
			kind: KindUnreachable, code: ExitTransient,
		},
		{
			name: "server starting up",
			err:  &pgconn.PgError{Severity: "FATAL", Code: "57P03", Message: "the database system is starting up"},
			kind: KindUnreachable, code: ExitTransient,
		},
		{
			name: "too many connections",
			err:  &pgconn.PgError{Severity: "FATAL", Code: "53300", Message: "sorry, too many clients already"},
			kind: KindUnreachable, code: ExitTransient,
		},
		{
			name: "lock held by another migration",
			err:  migrate.ErrLockTimeout,
			kind: KindLocked, code: ExitTransient,
			contains: []string{"another migration"},
		},
		{
			name: "dirty schema",
			err:  migrate.ErrDirty{Version: 27},
			kind: KindDirty, code: ExitDirty,
			contains: []string{"dirty at version 27", "/operate/upgrades/#an-interrupted-migration", "force"},
		},
		{
			name: "unparseable DSN keeps the password out",
			err:  parseErr,
			kind: KindBadDSN, code: ExitBadDSN,
			contains: []string{"database.url"},
			absent:   []string{"s3cret"},
		},
		{
			name: "anything else is retried",
			err:  errors.New("boom"),
			kind: KindOther, code: ExitFailed,
			contains: []string{"boom"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := Classify(tc.err, target)
			if f == nil {
				t.Fatal("Classify returned nil for a non-nil error")
			}
			if f.Kind != tc.kind {
				t.Errorf("kind = %v, want %v", f.Kind, tc.kind)
			}
			if f.Code != tc.code {
				t.Errorf("exit code = %d, want %d", f.Code, tc.code)
			}
			msg := f.Error()
			for _, s := range tc.contains {
				if !strings.Contains(msg, s) {
					t.Errorf("message lacks %q:\n%s", s, msg)
				}
			}
			for _, s := range tc.absent {
				if strings.Contains(msg, s) {
					t.Errorf("message leaks %q:\n%s", s, msg)
				}
			}
			if !errors.Is(f, tc.err) && tc.kind != KindBadDSN {
				t.Errorf("the failure does not wrap the driver error, so callers cannot errors.Is it")
			}
		})
	}
}

func TestClassifyNil(t *testing.T) {
	if f := Classify(nil, Target{}); f != nil {
		t.Fatalf("Classify(nil) = %v, want nil", f)
	}
}

// A Failure already classified (the DSN and CA preflights return one) passes
// through unchanged instead of being reclassified as KindOther.
func TestClassifyKeepsAFailure(t *testing.T) {
	in := &Failure{Kind: KindMissingCA, Code: ExitMissingCA, Summary: "x"}
	if got := Classify(fmt.Errorf("wrapped: %w", in), Target{}); got != in {
		t.Fatalf("Classify reclassified a Failure: %#v", got)
	}
}

// The deterministic codes are what podFailurePolicy fails the Job on; a code
// in that set that a transient error can return would stop retries on a
// database that is only starting up.
func TestDeterministicExitCodesExcludeTransientOnes(t *testing.T) {
	for _, c := range DeterministicExitCodes() {
		if c == ExitTransient || c == ExitFailed || c == 0 {
			t.Fatalf("deterministic set contains %d, which a retryable error returns", c)
		}
	}
	want := map[int]bool{ExitUsage: true, ExitBadDSN: true, ExitMissingDatabase: true, ExitAuth: true, ExitMissingCA: true, ExitDirty: true, ExitReplaced: true}
	got := DeterministicExitCodes()
	if len(got) != len(want) {
		t.Fatalf("DeterministicExitCodes() = %v, want the %d codes of %v", got, len(want), want)
	}
	for _, c := range got {
		if !want[c] {
			t.Fatalf("unexpected deterministic code %d", c)
		}
	}
}

func TestQuoteIdent(t *testing.T) {
	cases := map[string]string{
		"leoflow_test": "leoflow_test",
		"dexaflow2":    "dexaflow2",
		"Dexa":         `"Dexa"`,
		"my-db":        `"my-db"`,
		`we"ird`:       `"we""ird"`,
		"1abc":         `"1abc"`,
	}
	for in, want := range cases {
		if got := quoteIdent(in); got != want {
			t.Errorf("quoteIdent(%q) = %s, want %s", in, got, want)
		}
	}
}
