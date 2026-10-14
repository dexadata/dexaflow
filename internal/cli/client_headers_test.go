package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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
