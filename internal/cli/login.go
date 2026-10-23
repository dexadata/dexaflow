package cli

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/dexadata/dexaflow/internal/auth"
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
	var serverURL string
	var f loginFlags
	cmd := &cobra.Command{
		Use:   "login",
		Short: "Authenticate to a control plane (Pro) and store the token.",
		Long: "Authenticate to a control plane and save the token for that server.\n\n" +
			"When the server offers single sign-on and no username or password is given, " +
			"login opens the browser on the server's sign-in, where you approve the CLI sign-in, " +
			"and waits for it on a loopback address (127.0.0.1); --no-browser prints the URL instead. " +
			"On a machine with no browser (an SSH session, or Linux with no display) a server that serves it " +
			"gets the device sign-in instead, as with --device: login prints a page and a short code, you open the page " +
			"in a browser on any device, sign in and confirm the code, and login waits for that approval; --sso keeps the " +
			"browser sign-in. " +
			"A username or a password (flags, LEOFLOW_USERNAME/LEOFLOW_PASSWORD or --password-stdin) " +
			"signs in with a password, as does a server without single sign-on. A password is not " +
			"sent over plain http to a server that is not on this machine without --allow-insecure-password.",
		Args: cobra.NoArgs,
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
			token, err := obtainLoginToken(cmd, serverURL, f)
			if err != nil {
				return err
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
	cmd.Flags().StringVar(&f.username, "username", os.Getenv("LEOFLOW_USERNAME"), "username (signs in with a password)")
	cmd.Flags().StringVar(&f.password, "password", os.Getenv("LEOFLOW_PASSWORD"), "password")
	cmd.Flags().BoolVar(&f.passwordStdin, "password-stdin", false, "read the password from stdin instead of --password (avoids ps/shell-history exposure)")
	cmd.Flags().BoolVar(&f.sso, "sso", false, "sign in with the server's single sign-on in a browser (the default when the server offers it and no username or password is given)")
	cmd.Flags().BoolVar(&f.noBrowser, "no-browser", false, "with single sign-on, print the sign-in URL instead of opening a browser (implies --sso)")
	cmd.Flags().BoolVar(&f.device, "device", false, "sign in with single sign-on in a browser on another device, by a code (for SSH sessions and containers)")
	cmd.Flags().DurationVar(&f.timeout, "timeout", defaultSSOTimeout, "how long to wait for the single sign-on approval")
	cmd.Flags().BoolVar(&f.allowInsecurePassword, "allow-insecure-password", false, "send a password over plain http to a server that is not on this machine (it can be read on the way)")
	return cmd
}

// loginFlags are the flags of `dexaflow auth login`.
type loginFlags struct {
	username, password string
	passwordStdin      bool
	sso, noBrowser     bool
	device             bool
	timeout            time.Duration
	// allowInsecurePassword lets a password go over plain http to a host
	// that is not loopback.
	allowInsecurePassword bool
}

// loginMethod is how `auth login` signs in.
type loginMethod int

// The ways `auth login` signs in.
const (
	methodPassword loginMethod = iota
	methodBrowser
	methodDevice
)

// obtainLoginToken signs in the way chooseLoginMethod decides and returns the
// Dexaflow token.
func obtainLoginToken(cmd *cobra.Command, serverURL string, f loginFlags) (string, error) {
	method, err := chooseLoginMethod(cmd, serverURL, f)
	if err != nil {
		return "", err
	}
	switch method {
	case methodDevice:
		return deviceLogin(cmdContext(cmd), cmd.OutOrStdout(), serverURL, f.timeout)
	case methodBrowser:
		return ssoLogin(cmdContext(cmd), cmd.OutOrStdout(), serverURL, ssoOptions{noBrowser: f.noBrowser, timeout: f.timeout})
	case methodPassword:
	}
	if terr := checkPasswordTransport(serverURL, f.allowInsecurePassword); terr != nil {
		return "", terr
	}
	password, err := resolvePassword(cmd, f.password, f.passwordStdin)
	if err != nil {
		return "", err
	}
	username, password, err := resolveCredentials(cmd, f.username, password)
	if err != nil {
		return "", err
	}
	token, err := requestToken(cmdContext(cmd), serverURL, username, password)
	if err != nil {
		return "", hintEmailUsername(username, err)
	}
	return token, nil
}

// chooseLoginMethod decides how login signs in, and says which way and why
// before anything is asked for. --device asks for the device sign-in and
// --sso (or --no-browser) for the browser one; the server must serve it, and
// neither combines with a password flag. Otherwise any username or password,
// from a flag or the environment, means a password login, and with neither
// the server's answer decides: single sign-on when it offers it, on another
// device when this session has no browser, its output is a terminal someone
// can read the code on, and the server serves that, and a password
// otherwise.
func chooseLoginMethod(cmd *cobra.Command, serverURL string, f loginFlags) (loginMethod, error) {
	out := cmd.OutOrStdout()
	if f.device {
		return chooseDevice(cmd, serverURL)
	}
	if f.sso || f.noBrowser {
		if err := refusePasswordFlags(cmd, "--sso and --no-browser sign in through the browser", "--sso"); err != nil {
			return methodPassword, err
		}
		if !serverMethods(cmdContext(cmd), serverURL).SSO {
			return methodPassword, fmt.Errorf("%s does not offer single sign-on; sign in with --username and a password", serverURL)
		}
		devPrintf(out, "Signing in with single sign-on (--sso)\n")
		return methodBrowser, nil
	}
	if f.username != "" || f.password != "" || f.passwordStdin {
		devPrintf(out, "Signing in with a password (a username or password was given; "+
			"where the server uses single sign-on, only break-glass accounts have a password)\n")
		return methodPassword, nil
	}
	m := serverMethods(cmdContext(cmd), serverURL)
	if !m.SSO {
		devPrintf(out, "Signing in with a password (the server does not offer single sign-on)\n")
		return methodPassword, nil
	}
	d := cmdDeps(cmd)
	if headless, why := d.sessionHeadless(); headless && m.Device && d.outputIsTerminal(out) {
		devPrintf(out, "Signing in with single sign-on on another device (the server offers it and this session has no browser: %s; "+
			"pass --sso to use a browser on this machine)\n", why)
		return methodDevice, nil
	}
	devPrintf(out, "Signing in with single sign-on (the server offers it and no username or password was given)\n")
	return methodBrowser, nil
}

// chooseDevice checks an explicit --device: no other way to sign in is
// asked for, and the server serves the device sign-in.
func chooseDevice(cmd *cobra.Command, serverURL string) (loginMethod, error) {
	for _, name := range []string{"sso", "no-browser"} {
		if cmd.Flags().Changed(name) {
			return methodPassword, fmt.Errorf("--device signs in on another device; drop --%s, or drop --device to sign in with a browser here", name)
		}
	}
	if err := refusePasswordFlags(cmd, "--device signs in with single sign-on", "--device"); err != nil {
		return methodPassword, err
	}
	m := serverMethods(cmdContext(cmd), serverURL)
	if !m.SSO || !m.Device {
		return methodPassword, fmt.Errorf("%s does not serve the device sign-in; sign in with --sso and a browser, or use an API token", serverURL)
	}
	devPrintf(cmd.OutOrStdout(), "Signing in with single sign-on on another device (--device)\n")
	return methodDevice, nil
}

// refusePasswordFlags refuses a password flag next to a single sign-on flag.
func refusePasswordFlags(cmd *cobra.Command, what, flag string) error {
	for _, name := range []string{"username", "password", "password-stdin"} {
		if cmd.Flags().Changed(name) {
			return fmt.Errorf("%s; drop --%s, or drop %s to sign in with a password", what, name, flag)
		}
	}
	return nil
}

// checkPasswordTransport refuses to send a password over plain http to a
// host that is not this machine, unless allowInsecure is set: anyone on the
// path could read it. https and loopback addresses are always fine.
func checkPasswordTransport(serverURL string, allowInsecure bool) error {
	u, err := url.Parse(serverURL)
	if err != nil {
		return fmt.Errorf("parsing the server URL %q: %w", serverURL, err)
	}
	if u.Scheme != "http" || allowInsecure || isLocalServerHost(u.Hostname()) {
		return nil
	}
	return fmt.Errorf("refusing to send a password over plain http to %s, where it can be read on the way; "+
		"use https, sign in with single sign-on, or pass --allow-insecure-password", u.Host)
}

// isLocalServerHost reports whether a server host names this machine: localhost, a
// .localhost name, or a loopback address.
func isLocalServerHost(host string) bool {
	h := strings.ToLower(strings.TrimSuffix(host, "."))
	if h == "localhost" || strings.HasSuffix(h, ".localhost") {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
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
// admin@dexaflow.local (admin@leoflow.local on installs created before the
// rename), and a bare "admin" returns a generic "invalid
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
	return fmt.Errorf("%w\nhint: the username is an e-mail address (the bootstrap admin is %s, or %s on installs created before the rename)",
		err, auth.DefaultAdminEmail, auth.LegacyAdminEmail)
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
