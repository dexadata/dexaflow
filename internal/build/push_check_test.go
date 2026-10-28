package build

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/google/go-containerregistry/pkg/registry"
)

// uploadRegistry fakes the upload endpoint of a registry: it answers a push
// start with status, records each request, and requires Basic auth when
// needAuth is set.
type uploadRegistry struct {
	mu       sync.Mutex
	status   int
	needAuth bool
	calls    []string
}

func (u *uploadRegistry) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.needAuth && r.Header.Get("Authorization") == "" {
		w.Header().Set("WWW-Authenticate", `Basic realm="test"`)
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if r.URL.Path == "/v2/" {
		return
	}
	u.calls = append(u.calls, r.Method+" "+r.URL.Path)
	switch {
	case r.Method == http.MethodPost && u.status == http.StatusAccepted:
		w.Header().Set("Location", r.URL.Path+"upload-1")
		w.WriteHeader(http.StatusAccepted)
	case r.Method == http.MethodPost:
		w.WriteHeader(u.status)
		_, _ = w.Write([]byte(`{"errors":[{"code":"DENIED","message":"requested access to the resource is denied"}]}`))
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

// withDockerConfig points the keychain at a config with no credential, or
// with one for host.
func withDockerConfig(t *testing.T, host string) {
	t.Helper()
	dir := t.TempDir()
	cfg := `{"auths":{}}`
	if host != "" {
		cfg = `{"auths":{"` + host + `":{"auth":"` + base64.StdEncoding.EncodeToString([]byte("bot:secret")) + `"}}}`
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DOCKER_CONFIG", dir)
}

// A registry that accepts the start of an upload may be pushed to; the
// upload is canceled, and the check names the repository.
func TestCheckPushAcceptsAPushableRepository(t *testing.T) {
	withDockerConfig(t, "")
	fake := &uploadRegistry{status: http.StatusAccepted}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	host := strings.TrimPrefix(srv.URL, "http://")
	got, err := Registry{}.CheckPush(context.Background(), host+"/team/etl:v1")
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if got.Registry != host || got.Repository != host+"/team/etl" || !got.Anonymous {
		t.Errorf("access = %+v, want the registry, the repository and no credential", got)
	}
	want := []string{"POST /v2/team/etl/blobs/uploads/", "DELETE /v2/team/etl/blobs/uploads/upload-1"}
	if strings.Join(fake.calls, ",") != strings.Join(want, ",") {
		t.Errorf("registry calls = %q, want %q", fake.calls, want)
	}
}

// The in-memory registry of go-containerregistry takes a push: the check
// speaks the real protocol.
func TestCheckPushAgainstARealRegistry(t *testing.T) {
	withDockerConfig(t, "")
	srv := httptest.NewServer(registry.New())
	t.Cleanup(srv.Close)
	if _, err := (Registry{}).CheckPush(context.Background(), strings.TrimPrefix(srv.URL, "http://")+"/team/etl:v1"); err != nil {
		t.Fatalf("check against an in-memory registry: %v", err)
	}
}

// A refused upload is ErrPushDenied, whether or not a credential was sent,
// and the access says which.
func TestCheckPushReportsADenial(t *testing.T) {
	for _, tc := range []struct {
		name      string
		status    int
		needAuth  bool
		withCred  bool
		anonymous bool
	}{
		{"no credential, 401", http.StatusUnauthorized, true, false, true},
		{"a credential, 403", http.StatusForbidden, true, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := &uploadRegistry{status: tc.status, needAuth: tc.needAuth}
			srv := httptest.NewServer(fake)
			t.Cleanup(srv.Close)
			host := strings.TrimPrefix(srv.URL, "http://")
			if tc.withCred {
				withDockerConfig(t, host)
			} else {
				withDockerConfig(t, "")
			}
			got, err := Registry{}.CheckPush(context.Background(), host+"/team/etl:v1")
			if !errors.Is(err, ErrPushDenied) {
				t.Fatalf("err = %v, want ErrPushDenied", err)
			}
			if got.Anonymous != tc.anonymous {
				t.Errorf("anonymous = %v, want %v", got.Anonymous, tc.anonymous)
			}
		})
	}
}

// Any other answer says nothing about the right to push: it is an error,
// never ErrPushDenied.
func TestCheckPushDoesNotCallAnyOtherAnswerADenial(t *testing.T) {
	withDockerConfig(t, "")
	srv := httptest.NewServer(&uploadRegistry{status: http.StatusNotFound})
	t.Cleanup(srv.Close)
	_, err := Registry{}.CheckPush(context.Background(), strings.TrimPrefix(srv.URL, "http://")+"/team/etl:v1")
	if err == nil || errors.Is(err, ErrPushDenied) {
		t.Fatalf("err = %v, want an error that is not ErrPushDenied", err)
	}
	if _, err := (Registry{}).CheckPush(context.Background(), "UPPER/Case::bad"); err == nil || errors.Is(err, ErrPushDenied) {
		t.Errorf("a malformed reference = %v, want an error that is not ErrPushDenied", err)
	}
}
