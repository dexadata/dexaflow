package cli

import (
	"context"
	"encoding/hex"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestProjectNameStableAndDistinct: the project name is a pure function of the
// install path — identical on every call (so a reinstall at the same HOME
// reconnects to the same volume) and distinct per path (so two users never share
// or clobber a datastore).
func TestProjectNameStableAndDistinct(t *testing.T) {
	a1 := projectName("/home/alice/.leoflow")
	a2 := projectName("/home/alice/.leoflow")
	b := projectName("/home/bob/.leoflow")
	if a1 != a2 {
		t.Errorf("project name must be deterministic: %q != %q", a1, a2)
	}
	if a1 == b {
		t.Errorf("different install paths must yield different project names, both %q", a1)
	}
	if !strings.HasPrefix(a1, "leoflow-") || len(a1) != len("leoflow-")+12 {
		t.Errorf("project name %q must be leoflow-<12 hex>", a1)
	}
	validProjectChar := func(r rune) bool {
		return r == '-' || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
	}
	for _, r := range a1 {
		if !validProjectChar(r) {
			t.Errorf("project name %q must be a valid docker compose project (lowercase/digits/-)", a1)
		}
	}
}

// TestResolveDevDBPortPersistsAndReuses: the first call picks a port and persists
// it; later calls reuse the persisted port even if pick would choose another (the
// running container holds it) — so separate processes agree.
func TestResolveDevDBPortPersistsAndReuses(t *testing.T) {
	dir := t.TempDir()
	got, err := resolveDevDBPort(dir, func() int { return 5455 })
	if err != nil || got != 5455 {
		t.Fatalf("first resolve = (%d, %v), want (5455, nil)", got, err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "db-port")); strings.TrimSpace(string(b)) != "5455" {
		t.Errorf("port not persisted, db-port = %q", string(b))
	}
	again, err := resolveDevDBPort(dir, func() int { return 9999 })
	if err != nil || again != 5455 {
		t.Errorf("second resolve must reuse persisted port, got (%d, %v), want 5455", again, err)
	}
}

// TestDevDBPortDefault: with no persisted port (source checkout / explicit
// --compose), the DSNs fall back to the standard 5432.
func TestDevDBPortDefault(t *testing.T) {
	if p := devDBPort(t.TempDir()); p != defaultDevDBPort {
		t.Errorf("devDBPort with no db-port = %d, want %d", p, defaultDevDBPort)
	}
}

// TestLeoflowHomeUnderUserHome anchors leoflowHome() to $HOME/.leoflow. A
// regression would break the docker compose project name (datastore
// reconnection) and the uninstall target on every machine.
func TestLeoflowHomeUnderUserHome(t *testing.T) {
	got, err := leoflowHome()
	if err != nil {
		t.Fatalf("leoflowHome: %v", err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("UserHomeDir unavailable: %v", err)
	}
	want := filepath.Join(home, ".dexaflow")
	if got != want {
		t.Errorf("leoflowHome() = %q, want %q", got, want)
	}
}

// TestDevProjectNameStableAndShaped asserts devProjectName() returns the
// `leoflow-<12hex>` shape projectName() produces for the leoflowHome() output.
// The string is the docker-compose project name; drift here would orphan the
// Postgres volume of every existing install.
func TestDevProjectNameStableAndShaped(t *testing.T) {
	got := devProjectName()
	if !strings.HasPrefix(got, "leoflow-") {
		t.Errorf("devProjectName() = %q, must start with 'leoflow-'", got)
	}
	rest := strings.TrimPrefix(got, "leoflow-")
	if len(rest) != 12 {
		t.Errorf("devProjectName() suffix = %q, want 12 hex chars", rest)
	}
	if _, err := hex.DecodeString(rest); err != nil {
		t.Errorf("devProjectName() suffix %q is not hex: %v", rest, err)
	}
	// Stability: calling again returns the same string.
	if again := devProjectName(); again != got {
		t.Errorf("devProjectName() is not stable across calls: %q != %q", again, got)
	}
}

// TestComposeEnvCarriesProjectAndPort: composeEnv() must publish both the
// per-install COMPOSE_PROJECT_NAME (so docker compose targets this install's
// resources) and the LEOFLOW_DB_PORT the compose file interpolates. A missing
// either silently re-binds to the default port or clobbers another install.
func TestComposeEnvCarriesProjectAndPort(t *testing.T) {
	env := composeEnv()
	var sawProject, sawPort bool
	for _, kv := range env {
		switch {
		case strings.HasPrefix(kv, "COMPOSE_PROJECT_NAME=leoflow-"):
			sawProject = true
		case strings.HasPrefix(kv, "LEOFLOW_DB_PORT="):
			sawPort = true
		}
	}
	if !sawProject {
		t.Error("composeEnv() missing COMPOSE_PROJECT_NAME=leoflow-*")
	}
	if !sawPort {
		t.Error("composeEnv() missing LEOFLOW_DB_PORT=<n>")
	}
}

// TestFirstFreePortSkipsBusy: a bound port is skipped and the next free one is
// returned.
func TestFirstFreePortSkipsBusy(t *testing.T) {
	var lc net.ListenConfig
	ln, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("cannot listen on loopback: %v", err)
	}
	defer func() { _ = ln.Close() }()
	busy := ln.Addr().(*net.TCPAddr).Port
	got := firstFreePort(busy)
	if got <= busy {
		t.Errorf("firstFreePort(%d) = %d, must skip the bound port", busy, got)
	}
	if !portFree(got) {
		t.Errorf("firstFreePort returned a port that is not free: %d", got)
	}
}
