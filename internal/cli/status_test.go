package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
)

// statusVersionFake is one registered version of a DAG on statusServer.
type statusVersionFake struct {
	label   string
	number  int
	created string
	current bool
}

// statusDagFake is one DAG on statusServer: its versions, is_paused, and
// its runs newest first.
type statusDagFake struct {
	versions []statusVersionFake
	paused   bool
	runs     []string // each a JSON object of the dagRuns list
}

// statusServer fakes the Airflow-compatible reads `dexaflow status` makes,
// for several DAGs, and records every request: the Authorization header
// of each, and any request that is not a GET (status must never write).
type statusServer struct {
	mu       sync.Mutex
	dags     map[string]*statusDagFake
	defaults string // the body of GET /api/v2/deployDefaults; "" answers 404
	status   int    // when set, the answer to every DAG read
	auth     []string
	writes   []string
	paths    []string
}

func newStatusServer(t *testing.T, s *statusServer) string {
	t.Helper()
	if s.dags == nil {
		s.dags = map[string]*statusDagFake{}
	}
	srv := httptest.NewServer(s)
	t.Cleanup(srv.Close)
	return srv.URL
}

func (s *statusServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	s.auth = append(s.auth, r.Header.Get("Authorization"))
	s.paths = append(s.paths, r.URL.Path+"?"+r.URL.RawQuery)
	if r.Method != http.MethodGet {
		s.writes = append(s.writes, r.Method+" "+r.URL.Path)
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if r.URL.Path == "/api/v2/deployDefaults" {
		if s.defaults == "" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = io.WriteString(w, s.defaults)
		return
	}
	rest, ok := strings.CutPrefix(r.URL.Path, "/api/v2/dags/")
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	if s.status != 0 {
		w.WriteHeader(s.status)
		_, _ = io.WriteString(w, `{"title":"refused","detail":"refused by the fake"}`)
		return
	}
	id, sub, _ := strings.Cut(rest, "/")
	d := s.dags[id]
	if d == nil {
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"title":"not found","detail":"DAG not found"}`)
		return
	}
	s.serveDag(w, id, sub, d)
}

// serveDag answers the DAG, its versions and its runs.
func (s *statusServer) serveDag(w http.ResponseWriter, id, sub string, d *statusDagFake) {
	switch sub {
	case "":
		_, _ = fmt.Fprintf(w, `{"dag_id":%q,"is_paused":%v}`, id, d.paused)
	case "dagVersions":
		parts := make([]string, 0, len(d.versions))
		for _, v := range d.versions {
			parts = append(parts, fmt.Sprintf(`{"dag_id":%q,"bundle_version":%q,"version_number":%d,"created_at":%q,"is_current":%v}`,
				id, v.label, v.number, v.created, v.current))
		}
		_, _ = fmt.Fprintf(w, `{"dag_versions":[%s],"total_entries":%d}`, strings.Join(parts, ","), len(parts))
	case "dagRuns":
		runs := d.runs
		if len(runs) > 1 {
			runs = runs[:1]
		}
		_, _ = fmt.Fprintf(w, `{"dag_runs":[%s],"total_entries":%d}`, strings.Join(runs, ","), len(d.runs))
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

// statusWorkspace writes a workspace holding one project per dag_id under
// dags/, each with its own registry so its content label needs no target
// defaults, and the targets given as name=url (the first is the default).
// It returns the root and each project's directory.
func statusWorkspace(t *testing.T, targets []string, dagIDs ...string) (root string, dirs map[string]string) {
	t.Helper()
	root = t.TempDir()
	var b strings.Builder
	b.WriteString("targets:\n")
	for _, tg := range targets {
		name, url, _ := strings.Cut(tg, "=")
		fmt.Fprintf(&b, "  %s:\n    server: %s\n", name, url)
	}
	if len(targets) > 0 {
		name, _, _ := strings.Cut(targets[0], "=")
		fmt.Fprintf(&b, "default_target: %s\n", name)
	} else {
		b.Reset()
		b.WriteString("targets: {}\n")
	}
	writeTree(t, filepath.Join(root, workspaceFileName), b.String())
	dirs = map[string]string{}
	for _, id := range dagIDs {
		dir := filepath.Join(root, "dags", id)
		writeProjectFile(t, dir, "dag.py", "# "+id+"\n")
		writeProjectFile(t, dir, "dexaflow.yaml", "dag_id: "+id+"\nregistry:\n  url: ghcr.io/acme\n  image_name: "+id+"\n")
		dirs[id] = dir
	}
	return root, dirs
}

// sessionsConfig writes a config file that saves token tokens[i] for
// server servers[i].
func sessionsConfig(t *testing.T, servers, tokens []string) string {
	t.Helper()
	var b strings.Builder
	b.WriteString("sessions:\n")
	for i, s := range servers {
		fmt.Fprintf(&b, "  - server: %s\n    token: %s\n", s, tokens[i])
	}
	p := filepath.Join(t.TempDir(), "config.yaml")
	writeTree(t, p, b.String())
	return p
}

// statusJSON is the documented shape of `dexaflow status --output json`.
type statusJSON struct {
	Workspace string `json:"workspace"`
	Targets   []struct {
		Target string `json:"target"`
		Server string `json:"server"`
		Source string `json:"source"`
		Error  string `json:"error"`
		Dags   []struct {
			DagID          string `json:"dag_id"`
			Path           string `json:"path"`
			LocalVersion   string `json:"local_version"`
			State          string `json:"state"`
			CurrentVersion *struct {
				Label         string `json:"label"`
				VersionNumber int    `json:"version_number"`
				CreatedAt     string `json:"created_at"`
			} `json:"current_version"`
			MatchedVersion *struct {
				Label         string `json:"label"`
				VersionNumber int    `json:"version_number"`
				CreatedAt     string `json:"created_at"`
			} `json:"matched_version"`
			IsPaused *bool `json:"is_paused"`
			LastRun  *struct {
				RunID     string `json:"run_id"`
				State     string `json:"state"`
				StartDate string `json:"start_date"`
			} `json:"last_run"`
			Error string `json:"error"`
		} `json:"dags"`
	} `json:"targets"`
	Summary  map[string]int `json:"summary"`
	ExitCode int            `json:"exit_code"`
}

// lineOf returns the first line of out that starts with prefix (spaces
// trimmed), or "".
func lineOf(out, prefix string) string {
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(strings.TrimSpace(l), prefix) {
			return l
		}
	}
	return ""
}

// TestStatusReportsEachState drives one workspace whose DAGs are in every
// state status names: in sync with a last run, behind (an older version),
// drift, not deployed, and paused with no runs.
func TestStatusReportsEachState(t *testing.T) {
	isolateCLIEnv(t)
	fake := &statusServer{}
	url := newStatusServer(t, fake)
	root, dirs := statusWorkspace(t, []string{"dev=" + url}, "synced", "older", "edited", "fresh")
	t.Chdir(root)
	synced, older := labelOf(t, dirs["synced"]), labelOf(t, dirs["older"])
	fake.dags["synced"] = &statusDagFake{
		versions: []statusVersionFake{{synced, 1, "2026-10-01T12:00:00Z", true}},
		runs:     []string{`{"dag_run_id":"manual__1","state":"success","start_date":"2026-10-02T08:30:00Z"}`},
	}
	fake.dags["older"] = &statusDagFake{paused: true, versions: []statusVersionFake{
		{older, 1, "2026-09-01T00:00:00Z", false}, {"v-newer", 2, "2026-09-05T10:00:00Z", true}}}
	fake.dags["edited"] = &statusDagFake{versions: []statusVersionFake{{"v-other", 4, "2026-10-03T09:15:00Z", true}}}
	cfg := sessionsConfig(t, []string{url}, []string{"tok"})

	out, _, err := run(t, "status", "--config", cfg)
	if code := exitCodeOf(err); code != 1 {
		t.Fatalf("exit code = %d (err %v), want 1 for drift\n%s", code, err, out)
	}
	for dag, want := range map[string][]string{
		"synced": {"in sync", synced + " (#1)", "2026-10-01 12:00", "no", "manual__1 success 2026-10-02 08:30"},
		"older":  {"behind: local equals " + older, "v-newer (#2)", "yes", "none"},
		"edited": {"drift: local changes not deployed", "v-other (#4)"},
		"fresh":  {"not deployed"},
	} {
		line := lineOf(out, dag+" ")
		for _, w := range want {
			if !strings.Contains(line, w) {
				t.Errorf("%s: line %q does not say %q\n%s", dag, line, w, out)
			}
		}
	}
	if !strings.Contains(out, "target dev: "+url) {
		t.Errorf("status must name the target and its server:\n%s", out)
	}
	if strings.Contains(lineOf(out, "synced "), "drift") || strings.Contains(lineOf(out, "edited "), "in sync") {
		t.Errorf("states mixed up:\n%s", out)
	}
	if len(fake.writes) != 0 {
		t.Errorf("status wrote to the server: %v", fake.writes)
	}
	for _, d := range dirs {
		if fileExists(filepath.Join(d, "dag.json")) {
			t.Errorf("status compiled %s", d)
		}
	}
	for _, a := range fake.auth {
		if a != "Bearer tok" {
			t.Errorf("a read went with Authorization %q, want the target's saved token", a)
		}
	}
}

// TestStatusInSyncExitsZero is a workspace of two projects, both current.
func TestStatusInSyncExitsZero(t *testing.T) {
	isolateCLIEnv(t)
	fake := &statusServer{}
	url := newStatusServer(t, fake)
	root, dirs := statusWorkspace(t, []string{"dev=" + url}, "alpha", "beta")
	for id, dir := range dirs {
		fake.dags[id] = &statusDagFake{versions: []statusVersionFake{{labelOf(t, dir), 1, "2026-10-01T12:00:00Z", true}}}
	}
	t.Chdir(filepath.Join(root, "dags"))
	out, _, err := run(t, "status", "--config", emptyConfig(t))
	if err != nil {
		t.Fatalf("status: %v\n%s", err, out)
	}
	for _, id := range []string{"alpha", "beta"} {
		if l := lineOf(out, id+" "); !strings.Contains(l, "in sync") {
			t.Errorf("%s: %q, want in sync\n%s", id, l, out)
		}
	}
	if !strings.Contains(out, "2 in sync") {
		t.Errorf("status must close with a summary:\n%s", out)
	}
}

// TestStatusSelectsDAGsByIDAndPath reads only the DAGs named, by dag_id or
// by project folder.
func TestStatusSelectsDAGsByIDAndPath(t *testing.T) {
	isolateCLIEnv(t)
	fake := &statusServer{}
	url := newStatusServer(t, fake)
	root, dirs := statusWorkspace(t, []string{"dev=" + url}, "alpha", "beta", "gamma")
	t.Chdir(root)
	out, _, err := run(t, "status", "alpha", dirs["gamma"], "--config", emptyConfig(t))
	if exitCodeOf(err) != 1 {
		t.Fatalf("err = %v, want exit 1 (nothing deployed)\n%s", err, out)
	}
	if lineOf(out, "alpha ") == "" || lineOf(out, "gamma ") == "" || lineOf(out, "beta ") != "" {
		t.Errorf("status alpha <gamma dir> must report alpha and gamma only:\n%s", out)
	}
	t.Chdir(dirs["beta"])
	out, _, _ = run(t, "status", "--config", emptyConfig(t))
	if lineOf(out, "beta ") == "" || lineOf(out, "alpha ") != "" {
		t.Errorf("status in a project folder must report that project only:\n%s", out)
	}
	if _, _, err := run(t, "status", "nope", "--config", emptyConfig(t)); exitCodeOf(err) != 2 || !strings.Contains(err.Error(), "available") {
		t.Errorf("unknown dag_id: err = %v (exit %d), want exit 2 listing the available ids", err, exitCodeOf(err))
	}
}

// TestStatusAllTargets reports every target of the workspace, each with its
// own server and token; an unreachable one is an error line, and the others
// are still reported.
func TestStatusAllTargets(t *testing.T) {
	isolateCLIEnv(t)
	dev, prod := &statusServer{}, &statusServer{}
	devURL, prodURL := newStatusServer(t, dev), newStatusServer(t, prod)
	down := "http://127.0.0.1:1"
	root, dirs := statusWorkspace(t, []string{"dev=" + devURL, "down=" + down, "prod=" + prodURL}, "alpha")
	label := labelOf(t, dirs["alpha"])
	dev.dags["alpha"] = &statusDagFake{versions: []statusVersionFake{{label, 1, "2026-10-01T12:00:00Z", true}}}
	prod.dags["alpha"] = &statusDagFake{versions: []statusVersionFake{{"v-prod", 3, "2026-10-01T12:00:00Z", true}}}
	cfg := sessionsConfig(t, []string{devURL, prodURL, down}, []string{"dev-tok", "prod-tok", "down-tok"})
	t.Chdir(root)

	out, _, err := run(t, "status", "--all-targets", "--config", cfg)
	if code := exitCodeOf(err); code != 2 {
		t.Fatalf("exit code = %d (err %v), want 2 for the unreachable target\n%s", code, err, out)
	}
	devAt, downAt, prodAt := strings.Index(out, "target dev: "), strings.Index(out, "target down: "), strings.Index(out, "target prod: ")
	if devAt < 0 || downAt < 0 || prodAt < 0 || devAt > downAt || downAt > prodAt {
		t.Fatalf("want dev, down and prod in order:\n%s", out)
	}
	if !strings.Contains(out[devAt:downAt], "in sync") {
		t.Errorf("dev: want in sync:\n%s", out[devAt:downAt])
	}
	if !strings.Contains(out[downAt:prodAt], "error:") {
		t.Errorf("down: want an error line:\n%s", out[downAt:prodAt])
	}
	if !strings.Contains(out[prodAt:], "drift") || !strings.Contains(out[prodAt:], "v-prod (#3)") {
		t.Errorf("prod: want drift against v-prod:\n%s", out[prodAt:])
	}
	for name, fake := range map[string]*statusServer{"dev": dev, "prod": prod} {
		if len(fake.auth) == 0 {
			t.Errorf("%s was never read", name)
		}
		for _, a := range fake.auth {
			if a != "Bearer "+name+"-tok" {
				t.Errorf("%s got Authorization %q, want its own token", name, a)
			}
		}
	}
}

func TestStatusAllTargetsRefusesOneServerOrToken(t *testing.T) {
	isolateCLIEnv(t)
	root, _ := statusWorkspace(t, []string{"dev=https://dev.example"}, "alpha")
	t.Chdir(root)
	for _, extra := range [][]string{{"--server", "https://x.example"}, {"--target", "dev"}, {"--token", "t"}} {
		_, _, err := run(t, append([]string{"status", "--all-targets", "--config", emptyConfig(t)}, extra...)...)
		if exitCodeOf(err) != 2 || !strings.Contains(err.Error(), extra[0]) {
			t.Errorf("--all-targets %v: err = %v, want exit 2 naming %s", extra, err, extra[0])
		}
	}
	t.Setenv("DEXAFLOW_TOKEN", "t")
	if _, _, err := run(t, "status", "--all-targets", "--config", emptyConfig(t)); exitCodeOf(err) != 2 || !strings.Contains(err.Error(), "DEXAFLOW_TOKEN") {
		t.Errorf("--all-targets with DEXAFLOW_TOKEN: err = %v, want exit 2 naming it", err)
	}
}

// TestStatusErrorsExitTwo: a server that refuses the reads is an error,
// never a drift.
func TestStatusErrorsExitTwo(t *testing.T) {
	isolateCLIEnv(t)
	for _, code := range []int{http.StatusUnauthorized, http.StatusInternalServerError} {
		fake := &statusServer{status: code}
		url := newStatusServer(t, fake)
		root, _ := statusWorkspace(t, []string{"dev=" + url}, "alpha")
		t.Chdir(root)
		out, _, err := run(t, "status", "--config", emptyConfig(t))
		if exitCodeOf(err) != 2 {
			t.Errorf("server answering %d: exit %d (err %v), want 2\n%s", code, exitCodeOf(err), err, out)
		}
		if !strings.Contains(out, "error") || strings.Contains(out, "drift") || strings.Contains(out, "not deployed") {
			t.Errorf("server answering %d: want an error, not a state:\n%s", code, out)
		}
	}
}

// TestStatusJSON locks the machine-readable shape.
func TestStatusJSON(t *testing.T) {
	isolateCLIEnv(t)
	fake := &statusServer{}
	url := newStatusServer(t, fake)
	root, dirs := statusWorkspace(t, []string{"dev=" + url}, "alpha", "beta", "gamma")
	alpha, beta := labelOf(t, dirs["alpha"]), labelOf(t, dirs["beta"])
	fake.dags["alpha"] = &statusDagFake{
		versions: []statusVersionFake{{alpha, 2, "2026-10-01T12:00:00Z", true}},
		runs:     []string{`{"dag_run_id":"scheduled__x","state":"failed","start_date":"2026-10-02T08:30:00Z"}`},
	}
	fake.dags["beta"] = &statusDagFake{paused: true, versions: []statusVersionFake{
		{beta, 1, "2026-09-01T00:00:00Z", false}, {"v-new", 2, "2026-09-02T00:00:00Z", true}}}
	t.Chdir(root)
	out, stderr, err := run(t, "status", "--output", "json", "--config", emptyConfig(t))
	if exitCodeOf(err) != 1 {
		t.Fatalf("exit %d (err %v), want 1", exitCodeOf(err), err)
	}
	var got statusJSON
	dec := json.NewDecoder(strings.NewReader(out))
	dec.DisallowUnknownFields()
	if derr := dec.Decode(&got); derr != nil {
		t.Fatalf("stdout is not the documented JSON document: %v\n%s\nstderr: %s", derr, out, stderr)
	}
	if got.ExitCode != 1 || len(got.Targets) != 1 || got.Targets[0].Target != "dev" || got.Targets[0].Server != url {
		t.Fatalf("report = %+v", got)
	}
	if got.Workspace != root {
		t.Errorf("workspace = %q, want %q", got.Workspace, root)
	}
	byID := map[string]int{}
	for i, d := range got.Targets[0].Dags {
		byID[d.DagID] = i
	}
	dags := got.Targets[0].Dags
	a, b, g := dags[byID["alpha"]], dags[byID["beta"]], dags[byID["gamma"]]
	if a.State != "in_sync" || a.LocalVersion != alpha || a.CurrentVersion == nil || a.CurrentVersion.Label != alpha ||
		a.CurrentVersion.VersionNumber != 2 || a.CurrentVersion.CreatedAt != "2026-10-01T12:00:00Z" ||
		a.IsPaused == nil || *a.IsPaused || a.LastRun == nil || a.LastRun.RunID != "scheduled__x" ||
		a.LastRun.State != "failed" || a.LastRun.StartDate != "2026-10-02T08:30:00Z" || a.Path != dirs["alpha"] {
		t.Errorf("alpha = %+v", a)
	}
	if b.State != "behind" || b.MatchedVersion == nil || b.MatchedVersion.Label != beta || b.MatchedVersion.VersionNumber != 1 ||
		b.CurrentVersion == nil || b.CurrentVersion.Label != "v-new" || b.IsPaused == nil || !*b.IsPaused || b.LastRun != nil {
		t.Errorf("beta = %+v", b)
	}
	if g.State != "not_deployed" || g.CurrentVersion != nil || g.IsPaused != nil || g.LastRun != nil {
		t.Errorf("gamma = %+v", g)
	}
	wantSummary := map[string]int{"in_sync": 1, "behind": 1, "drift": 0, "not_deployed": 1, "errors": 0}
	keys := make([]string, 0, len(got.Summary))
	for k := range got.Summary {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if fmt.Sprint(got.Summary) != fmt.Sprint(wantSummary) {
		t.Errorf("summary = %v (keys %v), want %v", got.Summary, keys, wantSummary)
	}
}

// TestStatusAgreesWithDeploy locks that status computes the content label
// through the same path as deploy, the cluster's deploy defaults included:
// right after a deploy the DAG is in sync, and after an edit it drifts.
func TestStatusAgreesWithDeploy(t *testing.T) {
	isolateCLIEnv(t)
	reg := newFakeOCIRegistry(t, singleManifest)
	// No registry in the project: the cluster's deploy defaults name it, so
	// a label computed without them differs from the deployed one.
	dir := versionProject(t, "build:\n  platforms: [linux/amd64]\n")
	t.Setenv("LEOFLOW_PARSER_CMD", recordingParser(t, filepath.Join(t.TempDir(), "parser.log")))
	builder, _ := argvBuilder(t)
	deploy := &deployServer{}
	ws := &watchServer{fake: deploy}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/api/v2/deployDefaults" {
			_, _ = fmt.Fprintf(w, `{"registry":%q}`, reg.host+"/team")
			return
		}
		if r.Method == http.MethodGet && (r.URL.Path == "/api/v2/dags/etl" || r.URL.Path == "/api/v2/dags/etl/dagVersions" || r.URL.Path == "/api/v2/dags/etl/dagRuns") {
			serveDeployedState(w, r, deploy)
			return
		}
		ws.ServeHTTP(w, r)
	}))
	defer srv.Close()
	cfg := emptyConfig(t)
	if out, errOut, err := run(t, "deploy", dir, "--server", srv.URL, "--token", "x", "--yes", "--builder", builder, "--config", cfg); err != nil {
		t.Fatalf("deploy: %v\n%s\n%s", err, out, errOut)
	}
	if len(ws.registered()) != 1 {
		t.Fatalf("deploy registered %v, want one version", ws.registered())
	}
	out, _, err := run(t, "status", dir, "--server", srv.URL, "--token", "x", "--config", cfg)
	if err != nil || !strings.Contains(lineOf(out, "etl "), "in sync") {
		t.Fatalf("right after a deploy: err = %v, want in sync\n%s", err, out)
	}
	writeProjectFile(t, dir, "dag.py", "# dag, edited\n")
	out, _, err = run(t, "status", dir, "--server", srv.URL, "--token", "x", "--config", cfg)
	if exitCodeOf(err) != 1 || !strings.Contains(lineOf(out, "etl "), "drift: local changes not deployed") {
		t.Fatalf("after an edit: err = %v, want drift\n%s", err, out)
	}
}

// serveDeployedState answers the DAG, its versions and its runs from what
// the deploy registered on fake.
func serveDeployedState(w http.ResponseWriter, r *http.Request, fake *deployServer) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if len(fake.registered) == 0 {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	switch {
	case strings.HasSuffix(r.URL.Path, "/dagVersions"):
		parts := make([]string, 0, len(fake.registered))
		for i, l := range fake.registered {
			parts = append(parts, fmt.Sprintf(`{"bundle_version":%q,"version_number":%d,"is_current":%v}`, l, i+1, l == fake.current))
		}
		_, _ = io.WriteString(w, `{"dag_versions":[`+strings.Join(parts, ",")+`]}`)
	case strings.HasSuffix(r.URL.Path, "/dagRuns"):
		_, _ = io.WriteString(w, `{"dag_runs":[],"total_entries":0}`)
	default:
		_, _ = fmt.Fprintf(w, `{"dag_id":"etl","is_paused":%v}`, fake.paused)
	}
}

// TestStatusCurrentWithoutIsCurrent: a server that does not mark the current
// version (Airflow has no is_current) is read as running its newest one.
func TestStatusCurrentWithoutIsCurrent(t *testing.T) {
	isolateCLIEnv(t)
	fake := &statusServer{}
	url := newStatusServer(t, fake)
	root, dirs := statusWorkspace(t, []string{"dev=" + url}, "alpha")
	label := labelOf(t, dirs["alpha"])
	fake.dags["alpha"] = &statusDagFake{versions: []statusVersionFake{
		{"v-old", 1, "2026-09-01T00:00:00Z", false}, {label, 3, "2026-09-03T00:00:00Z", false}, {"v-mid", 2, "2026-09-02T00:00:00Z", false}}}
	t.Chdir(root)
	out, _, err := run(t, "status", "--config", emptyConfig(t))
	if err != nil || !strings.Contains(lineOf(out, "alpha "), "in sync") || !strings.Contains(out, label+" (#3)") {
		t.Errorf("err = %v, want in sync with version 3\n%s", err, out)
	}
}

// TestStatusNeedsAServer: with nothing naming a server, status says how to
// name one instead of reading the built-in localhost.
func TestStatusNeedsAServer(t *testing.T) {
	isolateCLIEnv(t)
	dir := versionProject(t, "registry:\n  url: ghcr.io/acme\n  image_name: etl\n")
	t.Chdir(dir)
	out, _, err := run(t, "status", "--config", emptyConfig(t))
	if exitCodeOf(err) != 2 || !strings.Contains(out+err.Error(), "--target") {
		t.Errorf("err = %v (exit %d), want exit 2 saying how to name a server\n%s", err, exitCodeOf(err), out)
	}
}

func TestStatusRefusesAnUnknownOutput(t *testing.T) {
	isolateCLIEnv(t)
	if _, _, err := run(t, "status", "--output", "yaml"); exitCodeOf(err) != 2 {
		t.Errorf("err = %v, want exit 2", err)
	}
}
