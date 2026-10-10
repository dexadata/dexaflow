package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// defaultDevDBPort is the host port Lite's Docker Postgres prefers; if it is busy
// (a foreign Postgres, another install) Lite picks the next free one.
const defaultDevDBPort = 5432

// leoflowHome returns the per-user Dexaflow home (~/.dexaflow) — the install
// identity that scopes the datastore to this user.
func leoflowHome() (string, error) {
	h, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolving home dir: %w", err)
	}
	return stateDirIn(h), nil
}

// projectName derives a stable, per-install docker compose project name from the
// Dexaflow home path: leoflow-<12 hex>. It is DERIVED (a pure function of the
// path), never stored — so it is recomputed identically on every run and after a
// reinstall at the same HOME, which is what lets `dexaflow lite` reconnect to the
// same Docker volume (the datastore's identity) instead of orphaning it. Two
// users (different HOME) get different names, so their datastores never share or
// clobber, and `uninstall` targets exactly this install's container and volume.
func projectName(leoflowRoot string) string {
	sum := sha256.Sum256([]byte(leoflowRoot))
	return "leoflow-" + hex.EncodeToString(sum[:])[:12]
}

// devProjectName is projectName for the current user's Dexaflow home.
func devProjectName() string {
	root, err := leoflowHome()
	if err != nil {
		return "leoflow"
	}
	return projectName(root)
}

// composeEnv returns the environment for `docker compose` (up and down) so it runs
// under this install's per-user project name — isolating its container and volume
// from other users/installs, and letting uninstall target exactly this install's
// datastore — and publishes Postgres on the resolved host port (the compose
// interpolates LEOFLOW_DB_PORT).
func composeEnv() []string {
	return append(os.Environ(),
		"COMPOSE_PROJECT_NAME="+devProjectName(),
		fmt.Sprintf("LEOFLOW_DB_PORT=%d", devDBPort(liteDevDir())),
	)
}

// portFree reports whether the loopback TCP port can be bound right now.
func portFree(port int) bool {
	var lc net.ListenConfig
	ln, err := lc.Listen(context.Background(), "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return false
	}
	_ = ln.Close() //nolint:errcheck // probe only; the port is free if it bound
	return true
}

// firstFreePort returns the first bindable loopback port at or above start,
// scanning a bounded window so a pathological host fails fast rather than hanging.
func firstFreePort(start int) int {
	for p := start; p < start+100; p++ {
		if portFree(p) {
			return p
		}
	}
	return start
}

// resolveDevDBPort returns the host port for this install's Docker Postgres,
// persisting it under devDir so every entry point (the lite runner,
// reset-password, db reset — separate processes) agrees on it. A persisted port
// is reused unconditionally (our own container holds it; it is not "free"); only
// on first run is a free port picked via pick. The port is just the host mapping
// — the datastore's identity is the project/volume (see projectName) — so a
// reinstall that re-picks a port still reconnects to the same volume.
func resolveDevDBPort(devDir string, pick func() int) (int, error) {
	pf := filepath.Join(devDir, "db-port")
	if p, ok := readPort(pf); ok {
		return p, nil
	}
	p := pick()
	if err := os.MkdirAll(devDir, 0o750); err != nil {
		return 0, fmt.Errorf("creating dev state dir %s: %w", devDir, err)
	}
	if err := os.WriteFile(pf, []byte(strconv.Itoa(p)), 0o600); err != nil {
		return 0, fmt.Errorf("persisting db port to %s: %w", pf, err)
	}
	return p, nil
}

// devDBPort reads the persisted host port for this install's Docker Postgres,
// defaulting to defaultDevDBPort when none is recorded (a source checkout or an
// explicit --compose, where the port is whatever the compose file maps).
func devDBPort(devDir string) int {
	if p, ok := readPort(filepath.Join(devDir, "db-port")); ok {
		return p
	}
	return defaultDevDBPort
}

// readPort reads a positive integer port from a file, if present and valid.
func readPort(path string) (int, bool) {
	b, err := os.ReadFile(path) //nolint:gosec // path derived from the per-user Dexaflow home
	if err != nil {
		return 0, false
	}
	p, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || p <= 0 {
		return 0, false
	}
	return p, true
}
