package cli

import (
	"fmt"
	"strings"
)

// resolvedTarget records what the last resolveServerToken call decided, so
// apiStatusError can explain a 401 without every command having to thread the
// comparison through. A CLI process runs exactly one command, and every command
// resolves before it calls, so a package-level record is the whole lifetime of
// the fact rather than shared mutable state in the usual sense. Tests set it
// directly.
var resolvedTarget struct {
	server     string // the control plane being called
	configured string // server_url in the config file, when one was read
	fromConfig bool   // the token came from the config file, not a flag or env
}

// apiStatusError is the one place a non-2xx from the control plane becomes an
// error, so the 401 explanation lives here instead of at fifteen call sites.
func apiStatusError(status int, body []byte) error {
	err := fmt.Errorf("server returned %d: %s", status, string(body))
	return tokenServerHint(err, resolvedTarget.server, resolvedTarget.configured, resolvedTarget.fromConfig)
}

// loginCommandPath is the command the hint tells the user to run. It is a
// constant rather than a literal in the format string so a test can resolve it
// against the real command tree — the first version of this hint printed
// "leoflow login", which does not exist (the command is under `auth`), and the
// test asserted that same wrong literal, so the test locked the defect in.
const loginCommandPath = "dexaflow auth login"

// tokenServerHint turns a bare 401 into something actionable when the token was
// read from the config file and the command is talking to a DIFFERENT control
// plane than the one that token was persisted for.
//
// The config stores `server_url` and `token` together — internal/config/persist.go
// writes both keys in the same call — so the CLI has always known which server it
// minted a token for. It just never said so: pointing a command at another server
// with --server carried the persisted token along, that server rejected it, and
// the user saw a 401 with no way to tell an expired token from a token that
// belongs somewhere else (#1102).
//
// It stays silent in every case where a mismatch is not what happened: a non-401,
// a token the user supplied explicitly, no config server to compare against, or
// two spellings of the same server. A hint that fires when nothing is wrong sends
// people chasing the wrong thing, which is worse than the bare 401 it replaces.
//
// This decorates the error rather than being a new pattern: hintEmailUsername
// already does the same for the login flow's 401.
func tokenServerHint(err error, effectiveServer, configServer string, tokenFromConfig bool) error {
	if err == nil || !tokenFromConfig || configServer == "" {
		return err
	}
	// Match the shape the clients format ("server returned 401: ..."), not a bare
	// "401" that a host:port or a response body could contain by accident.
	if !strings.Contains(err.Error(), "server returned 401") {
		return err
	}
	if sameServer(effectiveServer, configServer) {
		return err
	}
	return fmt.Errorf("%w\nhint: the saved token was issued for %s, and this command is calling %s.\n      Log in to that server first:  %s --server %s",
		err, configServer, effectiveServer, loginCommandPath, effectiveServer)
}

// sameServer reports whether two base URLs name the same control plane. Only
// trailing slashes are normalized: anything cleverer (default ports, host
// casing) would risk calling two genuinely different servers the same, and the
// cost of a missed hint is the status quo while the cost of a wrong one is
// sending someone to re-authenticate against a server that was never the
// problem.
func sameServer(a, b string) bool {
	return strings.TrimRight(a, "/") == strings.TrimRight(b, "/")
}
