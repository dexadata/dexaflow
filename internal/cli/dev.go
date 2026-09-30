package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5" // registers the "pgx5" migrate scheme
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5"
	"github.com/spf13/cobra"

	leoflow "github.com/neochaotic/leoflow"
	"github.com/neochaotic/leoflow/internal/auth"
	"github.com/neochaotic/leoflow/internal/config"
	"github.com/neochaotic/leoflow/internal/domain"
	"github.com/neochaotic/leoflow/internal/setup"
	"github.com/neochaotic/leoflow/internal/version"
	"github.com/neochaotic/leoflow/migrations"
)

// devEnv is the fixed local-development environment label and its defaults. The
// subprocess executor runs user code unsandboxed, so `leoflow dev` is dev-only
// and shouts that fact in the banner and the UI navbar (ADR 0023).
const (
	devInstanceName = "Leoflow Lite"
	// devDatabaseURL targets a DEDICATED database, isolated from the product's
	// "leoflow" db so the dev experience never mixes data with product development
	// (no split brain). devMaintenanceURL is used only to CREATE it on first run.
	devDBName         = "leoflow_dev"
	devDatabaseURL    = "postgres://leoflow:leoflow@localhost:5432/leoflow_dev?sslmode=disable"
	devMaintenanceURL = "postgres://leoflow:leoflow@localhost:5432/postgres?sslmode=disable"
	// devMigrateURL is the same dev database via golang-migrate's pgx5 scheme, used
	// to apply the embedded migrations (no source tree / migrate CLI needed).
	devMigrateURL = "pgx5://leoflow:leoflow@localhost:5432/leoflow_dev?sslmode=disable"
	// taskSDKVersion matches the task image (runtime/Dockerfile); the dev venv
	// installs it so dag.py's `from airflow.sdk import ...` resolves.
	taskSDKVersion = "apache-airflow-task-sdk==1.3.1"
	// devJWTSecret is the legacy/fallback Lite JWT signing secret used only when a
	// pre-#121 install has no jwt_secret in its config.yaml. Modern setups write a
	// random per-install secret (rotated on every fresh install), so tokens from a
	// prior install fail verification and the SPA shows the login screen.
	devJWTSecret = "dev-insecure-jwt-secret-change-me"
	devSecretKey = "dev-insecure-secret-key-32bytes!"
	// liteTokenTTLSeconds is the lite session lifetime: 30 days. Lite is a local
	// single-user tool, so the server's 1-hour default just means surprise
	// re-logins mid-session.
	liteTokenTTLSeconds = 30 * 24 * 60 * 60
	// liteLoginRateLimit is the per-minute failed-login cap for Lite — generous,
	// because locking out the single local user is pure friction, not security.
	liteLoginRateLimit = 30
	devAdminUser       = "admin@leoflow.local"
	devPollInterval    = 750 * time.Millisecond
	devReadyTimeout    = 30 * time.Second
	// Dev uses ports distinct from the demo/production defaults (8080/9090/9091)
	// so a `leoflow dev` and a demo control plane can run side by side without
	// colliding. --port overrides the HTTP port; the gRPC and metrics ports derive
	// from it (devGRPCPort/devMetricsPort) so multiple Lite instances can coexist.
	devDefaultPort = 8088
	// The gRPC and metrics ports are offset from the HTTP --port so that distinct
	// --port values yield distinct gRPC/metrics ports (letting two Lite instances
	// run on one host). The offsets preserve the historical defaults: the default
	// HTTP port 8088 maps to gRPC 9099 and metrics 9098.
	devGRPCPortOffset    = 1011
	devMetricsPortOffset = 1010
	// Cluster-mode (default) runs real pod-per-task on a dedicated k3d cluster,
	// fully isolated from any product/demo cluster. Pods dial the host control
	// plane's gRPC; host.docker.internal resolves to the host on Docker Desktop.
	devClusterName = "leoflow-dev"
	devNamespace   = "leoflow"
	devPyVersion   = "3.11"
	devBaseImage   = "leoflow-base:py3.11"
)

// devGRPCPort derives the gRPC port from the HTTP --port; see devGRPCPortOffset.
func devGRPCPort(httpPort int) int { return httpPort + devGRPCPortOffset }

// devMetricsPort derives the metrics port from the HTTP --port.
func devMetricsPort(httpPort int) int { return httpPort + devMetricsPortOffset }

// devGRPCBindAddr is the gRPC listen address for the given HTTP --port.
func devGRPCBindAddr(httpPort int) string { return fmt.Sprintf(":%d", devGRPCPort(httpPort)) }

// devMetricsBindAddr is the metrics listen address for the given HTTP --port.
func devMetricsBindAddr(httpPort int) string { return fmt.Sprintf(":%d", devMetricsPort(httpPort)) }

// devHostGRPCAddr is the address task pods dial back for gRPC (cluster mode),
// derived from the HTTP --port; host.docker.internal resolves to the host.
func devHostGRPCAddr(httpPort int) string {
	return fmt.Sprintf("host.docker.internal:%d", devGRPCPort(httpPort))
}

const (
	ansiReset = "\x1b[0m"
	ansiLite  = "\x1b[100;97m" // white text on a gray background
)

// devOptions holds the resolved flags for a dev run.
type devOptions struct {
	// fresh drops the Lite dev database before provisioning, so the session
	// starts with nothing registered (#1104).
	fresh       bool
	image       string
	executor    string
	host        string
	port        int
	composeFile string
	runtimeSrc  string
	serverBin   string
	agentBin    string
	noUp        bool
	postgres    string // "auto" (default), "docker", or "managed" (relocatable PG, no Docker)
	// Resolved from ~/.leoflow/config.yaml (written by `leoflow setup`), not flags.
	adminHash  string
	adminEmail string
	// jwtSecret is the per-install Lite JWT signing secret loaded from
	// ~/.leoflow/config.yaml; empty on a legacy install (resolveLiteJWTSecret
	// then falls back to devJWTSecret with a one-shot warning).
	jwtSecret string
	// secretKey is the per-install connection-encryption key from
	// ~/.leoflow/config.yaml (#486).
	secretKey string
	// secretKeyPrevious is a decrypt-only predecessor. Nothing writes it: it is
	// a hand-set escape hatch for an install whose key was changed by hand and
	// still has rows under the old one.
	secretKeyPrevious string
}

// prepareWorkspace resolves the workspace, scaffolds a starter subdir when it
// is empty, and validates every yaml-bearing project's config. Extracted from
// runDev to keep its cyclomatic complexity under the gocyclo limit.
func prepareWorkspace(cmd *cobra.Command, out io.Writer, dir string) (*WorkspaceSpec, error) {
	ws, err := ResolveWorkspace(dir)
	if err != nil {
		return nil, err
	}
	// Empty workspace → scaffold a starter as a subdir (multi-DAG model). The
	// older single-DAG layout (workspace itself IS the project) is still
	// auto-detected by ResolveWorkspace via the root-back-compat path, so
	// existing setups keep working without migration.
	if len(ws.Projects) == 0 {
		starterDir := filepath.Join(ws.Path, "hello")
		dagID, serr := scaffoldProject(starterDir)
		if serr != nil {
			return nil, serr
		}
		devPrintf(out, "▸ no DAGs yet — scaffolded a starter project %q in %s (edit it in the web editor)\n", dagID, starterDir)
		ws, err = ResolveWorkspace(dir)
		if err != nil {
			return nil, err
		}
	}
	// Validate every project's config so an invalid yaml fails the boot, not the
	// first reload. Yaml-less projects (auto-defaults) skip validation; their
	// synthesized config is already schema-clean.
	for _, p := range ws.Projects {
		if !p.HasYAML {
			continue
		}
		if verr := p.Config.Validate(); verr != nil {
			return nil, fmt.Errorf("invalid %s: %w", p.ConfigPath, verr)
		}
	}
	return ws, nil
}

// resolveLiteProject picks the workspace dir for `leoflow lite`. With an
// explicit path argument it uses that — must exist as a directory; the path
// can be either a single-DAG project (back-compat: root holds leoflow.yaml +
// dag.py) or a multi-DAG workspace (subdirs each with their own pair). With
// no argument it uses the configured workspace (the directory `leoflow setup`
// chose). Scaffolding of an empty workspace is the caller's responsibility —
// it now creates a subdir (`<workspace>/hello/`), not a root project.
func resolveLiteProject(cmd *cobra.Command, args []string) (string, error) {
	if len(args) == 1 {
		p := args[0]
		// An explicit argument must be an existing directory. Without this check a
		// typo like `leoflow lite uninstall` was swallowed as a project path and
		// failed later with a cryptic "open uninstall/leoflow.yaml". Fail clearly.
		info, err := os.Stat(p)
		if err != nil || !info.IsDir() {
			return "", fmt.Errorf("workspace path %q does not exist or is not a directory.\n"+
				"  - run `leoflow lite` with no argument to use your workspace (%s)\n"+
				"  - run `leoflow init %s` to create a project there\n"+
				"  - for other actions see `leoflow --help` (e.g. `leoflow uninstall`)",
				p, defaultWorkspace(cmd), p)
		}
		return p, nil
	}
	dir := defaultWorkspace(cmd)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", fmt.Errorf("creating workspace %s: %w", dir, err)
	}
	return dir, nil
}

// defaultWorkspace returns the workspace from config (set by `leoflow setup`),
// falling back to ~/leoflow.
func defaultWorkspace(cmd *cobra.Command) string {
	if c, err := config.Load(configFilePath(cmd), nil); err == nil && c.Workspace != "" {
		return c.Workspace
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, "leoflow")
	}
	return "."
}

// devURL is the dev UI/API base for the given HTTP port. It is always localhost
// because the control plane is reachable on loopback regardless of bind address
// (used for the in-process readiness check and token push).
func devURL(port int) string { return fmt.Sprintf("http://localhost:%d", port) }

// displayURL is the URL to show the user, reflecting the bind host. For a
// wildcard bind it resolves the machine's own LAN IP so the printed URL is
// directly reachable from another machine; if detection fails it falls back to a
// placeholder hint.
func displayURL(host string, port int) string {
	switch host {
	case "", "0.0.0.0", "::", "[::]":
		if ip := machineIP(); ip != "" {
			return fmt.Sprintf("http://%s:%d", ip, port)
		}
		return fmt.Sprintf("http://<this-machine-ip>:%d", port)
	default:
		return fmt.Sprintf("http://%s:%d", host, port)
	}
}

// machineIP returns this machine's primary LAN IPv4. It first asks the OS which
// local address would route to a default gateway (a UDP "connect" sets up the
// route without sending any packet), which picks the real LAN interface over
// docker/virtual bridges; it falls back to the first non-loopback IPv4. Empty
// when there is no usable address.
func machineIP() string {
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if c, err := (&net.Dialer{}).DialContext(ctx, "udp", "8.8.8.8:80"); err == nil {
		defer func() { _ = c.Close() }() //nolint:errcheck // no packet sent; close is best-effort
		if a, ok := c.LocalAddr().(*net.UDPAddr); ok && a.IP != nil && !a.IP.IsLoopback() {
			return a.IP.String()
		}
	}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return ""
	}
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok && !n.IP.IsLoopback() {
			if v4 := n.IP.To4(); v4 != nil {
				return v4.String()
			}
		}
	}
	return ""
}

// isLoopbackHost reports whether host keeps the UI reachable only from the
// machine itself (the safe default).
func isLoopbackHost(host string) bool {
	return host == "" || host == "127.0.0.1" || host == "localhost" || host == "::1"
}

// warnIfExposed alerts when the user asked to bind beyond loopback: a clear
// security warning with real auth, or a notice that no-auth was forced back to
// loopback (resolveBindHost enforces the latter).
func warnIfExposed(out io.Writer, host, adminHash string) {
	if isLoopbackHost(host) {
		return
	}
	if adminHash == "" {
		devPrintf(out, "  NOTE: --host %s ignored — no admin configured, so Lite stays on loopback "+
			"(an unauthenticated control plane is never exposed). Run `leoflow setup` to enable a login first.\n", host)
		return
	}
	devPrintf(out, "  ⚠ SECURITY: binding to %s exposes Leoflow Lite on your network. Lite uses a short "+
		"admin password — only do this on a trusted internal network or VPN, never the public internet.\n", host)
}

// announceReady prints, once the control plane is up, a prominent block with the
// URL to open, the login, and the watched project path — so they are not lost
// above the provisioning output. When the friendly name leoflow.local resolves it
// is shown too; otherwise a one-line tip explains how to enable it.
//
// inherited is how many DAGs were already registered in the local database when
// this session started — leftovers from earlier runs, which keep scheduling and
// failing inside a session that has nothing to do with them (#1104). It is
// reported only when there are some: a line that prints every time stops being
// read, and the clean slate is the common case. A NEGATIVE value means the count
// could not be determined, and says nothing at all — claiming "0" would assert a
// clean slate nobody verified.
func announceReady(out io.Writer, host string, port int, adminEmail, dir string, inherited int) {
	login := "no-auth (loopback only)"
	if adminEmail != "" {
		login = adminEmail
	}
	project := dir
	if abs, err := filepath.Abs(dir); err == nil {
		project = abs
	}
	devPrintf(out, "\n  ✓ Leoflow Lite is ready\n")
	devPrintf(out, "      open:    %s\n", displayURL(host, port))
	if friendlyResolves() {
		devPrintf(out, "      or:      %s\n", fmt.Sprintf("http://%s:%d", friendlyHost, port))
	}
	devPrintf(out, "      login:   %s\n", login)
	devPrintf(out, "      project: %s\n", project)
	if inherited > 0 {
		noun := "DAGs"
		if inherited == 1 {
			noun = "DAG"
		}
		devPrintf(out, "      state:   %d %s registered by earlier sessions (they still run; --fresh starts empty)\n", inherited, noun)
	}
	if !friendlyResolves() {
		devPrintf(out, "      tip: for %s, add '127.0.0.1 %s' to /etc/hosts (sudo).\n",
			friendlyHost, friendlyHost)
	}
	devPrintf(out, "\n")
}

// friendlyHost is the convenience hostname Leoflow suggests for the local UI.
const friendlyHost = "leoflow.local"

// friendlyResolves reports whether leoflow.local resolves on this machine, so the
// ready banner only offers it when it actually works (a hosts entry or mDNS).
func friendlyResolves() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	addrs, err := net.DefaultResolver.LookupHost(ctx, friendlyHost)
	return err == nil && len(addrs) > 0
}

// liteJWTFallbackOnce keeps the legacy-fallback warning to a single log line per
// process (resolveLiteJWTSecret is called by both the token mint and the env
// build, so without this we would log twice).
var liteJWTFallbackOnce sync.Once

// resolveLiteJWTSecret returns the per-install JWT signing secret — rotated on
// every fresh install so a reinstall invalidates stale browser tokens (#121).
// On a legacy install whose config.yaml has no jwt_secret, it falls back to the
// dev-only constant once with a warning, so the upgrade does not break existing
// setups. The input is a plain string (the loaded `jwt_secret`) to stay
// independent of the surrounding cfg type at each call site.
func resolveLiteJWTSecret(secret string) string {
	if secret != "" {
		return secret
	}
	liteJWTFallbackOnce.Do(func() {
		slog.Warn("config jwt_secret is empty; falling back to the dev-only constant — run `leoflow setup` to rotate the per-install secret (#121)")
	})
	return devJWTSecret
}

// liteEnv gathers the run's identity and secrets for the server env. One place
// builds it, so a new field cannot reach one executor and miss the other.
func (o *devOptions) liteEnv() liteEnvParams {
	return liteEnvParams{
		host:              o.host,
		port:              o.port,
		adminHash:         o.adminHash,
		adminEmail:        o.adminEmail,
		jwtSecret:         o.jwtSecret,
		secretKey:         o.secretKey,
		secretKeyPrevious: o.secretKeyPrevious,
	}
}

// warnIfSharedSecretKey tells a user still on the key published in this
// repository what that costs them.
//
// It does not offer a fix, because there is not one yet, and a warning that
// names a command which does not exist is worse than one that admits the gap.
// Migrating an existing install means re-encrypting every stored credential;
// three security reviews of an attempt at it found ordering, interruption and
// privilege defects that each destroyed credentials, so it was pulled out and
// is tracked separately rather than shipped half-right (#486).
func warnIfSharedSecretKey(out io.Writer, key string) {
	if key != "" {
		return
	}
	devPrintln(out, "  WARNING: your connection passwords are encrypted with a key published in this")
	devPrintln(out, "           repository, which every Lite install shares. Anyone who obtains your")
	devPrintln(out, "           datastore file can read them.")
	devPrintln(out, "           Moving an existing install to its own key means re-encrypting every stored")
	devPrintln(out, "           secret; that migration is tracked and not available yet. Until it ships,")
	devPrintln(out, "           treat this datastore as holding readable credentials.")
}

// liteSecretKeyList builds LEOFLOW_SECRET_KEY: the encrypting key first, then
// any decrypt-only predecessor, comma separated. Same rule as Airflow's
// fernet_key.
//
// Lite used to encrypt every connection password with devSecretKey, a constant
// compiled into this repository and therefore identical on every install on
// earth: anyone holding a Lite database file read every credential in it, while
// the docs said "encrypted at rest" (#486).
//
// The predecessor is included ONLY when the install recorded one. A fresh
// install never wrote anything under the constant, so accepting it would
// downgrade that install from "ciphertext needs my key" to "ciphertext needs a
// key published on GitHub", for no benefit.
//
// An empty key means the backfill could not run (a read-only home, a config
// this tool could not parse). Falling back to the constant keeps such an
// install working rather than making its credentials unreadable, with a warning
// that says what is at stake.
func liteSecretKeyList(key, previous string) string {
	if key == "" {
		// An install that predates per-install keys. Its rows are under the
		// published constant, so that is the only key that can read them;
		// warnIfSharedSecretKey has already said what that costs.
		return devSecretKey
	}
	if previous == "" {
		return key
	}
	return key + "," + previous
}

// bringUpDependencies starts Lite's datastore and returns a cleanup func the
// caller defers. It resolves the "auto" --postgres for the host first (Docker
// Postgres when Docker is present, else a managed relocatable PG), then: the
// Docker path is a no-op cleanup (its container is left up across runs); the
// managed path returns a stop, since this run owns the cluster.
func bringUpDependencies(ctx context.Context, cmd *cobra.Command, o *devOptions) (func(), error) {
	noop := func() {}
	if o.noUp {
		return noop, nil
	}
	o.postgres = autoDatastore(cmd, o.postgres)
	if o.postgres == datastoreManaged {
		// Managed relocatable Postgres, no Docker at all: Lite is Redis-free (XCom
		// on Postgres, in-process log tailer — ADR 0026), so nothing comes up via
		// docker compose.
		if perr := startManagedPostgres(ctx, cmd); perr != nil {
			return noop, perr
		}
		//nolint:contextcheck // stop runs at shutdown with a fresh context; the run's ctx is already canceled
		return func() { stopManagedPostgres(cmd) }, nil
	}
	// Docker datastore: only Postgres (Lite needs no Redis). Pick a host port that
	// is free (so a foreign Postgres on 5432 — system or another project — never
	// conflicts; Lite stays isolated rather than adopting it). The port is
	// persisted so reset-password / db reset agree with the running server.
	port, perr := resolveDevDBPort(liteDevDir(), func() int { return firstFreePort(defaultDevDBPort) })
	if perr != nil {
		return noop, perr
	}
	cf, err := resolveComposeFile(o.composeFile)
	if err != nil {
		return noop, err
	}
	o.composeFile = cf
	devPrintf(cmd.OutOrStdout(), "▸ Postgres (Docker) on localhost:%d  [project %s]\n", port, devProjectName())
	return noop, devComposeUp(ctx, cmd, *o, "postgres")
}

// resolveComposeFile returns the docker-compose file Lite uses for its local
// Postgres (Lite is Redis-free — ADR 0026). An explicit --compose wins; else a docker-compose.dev.yaml in
// the working dir (a source checkout) is used; else the compose embedded in the
// binary is materialized under ~/.leoflow, so a binary-only install runs with
// `leoflow lite` alone.
func resolveComposeFile(flagValue string) (string, error) {
	if flagValue != "" {
		return flagValue, nil
	}
	if _, err := os.Stat("docker-compose.dev.yaml"); err == nil {
		return "docker-compose.dev.yaml", nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolving home dir: %w", err)
	}
	dir := filepath.Join(home, ".leoflow")
	if mkErr := os.MkdirAll(dir, 0o750); mkErr != nil {
		return "", fmt.Errorf("creating %s: %w", dir, mkErr)
	}
	path := filepath.Join(dir, "docker-compose.yaml")
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		if wErr := os.WriteFile(path, leoflow.DevCompose(), 0o600); wErr != nil {
			return "", fmt.Errorf("writing managed compose %s: %w", path, wErr)
		}
	}
	return path, nil
}

func newLiteCommand() *cobra.Command {
	var o devOptions
	cmd := &cobra.Command{
		Use:     "lite [path]",
		Aliases: []string{"dev"},
		Short:   "Run Leoflow Lite locally with hot reload.",
		Long: "lite is the Leoflow Lite edition: it brings up local dependencies and runs the " +
			"control plane against an isolated local database, registers the DAG, and hot-reloads " +
			"on every save. The UI is served on a Lite port (default 8088, --port), marked with a " +
			("LITE badge, and behind a login (the admin created by `leoflow setup`, which prints the\n" +
				"generated password ONCE — `leoflow lite reset-password` sets a new one if it is gone).\n\nExecutor ") +
			"(--executor): 'subprocess' runs tasks unsandboxed on the host with no image build — " +
			"the fast inner loop, best for local use. 'k8s' runs real pod-per-task on a dedicated, " +
			"isolated k3d mini-cluster (leoflow-dev) — highest fidelity, best for development; it " +
			"rebuilds the DAG image on each change.\n\n('leoflow dev' remains as a deprecated alias.)",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, err := resolveLiteProject(cmd, args)
			if err != nil {
				return err
			}
			return runDev(cmd, dir, o)
		},
	}
	cmd.Flags().StringVar(&o.executor, "executor", "auto", "execution mode: 'auto' (default; k3d if Docker is present, else subprocess), 'k8s' (dedicated k3d cluster, real pods), or 'subprocess' (host, fast, unsandboxed)")
	cmd.Flags().IntVar(&o.port, "port", devDefaultPort, "HTTP/UI port (dev default 8088, distinct from the demo's 8080)")
	cmd.Flags().StringVar(&o.host, "host", "127.0.0.1", "address to bind the UI/API to; use 0.0.0.0 to reach it from your internal network/VPN (insecure — see the warning)")
	cmd.Flags().StringVar(&o.image, "image", "leoflow-dev:local", "placeholder image recorded in dag.json (subprocess mode only)")
	cmd.Flags().StringVar(&o.composeFile, "compose", "", "compose file for the local Postgres (default: a managed one under ~/.leoflow, materialized on first run)")
	cmd.Flags().StringVar(&o.runtimeSrc, "runtime-src", "runtime/python", "source of the leoflow_runtime package installed into the dev venv")
	cmd.Flags().StringVar(&o.serverBin, "server-bin", "", "leoflow-server binary (default: PATH, then ./bin)")
	cmd.Flags().StringVar(&o.agentBin, "agent-bin", "", "leoflow-agent binary (default: PATH, then ./bin)")
	cmd.Flags().BoolVar(&o.noUp, "no-up", false, "skip docker compose (Postgres already running); the dev DB + venv are still provisioned")
	cmd.Flags().BoolVar(&o.fresh, "fresh", false, "drop the local dev database first, so the session starts with nothing registered (DESTRUCTIVE: registered DAGs, runs and history)")
	cmd.Flags().StringVar(&o.postgres, "postgres", datastoreAuto, "Postgres backend: 'auto' (default; the Docker postgres:16 when Docker is present, else a managed relocatable PG under ~/.leoflow on a Unix socket, no Docker), 'docker', or 'managed' (best on full distros; minimal hosts may lack its system libs)")
	cmd.AddCommand(newLiteProvisionCommand())
	cmd.AddCommand(newResetPasswordCommand())
	cmd.AddCommand(newForgetCommand())
	cmd.AddCommand(newBackupCommand())
	cmd.AddCommand(newRestoreCommand())
	return cmd
}

// devPrintf writes progress output for the dev loop, discarding the unhelpful
// write error (output is a terminal in dev).
func devPrintf(w io.Writer, format string, a ...any) {
	_, _ = fmt.Fprintf(w, format, a...) //nolint:errcheck // best-effort terminal progress output
}

// devPrintln writes a progress line for the dev loop, discarding the write error.
func devPrintln(w io.Writer, a ...any) {
	_, _ = fmt.Fprintln(w, a...) //nolint:errcheck // best-effort terminal progress output
}

// devDagImageRef returns the local image tag built for a DAG in cluster-mode.
func devDagImageRef(dagID string) string {
	return "leoflow-dev-" + dagID + ":dev"
}

// k3dCreateArgs builds the argv to create the dedicated dev cluster.
func k3dCreateArgs(cluster string) []string {
	return []string{"cluster", "create", cluster, "--wait"}
}

// k3dImportArgs builds the argv to import local images into the dev cluster (so
// task pods can use them without a registry).
func k3dImportArgs(cluster string, images ...string) []string {
	args := make([]string, 0, len(images)+3)
	args = append(args, "image", "import")
	args = append(args, images...)
	return append(args, "--cluster", cluster)
}

// devKubeconfigPath returns the isolated kubeconfig file under the dev home; the
// control plane is pointed here so it only ever targets the dev cluster.
func devKubeconfigPath(home string) string {
	return filepath.Join(home, "kubeconfig")
}

// baseImageBuildArgs builds the docker argv for the task base image.
func baseImageBuildArgs() []string {
	return []string{"build", "-f", filepath.Join("runtime", "Dockerfile"),
		"--build-arg", "PYTHON_VERSION=" + devPyVersion, "-t", devBaseImage, "."}
}

// kubectlNamespaceArgs builds the kubectl argv that creates the task-pod
// namespace in the dev cluster.
func kubectlNamespaceArgs(kubeconfig string) []string {
	return []string{"--kubeconfig", kubeconfig, "create", "namespace", devNamespace}
}

// devDockerfile is the Dockerfile generated for a project that does not ship its
// own: it layers the DAG source onto the task base image so the agent can import
// it (matching runtime/Dockerfile's PYTHONPATH convention).
//
// dbtGroups carries the cleaned, sorted project directories of a hybrid DAG's
// dbt task groups — see dbtGroupProjectDirs. They belong in the image for the
// same reason the DAG source does: a group's tasks run `dbt --project-dir
// <project>` from WORKDIR /home/leoflow (#20).
// It shares compile_build.go's guards deliberately. This generator had the whole
// #1070 class and #1064 alongside it, and it is the worse of the two places to
// have them: ensureProjectDockerfile WRITES the result to <project>/Dockerfile
// and never removes it, and ensureDockerfile honours a project-shipped
// Dockerfile verbatim, so one `leoflow lite` run would persist a poisoned file
// that every later `compile --build` then used.
func devDockerfile(baseImage, dagSource string, deps []string, dbtGroups []string) (string, error) {
	base := filepath.Base(dagSource)
	from, ferr := fromOperand("base_image", baseImage)
	if ferr != nil {
		return "", ferr
	}
	var b strings.Builder
	b.WriteString("FROM " + from + "\n")
	// Install the DAG's declared dependencies before COPY so the (rarely-changing)
	// dependency layer is cached across edits to dag.py.
	if len(deps) > 0 {
		args, aerr := shellArgs("dependencies", deps)
		if aerr != nil {
			return "", aerr
		}
		b.WriteString("RUN pip install --no-cache-dir -- " + args + "\n")
	}
	if slices.Contains(dbtGroups, ".") {
		// project: "." means the project is the DAG directory; one COPY covers both.
		b.WriteString("COPY . /home/leoflow/\n")
	} else {
		if err := writeCopy(&b, "dag_source", base); err != nil {
			return "", err
		}
		for _, project := range dbtGroups {
			if err := writeCopy(&b, "dbt_groups.*.project", project); err != nil {
				return "", err
			}
		}
	}
	b.WriteString("ENV PYTHONPATH=/home/leoflow\n")
	return b.String(), nil
}

// liteBanner renders a high-visibility Lite-environment banner so a developer
// never mistakes the local loop for production. url is the served UI address.
func liteBanner(uiURL string) string {
	line := fmt.Sprintf(" LEOFLOW LITE — local — %s ", uiURL)
	bar := ""
	for range line {
		bar += "─"
	}
	return fmt.Sprintf("%s╭%s╮%s\n%s│%s│%s\n%s╰%s╯%s",
		ansiLite, bar, ansiReset,
		ansiLite, line, ansiReset,
		ansiLite, bar, ansiReset)
}

// projectMtimes returns the modtime of each existing path, silently skipping
// paths that do not exist so a not-yet-created file can be detected on creation.
func projectMtimes(paths []string) map[string]time.Time {
	out := make(map[string]time.Time, len(paths))
	for _, p := range paths {
		if fi, err := os.Stat(p); err == nil {
			out[p] = fi.ModTime()
		}
	}
	return out
}

// mtimesChanged reports whether any watched file appeared, vanished, or had its
// modtime move between two snapshots.
func mtimesChanged(prev, cur map[string]time.Time) bool {
	if len(prev) != len(cur) {
		return true
	}
	for p, t := range cur {
		if old, ok := prev[p]; !ok || !old.Equal(t) {
			return true
		}
	}
	return false
}

// runDev orchestrates the all-in-one local loop: deps, control plane (subprocess
// executor), DAG registration, and hot reload on save.
func runDev(cmd *cobra.Command, dir string, o devOptions) error {
	out := cmd.OutOrStdout()
	ws, err := prepareWorkspace(cmd, out, dir)
	if err != nil {
		return err
	}
	// Apply the executor/port chosen in `leoflow setup` (stored in config) as the
	// defaults, unless overridden on the command line. This honors the wizard's
	// choice while keeping --executor/--port changeable per run.
	applyLiteConfigDefaults(cmd, &o)
	if o.port == 0 {
		o.port = devDefaultPort
	}
	uiURL := devURL(o.port)
	devPrintln(out, liteBanner(uiURL))

	// The admin login is provisioned by `leoflow setup` (hash-only in config).
	// With it, Lite enforces real auth; without it, fall back to no-auth + warn.
	id := resolveLiteAdmin(cmd, out)
	o.adminHash, o.adminEmail, o.jwtSecret = id.adminHash, id.adminEmail, id.jwtSecret
	o.secretKey, o.secretKeyPrevious = id.secretKey, id.secretKeyPrevious
	warnIfSharedSecretKey(out, o.secretKey)

	ctx, stop := signal.NotifyContext(cmdContext(cmd), os.Interrupt, syscall.SIGTERM)
	defer stop()

	warnIfExposed(out, o.host, o.adminHash)
	// Fail fast on a port conflict (a second Lite instance, or another service on
	// the HTTP/gRPC/metrics ports) with a clear message, before starting Docker.
	if perr := preflightDevPorts(ctx, o.host, o.port); perr != nil {
		return perr
	}
	cleanupDeps, uerr := bringUpDependencies(ctx, cmd, &o)
	if uerr != nil {
		return uerr
	}
	defer cleanupDeps() // stops managed Postgres on exit; no-op for the Docker path
	// --fresh drops the local database before it is recreated, so a session
	// starts with nothing registered. State under ~/.leoflow/dev otherwise
	// outlives every session: a DAG from an old spike stays registered, keeps
	// being scheduled, and fails inside a run that has nothing to do with it
	// (#1104). Destructive by definition, and scoped to the Lite dev database —
	// it is the same drop `leoflow db reset` performs.
	// Provision the isolated dev state: own database + own venv (never the
	// product's database or the system Python).
	if derr := provisionDevDatabase(ctx, cmd, out, o.fresh); derr != nil {
		return derr
	}
	if merr := devMigrate(cmd); merr != nil {
		return merr
	}
	home, herr := devHome()
	if herr != nil {
		return herr
	}
	serverBin, berr := resolveAndReport(cmdContext(cmd), cmd, o.serverBin, "leoflow-server")
	if berr != nil {
		return berr
	}

	// "auto" (the default) uses k3d when Docker is present, else the unsandboxed
	// subprocess executor so `leoflow lite` still runs without Docker.
	o.executor = autoExecutor(cmd, o.executor)

	// Mode-specific setup: the env the control plane runs with and the per-reload
	// build/register strategy. k8s runs real pods on a dedicated k3d cluster;
	// subprocess runs unsandboxed on the host (fast loop).
	var serverEnv []string
	var makeReload func(mintToken func() string) func() error
	if o.executor == "subprocess" {
		serverEnv, makeReload, err = devSubprocessSetup(ctx, cmd, ws, o, home)
	} else {
		serverEnv, makeReload, err = devClusterSetup(ctx, cmd, ws, o, home)
	}
	if err != nil {
		return err
	}

	server, serr := startDevServer(ctx, cmd, serverBin, serverEnv)
	if serr != nil {
		return serr
	}
	defer func() { _ = server.Process.Signal(syscall.SIGTERM) }() //nolint:errcheck // best-effort shutdown of the dev server

	if werr := waitForReady(ctx, uiURL); werr != nil {
		return werr
	}
	announceReady(out, o.host, o.port, o.adminEmail, ws.Path, countRegisteredDags(ctx))
	// Mint an admin token in-process signed with the dev JWT secret; the control
	// plane validates it by signature + claims, so no login or seeded user is
	// needed. Re-minted per operation (signing is cheap) so a Lite left running
	// for hours never hits the token's expiry — hot-reload registration keeps
	// working instead of silently 401-ing after an hour (#407).
	logf := func(format string, args ...any) { devPrintf(cmd.OutOrStdout(), format+"\n", args...) }
	mintToken := func() string {
		tok, err := auth.MintUserToken(resolveLiteJWTSecret(o.jwtSecret), time.Hour, auth.User{
			ID: auth.DevTokenSubject, TenantID: "default", Email: devAdminUser, Roles: []string{"admin"},
		})
		if err != nil {
			logf("✗ minting dev token: %v", err)
			return ""
		}
		return tok
	}
	if mintToken() == "" {
		return fmt.Errorf("minting the dev token failed — check the JWT secret")
	}
	del := makeDeleteDag(mintToken, uiURL, home, logf)
	boot := makeBootReconcile(mintToken, uiURL, ws.Path, projectDagIDs(ws), del, logf)
	return devWatchLoop(ctx, cmd, ws, makeReload(mintToken), del, boot)
}

// makeDeleteDag returns a callback the Lite watcher calls when it notices a
// project disappeared from disk (issue #345). The callback hits the control
// plane's DELETE /api/v2/dags/<id>?deregister=true endpoint — the
// hard-delete variant (cascades versions/runs/TIs/XCom via the schema's
// ON DELETE CASCADE) — because the user's intent in deleting the project
// folder is to fully forget the DAG, not just clear its run history.
func makeDeleteDag(mintToken func() string, uiURL, home string, logf func(format string, args ...any)) func(dagID string) error {
	return func(dagID string) error {
		reqURL := fmt.Sprintf("%s/api/v2/dags/%s?deregister=true", uiURL, url.PathEscape(dagID))
		if err := devImportErrorRequest(context.Background(), http.MethodDelete, reqURL, mintToken(), nil); err != nil {
			return err
		}
		// Reclaim the per-DAG venv with the DAG (it carries the Airflow SDK). A
		// later reload re-creates it if the DAG comes back. Best-effort + logged.
		if removed, rerr := removeDagVenv(home, dagID); rerr != nil {
			logf("✗ removed dag %q but could not remove its venv: %v", dagID, rerr)
		} else if removed {
			logf("🧹 removed the per-DAG venv for %q", dagID)
		}
		return nil
	}
}

// devSubprocessSetup provisions the isolated venv and returns the subprocess
// server env plus a reload that re-discovers + recompiles + registers every
// project in the workspace (no image build) — the fast multi-DAG inner loop,
// with user code running unsandboxed on the host. The venv is shared across
// projects and uses the dependency-union from WorkspaceSpec.RootCfg so every
// DAG's imports resolve from a single virtualenv.
func devSubprocessSetup(ctx context.Context, cmd *cobra.Command, ws *WorkspaceSpec, o devOptions, home string) (env []string, makeReload func(func() string) func() error, err error) {
	agentBin, err := resolveAndReport(ctx, cmd, o.agentBin, "leoflow-agent")
	if err != nil {
		return nil, nil, err
	}
	// Self-heal the extracted Python sources before anything references them.
	// On a binary-only install (no repo) resolveRuntimeSrc points the per-DAG
	// venv at ~/.leoflow/pysrc/runtime/python; if the boot provisions that venv
	// before the sources are extracted, pip aborts with "does not exist" (#587).
	// Checksum-gated, so a warm install pays nothing.
	ensurePysrc(cmd)
	runtimeSrc := resolveRuntimeSrc(o.runtimeSrc, home)
	// Per-DAG venvs: every project gets its own ~/.leoflow/dev/venvs/<dag_id>/.
	// Editing one project's `dependencies:` only re-runs pip for THAT project,
	// not the whole workspace (#346). The first project's venv is the boot
	// fallback exposed via LEOFLOW_PYTHON so the agent always has a runnable
	// interpreter even before per-DAG resolution kicks in on Execute().
	bootPy, verr := ensureWorkspaceDagVenvs(ctx, cmd, ws, home, runtimeSrc)
	if verr != nil {
		return nil, nil, verr
	}
	venvsRoot := filepath.Join(home, "venvs")
	env = subprocessServerEnv(o.liteEnv(), agentBin, ws.Path, bootPy, venvsRoot)
	env = append(env, liteEditorEnv(ws.Path, filepath.Dir(home))...)
	makeReload = func(mintToken func() string) func() error {
		return func() error {
			token := mintToken()
			// Re-resolve the workspace on every reload so DAGs added or removed
			// since boot are picked up (no need to restart lite to register a new
			// subdir).
			curWs, rerr := ResolveWorkspace(ws.Path)
			if rerr != nil {
				return rerr
			}
			// Refresh per-DAG venvs on every reload too — a new project, or a
			// `dependencies:` edit, is picked up here (the gate inside
			// ensureDagVenv makes the unchanged-project case a cheap no-op).
			if _, perr := ensureWorkspaceDagVenvs(ctx, cmd, curWs, home, runtimeSrc); perr != nil {
				return perr
			}
			// Seed declared connections the environment already carries, BEFORE
			// registration reads the vault — registration is fail-closed, and in
			// Lite the value is usually already exported for the task to consume
			// (#1103).
			seedDeclaredConnections(ctx, cmd, curWs, token, devURL(o.port))
			// local: this is the subprocess run mode, so tasks execute on this
			// host and dbt needs the absolute workspace path (#993).
			return devCompileAndRegisterAll(ctx, cmd, curWs, compileOptions{image: o.image, local: true}, token, nil, devURL(o.port))
		}
	}
	return env, makeReload, nil
}

// ensureWorkspaceDagVenvs creates / refreshes the per-DAG venv for every
// project in the workspace and returns the first project's venv Python, which
// the control plane exports as LEOFLOW_PYTHON for the boot fallback path.
// Failure to provision any one project's venv stops the loop and surfaces
// which project failed — there is no point pretending the workspace is up
// when a DAG cannot import its runtime.
func ensureWorkspaceDagVenvs(ctx context.Context, cmd *cobra.Command, ws *WorkspaceSpec, home, runtimeSrc string) (bootPy string, err error) {
	for _, p := range ws.Projects {
		dagID := p.DagID
		deps := []string(nil)
		pyVersion := ""
		if p.Config != nil {
			var derr error
			if deps, derr = p.Config.EffectiveDependencies(); derr != nil {
				return "", fmt.Errorf("resolving dependencies for project %q: %w", p.Path, derr)
			}
			pyVersion = devEnforcedPythonVersion(cmd, p.Config)
		}
		py, verr := ensureDagVenv(ctx, cmd, home, dagID, runtimeSrc, pyVersion, deps)
		if verr != nil {
			return "", fmt.Errorf("provisioning venv for project %q: %w", p.Path, verr)
		}
		if bootPy == "" {
			bootPy = py
		}
	}
	if bootPy == "" {
		// No projects discovered yet — fall back to the legacy single-venv
		// location so the server can still boot with a usable LEOFLOW_PYTHON.
		// A later watcher tick will create per-DAG venvs once the user adds a
		// dag.py to the workspace.
		bootPy = venvPython(home)
	}
	return bootPy, nil
}

// devClusterSetup ensures the task base image, the dedicated k3d cluster, its
// namespace, and an isolated kubeconfig, then returns the Kubernetes-executor
// server env plus a reload that builds the DAG image, imports it into the
// cluster, and registers — real pod-per-task, fully isolated, at the cost of
// an image build per change.
//
// Multi-DAG workspaces are NOT supported in cluster mode for v1: every save
// would rebuild N images and re-import each into k3d (~15-30s per save with
// BuildKit cache; far worse cold). The function fails loud with a hint to use
// --executor=subprocess. Cluster-mode multi-DAG is tracked as a follow-up
// (the "Stage" mode design conversation).
func devClusterSetup(ctx context.Context, cmd *cobra.Command, ws *WorkspaceSpec, o devOptions, home string) (env []string, makeReload func(func() string) func() error, err error) {
	if len(ws.Projects) != 1 {
		paths := make([]string, 0, len(ws.Projects))
		for _, p := range ws.Projects {
			paths = append(paths, p.Path)
		}
		return nil, nil, fmt.Errorf("cluster mode (--executor=k8s) does not support multi-DAG workspaces in v1 (got %d projects: %v)\n"+
			"  - use --executor=subprocess for multi-DAG dev (the default, no Docker needed)\n"+
			"  - or point lite at a single project: `leoflow lite <project-dir>`",
			len(ws.Projects), paths)
	}
	project := ws.Projects[0]
	dir := project.Path
	cfg := project.Config
	if berr := ensureBaseImage(ctx, cmd); berr != nil {
		return nil, nil, berr
	}
	if cerr := ensureDevCluster(ctx, cmd); cerr != nil {
		return nil, nil, cerr
	}
	kubeconfig := devKubeconfigPath(home)
	if kerr := writeDevKubeconfig(ctx, cmd, kubeconfig); kerr != nil {
		return nil, nil, kerr
	}
	if nerr := ensureDevNamespace(ctx, cmd, kubeconfig); nerr != nil {
		return nil, nil, nerr
	}
	if derr := ensureProjectDockerfile(cmd, dir, cfg); derr != nil {
		return nil, nil, derr
	}
	image := devDagImageRef(cfg.DagID)
	makeReload = func(mintToken func() string) func() error {
		return func() error {
			token := mintToken()
			base := func() error {
				opts := compileOptions{image: image, build: true, builder: "docker", dockerfile: "Dockerfile"}
				return devCompileAndRegister(ctx, cmd, dir, opts, token, func() error {
					return k3dImport(ctx, cmd, image)
				}, devURL(o.port))
			}
			return devReportingReload(ctx, base, devURL(o.port), token, dagSourcePath(dir, cfg))()
		}
	}
	env = clusterServerEnv(o.liteEnv(), kubeconfig)
	if wd, aerr := filepath.Abs(dir); aerr == nil {
		env = append(env, liteEditorEnv(wd, filepath.Dir(home))...)
	}
	return env, makeReload, nil
}

// devComposeUp starts the named local datastore services via docker compose. Lite
// brings up only Postgres (it is Redis-free — ADR 0026); the dev's own database
// lives inside it, isolated by name. The compose runs under a per-install project
// name (so two users/installs never share or clobber a container/volume) and on
// the resolved host port (LEOFLOW_DB_PORT, which the compose interpolates).
func devComposeUp(ctx context.Context, cmd *cobra.Command, o devOptions, services ...string) error {
	devPrintln(cmd.OutOrStdout(), "▸ starting dependencies (docker compose) …")
	var captured bytes.Buffer
	args := append([]string{"compose", "-f", o.composeFile, "up", "-d", "--wait"}, services...)
	up := exec.CommandContext(ctx, "docker", args...) //nolint:gosec // operator-supplied compose file on the dev CLI
	up.Env = composeEnv()
	up.Stdout = cmd.OutOrStdout()
	// Tee stderr so the user still sees compose progress while we inspect it to
	// translate a port-allocation failure into an actionable message.
	up.Stderr = io.MultiWriter(cmd.ErrOrStderr(), &captured)
	if err := up.Run(); err != nil {
		return composeUpError(err, captured.String())
	}
	return nil
}

// composeUpError turns a `docker compose up` failure into an actionable message.
// A port-allocation failure — a foreign Postgres already bound to 5432, the
// common real-world conflict — produces a cryptic raw Docker error, so it is
// translated; any other failure keeps the generic "is Docker running?" hint.
func composeUpError(err error, output string) error {
	low := strings.ToLower(output)
	if strings.Contains(low, "already allocated") || strings.Contains(low, "address already in use") || strings.Contains(low, "port is already") {
		return fmt.Errorf("the Postgres port 5432 is already in use — another Postgres is bound to it. Stop it, run `leoflow lite --postgres managed` (a private, socket-only Postgres), or `leoflow lite --no-up` to point at your own (LEOFLOW_DATABASE_URL): %w", err)
	}
	if strings.Contains(low, "unknown command") || strings.Contains(low, "is not a docker command") || strings.Contains(low, "compose") && strings.Contains(low, "not found") {
		return fmt.Errorf("the Docker Compose v2 plugin is not installed (the `docker compose` subcommand is missing). Install it, or run `leoflow lite --postgres managed` for a Docker-free Postgres: %w", err)
	}
	return fmt.Errorf("docker compose up (is Docker running, with the Compose v2 plugin?): %w", err)
}

// preflightDevPorts checks that the HTTP, gRPC, and metrics ports Lite needs are
// free, failing with a clear message that names the busy port — turning the
// server's deep "bind: address already in use" into actionable advice before
// anything starts. Best-effort: a port freed between the check and the bind still
// surfaces the server's own error. The gRPC/metrics ports derive from the HTTP
// --port, so picking a different --port sidesteps a conflict.
func preflightDevPorts(ctx context.Context, host string, port int) error {
	bindHost := host
	if bindHost == "" || bindHost == "0.0.0.0" {
		bindHost = "127.0.0.1"
	}
	checks := []struct {
		role string
		addr string
		num  int
	}{
		{"the HTTP/UI server", net.JoinHostPort(bindHost, strconv.Itoa(port)), port},
		{"the agent gRPC server", fmt.Sprintf(":%d", devGRPCPort(port)), devGRPCPort(port)},
		{"the metrics endpoint", fmt.Sprintf(":%d", devMetricsPort(port)), devMetricsPort(port)},
	}
	var lc net.ListenConfig
	for _, c := range checks {
		ln, err := lc.Listen(ctx, "tcp", c.addr)
		if err != nil {
			return fmt.Errorf("port %d is already in use (needed for %s); another Leoflow Lite may be running — stop it, or pass --port to pick a free port", c.num, c.role)
		}
		_ = ln.Close() //nolint:errcheck // best-effort probe; closing frees the port for the real bind
	}
	return nil
}

// devMigrate applies the embedded SQL migrations to the isolated dev database.
// The migrations are compiled into the binary (no source tree or migrate CLI),
// a step toward a binaries-only dev install (#60).
//
// Before applying anything, devMigrate refuses to start when the database's
// schema_migrations.version is HIGHER than the highest version embedded in
// this binary (#136). That shape means an older binary is being run against
// a database that a newer binary already upgraded — proceeding silently would
// let the older binary read/write rows under a schema it does not understand.
// The user is told to upgrade the binary, or to run `leoflow uninstall --purge`
// to start over.
func devMigrate(cmd *cobra.Command) error {
	devPrintln(cmd.OutOrStdout(), "▸ migrating "+devDBName+" (embedded) …")
	src, err := iofs.New(migrations.Files, ".")
	if err != nil {
		return fmt.Errorf("loading embedded migrations: %w", err)
	}
	m, err := migrate.NewWithSourceInstance("iofs", src, devDSNs().migrate)
	if err != nil {
		return fmt.Errorf("initializing migrator: %w", err)
	}
	defer func() { _, _ = m.Close() }() //nolint:errcheck // best-effort close of source + db handles
	if derr := checkDevSchemaDrift(m); derr != nil {
		return derr
	}
	if uerr := m.Up(); uerr != nil && !errors.Is(uerr, migrate.ErrNoChange) {
		return fmt.Errorf("applying migrations: %w", uerr)
	}
	return nil
}

// checkDevSchemaDrift compares the DB's current schema version against the
// highest migration the binary embeds and refuses to start when the DB is
// ahead. A fresh DB (no rows in schema_migrations, surfaced as
// migrate.ErrNilVersion) is the expected first-run state and proceeds.
func checkDevSchemaDrift(m *migrate.Migrate) error {
	dbVersion, dirty, err := m.Version()
	if errors.Is(err, migrate.ErrNilVersion) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("reading current schema version: %w", err)
	}
	embedded, lerr := migrations.Latest()
	if lerr != nil {
		return fmt.Errorf("checking embedded migrations: %w", lerr)
	}
	return decideSchemaDrift(dbVersion, dirty, embedded)
}

// decideSchemaDrift is the pure decision the drift check applies — split out
// so it can be unit-tested without a real Postgres. dbVersion is the value in
// the database's schema_migrations table; embedded is the highest version
// the binary embeds (migrations.Latest()). A DB ahead of the binary is the
// drift case (#136); a dirty marker is a separate operational failure.
func decideSchemaDrift(dbVersion uint, dirty bool, embedded uint) error {
	if dirty {
		return fmt.Errorf(
			"database schema is marked dirty at version %d (a prior migration was interrupted); "+
				"run `leoflow uninstall --purge` to reset, or fix manually with `migrate force` if you know what you are doing",
			dbVersion,
		)
	}
	if dbVersion > embedded {
		return fmt.Errorf(
			"database is at schema version %d but this binary only knows up to %d; "+
				"an older `leoflow` is being run against a newer database. "+
				"Upgrade the binary, or run `leoflow uninstall --purge` to start over (this WIPES your data)",
			dbVersion, embedded,
		)
	}
	return nil
}

// ensureDevDatabase creates the isolated leoflow_dev database if it does not yet
// exist, so the dev experience never shares the product's "leoflow" database.
func ensureDevDatabase(ctx context.Context, cmd *cobra.Command) error {
	conn, err := pgx.Connect(ctx, devDSNs().maintenance)
	if err != nil {
		return fmt.Errorf("connecting to Postgres (is it up?): %w", err)
	}
	defer func() { _ = conn.Close(ctx) }() //nolint:errcheck // best-effort close of a short-lived maintenance connection
	var exists bool
	if qerr := conn.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM pg_database WHERE datname=$1)", devDBName).Scan(&exists); qerr != nil {
		return fmt.Errorf("checking for %s: %w", devDBName, qerr)
	}
	if exists {
		return nil
	}
	devPrintln(cmd.OutOrStdout(), "▸ creating isolated dev database "+devDBName+" …")
	//nolint:gosec // G201: the database name is a fixed constant, sanitized as an identifier.
	if _, eerr := conn.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{devDBName}.Sanitize()); eerr != nil {
		return fmt.Errorf("creating database %s: %w", devDBName, eerr)
	}
	return nil
}

// devHome returns the isolated dev state directory (~/.leoflow/dev), created on
// demand. All dev state (the venv, etc.) lives here, never in the project.
func devHome() (string, error) {
	h, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolving home dir: %w", err)
	}
	d := filepath.Join(h, ".leoflow", "dev")
	if mkerr := os.MkdirAll(d, 0o750); mkerr != nil {
		return "", fmt.Errorf("creating dev home %s: %w", d, mkerr)
	}
	return d, nil
}

// liteDevDir is the per-user Lite scratch dir (~/.leoflow/dev) for state that must
// never be shared between users: the compiled dag.json and task logs. A global
// /tmp path is owned by whoever ran Lite first and then denies every other user
// (the root-vs-non-root "permission denied" trap). Best-effort: it falls back to a
// temp dir only if the home cannot be resolved.
func liteDevDir() string {
	if h, err := os.UserHomeDir(); err == nil {
		return filepath.Join(h, ".leoflow", "dev")
	}
	return os.TempDir()
}

// venvPython returns the Python interpreter inside the dev venv under home,
// honoring the platform layout (bin on Unix, Scripts on Windows).
func venvPython(home string) string {
	if runtime.GOOS == "windows" {
		return filepath.Join(home, "venv", "Scripts", "python.exe")
	}
	return filepath.Join(home, "venv", "bin", "python")
}

// minPythonMinor is the lowest Python 3.x the task runtime + Airflow SDK
// support; the managed relocatable CPython is pinned to this minor.
const minPythonMinor = 11

// devBasePython returns the interpreter used to CREATE a dev venv.
//
// With a declared wantVersion ("3.13") it resolves an interpreter REPORTING that
// minor, and refuses rather than substituting — see resolvePythonFor. With no
// declared version it keeps the historical precedence: the managed relocatable
// CPython (installed by `leoflow setup` under ~/.leoflow/python) when present,
// since it bundles venv + ensurepip, falling back to a python3.11 / python3 on
// PATH that reports >= 3.11. Using the managed interpreter avoids
// needing the system python3-venv package, which Debian/Ubuntu split out (the
// common first-run failure: "ensurepip is not available").
//
// It errors when an interpreter IS present but reports an unsupported version,
// or when none is found at all — a venv cannot be built without a base — rather
// than returning a bare "python3" that fails opaquely later (#742).
func devBasePython(ctx context.Context, home, wantVersion string) (string, error) {
	managed := filepath.Join(filepath.Dir(home), "python", "bin", "python3.11")
	// A declared python_version is the authoring surface's statement about which
	// interpreter the task runs on, and the cluster honors it through the task
	// base image. Honor it here too, or `leoflow dev` validates the DAG on an
	// interpreter the deployment will never use (#1092).
	if wantVersion != "" {
		want, verr := parsePythonMinor(wantVersion)
		if verr != nil {
			return "", verr
		}
		return resolvePythonFor(ctx, want, managed, exec.LookPath, pythonVersion)
	}
	p, err := resolvePython3(ctx, managed, exec.LookPath, pythonVersion)
	if err != nil {
		return "", err
	}
	if p == "" {
		return "", fmt.Errorf("no Python interpreter found; run `leoflow setup` to provision a managed CPython 3.%d", minPythonMinor)
	}
	return p, nil
}

// leoflowManagedPython returns the path to the managed relocatable CPython that
// `leoflow setup` installs under ~/.leoflow/python, or "" if the home directory
// cannot be resolved.
func leoflowManagedPython() string {
	h, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(h, ".leoflow", "python", "bin", "python3.11")
}

// resolvePython3 returns a usable Python >= 3.11 with a single, unified
// precedence shared by `leoflow dev` and `leoflow validate` (#742):
//
//   - the managed pinned build (when present) wins — it is the checksum-verified
//     CPython `leoflow setup` provisioned at exactly the pinned version, so it is
//     trusted without being re-executed;
//   - otherwise the first host python3.11/python3 that reports >= 3.11 is used;
//   - a host interpreter that IS present but reports an unsupported version is
//     rejected with an actionable `leoflow setup` hint, so a half-provisioned
//     host never silently runs an unsupported interpreter that fails later at an
//     unrelated component;
//   - when no interpreter exists at all, it returns ("", nil) and lets the
//     caller decide whether that is fatal.
//
// managed is the managed interpreter path (may be ""); lookPath and runVersion
// are injectable for tests.
func resolvePython3(ctx context.Context, managed string, lookPath func(string) (string, error), runVersion func(context.Context, string) (int, int, error)) (string, error) {
	if managed != "" {
		if _, err := os.Stat(managed); err == nil {
			return managed, nil
		}
	}
	sawUnsupported := false
	for _, name := range []string{"python3.11", "python3"} {
		p, err := lookPath(name)
		if err != nil {
			continue
		}
		major, minor, verr := runVersion(ctx, p)
		if verr != nil {
			continue // cannot determine version; try the next candidate
		}
		if major > 3 || (major == 3 && minor >= minPythonMinor) {
			return p, nil
		}
		sawUnsupported = true
	}
	if sawUnsupported {
		return "", fmt.Errorf("found a Python interpreter but it is older than 3.%d; run `leoflow setup` to provision a managed CPython", minPythonMinor)
	}
	return "", nil
}

// pythonVersion runs `<py> --version` and returns the reported major.minor.
func pythonVersion(ctx context.Context, py string) (major, minor int, err error) {
	out, err := exec.CommandContext(ctx, py, "--version").CombinedOutput() //nolint:gosec // py is a resolved interpreter path (managed or LookPath result)
	if err != nil {
		return 0, 0, fmt.Errorf("running %s --version: %w", py, err)
	}
	return parsePythonVersion(string(out))
}

// parsePythonVersion extracts the major and minor from a `python --version` line
// such as "Python 3.11.15" (the version token may carry a patch and a pre-release
// suffix, e.g. "3.12.0rc1").
func parsePythonVersion(s string) (major, minor int, err error) {
	fields := strings.Fields(s)
	if len(fields) < 2 {
		return 0, 0, fmt.Errorf("unrecognized python version output %q", strings.TrimSpace(s))
	}
	ver := fields[len(fields)-1] // "3.11.15"
	parts := strings.SplitN(ver, ".", 3)
	if len(parts) < 2 {
		return 0, 0, fmt.Errorf("unrecognized python version %q", ver)
	}
	major, err = strconv.Atoi(parts[0])
	if err != nil {
		return 0, 0, fmt.Errorf("parsing python major from %q: %w", ver, err)
	}
	minor, err = strconv.Atoi(parts[1])
	if err != nil {
		return 0, 0, fmt.Errorf("parsing python minor from %q: %w", ver, err)
	}
	return major, minor, nil
}

// resolveRuntimeSrc returns the leoflow_runtime package source to pip-install
// into the dev venv. An explicit --runtime-src wins; otherwise the repo path
// (source checkout) is used when present; otherwise the copy `leoflow setup`
// extracted under ~/.leoflow/pysrc — a binary-only install has no repo, so the
// repo-relative "runtime/python" does not exist there.
func resolveRuntimeSrc(flagValue, home string) string {
	if flagValue != "" && flagValue != "runtime/python" {
		return flagValue
	}
	if _, err := os.Stat(filepath.Join("runtime", "python", "pyproject.toml")); err == nil {
		return "runtime/python"
	}
	return filepath.Join(filepath.Dir(home), "pysrc", "runtime", "python")
}

// devDepsSignature is an order-independent canonical form of a dependency list, so
// reordering `dependencies:` does not force a needless reinstall.
func devDepsSignature(deps []string) string {
	s := append([]string(nil), deps...)
	sort.Strings(s)
	return strings.Join(s, "\n")
}

// runtimeSrcChecksum returns a stable SHA-256 hex digest covering every `.py`
// file under runtimeSrc, walked in deterministic path order. Used by
// ensureDagVenv to decide whether the venv's installed leoflow_runtime has
// drifted from the bundled pysrc after a binary upgrade (#239).
//
// Non-Python files (READMEs, __pycache__/*.pyc, etc.) are ignored: a stray
// .pyc generated by a previous run would otherwise force perpetual
// reinstalls.
func runtimeSrcChecksum(runtimeSrc string) (string, error) {
	var files []string
	err := filepath.WalkDir(runtimeSrc, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".py") {
			return nil
		}
		files = append(files, path)
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("walking runtime source: %w", err)
	}
	sort.Strings(files)

	h := sha256.New()
	for _, p := range files {
		rel, rerr := filepath.Rel(runtimeSrc, p)
		if rerr != nil {
			return "", fmt.Errorf("relativizing %s: %w", p, rerr)
		}
		// Hash the path + a separator + the content + a separator so two files
		// can't collide by content concatenation (e.g. moving lines between files).
		h.Write([]byte(rel))
		h.Write([]byte{0})
		b, rerr := os.ReadFile(p) //nolint:gosec // path is under the bundled pysrc tree
		if rerr != nil {
			return "", fmt.Errorf("reading %s: %w", p, rerr)
		}
		h.Write(b)
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// devRun and devOutput run external dev tools (k3d/docker/kubectl). They are
// package variables so tests can stub the external-tool calls; devRun streams to
// the command's output, devOutput captures combined output for inspection.
var (
	devRun = func(ctx context.Context, cmd *cobra.Command, name string, args ...string) error {
		c := exec.CommandContext(ctx, name, args...) //nolint:gosec // dev tool invoking fixed external binaries
		c.Stdout, c.Stderr = cmd.OutOrStdout(), cmd.ErrOrStderr()
		return c.Run()
	}
	devOutput = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return exec.CommandContext(ctx, name, args...).CombinedOutput() //nolint:gosec // dev tool invoking fixed external binaries
	}
)

// ensureBaseImage builds the task base image (runtime/Dockerfile) if it is not
// already present, so DAG images can layer onto it. Requires the leoflow source
// tree (it builds from runtime/Dockerfile with the repo as context).
func ensureBaseImage(ctx context.Context, cmd *cobra.Command) error {
	if _, err := devOutput(ctx, "docker", "image", "inspect", devBaseImage); err == nil {
		return nil
	}
	// The base image is built from runtime/Dockerfile with the repo as context;
	// a binary install (curl|sh) has no source tree, so fail clearly and point at
	// the local run mode instead of the cryptic "lstat runtime: no such file".
	if _, err := os.Stat(filepath.Join("runtime", "Dockerfile")); err != nil {
		return fmt.Errorf("cluster run mode needs the Leoflow source tree to build the task base image " +
			"(runtime/Dockerfile), which a binary install does not have.\n" +
			"  Use the 'local' run mode: re-run `leoflow setup` and choose 1 (local), " +
			"or set `lite_executor: subprocess` in ~/.leoflow/config.yaml.\n" +
			"  (Cluster mode works when you run `leoflow lite` from a Leoflow source checkout.)")
	}
	devPrintln(cmd.OutOrStdout(), "▸ building task base image "+devBaseImage+" (first run) …")
	if err := devRun(ctx, cmd, "docker", baseImageBuildArgs()...); err != nil {
		return fmt.Errorf("building base image (run from the leoflow source tree): %w", err)
	}
	return nil
}

// ensureDevCluster creates the dedicated k3d cluster if it does not exist.
func ensureDevCluster(ctx context.Context, cmd *cobra.Command) error {
	out, _ := devOutput(ctx, "k3d", "cluster", "list", "--no-headers") //nolint:errcheck // absence is handled below
	if strings.Contains(string(out), devClusterName) {
		return nil
	}
	devPrintln(cmd.OutOrStdout(), "▸ creating dedicated dev cluster "+devClusterName+" (first run) …")
	if err := devRun(ctx, cmd, "k3d", k3dCreateArgs(devClusterName)...); err != nil {
		return fmt.Errorf("creating k3d cluster %s (is k3d installed?): %w", devClusterName, err)
	}
	return nil
}

// writeDevKubeconfig writes the dev cluster's kubeconfig to an isolated file so
// the control plane only ever targets leoflow-dev, never the product cluster.
func writeDevKubeconfig(ctx context.Context, _ *cobra.Command, path string) error {
	out, err := devOutput(ctx, "k3d", "kubeconfig", "get", devClusterName)
	if err != nil {
		return fmt.Errorf("getting kubeconfig for %s: %w", devClusterName, err)
	}
	if werr := os.WriteFile(path, out, 0o600); werr != nil {
		return fmt.Errorf("writing kubeconfig %s: %w", path, werr)
	}
	return nil
}

// ensureDevNamespace creates the task-pod namespace in the dev cluster (idempotent).
func ensureDevNamespace(ctx context.Context, _ *cobra.Command, kubeconfig string) error {
	out, err := devOutput(ctx, "kubectl", kubectlNamespaceArgs(kubeconfig)...)
	if err != nil && !strings.Contains(string(out), "already exists") {
		return fmt.Errorf("creating namespace %s: %s: %w", devNamespace, strings.TrimSpace(string(out)), err)
	}
	return nil
}

// k3dImport imports a locally-built image into the dev cluster so task pods can
// use it without a registry.
func k3dImport(ctx context.Context, cmd *cobra.Command, image string) error {
	devPrintln(cmd.OutOrStdout(), "▸ importing "+image+" into "+devClusterName+" …")
	if err := devRun(ctx, cmd, "k3d", k3dImportArgs(devClusterName, image)...); err != nil {
		return fmt.Errorf("importing %s into %s: %w", image, devClusterName, err)
	}
	return nil
}

// ensureProjectDockerfile generates a default Dockerfile when the project lacks
// one, layering the DAG source onto the task base image.
func ensureProjectDockerfile(cmd *cobra.Command, dir string, cfg *domain.LeoflowConfig) error {
	df := filepath.Join(dir, "Dockerfile")
	if _, err := os.Stat(df); err == nil {
		return nil
	}
	src := cfg.DagSource
	if src == "" {
		src = "dag.py"
	}
	deps, derr := cfg.EffectiveDependencies()
	if derr != nil {
		return fmt.Errorf("resolving dependencies: %w", derr)
	}
	content, gerr := devDockerfile(devBaseImage, src, deps, dbtGroupProjectDirs(cfg))
	if gerr != nil {
		return gerr
	}
	devPrintln(cmd.OutOrStdout(), "▸ generating a default Dockerfile (none found) …")
	if werr := os.WriteFile(df, []byte(content), 0o600); werr != nil {
		return fmt.Errorf("writing Dockerfile: %w", werr)
	}
	return nil
}

// resolveAndReport locates a companion binary and announces the choice, so the
// two always happen together — a resolution nobody printed is what let a
// month-old server run unnoticed (#471).
func resolveAndReport(ctx context.Context, cmd *cobra.Command, explicit, name string) (string, error) {
	path, err := resolveBinary(explicit, name)
	if err != nil {
		return "", err
	}
	if werr := reportCompanionBinary(ctx, cmd, name, path); werr != nil {
		return "", werr
	}
	return path, nil
}

// reportCompanionBinary announces which companion binary was chosen and whether
// its version matches this CLI's.
//
// The path alone is not enough. When `leoflow lite` silently ran a month-old
// leoflow-server, every visible signal — the banner, /readyz, the logs — looked
// correct, and the mismatch was found only by hashing the running process
// afterwards. The trio is co-versioned (ADR 0028), so a disagreement is never
// intentional and is worth interrupting for.
//
// Best-effort: a binary that will not report its version still runs. Refusing to
// start over an unreadable version string would turn a diagnostic into an
// outage, and the resolution order already makes the mismatch rare.
func reportCompanionBinary(ctx context.Context, cmd *cobra.Command, kind, path string) error {
	got := companionVersion(ctx, path)
	mine := version.Get().Version
	var werr error
	switch {
	case got == "":
		_, werr = fmt.Fprintf(cmd.ErrOrStderr(), "  %s: %s (version unavailable)\n", kind, path)
	case mine != "" && got != mine:
		_, werr = fmt.Fprintf(cmd.ErrOrStderr(),
			"  %s: %s\n  WARNING: %s reports %s but this CLI is %s. They ship together and are\n"+
				"  meant to match; a mismatch usually means an older copy was found first.\n"+
				"  Pass --%s to pin the one you want.\n",
			kind, path, kind, got, mine, kind)
	default:
		_, werr = fmt.Fprintf(cmd.ErrOrStderr(), "  %s: %s (%s)\n", kind, path, got)
	}
	return werr
}

// companionVersion asks a binary for its version, returning "" when it cannot
// be determined. Bounded so a wedged binary cannot hang startup.
//
// The version check is best-effort and must never take the CLI down with it: a
// nil parent context — which context.WithTimeout would panic on ("cannot create
// context from nil parent") — is treated as no context rather than a fatal
// error.
//
//nolint:contextcheck // the nil-ctx fallback below has no parent to inherit; a fresh root is the only safe choice, and the CLI must not panic over a best-effort version probe.
func companionVersion(ctx context.Context, path string) string {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, path, "version").Output() //nolint:gosec // path resolved by resolveBinary, not user input
	if err != nil {
		return ""
	}
	fields := strings.Fields(string(out))
	// Output shape: "<name> <version> (commit …, built …, go…)".
	if len(fields) >= 2 {
		return fields[1]
	}
	return ""
}

// resolveBinary locates a companion binary, preferring the copy that belongs
// with the running CLI over whatever happens to be installed.
//
// Order: an explicit --flag, then the directory holding this executable, then
// the installer's ~/.leoflow/bin, then PATH, then ./bin.
//
// PATH used to come first, which meant `leoflow lite` ran whatever
// leoflow-server was installed earliest — however old. A validation run against
// v0.1.2-rc.1 spent its first boot exercising a v0.1.0-rc.4 server that predated
// every feature under test, and the mismatch was invisible: the banner, /readyz
// and the logs all looked correct. The trio is co-versioned (ADR 0028), so the
// binary shipped beside the running CLI is the one it was built and tested
// against; an older copy earlier on PATH is never the intended answer (#471).
//
// PATH is still consulted, so anyone relying on it keeps working — it is simply
// no longer the first answer.
func resolveBinary(explicit, name string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	for _, dir := range companionDirs() {
		cand := filepath.Join(dir, name)
		if isExecutableFile(cand) {
			return cand, nil
		}
	}
	if p, err := exec.LookPath(name); err == nil {
		return p, nil
	}
	local := filepath.Join("bin", name)
	if isExecutableFile(local) {
		// Absolute, because the subprocess executor runs the agent with a different
		// working directory; a relative path would not resolve there.
		return filepath.Abs(local)
	}
	return "", fmt.Errorf("%s not found beside this binary, in ~/.leoflow/bin, on PATH, or in ./bin; "+
		"run `make build` or pass --%s", name, name)
}

// companionDirs lists the directories that hold binaries shipped WITH this one,
// most-specific first. Both are best-effort: a failure to resolve either simply
// falls through to PATH.
func companionDirs() []string {
	var dirs []string
	if exe, err := os.Executable(); err == nil {
		if resolved, rerr := filepath.EvalSymlinks(exe); rerr == nil {
			exe = resolved
		}
		dirs = append(dirs, filepath.Dir(exe))
	}
	if home, err := os.UserHomeDir(); err == nil {
		dirs = append(dirs, filepath.Join(home, ".leoflow", "bin"))
	}
	return dirs
}

// isExecutableFile reports whether path is a regular file with an execute bit.
// The directory check matters: `~/.leoflow/bin/leoflow-agent/` as a directory
// would otherwise satisfy a plain os.Stat and be handed to exec.
func isExecutableFile(path string) bool {
	fi, err := os.Stat(path)
	if err != nil || fi.IsDir() {
		return false
	}
	return fi.Mode().Perm()&0o111 != 0
}

// sharedServerEnv is the Lite control plane environment common to both executor
// modes: the isolated local database, the LITE edition marker, and a writable
// logs dir. When an admin hash is configured (by `leoflow setup`), Lite enforces
// real auth and bootstraps that admin; otherwise it falls back to the dev no-auth
// bypass.
//
// host is the bind address. It is honored only with real auth; a no-auth
// fallback is ALWAYS forced to loopback so an unauthenticated control plane can
// never be exposed to the network (resolveBindHost enforces this).
// liteEnvParams carries what the Lite server env needs. A struct rather than
// positional arguments because jwtSecret and secretKey are adjacent strings
// with entirely different jobs: swapping them compiles, boots, and silently
// encrypts every connection password with the token-signing secret. Naming the
// fields makes that swap impossible to write.
type liteEnvParams struct {
	host       string
	port       int
	adminHash  string
	adminEmail string
	jwtSecret  string
	// secretKey is the per-install connection-encryption key from config.yaml;
	// empty on a legacy install, which resolveLiteSecretKey maps to the
	// published constant.
	secretKey string
	// secretKeyPrevious is a decrypt-only predecessor recorded when the install
	// was migrated off the published constant; empty once nothing needs it.
	secretKeyPrevious string
}

func sharedServerEnv(p liteEnvParams) []string {
	host, port, adminHash, adminEmail, jwtSecret := p.host, p.port, p.adminHash, p.adminEmail, p.jwtSecret
	env := []string{
		fmt.Sprintf("LEOFLOW_SERVER_HTTP_ADDR=%s:%d", resolveBindHost(host, adminHash), port),
		"LEOFLOW_SERVER_GRPC_ADDR=" + devGRPCBindAddr(port),
		"LEOFLOW_SERVER_METRICS_ADDR=" + devMetricsBindAddr(port),
		// Lite has no OTLP collector locally; disabling the exporter avoids a noisy
		// "connection refused to :4317" every export interval. Prometheus metrics
		// (scraped, not pushed) stay on.
		"LEOFLOW_OBSERVABILITY_OTEL_ENABLED=false",
		// Per-user, under ~/.leoflow — NOT a shared /tmp path. A global
		// /tmp/leoflow-dev-logs is created by whoever runs Lite first and then
		// rejects every other user with "permission denied" (root vs non-root). The
		// user's own .leoflow dir never collides.
		"LEOFLOW_LOGS_DIR=" + filepath.Join(liteDevDir(), "logs"),
		"LEOFLOW_UI_INSTANCE_NAME=" + devInstanceName,
		"LEOFLOW_UI_EDITION=lite",
		// Dial the SPA's auto-refresh down hard for the inner-dev loop: a save
		// should feel near-instant on screen. Pro keeps the 30s production
		// default. User decision 2026-06-01: start at 1s for testing, raise to
		// 5s later if it proves too aggressive.
		"LEOFLOW_UI_AUTO_REFRESH_INTERVAL_SECONDS=1",
		"LEOFLOW_DATABASE_URL=" + devDSNs().database,
		// No LEOFLOW_REDIS_URL: Lite runs Redis-free — XCom on Postgres and an
		// in-process log tailer. The empty Redis URL is the signal the server uses
		// to select the embedded backends (ADR 0026).
		"LEOFLOW_AUTH_JWT_SECRET=" + resolveLiteJWTSecret(jwtSecret),
		// Lite is a local, single-user tool: a 1-hour token (the server default)
		// expires mid-session and silently bounces the user to a re-login they did
		// not ask for. Mint 30-day sessions so signing in is a once-a-month event,
		// not an hourly tax.
		fmt.Sprintf("LEOFLOW_AUTH_JWT_TOKEN_TTL_SECONDS=%d", liteTokenTTLSeconds),
		// A local single-user tool should not lock you out for fat-fingering the
		// password a few times (only failures count, but the production default of
		// 5/min is still tight here). Be generous.
		fmt.Sprintf("LEOFLOW_AUTH_LOGIN_RATE_LIMIT_PER_MINUTE=%d", liteLoginRateLimit),
		// The per-install key, plus any decrypt-only predecessor, as one
		// comma-separated list (#486). The published constant appears ONLY for an
		// install that still has rows under it: a fresh install never wrote a
		// byte with it and must not accept it as a valid key.
		"LEOFLOW_SECRET_KEY=" + liteSecretKeyList(p.secretKey, p.secretKeyPrevious),
		"LEOFLOW_AGENT_ALLOW_INSECURE_SECRETS=true",
	}
	if adminHash != "" {
		// Real auth: bootstrap the admin from the hash; no bypass.
		return append(env,
			"LEOFLOW_BOOTSTRAP_PASSWORD_HASH="+adminHash,
			"LEOFLOW_BOOTSTRAP_EMAIL="+adminEmail,
		)
	}
	// No admin configured: dev no-auth fallback (runDev warns; loopback-bound).
	return append(env, "LEOFLOW_AUTH_DEV_NO_AUTH=true")
}

// resolveBindHost returns the address the control plane binds to. A non-loopback
// host (e.g. 0.0.0.0 for internal-network access) is honored only when real auth
// is configured; without an admin (no-auth fallback) it is forced to loopback,
// so an unauthenticated control plane is never exposed beyond the machine.
func resolveBindHost(host, adminHash string) string {
	if host == "" {
		host = "127.0.0.1"
	}
	if adminHash == "" {
		return "127.0.0.1"
	}
	return host
}

// liteEditorEnv enables the Lite web editor (ADR 0025) for the launched server:
// the workspace it edits (the watched project dir) and the directory holding the
// Monaco bundle that `leoflow setup` fetched. Both executors get it — the editor
// is orthogonal to execution.
func liteEditorEnv(workspaceDir, leoflowRoot string) []string {
	return []string{
		"LEOFLOW_UI_WORKSPACE=" + workspaceDir,
		"LEOFLOW_UI_MONACO_DIR=" + setup.MonacoDir(leoflowRoot),
	}
}

// applyLiteConfigDefaults loads ~/.leoflow/config.yaml and applies the recorded
// executor/port to o, unless those flags were set on the command line.
func applyLiteConfigDefaults(cmd *cobra.Command, o *devOptions) {
	c, err := config.Load(configFilePath(cmd), nil)
	if err != nil {
		return
	}
	mergeLiteDefaults(o, c, cmd.Flags().Changed("executor"), cmd.Flags().Changed("port"))
}

// mergeLiteDefaults applies the executor/port from config (written by
// `leoflow setup`) when the corresponding flag was not set on the command line.
func mergeLiteDefaults(o *devOptions, c *config.Config, executorSet, portSet bool) {
	if c == nil {
		return
	}
	if !executorSet && c.LiteExecutor != "" {
		o.executor = c.LiteExecutor
	}
	if !portSet && c.LitePort != 0 {
		o.port = c.LitePort
	}
}

// resolveLiteAdmin loads the configured admin credential and the two
// per-install secrets, warning when no admin is set (Lite then falls back to
// no-auth). It returns a struct rather than four strings because two of them
// are secrets with different jobs, and a swapped pair would compile.
func resolveLiteAdmin(cmd *cobra.Command, out io.Writer) liteEnvParams {
	p := loadLiteAdmin(cmd)
	if p.adminHash == "" {
		devPrintln(out, "  WARNING: no admin configured — run `leoflow setup`. Falling back to no-auth (local only, insecure).")
	}
	return p
}

// loadLiteAdmin reads the Lite admin credential the setup wizard persisted (hash
// only) and the per-install JWT secret (rotated by `leoflow setup` so a reinstall
// invalidates the prior install's tokens — #121) from ~/.leoflow/config.yaml.
// Returns an empty hash when no admin is configured.
func loadLiteAdmin(cmd *cobra.Command) liteEnvParams {
	c, err := config.Load(configFilePath(cmd), nil)
	if err != nil || c == nil {
		return liteEnvParams{}
	}
	email := c.AdminEmail
	if email == "" {
		email = "admin@leoflow.local"
	}
	return liteEnvParams{
		adminHash:  c.AdminPasswordHash,
		adminEmail: email,
		jwtSecret:  c.JWTSecret,
		secretKey:  c.SecretKey,

		secretKeyPrevious: c.SecretKeyPrevious,
	}
}

// subprocessServerEnv adds the subprocess-executor settings: the agent binary,
// the project workdir (so dag.py imports), the venv Python (boot fallback when
// the per-DAG venv lookup misses), the per-DAG venvs root (LEOFLOW_LITE_VENVS_ROOT
// — the subprocess executor consults <root>/<dag_id>/bin/python to override the
// fallback per task), and a dialable control-plane address (the server binds
// 0.0.0.0, which is not a dial target).
func subprocessServerEnv(p liteEnvParams, agentBin, workDir, venvPython, venvsRoot string) []string {
	return append(sharedServerEnv(p),
		"LEOFLOW_EXECUTOR_TYPE=subprocess",
		"LEOFLOW_EXECUTOR_AGENT_PATH="+agentBin,
		"LEOFLOW_EXECUTOR_AGENT_CONTROL_PLANE_ADDR=127.0.0.1"+devGRPCBindAddr(p.port),
		"LEOFLOW_EXECUTOR_SUBPROCESS_WORKDIR="+workDir,
		"LEOFLOW_PYTHON="+venvPython,
		"LEOFLOW_LITE_VENVS_ROOT="+venvsRoot,
	)
}

// clusterServerEnv adds the Kubernetes-executor settings: the isolated dev
// cluster's kubeconfig (so the control plane targets leoflow-dev, never the
// product cluster) and the host address task pods dial back for gRPC.
func clusterServerEnv(p liteEnvParams, kubeconfig string) []string {
	return append(sharedServerEnv(p),
		"LEOFLOW_EXECUTOR_TYPE=kubernetes",
		"KUBECONFIG="+kubeconfig,
		"LEOFLOW_EXECUTOR_AGENT_CONTROL_PLANE_ADDR="+devHostGRPCAddr(p.port),
		// The dev k3d cluster's local-path provisioner rejects RWX; it is
		// single-node, so RWO is sufficient for a run's sequential pods (ADR 0022).
		"LEOFLOW_EXECUTOR_DEFAULTS_STAGING_ACCESS_MODE=ReadWriteOnce",
	)
}

// startDevServer launches the control plane with the given environment and
// returns once it has started.
func startDevServer(ctx context.Context, cmd *cobra.Command, serverBin string, env []string) (*exec.Cmd, error) {
	devPrintln(cmd.OutOrStdout(), "▸ starting control plane …")
	srv := exec.CommandContext(ctx, serverBin) //nolint:gosec // serverBin is operator-resolved on the dev CLI
	srv.Env = append(os.Environ(), env...)
	srv.Stdout, srv.Stderr = cmd.OutOrStdout(), cmd.ErrOrStderr()
	if err := srv.Start(); err != nil {
		return nil, fmt.Errorf("starting control plane: %w", err)
	}
	return srv, nil
}

// waitForReady polls the control plane's /readyz until it is up or the timeout
// (or context) elapses.
func waitForReady(ctx context.Context, baseURL string) error {
	deadline := time.Now().Add(devReadyTimeout)
	for time.Now().Before(deadline) {
		if devReadyOnce(ctx, baseURL) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
	return errors.New("control plane did not become ready in time")
}

// devReadyOnce reports whether /readyz currently returns 200.
func devReadyOnce(ctx context.Context, baseURL string) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/readyz", http.NoBody)
	if err != nil {
		return false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	defer func() { _ = resp.Body.Close() }() //nolint:errcheck // health poll; close error is irrelevant
	return resp.StatusCode == http.StatusOK
}

// devWatchLoop runs reload once, then again on every save of a watched file
// until the context is canceled (Ctrl-C). reload encapsulates the mode-specific
// build/register step. The set of watched files is re-derived from the
// workspace on every tick so a new project subdir added at runtime is picked
// up without restart.
func devWatchLoop(ctx context.Context, cmd *cobra.Command, ws *WorkspaceSpec, reload func() error, deleteDag func(dagID string) error, bootReconcile func() map[string]struct{}) error {
	if rerr := reload(); rerr != nil {
		devPrintf(cmd.ErrOrStderr(), "✗ %v\n", rerr)
	}
	watched := workspaceWatchPaths(ws)
	snap := projectMtimes(watched)
	// lastSeenDagIDs holds the set of DAG IDs the watcher registered in the
	// previous tick. On every tick we set-diff against the current workspace
	// scan; any DAG in lastSeen but not in current was deleted from disk and
	// must be deregistered (issue #345). Seeded from the initial reload by
	// re-resolving the workspace one more time below.
	lastSeenDagIDs := projectDagIDs(ws)
	// Boot-time self-heal (#404): deregister ghost DAGs (registered but absent on
	// disk) and clear stale import errors so a reused metadata DB or a previous
	// workspace doesn't leave un-removable entries in the UI. The closure is built
	// fail-safe by the caller; nil in tests.
	if bootReconcile != nil {
		lastSeenDagIDs = bootReconcile()
	}
	devPrintf(cmd.OutOrStdout(), "👀 watching %s — edit and save to reload (Ctrl-C to stop)\n", ws.Path)
	ticker := time.NewTicker(devPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			devPrintln(cmd.OutOrStdout(), "\nstopping dev environment …")
			return nil
		case <-ticker.C:
			// Re-resolve the workspace each tick so newly-added or removed
			// projects are detected. Discovery errors (e.g. a fresh
			// duplicate-dag_id collision) surface here so the user sees them
			// immediately.
			curWs, derr := ResolveWorkspace(ws.Path)
			if derr != nil {
				devPrintf(cmd.ErrOrStderr(), "✗ workspace discovery: %v\n", derr)
				continue
			}
			watched = workspaceWatchPaths(curWs)
			cur := projectMtimes(watched)
			if !mtimesChanged(snap, cur) {
				continue
			}
			snap = cur
			// Set-diff lastSeen vs current: any DAG present last tick but
			// missing now was removed from disk and needs to be deregistered
			// from the control plane (issue #345). Logged to stderr so the
			// user sees what the watcher did.
			if deleteDag != nil {
				logf := func(format string, args ...any) {
					devPrintf(cmd.OutOrStdout(), format+"\n", args...)
				}
				lastSeenDagIDs = removeMissingDags(lastSeenDagIDs, projectDagIDs(curWs), deleteDag, logf)
			}
			devPrintf(cmd.OutOrStdout(), "[%s] change detected → reloading …\n", time.Now().Format("15:04:05"))
			// Time the reload — a broken DAG's reload should still finish in
			// ~1s. Anything multi-second is a red flag (parser hang, lock
			// contention with the running control plane, slow registration
			// retry). Log it so the user can grep for slow reloads when they
			// notice the UI freezing on save (#217 diagnostic).
			reloadStart := time.Now()
			rerr := reload()
			devPrintf(cmd.OutOrStdout(), "[%s] reload finished in %s\n", time.Now().Format("15:04:05"), time.Since(reloadStart).Truncate(time.Millisecond))
			if rerr != nil {
				devPrintf(cmd.ErrOrStderr(), "✗ %v\n", rerr)
			}
		}
	}
}

// projectDagIDs returns the set of DAG IDs the workspace currently exposes.
// It powers the watcher's "what disappeared since last tick" set-diff
// (issue #345). Returns a map so set-membership checks stay O(1) and the
// pure-function removeMissingDags can mutate it without aliasing.
func projectDagIDs(ws *WorkspaceSpec) map[string]struct{} {
	if ws == nil {
		return map[string]struct{}{}
	}
	out := make(map[string]struct{}, len(ws.Projects))
	for _, p := range ws.Projects {
		if p.Config != nil && p.Config.DagID != "" {
			out[p.Config.DagID] = struct{}{}
		}
	}
	return out
}

// workspaceWatchPaths returns the paths the mtime poller should track for a
// given workspace: every project's leoflow.yaml (when present) and dag.py,
// PLUS the workspace root itself so a new subdir appearing nudges the mtime
// signal. Lite re-discovers projects on every reload, so a new subdir's files
// will start being watched at the next tick after it's noticed.
func workspaceWatchPaths(ws *WorkspaceSpec) []string {
	if ws == nil {
		return nil
	}
	paths := ws.WatchedPaths()
	// Workspace root mtime catches "a new subdir was just created" before any
	// of its files exist — fsnotify would be cleaner, but mtime-poll is the
	// existing primitive.
	return append(paths, ws.Path)
}

// devCompileAndRegister compiles the project (parser + overlay + guardrails),
// optionally runs afterCompile (e.g. import the built image into the cluster),
// and registers the resulting dag.json with the running control plane. Each call
// stamps a fresh dev version so a hot reload never collides with the previous
// registration (dag_versions is unique per dag_id + version).
func devCompileAndRegister(ctx context.Context, cmd *cobra.Command, dir string, opts compileOptions, token string, afterCompile func() error, uiURL string) error {
	opts.output = filepath.Join(liteDevDir(), "dag.json")
	opts.dagVersion = fmt.Sprintf("dev-%d", time.Now().UnixNano())
	//nolint:contextcheck // runCompile derives its context from cmd; ctx here is used for registration.
	if cerr := runCompile(cmd, dir, opts); cerr != nil {
		return cerr
	}
	if afterCompile != nil {
		if aerr := afterCompile(); aerr != nil {
			return aerr
		}
	}
	data, err := os.ReadFile(opts.output) //nolint:gosec // path is leoflow-controlled under ~/.leoflow
	if err != nil {
		return fmt.Errorf("reading compiled dag.json: %w", err)
	}
	var spec domain.DAGSpec
	if jerr := json.Unmarshal(data, &spec); jerr != nil {
		return fmt.Errorf("parsing compiled dag.json: %w", jerr)
	}
	status, body, perr := pushVersion(ctx, uiURL, token, spec.DagID, data)
	if perr != nil {
		return perr
	}
	if status >= http.StatusMultipleChoices {
		return fmt.Errorf("control plane returned %d registering %q: %s", status, spec.DagID, body)
	}
	devPrintf(cmd.OutOrStdout(), "✓ registered %q\n", spec.DagID)
	return nil
}

// devCompileAndRegisterFn is the per-project compile-and-register entry point
// used by devCompileAndRegisterAll. It is a package-level variable so tests
// can substitute a mock that records calls without spinning up the parser +
// HTTP server (the same pattern devRun / devOutput follow). Production code
// keeps the default; do NOT swap it outside _test.go.
var devCompileAndRegisterFn = devCompileAndRegister

// devCompileAndRegisterAll compiles + registers every project in the workspace.
// Each project is processed independently — one bad DAG does not stop the
// others — and per-project errors are reported through the Airflow import-
// errors UI (keyed by the project's dag.py path) so the SPA banner names the
// culprit. The pre-compile log line names the resolved config source for each
// DAG (yaml path or "auto-defaults: <subdir>") so "which config did it use?"
// is greppable.
//
// Returns nil when every project compiled OR when at least one succeeded
// (transient single-DAG failures don't block the loop). Returns the last
// error when ALL projects failed.
func devCompileAndRegisterAll(ctx context.Context, cmd *cobra.Command, ws *WorkspaceSpec, baseOpts compileOptions, token string, afterCompile func() error, uiURL string) error {
	if ws == nil || len(ws.Projects) == 0 {
		path := "<nil>"
		if ws != nil {
			path = ws.Path
		}
		devPrintf(cmd.OutOrStdout(), "▸ no DAGs in workspace %s — drop a `dag.py` into a subdirectory and save\n", path)
		return nil
	}
	var lastErr error
	successes := 0
	for _, p := range ws.Projects {
		cfgSource := p.ConfigPath
		if cfgSource == "" {
			cfgSource = "auto-defaults: " + filepath.Base(p.Path)
		}
		devPrintf(cmd.OutOrStdout(), "▸ compiling %q (config: %s)\n", p.DagID, cfgSource)
		dagPyPath := filepath.Join(p.Path, p.Config.DagSource)
		if err := devCompileAndRegisterFn(ctx, cmd, p.Path, baseOpts, token, afterCompile, uiURL); err != nil {
			devPrintf(cmd.ErrOrStderr(), "✗ %s: %v\n", p.DagID, err)
			_ = reportImportError(ctx, uiURL, token, dagPyPath, err.Error()) //nolint:errcheck // best-effort UI hint; user already sees the terminal error
			lastErr = err
			continue
		}
		_ = clearImportError(ctx, uiURL, token, dagPyPath) //nolint:errcheck // best-effort: clears the banner on a good compile
		successes++
	}
	if successes == 0 {
		return lastErr
	}
	return nil
}

// devReportingReload wraps a reload so a failed compile is published to the
// control plane as an import error — lighting the Airflow home's native "Import
// Errors" banner so the failure is visible in the UI, not only the terminal — and
// a good compile clears it. Reporting is best-effort and never masks the reload's
// own result.
//
// Multi-DAG callers should use devCompileAndRegisterAll instead, which reports
// import errors per-project. devReportingReload is the single-project path
// (cluster mode for v1).
func devReportingReload(ctx context.Context, reload func() error, serverURL, token, filename string) func() error {
	return func() error {
		if err := reload(); err != nil {
			_ = reportImportError(ctx, serverURL, token, filename, err.Error()) //nolint:errcheck // best-effort UI hint; the reload error below is authoritative
			return err
		}
		_ = clearImportError(ctx, serverURL, token, filename) //nolint:errcheck // best-effort: clears the banner on a good compile
		return nil
	}
}

// reportImportError records a failed compile as an Airflow import error so the
// UI home banner surfaces it (keyed by filename; replaces any previous error).
func reportImportError(ctx context.Context, serverURL, token, filename, stack string) error {
	body, err := json.Marshal(map[string]string{"filename": filename, "stack_trace": stack, "bundle_name": "leoflow"})
	if err != nil {
		return fmt.Errorf("encoding import error: %w", err)
	}
	return devImportErrorRequest(ctx, http.MethodPut, strings.TrimRight(serverURL, "/")+"/api/v2/importErrors", token, body)
}

// clearImportError removes any recorded import error for a file (a good re-import).
func clearImportError(ctx context.Context, serverURL, token, filename string) error {
	u := strings.TrimRight(serverURL, "/") + "/api/v2/importErrors?filename=" + url.QueryEscape(filename)
	return devImportErrorRequest(ctx, http.MethodDelete, u, token, nil)
}

// devImportErrorRequest issues an authenticated import-error write to the control plane.
func devImportErrorRequest(ctx context.Context, method, reqURL, token string, body []byte) error {
	var r io.Reader
	if body != nil {
		r = strings.NewReader(string(body))
	}
	req, err := http.NewRequestWithContext(ctx, method, reqURL, r)
	if err != nil {
		return fmt.Errorf("building request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return fmt.Errorf("calling %s: %w", reqURL, err)
	}
	defer func() { _ = resp.Body.Close() }() //nolint:errcheck // response body discarded
	if resp.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("import error endpoint returned %d", resp.StatusCode)
	}
	return nil
}

// parsePythonMinor turns a `python_version` field ("3.13") into its minor. The
// field is an authoring-surface value that decides which interpreter runs the
// task, so anything that is not exactly `3.<minor>` is refused rather than
// coerced — a coerced version is how dev and the cluster end up disagreeing
// without anyone being told (#1092).
func parsePythonMinor(v string) (int, error) {
	major, minor, ok := strings.Cut(v, ".")
	if !ok || major != "3" || minor == "" || strings.Contains(minor, ".") {
		return 0, fmt.Errorf("python_version %q is not of the form 3.<minor> (e.g. \"3.13\")", v)
	}
	n, err := strconv.Atoi(minor)
	if err != nil {
		return 0, fmt.Errorf("python_version %q is not of the form 3.<minor> (e.g. \"3.13\"): %w", v, err)
	}
	return n, nil
}

// resolvePythonFor resolves the interpreter for a DECLARED python_version, and
// differs from resolvePython3 on the one point that caused #1092: the managed
// build is trusted by path when nothing was requested, and is version-CHECKED
// when something was. Trusting the path is what silently gave a project pinned
// to 3.13 a 3.11 venv, because the managed build's path ends in `python3.11`.
//
// Order: the managed build when it reports the requested minor, then
// `python3.<minor>`, then the shared candidate list, then bare `python3` — each
// accepted only on the version it REPORTS, never on its name. When nothing
// matches it returns an error naming the version asked for and the versions
// found, because "install 3.13" is the only useful next step and the caller
// cannot phrase it.
func resolvePythonFor(ctx context.Context, want int, managed string,
	lookPath func(string) (string, error),
	runVersion func(context.Context, string) (int, int, error),
) (string, error) {
	matches := func(p string) bool {
		major, minor, err := runVersion(ctx, p)
		return err == nil && major == 3 && minor == want
	}
	if managed != "" {
		if _, err := os.Stat(managed); err == nil && matches(managed) {
			return managed, nil
		}
	}
	// `python3.<want>` first: on a host with several minors it is the only name
	// that cannot resolve to the wrong one.
	names := append([]string{fmt.Sprintf("python3.%d", want)}, setup.PythonCandidates()...)
	names = append(names, "python3")
	seen := map[string]bool{}
	var found []string
	for _, name := range names {
		if seen[name] {
			continue
		}
		seen[name] = true
		p, err := lookPath(name)
		if err != nil {
			continue
		}
		major, minor, verr := runVersion(ctx, p)
		if verr != nil {
			continue
		}
		if major == 3 && minor == want {
			return p, nil
		}
		found = append(found, fmt.Sprintf("%d.%d", major, minor))
	}
	if len(found) > 0 {
		return "", fmt.Errorf("this project declares python_version 3.%d, but the only interpreters on this host report %s; %s, or change python_version to a version you have",
			want, strings.Join(dedupe(found), ", "), installHint(want))
	}
	return "", fmt.Errorf("this project declares python_version 3.%d and no Python interpreter was found; %s", want, installHint(want))
}

// installHint names the action that actually produces the requested minor.
// `leoflow setup` provisions exactly one version — the managed build's pinned
// minor — so offering it for any other minor sends the user around a loop that
// cannot end: setup succeeds, the version they asked for is still missing, and
// the same error comes back.
func installHint(want int) string {
	if want == minPythonMinor {
		return fmt.Sprintf("run `leoflow setup` to provision a managed CPython 3.%d", want)
	}
	return fmt.Sprintf("install Python 3.%d", want)
}

// dedupe keeps the first occurrence of each entry, so an error that lists the
// versions found does not repeat "3.11" once per alias pointing at it.
func dedupe(in []string) []string {
	seen := map[string]bool{}
	out := in[:0]
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// pyvenvMinor reports the Python minor a venv was BUILT with, read from its
// pyvenv.cfg. ensureDagVenv short-circuits on the interpreter file existing, so
// without this a venv created before the project changed `python_version` keeps
// serving the old interpreter forever and the declared version would apply only
// to venvs that never existed.
//
// A venv whose version cannot be determined reports ok=false rather than 0, so
// an unreadable cfg leaves the venv alone instead of rebuilding it every boot.
func pyvenvMinor(dir string) (int, bool) {
	b, err := os.ReadFile(filepath.Join(dir, "pyvenv.cfg")) //nolint:gosec // dir is a leoflow-managed venv path
	if err != nil {
		return 0, false
	}
	var fallback string
	for _, line := range strings.Split(string(b), "\n") {
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		switch strings.TrimSpace(key) {
		case "version":
			if m, err := minorOf(strings.TrimSpace(val)); err == nil {
				return m, true
			}
		case "version_info":
			fallback = strings.TrimSpace(val)
		}
	}
	if fallback != "" {
		if m, err := minorOf(fallback); err == nil {
			return m, true
		}
	}
	return 0, false
}

// minorOf extracts the minor from a dotted version such as "3.12.0" or
// "3.13.1.final.0".
func minorOf(v string) (int, error) {
	parts := strings.Split(v, ".")
	if len(parts) < 2 {
		return 0, fmt.Errorf("unrecognized version %q", v)
	}
	return strconv.Atoi(parts[1])
}

// devEnforcedPythonVersion reports the python_version a dev venv must be built
// on, or "" to keep the historical precedence (managed build, else any host
// python3 >= 3.11).
//
// Enforcing this field is right only when it says something. It says nothing in
// three cases, and refusing to boot in any of them would break a working dev
// loop over a choice the user never made:
//
//   - ApplyDefaults fills it with "3.11" for every config that omits it — and
//     discovery defaults EVERY project, including directories with no
//     leoflow.yaml at all. Enforcing there turns "no python3.11 on this host"
//     from a silent, working fallback into a hard stop, on a machine where
//     nothing is actually inconsistent: the image is py3.11 because the same
//     default chose it, so dev and the cluster already agree.
//   - An explicit build.base_image wins over python_version when the image is
//     resolved (resolveBaseImage), so the field has no bearing on what the
//     cluster runs and cannot be skewed against it.
//   - A deprecated version is one we are withdrawing. Refusing tells the author
//     to install an interpreter that is on its way out, to keep using a version
//     that is on its way out. Warn and fall back, which is the nudge that
//     matches the direction of travel.
func devEnforcedPythonVersion(cmd *cobra.Command, cfg *domain.LeoflowConfig) string {
	if cfg == nil || cfg.PythonVersionDefaulted || cfg.BaseImage != "" {
		return ""
	}
	if _, deprecated := domain.DeprecatedPythonVersion(cfg.PythonVersion); deprecated {
		devPrintf(cmd.OutOrStdout(), "▸ python_version %s is deprecated; building this venv on the default interpreter instead — migrate to a supported version\n", cfg.PythonVersion)
		return ""
	}
	return cfg.PythonVersion
}

// countRegisteredDags reports how many DAGs the local database already holds.
// Called before this session registers anything, so the number is exactly what
// earlier runs left behind (#1104).
//
// It reads Postgres directly rather than going through the API: at banner time
// the admin token has not been minted yet, and a read-only count has no business
// depending on the auth surface. A failure returns -1 — "unknown", which the
// banner reports as nothing — because a state line that guesses is worse than no
// state line: it would assert a clean slate on a lookup that never ran.
func countRegisteredDags(ctx context.Context) int {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, devDSNs().database)
	if err != nil {
		return -1
	}
	defer func() { _ = conn.Close(ctx) }() //nolint:errcheck // best-effort close of a short-lived read connection
	var n int
	if qerr := conn.QueryRow(ctx, "SELECT count(*) FROM dags").Scan(&n); qerr != nil {
		return -1
	}
	return n
}

// provisionDevDatabase brings the Lite dev database up, dropping it first when
// fresh is set.
//
// State under ~/.leoflow/dev otherwise outlives every session: a DAG registered
// during an old spike stays registered, keeps being scheduled, and fails inside
// a run that has nothing to do with it (#1104). The drop is the same one
// `leoflow db reset` performs, and it is announced before it happens — it takes
// registered DAGs, runs and history with it.
func provisionDevDatabase(ctx context.Context, cmd *cobra.Command, out io.Writer, fresh bool) error {
	if fresh {
		devPrintln(out, "▸ --fresh: dropping the local dev database (registered DAGs, runs and history go with it)")
		if err := dropDevDatabase(ctx, cmd); err != nil {
			return err
		}
	}
	return ensureDevDatabase(ctx, cmd)
}
