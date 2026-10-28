package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	apiclient "github.com/dexadata/dexaflow/pkg/client"
)

// apiTokenRequest is what `auth create-token --scope` asks the server for.
type apiTokenRequest struct {
	scope, name string
	ttl         time.Duration // zero leaves the lifetime to the server (90 days)
}

// parseTokenTTL reads a token lifetime: a Go duration ("12h", "1h30m") or a
// whole number of days ("90d"). It must be positive; the server enforces the
// bounds (one hour to 365 days).
func parseTokenTTL(s string) (time.Duration, error) {
	if days, ok := strings.CutSuffix(s, "d"); ok {
		n, err := strconv.ParseInt(days, 10, 64)
		if err != nil || n <= 0 || n > math.MaxInt64/int64(24*time.Hour) {
			return 0, fmt.Errorf("invalid --ttl %q: use a positive number of days (90d) or a duration (12h)", s)
		}
		return time.Duration(n) * 24 * time.Hour, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("invalid --ttl %q: use a positive number of days (90d) or a duration (12h)", s)
	}
	return d, nil
}

// apiTokenFlags checks the API token flags of create-token: --name and --ttl
// only make sense with --scope, which needs --name. It returns nil when
// --scope is absent, so the command keeps printing a session JWT.
func apiTokenFlags(scope, name, ttl string) (*apiTokenRequest, error) {
	if scope == "" {
		if name != "" || ttl != "" {
			return nil, errors.New("--name and --ttl create an API token and need --scope (deploy or read)")
		}
		return nil, nil //nolint:nilnil // no API token asked for: the JWT path runs
	}
	if name == "" {
		return nil, errors.New("--scope needs --name, so the token can be told apart and revoked")
	}
	req := &apiTokenRequest{scope: scope, name: name}
	if ttl != "" {
		d, err := parseTokenTTL(ttl)
		if err != nil {
			return nil, err
		}
		req.ttl = d
	}
	return req, nil
}

// createAPIToken asks the server for an API token with the bearer of a
// signed-in user and returns it.
func createAPIToken(ctx context.Context, serverURL, bearer string, req *apiTokenRequest) (*apiclient.CreatedAPIToken, error) {
	c, err := cliAPIClient(serverURL, bearer)
	if err != nil {
		return nil, err
	}
	body := apiclient.CreateAPITokenRequest{Name: req.name, Scope: apiclient.CreateAPITokenRequestScope(req.scope)}
	if req.ttl > 0 {
		secs := int64(req.ttl / time.Second)
		body.ExpiresInSeconds = &secs
	}
	resp, err := c.CreateAPITokenWithResponse(ctx, body)
	if err != nil {
		return nil, fmt.Errorf("posting to %s/api/v2/apiTokens: %w", serverURL, err)
	}
	if resp.StatusCode() != http.StatusCreated {
		return nil, apiStatusError(resp.StatusCode(), resp.Body)
	}
	if resp.JSON201 == nil || resp.JSON201.Token == "" {
		return nil, errors.New("server returned no token")
	}
	return resp.JSON201, nil
}

// runCreateAPIToken is create-token with --scope: it signs in with the
// password when a username is given, otherwise uses --token, the token
// variable or the saved session, then prints the new token alone on stdout,
// so `$(dexaflow auth create-token ...)` captures it.
func runCreateAPIToken(cmd *cobra.Command, serverFlag, tokenFlag, username, password string, passwordStdin bool, req *apiTokenRequest) error {
	ctx := cmdContext(cmd)
	var serverURL, bearer string
	if username != "" {
		choice, err := resolveServer(cmd, serverFlag)
		if err != nil {
			return err
		}
		pw, err := resolvePassword(cmd, password, passwordStdin)
		if err != nil {
			return err
		}
		serverURL = choice.URL
		if bearer, err = requestToken(ctx, serverURL, username, pw); err != nil {
			return hintEmailUsername(username, err)
		}
	} else {
		var err error
		if serverURL, bearer, err = resolveServerToken(cmd, serverFlag, tokenFlag); err != nil {
			return err
		}
	}
	created, err := createAPIToken(ctx, serverURL, bearer, req)
	if err != nil {
		return err
	}
	if _, werr := fmt.Fprintf(cmd.ErrOrStderr(), "Created %s token %s (id %s, scopes %s), expires %s.\nStore it now: it is not shown again.\n",
		req.scope, created.Name, created.Id, strings.Join(created.Scopes, " "), created.ExpiresAt.UTC().Format(time.RFC3339)); werr != nil {
		return werr
	}
	_, err = fmt.Fprintln(cmd.OutOrStdout(), created.Token)
	return err
}

// newTokensCommand builds `dexaflow auth tokens`: list and revoke API tokens.
func newTokensCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "tokens",
		Short: "List and revoke API tokens.",
	}
	cmd.AddCommand(newTokensListCommand(), newTokensRevokeCommand())
	return cmd
}

func newTokensListCommand() *cobra.Command {
	var serverURL, token string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List your API tokens (every token of the tenant for an admin).",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			server, bearer, err := resolveServerToken(cmd, serverURL, token)
			if err != nil {
				return err
			}
			c, err := cliAPIClient(server, bearer)
			if err != nil {
				return err
			}
			resp, err := c.ListAPITokensWithResponse(cmdContext(cmd), &apiclient.ListAPITokensParams{})
			if err != nil {
				return fmt.Errorf("listing %s/api/v2/apiTokens: %w", server, err)
			}
			if resp.StatusCode() != http.StatusOK || resp.JSON200 == nil {
				return apiStatusError(resp.StatusCode(), resp.Body)
			}
			var toks []apiclient.APIToken
			if resp.JSON200.ApiTokens != nil {
				toks = *resp.JSON200.ApiTokens
			}
			return printAPITokens(cmd.OutOrStdout(), toks)
		},
	}
	addTokenServerFlags(cmd, &serverURL, &token)
	return cmd
}

// printAPITokens writes the token table.
func printAPITokens(out io.Writer, toks []apiclient.APIToken) error {
	tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(tw, "ID\tNAME\tSCOPES\tOWNER\tCREATED\tEXPIRES\tLAST USED"); err != nil {
		return err
	}
	for _, t := range toks {
		owner, lastUsed := "-", "never"
		if t.OwnerEmail != nil && *t.OwnerEmail != "" {
			owner = *t.OwnerEmail
		}
		if t.LastUsedAt != nil {
			lastUsed = t.LastUsedAt.UTC().Format(time.RFC3339)
		}
		if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", t.Id, t.Name, strings.Join(t.Scopes, ","), owner,
			t.CreatedAt.UTC().Format(time.RFC3339), t.ExpiresAt.UTC().Format(time.RFC3339), lastUsed); err != nil {
			return err
		}
	}
	return tw.Flush()
}

func newTokensRevokeCommand() *cobra.Command {
	var serverURL, token string
	cmd := &cobra.Command{
		Use:   "revoke <token-id>",
		Short: "Revoke an API token; it stops working on its next request.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			server, bearer, err := resolveServerToken(cmd, serverURL, token)
			if err != nil {
				return err
			}
			c, err := cliAPIClient(server, bearer)
			if err != nil {
				return err
			}
			resp, err := c.RevokeAPITokenWithResponse(cmdContext(cmd), args[0])
			if err != nil {
				return fmt.Errorf("revoking token %s: %w", args[0], err)
			}
			if resp.StatusCode() != http.StatusNoContent {
				return apiStatusError(resp.StatusCode(), resp.Body)
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Revoked token %s\n", args[0])
			return err
		},
	}
	addTokenServerFlags(cmd, &serverURL, &token)
	return cmd
}

// addTokenServerFlags adds the --server and --token flags the token commands
// share. Managing tokens needs a signed-in user: an API token is refused.
func addTokenServerFlags(cmd *cobra.Command, serverURL, token *string) {
	cmd.Flags().StringVar(serverURL, "server", "", "control plane base URL, over --target (default: the target in use, then server_url from the config file)")
	cmd.Flags().StringVar(token, "token", "", tokenFlagHelp)
}

// tokenFlagHelp describes --token on the token commands. Managing tokens needs
// a signed-in user's session JWT, and argv is the wrong place for it.
const tokenFlagHelp = "session JWT of a signed-in user; prefer leaving it out: the command reads DEXAFLOW_TOKEN, " +
	"then the saved session from `dexaflow auth login`, and a value passed here shows in ps and shell history. " +
	"An API token cannot manage tokens"
