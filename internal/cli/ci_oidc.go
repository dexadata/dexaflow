package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"time"

	"github.com/spf13/cobra"

	apiclient "github.com/dexadata/dexaflow/pkg/client"
)

// The --auth modes of `ci init`: how the pipeline signs in to the control
// plane (ADR 0092).
const (
	// ciAuthAuto writes a tokenless pipeline when the server offers a CI
	// OIDC issuer for the system, the deploy token path otherwise.
	ciAuthAuto = "auto"
	// ciAuthOIDC writes a tokenless pipeline, or fails.
	ciAuthOIDC = "oidc"
	// ciAuthToken writes the deploy token path without asking the server.
	ciAuthToken = "token"
)

// ciOIDCProviders maps each system ci init writes a pipeline for to the
// provider of the issuer entries its jobs' tokens match. generic has none:
// no CI provider mints a token for it.
var ciOIDCProviders = map[string]string{"github": "github", "gitlab": "gitlab", "cloudbuild": "cloudbuild"}

// ciAudienceRe is an audience the pipelines can write into YAML and shell
// without quoting it again.
var ciAudienceRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/@-]{0,254}$`)

// ciIssuerNameRe is an issuer entry name the pipelines can write into a
// comment.
var ciIssuerNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,99}$`)

// ciOIDC is the issuer entry a tokenless pipeline signs in through.
type ciOIDC struct {
	// Server is the control plane that offered the entry.
	Server string
	// Name is the entry's name in the server's auth.ci_oidc.
	Name string
	// Audience is the audience the job asks its provider's token for.
	Audience string
}

// chooseCIAuth decides how the pipeline of system signs in: the issuer entry
// a tokenless pipeline uses, or nil for the deploy token path. With
// --auth auto a reason not to use OIDC is a note on stderr; with --auth oidc
// it is an error.
func chooseCIAuth(cmd *cobra.Command, system, cliVersion, mode string) (*ciOIDC, error) {
	switch mode {
	case ciAuthToken:
		return nil, nil //nolint:nilnil // the token path is a normal outcome
	case ciAuthAuto, ciAuthOIDC:
	default:
		return nil, fmt.Errorf("--auth %q: use auto, oidc or token", mode)
	}
	strict := mode == ciAuthOIDC
	provider, ok := ciOIDCProviders[system]
	if !ok {
		if strict {
			return nil, errors.New("--auth oidc: the generic pipeline runs on no CI provider that mints a token; " +
				"write it for github, gitlab or cloudbuild, or use --auth token")
		}
		return nil, nil //nolint:nilnil // the token path is a normal outcome
	}
	choice, token, err := resolveServerChoiceToken(cmd, "", "")
	if err != nil {
		return nil, err
	}
	if choice.Implicit {
		if strict {
			return nil, errors.New("--auth oidc: no control plane to ask for its CI OIDC issuers; set DEXAFLOW_SERVER_URL or a workspace target")
		}
		return nil, nil //nolint:nilnil // nothing to ask: the token path, as before
	}
	is, why := offeredCIIssuer(cmdContext(cmd), cmd.ErrOrStderr(), choice.URL, token, provider)
	if is != nil && semverLess(cliVersion, ciOIDCMinVersion) {
		is, why = nil, fmt.Sprintf("the pipeline installs dexaflow %s, and `dexaflow auth ci-token` ships from %s", cliVersion, ciOIDCMinVersion)
	}
	if is != nil {
		return is, nil
	}
	if strict {
		return nil, fmt.Errorf("--auth oidc: %s", why)
	}
	//nolint:errcheck // advisory
	fmt.Fprintf(cmd.ErrOrStderr(), "note: %s; the pipeline signs in with a deploy token\n", why)
	return nil, nil //nolint:nilnil // the token path is a normal outcome
}

// offeredCIIssuer is the first issuer entry for provider that serverURL
// offers the CLI's tenant, or nil and why not.
func offeredCIIssuer(ctx context.Context, warn io.Writer, serverURL, token, provider string) (offered *ciOIDC, why string) {
	issuers, err := fetchCIOIDCIssuers(ctx, serverURL, token)
	if err != nil {
		return nil, err.Error()
	}
	var found []apiclient.DeployCIOIDCIssuer
	for _, is := range issuers {
		if is.Provider == provider {
			found = append(found, is)
		}
	}
	if len(found) == 0 {
		return nil, fmt.Sprintf("%s offers no CI OIDC issuer for %s to your tenant", serverURL, provider)
	}
	is := found[0]
	if !ciIssuerNameRe.MatchString(is.Name) || !ciAudienceRe.MatchString(is.Audience) {
		return nil, fmt.Sprintf("the CI OIDC issuer %q (audience %q) of %s cannot be written into the pipeline",
			printable(is.Name), printable(is.Audience), serverURL)
	}
	if len(found) > 1 {
		//nolint:errcheck // advisory
		fmt.Fprintf(warn, "note: %s offers %d CI OIDC issuers for %s; the pipeline uses the first, %s\n", serverURL, len(found), provider, is.Name)
	}
	return &ciOIDC{Server: serverURL, Name: is.Name, Audience: is.Audience}, ""
}

// fetchCIOIDCIssuers reads the CI OIDC issuers serverURL lists in its
// deploy defaults for the tenant of token.
func fetchCIOIDCIssuers(ctx context.Context, serverURL, token string) ([]apiclient.DeployCIOIDCIssuer, error) {
	c, err := cliBareAPIClient(serverURL, 15*time.Second)
	if err != nil {
		return nil, fmt.Errorf("building the client: %w", err)
	}
	got, err := c.GetDeployDefaults(ctx, func(_ context.Context, req *http.Request) error {
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		return nil
	})
	if errors.Is(err, apiclient.ErrDeployDefaultsNotServed) {
		return nil, fmt.Errorf("%s answers 404 on its deploy defaults (a release before %s offers no CI OIDC issuer)", serverURL, ciOIDCMinVersion)
	}
	if err != nil {
		return nil, fmt.Errorf("could not read the CI OIDC issuers of %s (sign in with `dexaflow auth login` first): %w", serverURL, err)
	}
	if got.CIOIDC == nil {
		return nil, nil
	}
	return got.CIOIDC.Issuers, nil
}
