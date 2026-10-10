package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/user"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/dexadata/dexaflow/internal/config"
	"github.com/dexadata/dexaflow/internal/storage"
)

// newResetPasswordCommand resets the Lite admin password. Lite is a per-user
// install (the database and ~/.dexaflow config belong to the user who ran it), so
// this runs as that user — NOT root. Running it under sudo would resolve HOME to
// /root and miss the user's config; run it as the same user as `dexaflow lite`.
func newResetPasswordCommand() *cobra.Command {
	var userEmail string
	cmd := &cobra.Command{
		Use:   "reset-password",
		Short: "Reset the Dexaflow Lite admin password.",
		Long: "reset-password generates a new admin password, updates it in the Lite " +
			"database, and shows it once. It also rotates the per-install session secret " +
			"in ~/.dexaflow/config.yaml, so every browser session signed before the reset " +
			"ends: at once when Lite is stopped, or when you restart `dexaflow lite` if it " +
			"is running (the command says which). Run it as the same user as `dexaflow lite` " +
			"(no sudo). The Lite Postgres must be reachable (start `dexaflow lite` if it " +
			"is not).",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runResetPassword(cmd, userEmail)
		},
	}
	cmd.Flags().StringVar(&userEmail, "user", "", "admin email to reset (default: the admin from config)")
	return cmd
}

func runResetPassword(cmd *cobra.Command, userEmail string) error {
	open := func(ctx context.Context) (resetPasswordStore, func(), error) {
		pg, err := storage.NewPostgres(ctx, config.DatabaseSection{URL: devDSNs().database})
		if err != nil {
			return nil, nil, fmt.Errorf("connecting to the Lite database (is Postgres up? start `dexaflow lite`): %w", err)
		}
		return liteResetStore{repo: storage.NewRepository(pg), pool: pg.Pool}, pg.Close, nil
	}
	return resetPassword(cmdContext(cmd), cmd.OutOrStdout(), invokingUserHome(), userEmail, open)
}

// openResetStore connects to the Lite datastore and returns it with its close.
type openResetStore func(ctx context.Context) (resetPasswordStore, func(), error)

// resetPasswordStore is what reset-password needs from the Lite datastore.
type resetPasswordStore interface {
	// SetUserPassword stores hash as the admin's password and reports whether
	// an admin with that email existed.
	SetUserPassword(ctx context.Context, tenant, email, hash string) (bool, error)
	// ServerRunning reports whether a Lite server is running against the
	// datastore.
	ServerRunning(ctx context.Context) (bool, error)
}

// liteResetStore is the Postgres-backed resetPasswordStore.
type liteResetStore struct {
	repo *storage.Repository
	pool storage.RowQueryer
}

// SetUserPassword stores the admin's new password hash.
func (s liteResetStore) SetUserPassword(ctx context.Context, tenant, email, hash string) (bool, error) {
	ok, err := s.repo.SetUserPassword(ctx, tenant, email, hash)
	if err != nil {
		return false, fmt.Errorf("setting the admin password: %w", err)
	}
	return ok, nil
}

// ServerRunning looks for the key-migration lock a Lite server holds for its
// whole life, without taking it.
func (s liteResetStore) ServerRunning(ctx context.Context) (bool, error) {
	held, err := storage.KeyMigrationLockHeld(ctx, s.pool)
	if err != nil {
		return false, fmt.Errorf("checking for a running Lite server: %w", err)
	}
	return held, nil
}

// resetPassword sets a new admin password and signs out every browser session
// signed before it (#413).
//
// Lite sessions are JWTs checked by signature, independent of the password, so
// changing the password alone left an existing session cookie working. The
// per-install JWT secret is therefore rotated as well, the same lifecycle a
// reinstall already gives it (#121). The watcher's in-process token is minted
// with the secret its server booted with and re-minted per operation (#407),
// so it keeps working against the running server and picks up the new secret
// with it on the next start.
//
// A running server keeps the secret it booted with until it restarts, so the
// report says which case applies instead of claiming sessions are already gone.
func resetPassword(ctx context.Context, out io.Writer, home, userEmail string, open openResetStore) error {
	// The config lock first, before anything else (ADR 0065 section 3): this
	// command rewrites config.yaml and must not interleave with a key
	// migration or another writer.
	if home != "" {
		if _, serr := os.Stat(stateDirIn(home)); serr == nil {
			release, lerr := lockConfigDir(stateDirIn(home), configLockExclusive, false)
			if lerr != nil {
				return lerr
			}
			defer release()
		}
	}
	cfg := loadUserConfig(home)
	email := resolveAdminEmail(userEmail, cfg)

	pw, hash, err := generateAdminCredential()
	if err != nil {
		return err
	}
	store, closeStore, err := open(ctx)
	if err != nil {
		return err
	}
	defer closeStore()
	ok, err := store.SetUserPassword(ctx, "default", email, hash)
	if err != nil {
		return fmt.Errorf("resetting password: %w", err)
	}
	if !ok {
		return fmt.Errorf("no admin %q found; run `dexaflow lite` once to create it", email)
	}
	rerr := rotateLiteConfigOnReset(home, cfg, email, hash)

	_, _ = fmt.Fprintf(out, "\n  password reset for %s\n  new password: %s\n  (shown once, save it)\n", email, pw) //nolint:errcheck // best-effort terminal output
	reportResetSessions(ctx, out, store, rerr)
	return nil
}

// errNoLiteConfig reports that there is no readable config.yaml to rotate the
// session secret in.
var errNoLiteConfig = errors.New("there is no readable ~/.dexaflow/config.yaml to rotate the session secret in")

// rotateLiteConfigOnReset rewrites config.yaml with the new admin hash and a
// fresh JWT signing secret.
//
// The connection secret key is carried forward untouched, for a harder reason
// (#486): it decrypts every stored connection password. Dropping it here would
// rewrite config.yaml without a key, the next boot would fall back to the
// published constant, and every credential encrypted under the real key would
// stop opening. A password reset must not be able to do that.
func rotateLiteConfigOnReset(home string, cfg *config.Config, email, hash string) error {
	if cfg == nil || home == "" {
		return errNoLiteConfig
	}
	// Read straight from the FILE, never through cfg: config.Load overlays
	// LEOFLOW_* environment variables, so an operator with LEOFLOW_SECRET_KEY
	// exported would have the shell value written over the per-install key,
	// and this command would destroy the only copy of the key that decrypts
	// every stored connection.
	sec := configFileSecrets(filepath.Join(stateDirIn(home), "config.yaml"))
	jwtSecret, err := generateJWTSecret()
	if err != nil {
		return err
	}
	sec.jwtSecret = jwtSecret
	return writeLiteConfig(stateDirIn(home), cfg.ParserCmd,
		liteSettings{Workspace: cfg.Workspace, Executor: cfg.LiteExecutor, AdminEmail: email, Port: cfg.LitePort}, hash, sec)
}

// reportResetSessions tells the user what happened to the browser sessions
// signed before the reset. It only states what this run verified: a failed
// check for a running server is reported like a running server.
func reportResetSessions(ctx context.Context, out io.Writer, store resetPasswordStore, rotateErr error) {
	if rotateErr != nil {
		_, _ = fmt.Fprintf(out, "  existing browser sessions were NOT signed out: %v\n", rotateErr) //nolint:errcheck // best-effort terminal output
		return
	}
	running, err := store.ServerRunning(ctx)
	if err != nil || running {
		_, _ = fmt.Fprintln(out, "  the session secret was rotated; restart `dexaflow lite` to sign out existing\n  browser sessions (a running server accepts them until it restarts)") //nolint:errcheck // best-effort terminal output
		return
	}
	_, _ = fmt.Fprintln(out, "  existing browser sessions are signed out (the session secret was rotated)") //nolint:errcheck // best-effort terminal output
}

// invokingUserHome returns the home of the human who ran the command, resolving
// SUDO_USER so `sudo dexaflow lite reset-password` still finds the user's config
// rather than root's.
func invokingUserHome() string {
	if su := os.Getenv("SUDO_USER"); su != "" {
		if u, err := user.Lookup(su); err == nil && u.HomeDir != "" {
			return u.HomeDir
		}
	}
	h, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return h
}

// loadUserConfig loads ~/.dexaflow/config.yaml for the given home, or nil.
func loadUserConfig(home string) *config.Config {
	if home == "" {
		return nil
	}
	c, err := config.Load(filepath.Join(stateDirIn(home), "config.yaml"), nil)
	if err != nil {
		return nil
	}
	return c
}

// resolveAdminEmail picks the email to reset: the --user flag, else the config
// admin, else the Lite default.
func resolveAdminEmail(flag string, cfg *config.Config) string {
	if flag != "" {
		return flag
	}
	if cfg != nil && cfg.AdminEmail != "" {
		return cfg.AdminEmail
	}
	return "admin@leoflow.local"
}
