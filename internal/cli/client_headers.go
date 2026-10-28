package cli

import (
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/dexadata/dexaflow/internal/version"
	apiclient "github.com/dexadata/dexaflow/pkg/client"
)

// ciHeader is the header the CLI sets to "true" on its requests to a control
// plane when it runs in a pipeline (#1772), so a server, or a platform in
// front of it, can tell an interactive deploy from an automated one.
const ciHeader = "X-Dexaflow-CI"

// ciEnvVars are the variables the usual CI systems set: CI (most of them),
// GITHUB_ACTIONS and GITLAB_CI.
var ciEnvVars = []string{"CI", "GITHUB_ACTIONS", "GITLAB_CI"}

// cliUserAgent is the User-Agent of every request the CLI sends to a
// control plane: dexaflow-cli/<version>.
func cliUserAgent() string {
	return "dexaflow-cli/" + version.Get().Version
}

// inCI reports whether one of ciEnvVars is set to anything but empty,
// "false" or "0".
func inCI() bool {
	for _, k := range ciEnvVars {
		switch strings.ToLower(strings.TrimSpace(os.Getenv(k))) {
		case "", "false", "0":
		default:
			return true
		}
	}
	return false
}

// setClientHeaders sets the headers that name the CLI on a request to a
// control plane: its User-Agent, and ciHeader when it runs in CI.
func setClientHeaders(h http.Header) {
	h.Set("User-Agent", cliUserAgent())
	if inCI() {
		h.Set(ciHeader, "true")
	}
}

// cliTransport sets the headers that name the CLI (setClientHeaders) on a
// copy of every request, then sends it with base. Every client the CLI
// builds goes through it (cliHTTPClient), so no request can leave without
// them.
type cliTransport struct {
	base http.RoundTripper
}

// RoundTrip sends a copy of req with the CLI's headers; req itself is left
// as it is, as http.RoundTripper requires.
func (t cliTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	r := req.Clone(req.Context())
	setClientHeaders(r.Header)
	return t.base.RoundTrip(r)
}

// cliHTTPClient is the HTTP client of every request the CLI sends to a
// control plane, with the given timeout (0 for none): http.DefaultTransport
// behind cliTransport.
func cliHTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout, Transport: cliTransport{base: http.DefaultTransport}}
}

// cliAPIClient is apiclient.New on cliHTTPClient, with no timeout unless
// opts give another cliHTTPClient.
func cliAPIClient(baseURL, token string, opts ...apiclient.ClientOption) (*apiclient.ClientWithResponses, error) {
	all := append([]apiclient.ClientOption{apiclient.WithHTTPClient(cliHTTPClient(0))}, opts...)
	return apiclient.New(baseURL, token, all...)
}

// cliBareAPIClient is apiclient.NewClient, with no bearer token, on
// cliHTTPClient with the given timeout.
func cliBareAPIClient(serverURL string, timeout time.Duration) (*apiclient.Client, error) {
	return apiclient.NewClient(serverURL, apiclient.WithHTTPClient(cliHTTPClient(timeout)))
}
