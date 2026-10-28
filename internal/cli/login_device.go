package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"runtime"
	"strings"
	"time"

	"golang.org/x/term"

	apiclient "github.com/dexadata/dexaflow/pkg/client"
)

// The device sign-in of `dexaflow auth login --device` (#1704, ADR 0081),
// for a machine with no browser, or whose loopback the browser cannot
// reach: an SSH session, a container. The CLI asks the server for a device
// code and a short user code, prints the page and the code, and polls the
// server while the person opens the page in a browser anywhere, signs in
// with the cluster's single sign-on and confirms the code. The server stays
// the only OIDC client, as for the browser sign-in; the CLI never talks to
// the IdP. The device code is a secret and is never printed.

// deviceSlowDown is what a slow_down adds to the interval (RFC 8628).
const deviceSlowDown = 5 * time.Second

// defaultDeviceInterval is the interval when the server names none.
const defaultDeviceInterval = 5 * time.Second

// headlessHere reports whether this session looks like it has no browser to
// open, and why. Commands reach it as cliDeps.sessionHeadless.
func headlessHere() (headless bool, why string) { return headlessSession(os.Getenv, runtime.GOOS) }

// writerIsTerminal reports whether the command's output is a terminal, where
// a person can read a printed code. Commands reach it as
// cliDeps.outputIsTerminal.
func writerIsTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	return ok && term.IsTerminal(int(f.Fd()))
}

// headlessSession decides from the environment whether a browser opened here
// would be seen by the person: not over SSH, where it would open on the
// remote machine if at all, and not on Linux or a BSD with no X11 or Wayland
// display, as in a container. macOS and Windows always have one, and so does
// a session that names one in BROWSER, the variable openers such as
// xdg-open honor.
func headlessSession(getenv func(string) string, goos string) (headless bool, why string) {
	if getenv("BROWSER") != "" {
		return false, ""
	}
	if getenv("SSH_CONNECTION") != "" || getenv("SSH_TTY") != "" {
		return true, "an SSH session"
	}
	switch goos {
	case "darwin", "windows":
		return false, ""
	}
	if getenv("DISPLAY") == "" && getenv("WAYLAND_DISPLAY") == "" {
		return true, "no display (DISPLAY and WAYLAND_DISPLAY are unset)"
	}
	return false, ""
}

// deviceLogin runs the device sign-in against serverURL and returns the
// Dexaflow token. It gives up after timeout or when the sign-in expires,
// whichever comes first.
func deviceLogin(ctx context.Context, out io.Writer, serverURL string, timeout time.Duration) (string, error) {
	c, err := cliBareAPIClient(serverURL, 30*time.Second)
	if err != nil {
		return "", fmt.Errorf("building the client for %s: %w", serverURL, err)
	}
	d, err := c.StartCLIDeviceLogin(ctx)
	if errors.Is(err, apiclient.ErrDeviceLoginNotServed) {
		return "", fmt.Errorf("%s does not serve the device sign-in; sign in with --sso and a browser, or use an API token", serverURL)
	}
	if err != nil {
		return "", fmt.Errorf("starting the device sign-in at %s: %w", serverURL, err)
	}
	life := time.Duration(d.ExpiresIn) * time.Second
	if timeout <= 0 {
		timeout = defaultSSOTimeout
	}
	if life > 0 && life < timeout {
		timeout = life
	}
	warnOtherHost(out, serverURL, d.VerificationURI)
	devPrintf(out, "To sign in, open this page in a browser on any device:\n\n  %s\n\nsign in, and enter the code:\n\n  %s\n\n"+
		"Or open %s, which fills in the code.\n"+
		"Approve only a page that shows this same code. Waiting up to %s...\n",
		d.VerificationURI, d.UserCode, d.VerificationURIComplete, timeout.Round(time.Second))
	return pollDeviceLogin(ctx, c, serverURL, d, timeout)
}

// warnOtherHost warns when the page to open is not on the server's host: the
// server's public address (its OIDC redirect URL) may be misconfigured, and
// the person should know which site they are about to sign in to.
func warnOtherHost(out io.Writer, serverURL, page string) {
	s, serr := url.Parse(serverURL)
	p, perr := url.Parse(page)
	if serr != nil || perr != nil || strings.EqualFold(s.Hostname(), p.Hostname()) {
		return
	}
	devPrintf(out, "warning: the sign-in page is on %s, not on %s, the server this login talks to; "+
		"open it only if %s is this Dexaflow's public address\n", p.Hostname(), s.Hostname(), p.Hostname())
}

// pollDeviceLogin polls until the sign-in is approved, refused or expired,
// the timeout passes, or ctx ends. A poll that fails on the way (the network,
// a 5xx) is retried at the next interval.
func pollDeviceLogin(ctx context.Context, c *apiclient.Client, serverURL string, d *apiclient.CLIDeviceAuthorization, timeout time.Duration) (string, error) {
	interval := time.Duration(d.Interval) * time.Second
	if interval <= 0 {
		interval = defaultDeviceInterval
	}
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	wait := depsFrom(ctx).devicePollWait
	var lastErr error
	for {
		select {
		case <-ctx.Done():
			return "", fmt.Errorf("single sign-on canceled: %w", ctx.Err())
		case <-deadline.C:
			return "", deviceTimeout(timeout, lastErr)
		case <-wait(interval):
		}
		tok, err := c.PollCLIDeviceLogin(ctx, d.DeviceCode)
		switch {
		case err == nil:
			if tok.AccessToken == nil || *tok.AccessToken == "" {
				return "", fmt.Errorf("completing single sign-on at %s: the server returned no access_token", serverURL)
			}
			return *tok.AccessToken, nil
		case errors.Is(err, apiclient.ErrDeviceAuthorizationPending):
		case errors.Is(err, apiclient.ErrDeviceSlowDown):
			interval += deviceSlowDown
		case errors.Is(err, apiclient.ErrDeviceAccessDenied):
			return "", errors.New("single sign-on was canceled in the browser")
		case errors.Is(err, apiclient.ErrDeviceExpired):
			return "", errors.New("the sign-in expired before it was approved; run dexaflow auth login --device again")
		case errors.Is(err, apiclient.ErrCLILoginCodeRefused):
			return "", errors.New("the server no longer knows this sign-in; run dexaflow auth login --device again")
		default:
			lastErr = err
		}
	}
}

// deviceTimeout is the error of a sign-in nobody approved in time, with the
// last failed poll when there was one.
func deviceTimeout(timeout time.Duration, lastErr error) error {
	msg := fmt.Sprintf("single sign-on timed out after %s waiting for the approval; run dexaflow auth login --device again", timeout)
	if lastErr != nil {
		return fmt.Errorf("%s (last poll: %w)", msg, lastErr)
	}
	return errors.New(msg)
}
