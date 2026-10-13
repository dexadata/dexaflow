package cli

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/dexadata/dexaflow/internal/config"
)

// newLoginCommand builds `dexaflow auth login`: it exchanges credentials for a
// JWT at a control plane (typically Pro) and persists the token (and server URL)
// to the config file, so subsequent `push`/`deploy` calls need no auth flags.
// This is what makes the pipeline-less Lite->Pro loop fluid (login once, deploy
// many) — ADR 0041. It is distinct from `docker login` (registry auth), which
// Dexaflow never handles. It is a sibling of `auth create-token`, which prints a
// token for CI rather than persisting an interactive session.
func newLoginCommand() *cobra.Command {
	var serverURL, username, password string
	var passwordStdin bool
	cmd := &cobra.Command{
		Use:   "login",
		Short: "Authenticate to a control plane (Pro) and store the token.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			choice, rerr := resolveServer(cmd, serverURL)
			if rerr != nil {
				return rerr
			}
			serverURL = choice.URL
			// Before any password is asked for or read: a workspace file can
			// name the server, so the user sees where the credentials go.
			if _, werr := fmt.Fprintf(cmd.OutOrStdout(), "Logging in to %s (%s)\n", serverURL, choice.Source); werr != nil {
				return werr
			}
			var cerr error
			if password, cerr = resolvePassword(cmd, password, passwordStdin); cerr != nil {
				return cerr
			}
			username, password, cerr = resolveCredentials(cmd, username, password)
			if cerr != nil {
				return cerr
			}
			token, err := requestToken(cmdContext(cmd), serverURL, username, password)
			if err != nil {
				return hintEmailUsername(username, err)
			}
			path, perr := sessionConfigPath(cmd)
			if perr != nil {
				return perr
			}
			if serr := persistLogin(path, serverURL, token, choice.Target); serr != nil {
				return serr
			}
			if choice.Target != nil {
				_, err = fmt.Fprintf(cmd.OutOrStdout(), "Logged in to %s (target %s; token saved to %s)\n", serverURL, choice.Target.Name, path)
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Logged in to %s (token saved to %s)\n", serverURL, path)
			return err
		},
	}
	cmd.Flags().StringVar(&serverURL, "server", "", "control plane base URL, over --target (default: the target in use, then server_url from the config file)")
	cmd.Flags().StringVar(&username, "username", os.Getenv("LEOFLOW_USERNAME"), "username")
	cmd.Flags().StringVar(&password, "password", os.Getenv("LEOFLOW_PASSWORD"), "password")
	cmd.Flags().BoolVar(&passwordStdin, "password-stdin", false, "read the password from stdin instead of --password (avoids ps/shell-history exposure)")
	return cmd
}

// persistLogin saves a fresh login. The token is always saved for its server,
// so each control plane keeps its own. A login to a named target leaves the
// single-server session (server_url and token) alone; any other login also
// writes it, as before targets existed, so configs and tools that read those
// two keys keep working.
func persistLogin(path, serverURL, token string, target *activeTarget) error {
	if err := config.UpdateUserStore(path, func(s *config.UserStore) error {
		s.SetSession(serverURL, token)
		return nil
	}); err != nil {
		return err
	}
	if target != nil {
		return nil
	}
	return config.PersistSession(path, serverURL, token)
}

// sessionConfigPath resolves the config file `login` writes to: the --config
// flag when set, otherwise the default ~/.dexaflow/config.yaml. Unlike
// configFilePath, it returns the default path even when the file does not yet
// exist, because login is allowed to create it.
func sessionConfigPath(cmd *cobra.Command) (string, error) {
	if p, err := cmd.Flags().GetString("config"); err == nil && strings.TrimSpace(p) != "" {
		return p, nil
	}
	return config.DefaultConfigFile()
}

// resolveCredentials fills missing username/password by prompting when the
// session is interactive — the password is read hidden so it never lands in
// shell history. In a non-interactive session (CI) the values must come from
// flags/env, so a missing one is a loud error rather than a hang on a prompt.
func resolveCredentials(cmd *cobra.Command, username, password string) (user, pass string, err error) {
	user, pass = username, password
	interactive := cmdInteractive(cmd)
	if user == "" {
		if !interactive {
			return "", "", fmt.Errorf("username required: pass --username or set LEOFLOW_USERNAME")
		}
		if user, err = promptValue(cmd.InOrStdin(), cmd.OutOrStdout(), "Username: "); err != nil {
			return "", "", err
		}
	}
	if pass == "" {
		if !interactive {
			return "", "", fmt.Errorf("password required: pass --password or set LEOFLOW_PASSWORD")
		}
		if pass, err = promptPassword(cmd.InOrStdin(), cmd.OutOrStdout()); err != nil {
			return "", "", err
		}
	}
	return user, pass, nil
}

// promptValue prints a label and reads a trimmed line of input.
// hintEmailUsername appends a first-login hint when authentication fails (a 401)
// with a username that is not an e-mail. The bootstrap admin is
// admin@leoflow.local, and a bare "admin" returns a generic "invalid
// credentials"; the hint saves the trip. It is safe: it reveals nothing about
// which accounts exist, only the expected username format.
func hintEmailUsername(username string, err error) error {
	if err == nil || username == "" || strings.Contains(username, "@") {
		return err
	}
	// Match the exact shape requestToken formats ("server returned 401:"), not a
	// bare "401" that a host:port or a response body could contain by accident.
	if !strings.Contains(err.Error(), "server returned 401") {
		return err
	}
	return fmt.Errorf("%w\nhint: the username is an e-mail address (the bootstrap admin is admin@leoflow.local)", err)
}

// readPasswordStdin reads one line — the password — from r. It backs the
// --password-stdin flag: the CI-safe way to supply a credential without it
// appearing on argv (visible in `ps` and the shell history) or in an env var. The
// trailing newline is stripped.
func readPasswordStdin(r io.Reader) (string, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	if sc.Scan() {
		// Treat an empty first line the same as empty stdin: --password-stdin must
		// never yield a silent empty password (echo '' | ... would otherwise pass
		// "" downstream). A password is not trimmed — only a truly empty line fails.
		if line := sc.Text(); line != "" {
			return line, nil
		}
		return "", fmt.Errorf("--password-stdin got an empty password")
	}
	if err := sc.Err(); err != nil {
		return "", fmt.Errorf("reading password from stdin: %w", err)
	}
	return "", fmt.Errorf("--password-stdin was set but stdin was empty")
}

// resolvePassword applies the auth commands' credential-source precedence:
// --password-stdin (read a line from stdin) wins; otherwise the --password flag
// or its LEOFLOW_PASSWORD default is used. An explicit --password together with
// --password-stdin is a conflict — but an env-var default is not, so only a flag
// the user actually set counts. When neither is given the value is returned empty
// and the caller decides whether to prompt (interactive) or error (CI).
func resolvePassword(cmd *cobra.Command, passwordFlag string, stdin bool) (string, error) {
	if !stdin {
		return passwordFlag, nil
	}
	if cmd.Flags().Changed("password") {
		return "", fmt.Errorf("--password and --password-stdin are mutually exclusive")
	}
	return readPasswordStdin(cmd.InOrStdin())
}

func promptValue(in io.Reader, out io.Writer, label string) (string, error) {
	devPrintf(out, "%s", label)
	line, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && err != io.EOF {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

// promptPassword reads a password without echoing it when in is a terminal;
// otherwise (a pipe, e.g. `echo pw | dexaflow auth login`) it falls back to a
// plain line read so the value can still be supplied non-interactively.
func promptPassword(in io.Reader, out io.Writer) (string, error) {
	if f, ok := in.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		devPrintf(out, "Password: ")
		secret, err := term.ReadPassword(int(f.Fd()))
		devPrintf(out, "\n") // ReadPassword swallows the user's newline
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(secret)), nil
	}
	return promptValue(in, out, "Password: ")
}
