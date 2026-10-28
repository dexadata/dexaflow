package cli

import (
	"context"
	"net"
	"strconv"
	"strings"
	"testing"
)

// TestPreflightDevPorts: a busy HTTP port is reported clearly (naming the port),
// while free ports pass. This turns "bind: address already in use" deep in the
// server into an actionable message before anything starts.
func TestPreflightDevPorts(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	// Grab a free port, then hold it so the preflight sees it as busy.
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	busy := ln.Addr().(*net.TCPAddr).Port

	if err := preflightDevPorts(ctx, "127.0.0.1", busy); err == nil {
		t.Fatal("expected an error for a busy HTTP port")
	} else if !strings.Contains(err.Error(), strconv.Itoa(busy)) || !strings.Contains(err.Error(), "--port") {
		t.Errorf("error should name the busy port and suggest --port, got: %v", err)
	}

	// A free port range passes (use a high base unlikely to collide in CI).
	if err := preflightDevPorts(ctx, "127.0.0.1", 18088); err != nil {
		t.Errorf("free ports should pass, got: %v", err)
	}
}

// TestComposeUpError translates the cryptic Docker port-allocation failure into a
// clear "another Postgres is on 5432, use --postgres managed" message, while
// keeping the generic hint for other failures.
//
// The port message used to offer `--no-up` "to point at your own
// (LEOFLOW_DATABASE_URL)", which Lite never honored: it sets the server's
// database URL itself (#1185). Lite owns its database, and the message says so.
func TestComposeUpError(t *testing.T) {
	t.Parallel()
	base := errString("exit status 1")
	portErr := composeUpError(base, "Error response from daemon: ... Bind for 0.0.0.0:5432 failed: port is already allocated")
	for _, want := range []string{"5432", "--postgres managed", "Lite owns its database"} {
		if !strings.Contains(portErr.Error(), want) {
			t.Errorf("port-allocation failure should contain %q, got: %v", want, portErr)
		}
	}
	for _, promise := range []string{"DATABASE_URL", "--no-up", "your own"} {
		if strings.Contains(portErr.Error(), promise) {
			t.Errorf("port-allocation failure must not offer %q, which Lite does not support, got: %v", promise, portErr)
		}
	}
	noCompose := composeUpError(base, "docker: 'compose' is not a docker command.")
	if !strings.Contains(noCompose.Error(), "Compose v2") || !strings.Contains(noCompose.Error(), "--postgres managed") {
		t.Errorf("missing-compose-plugin should point at the Compose v2 plugin and --postgres managed, got: %v", noCompose)
	}
	other := composeUpError(base, "Cannot connect to the Docker daemon")
	if !strings.Contains(other.Error(), "is Docker running") {
		t.Errorf("non-port failure should keep the generic hint, got: %v", other)
	}
}

type errString string

func (e errString) Error() string { return string(e) }
