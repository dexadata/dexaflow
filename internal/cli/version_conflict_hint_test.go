package cli

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const conflictBody = `{"title":"conflict","status":409,"detail":"inserting version: conflict"}`

func TestVersionConflictHintNamesTheWayOut(t *testing.T) {
	t.Parallel()
	base := apiStatusError(http.StatusConflict, []byte(conflictBody))
	cases := []struct {
		origin labelOrigin
		want   []string
	}{
		{labelExplicit, []string{`"v1"`, "--dag-version", "omit it"}},
		{labelDefault, []string{`"v1"`, "unchanged", "new digest", "add them to include_paths"}},
		{labelUnknown, []string{`"v1"`, "--dag-version", "omit it", "new digest"}},
	}
	for _, c := range cases {
		err := versionConflictHint(base, "v1", c.origin)
		if !errors.Is(err, base) {
			t.Errorf("origin %v: the hint must wrap the server error", c.origin)
		}
		msg := err.Error()
		if !strings.Contains(msg, "server returned 409") || !strings.Contains(msg, "hint:") {
			t.Errorf("origin %v: %q, want the 409 and a hint", c.origin, msg)
		}
		for _, w := range c.want {
			if !strings.Contains(msg, w) {
				t.Errorf("origin %v: hint %q does not say %q", c.origin, msg, w)
			}
		}
	}
}

func TestVersionConflictHintStaysOutOfOtherErrors(t *testing.T) {
	t.Parallel()
	for _, status := range []int{http.StatusBadRequest, http.StatusForbidden, http.StatusInternalServerError} {
		err := versionConflictHint(apiStatusError(status, []byte("x")), "v1", labelDefault)
		if strings.Contains(err.Error(), "hint:") {
			t.Errorf("status %d got a version hint: %q", status, err)
		}
	}
	if versionConflictHint(nil, "v1", labelDefault) != nil {
		t.Error("a nil error must stay nil")
	}
}

func conflictServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = io.WriteString(w, conflictBody)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestPushCommandExplainsA409(t *testing.T) {
	srv := conflictServer(t)
	f := filepath.Join(t.TempDir(), "dag.json")
	if err := os.WriteFile(f, []byte(pushSpec), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, err := run(t, "push", f, "--server", srv.URL, "--token", "x")
	if err == nil || !strings.Contains(err.Error(), "hint:") || !strings.Contains(err.Error(), `"v1"`) {
		t.Fatalf("push on 409 = %v, want a hint naming version v1", err)
	}
}

func TestRegisterDeployedDAGExplainsA409(t *testing.T) {
	t.Parallel()
	srv := conflictServer(t)
	out := filepath.Join(t.TempDir(), "dag.json")
	if err := os.WriteFile(out, []byte(pushSpec), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := registerDeployedDAG(context.Background(), io.Discard, registerRequest{serverURL: srv.URL, token: "tok", output: out, digestRef: "r@sha256:x", version: "v1", origin: labelExplicit})
	if err == nil || !strings.Contains(err.Error(), "pass a new --dag-version or omit it") {
		t.Fatalf("deploy register on 409 = %v, want the explicit-label hint", err)
	}
}
