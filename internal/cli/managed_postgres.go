package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/dexadata/dexaflow/internal/setup"
)

// Datastore backends for Lite's Postgres. The default is "auto": Docker Postgres
// when Docker is present (the realistic case, since the k3d executor needs Docker
// too), else a managed relocatable PostgreSQL under ~/.dexaflow on a Unix socket —
// so `dexaflow lite` runs on a Docker-free host with nothing to install. Either can
// be forced explicitly.
const (
	datastoreAuto    = "auto"
	datastoreDocker  = "docker"
	datastoreManaged = "managed"
)

// resolveDatastore maps the --postgres flag to a concrete backend: "auto" picks
// the Docker Postgres when Docker is available, else the managed relocatable PG
// (Docker-free); any explicit value is returned unchanged. It mirrors
// resolveExecutor so the datastore and executor track the same host capability.
func resolveDatastore(flag string, dockerOK bool) string {
	if flag != datastoreAuto {
		return flag
	}
	if dockerOK {
		return datastoreDocker
	}
	return datastoreManaged
}

// autoDatastore resolves an "auto" --postgres at run time (detecting Docker) and
// prints which backend it chose; a non-auto value is returned unchanged.
func autoDatastore(cmd *cobra.Command, flag string) string {
	if flag != datastoreAuto {
		return flag
	}
	present := dockerAvailable()
	mode := resolveDatastore(datastoreAuto, present && dockerResponsive())
	devPrintln(cmd.OutOrStdout(), datastoreNote(mode, present))
	return mode
}

// dockerPingFn probes whether the Docker daemon actually answers — not just that
// the `docker` binary is on PATH. A wedged Docker Desktop resolves on PATH but
// returns an error from its socket (the 500s seen on macOS, #403); the ping
// catches that. Package-level so tests can substitute it.
var dockerPingFn = func() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, "docker", "version").Run()
}

// dockerResponsive reports whether the Docker daemon answered the ping. The auto
// resolvers gate on this so a present-but-unresponsive daemon falls back to the
// Docker-free path instead of failing later on `docker compose up` (#403).
func dockerResponsive() bool { return dockerPingFn() == nil }

// datastoreNote is the user-facing line for the resolved Postgres backend. It
// distinguishes "no Docker at all" from "Docker present but not responding" so a
// wedged daemon gets an actionable message instead of the misleading "no Docker".
func datastoreNote(mode string, dockerPresent bool) string {
	switch {
	case mode == datastoreDocker:
		return "▸ Docker detected — using the Docker Postgres (postgres:16). Pass --postgres managed for a Docker-free Postgres."
	case dockerPresent:
		return "▸ Docker detected but not responding — using a managed Postgres (no Docker). Fix Docker, or pass --postgres docker to force it."
	default:
		return "▸ no Docker detected — using a managed Postgres (downloaded under ~/.dexaflow, no Docker). Install Docker for the postgres:16 container instead."
	}
}

// managedPGPaths returns the managed Postgres bin dir and data dir, both per-user
// under ~/.dexaflow (so root and an unprivileged user never share a cluster).
func managedPGPaths() (binDir, dataDir string, err error) {
	h, herr := os.UserHomeDir()
	if herr != nil {
		return "", "", fmt.Errorf("resolving home dir: %w", herr)
	}
	root := stateDirIn(h)
	return filepath.Join(root, "postgres", "bin"), filepath.Join(root, "pgdata"), nil
}

// startManagedPostgres downloads the relocatable PostgreSQL if needed, initdb's a
// per-user data dir on first run, and starts it on a Unix socket in that data dir
// (TCP disabled) — so dev-DB creation, migrations, and the server reach it only
// through the per-user socket and never collide with, or connect to, a foreign
// Postgres bound to localhost:5432. Idempotent: an already-running cluster is
// left as is. trust auth is safe here: the socket lives in the user's own
// ~/.dexaflow, the same single-user threat model as the Docker datastore.
//
// started reports whether THIS call started the cluster. Only then may the
// caller stop it: a cluster found running may belong to a running
// `dexaflow lite migrate-key`, whose session a stop would kill (ADR 0065
// section 3, "leave the cluster as found").
func startManagedPostgres(ctx context.Context, cmd *cobra.Command) (started bool, err error) {
	out := cmd.OutOrStdout()
	h, herr := os.UserHomeDir()
	if herr != nil {
		return false, fmt.Errorf("resolving home dir: %w", herr)
	}
	root := stateDirIn(h)
	binDir, eerr := setup.EnsurePostgres(ctx, setup.EnsureOpts{
		Home: root, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH, Libc: detectLibc(),
		Stat: os.Stat,
		Logf: func(format string, a ...any) { devPrintf(out, "  "+format+"\n", a...) },
	})
	if eerr != nil {
		return false, fmt.Errorf("installing managed Postgres: %w", eerr)
	}
	dataDir := filepath.Join(root, "pgdata")
	if serr := checkSocketPathLen(dataDir); serr != nil {
		return false, serr
	}
	// Pre-flight: the relocatable Postgres dynamically links a chain of system
	// libraries it does not bundle (ICU, Kerberos, zstd, lz4, libxml2, …). On a
	// minimal host (Alpine/musl, slim containers) some are absent and the server
	// dies with a cryptic loader error. Probe the `postgres` binary — it has the
	// widest dependency set, wider than initdb — and fail with an actionable
	// message before the confusing startup error.
	if verr := exec.CommandContext(ctx, filepath.Join(binDir, "postgres"), "--version").Run(); verr != nil { //nolint:gosec // managed binary + fixed arg
		return false, fmt.Errorf("the managed Postgres can't run on this host: it needs system libraries (ICU, Kerberos) that are missing here (common on Alpine/musl and slim containers): %w\n"+
			"  use `dexaflow lite --postgres docker` (recommended; works everywhere).\n"+
			"  installing the libs may help if the versions match (Debian/Ubuntu: `apt-get install libicu-dev libgssapi-krb5-2`; Alpine: `apk add icu-libs krb5-libs` — but the bundled build may need exact versions)", verr)
	}

	// Our cluster already accepting connections on its socket? leave it (idempotent
	// across lite runs). Probing the socket dir — not 127.0.0.1 — means a foreign
	// Postgres on 5432 is never mistaken for ours.
	if exec.CommandContext(ctx, filepath.Join(binDir, "pg_isready"), "-h", dataDir, "-p", "5432").Run() == nil { //nolint:gosec // managed binary + fixed args
		devPrintln(out, "▸ managed Postgres already running")
		return false, nil
	}
	if _, serr := os.Stat(filepath.Join(dataDir, "PG_VERSION")); os.IsNotExist(serr) {
		devPrintln(out, "▸ initializing managed Postgres data dir …")
		id := exec.CommandContext(ctx, filepath.Join(binDir, "initdb"), //nolint:gosec // managed binary + fixed args
			"-D", dataDir, "-U", "leoflow", "-A", "trust", "--encoding=UTF8", "--locale=C")
		// Force a valid, deterministic locale: a host (e.g. an SSH session from
		// macOS forwarding LC_CTYPE=UTF-8) may export a locale initdb rejects on
		// Linux ("invalid locale settings"). --locale=C pins the cluster; the
		// sanitized env keeps initdb itself from choking on the inherited one.
		id.Env = pgLocaleEnv(os.Environ())
		id.Stdout, id.Stderr = io.Discard, cmd.ErrOrStderr()
		if rerr := id.Run(); rerr != nil {
			return false, fmt.Errorf("initdb failed%s: %w", managedPGHint, rerr)
		}
		// Pin the socket-only listener in postgresql.conf rather than via pg_ctl -o:
		// the conf parser quotes paths natively, so a data dir with spaces works and
		// we avoid the fragile space-split of an -o option string.
		confPath := filepath.Join(dataDir, "postgresql.conf")
		cf, oerr := os.OpenFile(confPath, os.O_APPEND|os.O_WRONLY, 0o600) //nolint:gosec // path derived from our per-user data dir
		if oerr != nil {
			return false, fmt.Errorf("opening %s: %w", confPath, oerr)
		}
		if _, werr := cf.WriteString(managedPGConfLines(dataDir)); werr != nil {
			_ = cf.Close() //nolint:errcheck // already returning an error
			return false, fmt.Errorf("writing managed Postgres config: %w", werr)
		}
		if cerr := cf.Close(); cerr != nil {
			return false, fmt.Errorf("closing %s: %w", confPath, cerr)
		}
	}
	devPrintln(out, "▸ starting managed Postgres (no Docker) …")
	logFile := filepath.Join(root, "dev", "postgres.log")
	if mkErr := os.MkdirAll(filepath.Dir(logFile), 0o750); mkErr != nil {
		return false, fmt.Errorf("creating postgres log dir: %w", mkErr)
	}
	// Socket-only listener (no TCP) is configured in postgresql.conf at initdb
	// time, so pg_ctl needs no -o options carrying the data-dir path.
	start := exec.CommandContext(ctx, filepath.Join(binDir, "pg_ctl"), //nolint:gosec // managed binary + fixed args
		"-D", dataDir, "-l", logFile, "-w", "-t", "30", "start")
	start.Env = pgLocaleEnv(os.Environ())
	start.Stdout, start.Stderr = io.Discard, cmd.ErrOrStderr()
	if rerr := start.Run(); rerr != nil {
		return false, fmt.Errorf("starting managed Postgres (see %s)%s: %w", logFile, managedPGHint, rerr)
	}
	return true, nil
}

// stopManagedPostgres stops the managed cluster (best-effort), leaving its data
// dir intact for the next run.
func stopManagedPostgres(cmd *cobra.Command) {
	binDir, dataDir, err := managedPGPaths()
	if err != nil {
		return
	}
	if _, serr := os.Stat(filepath.Join(binDir, "pg_ctl")); serr != nil {
		return
	}
	devPrintln(cmd.OutOrStdout(), "▸ stopping managed Postgres …")
	c := exec.CommandContext(context.Background(), filepath.Join(binDir, "pg_ctl"), "-D", dataDir, "stop", "-m", "fast") //nolint:gosec // managed binary + fixed args
	c.Stdout, c.Stderr = io.Discard, io.Discard
	_ = c.Run() //nolint:errcheck // best-effort stop on shutdown
}

// managedCleanup returns stop when this run started the cluster, else a no-op.
func managedCleanup(started bool, stop func()) func() {
	if !started {
		return func() {}
	}
	return stop
}

// managedPGHint is appended to managed-Postgres startup failures: if the
// relocatable build can't run on this host (very old glibc, musl, locale), the
// Docker datastore is the escape hatch.
const managedPGHint = " — if the managed Postgres can't run on this host (very old glibc, musl, or a locale issue), use `dexaflow lite --postgres docker`"

// maxUnixSocketPath is a conservative cap on the managed Postgres socket path.
// The OS sun_path limit is ~104 (macOS) to ~108 (Linux); we guard below it so a
// deep HOME fails with a clear message instead of a cryptic Postgres error.
const maxUnixSocketPath = 100

// managedPGConfLines are the socket-only settings appended to postgresql.conf at
// initdb time: TCP off, the Unix socket in the per-user data dir, port 5432 (so
// the socket file is .s.PGSQL.5432). The data dir is single-quoted, so a path
// with spaces is parsed correctly (unlike a space-split pg_ctl -o string).
func managedPGConfLines(dataDir string) string {
	return "\n# Dexaflow Lite: socket-only datastore (no TCP), socket in the data dir.\n" +
		"listen_addresses = ''\n" +
		"unix_socket_directories = '" + dataDir + "'\n" +
		"port = 5432\n"
}

// checkSocketPathLen fails loud when the managed Postgres Unix socket path would
// exceed the OS limit (e.g. a deeply nested HOME / service account), pointing at
// the workaround instead of letting Postgres emit a cryptic socket error.
func checkSocketPathLen(dataDir string) error {
	sock := filepath.Join(dataDir, ".s.PGSQL.5432")
	if len(sock) > maxUnixSocketPath {
		return fmt.Errorf("managed Postgres socket path is too long (%d > %d chars): %s\n"+
			"  your home directory is too deeply nested for a Unix socket.\n"+
			"  use `dexaflow lite --postgres docker`, or set a shorter HOME",
			len(sock), maxUnixSocketPath, sock)
	}
	return nil
}

// pgLocaleEnv returns env with any inherited LANG/LC_* removed and a valid,
// deterministic LANG=C / LC_ALL=C forced, so initdb and pg_ctl never inherit a
// locale Linux rejects — e.g. an SSH session from macOS forwards LC_CTYPE=UTF-8,
// which is invalid on glibc and made initdb fail ("invalid locale settings").
func pgLocaleEnv(base []string) []string {
	out := make([]string, 0, len(base)+2)
	for _, kv := range base {
		if strings.HasPrefix(kv, "LANG=") || strings.HasPrefix(kv, "LC_") {
			continue
		}
		out = append(out, kv)
	}
	return append(out, "LANG=C", "LC_ALL=C")
}

// detectLibc reports the host libc ("glibc"/"musl" on linux, "" on darwin) so the
// matching relocatable PostgreSQL build is selected.
func detectLibc() string {
	return hostProbe().Libc
}

// dockerAvailable reports whether Docker is usable on this host, used to resolve
// the "auto" executor (k3d when present, else subprocess).
func dockerAvailable() bool {
	return hostProbe().Docker
}

// hostProbe runs the shared environment detection once per call site.
func hostProbe() setup.Report {
	return setup.Detect(setup.Probe{
		GOOS: runtime.GOOS, GOARCH: runtime.GOARCH,
		LookPath: exec.LookPath, Stat: os.Stat, Getwd: os.Getwd,
	})
}

// autoExecutor resolves an "auto" --executor at run time (detecting Docker) and
// prints which path it chose; a non-auto value is returned unchanged.
func autoExecutor(cmd *cobra.Command, flag string) string {
	if flag != "auto" {
		return flag
	}
	present := dockerAvailable()
	mode := resolveExecutor("auto", present && dockerResponsive())
	devPrintln(cmd.OutOrStdout(), executorNote(mode, present))
	return mode
}

// executorNote is the user-facing line for the resolved executor, naming a
// present-but-unresponsive Docker so the user fixes it (or forces a choice)
// instead of seeing the misleading "no Docker detected" (#403).
func executorNote(mode string, dockerPresent bool) string {
	switch {
	case mode != "subprocess":
		return "▸ Docker detected — using the k3d executor (pod-per-task). Pass --executor subprocess for a no-Docker run."
	case dockerPresent:
		return "⚠ Docker detected but not responding — running tasks via the subprocess executor (no isolation, dev-only). Fix Docker for pod-per-task (k3d), or pass --executor k8s."
	default:
		return "⚠ no Docker detected — running tasks via the subprocess executor (no isolation, dev-only). Install Docker for pod-per-task (k3d), or pass --executor k8s."
	}
}

// resolveExecutor maps the --executor flag to a concrete mode: "auto" picks k3d
// when Docker is available, else the subprocess executor (Docker-free); any
// explicit value is returned unchanged.
func resolveExecutor(flag string, dockerOK bool) string {
	if flag != "auto" {
		return flag
	}
	if dockerOK {
		return "k8s"
	}
	return "subprocess"
}
