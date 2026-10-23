package cli

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// deployTo runs a whole deploy of a fresh fixture against fake, with a
// registry that holds the image the stub builder "pushed".
func deployTo(t *testing.T, fake *deployServer, args ...string) (dir, stdout, stderr, log string, err error) {
	t.Helper()
	reg := newFakeOCIRegistry(t, singleManifest)
	dir, stub, log := deployFixtureAt(t, reg.host+"/team")
	stdout, stderr, err = deployDir(t, fake, dir, stub, args...)
	return dir, stdout, stderr, log, err
}

// deployDir runs a whole deploy of dir with builder against fake.
func deployDir(t *testing.T, fake *deployServer, dir, builder string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	return deployDirDeps(t, nil, fake, dir, builder, args...)
}

// deployDirDeps is deployDir under d (testDeps when nil).
func deployDirDeps(t *testing.T, d *cliDeps, fake *deployServer, dir, builder string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	all := append([]string{"deploy", dir, "--server", srv.URL, "--token", "x", "--yes", "--builder", builder}, args...)
	return runDeps(t, d, all...)
}

// TestDeployPausedAndActiveSetIsPaused: --paused and --active reach the
// register as is_paused, nothing is sent without them, and the summary says
// what happens next with the state the server reports.
func TestDeployPausedAndActiveSetIsPaused(t *testing.T) {
	for _, tc := range []struct {
		name      string
		args      []string
		start     bool // the DAG's state before the deploy
		wantQuery string
		wantLine  string
	}{
		{"no flag keeps the server's choice", nil, true, "", "next: etl is paused"},
		{"--active", []string{"--active"}, true, "is_paused=false", "next: etl is active"},
		{"--paused", []string{"--paused"}, false, "is_paused=true", "next: etl is paused"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := &deployServer{paused: tc.start}
			_, out, _, _, err := deployTo(t, fake, tc.args...)
			if err != nil {
				t.Fatalf("deploy: %v\n%s", err, out)
			}
			if len(fake.regQueries) != 1 || fake.regQueries[0] != tc.wantQuery {
				t.Errorf("register queries = %q, want [%q]", fake.regQueries, tc.wantQuery)
			}
			if !strings.Contains(out, tc.wantLine) {
				t.Errorf("output %q does not say %q", out, tc.wantLine)
			}
		})
	}
}

func TestDeployPausedAndActiveAreExclusive(t *testing.T) {
	_, _, err := run(t, "deploy", t.TempDir(), "--paused", "--active")
	if err == nil || !strings.Contains(err.Error(), "paused") || !strings.Contains(err.Error(), "active") {
		t.Fatalf("err = %v, want --paused and --active refused together", err)
	}
}

// TestDeployOfTheCurrentContentStillAppliesTheOverride: nothing is built or
// registered for content that is already current, but --active still
// unpauses the DAG, through the Airflow PATCH.
func TestDeployOfTheCurrentContentStillAppliesTheOverride(t *testing.T) {
	dir, stub, log := deployFixture(t)
	label := labelOf(t, dir)
	fake := &deployServer{current: label, registered: []string{label}, paused: true}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	out, _, err := run(t, "deploy", dir, "--server", srv.URL, "--token", "x", "--yes", "--builder", stub, "--active")
	if err != nil {
		t.Fatalf("deploy: %v", err)
	}
	if builderRan(log) || fake.registers != 0 {
		t.Errorf("built=%v registers=%d, want neither", builderRan(log), fake.registers)
	}
	if len(fake.patches) != 1 || !strings.Contains(fake.patches[0], `"is_paused":false`) {
		t.Errorf("patches = %q, want one is_paused false", fake.patches)
	}
	if !strings.Contains(out, "next: etl is active") {
		t.Errorf("output %q does not say the DAG is active", out)
	}
}

// isolateServerConfig leaves deploy no server from the environment, the user
// config or a workspace: only flags and what the test writes to home count.
func isolateServerConfig(t *testing.T) (home string) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("HOME", home)
	for _, k := range []string{"DEXAFLOW_SERVER_URL", "LEOFLOW_SERVER_URL", "DEXAFLOW_TARGET", "LEOFLOW_TARGET", "DEXAFLOW_TOKEN", "LEOFLOW_TOKEN"} {
		t.Setenv(k, "")
		if err := os.Unsetenv(k); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(t.TempDir())
	return home
}

// TestDeployRefusesAnImplicitLocalhost: with no server set anywhere, deploy
// used to go to http://localhost:8080 without asking. It now stops before
// anything is built and says how to name a server.
func TestDeployRefusesAnImplicitLocalhost(t *testing.T) {
	dir, stub, log := deployFixture(t)
	isolateServerConfig(t)
	_, _, err := run(t, "deploy", dir, "--yes", "--builder", stub)
	if err == nil {
		t.Fatal("deploy with no server configured must fail")
	}
	for _, want := range []string{"no control plane", "--server", "DEXAFLOW_SERVER_URL", "dexaflow target add",
		"For the local Lite server, pass --server http://localhost:8088"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not say %q", err, want)
		}
	}
	if builderRan(log) {
		t.Error("built before refusing the implicit server")
	}
}

// TestDeployToLiteKeepsTheLocalDefault: on a machine set up for Lite the
// local server is the expected one, so the default is not refused.
func TestDeployToLiteKeepsTheLocalDefault(t *testing.T) {
	dir, stub, _ := deployFixture(t)
	home := isolateServerConfig(t)
	writeProjectFile(t, filepath.Join(home, ".dexaflow"), "config.yaml", "lite_executor: \"subprocess\"\nlite_port: 1\n")
	out, _, err := run(t, "deploy", dir, "--yes", "--builder", stub, "--plan")
	if err != nil && strings.Contains(err.Error(), "no control plane") {
		t.Fatalf("a Lite install refused its local server: %v", err)
	}
	if !strings.Contains(out, "server:     http://localhost:1 (lite_port in ") {
		t.Errorf("output %q does not name the Lite server on lite_port (#1912)", out)
	}
}

// TestDeployPlanChangesNothing: --plan says per DAG what a deploy would build,
// push and register and why, from the content label and the server's current
// version, and runs no parser, no builder and no register.
func TestDeployPlanChangesNothing(t *testing.T) {
	for _, tc := range []struct {
		name  string
		fake  func(label string) *deployServer
		args  []string
		want  []string
		reads bool // the server was asked (the details, for is_paused, at least)
	}{
		{"current content", func(l string) *deployServer { return &deployServer{current: l, registered: []string{l}} }, nil,
			[]string{"plan for etl", "up to date", "nothing to build, push or register"}, true},
		{"changed content", func(string) *deployServer { return &deployServer{current: "LB", registered: []string{"LB"}} }, nil,
			[]string{"plan for etl", "build", "push", "register", "content changed", "the current version is LB"}, true},
		{"new DAG", func(string) *deployServer { return &deployServer{status: 404} }, nil,
			[]string{"plan for etl", "not registered on the server"}, true},
		{"older content", func(l string) *deployServer { return &deployServer{current: "LB", registered: []string{"LB", l}} }, nil,
			[]string{"refused", "dexaflow rollback"}, true},
		{"explicit label", func(string) *deployServer { return &deployServer{} }, []string{"--dag-version", "mine"},
			[]string{"version     mine (--dag-version)", "not checked"}, true},
		{"skip build", func(string) *deployServer { return &deployServer{status: 404} }, []string{"--skip-build"},
			[]string{"build       no (--skip-build)"}, true},
		{"paused override", func(string) *deployServer { return &deployServer{status: 404} }, []string{"--paused"},
			[]string{"is_paused   set to true (--paused)"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, stub, log := deployFixture(t)
			fake := tc.fake(labelOf(t, dir))
			srv := httptest.NewServer(fake)
			defer srv.Close()
			args := append([]string{"deploy", dir, "--server", srv.URL, "--token", "x", "--builder", stub, "--plan"}, tc.args...)
			out, _, err := run(t, args...)
			if err != nil {
				t.Fatalf("plan: %v\n%s", err, out)
			}
			for _, w := range tc.want {
				if !strings.Contains(out, w) {
					t.Errorf("plan %q does not say %q", out, w)
				}
			}
			if builderRan(log) || fake.registers != 0 || fake.triggers != 0 || len(fake.patches) != 0 {
				t.Errorf("built=%v registers=%d triggers=%d patches=%d, want nothing", builderRan(log), fake.registers, fake.triggers, len(fake.patches))
			}
			if _, perr := os.Stat(filepath.Join(filepath.Dir(log), "parser.log")); perr == nil {
				t.Error("--plan ran the parser")
			}
			if (fake.reads > 0) != tc.reads {
				t.Errorf("reads = %d, want reads=%v", fake.reads, tc.reads)
			}
		})
	}
}

func TestDeployPlanRefusesTrigger(t *testing.T) {
	_, _, err := run(t, "deploy", t.TempDir(), "--plan", "--trigger")
	if err == nil || !strings.Contains(err.Error(), "--plan") {
		t.Fatalf("err = %v, want --plan with --trigger refused", err)
	}
}

// deployJSON is the --output json document.
type deployJSON struct {
	Server string `json:"server"`
	Target string `json:"target"`
	DAGs   []struct {
		DagID    string `json:"dag_id"`
		Status   string `json:"status"`
		Version  string `json:"version"`
		Image    string `json:"image"`
		Digest   string `json:"digest"`
		SpecHash string `json:"spec_hash"`
		Created  *bool  `json:"created"`
		IsPaused *bool  `json:"is_paused"`
		Next     string `json:"next"`
		Plan     *struct {
			Build    bool   `json:"build"`
			Push     bool   `json:"push"`
			Register bool   `json:"register"`
			Reason   string `json:"reason"`
		} `json:"plan"`
	} `json:"dags"`
}

// TestDeployOutputJSON: --output json prints one JSON document on stdout for
// a pipeline to read, and the human lines go to stderr.
func TestDeployOutputJSON(t *testing.T) {
	fake := &deployServer{paused: true}
	reg := newFakeOCIRegistry(t, singleManifest)
	dir, stub, _ := deployFixtureAt(t, reg.host+"/team")
	out, errOut, err := deployDir(t, fake, dir, stub, "--output", "json")
	if err != nil {
		t.Fatalf("deploy: %v\n%s", err, errOut)
	}
	var doc deployJSON
	if jerr := json.Unmarshal([]byte(out), &doc); jerr != nil {
		t.Fatalf("stdout is not one JSON document: %v\n%s", jerr, out)
	}
	if !strings.HasPrefix(doc.Server, "http://127.0.0.1:") || len(doc.DAGs) != 1 {
		t.Fatalf("doc = %+v", doc)
	}
	d := doc.DAGs[0]
	got := map[string]any{"dag_id": d.DagID, "status": d.Status, "version": d.Version, "image": d.Image,
		"digest": d.Digest, "spec_hash": d.SpecHash, "created": d.Created != nil && *d.Created,
		"is_paused": d.IsPaused != nil && *d.IsPaused, "next says paused": strings.Contains(d.Next, "paused")}
	want := map[string]any{"dag_id": "etl", "status": "registered", "version": labelOf(t, dir), "image": reg.host + "/team/etl@" + reg.digest,
		"digest": reg.digest, "spec_hash": "abc123", "created": true,
		"is_paused": true, "next says paused": true}
	for k, w := range want {
		if got[k] != w {
			t.Errorf("%s = %v, want %v", k, got[k], w)
		}
	}
	if !strings.Contains(errOut, "server:") || !strings.Contains(errOut, "Deployed etl") {
		t.Errorf("stderr %q, want the human summary there", errOut)
	}
}

func TestDeployPlanOutputJSON(t *testing.T) {
	dir, stub, _ := deployFixture(t)
	srv := httptest.NewServer(&deployServer{current: "LB", registered: []string{"LB"}})
	defer srv.Close()
	out, _, err := run(t, "deploy", dir, "--server", srv.URL, "--token", "x", "--builder", stub, "--plan", "--output", "json")
	if err != nil {
		t.Fatal(err)
	}
	var doc deployJSON
	if jerr := json.Unmarshal([]byte(out), &doc); jerr != nil {
		t.Fatalf("stdout is not one JSON document: %v\n%s", jerr, out)
	}
	if len(doc.DAGs) != 1 || doc.DAGs[0].Status != "planned" || doc.DAGs[0].Plan == nil ||
		!doc.DAGs[0].Plan.Build || !doc.DAGs[0].Plan.Register || !strings.Contains(doc.DAGs[0].Plan.Reason, "content changed") {
		t.Errorf("doc = %s", out)
	}
}

func TestDeployRejectsAnUnknownOutput(t *testing.T) {
	_, _, err := run(t, "deploy", t.TempDir(), "--output", "yaml")
	if err == nil || !strings.Contains(err.Error(), "--output") {
		t.Fatalf("err = %v, want --output yaml refused", err)
	}
}

// TestDeploySummaryOfANewScheduledDAG: the next-step line knows the deploy
// created the DAG, so it says what the scheduler starts with rather than only
// the next tick.
func TestDeploySummaryOfANewScheduledDAG(t *testing.T) {
	fake := &deployServer{schedule: "@daily", dagCreated: true}
	_, out, _, _, err := deployTo(t, fake)
	if err != nil {
		t.Fatalf("deploy: %v\n%s", err, out)
	}
	if !strings.Contains(out, "next: etl is active; the scheduler creates one run for the latest tick") {
		t.Errorf("output %q does not say what the scheduler starts with", out)
	}
}

// TestDeploySummaryOfANewVersionOfAnActiveDAG: a 201 for a new version of an
// existing active DAG does not mean the DAG is new, so the line gives the
// next run and claims no catchup since start_date.
func TestDeploySummaryOfANewVersionOfAnActiveDAG(t *testing.T) {
	fake := &deployServer{schedule: "@daily", catchup: true, startDate: "2026-01-01T00:00:00Z"}
	_, out, _, _, err := deployTo(t, fake)
	if err != nil {
		t.Fatalf("deploy: %v\n%s", err, out)
	}
	if !strings.Contains(out, "next: etl is active; next run") || strings.Contains(out, "catchup") {
		t.Errorf("output %q, want the next run and no catchup backlog", out)
	}
}

// TestDeploySummaryWhenActiveUnpausesADAG: --active on a DAG that was paused
// releases the scheduler's backlog, which the line describes as an upper
// bound.
func TestDeploySummaryWhenActiveUnpausesADAG(t *testing.T) {
	fake := &deployServer{paused: true, schedule: "@daily", catchup: true, startDate: "2026-01-01T00:00:00Z"}
	_, out, _, _, err := deployTo(t, fake, "--active")
	if err != nil {
		t.Fatalf("deploy: %v\n%s", err, out)
	}
	for _, want := range []string{"next: etl is active; the scheduler creates up to", "fewer if it already ran"} {
		if !strings.Contains(out, want) {
			t.Errorf("output %q does not say %q", out, want)
		}
	}
}

// TestDeployWarnsWhenTheServerIgnoresIsPaused: an older server registers
// without applying is_paused; the deploy says so instead of implying it.
func TestDeployWarnsWhenTheServerIgnoresIsPaused(t *testing.T) {
	fake := &deployServer{paused: true, ignorePaused: true}
	_, out, errOut, _, err := deployTo(t, fake, "--active")
	if err != nil {
		t.Fatalf("deploy: %v\n%s", err, out)
	}
	if !strings.Contains(errOut, "warning:") || !strings.Contains(errOut, "--active") {
		t.Errorf("stderr %q, want a warning that --active was not applied", errOut)
	}
	if !strings.Contains(out, "next: etl is paused") {
		t.Errorf("output %q, want the state the server reports", out)
	}
}

// TestDeployOutputJSONReportsAFailure: a failed deploy still prints the JSON
// document, with the DAG marked failed and the error.
func TestDeployOutputJSONReportsAFailure(t *testing.T) {
	dir, stub, _ := deployFixture(t)
	srv := httptest.NewServer(&deployServer{status: 404})
	defer srv.Close()
	// The plain stub builder cannot report a digest, so the deploy fails after the build.
	out, _, err := run(t, "deploy", dir, "--server", srv.URL, "--token", "x", "--yes", "--builder", stub, "--output", "json")
	if err == nil {
		t.Fatal("want the deploy to fail")
	}
	var doc struct {
		Server string `json:"server"`
		DAGs   []struct {
			DagID  string `json:"dag_id"`
			Status string `json:"status"`
			Error  string `json:"error"`
		} `json:"dags"`
	}
	if jerr := json.Unmarshal([]byte(out), &doc); jerr != nil {
		t.Fatalf("stdout is not one JSON document: %v\n%s", jerr, out)
	}
	if doc.Server == "" || len(doc.DAGs) != 1 || doc.DAGs[0].DagID != "etl" || doc.DAGs[0].Status != "failed" || doc.DAGs[0].Error == "" {
		t.Errorf("doc = %s", out)
	}
}

// TestDeployOutputJSONReportsAFailedTriggerOnce: the version registered, then
// --trigger failed. The DAG used to appear twice, once registered and once
// failed; it is one entry, failed, that still names the registered version.
func TestDeployOutputJSONReportsAFailedTriggerOnce(t *testing.T) {
	fake := &deployServer{failTrigger: true}
	_, out, _, _, err := deployTo(t, fake, "--trigger", "--output", "json")
	if err == nil {
		t.Fatal("want the deploy to fail on the trigger")
	}
	var doc struct {
		DAGs []struct {
			DagID   string `json:"dag_id"`
			Status  string `json:"status"`
			Version string `json:"version"`
			Error   string `json:"error"`
		} `json:"dags"`
	}
	if jerr := json.Unmarshal([]byte(out), &doc); jerr != nil {
		t.Fatalf("stdout is not one JSON document: %v\n%s", jerr, out)
	}
	if fake.registers != 1 || fake.triggers != 1 {
		t.Fatalf("registers=%d triggers=%d, want one of each", fake.registers, fake.triggers)
	}
	if len(doc.DAGs) != 1 || doc.DAGs[0].Status != "failed" || doc.DAGs[0].Error == "" || doc.DAGs[0].Version == "" {
		t.Errorf("doc = %s", out)
	}
}
