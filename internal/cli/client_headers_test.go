package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/version"
)

// ciVariables are the variables whose presence says the CLI runs in a
// pipeline (#1772).
var ciVariables = []string{"CI", "GITHUB_ACTIONS", "GITLAB_CI"}

// clearCI unsets every CI variable for the test, as on a laptop.
func clearCI(t *testing.T) {
	t.Helper()
	for _, k := range ciVariables {
		t.Setenv(k, "")
	}
}

// TestDeploySendsTheCLIUserAgent: every request of a deploy names the CLI
// and its version, and says it comes from a pipeline when a usual CI
// variable is set, so a server can tell an interactive deploy from one.
func TestDeploySendsTheCLIUserAgent(t *testing.T) {
	wantAgent := "dexaflow-cli/" + version.Get().Version
	deploy := func(t *testing.T) *recordingServer {
		t.Helper()
		fake := newRecordingServer("")
		srv := httptest.NewServer(fake)
		t.Cleanup(srv.Close)
		_, _, builder, _ := sourceModeWorkspace(t, srv.URL, runtimeRef, "")
		if out, stderr, err := run(t, "deploy", "etl", "--token", "tok", "--yes", "--builder", builder, "--config", emptyConfig(t)); err != nil {
			t.Fatalf("deploy: %v\n%s\n%s", err, out, stderr)
		}
		if fake.requests < 4 {
			t.Fatalf("the deploy made %d requests, want the defaults read, the probe, the dry run and the register at least", fake.requests)
		}
		for i, a := range fake.agents {
			if a != wantAgent {
				t.Errorf("request %d sent User-Agent %q, want %q", i, a, wantAgent)
			}
		}
		return fake
	}
	t.Run("interactive", func(t *testing.T) {
		clearCI(t)
		for i, h := range deploy(t).ciHeaders {
			if h != "" {
				t.Errorf("request %d sent X-Dexaflow-CI %q outside CI", i, h)
			}
		}
	})
	for _, k := range ciVariables {
		t.Run(k, func(t *testing.T) {
			clearCI(t)
			t.Setenv(k, "true")
			for i, h := range deploy(t).ciHeaders {
				if h != "true" {
					t.Errorf("request %d sent X-Dexaflow-CI %q with %s set, want true", i, h, k)
				}
			}
		})
	}
}

// TestInCI: a CI variable counts when set to anything but empty, false or 0.
func TestInCI(t *testing.T) {
	cases := map[string]bool{"": false, "false": false, "0": false, "FALSE": false, "true": true, "1": true, "yes": true}
	for v, want := range cases {
		clearCI(t)
		t.Setenv("GITLAB_CI", v)
		if got := inCI(); got != want {
			t.Errorf("GITLAB_CI=%q: inCI() = %v, want %v", v, got, want)
		}
	}
}

// TestDeployPrintsServerLinksAndChecks: a server, or a platform in front of
// it, may add a links object and a checks list to the register and the dry
// run answers; the deploy prints both, without control characters, and
// reports the register's in --output json.
func TestDeployPrintsServerLinksAndChecks(t *testing.T) {
	fake := newRecordingServer("")
	fake.dryRunBody = `{"dag_id":"etl","created":true,"dag_created":true,"is_paused":true,"dry_run":true,` +
		`"links":{"review":"https://review.example.com/1"},"checks":[{"name":"quota","status":"ok"}]}`
	fake.registerBody = `{"dag_id":"etl","version":"v","spec_hash":"abc","created":true,"dag_created":true,"is_paused":true,` +
		`"links":{"console":"https://console.example.com/dags/etl","logs\u001b[31m":"https://logs.example.com/etl"},` +
		`"checks":[{"name":"lint","status":"passed","detail":"0 findings"},"policy ok"]}`
	srv := httptest.NewServer(fake)
	defer srv.Close()
	_, _, builder, _ := sourceModeWorkspace(t, srv.URL, runtimeRef, "")

	out, stderr, err := run(t, "deploy", "etl", "--token", "tok", "--yes", "--builder", builder, "--config", emptyConfig(t))
	if err != nil {
		t.Fatalf("deploy: %v\n%s\n%s", err, out, stderr)
	}
	for _, want := range []string{
		"  link     review: https://review.example.com/1",
		"  check    quota: ok",
		"  link     console: https://console.example.com/dags/etl",
		"  link     logs[31m: https://logs.example.com/etl",
		"  check    lint: passed (0 findings)",
		"  check    policy ok",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output does not say %q:\n%s", want, out)
		}
	}
	if strings.ContainsRune(out, '\x1b') {
		t.Errorf("the output carries an escape character from the server:\n%q", out)
	}
	if strings.Index(out, "link     console") > strings.Index(out, "link     logs") {
		t.Errorf("the links are not sorted by name:\n%s", out)
	}
	if strings.Index(out, "review.example.com") > strings.Index(out, "Deployed etl") {
		t.Errorf("the dry run's links are not printed with the preflight, before the register:\n%s", out)
	}

	jsonOut, _, err := run(t, "deploy", "etl", "--force", "--output", "json", "--token", "tok", "--yes", "--builder", builder, "--config", emptyConfig(t))
	if err != nil {
		t.Fatalf("deploy --output json: %v", err)
	}
	var report struct {
		DAGs []struct {
			Links  map[string]string `json:"links"`
			Checks []struct {
				Name   string `json:"name"`
				Status string `json:"status"`
				Detail string `json:"detail"`
			} `json:"checks"`
		} `json:"dags"`
	}
	if jerr := json.Unmarshal([]byte(jsonOut), &report); jerr != nil || len(report.DAGs) != 1 {
		t.Fatalf("report %s: %v", jsonOut, jerr)
	}
	d := report.DAGs[0]
	if d.Links["console"] != "https://console.example.com/dags/etl" || len(d.Checks) != 2 ||
		d.Checks[0].Name != "lint" || d.Checks[0].Status != "passed" || d.Checks[0].Detail != "0 findings" || d.Checks[1].Name != "policy ok" {
		t.Errorf("report entry = %+v, want the register's links and checks", d)
	}

	// A register the server answers 200 (this exact content, nothing new)
	// prints them too.
	fake.registerStatus = http.StatusOK
	unchanged, _, err := run(t, "deploy", "etl", "--force", "--token", "tok", "--yes", "--builder", builder, "--config", emptyConfig(t))
	if err != nil {
		t.Fatalf("deploy of unchanged content: %v", err)
	}
	if !strings.Contains(unchanged, "already registered") || !strings.Contains(unchanged, "  link     console: https://console.example.com/dags/etl") {
		t.Errorf("an unchanged register does not print the links:\n%s", unchanged)
	}
}

// TestPrintableDropsFormatCharacters: a server's text loses the characters
// that reorder or hide what the terminal shows (bidi overrides and isolates,
// zero-width characters, line and paragraph separators) and the C1 controls
// such as CSI (U+009B).
func TestPrintableDropsFormatCharacters(t *testing.T) {
	t.Parallel()
	in := "a\u202eb\u2066c\u200bd\u200de\u2028f\u2029g\u009bh\ufeffi\x1bj"
	if got := printable(in); got != "abcdefghij" {
		t.Errorf("printable(%q) = %q, want %q", in, got, "abcdefghij")
	}
	if got := printable("café, 日本"); got != "café, 日本" {
		t.Errorf("printable dropped ordinary text: %q", got)
	}
}

// TestServerLinksAreHTTPOnly: a link is printed and reported only when it is
// an http or https URL.
func TestServerLinksAreHTTPOnly(t *testing.T) {
	t.Parallel()
	e := readServerExtras([]byte(`{"links":{"js":"javascript:alert(1)","file":"file:///etc/passwd","up":"HTTPS://x.example/1",` +
		`"plain":"http://y.example/2","bare":"not a url","data":"data:text/html,x","rel":"/dags/etl","opaque":"https:x.example"}}`))
	if len(e.Links) != 2 || e.Links["up"] != "HTTPS://x.example/1" || e.Links["plain"] != "http://y.example/2" {
		t.Errorf("links = %v, want only the http and https ones", e.Links)
	}
}

// TestStatusSendsTheCLIUserAgent: the headers that name the CLI go on every
// request, not only a deploy's: status reads with the public client.
func TestStatusSendsTheCLIUserAgent(t *testing.T) {
	clearCI(t)
	t.Setenv("CI", "true")
	fake := newTrackingServer("")
	srv := httptest.NewServer(fake)
	defer srv.Close()
	sourceModeWorkspace(t, srv.URL, runtimeRef, "")
	_, _, _ = run(t, "status", "--token", "tok", "--config", emptyConfig(t))
	if fake.requests == 0 {
		t.Fatal("status made no request")
	}
	want := "dexaflow-cli/" + version.Get().Version
	for i, a := range fake.agents {
		if a != want || fake.ciHeaders[i] != "true" {
			t.Errorf("request %d sent User-Agent %q and X-Dexaflow-CI %q, want %q and true", i, a, fake.ciHeaders[i], want)
		}
	}
}

// TestEveryClientSetsTheCLIHeaders: the CLI builds its HTTP clients in one
// place (cliHTTPClient, cliAPIClient), whose transport sets the headers that
// name it, so no client can be built without them.
func TestEveryClientSetsTheCLIHeaders(t *testing.T) {
	t.Parallel()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	forbidden := []string{"http.Client{", "http.DefaultClient", "apiclient.New(", "apiclient.NewClient("}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") || f == "client_headers.go" {
			continue
		}
		data, rerr := os.ReadFile(f)
		if rerr != nil {
			t.Fatal(rerr)
		}
		for i, line := range strings.Split(string(data), "\n") {
			for _, bad := range forbidden {
				if strings.Contains(line, bad) {
					t.Errorf("%s:%d builds a client with %s; use cliHTTPClient or cliAPIClient", f, i+1, bad)
				}
			}
		}
	}
}

// TestCLITransportSetsTheHeaders: the transport sets the headers on a copy
// of each request, leaving the caller's untouched.
func TestCLITransportSetsTheHeaders(t *testing.T) {
	clearCI(t)
	var got http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { got = r.Header.Clone() }))
	defer srv.Close()
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL, http.NoBody)
	resp, err := cliHTTPClient(5 * time.Second).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if got.Get("User-Agent") != cliUserAgent() || got.Get(ciHeader) != "" {
		t.Errorf("headers = %v, want the CLI's User-Agent and no CI header outside CI", got)
	}
	if req.Header.Get("User-Agent") != "" {
		t.Errorf("the transport changed the caller's request: %v", req.Header)
	}
}
