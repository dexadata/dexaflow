package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/xcom"
)

// TestRejectEncodedPathSeparators pins that an encoded slash or backslash never
// reaches a route: a proxy in front of the server matches the raw path, so
// /auth%2Ftoken must not be served as /auth/token.
func TestRejectEncodedPathSeparators(t *testing.T) {
	srv := testServer(&fakeAuthn{token: "tok", user: &auth.User{ID: "u", TenantID: "default", Roles: []string{"admin"}}})
	login := `{"username":"a","password":"b"}`
	for _, tc := range []struct {
		method, path, body string
		want               int
	}{
		{http.MethodPost, "/auth/token", login, http.StatusOK},
		{http.MethodPost, "/auth%2Ftoken", login, http.StatusBadRequest},
		{http.MethodPost, "/auth%2ftoken", login, http.StatusBadRequest},
		{http.MethodPost, "/auth%5Ctoken", login, http.StatusBadRequest},
		{http.MethodPost, "/auth%5ctoken", login, http.StatusBadRequest},
		{http.MethodPost, "/api%2Fv2%2Fauth%2Fsession", "{}", http.StatusBadRequest},
		{http.MethodPut, "/api/v2%2Fservice/tenants/acme", "{}", http.StatusBadRequest},
		{http.MethodGet, "/api%2Fv2%2Fdags", "", http.StatusBadRequest},
		{http.MethodGet, "/api/v2/auth%2Flogin", "", http.StatusBadRequest},
		{http.MethodGet, "/api/v2/dags%2Fx/dagRuns/r/taskInstances/t/xcomEntries/k", "", http.StatusBadRequest},
		{http.MethodGet, "/api/v2/dags/x/dagRuns/r%2F/taskInstances/t/xcomEntries/k", "", http.StatusBadRequest},
		{http.MethodGet, "/api/v2/auth/login", "", http.StatusOK},
	} {
		rec := do(srv, tc.method, tc.path, tc.body)
		if rec.Code != tc.want {
			t.Errorf("%s %s = %d, want %d (body %s)", tc.method, tc.path, rec.Code, tc.want, rec.Body.String())
		}
		if tc.want == http.StatusBadRequest && !strings.Contains(rec.Body.String(), "encoded slash") {
			t.Errorf("%s %s: body %q does not explain the refusal", tc.method, tc.path, rec.Body.String())
		}
	}
}

func TestHasEncodedPathSeparatorXComKey(t *testing.T) {
	for _, tc := range []struct {
		path string
		want bool
	}{
		// The XCom key is percent-encoded by the UI and may hold a slash.
		{"/api/v2/dags/d/dagRuns/r/taskInstances/t/xcomEntries/a%2Fb", false},
		{"/api/v2/dags/d/dagRuns/r/taskInstances/t/xcomEntries/a%5Cb", false},
		// Everything before the key is still checked.
		{"/api/v2/dags/d%2Fe/dagRuns/r/taskInstances/t/xcomEntries/a%2Fb", true},
		{"/api/v2/dags/d/dagRuns/r/taskInstances/t%2F/xcomEntries/k", true},
		// The exception needs the real route shape, not just the segment name.
		{"/api/v2/xcomEntries/a%2Fb", true},
		{"/api/v2/dags/d/xcomEntries/a%2Fb", true},
		{"/auth/xcomEntries/%2F", true},
		{"/api/v2/dags/d/dagRuns/r/taskInstances/t/logs/1", false},
	} {
		if got := hasEncodedPathSeparator(tc.path); got != tc.want {
			t.Errorf("hasEncodedPathSeparator(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

// TestXComKeyWithEncodedSlashStillServed: the UI percent-encodes the XCom key,
// so a key holding a slash must still reach its value through the full server.
func TestXComKeyWithEncodedSlashStillServed(t *testing.T) {
	reader := &fakeXComReader{entry: xcom.Entry{Value: []byte(`42`), CreatedAt: time.Now().UTC()}}
	rec := authGet(xcomServer(reader), http.MethodGet,
		"/api/v2/dags/etl/dagRuns/run-1/taskInstances/extract/xcomEntries/out%2Fpart-1", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("xcomEntries/{key with slash} = %d (%s)", rec.Code, rec.Body.String())
	}
	var dto xComEntryDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &dto); err != nil {
		t.Fatal(err)
	}
	if dto.Key != "out/part-1" {
		t.Errorf("key = %q, want out/part-1", dto.Key)
	}
}
