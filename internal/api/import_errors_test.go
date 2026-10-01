package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/domain"
)

type fakeImportErrorStore struct {
	errs map[string]domain.ImportError
}

func (f *fakeImportErrorStore) ListImportErrors(_ context.Context, _ string) ([]domain.ImportError, error) {
	out := make([]domain.ImportError, 0, len(f.errs))
	for _, e := range f.errs {
		out = append(out, e)
	}
	return out, nil
}

func (f *fakeImportErrorStore) SetImportError(_ context.Context, _ string, e domain.ImportError) error {
	if f.errs == nil {
		f.errs = map[string]domain.ImportError{}
	}
	f.errs[e.Filename] = e
	return nil
}

func (f *fakeImportErrorStore) ClearImportError(_ context.Context, _, filename string) error {
	delete(f.errs, filename)
	return nil
}

func importErrorServer(store ImportErrorStore) *gin.Engine {
	return NewServer(Dependencies{
		Logger:        discardLogger(),
		Authenticator: &fakeAuthn{user: &auth.User{ID: "u1", TenantID: "default", Roles: []string{"admin"}}},
		RateLimiter:   auth.NewRateLimiter(100, time.Minute),
		CORSOrigins:   []string{"*"},
		ImportErrors:  store,
	})
}

func TestImportErrorsListReturnsAirflowShape(t *testing.T) {
	store := &fakeImportErrorStore{errs: map[string]domain.ImportError{
		"dags/broken/dag.py": {
			Filename: "dags/broken/dag.py", StackTrace: "SyntaxError: '(' was never closed",
			BundleName: "leoflow", Timestamp: time.Now(),
		},
	}}
	srv := importErrorServer(store)

	w := authGet(srv, http.MethodGet, "/api/v2/importErrors", "")
	if w.Code != http.StatusOK {
		t.Fatalf("GET importErrors: status %d, body %s", w.Code, w.Body.String())
	}
	var got struct {
		ImportErrors []struct {
			ImportErrorID int    `json:"import_error_id"`
			Filename      string `json:"filename"`
			StackTrace    string `json:"stack_trace"`
			Timestamp     string `json:"timestamp"`
		} `json:"import_errors"`
		TotalEntries int `json:"total_entries"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.TotalEntries != 1 || len(got.ImportErrors) != 1 {
		t.Fatalf("want 1 import error, got %d/%d", got.TotalEntries, len(got.ImportErrors))
	}
	e := got.ImportErrors[0]
	if e.Filename != "dags/broken/dag.py" || !strings.Contains(e.StackTrace, "SyntaxError") {
		t.Fatalf("unexpected entry: %+v", e)
	}
	if e.ImportErrorID == 0 || e.Timestamp == "" {
		t.Fatalf("import_error_id/timestamp must be populated: %+v", e)
	}
}

func TestImportErrorPushAndClear(t *testing.T) {
	store := &fakeImportErrorStore{errs: map[string]domain.ImportError{}}
	srv := importErrorServer(store)

	// push (upsert) an import error
	body := `{"filename":"dags/x/dag.py","stack_trace":"boom","bundle_name":"leoflow"}`
	w := authGet(srv, http.MethodPut, "/api/v2/importErrors", body)
	if w.Code != http.StatusNoContent && w.Code != http.StatusOK {
		t.Fatalf("PUT importErrors: status %d, body %s", w.Code, w.Body.String())
	}
	if _, ok := store.errs["dags/x/dag.py"]; !ok {
		t.Fatalf("push did not persist the error")
	}

	// clear it
	w = authGet(srv, http.MethodDelete, "/api/v2/importErrors?filename=dags/x/dag.py", "")
	if w.Code != http.StatusNoContent && w.Code != http.StatusOK {
		t.Fatalf("DELETE importErrors: status %d, body %s", w.Code, w.Body.String())
	}
	if _, ok := store.errs["dags/x/dag.py"]; ok {
		t.Fatalf("clear did not remove the error")
	}
}

func TestImportErrorsNilStoreServesEmpty(t *testing.T) {
	srv := importErrorServer(nil)
	w := authGet(srv, http.MethodGet, "/api/v2/importErrors", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), `"total_entries":0`) {
		t.Fatalf("nil store should serve empty collection, got %s", w.Body.String())
	}
}
