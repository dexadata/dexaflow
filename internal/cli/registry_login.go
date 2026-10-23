package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"
	"unicode"

	"github.com/spf13/cobra"

	"github.com/dexadata/dexaflow/internal/build"
)

// ghcrHost is the GitHub Container Registry, the registry whose login can
// exchange the gh CLI's session.
const ghcrHost = "ghcr.io"

// ghRunTimeout bounds one run of the gh CLI.
const ghRunTimeout = 30 * time.Second

// ghWaitDelay bounds the wait for gh's output once it has exited or been
// stopped, so a program it started that still holds the output open cannot
// outlast ghRunTimeout.
const ghWaitDelay = 2 * time.Second

// newRegistryCommand is `dexaflow registry`, the commands about the
// container registry DAG images are pushed to.
func newRegistryCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "registry",
		Short: "Sign in to the container registry DAG images are pushed to",
	}
	cmd.AddCommand(newRegistryLoginCommand())
	return cmd
}

// registryLoginFlags are the flags of `dexaflow registry login`.
type registryLoginFlags struct {
	username      string
	passwordStdin bool
}

// newRegistryLoginCommand is `dexaflow registry login <registry>` (ADR 0091).
func newRegistryLoginCommand() *cobra.Command {
	var f registryLoginFlags
	cmd := &cobra.Command{
		Use:   "login <registry>",
		Short: "Sign in to a registry without docker, storing the credential where builds and pushes read it",
		Long: "Sign in to a container registry without docker login. The credential is checked with the registry, " +
			"then stored in the docker config (DOCKER_CONFIG, else ~/.docker/config.json), or in its credsStore or " +
			"credHelpers helper, where the CLI's registry client and docker, podman and nerdctl read it.\n\n" +
			"For ghcr.io with no token given, the session of the gh CLI is used (gh auth login). " +
			"Otherwise pass --username and a token on stdin: a Docker Hub personal access token, a GitHub token " +
			"with write:packages, or the password of any other registry.",
		Example: "  dexaflow registry login ghcr.io\n" +
			"  echo \"$DOCKERHUB_TOKEN\" | dexaflow registry login docker.io --username my-user --password-stdin",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runRegistryLogin(cmd, args[0], f)
		},
	}
	cmd.Flags().StringVar(&f.username, "username", "", "the user name to sign in as")
	cmd.Flags().BoolVar(&f.passwordStdin, "password-stdin", false, "read the token or password from stdin (one line)")
	return cmd
}

// runRegistryLogin reads the credential, from stdin or the gh session,
// checks it with the registry and stores it.
func runRegistryLogin(cmd *cobra.Command, host string, f registryLoginFlags) error {
	ctx := cmdContext(cmd)
	if err := build.CheckRegistryHost(host); err != nil {
		return err
	}
	user, secret, via, err := registryCredential(ctx, cmd.InOrStdin(), host, f)
	if err != nil {
		return err
	}
	if uerr := checkRegistryUser(user); uerr != nil {
		return uerr
	}
	// cliDeps.verifyRegistryLogin is build.VerifyLogin; a test of the GHCR
	// path, whose registry is the real ghcr.io, stands in for it.
	if verr := depsFrom(ctx).verifyRegistryLogin(ctx, host, user, secret); verr != nil {
		return fmt.Errorf("signing in to %s as %s: %w", host, user, verr)
	}
	where, err := build.StoreLogin(ctx, host, user, secret)
	if err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	//nolint:errcheck // command output
	fmt.Fprintf(out, "signed in to %s as %s%s; the credential is stored for %s\n", host, user, via, where)
	if via != "" {
		//nolint:errcheck // command output
		fmt.Fprintln(out, "  a push needs the write:packages scope on that token; if the registry refuses one: gh auth refresh -h github.com -s write:packages")
	}
	return nil
}

// registryCredential is the user and secret to sign in to host with: the
// token on stdin with --password-stdin, else the gh session for GHCR. via
// says where a credential that was not given came from.
func registryCredential(ctx context.Context, stdin io.Reader, host string, f registryLoginFlags) (user, secret, via string, err error) {
	if f.passwordStdin {
		if f.username == "" {
			return "", "", "", errors.New("--password-stdin needs --username: the user the token belongs to")
		}
		secret, err = readSecretLine(stdin)
		return f.username, secret, "", err
	}
	if host != ghcrHost {
		return "", "", "", noCredential(host)
	}
	user, secret, err = ghSession(ctx)
	if err != nil {
		return "", "", "", fmt.Errorf("reading the gh session for %s (%w); sign in with `gh auth login`, "+
			"or pass a GitHub token with write:packages: echo \"$TOKEN\" | dexaflow registry login ghcr.io --username <user> --password-stdin", host, err)
	}
	if f.username != "" {
		user = f.username
	}
	return user, secret, " (the gh session)", nil
}

// noCredential says what to pass to sign in to host.
func noCredential(host string) error {
	what := "the registry's password or token"
	if loginKeyIsDockerHub(host) {
		what = "a Docker Hub personal access token (https://app.docker.com/settings/personal-access-tokens)"
	}
	return fmt.Errorf("pass %s on stdin: echo \"$TOKEN\" | dexaflow registry login %s --username <user> --password-stdin", what, host)
}

// loginKeyIsDockerHub reports whether host names Docker Hub.
func loginKeyIsDockerHub(host string) bool {
	switch host {
	case "docker.io", "index.docker.io", "registry-1.docker.io":
		return true
	}
	return false
}

// checkRegistryUser refuses a user name a docker config cannot hold: one
// with a colon, which splits user from secret there, or a control
// character, which the output would print as is.
func checkRegistryUser(user string) error {
	if strings.Contains(user, ":") || strings.IndexFunc(user, unicode.IsControl) >= 0 {
		return fmt.Errorf("the user name %q holds a colon or a control character, which a registry credential cannot", user)
	}
	return nil
}

// readSecretLine reads one non-empty line from r, without its line break.
func readSecretLine(r io.Reader) (string, error) {
	data, err := io.ReadAll(bufio.NewReader(io.LimitReader(r, 64<<10)))
	if err != nil {
		return "", fmt.Errorf("reading the token from stdin: %w", err)
	}
	secret := strings.TrimRight(string(data), "\r\n")
	switch {
	case strings.TrimSpace(secret) == "":
		return "", errors.New("the token on stdin is empty")
	case strings.ContainsAny(secret, "\r\n"):
		return "", errors.New("the token on stdin must be one line")
	}
	return secret, nil
}

// ghSession reads the token and the user of the gh CLI's session on
// github.com. The token goes to stdout and never into an error.
func ghSession(ctx context.Context) (user, token string, err error) {
	token, err = runGH(ctx, "auth", "token", "--hostname", "github.com")
	if err != nil {
		return "", "", err
	}
	user, err = runGH(ctx, "api", "user", "--jq", ".login")
	if err != nil {
		return "", "", err
	}
	return user, token, nil
}

// runGH runs gh with args, with no input so a prompt fails, and returns its
// one line of output.
func runGH(ctx context.Context, args ...string) (string, error) {
	if _, err := exec.LookPath("gh"); err != nil {
		return "", errors.New("gh is not on PATH")
	}
	ctx, cancel := context.WithTimeout(ctx, ghRunTimeout)
	defer cancel()
	c := exec.CommandContext(ctx, "gh", args...) //nolint:gosec // G204: gh from PATH, with arguments built here
	c.Stdin = strings.NewReader("")
	c.WaitDelay = ghWaitDelay
	var stderr strings.Builder
	c.Stderr = &stderr
	out, err := c.Output()
	if err != nil {
		return "", fmt.Errorf("gh %s failed: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	line := strings.TrimSpace(string(out))
	if line == "" || strings.ContainsAny(line, "\r\n") {
		return "", fmt.Errorf("gh %s %s printed no single line", args[0], args[1])
	}
	return line, nil
}
