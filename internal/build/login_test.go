package build

import (
	"context"
	"encoding/base64"
	"encoding/json"
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

// basicAuthRegistry answers /v2/ with 200 for user:secret and 401 with a
// Basic challenge for anything else, as a registry behind basic auth does.
func basicLoginRegistry(t *testing.T, user, secret string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, p, ok := r.BasicAuth()
		if !ok || u != user || p != secret {
			w.Header().Set("WWW-Authenticate", `Basic realm="test"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return strings.TrimPrefix(srv.URL, "http://")
}

// tokenRegistry is a registry with a token service, as GHCR and Docker Hub
// have: /v2/ asks for a bearer token, the token endpoint grants one only for
// user:secret, and /v2/ accepts only that token.
func tokenRegistry(t *testing.T, user, secret string) string {
	t.Helper()
	mux := http.NewServeMux()
	var host string
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		if u, p, ok := r.BasicAuth(); !ok || u != user || p != secret {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"token": "granted"}) //nolint:errcheck // a test server
	})
	mux.HandleFunc("/v2/", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer granted" {
			w.Header().Set("WWW-Authenticate", `Bearer realm="http://`+host+`/token",service="test"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	host = strings.TrimPrefix(srv.URL, "http://")
	return host
}

// A credential the registry accepts verifies, through a Basic challenge and
// through a token exchange; one it refuses does not, and the error never
// carries the secret.
func TestVerifyLogin(t *testing.T) {
	for name, host := range map[string]string{
		"basic": basicLoginRegistry(t, "ci", "tok-123"),
		"token": tokenRegistry(t, "ci", "tok-123"),
	} {
		t.Run(name, func(t *testing.T) {
			if err := VerifyLogin(context.Background(), host, "ci", "tok-123"); err != nil {
				t.Errorf("VerifyLogin with the right credential = %v", err)
			}
			err := VerifyLogin(context.Background(), host, "ci", "wrong-secret-xyz")
			if err == nil || !strings.Contains(err.Error(), host) {
				t.Fatalf("VerifyLogin with a wrong credential = %v, want a refusal naming the registry", err)
			}
			if strings.Contains(err.Error(), "wrong-secret-xyz") {
				t.Errorf("the error carries the secret: %v", err)
			}
		})
	}
}

// emptyDockerConfig points DOCKER_CONFIG at a fresh directory, holding
// content as config.json when it is not empty.
func emptyDockerConfig(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("DOCKER_CONFIG", dir)
	if content != "" {
		if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// The stored credential is the one the registry client then reads: the
// keychain every registry request uses resolves it for the registry, and
// what was in the config before is kept.
func TestStoreLoginIsReadByTheKeychain(t *testing.T) {
	otherAuth := base64.StdEncoding.EncodeToString([]byte("other:secret"))
	dir := emptyDockerConfig(t, `{"auths":{"other.example.com":{"auth":"`+otherAuth+`"}},"proxies":{"default":{"httpProxy":"http://p:3128"}}}`)
	where, err := StoreLogin(context.Background(), "ghcr.io", "octocat", "gho_token")
	if err != nil {
		t.Fatal(err)
	}
	if where != filepath.Join(dir, "config.json") {
		t.Errorf("stored in %q, want the docker config", where)
	}
	reg, _ := name.NewRegistry("ghcr.io")
	auth, err := authn.DefaultKeychain.Resolve(reg)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := auth.Authorization()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Username != "octocat" || cfg.Password != "gho_token" {
		t.Errorf("the keychain reads %q/%q, want the stored credential", cfg.Username, cfg.Password)
	}
	data, err := os.ReadFile(filepath.Join(dir, "config.json")) //nolint:gosec // a path this test created
	if err != nil {
		t.Fatal(err)
	}
	for _, kept := range []string{"other.example.com", "httpProxy"} {
		if !strings.Contains(string(data), kept) {
			t.Errorf("config.json lost %s:\n%s", kept, data)
		}
	}
	info, err := os.Stat(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		t.Errorf("config.json mode = %v, want it readable by its owner only", info.Mode().Perm())
	}
}

// Docker Hub is stored under the key docker and the keychain use for it,
// whichever of its names the login was given.
func TestStoreLoginNormalizesDockerHub(t *testing.T) {
	for _, host := range []string{"docker.io", "index.docker.io", "registry-1.docker.io"} {
		dir := emptyDockerConfig(t, "")
		if _, err := StoreLogin(context.Background(), host, "me", "dckr_pat_x"); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(filepath.Join(dir, "config.json")) //nolint:gosec // a path this test created
		if err != nil {
			t.Fatal(err)
		}
		var cfg struct {
			Auths map[string]struct {
				Auth string `json:"auth"`
			} `json:"auths"`
		}
		if err := json.Unmarshal(data, &cfg); err != nil {
			t.Fatal(err)
		}
		want := base64.StdEncoding.EncodeToString([]byte("me:dckr_pat_x"))
		if got := cfg.Auths["https://index.docker.io/v1/"].Auth; got != want || len(cfg.Auths) != 1 {
			t.Errorf("%s: auths = %+v, want one entry under https://index.docker.io/v1/", host, cfg.Auths)
		}
	}
}

// fakeHelper puts docker-credential-<name> on PATH; it writes what `store`
// received on stdin to the returned file.
func fakeHelper(t *testing.T, helper string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell stubs are POSIX-only")
	}
	bin := t.TempDir()
	got := filepath.Join(t.TempDir(), "stored.json")
	script := "#!/bin/sh\n[ \"$1\" = store ] || exit 1\n/bin/cat > \"" + got + "\"\n"
	if err := os.WriteFile(filepath.Join(bin, "docker-credential-"+helper), []byte(script), 0o700); err != nil { //nolint:gosec // a test fixture that must be executable
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	return got
}

// With a credential helper configured, the secret goes to the helper on
// stdin, as docker does, and the file records only the registry. A
// credHelpers entry for the host wins over credsStore.
func TestStoreLoginUsesTheCredentialHelper(t *testing.T) {
	for name, cfg := range map[string]string{
		"credsStore":  `{"credsStore":"fake"}`,
		"credHelpers": `{"credsStore":"other","credHelpers":{"ghcr.io":"fake"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			dir := emptyDockerConfig(t, cfg)
			got := fakeHelper(t, "fake")
			if _, err := StoreLogin(context.Background(), "ghcr.io", "octocat", "gho_secret"); err != nil {
				t.Fatal(err)
			}
			var stored map[string]string
			data, err := os.ReadFile(got) //nolint:gosec // a path this test created
			if err != nil {
				t.Fatal(err)
			}
			if jerr := json.Unmarshal(data, &stored); jerr != nil {
				t.Fatal(jerr)
			}
			if stored["ServerURL"] != "ghcr.io" || stored["Username"] != "octocat" || stored["Secret"] != "gho_secret" {
				t.Errorf("the helper stored %v", stored)
			}
			file, err := os.ReadFile(filepath.Join(dir, "config.json")) //nolint:gosec // a path this test created
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(file), "gho_secret") || strings.Contains(string(file), base64.StdEncoding.EncodeToString([]byte("octocat:gho_secret"))) ||
				!strings.Contains(string(file), `"ghcr.io"`) {
				t.Errorf("config.json = %s, want the registry recorded and no secret", file)
			}
		})
	}
}

// A helper that is configured and missing fails the login: the secret is
// not written to the file instead.
func TestStoreLoginFailsWithoutTheConfiguredHelper(t *testing.T) {
	dir := emptyDockerConfig(t, `{"credsStore":"missing"}`)
	t.Setenv("PATH", t.TempDir())
	if _, err := StoreLogin(context.Background(), "ghcr.io", "octocat", "gho_secret"); err == nil || !strings.Contains(err.Error(), "docker-credential-missing") {
		t.Fatalf("err = %v, want the missing helper named", err)
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "config.json")); strings.Contains(string(data), "auth\"") { //nolint:gosec,errcheck // a path this test created
		t.Errorf("config.json = %s, want no credential written", data)
	}
}

// writeHelper puts docker-credential-<name> on PATH running script.
func writeHelper(t *testing.T, helper, script string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell stubs are POSIX-only")
	}
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "docker-credential-"+helper), []byte("#!/bin/sh\n"+script), 0o700); err != nil { //nolint:gosec // a test fixture that must be executable
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":/bin:/usr/bin")
}

// A helper that fails and echoes what it received never puts the secret in
// the error, which the CLI prints: neither as given nor as the JSON the
// helper read it from encodes it.
func TestStoreLoginHelperErrorNeverCarriesTheSecret(t *testing.T) {
	for _, tc := range []struct{ secret, echoed string }{
		{"gho_leaky_secret", "gho_leaky_secret"},
		{"pa<ss>&word", `pa\u003css\u003e\u0026word`},
	} {
		emptyDockerConfig(t, `{"credsStore":"echo"}`)
		writeHelper(t, "echo", "echo \"cannot store: $(cat)\" >&2\nexit 1\n")
		_, err := StoreLogin(context.Background(), "ghcr.io", "octocat", tc.secret)
		if err == nil || !strings.Contains(err.Error(), "docker-credential-echo") || !strings.Contains(err.Error(), "cannot store") {
			t.Fatalf("err = %v, want the helper and its message named", err)
		}
		if strings.Contains(err.Error(), tc.secret) || strings.Contains(err.Error(), tc.echoed) {
			t.Errorf("the error carries the secret: %v", err)
		}
	}
}

// A helper name from the docker config that is a path, or holds anything
// but a name's characters, is refused before anything runs: the config
// names a helper on PATH, never a program elsewhere.
func TestStoreLoginRefusesAHelperNameThatIsAPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell stubs are POSIX-only")
	}
	work := t.TempDir()
	t.Chdir(work)
	marker := filepath.Join(t.TempDir(), "ran")
	if err := os.Mkdir(filepath.Join(work, "docker-credential-"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "evil"), []byte("#!/bin/sh\ntouch "+marker+"\n"), 0o700); err != nil { //nolint:gosec // a test fixture that must be executable
		t.Fatal(err)
	}
	writeHelper(t, "ok", "exit 0\n")
	for _, helper := range []string{"/../evil", "../evil", "a b", ""} {
		cfg, _ := json.Marshal(map[string]any{"credHelpers": map[string]string{"ghcr.io": helper}, "credsStore": helper}) //nolint:errcheck // a map of strings
		dir := emptyDockerConfig(t, string(cfg))
		_, err := StoreLogin(context.Background(), "ghcr.io", "octocat", "gho_secret")
		if helper == "" {
			if err != nil {
				t.Errorf("an empty credsStore means no helper: %v", err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), "credential helper name") {
			t.Errorf("helper %q: err = %v, want the name refused", helper, err)
		}
		if data, _ := os.ReadFile(filepath.Join(dir, "config.json")); strings.Contains(string(data), "auth\"") { //nolint:gosec,errcheck // a path this test created
			t.Errorf("helper %q: config.json = %s, want no credential written", helper, data)
		}
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("a helper named by a path ran")
	}
}

// A helper that hangs is stopped when its time is up, even when a program
// it started still holds its output open: the login fails instead of
// waiting for that program.
func TestStoreLoginStopsAHangingHelper(t *testing.T) {
	dir := emptyDockerConfig(t, `{"credsStore":"hang"}`)
	writeHelper(t, "hang", "sleep 20 &\nexec sleep 20\n")
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := StoreLogin(ctx, "ghcr.io", "octocat", "gho_secret")
	if took := time.Since(start); took > 10*time.Second {
		t.Errorf("StoreLogin took %v after the deadline, want the helper stopped", took)
	}
	if err == nil {
		t.Fatal("StoreLogin with a hanging helper succeeded")
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "config.json")); strings.Contains(string(data), "ghcr.io") { //nolint:gosec,errcheck // a path this test created
		t.Errorf("config.json = %s, want nothing recorded for a failed store", data)
	}
}

// An empty or malformed registry is refused before any request: the
// registry client would read an empty one as Docker Hub.
func TestVerifyLoginRefusesABadRegistryHost(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, host := range []string{"", "ghcr.io/octocat", "https://ghcr.io", "bad\x1bhost"} {
		err := VerifyLogin(ctx, host, "ci", "tok-123")
		if err == nil || !strings.Contains(err.Error(), "not a registry host") {
			t.Errorf("VerifyLogin(%q) = %v, want the host refused", host, err)
		}
		if err != nil && strings.ContainsRune(err.Error(), '\x1b') {
			t.Errorf("VerifyLogin(%q) error quotes a control character as is: %q", host, err)
		}
	}
}

// A config.json that is a symlink is read, but the credential never goes
// through the link into the file it points at, which can be readable by
// others: the link is replaced by a file readable by its owner only.
func TestStoreLoginDoesNotWriteThroughASymlink(t *testing.T) {
	dir := emptyDockerConfig(t, "")
	target := filepath.Join(t.TempDir(), "shared.json")
	const before = `{"auths":{},"psFormat":"table"}`
	if err := os.WriteFile(target, []byte(before), 0o644); err != nil { //nolint:gosec // a world-readable file is the point
		t.Fatal(err)
	}
	if err := os.Chmod(target, 0o644); err != nil { //nolint:gosec // a world-readable file is the point
		t.Fatal(err)
	}
	path := filepath.Join(dir, "config.json")
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if _, err := StoreLogin(context.Background(), "ghcr.io", "octocat", "gho_secret"); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(target); string(data) != before { //nolint:gosec,errcheck // a path this test created
		t.Errorf("the symlink's target was written: %s", data)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		t.Errorf("config.json mode = %v, want a regular file readable by its owner only", info.Mode())
	}
	if data, _ := os.ReadFile(path); !strings.Contains(string(data), "psFormat") || !strings.Contains(string(data), "ghcr.io") { //nolint:gosec,errcheck // a path this test created
		t.Errorf("config.json = %s, want the linked config's fields and the login", data)
	}
}

// A registry that redirects the check to another host does not pass it, and
// the credential never reaches that host.
func TestVerifyLoginDoesNotFollowARedirectToAnotherHost(t *testing.T) {
	var leaked []string
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a := r.Header.Get("Authorization"); a != "" {
			leaked = append(leaked, a)
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(other.Close)
	reg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, _, ok := r.BasicAuth(); !ok {
			w.Header().Set("WWW-Authenticate", `Basic realm="test"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		http.Redirect(w, r, other.URL+"/v2/", http.StatusTemporaryRedirect)
	}))
	t.Cleanup(reg.Close)
	err := VerifyLogin(context.Background(), strings.TrimPrefix(reg.URL, "http://"), "ci", "tok-123")
	if err == nil {
		t.Error("VerifyLogin passed on a redirect to another host")
	}
	if len(leaked) != 0 {
		t.Errorf("the other host received %q", leaked)
	}
}
