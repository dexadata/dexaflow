package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/dexadata/dexaflow/internal/auth"
)

// fakeAuditWriter records every audit entry attempt and can be told to return
// an error so the recordTaskAudit "log-and-continue" branch is exercised.
type fakeAuditWriter struct {
	calls []recordedAudit
	err   error
}

type recordedAudit struct {
	tenant, userID, action, dagID, runID, taskID string
	tryNumber                                    int
}

func (f *fakeAuditWriter) RecordTaskActionAudit(_ context.Context, tenant, userID, action, dagID, runID, taskID string, tryNumber int) error {
	f.calls = append(f.calls, recordedAudit{tenant, userID, action, dagID, runID, taskID, tryNumber})
	return f.err
}

// TestRecordTaskAuditNonFatal pins the contract on the audit side-channel:
// audit failure NEVER fails the user action. The function is best-effort by
// design — a flaky audit DB must not be allowed to flip "clear task instance"
// from 200 to 5xx in the UI. The branches here are: nil writer (no-op);
// no-user in context (action recorded with empty userID); writer error
// (logged and dropped). Each is a real production shape during an outage.
func TestRecordTaskAuditNonFatal(t *testing.T) {
	mkCtx := func(user *auth.User) *gin.Context {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/", http.NoBody)
		c.Params = gin.Params{{Key: "dag_id", Value: "etl"}}
		if user != nil {
			c.Set(contextKeyUser, user)
		}
		return c
	}

	t.Run("nil writer → no-op (no panic)", func(t *testing.T) {
		// If this returns, the early-return branch worked.
		recordTaskAudit(mkCtx(&auth.User{ID: "u1"}), nil, "clear", "r1", "extract", 1)
	})

	t.Run("happy path: writer records the action with the user id", func(t *testing.T) {
		w := &fakeAuditWriter{}
		recordTaskAudit(mkCtx(&auth.User{ID: "u1"}), w, "clear", "r1", "extract", 2)
		if len(w.calls) != 1 {
			t.Fatalf("calls = %d, want 1", len(w.calls))
		}
		got := w.calls[0]
		if got.userID != "u1" || got.action != "clear" || got.taskID != "extract" || got.tryNumber != 2 {
			t.Errorf("recorded audit = %+v, want u1/clear/extract/try=2", got)
		}
	})

	t.Run("no user in context: action still recorded with empty user id", func(t *testing.T) {
		w := &fakeAuditWriter{}
		recordTaskAudit(mkCtx(nil), w, "clear", "r1", "extract", 1)
		if len(w.calls) != 1 || w.calls[0].userID != "" {
			t.Errorf("expected one call with empty userID, got %+v", w.calls)
		}
	})

	t.Run("writer error is logged, not propagated", func(t *testing.T) {
		w := &fakeAuditWriter{err: errors.New("audit table briefly unreachable")}
		// No way for the caller to observe the error; if the helper grew a
		// return value the user-facing action would start failing here. The
		// test passes by simply returning.
		recordTaskAudit(mkCtx(&auth.User{ID: "u1"}), w, "mark_failed", "r1", "extract", 1)
		if len(w.calls) != 1 {
			t.Errorf("writer should still be called once on error, got %d", len(w.calls))
		}
	})
}
