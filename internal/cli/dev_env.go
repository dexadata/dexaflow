package cli

import (
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"sync"

	"github.com/spf13/cobra"

	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/config"
	"github.com/dexadata/dexaflow/internal/setup"
)

// liteJWTFallbackOnce keeps the legacy-fallback warning to a single log line per
// process (resolveLiteJWTSecret is called by both the token mint and the env
// build, so without this we would log twice).
var liteJWTFallbackOnce sync.Once

// resolveLiteJWTSecret returns the per-install JWT signing secret, rotated on
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
		slog.Warn("config jwt_secret is empty; falling back to the dev-only constant; run `dexaflow setup` to rotate the per-install secret (#121)")
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
// An empty key is an install that predates per-install keys (Legacy): its rows
// are under the published constant, which is then the encrypting key, and the
// boot warns and names `dexaflow lite migrate-key`. A predecessor recorded
// without a key of its own still travels after the constant.
func liteSecretKeyList(key, previous string) string {
	if key == "" {
		key = devSecretKey
	}
	if previous == "" {
		return key
	}
	return key + "," + previous
}

// sharedServerEnv is the Lite control plane environment common to both executor
// modes: the isolated local database, the LITE edition marker, and a writable
// logs dir. When an admin hash is configured (by `dexaflow setup`), Lite enforces
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
	// empty on a legacy install, which liteSecretKeyList maps to the published
	// constant.
	secretKey string
	// secretKeyPrevious is the comma-separated decrypt-only predecessor list a
	// `dexaflow lite migrate-key` that has not finished recorded (or an operator
	// set by hand); empty once a migration finished.
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
		// Per-user, under ~/.dexaflow, NOT a shared /tmp path. A global
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
		// No LEOFLOW_REDIS_URL: Lite runs Redis-free: XCom on Postgres and an
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
		// Lite never re-encrypts at boot: keys move only through
		// `dexaflow lite migrate-key` (ADR 0065 gap 2). And the server holds the
		// key-migration lock for its lifetime, refusing to start during a
		// migration and exiting if it loses the lock.
		"LEOFLOW_SECRET_KEY_REENCRYPT_ON_BOOT=false",
		"LEOFLOW_SECRET_KEY_MIGRATION_LOCK=true",
		"LEOFLOW_AGENT_ALLOW_INSECURE_SECRETS=true",
		// The server pauses a new DAG by default, as Airflow does (#1705). Lite
		// is the inner-dev loop: a DAG you run locally must start active, so
		// its schedule runs without an unpause after the first save. A DAG's
		// own is_paused_upon_creation still wins, and an exported
		// DEXAFLOW_DAGS_ARE_PAUSED_AT_CREATION overrides this.
		"LEOFLOW_DAGS_ARE_PAUSED_AT_CREATION=false",
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
// Monaco bundle that `dexaflow setup` fetched. Both executors get it: the editor
// is orthogonal to execution.
func liteEditorEnv(workspaceDir, leoflowRoot string) []string {
	return []string{
		"LEOFLOW_UI_WORKSPACE=" + workspaceDir,
		"LEOFLOW_UI_MONACO_DIR=" + setup.MonacoDir(leoflowRoot),
	}
}

// applyLiteConfigDefaults loads ~/.dexaflow/config.yaml and applies the recorded
// executor/port to o, unless those flags were set on the command line.
func applyLiteConfigDefaults(cmd *cobra.Command, o *devOptions) {
	c, err := config.Load(configFilePath(cmd), nil)
	if err != nil {
		return
	}
	mergeLiteDefaults(o, c, cmd.Flags().Changed("executor"), cmd.Flags().Changed("port"))
}

// mergeLiteDefaults applies the executor/port from config (written by
// `dexaflow setup`) when the corresponding flag was not set on the command line.
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
		devPrintln(out, "  WARNING: no admin configured; run `dexaflow setup`. Falling back to no-auth (local only, insecure).")
	}
	return p
}

// loadLiteAdmin reads the Lite admin credential the setup wizard persisted (hash
// only) and the per-install JWT secret (rotated by `dexaflow setup` so a reinstall
// invalidates the prior install's tokens, #121) from ~/.dexaflow/config.yaml.
// Returns an empty hash when no admin is configured.
//
// It does NOT read the encryption keys: config.Load overlays DEXAFLOW_SECRET_KEY
// and LEOFLOW_SECRET_KEY from the environment, and a key from the operator's
// shell would make the server encrypt new rows under a key config.yaml does not
// record (ADR 0065 gap 7). acquireLiteKeyBoot reads them from the file alone.
func loadLiteAdmin(cmd *cobra.Command) liteEnvParams {
	c, err := config.Load(configFilePath(cmd), nil)
	if err != nil || c == nil {
		return liteEnvParams{}
	}
	email := c.AdminEmail
	if email == "" {
		email = auth.DefaultAdminEmail
	}
	return liteEnvParams{
		adminHash:  c.AdminPasswordHash,
		adminEmail: email,
		jwtSecret:  c.JWTSecret,
	}
}

// subprocessServerEnv adds the subprocess-executor settings: the agent binary,
// the project workdir (so dag.py imports), the venv Python (boot fallback when
// the per-DAG venv lookup misses), the per-DAG venvs root (LEOFLOW_LITE_VENVS_ROOT:
// the subprocess executor consults <root>/<dag_id>/bin/python to override the
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
