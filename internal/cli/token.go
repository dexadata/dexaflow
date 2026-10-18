package cli

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/spf13/cobra"

	apiclient "github.com/dexadata/dexaflow/pkg/client"
)

func newAuthCommand() *cobra.Command {
	auth := &cobra.Command{
		Use:   "auth",
		Short: "Manage authentication tokens.",
	}
	auth.AddCommand(newCreateTokenCommand())
	auth.AddCommand(newLoginCommand())
	auth.AddCommand(newCreateUserCommand())
	auth.AddCommand(newTokensCommand())
	auth.AddCommand(newCITokenCommand())
	return auth
}

// newCreateUserCommand builds `dexaflow auth create-user`: it creates an account
// on the control plane via the admin-only POST /api/v2/users endpoint. It is the
// long-promised counterpart to bootstrap (ADR 0008) — until now the only way to
// mint a user was the Lite bootstrap admin. Unlike create-token/login (which
// exchange credentials for a token), this call is privileged, so it carries an
// existing admin's bearer token, resolved with the same precedence as `deploy`:
// --token, then LEOFLOW_TOKEN, then the session saved by `auth login`.
func newCreateUserCommand() *cobra.Command {
	var serverURL, token, email, password string
	var passwordStdin bool
	var roles []string
	cmd := &cobra.Command{
		Use:   "create-user",
		Short: "Create a user on the control plane (admin only).",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			resolvedServer, resolvedToken, rerr := resolveServerToken(cmd, serverURL, token)
			if rerr != nil {
				return rerr
			}
			pw, perr := resolvePassword(cmd, password, passwordStdin)
			if perr != nil {
				return perr
			}
			if email == "" || pw == "" {
				return fmt.Errorf("--email and --password are required")
			}
			created, err := createUser(cmdContext(cmd), resolvedServer, resolvedToken, email, pw, roles)
			if err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Created user %s (id %s, roles %s)\n",
				created.Email, created.Id, rolesOrNone(created.Roles))
			return err
		},
	}
	cmd.Flags().StringVar(&serverURL, "server", "", "control plane base URL, over --target (default: the target in use, then server_url from the config file)")
	cmd.Flags().StringVar(&token, "token", os.Getenv("LEOFLOW_TOKEN"), "admin JWT bearer token (default: config token)")
	cmd.Flags().StringVar(&email, "email", "", "email of the user to create")
	cmd.Flags().StringVar(&password, "password", os.Getenv("LEOFLOW_PASSWORD"), "password for the new user")
	cmd.Flags().BoolVar(&passwordStdin, "password-stdin", false, "read the password from stdin instead of --password (avoids ps/shell-history exposure)")
	cmd.Flags().StringArrayVar(&roles, "role", nil, "existing role to grant (repeatable); empty grants none")
	return cmd
}

// createUser posts to /api/v2/users through the shared typed client, carrying the
// admin bearer, and returns the created user. It mirrors requestToken's use of
// the generated client (ADR 0050 D8) rather than hand-rolling HTTP.
func createUser(ctx context.Context, serverURL, token, email, password string, roles []string) (apiclient.User, error) {
	c, err := cliAPIClient(serverURL, token)
	if err != nil {
		return apiclient.User{}, err
	}
	body := apiclient.CreateUserRequest{Email: email, Password: &password}
	if len(roles) > 0 {
		body.Roles = &roles
	}
	resp, err := c.CreateUserWithResponse(ctx, body)
	if err != nil {
		return apiclient.User{}, fmt.Errorf("posting to %s/api/v2/users: %w", serverURL, err)
	}
	if resp.StatusCode() != http.StatusCreated {
		return apiclient.User{}, apiStatusError(resp.StatusCode(), resp.Body)
	}
	if resp.JSON201 == nil {
		return apiclient.User{}, fmt.Errorf("server returned no user")
	}
	return *resp.JSON201, nil
}

// rolesOrNone renders the granted roles for CLI output, showing "(none)" when the
// user was created without any.
func rolesOrNone(roles []string) string {
	if len(roles) == 0 {
		return "(none)"
	}
	return strings.Join(roles, ",")
}

// newCreateTokenCommand builds `dexaflow auth create-token`. Without --scope
// it exchanges a username and password for a short-lived session JWT. With
// --scope it creates a named, long-lived API token for CI (ADR 0074), signing
// in first when a username is given, and otherwise using --token, the token
// variable or the saved session.
func newCreateTokenCommand() *cobra.Command {
	var serverURL, username, password, token, scope, name, ttl string
	var passwordStdin bool
	cmd := &cobra.Command{
		Use:   "create-token",
		Short: "Obtain a session JWT, or with --scope create a long-lived API token.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			apiReq, ferr := apiTokenFlags(scope, name, ttl)
			if ferr != nil {
				return ferr
			}
			if apiReq != nil {
				return runCreateAPIToken(cmd, serverURL, token, username, password, passwordStdin, apiReq)
			}
			choice, rerr := resolveServer(cmd, serverURL)
			if rerr != nil {
				return rerr
			}
			serverURL = choice.URL
			pw, perr := resolvePassword(cmd, password, passwordStdin)
			if perr != nil {
				return perr
			}
			jwt, err := requestToken(cmdContext(cmd), serverURL, username, pw)
			if err != nil {
				return hintEmailUsername(username, err)
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), jwt)
			return err
		},
	}
	cmd.Flags().StringVar(&serverURL, "server", "", "control plane base URL, over --target (default: the target in use, then server_url from the config file)")
	cmd.Flags().StringVar(&username, "username", os.Getenv("LEOFLOW_USERNAME"), "username")
	cmd.Flags().StringVar(&password, "password", os.Getenv("LEOFLOW_PASSWORD"), "password")
	cmd.Flags().BoolVar(&passwordStdin, "password-stdin", false, "read the password from stdin instead of --password (avoids ps/shell-history exposure)")
	cmd.Flags().StringVar(&scope, "scope", "", "create a long-lived API token with this scope instead of a session JWT: deploy (register versions, set the current version, read DAGs) or read")
	cmd.Flags().StringVar(&name, "name", "", "name of the API token, unique among your live tokens (with --scope)")
	cmd.Flags().StringVar(&ttl, "ttl", "", "lifetime of the API token, in days (90d) or as a duration (12h), from 1h to 365d (with --scope; default 90d)")
	cmd.Flags().StringVar(&token, "token", "", tokenFlagHelp)
	return cmd
}

// requestToken posts credentials to /auth/token and returns the access token,
// via the shared typed /api/v2 client (ADR 0050 D8). No token is presented to
// obtain a token, so New is called with an empty bearer.
func requestToken(ctx context.Context, serverURL, username, password string) (string, error) {
	c, err := cliAPIClient(serverURL, "")
	if err != nil {
		return "", err
	}
	resp, err := c.IssueTokenWithResponse(ctx, apiclient.TokenRequest{
		Username: username,
		Password: password,
	})
	if err != nil {
		return "", fmt.Errorf("posting to %s/auth/token: %w", serverURL, err)
	}
	if resp.StatusCode() != http.StatusOK {
		return "", apiStatusError(resp.StatusCode(), resp.Body)
	}
	if resp.JSON200 == nil || resp.JSON200.AccessToken == nil {
		return "", fmt.Errorf("server returned no access_token")
	}
	return *resp.JSON200.AccessToken, nil
}
