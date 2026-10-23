package cli

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
)

// loginRegistry is a registry behind basic auth that accepts user:secret.
func loginRegistry(t *testing.T, user, secret string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u, p, ok := r.BasicAuth(); !ok || u != user || p != secret {
			w.Header().Set("WWW-Authenticate", `Basic realm="test"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return strings.TrimPrefix(srv.URL, "http://")
}

// storedLogin is what the registry client's keychain reads for host.
func storedLogin(t *testing.T, host string) authn.AuthConfig {
	t.Helper()
	reg, err := name.NewRegistry(host)
	if err != nil {
		t.Fatal(err)
	}
	a, err := authn.DefaultKeychain.Resolve(reg)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := a.Authorization()
	if err != nil {
		t.Fatal(err)
	}
	return *cfg
}

// A token on stdin is checked against the registry, then stored in the
// docker config, where the registry client reads it; no docker runs, and
// the token is never printed.
func TestRegistryLoginStoresAVerifiedToken(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DOCKER_CONFIG", dir)
	t.Setenv("PATH", t.TempDir())
	host := loginRegistry(t, "ci", "tok-abc-123")

	out, errOut, err := runWithStdin(t, "tok-abc-123\n", "registry", "login", host, "--username", "ci", "--password-stdin")
	if err != nil {
		t.Fatalf("registry login: %v\n%s", err, errOut)
	}
	if got := storedLogin(t, host); got.Username != "ci" || got.Password != "tok-abc-123" {
		t.Errorf("the keychain reads %q/%q, want the token", got.Username, got.Password)
	}
	if !strings.Contains(out, "signed in to "+host+" as ci") || !strings.Contains(out, filepath.Join(dir, "config.json")) {
		t.Errorf("output does not say who signed in and where the credential is:\n%s", out)
	}
	if strings.Contains(out+errOut, "tok-abc-123") {
		t.Errorf("the token was printed:\n%s%s", out, errOut)
	}
}

// A token the registry refuses is not stored.
func TestRegistryLoginStoresNothingTheRegistryRefuses(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DOCKER_CONFIG", dir)
	host := loginRegistry(t, "ci", "tok-abc-123")
	_, _, err := runWithStdin(t, "wrong-token\n", "registry", "login", host, "--username", "ci", "--password-stdin")
	if err == nil || !strings.Contains(err.Error(), "refused") || strings.Contains(err.Error(), "wrong-token") {
		t.Fatalf("err = %v, want a refusal that does not quote the token", err)
	}
	if _, serr := os.Stat(filepath.Join(dir, "config.json")); serr == nil {
		t.Error("a refused credential was stored")
	}
}

// fakeGH puts a gh on PATH whose session is octocat's, with token.
func fakeGH(t *testing.T, token string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell stubs are POSIX-only")
	}
	bin := t.TempDir()
	script := "#!/bin/sh\n" +
		"if [ \"$1 $2\" = \"auth token\" ]; then echo " + token + "; exit 0; fi\n" +
		"if [ \"$1\" = api ]; then echo octocat; exit 0; fi\n" +
		"exit 1\n"
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(script), 0o700); err != nil { //nolint:gosec // a test fixture that must be executable
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
}

// For GHCR with no token given, the gh CLI's session is exchanged: its
// token and its user, verified with GHCR and stored.
func TestRegistryLoginToGHCRUsesTheGHSession(t *testing.T) {
	t.Setenv("DOCKER_CONFIG", t.TempDir())
	fakeGH(t, "gho_session_token")
	var verified []string
	d := testDeps()
	d.verifyRegistryLogin = func(_ context.Context, host, user, secret string) error {
		verified = append(verified, host+" "+user+" "+secret)
		return nil
	}
	out, errOut, err := runWithStdinDeps(t, d, "", "registry", "login", "ghcr.io")
	if err != nil {
		t.Fatalf("registry login ghcr.io: %v\n%s", err, errOut)
	}
	if strings.Join(verified, ",") != "ghcr.io octocat gho_session_token" {
		t.Errorf("verified %q, want the gh session's user and token", verified)
	}
	if got := storedLogin(t, "ghcr.io"); got.Username != "octocat" || got.Password != "gho_session_token" {
		t.Errorf("the keychain reads %q/%q", got.Username, got.Password)
	}
	if !strings.Contains(out, "gh") || !strings.Contains(out, "write:packages") || strings.Contains(out, "gho_session_token") {
		t.Errorf("output does not name the gh session and the scope a push needs, or prints the token:\n%s", out)
	}
}

// Without a token and without a session to exchange, the login says what
// to pass, and runs nothing.
func TestRegistryLoginSaysWhatToPass(t *testing.T) {
	t.Setenv("DOCKER_CONFIG", t.TempDir())
	t.Setenv("PATH", t.TempDir())
	for _, tc := range []struct{ host, want string }{
		{"ghcr.io", "gh auth login"},
		{"docker.io", "personal access token"},
		{"harbor.example.com", "--password-stdin"},
	} {
		_, _, err := runWithStdin(t, "", "registry", "login", tc.host)
		if err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "--password-stdin") {
			t.Errorf("%s: err = %v, want it to name %q and --password-stdin", tc.host, err, tc.want)
		}
	}
}

// A token on stdin needs a user name, and must be one non-empty line.
func TestRegistryLoginRefusesABadToken(t *testing.T) {
	t.Setenv("DOCKER_CONFIG", t.TempDir())
	for _, tc := range []struct {
		stdin string
		args  []string
		want  string
	}{
		{"tok\n", []string{"--password-stdin"}, "--username"},
		{"\n", []string{"--username", "ci", "--password-stdin"}, "empty"},
		{"a\nb\n", []string{"--username", "ci", "--password-stdin"}, "one line"},
	} {
		args := append([]string{"registry", "login", "harbor.example.com"}, tc.args...)
		_, _, err := runWithStdin(t, tc.stdin, args...)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%q %v: err = %v, want it to name %q", tc.stdin, tc.args, err, tc.want)
		}
	}
}

// A gh that hangs is stopped when its time is up, even when a program it
// started still holds its output open.
func TestRunGHStopsAHangingGH(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell stubs are POSIX-only")
	}
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte("#!/bin/sh\nsleep 20 &\nexec sleep 20\n"), 0o700); err != nil { //nolint:gosec // a test fixture that must be executable
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":/bin:/usr/bin")
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := runGH(ctx, "auth", "token")
	if took := time.Since(start); took > 10*time.Second {
		t.Errorf("runGH took %v after the deadline, want gh stopped", took)
	}
	if err == nil {
		t.Error("runGH with a hanging gh succeeded")
	}
}

// A registry argument that is not a registry host, or a user name a docker
// config cannot hold, is refused before anything is checked or stored: an
// empty argument does not fall back to Docker Hub, and a user name with a
// colon would be stored as another user.
func TestRegistryLoginRefusesABadRegistryOrUser(t *testing.T) {
	var verified []string
	d := testDeps()
	d.verifyRegistryLogin = func(_ context.Context, host, user, _ string) error {
		verified = append(verified, host+" "+user)
		return nil
	}
	for _, tc := range []struct{ host, user, want string }{
		{"", "ci", "not a registry host"},
		{"ghcr.io/octocat", "ci", "not a registry host"},
		{"ci:pw@ghcr.io", "ci", "not a registry host"},
		{"bad\x1bhost", "ci", "not a registry host"},
		{"harbor.example.com", "ci:admin", "user name"},
		{"harbor.example.com", "ci\x1b[2J", "user name"},
	} {
		dir := t.TempDir()
		t.Setenv("DOCKER_CONFIG", dir)
		_, _, err := runWithStdinDeps(t, d, "tok-abc-123\n", "registry", "login", tc.host, "--username", tc.user, "--password-stdin")
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%q as %q: err = %v, want it to say %q", tc.host, tc.user, err, tc.want)
		}
		if _, serr := os.Stat(filepath.Join(dir, "config.json")); serr == nil {
			t.Errorf("%q as %q: a credential was stored", tc.host, tc.user)
		}
	}
	if len(verified) != 0 {
		t.Errorf("verified %q, want nothing sent to a registry", verified)
	}
}
