package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/config"
)

// devEnv is the fixed local-development environment label and its defaults. The
// subprocess executor runs user code unsandboxed, so `dexaflow lite` is dev-only
// and shouts that fact in the banner and the UI navbar (ADR 0023).
const (
	devInstanceName = "Dexaflow Lite"
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
	devAdminUser       = auth.DefaultAdminEmail
	devPollInterval    = 750 * time.Millisecond
	devReadyTimeout    = 30 * time.Second
	// Dev uses ports distinct from the demo/production defaults (8080/9090/9091)
	// so a `dexaflow lite` and a demo control plane can run side by side without
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
	// Resolved from ~/.dexaflow/config.yaml (written by `dexaflow setup`), not flags.
	adminHash  string
	adminEmail string
	// jwtSecret is the per-install Lite JWT signing secret loaded from
	// ~/.dexaflow/config.yaml; empty on a legacy install (resolveLiteJWTSecret
	// then falls back to devJWTSecret with a one-shot warning).
	jwtSecret string
	// secretKey is the per-install connection-encryption key, read from
	// ~/.dexaflow/config.yaml alone (never the environment) after the
	// key-migration lock is held (#486, ADR 0065).
	secretKey string
	// secretKeyPrevious is the comma-separated decrypt-only predecessor list:
	// recorded by an unfinished `dexaflow lite migrate-key`, or set by hand.
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

// resolveLiteProject picks the workspace dir for `dexaflow lite`. With an
// explicit path argument it uses that — must exist as a directory; the path
// can be either a single-DAG project (back-compat: root holds dexaflow.yaml +
// dag.py) or a multi-DAG workspace (subdirs each with their own pair). With
// no argument it uses the configured workspace (the directory `dexaflow setup`
// chose). Scaffolding of an empty workspace is the caller's responsibility —
// it now creates a subdir (`<workspace>/hello/`), not a root project.
func resolveLiteProject(cmd *cobra.Command, args []string) (string, error) {
	if len(args) == 1 {
		p := args[0]
		// An explicit argument must be an existing directory. Without this check a
		// typo like `dexaflow lite uninstall` was swallowed as a project path and
		// failed later with a cryptic "open uninstall/dexaflow.yaml". Fail clearly.
		info, err := os.Stat(p)
		if err != nil || !info.IsDir() {
			return "", fmt.Errorf("workspace path %q does not exist or is not a directory.\n"+
				"  - run `dexaflow lite` with no argument to use your workspace (%s)\n"+
				"  - run `dexaflow init %s` to create a project there\n"+
				"  - for other actions see `dexaflow --help` (e.g. `dexaflow uninstall`)",
				p, defaultWorkspace(cmd), p)
		}
		return p, nil
	}
	dir := liteWorkspace(cmd)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", fmt.Errorf("creating workspace %s: %w", dir, err)
	}
	return dir, nil
}

// liteWorkspace is the workspace `dexaflow lite` runs with no argument: the one
// around the current directory when a dexaflow.workspace.yaml is found, else
// defaultWorkspace.
func liteWorkspace(cmd *cobra.Command) string {
	if ws, err := findWorkspaceFromCwd(); err == nil && ws != nil {
		return ws.Root
	}
	return defaultWorkspace(cmd)
}

// defaultWorkspace returns the workspace from config (set by `dexaflow setup`),
// falling back to ~/dexaflow (or the ~/leoflow of an install from before the
// rename, see defaultWorkspaceIn).
func defaultWorkspace(cmd *cobra.Command) string {
	if c, err := config.Load(configFilePath(cmd), nil); err == nil && c.Workspace != "" {
		return c.Workspace
	}
	if home, err := os.UserHomeDir(); err == nil {
		return defaultWorkspaceIn(home)
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
			"(an unauthenticated control plane is never exposed). Run `dexaflow setup` to enable a login first.\n", host)
		return
	}
	devPrintf(out, "  ⚠ SECURITY: binding to %s exposes Dexaflow Lite on your network. Lite uses a short "+
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
	devPrintf(out, "\n  ✓ Dexaflow Lite is ready\n")
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

// friendlyHost is the convenience hostname Dexaflow suggests for the local UI.
const friendlyHost = "leoflow.local"

// friendlyResolves reports whether leoflow.local resolves on this machine, so the
// ready banner only offers it when it actually works (a hosts entry or mDNS).
func friendlyResolves() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	addrs, err := net.DefaultResolver.LookupHost(ctx, friendlyHost)
	return err == nil && len(addrs) > 0
}

func newLiteCommand() *cobra.Command {
	var o devOptions
	cmd := &cobra.Command{
		Use:     "lite [path]",
		Aliases: []string{"dev"},
		Short:   "Run Dexaflow Lite locally with hot reload.",
		Long: "lite is the Dexaflow Lite edition: it brings up local dependencies and runs the " +
			"control plane against an isolated local database, registers the DAG, and hot-reloads " +
			"on every save. The UI is served on a Lite port (default 8088, --port), marked with a " +
			("LITE badge, and behind a login (the admin created by `dexaflow setup`, which prints the\n" +
				"generated password ONCE — `dexaflow lite reset-password` sets a new one if it is gone).\n\nExecutor ") +
			"(--executor): 'subprocess' runs tasks unsandboxed on the host with no image build — " +
			"the fast inner loop, best for local use. 'k8s' runs real pod-per-task on a dedicated, " +
			"isolated k3d mini-cluster (leoflow-dev) — highest fidelity, best for development; it " +
			"rebuilds the DAG image on each change.\n\n('dexaflow dev' remains as a deprecated alias.)",
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
	cmd.Flags().StringVar(&o.composeFile, "compose", "", "compose file for the local Postgres (default: a managed one under ~/.dexaflow, materialized on first run)")
	cmd.Flags().StringVar(&o.runtimeSrc, "runtime-src", "runtime/python", "source of the leoflow_runtime package installed into the dev venv")
	cmd.Flags().StringVar(&o.serverBin, "server-bin", "", "dexaflow-server binary (default: PATH, then ./bin)")
	cmd.Flags().StringVar(&o.agentBin, "agent-bin", "", "dexaflow-agent binary (default: PATH, then ./bin)")
	cmd.Flags().BoolVar(&o.noUp, "no-up", false, "skip docker compose (Lite's own Postgres is already running; this does not point Lite at another database); the dev DB + venv are still provisioned")
	cmd.Flags().BoolVar(&o.fresh, "fresh", false, "drop the local dev database first, so the session starts with nothing registered (DESTRUCTIVE: registered DAGs, runs and history)")
	cmd.Flags().StringVar(&o.postgres, "postgres", datastoreAuto, "Postgres backend: 'auto' (default; the Docker postgres:16 when the Docker daemon answers and has the Compose v2 plugin, else a managed relocatable PG under ~/.dexaflow on a Unix socket, no Docker), 'docker', or 'managed' (best on full distros; minimal hosts may lack its system libs)")
	cmd.AddCommand(newLiteProvisionCommand())
	cmd.AddCommand(newResetPasswordCommand())
	cmd.AddCommand(newForgetCommand())
	cmd.AddCommand(newBackupCommand())
	cmd.AddCommand(newRestoreCommand())
	cmd.AddCommand(newMigrateKeyCommand())
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

// liteBanner renders a high-visibility Lite-environment banner so a developer
// never mistakes the local loop for production. url is the served UI address.
func liteBanner(uiURL string) string {
	line := fmt.Sprintf(" DEXAFLOW LITE (local) %s ", uiURL)
	bar := ""
	for range line {
		bar += "─"
	}
	return fmt.Sprintf("%s╭%s╮%s\n%s│%s│%s\n%s╰%s╯%s",
		ansiLite, bar, ansiReset,
		ansiLite, line, ansiReset,
		ansiLite, bar, ansiReset)
}

// runDev orchestrates the all-in-one local loop: deps, control plane (subprocess
// executor), DAG registration, and hot reload on save.
func runDev(cmd *cobra.Command, dir string, o devOptions) error {
	out := cmd.OutOrStdout()
	ws, err := prepareWorkspace(cmd, out, dir)
	if err != nil {
		return err
	}
	// Apply the executor/port chosen in `dexaflow setup` (stored in config) as the
	// defaults, unless overridden on the command line. This honors the wizard's
	// choice while keeping --executor/--port changeable per run.
	applyLiteConfigDefaults(cmd, &o)
	if o.port == 0 {
		o.port = devDefaultPort
	}
	uiURL := devURL(o.port)
	devPrintln(out, liteBanner(uiURL))

	// The admin login is provisioned by `dexaflow setup` (hash-only in config).
	// With it, Lite enforces real auth; without it, fall back to no-auth + warn.
	id := resolveLiteAdmin(cmd, out)
	o.adminHash, o.adminEmail, o.jwtSecret = id.adminHash, id.adminEmail, id.jwtSecret

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
	defer cleanupDeps() // stops managed Postgres on exit if this run started it; no-op for the Docker path
	noteForeignDatabaseURL(out, "Lite runs on its own database and ignores the variable")
	// Provision the database, take the key-migration lock and read the keys,
	// in that order and under the config lock (ADR 0065 section 3). The lock
	// session is held until this process exits.
	keyLock, keySt, kerr := acquireLiteKeyBoot(ctx, cmd, out, &o)
	if kerr != nil {
		return kerr
	}
	defer keyLock.close(ctx)
	home, herr := devHome()
	if herr != nil {
		return herr
	}
	serverBin, berr := resolveServerBin(cmdContext(cmd), cmd, o.serverBin)
	if berr != nil {
		return berr
	}

	// "auto" (the default) uses k3d when Docker is present, else the unsandboxed
	// subprocess executor so `dexaflow lite` still runs without Docker.
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
	ctx, serverExited := superviseServer(ctx, server)

	if werr := waitForReady(ctx, uiURL); werr != nil {
		return firstErr(serverExited(), werr)
	}
	removePreRestoreAfterBoot(out, keySt)
	login := liteLoginEmail(ctx, o.adminEmail, liteAdminState)
	announceReady(out, o.host, o.port, login, ws.Path, countRegisteredDags(ctx))
	// Mint an admin token in-process signed with the dev JWT secret; the control
	// plane validates it by signature + claims, so no login or seeded user is
	// needed. Re-minted per operation (signing is cheap) so a Lite left running
	// for hours never hits the token's expiry — hot-reload registration keeps
	// working instead of silently 401-ing after an hour (#407).
	logf := func(format string, args ...any) { devPrintf(cmd.OutOrStdout(), format+"\n", args...) }
	mintToken := func() string {
		tok, err := auth.MintUserToken(resolveLiteJWTSecret(o.jwtSecret), time.Hour, auth.User{
			ID: auth.DevTokenSubject, TenantID: "default", Email: devTokenEmail(login), Roles: []string{"admin"},
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
	// The loop ends when ctx does; only then can serverExited say why.
	lerr := devWatchLoop(ctx, cmd, ws, makeReload(mintToken), del, boot)
	return firstErr(serverExited(), lerr)
}

// firstErr returns the first non-nil error.
func firstErr(errs ...error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
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
// with user code running unsandboxed on the host. Each project gets its own
// venv built from its own `dependencies:` and python_version, so DAGs that pin
// different versions of the same package coexist in one workspace (#1679).
func devSubprocessSetup(ctx context.Context, cmd *cobra.Command, ws *WorkspaceSpec, o devOptions, home string) (env []string, makeReload func(func() string) func() error, err error) {
	agentBin, err := resolveAndReport(ctx, cmd, o.agentBin, "agent")
	if err != nil {
		return nil, nil, err
	}
	// Self-heal the extracted Python sources before anything references them.
	// On a binary-only install (no repo) resolveRuntimeSrc points the per-DAG
	// venv at ~/.dexaflow/pysrc/runtime/python; if the boot provisions that venv
	// before the sources are extracted, pip aborts with "does not exist" (#587).
	// Checksum-gated, so a warm install pays nothing.
	ensurePysrc(cmd)
	runtimeSrc := resolveRuntimeSrc(o.runtimeSrc, home)
	// Per-DAG venvs: every project gets its own ~/.dexaflow/dev/venvs/<dag_id>/.
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
			return fmt.Errorf("port %d is already in use (needed for %s); another Dexaflow Lite may be running — stop it, or pass --port to pick a free port", c.num, c.role)
		}
		_ = ln.Close() //nolint:errcheck // best-effort probe; closing frees the port for the real bind
	}
	return nil
}

// devHome returns the isolated dev state directory (~/.dexaflow/dev), created on
// demand. All dev state (the venv, etc.) lives here, never in the project.
func devHome() (string, error) {
	h, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolving home dir: %w", err)
	}
	d := filepath.Join(stateDirIn(h), "dev")
	if mkerr := os.MkdirAll(d, 0o750); mkerr != nil {
		return "", fmt.Errorf("creating dev home %s: %w", d, mkerr)
	}
	return d, nil
}

// liteDevDir is the per-user Lite scratch dir (~/.dexaflow/dev) for state that must
// never be shared between users: the compiled dag.json and task logs. A global
// /tmp path is owned by whoever ran Lite first and then denies every other user
// (the root-vs-non-root "permission denied" trap). Best-effort: it falls back to a
// temp dir only if the home cannot be resolved.
func liteDevDir() string {
	if h, err := os.UserHomeDir(); err == nil {
		return filepath.Join(stateDirIn(h), "dev")
	}
	return os.TempDir()
}

// startDevServer launches the control plane with the given environment and
// returns once it has started.
func startDevServer(ctx context.Context, cmd *cobra.Command, serverBin string, env []string) (*exec.Cmd, error) {
	devPrintln(cmd.OutOrStdout(), "▸ starting control plane …")
	srv := exec.CommandContext(ctx, serverBin) //nolint:gosec // serverBin is operator-resolved on the dev CLI
	srv.Env = liteServerEnviron(os.Environ(), env)
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
	resp, err := cliHTTPClient(0).Do(req)
	if err != nil {
		return false
	}
	defer func() { _ = resp.Body.Close() }() //nolint:errcheck // health poll; close error is irrelevant
	return resp.StatusCode == http.StatusOK
}

// liteLoginEmail returns the address the ready banner names as the login. The
// server resolves the default admin to an install's legacy admin@leoflow.local
// (auth.ResolveAdminEmail), and the banner resolves it the same way so it names
// the account that logs in. An empty address (no-auth) stays empty, and a
// failed lookup keeps the configured address rather than guess.
func liteLoginEmail(ctx context.Context, configured string, lookup auth.AdminLookup) string {
	if configured == "" {
		return ""
	}
	res, err := auth.ResolveAdminEmail(ctx, configured, lookup)
	if err != nil {
		return configured
	}
	return res.Email
}

// devTokenEmail is the email claim of the in-process dev token: the admin the
// banner names, or the default admin address under no-auth.
func devTokenEmail(login string) string {
	if login != "" {
		return login
	}
	return devAdminUser
}
