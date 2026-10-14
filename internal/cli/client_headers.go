package cli

import (
	"net/http"
	"os"
	"strings"

	"github.com/dexadata/dexaflow/internal/version"
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
