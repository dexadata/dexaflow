package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/domain"
)

// The fixtures below are the error VALUES the driver hands us, not hand-written
// strings: a *pgconn.PgError renders itself as
// "severity: message (SQLSTATE code)", and its message carries the constraint,
// table and column names of our schema. A test built on a hand-made string
// would only prove that a clean string stays clean.

func pgForeignKeyViolation() *pgconn.PgError {
	return &pgconn.PgError{
		Severity:            "ERROR",
		SeverityUnlocalized: "ERROR",
		Code:                "23503",
		Message:             `insert or update on table "dag_versions" violates foreign key constraint "dag_versions_dag_id_fkey"`,
		Detail:              `Key (dag_id)=(6f1c0f2e-0c3f-4a1d-9a3e-2c9a0f4b7d11) is not present in table "dags".`,
		SchemaName:          "leoflow",
		TableName:           "dag_versions",
		ConstraintName:      "dag_versions_dag_id_fkey",
		File:                "ri_triggers.c",
		Line:                2528,
		Routine:             "ri_ReportViolation",
	}
}

func pgNotNullViolation() *pgconn.PgError {
	return &pgconn.PgError{
		Severity:            "ERROR",
		SeverityUnlocalized: "ERROR",
		Code:                "23502",
		Message:             `null value in column "spec_hash" of relation "dag_versions" violates not-null constraint`,
		SchemaName:          "leoflow",
		TableName:           "dag_versions",
		ColumnName:          "spec_hash",
		File:                "execMain.c",
		Routine:             "ExecConstraints",
	}
}

func pgUndefinedTable() *pgconn.PgError {
	return &pgconn.PgError{
		Severity:            "ERROR",
		SeverityUnlocalized: "ERROR",
		Code:                "42P01",
		Message:             `relation "dag_versions" does not exist`,
		Position:            13,
		File:                "parse_relation.c",
		Routine:             "parserOpenTable",
	}
}

func pgTooManyConnections() *pgconn.PgError {
	return &pgconn.PgError{
		Severity:            "FATAL",
		SeverityUnlocalized: "FATAL",
		Code:                "53300",
		Message:             "sorry, too many clients already",
		File:                "proc.c",
		Routine:             "InitProcess",
	}
}

// pgLeakTokens are the substrings that must never appear in a response body for
// pe: the SQLSTATE marker and code, the server's message (which carries the
// constraint name), and every schema identifier the driver attached.
func pgLeakTokens(pe *pgconn.PgError) []string {
	tokens := []string{"SQLSTATE", pe.Code, pe.Message, pe.Error(), pe.Severity + ":"}
	for _, id := range []string{pe.ConstraintName, pe.TableName, pe.ColumnName, pe.SchemaName, pe.Routine, pe.File} {
		if id != "" {
			tokens = append(tokens, id)
		}
	}
	return tokens
}

func assertNoPgLeak(t *testing.T, where, body string, pe *pgconn.PgError, echoed ...string) {
	t.Helper()
	scanned := leakScanTarget(body, echoed...)
	for _, leak := range pgLeakTokens(pe) {
		if strings.Contains(scanned, leak) {
			t.Errorf("%s: response body leaks database internals (%q): %s", where, leak, scanned)
		}
	}
}

// TestHandleRepoErrorNeverLeaksDriverTextToTheClient is the regression guard for
// #961. Every branch of the repository-error funnel is driven with a real
// *pgconn.PgError riding along, because that is how the leak reappears: a new
// SQLSTATE that mapConflict does not translate, or an existing sentinel joined
// with the driver error that produced it. The status classes must survive
// unchanged (a conflict stays a 409, a missing row stays a 404) while the body
// carries none of the driver's text — and the server log must still carry all
// of it, or we have traded a disclosure bug for a blindness bug.
func TestHandleRepoErrorNeverLeaksDriverTextToTheClient(t *testing.T) {
	gin.SetMode(gin.TestMode)
	fk := pgForeignKeyViolation()
	notNull := pgNotNullViolation()
	undefined := pgUndefinedTable()
	tooMany := pgTooManyConnections()

	cases := []struct {
		name   string
		err    error
		pgErr  *pgconn.PgError
		gone   bool // the caller hung up: its request context is done
		status int
	}{
		{
			name:   "unmapped SQLSTATE falls through to 500",
			err:    fmt.Errorf("upserting dag: %w", fk),
			pgErr:  fk,
			status: http.StatusInternalServerError,
		},
		{
			name:   "undefined table (migration behind) is a 500",
			err:    fmt.Errorf("checking existing version: %w", undefined),
			pgErr:  undefined,
			status: http.StatusInternalServerError,
		},
		{
			name:   "connection exhaustion is a 500",
			err:    fmt.Errorf("listing dags: %w", tooMany),
			pgErr:  tooMany,
			status: http.StatusInternalServerError,
		},
		{
			name:   "driver error joined to a not-found stays a 404",
			err:    errors.Join(domain.ErrNotFound, notNull),
			pgErr:  notNull,
			status: http.StatusNotFound,
		},
		{
			name:   "driver error joined to a conflict stays a 409",
			err:    errors.Join(domain.ErrConflict, fk),
			pgErr:  fk,
			status: http.StatusConflict,
		},
		{
			name:   "driver error joined to a validation failure stays a 400",
			err:    errors.Join(domain.ErrValidation, fk),
			pgErr:  fk,
			status: http.StatusBadRequest,
		},
		{
			name:   "driver error joined to a client cancel stays a 499",
			err:    errors.Join(context.Canceled, fk),
			pgErr:  fk,
			gone:   true,
			status: statusClientClosedRequest,
		},
		{
			// Same shape, caller still connected: a pgconn connect timeout carries
			// context.DeadlineExceeded in its chain, and routing on that alone
			// reported an outage as the tenant hanging up (#1071).
			name:   "a connect timeout with a live caller is a 500, and still leaks nothing",
			err:    fmt.Errorf("listing dags: %w", errors.Join(context.DeadlineExceeded, fk)),
			pgErr:  fk,
			status: http.StatusInternalServerError,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var logBuf bytes.Buffer
			logger := slog.New(slog.NewJSONHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))
			r := gin.New()
			r.Use(StructuredLogger(logger))
			r.GET("/x", func(c *gin.Context) { handleRepoError(c, tc.err) })

			rec := httptest.NewRecorder()
			ctx, cancel := context.WithCancel(context.Background())
			if tc.gone {
				cancel()
			} else {
				defer cancel()
			}
			r.ServeHTTP(rec, httptest.NewRequestWithContext(ctx, http.MethodGet, "/x", http.NoBody))

			if rec.Code != tc.status {
				t.Errorf("status = %d, want %d (%s)", rec.Code, tc.status, rec.Body.String())
			}
			assertNoPgLeak(t, "handleRepoError", rec.Body.String(), tc.pgErr)

			// The operator keeps everything the tenant lost: the SQLSTATE, the
			// server's message (constraint name included) and the operation the
			// repository was performing.
			var logLine map[string]any
			if err := json.Unmarshal(logBuf.Bytes(), &logLine); err != nil {
				t.Fatalf("log line is not JSON: %q", logBuf.String())
			}
			cause, _ := logLine["cause"].(string)
			for _, want := range []string{tc.pgErr.Code, tc.pgErr.Message, tc.pgErr.Error()} {
				if !strings.Contains(cause, want) {
					t.Errorf("server log lost %q, so the failure is no longer diagnosable: %v", want, logLine)
				}
			}
		})
	}
}

// TestHandleRepoErrorRedactsAConnectionString covers the case no PgError fixture
// can: a pgconn.ConnectError renders the database user and name, and a connect
// TIMEOUT also satisfies errors.Is(err, context.DeadlineExceeded) — so before
// #961 it took the 499 branch and echoed the DSN identity there, nowhere near
// the 500 the issue was written about. The error is produced by dialing, not
// hand-built, because ConnectError's inner error is unexported.
//
// The dial is to a refused port rather than to a black-holed address so the
// fixture is deterministic and costs no network wait. That makes it a real
// driver error but NOT a deadline one, so the deadline chain — the whole of
// #1071 — is composed onto it explicitly below rather than left to whether a
// runner's dial happened to time out. The earlier version dialed TEST-NET-3 with
// a 250ms budget and accepted EITHER status, so it asserted nothing about which
// branch ran.
func TestHandleRepoErrorRedactsAConnectionString(t *testing.T) {
	gin.SetMode(gin.TestMode)
	// Port 1 on loopback refuses immediately: a real *pgconn.ConnectError, whose
	// text is what this test exists to keep out of the body.
	dialCtx, cancelDial := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelDial()
	_, connErr := pgconn.Connect(dialCtx,
		"postgres://leoflow_app:s3cr3t@127.0.0.1:1/leoflow_meta?sslmode=disable")
	if connErr == nil {
		t.Skip("the dial unexpectedly succeeded; no connect error to redact")
	}
	if !strings.Contains(connErr.Error(), "leoflow_app") {
		t.Fatalf("the driver no longer renders the DSN identity, so this test would pass vacuously: %v", connErr)
	}

	cases := []struct {
		name   string
		err    error
		gone   bool // the caller hung up: its request context is done
		status int
	}{
		{
			// The shape #1071 was filed about, with the real driver error: an
			// unreachable database, a caller still on the other end. The deadline in
			// the chain must not make it the tenant's fault.
			name:   "unreachable database, live caller",
			err:    fmt.Errorf("listing dags: %w", errors.Join(context.DeadlineExceeded, connErr)),
			status: http.StatusInternalServerError,
		},
		{
			// The same driver text on the branch #961 was about: the caller really
			// did hang up, so 499 is right — and the DSN must stay out of the body
			// there too, which is the branch that used to echo it.
			name:   "same driver error, caller gone",
			err:    fmt.Errorf("listing dags: %w", errors.Join(context.Canceled, connErr)),
			gone:   true,
			status: statusClientClosedRequest,
		},
		{
			// No cancellation anywhere in the chain: the plain 500.
			name:   "connect refused, no cancellation in the chain",
			err:    fmt.Errorf("listing dags: %w", connErr),
			status: http.StatusInternalServerError,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.gone {
				cancel()
			}
			c.Request = httptest.NewRequestWithContext(ctx, http.MethodGet, "/x", http.NoBody)
			handleRepoError(c, tc.err)

			if rec.Code != tc.status {
				t.Errorf("status = %d, want %d (%s)", rec.Code, tc.status, rec.Body.String())
			}
			// Whichever branch it lands on, the identity of the database must not
			// be in the body.
			body := rec.Body.String()
			for _, leak := range []string{"leoflow_app", "leoflow_meta", "127.0.0.1", "failed to connect", connErr.Error()} {
				if strings.Contains(body, leak) {
					t.Errorf("response body leaks the connection string (%q): %s", leak, body)
				}
			}
		})
	}
}

// TestRegisterVersionUnmappedDriverErrorIsOpaque drives the real route the issue
// was found on. mapConflict translates only 23505; a 23503 reaches the handler
// unchanged, and before #961 the constraint name and SQLSTATE went straight into
// the 500 body.
func TestRegisterVersionUnmappedDriverErrorIsOpaque(t *testing.T) {
	fk := pgForeignKeyViolation()
	repo := &fakeVersionRepo{err: fmt.Errorf("upserting dag: %w", fk)}
	rec := authGet(versionServer(repo), http.MethodPost, "/api/v2/dags/etl/versions", validSpecJSON)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("unmapped driver error = %d, want 500 (%s)", rec.Code, rec.Body.String())
	}
	assertNoPgLeak(t, "POST /versions", rec.Body.String(), fk, "etl")
}

// TestTriggerDagRunSpecReadDriverErrorIsOpaque covers the second unguarded call
// site: applyDeclaredParams concatenated the spec-read error into its own 500
// detail, bypassing the funnel entirely.
func TestTriggerDagRunSpecReadDriverErrorIsOpaque(t *testing.T) {
	fk := pgForeignKeyViolation()
	srv := NewServer(Dependencies{
		Logger:        discardLogger(),
		Authenticator: &fakeAuthn{user: &auth.User{ID: "u1", TenantID: "default", Roles: []string{"admin"}}},
		RateLimiter:   auth.NewRateLimiter(100, time.Minute),
		CORSOrigins:   []string{"*"},
		DagRuns:       &fakeRunRepo{},
		Specs:         &fakeSpecReader{err: fmt.Errorf("loading current spec: %w", fk)},
	})
	rec := authGet(srv, http.MethodPost, "/api/v2/dags/etl/dagRuns", `{"dag_run_id":"manual__1","conf":{"limit":5}}`)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("spec read failure = %d, want 500 (%s)", rec.Code, rec.Body.String())
	}
	assertNoPgLeak(t, "POST /dagRuns", rec.Body.String(), fk, "etl")
}

// TestSafeErrorMessagesStillReachTheClient is the other half of the contract.
// Redacting everything would be easy and useless: the messages Leoflow composes
// itself — an unknown role, a max_active_runs cap — are the ones a caller can
// act on, so they must survive the redaction that removes the driver's.
func TestSafeErrorMessagesStillReachTheClient(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cases := []struct {
		name   string
		err    error
		status int
		want   string
	}{
		{
			name:   "validation message survives",
			err:    domain.Safef(domain.ErrValidation, "unknown role %q", "analyst"),
			status: http.StatusBadRequest,
			want:   `unknown role "analyst"`,
		},
		{
			name:   "conflict message survives",
			err:    fmt.Errorf("creating dag run: %w", domain.Safef(domain.ErrConflict, "dag %q is at max_active_runs cap of %d", "etl", 3)),
			status: http.StatusConflict,
			want:   `dag "etl" is at max_active_runs cap of 3`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/x", http.NoBody)
			handleRepoError(c, tc.err)
			if rec.Code != tc.status {
				t.Fatalf("status = %d, want %d (%s)", rec.Code, tc.status, rec.Body.String())
			}
			var p Problem
			if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
				t.Fatalf("body is not a problem document: %v (%s)", err, rec.Body.String())
			}
			if !strings.Contains(p.Detail, tc.want) {
				t.Errorf("detail = %q, want it to carry %q", p.Detail, tc.want)
			}
		})
	}
}

// TestSafeErrorDoesNotCarryDriverTextThrough guards the one way the allowlist
// could be defeated: a Safef message is returned verbatim, so interpolating a
// driver error into one would reopen the hole. Nothing in the tree does it, and
// this pins that a Safef built around a sentinel alone stays clean.
func TestSafeErrorDoesNotCarryDriverTextThrough(t *testing.T) {
	fk := pgForeignKeyViolation()
	err := errors.Join(domain.Safef(domain.ErrConflict, "the default pool cannot be deleted"), fk)

	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/x", http.NoBody)
	handleRepoError(c, err)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (%s)", rec.Code, rec.Body.String())
	}
	assertNoPgLeak(t, "safe conflict beside a driver error", rec.Body.String(), fk)
	if !strings.Contains(rec.Body.String(), "the default pool cannot be deleted") {
		t.Errorf("the composed message was dropped: %s", rec.Body.String())
	}
}
