package cli

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/dexadata/dexaflow/internal/envcompat"
	apiclient "github.com/dexadata/dexaflow/pkg/client"
)

// ciOIDCTokenVar is the variable `auth ci-token` reads the job's OIDC token
// from, without its DEXAFLOW_ prefix (LEOFLOW_OIDC_TOKEN still works).
const ciOIDCTokenVar = "OIDC_TOKEN"

// newCITokenCommand builds `dexaflow auth ci-token`: a CI job exchanges the
// OIDC token its provider minted for it for a short-lived deploy session of
// the control plane (ADR 0092).
func newCITokenCommand() *cobra.Command {
	var serverURL string
	cmd := &cobra.Command{
		Use:   "ci-token",
		Short: "Exchange a CI job's OIDC token for a deploy session.",
		Long: "Posts the OIDC token of a CI job, read from DEXAFLOW_OIDC_TOKEN, to the control plane's\n" +
			"POST /api/v2/auth/ciToken, and prints the session it answers, alone on stdout:\n" +
			"  DEXAFLOW_TOKEN=\"$(dexaflow auth ci-token)\"\n" +
			"The server checks the token against its trusted CI issuers (auth.ci_oidc) and\n" +
			"maps its claims to a role of the issuer's tenant. Pipelines written by\n" +
			"`dexaflow ci init` for a server that offers it run this for you.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runCIToken(cmd, serverURL)
		},
	}
	cmd.Flags().StringVar(&serverURL, "server", "", "control plane URL (default: the target, DEXAFLOW_SERVER_URL, then the config file)")
	return cmd
}

// runCIToken exchanges the job's token with the server the command resolves
// and prints the session.
func runCIToken(cmd *cobra.Command, serverFlag string) error {
	raw, ok := envcompat.Lookup(ciOIDCTokenVar)
	if !ok {
		return errors.New("DEXAFLOW_OIDC_TOKEN is not set: set it to the OIDC token the CI provider minted for the job")
	}
	choice, err := resolveServer(cmd, serverFlag)
	if err != nil {
		return err
	}
	if choice.Implicit {
		return errors.New("no control plane to exchange the token with: set DEXAFLOW_SERVER_URL, a target, or --server")
	}
	c, err := cliBareAPIClient(choice.URL, 30*time.Second)
	if err != nil {
		return err
	}
	s, err := c.ExchangeCIToken(cmdContext(cmd), apiclient.CITokenRequest{Token: raw})
	switch {
	case errors.Is(err, apiclient.ErrCITokenNotServed):
		return fmt.Errorf("%s does not accept CI OIDC tokens (no auth.ci_oidc issuer, or a release before %s): "+
			"use a deploy token in DEXAFLOW_TOKEN instead", choice.URL, ciOIDCMinVersion)
	case err != nil:
		return fmt.Errorf("exchanging the CI token with %s: %w", choice.URL, err)
	case s.AccessToken == "":
		return fmt.Errorf("%s answered no session", choice.URL)
	}
	//nolint:errcheck // informational, on stderr so stdout holds the session alone
	fmt.Fprintf(cmd.ErrOrStderr(), "CI session from issuer %s: role %s of tenant %s, scopes %s, until %s\n",
		printable(s.Issuer), printable(s.Role), printable(s.Tenant), printable(strings.Join(s.Scopes, " ")), s.ExpiresAt.UTC().Format(time.RFC3339))
	_, err = fmt.Fprintln(cmd.OutOrStdout(), s.AccessToken)
	return err
}
