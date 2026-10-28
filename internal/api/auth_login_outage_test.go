package api

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// loginPageOutageMessage is what the sign-in page says when POST /auth/token
// answers 503: the credential store could not be reached, so nothing is known
// about the password.
const loginPageOutageMessage = "Sign-in is temporarily unavailable. Try again shortly."

// TestLoginPageSaysAnOutageIsNotAWrongPassword is the sign-in page's half of
// #1099. POST /auth/token answers 503 when the user store cannot be reached
// (#843), but the page showed "Invalid credentials" for every answer other
// than 429, so during an outage a user with the right password was told it was
// wrong, and typed it again.
func TestLoginPageSaysAnOutageIsNotAWrongPassword(t *testing.T) {
	body := anonGet(loginServer(), "/api/v2/auth/login").Body.String()
	if !strings.Contains(body, loginPageOutageMessage) {
		t.Fatalf("the login page has no outage message for a 503:\n%s", body)
	}

	// Run the page's own script against a stubbed browser, so the test locks
	// which message each status shows, not only that the text is present.
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed; the message mapping is checked by text only")
	}
	script := pageScript(t, body)
	for status, want := range map[int]string{
		503: loginPageOutageMessage,
		401: "Invalid credentials",
		429: "Too many attempts. Wait about a minute, then try again.",
	} {
		if got := runLoginScript(t, node, script, status); got != want {
			t.Errorf("POST /auth/token answering %d shows %q, want %q", status, got, want)
		}
	}
}

// pageScript returns the body of the page's only inline script.
func pageScript(t *testing.T, page string) string {
	t.Helper()
	start := strings.Index(page, "<script>")
	end := strings.Index(page, "</script>")
	if start < 0 || end < start {
		t.Fatal("the login page has no inline script")
	}
	return page[start+len("<script>") : end]
}

// loginHarness stubs the few browser objects the sign-in script touches,
// submits the form once with fetch answering STATUS, and prints the message
// the page shows.
const loginHarness = `
const els = {e: {textContent: ''}};
let submit;
els.f = {addEventListener: (_, fn) => { submit = fn; }};
globalThis.f = {username: {value: 'admin'}, password: {value: 'right'}};
globalThis.document = {getElementById: (id) => els[id]};
globalThis.window = {location: {replace() {}}};
globalThis.fetch = async () => ({ok: STATUS < 300, status: STATUS});
SCRIPT
submit({preventDefault() {}}).then(() => console.log(els.e.textContent));
`

// runLoginScript runs script under loginHarness with fetch answering status
// and returns the message the page shows.
func runLoginScript(t *testing.T, node, script string, status int) string {
	t.Helper()
	src := strings.NewReplacer("STATUS", strconv.Itoa(status), "SCRIPT", script).Replace(loginHarness)
	path := filepath.Join(t.TempDir(), "login.js")
	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		t.Fatalf("writing the harness: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, node, path).CombinedOutput()
	if err != nil {
		t.Fatalf("running the login script: %v\n%s", err, out)
	}
	return strings.TrimSpace(string(out))
}
