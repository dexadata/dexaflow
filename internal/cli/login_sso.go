package cli

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"

	"golang.org/x/oauth2"

	apiclient "github.com/dexadata/dexaflow/pkg/client"
)

// Single sign-on for `dexaflow auth login` (#1704). The control plane stays
// the only OIDC client: the CLI opens the server's own sign-in route in the
// browser with a loopback redirect, the person approves the CLI sign-in on
// the server's consent page, and the CLI waits on the loopback for the
// one-time code that follows and exchanges it, with the PKCE verifier only it holds, for the token a
// password login would have saved. The CLI never talks to the IdP, so it
// needs no client ID, secret or IdP configuration of its own.

// defaultSSOTimeout is how long `auth login` waits for the browser sign-in.
const defaultSSOTimeout = 5 * time.Minute

// ssoCallbackPath is where the loopback listener receives the code.
const ssoCallbackPath = "/callback"

// systemOpenBrowser starts the platform's URL opener and does not wait for
// the browser. Commands reach it as cliDeps.openBrowser, which tests
// replace.
func systemOpenBrowser(target string) error {
	// Only a web URL reaches the opener: rundll32 and xdg-open would hand
	// anything else (a file path, another scheme) to whatever handles it.
	if !isWebURL(target) {
		return fmt.Errorf("refusing to open %q: not an http or https URL", target)
	}
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.CommandContext(context.Background(), "open", target) //nolint:gosec // G204: a fixed opener; the URL is one argument, never a shell string
	case "windows":
		cmd = exec.CommandContext(context.Background(), "rundll32", "url.dll,FileProtocolHandler", target) //nolint:gosec // G204: a fixed opener; the URL is one argument, never a shell string
	default:
		cmd = exec.CommandContext(context.Background(), "xdg-open", target) //nolint:gosec // G204: a fixed opener; the URL is one argument, never a shell string
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting %s: %w", cmd.Path, err)
	}
	go func() { _ = cmd.Wait() }() //nolint:errcheck // the opener's exit says nothing about the sign-in
	return nil
}

// isWebURL reports whether target is an absolute http or https URL.
func isWebURL(target string) bool {
	u, err := url.Parse(target)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

// ssoOptions are the flags of a browser sign-in.
type ssoOptions struct {
	noBrowser bool
	timeout   time.Duration
}

// loopbackResult is what the loopback listener received.
type loopbackResult struct {
	code string
	err  error
}

// ssoLogin runs the browser sign-in against serverURL and returns the
// Dexaflow token. Nothing it prints or returns contains the code or the token.
func ssoLogin(ctx context.Context, out io.Writer, serverURL string, opt ssoOptions) (string, error) {
	verifier := oauth2.GenerateVerifier()
	state, err := randomState()
	if err != nil {
		return "", err
	}
	// 127.0.0.1 rather than localhost, which can resolve elsewhere; port 0
	// for a free one. The server refuses any redirect that is not loopback.
	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		return "", fmt.Errorf("listening for the sign-in redirect: %w", err)
	}
	redirect := fmt.Sprintf("http://%s%s", ln.Addr().String(), ssoCallbackPath)
	results := make(chan loopbackResult, 1)
	srv := &http.Server{Handler: loopbackHandler(state, results), ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = srv.Serve(ln) }() //nolint:errcheck // Serve returns ErrServerClosed on Shutdown
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx) //nolint:errcheck // best-effort teardown of the loopback listener
	}()

	signIn := ssoSignInURL(serverURL, redirect, state, oauth2.S256ChallengeFromVerifier(verifier))
	announceSignIn(ctx, out, signIn, opt.noBrowser)

	code, err := waitForCode(ctx, results, opt.timeout)
	if err != nil {
		return "", err
	}
	return exchangeSSOCode(ctx, serverURL, code, verifier, redirect)
}

// announceSignIn opens the browser unless told not to, and always prints the
// URL so a person can open it when no browser starts.
func announceSignIn(ctx context.Context, out io.Writer, signIn string, noBrowser bool) {
	if noBrowser {
		devPrintf(out, "Open this URL in a browser to sign in:\n\n  %s\n\nWaiting for the sign-in...\n", signIn)
		return
	}
	devPrintf(out, "Opening your browser to sign in. If it does not open, visit:\n\n  %s\n\nWaiting for the sign-in...\n", signIn)
	if err := depsFrom(ctx).openBrowser(signIn); err != nil {
		devPrintf(out, "Could not open a browser (%v); open the URL above.\n", err)
	}
}

// ssoSignInURL is the server's sign-in route with the CLI's parameters.
func ssoSignInURL(serverURL, redirect, state, challenge string) string {
	q := url.Values{}
	q.Set("redirect_uri", redirect)
	q.Set("state", state)
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")
	return strings.TrimRight(serverURL, "/") + "/api/v2/auth/oidc/login?" + q.Encode()
}

// waitForCode waits for the loopback to receive a code, the context to end,
// or the timeout.
func waitForCode(ctx context.Context, results <-chan loopbackResult, timeout time.Duration) (string, error) {
	if timeout <= 0 {
		timeout = defaultSSOTimeout
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case r := <-results:
		return r.code, r.err
	case <-timer.C:
		return "", fmt.Errorf("single sign-on timed out after %s waiting for the browser; run dexaflow auth login again", timeout)
	case <-ctx.Done():
		return "", fmt.Errorf("single sign-on canceled: %w", ctx.Err())
	}
}

// loopbackHandler answers the browser's redirect. Only a request on the
// callback path that carries the CLI's own state counts: anything else (a
// port scan, a forged link, a stray local process) gets a 400 and the login
// keeps waiting, so it cannot be aborted from outside. The first request with
// the right state decides the result, exactly once however many arrive
// together; later ones are told the sign-in already completed.
func loopbackHandler(state string, results chan<- loopbackResult) http.Handler {
	var once sync.Once
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != ssoCallbackPath {
			http.NotFound(w, r)
			return
		}
		q := r.URL.Query()
		if subtle.ConstantTimeCompare([]byte(q.Get("state")), []byte(state)) != 1 {
			http.Error(w, "This is not the sign-in this dexaflow auth login is waiting for.", http.StatusBadRequest)
			return
		}
		first := false
		var res loopbackResult
		once.Do(func() {
			first = true
			res = readLoopback(q)
			results <- res
		})
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		switch {
		case !first:
			http.Error(w, "This sign-in has already completed.", http.StatusBadRequest)
		case res.err != nil:
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, "Dexaflow sign-in did not complete. Return to the terminal for details.\n") //nolint:errcheck // best-effort page
		default:
			_, _ = io.WriteString(w, "Signed in to Dexaflow. You can close this window and return to the terminal.\n") //nolint:errcheck // best-effort page
		}
	})
}

// readLoopback turns the query of a redirect with the right state into a
// code or a refusal.
func readLoopback(q url.Values) loopbackResult {
	switch q.Get("error") {
	case "":
	case "server_error":
		return loopbackResult{err: errors.New("single sign-on failed on the server; try again, and if it keeps failing the server log has the cause")}
	default:
		return loopbackResult{err: errors.New("single sign-on was refused; the reason is in the server log and audit trail, ask whoever administers this Dexaflow")}
	}
	if q.Get("code") == "" {
		return loopbackResult{err: errors.New("single sign-on refused: the redirect carried no code")}
	}
	return loopbackResult{code: q.Get("code")}
}

// exchangeSSOCode redeems the one-time code for the Dexaflow token.
func exchangeSSOCode(ctx context.Context, serverURL, code, verifier, redirect string) (string, error) {
	c, err := cliBareAPIClient(serverURL, 30*time.Second)
	if err != nil {
		return "", fmt.Errorf("building the client for %s: %w", serverURL, err)
	}
	tok, err := c.ExchangeCLILoginCode(ctx, apiclient.CLITokenRequest{Code: code, CodeVerifier: verifier, RedirectURI: redirect})
	if err != nil {
		return "", fmt.Errorf("completing single sign-on at %s: %w", serverURL, err)
	}
	if tok.AccessToken == nil || *tok.AccessToken == "" {
		return "", fmt.Errorf("completing single sign-on at %s: the server returned no access_token", serverURL)
	}
	return *tok.AccessToken, nil
}

// serverMethods asks the server which sign-in methods it offers. A server
// that predates the question, or cannot be asked, is treated as offering
// none but the password: the password login then runs, and reports its own
// errors.
func serverMethods(ctx context.Context, serverURL string) apiclient.AuthMethods {
	c, err := cliBareAPIClient(serverURL, 15*time.Second)
	if err != nil {
		return apiclient.AuthMethods{}
	}
	m, err := c.GetAuthMethods(ctx)
	if err != nil || m == nil {
		return apiclient.AuthMethods{}
	}
	return *m
}

// randomState is the CLI's own state for one sign-in.
func randomState() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generating the sign-in state: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
