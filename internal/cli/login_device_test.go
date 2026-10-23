package cli

import (
	"context"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/domain"
)

// memDevices is the server's device store in memory, for driving the real
// API server from the CLI tests.
type memDevices struct {
	mu   sync.Mutex
	rows map[string]*memDevice
	seq  int
}

type memDevice struct {
	id, userHash, status string
	user                 auth.User
}

func (m *memDevices) CreateCLIDeviceAuthorization(_ context.Context, req auth.DeviceAuthorizationRequest) (auth.DeviceAuthorization, error) {
	deviceHash, userHash, ttl := req.DeviceCodeHash, req.UserCodeHash, req.TTL
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.rows == nil {
		m.rows = map[string]*memDevice{}
	}
	m.seq++
	id := "0b6c9a52-3f0e-4c55-9d6f-00000000000" + string(rune('0'+m.seq%10))
	m.rows[deviceHash] = &memDevice{id: id, userHash: userHash, status: "pending"}
	return auth.DeviceAuthorization{ID: id, ExpiresAt: time.Now().Add(ttl)}, nil
}

func (m *memDevices) FindCLIDeviceByUserCode(_ context.Context, userHash string) (auth.DeviceAuthorization, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, d := range m.rows {
		if d.userHash == userHash && d.status == "pending" {
			return auth.DeviceAuthorization{ID: d.id}, nil
		}
	}
	return auth.DeviceAuthorization{}, domain.ErrNotFound
}

func (m *memDevices) DecideCLIDeviceAuthorization(_ context.Context, id string, approve bool, user auth.User) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, d := range m.rows {
		if d.id == id && d.status == "pending" {
			d.status, d.user = "denied", user
			if approve {
				d.status = "approved"
			}
			return nil
		}
	}
	return domain.ErrNotFound
}

func (m *memDevices) PollCLIDeviceAuthorization(_ context.Context, deviceHash string) (auth.DevicePoll, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := m.rows[deviceHash]
	if !ok {
		return auth.DevicePoll{Outcome: auth.DevicePollUnknown}, nil
	}
	switch d.status {
	case "approved":
		delete(m.rows, deviceHash)
		return auth.DevicePoll{Outcome: auth.DevicePollApproved, User: d.user}, nil
	case "denied":
		delete(m.rows, deviceHash)
		return auth.DevicePoll{Outcome: auth.DevicePollDenied}, nil
	}
	return auth.DevicePoll{Outcome: auth.DevicePollPending}, nil
}

// fastDevicePolls makes the commands run under d wait 10ms where the server
// asks for an interval, and records each interval they were asked to wait.
func fastDevicePolls(t *testing.T, d *cliDeps) *[]time.Duration {
	t.Helper()
	var mu sync.Mutex
	waits := &[]time.Duration{}
	d.devicePollWait = func(wait time.Duration) <-chan time.Time {
		mu.Lock()
		*waits = append(*waits, wait)
		mu.Unlock()
		return time.After(10 * time.Millisecond)
	}
	return waits
}

// headless makes the session of the commands run under d look like one with
// no browser, or not, and its output a terminal.
func headless(t *testing.T, d *cliDeps, yes bool) {
	t.Helper()
	interactive(t, d, true)
	d.sessionHeadless = func() (bool, string) {
		if yes {
			return true, "an SSH session"
		}
		return false, ""
	}
}

// interactive makes the output of the commands run under d look like a
// terminal, or not.
func interactive(t *testing.T, d *cliDeps, yes bool) {
	t.Helper()
	d.outputIsTerminal = func(io.Writer) bool { return yes }
}

var (
	printedVerifyURI = regexp.MustCompile(`(?m)^\s+(http://\S+/api/v2/auth/device)\s*$`)
	printedUserCode  = regexp.MustCompile(`(?m)^\s+([BCDFGHJKLMNPQRSTVWXZ]{4}-[BCDFGHJKLMNPQRSTVWXZ]{4})\s*$`)
)

// deviceWatcher is a terminal read by a person on another machine: once the
// page and the code are printed, they open the page in their own browser,
// type the code, sign in, confirm the code and approve.
type deviceWatcher struct {
	mu   sync.Mutex
	buf  strings.Builder
	seen bool
	act  func(verifyURI, userCode string)
}

func (w *deviceWatcher) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n, err := w.buf.Write(p)
	page, code := printedVerifyURI.FindStringSubmatch(w.buf.String()), printedUserCode.FindStringSubmatch(w.buf.String())
	if page != nil && code != nil && !w.seen {
		w.seen = true
		go w.act(page[1], code[1])
	}
	return n, err
}

func (w *deviceWatcher) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

// approveOnAnotherDevice is the person's browser on another machine.
func approveOnAnotherDevice(t *testing.T, verifyURI, userCode string) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Error(err)
		return
	}
	client := &http.Client{Jar: jar, Timeout: 10 * time.Second}
	post := func(target string, form url.Values) (landed *url.URL, page string) {
		req, rerr := http.NewRequestWithContext(context.Background(), http.MethodPost, target, strings.NewReader(form.Encode()))
		if rerr != nil {
			t.Error(rerr)
			return nil, ""
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Sec-Fetch-Site", "same-origin")
		resp, derr := client.Do(req)
		if derr != nil {
			t.Errorf("browser: %v", derr)
			return nil, ""
		}
		defer func() { _ = resp.Body.Close() }()
		body, _ := io.ReadAll(resp.Body)
		return resp.Request.URL, string(body)
	}
	// The page starts the sign-in; the redirects lead through the IdP to the
	// page that asks for the code, and a live code to the consent page.
	landed, page := post(verifyURI, url.Values{})
	if landed == nil {
		return
	}
	m := consentCSRF.FindStringSubmatch(page)
	if m == nil || !strings.Contains(page, `name="user_code"`) {
		t.Errorf("the sign-in did not end on the page that asks for the code:\n%s", page)
		return
	}
	entry := landed.ResolveReference(&url.URL{Path: "/api/v2/auth/cli/device/code"}).String()
	landed, page = post(entry, url.Values{"csrf": {m[1]}, "user_code": {strings.ToLower(userCode)}})
	if landed == nil {
		return
	}
	m = consentCSRF.FindStringSubmatch(page)
	if m == nil || !strings.Contains(page, userCode) {
		t.Errorf("the consent page does not show the code %s with a form:\n%s", userCode, page)
		return
	}
	consent := landed.ResolveReference(&url.URL{Path: "/api/v2/auth/cli/device/consent"}).String()
	post(consent, url.Values{"csrf": {m[1]}, "decision": {"approve"}, "confirm": {"yes"}})
}

func TestLoginDeviceFlowSavesTheServerToken(t *testing.T) {
	d := testDeps()
	t.Setenv("LEOFLOW_USERNAME", "")
	t.Setenv("LEOFLOW_PASSWORD", "")
	fastDevicePolls(t, d)
	idp := newSSOIDP(t)
	cp := newSSOControlPlaneWith(t, idp, &memDevices{})
	opened := fakeBrowser(t, d, func(string) {})
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	out := &deviceWatcher{act: func(page, code string) { approveOnAnotherDevice(t, page, code) }}

	root := NewRootCommand()
	root.SetOut(out)
	root.SetErr(io.Discard)
	root.SetArgs([]string{"auth", "login", "--device", "--server", cp.URL, "--config", cfgPath, "--timeout", "20s"})
	if err := root.ExecuteContext(testCtx(d)); err != nil {
		t.Fatalf("login: %v\n%s", err, out.String())
	}
	if len(*opened) != 0 {
		t.Errorf("--device opened a browser: %v", *opened)
	}
	tok := savedToken(t, cfgPath, cp.URL)
	var claims jwt.MapClaims
	if _, perr := jwt.ParseWithClaims(tok, &claims, func(*jwt.Token) (any, error) { return []byte(ssoTestSecret), nil }); perr != nil {
		t.Fatalf("the saved token %q is not a Dexaflow token: %v\n%s", tok, perr, out.String())
	}
	if claims["sub"] != "user-42" || claims["tenant_id"] != "default" {
		t.Errorf("token claims = %v, want the SSO user in tenant default", claims)
	}
	if !strings.Contains(out.String(), "Signing in with single sign-on on another device (--device)") {
		t.Errorf("output does not say which method:\n%s", out.String())
	}
}

// deviceCLIServer is a control plane that serves the device sign-in and
// answers each poll with the next of polls, a status and a body.
func deviceCLIServer(t *testing.T, device bool, polls ...[2]string) (srv *httptest.Server, started *int) {
	t.Helper()
	return deviceCLIServerExpiring(t, device, 600, polls...)
}

// deviceCLIServerExpiring is deviceCLIServer with sign-ins that expire after
// expiresIn seconds.
func deviceCLIServerExpiring(t *testing.T, device bool, expiresIn int, polls ...[2]string) (srv *httptest.Server, started *int) {
	t.Helper()
	started = new(int)
	var mu sync.Mutex
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v2/auth/methods", func(w http.ResponseWriter, _ *http.Request) {
		writeTestJSON(w, map[string]bool{"sso": true, "password": false, "device": device})
	})
	mux.HandleFunc("/api/v2/auth/cli/device", func(w http.ResponseWriter, _ *http.Request) {
		*started++
		writeTestJSON(w, map[string]any{
			"device_code": "secret-device-code-0123456789", "user_code": "BCDF-GHJK",
			"verification_uri": "https://flow.example/api/v2/auth/device", "verification_uri_complete": "https://flow.example/api/v2/auth/device?user_code=BCDF-GHJK",
			"expires_in": expiresIn, "interval": 5,
		})
	})
	mux.HandleFunc("/api/v2/auth/cli/token", func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if len(polls) == 0 {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":"invalid_grant"}`)
			return
		}
		next := polls[0]
		polls = polls[1:]
		if next[0] == "200" {
			_, _ = io.WriteString(w, next[1])
			return
		}
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":"`+next[1]+`"}`)
	})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, started
}

var tokenAnswer = [2]string{"200", `{"access_token":"jwt-device","token_type":"bearer","expires_in":3600}`}

// With no flag and no browser on this machine, a server that serves the
// device sign-in gets it, and the command says why.
func TestLoginChoosesTheDeviceSignInWithoutABrowser(t *testing.T) {
	d := testDeps()
	fastDevicePolls(t, d)
	headless(t, d, true)
	srv, started := deviceCLIServer(t, true, [2]string{"400", "authorization_pending"}, tokenAnswer)
	opened := fakeBrowser(t, d, func(string) {})
	cfgPath := filepath.Join(t.TempDir(), "c.yaml")
	out, _, err := runDeps(t, d, "auth", "login", "--server", srv.URL, "--config", cfgPath, "--timeout", "5s")
	if err != nil {
		t.Fatalf("login: %v\n%s", err, out)
	}
	if *started != 1 || len(*opened) != 0 {
		t.Errorf("device sign-ins started %d, browsers opened %v; want one device sign-in and no browser", *started, *opened)
	}
	for _, want := range []string{"on another device", "an SSH session", "https://flow.example/api/v2/auth/device", "BCDF-GHJK", "--sso"} {
		if !strings.Contains(out, want) {
			t.Errorf("output does not mention %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "secret-device-code") {
		t.Errorf("the device code was printed:\n%s", out)
	}
	if got := savedToken(t, cfgPath, srv.URL); got != "jwt-device" {
		t.Errorf("token = %q, want jwt-device", got)
	}
}

// Output that is not a terminal (a script, a pipe) never gets the device
// sign-in by itself: nobody may be reading the code.
func TestLoginDoesNotPickTheDeviceSignInWithoutATerminal(t *testing.T) {
	d := testDeps()
	headless(t, d, true)
	interactive(t, d, false)
	srv, started := deviceCLIServer(t, true)
	opened := fakeBrowser(t, d, func(string) {})
	_, _, _ = runDeps(t, d, "auth", "login", "--server", srv.URL, "--config", filepath.Join(t.TempDir(), "c.yaml"), "--timeout", "100ms")
	if *started != 0 || len(*opened) != 1 {
		t.Errorf("device sign-ins %d, browsers opened %v; want the browser sign-in", *started, *opened)
	}
}

// A verification page on another host than the server is printed with a
// warning: the server's public address may be misconfigured, or the answer
// is not from the server the person meant.
func TestLoginDeviceWarnsWhenThePageIsOnAnotherHost(t *testing.T) {
	d := testDeps()
	fastDevicePolls(t, d)
	srv, _ := deviceCLIServer(t, true, tokenAnswer)
	out, _, err := runDeps(t, d, "auth", "login", "--device", "--server", srv.URL, "--config", filepath.Join(t.TempDir(), "c.yaml"), "--timeout", "5s")
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if !strings.Contains(out, "warning: the sign-in page is on flow.example") {
		t.Errorf("output does not warn about the other host:\n%s", out)
	}
}

// A machine with a browser keeps the browser sign-in, and so does a server
// that does not serve the device sign-in.
func TestLoginKeepsTheBrowserSignInWhenItCanWork(t *testing.T) {
	for name, tc := range map[string]struct{ headless, device bool }{
		"browser here": {false, true},
		"older server": {true, false},
	} {
		t.Run(name, func(t *testing.T) {
			d := testDeps()
			headless(t, d, tc.headless)
			srv, started := deviceCLIServer(t, tc.device)
			opened := fakeBrowser(t, d, func(string) {})
			_, _, _ = runDeps(t, d, "auth", "login", "--server", srv.URL, "--config", filepath.Join(t.TempDir(), "c.yaml"), "--timeout", "100ms")
			if *started != 0 || len(*opened) != 1 {
				t.Errorf("device sign-ins %d, browsers opened %v; want the browser sign-in", *started, *opened)
			}
		})
	}
}

func TestLoginDeviceSlowsDownWhenTold(t *testing.T) {
	d := testDeps()
	waits := fastDevicePolls(t, d)
	srv, _ := deviceCLIServer(t, true, [2]string{"400", "slow_down"}, [2]string{"400", "authorization_pending"}, tokenAnswer)
	if _, _, err := runDeps(t, d, "auth", "login", "--device", "--server", srv.URL, "--config", filepath.Join(t.TempDir(), "c.yaml"), "--timeout", "5s"); err != nil {
		t.Fatalf("login: %v", err)
	}
	if len(*waits) != 3 || (*waits)[0] != 5*time.Second || (*waits)[1] != 10*time.Second || (*waits)[2] != 10*time.Second {
		t.Errorf("waits = %v, want 5s, then 10s after slow_down", *waits)
	}
}

func TestLoginDeviceReportsHowItEnded(t *testing.T) {
	d := testDeps()
	fastDevicePolls(t, d)
	for answer, want := range map[string]string{
		"access_denied": "canceled",
		"expired_token": "expired",
		"invalid_grant": "no longer",
	} {
		srv, _ := deviceCLIServer(t, true, [2]string{"400", answer})
		_, _, err := runDeps(t, d, "auth", "login", "--device", "--server", srv.URL, "--config", filepath.Join(t.TempDir(), "c.yaml"), "--timeout", "5s")
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v, want it to say %q", answer, err, want)
		}
	}
}

func TestLoginDeviceTimesOut(t *testing.T) {
	d := testDeps()
	fastDevicePolls(t, d)
	pending := make([][2]string, 1000)
	for i := range pending {
		pending[i] = [2]string{"400", "authorization_pending"}
	}
	srv, _ := deviceCLIServer(t, true, pending...)
	_, _, err := runDeps(t, d, "auth", "login", "--device", "--server", srv.URL, "--config", filepath.Join(t.TempDir(), "c.yaml"), "--timeout", "200ms")
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("err = %v, want a timeout", err)
	}
}

// The CLI stops waiting when the sign-in expires, even before --timeout.
func TestLoginDeviceStopsWhenTheSignInExpires(t *testing.T) {
	d := testDeps()
	fastDevicePolls(t, d)
	pending := make([][2]string, 1000)
	for i := range pending {
		pending[i] = [2]string{"400", "authorization_pending"}
	}
	srv, _ := deviceCLIServerExpiring(t, true, 1, pending...)
	start := time.Now()
	_, _, err := runDeps(t, d, "auth", "login", "--device", "--server", srv.URL, "--config", filepath.Join(t.TempDir(), "c.yaml"), "--timeout", "1m")
	if err == nil || !strings.Contains(err.Error(), "timed out after 1s") || time.Since(start) > 10*time.Second {
		t.Fatalf("err = %v after %s, want a timeout at the 1s expiry", err, time.Since(start))
	}
}

func TestLoginDeviceFlagNeedsAServerThatServesIt(t *testing.T) {
	srv, _ := deviceCLIServer(t, false)
	_, _, err := run(t, "auth", "login", "--device", "--server", srv.URL, "--config", filepath.Join(t.TempDir(), "c.yaml"))
	if err == nil || !strings.Contains(err.Error(), "does not serve the device sign-in") {
		t.Fatalf("err = %v, want a clear refusal", err)
	}
}

func TestLoginDeviceFlagConflicts(t *testing.T) {
	for _, extra := range [][]string{{"--no-browser"}, {"--sso"}, {"--password", "pw"}, {"--username", "u@x"}} {
		args := append([]string{"auth", "login", "--device", "--server", "http://127.0.0.1:1", "--config", filepath.Join(t.TempDir(), "c.yaml")}, extra...)
		if _, _, err := run(t, args...); err == nil || !strings.Contains(err.Error(), "--device") {
			t.Errorf("--device with %v: err = %v, want a refusal naming --device", extra, err)
		}
	}
}

func TestSessionHeadlessDetection(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		env  map[string]string
		goos string
		want bool
	}{
		"ssh":                    {map[string]string{"SSH_CONNECTION": "10.0.0.1 22 10.0.0.2 51000"}, "darwin", true},
		"ssh tty":                {map[string]string{"SSH_TTY": "/dev/pts/1", "DISPLAY": "localhost:10.0"}, "linux", true},
		"linux with x11":         {map[string]string{"DISPLAY": ":0"}, "linux", false},
		"linux with wayland":     {map[string]string{"WAYLAND_DISPLAY": "wayland-0"}, "linux", false},
		"linux without display":  {map[string]string{}, "linux", true},
		"mac desktop":            {map[string]string{}, "darwin", false},
		"ssh with BROWSER":       {map[string]string{"SSH_CONNECTION": "x", "BROWSER": "wslview"}, "linux", false},
		"container with BROWSER": {map[string]string{"BROWSER": "/usr/local/bin/open-on-host"}, "linux", false},
		"windows desktop":        {map[string]string{}, "windows", false},
	} {
		got, reason := headlessSession(func(k string) string { return tc.env[k] }, tc.goos)
		if got != tc.want || (got && reason == "") {
			t.Errorf("%s: headless = %v (%q), want %v with a reason", name, got, reason, tc.want)
		}
	}
}
