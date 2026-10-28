package cli

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A failed compile publishes an import error (UI banner); a good compile clears
// it. Reporting is best-effort and must never mask the reload's own result.
func TestDevReportingReloadPushesAndClears(t *testing.T) {
	t.Parallel()
	var puts, dels int
	var lastBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPut:
			puts++
			b, _ := io.ReadAll(r.Body)
			lastBody = string(b)
			w.WriteHeader(http.StatusNoContent)
		case http.MethodDelete:
			dels++
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	defer srv.Close()
	ctx := context.Background()

	// Failing reload -> PUT with the error, and the error propagates.
	failing := devReportingReload(ctx, func() error { return errors.New("SyntaxError: '(' was never closed") },
		srv.URL, "tok", "dags/x/dag.py")
	if err := failing(); err == nil {
		t.Fatal("expected the reload error to propagate")
	}
	if puts != 1 {
		t.Fatalf("expected 1 PUT, got %d", puts)
	}
	if !strings.Contains(lastBody, "SyntaxError") || !strings.Contains(lastBody, "dags/x/dag.py") {
		t.Fatalf("PUT body missing filename/trace: %s", lastBody)
	}

	// Succeeding reload -> DELETE (clear the banner), no error.
	ok := devReportingReload(ctx, func() error { return nil }, srv.URL, "tok", "dags/x/dag.py")
	if err := ok(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dels != 1 {
		t.Fatalf("expected 1 DELETE, got %d", dels)
	}
}
