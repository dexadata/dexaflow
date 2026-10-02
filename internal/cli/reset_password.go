package cli

import (
	"fmt"
	"os"
	"os/user"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/dexadata/dexaflow/internal/config"
	"github.com/dexadata/dexaflow/internal/storage"
)

// newResetPasswordCommand resets the Lite admin password. Lite is a per-user
// install (the database and ~/.leoflow config belong to the user who ran it), so
// this runs as that user — NOT root. Running it under sudo would resolve HOME to
// /root and miss the user's config; run it as the same user as `leoflow lite`.
func newResetPasswordCommand() *cobra.Command {
	var userEmail string
	cmd := &cobra.Command{
		Use:   "reset-password",
		Short: "Reset the Leoflow Lite admin password.",
		Long: "reset-password generates a new admin password, updates it in the Lite " +
			"database, and shows it once. Run it as the same user as `leoflow lite` " +
			"(no sudo). The Lite Postgres must be reachable (start `leoflow lite` if it " +
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
	out := cmd.OutOrStdout()
	home := invokingUserHome()
	cfg := loadUserConfig(home)
	email := resolveAdminEmail(userEmail, cfg)

	pw, hash, err := generateAdminCredential()
	if err != nil {
		return err
	}

	ctx := cmdContext(cmd)
	pg, err := storage.NewPostgres(ctx, config.DatabaseSection{URL: devDSNs().database})
	if err != nil {
		return fmt.Errorf("connecting to the Lite database (is Postgres up? start `leoflow lite`): %w", err)
	}
	defer pg.Close()

	repo := storage.NewRepository(pg)
	ok, err := repo.SetUserPassword(ctx, "default", email, hash)
	if err != nil {
		return fmt.Errorf("resetting password: %w", err)
	}
	if !ok {
		return fmt.Errorf("no admin %q found; run `leoflow lite` once to create it", email)
	}

	// Keep config.yaml's hash in sync (used to bootstrap a fresh database).
	// Preserve the per-install JWT secret (#121) — reset-password rotates the
	// password, not the signing secret, so existing browser sessions are not
	// invalidated by a password reset.
	//
	// The secret key is preserved for a harder reason (#486): it decrypts every
	// stored connection password. Dropping it here would rewrite config.yaml
	// without a key, the next boot would fall back to the published constant,
	// and every credential encrypted under the real key would stop opening. A
	// password reset must not be able to do that.
	if cfg != nil && home != "" {
		// Read straight from the FILE, never through cfg: config.Load overlays
		// LEOFLOW_* environment variables, so an operator with LEOFLOW_SECRET_KEY
		// exported would have the shell value written over the per-install key,
		// and this command would destroy the only copy of the key that decrypts
		// every stored connection.
		sec := configFileSecrets(filepath.Join(stateDirIn(home), "config.yaml"))
		_ = writeLiteConfig(stateDirIn(home), cfg.ParserCmd, //nolint:errcheck // best-effort sync; the DB is the source of truth
			liteSettings{Workspace: cfg.Workspace, Executor: cfg.LiteExecutor, AdminEmail: email, Port: cfg.LitePort}, hash, sec)
	}

	_, _ = fmt.Fprintf(out, "\n  password reset for %s\n  new password: %s\n  (shown once — save it)\n", email, pw) //nolint:errcheck // best-effort terminal output
	return nil
}

// invokingUserHome returns the home of the human who ran the command, resolving
// SUDO_USER so `sudo leoflow lite reset-password` still finds the user's config
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

// loadUserConfig loads ~/.leoflow/config.yaml for the given home, or nil.
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
