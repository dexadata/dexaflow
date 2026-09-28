package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestResolveBindHost is the security guard: a no-auth fallback must never bind
// beyond loopback, while real auth honors the requested host.
func TestResolveBindHost(t *testing.T) {
	cases := []struct {
		host, adminHash, want string
	}{
		{"0.0.0.0", "", "127.0.0.1"},       // no auth -> forced loopback
		{"0.0.0.0", "$2a$hash", "0.0.0.0"}, // real auth -> honored
		{"", "", "127.0.0.1"},              // default
		{"192.168.1.5", "$2a$hash", "192.168.1.5"},
		{"", "$2a$hash", "127.0.0.1"}, // empty -> loopback even with auth
	}
	for _, c := range cases {
		if got := resolveBindHost(c.host, c.adminHash); got != c.want {
			t.Errorf("resolveBindHost(%q, auth=%t) = %q, want %q", c.host, c.adminHash != "", got, c.want)
		}
	}
}

// TestSharedServerEnvBindsRequestedHost ties it together at the env level.
func TestSharedServerEnvBindsRequestedHost(t *testing.T) {
	withAuth := strings.Join(sharedServerEnv(liteEnvParams{host: "0.0.0.0", port: 8088, adminHash: "$2a$hash", adminEmail: "admin@x", jwtSecret: ""}), "\n")
	if !strings.Contains(withAuth, "LEOFLOW_SERVER_HTTP_ADDR=0.0.0.0:8088") {
		t.Errorf("real auth should bind the requested host:\n%s", withAuth)
	}
	noAuth := strings.Join(sharedServerEnv(liteEnvParams{host: "0.0.0.0", port: 8088, adminHash: "", adminEmail: "", jwtSecret: ""}), "\n")
	if !strings.Contains(noAuth, "LEOFLOW_SERVER_HTTP_ADDR=127.0.0.1:8088") {
		t.Errorf("no-auth must be forced to loopback regardless of --host:\n%s", noAuth)
	}
}

func TestDisplayURL(t *testing.T) {
	if got := displayURL("127.0.0.1", 8088); got != "http://127.0.0.1:8088" {
		t.Errorf("loopback display = %q", got)
	}
	// A wildcard bind resolves to a reachable address (the LAN IP when one exists,
	// else the placeholder hint) — never the unreachable 0.0.0.0.
	if got := displayURL("0.0.0.0", 8088); !strings.HasPrefix(got, "http://") || !strings.HasSuffix(got, ":8088") || strings.Contains(got, "0.0.0.0") {
		t.Errorf("wildcard display = %q, want a reachable http://<ip|hint>:8088", got)
	}
}

// TestResolveComposeFile covers the precedence: explicit flag, then a CWD
// docker-compose.dev.yaml, then a materialized managed file.
func TestResolveComposeFile(t *testing.T) {
	// Explicit flag wins.
	if got, err := resolveComposeFile("/my/compose.yaml"); err != nil || got != "/my/compose.yaml" {
		t.Errorf("explicit = (%q,%v)", got, err)
	}

	// A CWD docker-compose.dev.yaml (source checkout) is used.
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "docker-compose.dev.yaml"), []byte("services: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(repo)
	if got, err := resolveComposeFile(""); err != nil || got != "docker-compose.dev.yaml" {
		t.Errorf("CWD compose = (%q,%v), want docker-compose.dev.yaml", got, err)
	}

	// No flag, no CWD file: materialize the managed one under HOME/.leoflow.
	home := t.TempDir()
	t.Setenv("HOME", home)
	clean := t.TempDir() // a dir with no compose file
	t.Chdir(clean)
	got, err := resolveComposeFile("")
	if err != nil {
		t.Fatalf("managed resolve: %v", err)
	}
	want := filepath.Join(home, ".leoflow", "docker-compose.yaml")
	if got != want {
		t.Errorf("managed compose = %q, want %q", got, want)
	}
	data, rerr := os.ReadFile(got)
	if rerr != nil || !strings.Contains(string(data), "postgres") || !strings.Contains(string(data), "redis") {
		t.Errorf("materialized compose should contain postgres+redis, err=%v", rerr)
	}
}
