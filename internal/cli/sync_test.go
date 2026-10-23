package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/spf13/cobra"
)

// syncServer is statusServer that also answers what `dexaflow sync` writes
// and the DAG list it reads: GET /api/v2/dags, a register (POST
// .../versions, which adds a current version labeled with the spec's
// dag_version), PUT .../currentVersion and PATCH /api/v2/dags/{dag_id}. The
// register dry run answers 404, as a server that predates it. Every write
// is recorded in order.
type syncServer struct {
	st *statusServer
	mu sync.Mutex
	// writes lists each write as "METHOD path body".
	writes []string
	// failPatch answers every PATCH with 500.
	failPatch bool
	// failRegister answers the register of these dag_ids with 500.
	failRegister map[string]bool
	// failList answers GET /api/v2/dags with 500.
	failList bool
	// noRuns answers the runs list with 403, as for a deploy token, which
	// may not read runs.
	noRuns bool
}

func newSyncServer(t *testing.T, s *syncServer) string {
	t.Helper()
	if s.st == nil {
		s.st = &statusServer{}
	}
	if s.st.dags == nil {
		s.st.dags = map[string]*statusDagFake{}
	}
	srv := httptest.NewServer(s)
	t.Cleanup(srv.Close)
	return srv.URL
}

func (s *syncServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet && s.noRuns && strings.HasSuffix(r.URL.Path, "/dagRuns") {
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `{"title":"forbidden","detail":"the token's scopes do not grant this"}`)
		return
	}
	if r.Method == http.MethodGet && r.URL.Path != "/api/v2/dags" {
		s.st.ServeHTTP(w, r)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.st.mu.Lock()
	defer s.st.mu.Unlock()
	s.st.auth = append(s.st.auth, r.Header.Get("Authorization"))
	w.Header().Set("Content-Type", "application/json")
	body, _ := io.ReadAll(r.Body)
	if r.Method == http.MethodGet {
		if s.failList {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		s.serveList(w)
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/api/v2/dags/")
	id, sub, _ := strings.Cut(rest, "/")
	if sub == "versions/dry_run" {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	s.writes = append(s.writes, r.Method+" "+r.URL.Path+" "+strings.TrimSpace(string(body)))
	switch {
	case r.Method == http.MethodPost && sub == "versions" && s.failRegister[id]:
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `{"title":"boom","detail":"register refused by the fake"}`)
	case r.Method == http.MethodPost && sub == "versions":
		s.serveRegister(w, id, body)
	case r.Method == http.MethodPut && sub == "currentVersion":
		s.serveCurrent(w, id, body)
	case r.Method == http.MethodPatch && sub == "":
		if s.failPatch {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, `{"title":"boom","detail":"patch refused by the fake"}`)
			return
		}
		d := s.st.dags[id]
		d.paused = strings.Contains(string(body), "true")
		_, _ = fmt.Fprintf(w, `{"dag_id":%q,"is_paused":%v}`, id, d.paused)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

// serveList answers GET /api/v2/dags with every DAG, sorted.
func (s *syncServer) serveList(w http.ResponseWriter) {
	ids := make([]string, 0, len(s.st.dags))
	for id := range s.st.dags {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	parts := make([]string, 0, len(ids))
	for _, id := range ids {
		parts = append(parts, fmt.Sprintf(`{"dag_id":%q,"is_paused":%v}`, id, s.st.dags[id].paused))
	}
	_, _ = fmt.Fprintf(w, `{"dags":[%s],"total_entries":%d}`, strings.Join(parts, ","), len(ids))
}

// serveRegister adds the spec's dag_version as the current version.
func (s *syncServer) serveRegister(w http.ResponseWriter, id string, body []byte) {
	var spec struct {
		DagVersion string `json:"dag_version"`
	}
	_ = json.Unmarshal(body, &spec)
	d := s.st.dags[id]
	created := d == nil
	if created {
		d = &statusDagFake{paused: true}
		s.st.dags[id] = d
	}
	for i := range d.versions {
		d.versions[i].current = false
	}
	d.versions = append(d.versions, statusVersionFake{spec.DagVersion, len(d.versions) + 1, "2026-10-14T00:00:00Z", true})
	w.WriteHeader(http.StatusCreated)
	_, _ = fmt.Fprintf(w, `{"dag_id":%q,"version":%q,"spec_hash":"abc","created":true,"dag_created":%v,"is_paused":%v}`,
		id, spec.DagVersion, created, d.paused)
}

// serveCurrent makes the version labeled in the body current.
func (s *syncServer) serveCurrent(w http.ResponseWriter, id string, body []byte) {
	var req struct {
		Version string `json:"version"`
	}
	_ = json.Unmarshal(body, &req)
	d := s.st.dags[id]
	from, to := "null", ""
	for i := range d.versions {
		v := &d.versions[i]
		if v.current {
			from = fmt.Sprintf(`{"bundle_version":%q,"version_number":%d}`, v.label, v.number)
		}
		v.current = v.label == req.Version
		if v.current {
			to = fmt.Sprintf(`{"bundle_version":%q,"version_number":%d}`, v.label, v.number)
		}
	}
	if to == "" {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	_, _ = fmt.Fprintf(w, `{"dag_id":%q,"changed":true,"from":%s,"to":%s}`, id, from, to)
}

// writesOf returns the recorded writes.
func (s *syncServer) writesOf() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.writes)
}

// syncParser stands in for the DAG parser: it writes a dag.json whose
// dag_id is the first comment of the project's dag.py (statusWorkspace
// writes "# <dag_id>"), with the version and image it is given.
func syncParser(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "parser.sh")
	body := "#!/usr/bin/env bash\n" +
		"out=\"\"; ver=\"\"; img=\"\"; src=\"\"\n" +
		"while [ $# -gt 0 ]; do case \"$1\" in --output) out=\"$2\"; shift 2;; --dag-version) ver=\"$2\"; shift 2;; " +
		"--image) img=\"$2\"; shift 2;; --source) src=\"$2\"; shift 2;; *) shift;; esac; done\n" +
		"[ -d \"$src\" ] && src=\"$src/dag.py\"\n" +
		"id=$(sed -n 's/^# //p' \"$src\" | head -n 1)\n" +
		"cat > \"$out\" <<JSON\n" +
		"{\"schema_version\":\"1.0\",\"dag_id\":\"$id\",\"dag_version\":\"$ver\",\"image\":\"$img\"," +
		"\"tasks\":[{\"task_id\":\"t\",\"type\":\"python\",\"entrypoint\":\"dag:t\"}]}\n" +
		"JSON\n"
	if err := os.WriteFile(p, []byte(body), 0o755); err != nil { //nolint:gosec // test fixture must be executable
		t.Fatal(err)
	}
	return p
}

// syncFixture is a workspace in every state sync acts on, against one
// target (dev, the default): synced is current, older matches an older
// version (behind), edited and fresh would deploy (drift, not deployed),
// and the server also holds orphan (active) and retired (paused), which the
// workspace does not. The projects push to a fake registry holding an image
// under every tag, so a deploy reuses it and builds nothing.
type syncFixture struct {
	root    string
	dirs    map[string]string
	fake    *syncServer
	url     string
	cfg     string
	builder string
	labels  map[string]string
}

func newSyncFixture(t *testing.T) *syncFixture {
	t.Helper()
	isolateCLIEnv(t)
	f := &syncFixture{fake: &syncServer{}}
	f.url = newSyncServer(t, f.fake)
	f.root, f.dirs = statusWorkspace(t, []string{"dev=" + f.url}, "synced", "older", "edited", "fresh")
	reg := newFakeOCIRegistry(t, singleManifest)
	f.labels = map[string]string{}
	for id, dir := range f.dirs {
		writeProjectFile(t, dir, "dexaflow.yaml", "dag_id: "+id+"\nregistry:\n  url: "+reg.host+"/team\n  image_name: "+id+"\nbuild:\n  platforms: [linux/amd64]\n")
		f.labels[id] = labelOf(t, dir)
	}
	t.Setenv("LEOFLOW_PARSER_CMD", syncParser(t))
	f.builder, _ = argvBuilder(t)
	dags := f.fake.st.dags
	dags["synced"] = &statusDagFake{versions: []statusVersionFake{{f.labels["synced"], 1, "2026-10-01T12:00:00Z", true}}}
	dags["older"] = &statusDagFake{versions: []statusVersionFake{
		{f.labels["older"], 1, "2026-09-01T00:00:00Z", false}, {"v-newer", 2, "2026-09-05T10:00:00Z", true}}}
	dags["edited"] = &statusDagFake{versions: []statusVersionFake{{"v-other", 4, "2026-10-03T09:15:00Z", true}}}
	dags["orphan"] = &statusDagFake{versions: []statusVersionFake{{"v-gone", 1, "2026-08-01T00:00:00Z", true}}}
	dags["retired"] = &statusDagFake{paused: true, versions: []statusVersionFake{{"v-old", 1, "2026-08-01T00:00:00Z", true}}}
	f.cfg = sessionsConfig(t, []string{f.url}, []string{"tok"})
	return f
}

// sync runs `dexaflow sync <root>` with args.
func (f *syncFixture) sync(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	all := append([]string{"sync", f.root, "--config", f.cfg, "--builder", f.builder}, args...)
	return run(t, all...)
}

// TestSyncDryRunReportsEveryDAGAndWritesNothing: a dry run says, per DAG,
// what sync would do, exits 1 on drift, and writes nothing to the server or
// the projects.
func TestSyncDryRunReportsEveryDAGAndWritesNothing(t *testing.T) {
	f := newSyncFixture(t)
	out, _, err := f.sync(t, "--dry-run", "--prune", "--rollback")
	if code := exitCodeOf(err); code != syncExitDrift {
		t.Fatalf("exit %d (err %v), want %d for drift\n%s", code, err, syncExitDrift, out)
	}
	for dag, want := range map[string][]string{
		"synced":  {"in sync", "none"},
		"older":   {"behind", "rollback to " + f.labels["older"] + " (#1)"},
		"edited":  {"drift", "deploy " + f.labels["edited"]},
		"fresh":   {"not deployed", "deploy " + f.labels["fresh"]},
		"orphan":  {"not in workspace", "pause"},
		"retired": {"not in workspace", "none: already paused"},
	} {
		line := lineOf(out, dag+" ")
		for _, w := range want {
			if !strings.Contains(line, w) {
				t.Errorf("%s: line %q does not say %q\n%s", dag, line, w, out)
			}
		}
	}
	if !strings.Contains(out, "target dev: "+f.url) {
		t.Errorf("sync must name the target and its server:\n%s", out)
	}
	if !strings.Contains(out, "dry run: 4 change(s) would be made (2 deploy, 1 rollback, 1 pause)") {
		t.Errorf("sync must close with what it would change:\n%s", out)
	}
	if w := f.fake.writesOf(); len(w) != 0 {
		t.Errorf("a dry run wrote to the server: %v", w)
	}
	if len(f.fake.st.writes) != 0 {
		t.Errorf("a dry run wrote to the server: %v", f.fake.st.writes)
	}
	for id, d := range f.dirs {
		if fileExists(filepath.Join(d, "dag.json")) {
			t.Errorf("a dry run compiled %s", id)
		}
	}
	for _, a := range f.fake.st.auth {
		if a != "Bearer tok" {
			t.Errorf("a read went with Authorization %q, want the target's saved token", a)
		}
	}
}

// TestSyncDryRunWithoutPruneOnlyReportsAbsentDAGs: without --prune a DAG the
// workspace does not hold is listed and changes nothing, so a target whose
// workspace DAGs are all in sync passes the check.
func TestSyncDryRunWithoutPruneOnlyReportsAbsentDAGs(t *testing.T) {
	f := newSyncFixture(t)
	for _, id := range []string{"older", "edited", "fresh"} {
		delete(f.fake.st.dags, id)
		if err := os.RemoveAll(f.dirs[id]); err != nil {
			t.Fatal(err)
		}
	}
	out, _, err := f.sync(t, "--dry-run")
	if err != nil {
		t.Fatalf("in sync but for absent DAGs, without --prune: err = %v, want exit 0\n%s", err, out)
	}
	if l := lineOf(out, "orphan "); !strings.Contains(l, "not in workspace") || !strings.Contains(l, "none: pass --prune to pause it") {
		t.Errorf("orphan: %q\n%s", l, out)
	}
	if !strings.Contains(out, "dry run: in sync, nothing to change") {
		t.Errorf("want the in-sync closing line:\n%s", out)
	}
	out, _, err = f.sync(t, "--dry-run", "--prune")
	if exitCodeOf(err) != syncExitDrift || !strings.Contains(lineOf(out, "orphan "), "pause") {
		t.Errorf("with --prune the active orphan is drift: err = %v\n%s", err, out)
	}
}

// TestSyncAppliesThroughDeploy: without --dry-run, sync deploys what is new
// or changed through the deploy path, makes the matching older version
// current, pauses the absent active DAG with --prune, and leaves the rest
// alone. A second sync then finds nothing to change.
func TestSyncAppliesThroughDeploy(t *testing.T) {
	f := newSyncFixture(t)
	out, stderr, err := f.sync(t, "--prune", "--rollback", "--yes")
	if err != nil {
		t.Fatalf("sync: %v\n%s\n%s", err, out, stderr)
	}
	writes := f.fake.writesOf()
	want := map[string]string{
		"edited": "POST /api/v2/dags/edited/versions",
		"fresh":  "POST /api/v2/dags/fresh/versions",
		"older":  `PUT /api/v2/dags/older/currentVersion {"version":"` + f.labels["older"] + `"}`,
		"orphan": `PATCH /api/v2/dags/orphan {"is_paused":true}`,
	}
	if len(writes) != len(want) {
		t.Errorf("writes = %q, want exactly %d", writes, len(want))
	}
	for dag, w := range want {
		if !slices.ContainsFunc(writes, func(s string) bool { return strings.HasPrefix(s, w) }) {
			t.Errorf("%s: no write %q in %q", dag, w, writes)
		}
	}
	for _, id := range []string{"edited", "fresh"} {
		for _, wr := range writes {
			if strings.HasPrefix(wr, "POST /api/v2/dags/"+id+"/versions") && !strings.Contains(wr, `"dag_version":"`+f.labels[id]+`"`) {
				t.Errorf("%s registered %q, want its content label %s", id, wr, f.labels[id])
			}
		}
	}
	for _, d := range []string{"synced", "retired"} {
		for _, wr := range writes {
			if strings.Contains(wr, "/dags/"+d) {
				t.Errorf("sync wrote to %s, which needed nothing: %s", d, wr)
			}
		}
	}
	for dag, w := range map[string]string{"edited": "deployed", "fresh": "deployed", "older": "rolled back", "orphan": "paused", "synced": "none"} {
		if l := lineOf(out, dag+" "); !strings.Contains(l, w) {
			t.Errorf("%s: %q, want %q\n%s", dag, l, w, out)
		}
	}
	if !strings.Contains(out, "==> deploying edited") {
		t.Errorf("the deploys must go through deploy and print its lines:\n%s", out)
	}
	out, _, err = f.sync(t, "--dry-run", "--prune")
	if err != nil {
		t.Errorf("after a sync, a dry run must find nothing to change: %v\n%s", err, out)
	}
}

// TestSyncJSONDryRun locks the machine-readable shape of a dry run.
func TestSyncJSONDryRun(t *testing.T) {
	f := newSyncFixture(t)
	out, stderr, err := f.sync(t, "--dry-run", "--prune", "--rollback", "--output", "json")
	if exitCodeOf(err) != syncExitDrift {
		t.Fatalf("exit %d (err %v)\n%s", exitCodeOf(err), err, stderr)
	}
	got := decodeSyncJSON(t, out)
	if got.ExitCode != syncExitDrift || !got.DryRun || !got.Prune || got.Target != "dev" || got.Server != f.url || got.Workspace != f.root {
		t.Fatalf("report = %+v", got)
	}
	by := map[string]syncJSONDag{}
	for _, d := range got.Dags {
		by[d.DagID] = d
	}
	for id, want := range map[string][2]string{
		"synced": {"in_sync", "none"}, "older": {"behind", "rollback"}, "edited": {"drift", "deploy"},
		"fresh": {"not_deployed", "deploy"}, "orphan": {"not_in_workspace", "pause"}, "retired": {"not_in_workspace", "none"},
	} {
		d := by[id]
		if d.State != want[0] || d.Action != want[1] {
			t.Errorf("%s = state %q action %q, want %v", id, d.State, d.Action, want)
		}
		if wantResult := map[bool]string{true: "", false: "planned"}[want[1] == "none"]; d.Result != wantResult {
			t.Errorf("%s: result %q, want %q", id, d.Result, wantResult)
		}
	}
	if by["edited"].LocalVersion != f.labels["edited"] || by["edited"].Path != f.dirs["edited"] || by["orphan"].Path != "" {
		t.Errorf("edited = %+v, orphan = %+v", by["edited"], by["orphan"])
	}
	if by["older"].MatchedVersion == nil || by["older"].MatchedVersion.Label != f.labels["older"] {
		t.Errorf("older = %+v, want the matched version", by["older"])
	}
	if s := got.Summary; s["in_sync"] != 1 || s["deploy"] != 2 || s["rollback"] != 1 || s["pause"] != 1 || s["not_in_workspace"] != 2 || s["errors"] != 0 {
		t.Errorf("summary = %v", s)
	}
}

// TestSyncJSONApplied locks the machine-readable shape after an apply: each
// change applied, the deploy's own result, and the deploy lines on stderr.
func TestSyncJSONApplied(t *testing.T) {
	f := newSyncFixture(t)
	out, stderr, err := f.sync(t, "--prune", "--rollback", "--yes", "--output", "json")
	if err != nil {
		t.Fatalf("sync --output json: %v\n%s", err, stderr)
	}
	got := decodeSyncJSON(t, out)
	by := map[string]syncJSONDag{}
	for _, d := range got.Dags {
		by[d.DagID] = d
	}
	if got.DryRun || got.ExitCode != 0 || by["edited"].Result != "applied" || by["edited"].Deploy == nil ||
		by["edited"].Deploy.Version != f.labels["edited"] || by["orphan"].Result != "applied" || by["synced"].Result != "" {
		t.Errorf("applied report = %+v", got)
	}
	if !strings.Contains(stderr, "==> deploying edited") {
		t.Errorf("with --output json the deploy lines go to stderr:\n%s", stderr)
	}
}

type syncJSONDag struct {
	DagID          string         `json:"dag_id"`
	Path           string         `json:"path"`
	State          string         `json:"state"`
	LocalVersion   string         `json:"local_version"`
	CurrentVersion *statusVersion `json:"current_version"`
	MatchedVersion *statusVersion `json:"matched_version"`
	IsPaused       *bool          `json:"is_paused"`
	Action         string         `json:"action"`
	Result         string         `json:"result"`
	Deploy         *struct {
		Status  string `json:"status"`
		Version string `json:"version"`
	} `json:"deploy"`
	Error string `json:"error"`
}

type syncJSON struct {
	Workspace string         `json:"workspace"`
	Target    string         `json:"target"`
	Server    string         `json:"server"`
	Source    string         `json:"source"`
	DryRun    bool           `json:"dry_run"`
	Prune     bool           `json:"prune"`
	Error     string         `json:"error"`
	Dags      []syncJSONDag  `json:"dags"`
	Summary   map[string]int `json:"summary"`
	ExitCode  int            `json:"exit_code"`
}

func decodeSyncJSON(t *testing.T, out string) syncJSON {
	t.Helper()
	var got syncJSON
	dec := json.NewDecoder(strings.NewReader(out))
	if err := dec.Decode(&got); err != nil {
		t.Fatalf("stdout is not one JSON document: %v\n%s", err, out)
	}
	if dec.More() {
		t.Fatalf("stdout holds more than one JSON document:\n%s", out)
	}
	return got
}

// TestSyncFailedChangeExitsTwo: a change the server refuses is reported on
// its DAG, the other changes still go through, and the exit code is 2.
func TestSyncFailedChangeExitsTwo(t *testing.T) {
	f := newSyncFixture(t)
	f.fake.failPatch = true
	f.fake.failRegister = map[string]bool{"fresh": true}
	out, _, err := f.sync(t, "--prune", "--rollback", "--yes")
	if exitCodeOf(err) != syncExitError {
		t.Fatalf("exit %d (err %v), want %d\n%s", exitCodeOf(err), err, syncExitError, out)
	}
	if l := lineOf(out, "orphan "); !strings.Contains(l, "pause failed") || !strings.Contains(l, "patch refused by the fake") {
		t.Errorf("orphan: %q\n%s", l, out)
	}
	if l := lineOf(out, "fresh "); !strings.Contains(l, "deploy failed") || !strings.Contains(l, "register refused by the fake") {
		t.Errorf("fresh: %q\n%s", l, out)
	}
	if !strings.Contains(lineOf(out, "edited "), "deployed") || !strings.Contains(lineOf(out, "older "), "rolled back") {
		t.Errorf("the other changes must still be applied:\n%s", out)
	}
	if !strings.Contains(out, "2 error(s)") {
		t.Errorf("the closing line must count the failures:\n%s", out)
	}
}

// TestSyncChangesNothingWhenTheDAGListFails: without the list of the
// target's DAGs the plan is incomplete, so nothing is applied, not even the
// deploys it could tell.
func TestSyncChangesNothingWhenTheDAGListFails(t *testing.T) {
	f := newSyncFixture(t)
	f.fake.failList = true
	out, _, err := f.sync(t, "--prune", "--yes")
	if exitCodeOf(err) != syncExitError || !strings.Contains(out, "error: reading the DAGs of") || !strings.Contains(out, "nothing was changed") {
		t.Errorf("err = %v (exit %d), want exit 2 naming the failed list\n%s", err, exitCodeOf(err), out)
	}
	if w := f.fake.writesOf(); len(w) != 0 {
		t.Errorf("sync with an incomplete plan wrote %v", w)
	}
}

// TestSyncUsesTheTargetAndTheWorkspaceOfDir: --target picks the server and
// its saved token, and the targets are those of the workspace at <dir>, from
// whatever folder sync runs in.
func TestSyncUsesTheTargetAndTheWorkspaceOfDir(t *testing.T) {
	isolateCLIEnv(t)
	dev, prod := &syncServer{}, &syncServer{}
	devURL, prodURL := newSyncServer(t, dev), newSyncServer(t, prod)
	root, dirs := statusWorkspace(t, []string{"dev=" + devURL, "prod=" + prodURL}, "alpha")
	prod.st.dags["alpha"] = &statusDagFake{versions: []statusVersionFake{{labelOf(t, dirs["alpha"]), 1, "2026-10-01T12:00:00Z", true}}}
	cfg := sessionsConfig(t, []string{devURL, prodURL}, []string{"dev-tok", "prod-tok"})
	elsewhere := t.TempDir()
	t.Chdir(elsewhere)
	out, _, err := run(t, "sync", root, "--dry-run", "--target", "prod", "--config", cfg)
	if err != nil {
		t.Fatalf("sync --target prod: %v\n%s", err, out)
	}
	if !strings.Contains(out, "target prod: "+prodURL) || !strings.Contains(lineOf(out, "alpha "), "in sync") {
		t.Errorf("want prod, in sync:\n%s", out)
	}
	if len(dev.st.auth) != 0 {
		t.Errorf("dev was read with --target prod")
	}
	for _, a := range prod.st.auth {
		if a != "Bearer prod-tok" {
			t.Errorf("prod got Authorization %q, want its own token", a)
		}
	}
	if wd, _ := os.Getwd(); wd != elsewhere {
		t.Errorf("sync changed the working directory to %s", wd)
	}
	// A relative --config and a relative <dir> name what they name from the
	// folder sync runs in.
	t.Chdir(filepath.Dir(cfg))
	rel, rerr := filepath.Rel(filepath.Dir(cfg), root)
	if rerr != nil {
		t.Fatal(rerr)
	}
	out, _, err = run(t, "sync", rel, "--dry-run", "--config", filepath.Base(cfg))
	if exitCodeOf(err) != syncExitDrift || !strings.Contains(out, "target dev: "+devURL) {
		t.Errorf("without --target, the workspace's default target: err = %v\n%s", err, out)
	}
	for _, a := range dev.st.auth {
		if a != "Bearer dev-tok" {
			t.Errorf("dev got Authorization %q, want the token the relative --config saves for it", a)
		}
	}
	if wd, _ := os.Getwd(); wd != filepath.Dir(cfg) {
		t.Errorf("sync changed the working directory to %s", wd)
	}
}

// TestSyncRefusesAnEmptyDirAndBadUsage: a folder with no DAG project is
// never taken as "nothing is desired" (with --prune it would pause every
// DAG), and misuse exits 2.
func TestSyncRefusesAnEmptyDirAndBadUsage(t *testing.T) {
	f := newSyncFixture(t)
	empty := t.TempDir()
	out, _, err := run(t, "sync", empty, "--prune", "--yes", "--config", f.cfg, "--server", f.url)
	if exitCodeOf(err) != syncExitError || !strings.Contains(err.Error(), "no DAG projects") {
		t.Errorf("empty dir: err = %v, want exit 2 saying it holds no DAG projects\n%s", err, out)
	}
	if w := f.fake.writesOf(); len(w) != 0 {
		t.Errorf("sync of an empty folder wrote %v", w)
	}
	for _, args := range [][]string{
		{"sync"},
		{"sync", f.root, "--output", "yaml"},
		{"sync", filepath.Join(f.root, "missing")},
	} {
		if _, _, err := run(t, append(args, "--config", f.cfg)...); exitCodeOf(err) != syncExitError {
			t.Errorf("%v: err = %v (exit %d), want exit 2", args, err, exitCodeOf(err))
		}
	}
}

// TestSyncUnreadableTargetExitsTwo: a target that cannot be read is an
// error, never a drift, and nothing is changed.
func TestSyncUnreadableTargetExitsTwo(t *testing.T) {
	f := newSyncFixture(t)
	f.fake.st.status = http.StatusUnauthorized
	out, _, err := f.sync(t, "--prune", "--yes")
	if exitCodeOf(err) != syncExitError || !strings.Contains(out, "error:") {
		t.Errorf("err = %v (exit %d), want exit 2 with an error line\n%s", err, exitCodeOf(err), out)
	}
	if w := f.fake.writesOf(); len(w) != 0 {
		t.Errorf("sync of an unreadable target wrote %v", w)
	}
}

// TestSyncWorksWithADeployToken: a deploy token may not read runs, and sync
// reads none, so every DAG still gets its state.
func TestSyncWorksWithADeployToken(t *testing.T) {
	f := newSyncFixture(t)
	f.fake.noRuns = true
	out, _, err := f.sync(t, "--dry-run")
	if exitCodeOf(err) != syncExitDrift || strings.Contains(out, "error") {
		t.Errorf("err = %v (exit %d), want exit 1 with no error\n%s", err, exitCodeOf(err), out)
	}
	if !strings.Contains(lineOf(out, "synced "), "in sync") {
		t.Errorf("synced: %q\n%s", lineOf(out, "synced "), out)
	}
}

// TestSyncLeavesABehindDAGWithoutRollback: a DAG whose content is an older
// registered version is reported and left alone unless --rollback is given;
// it still counts as drift, in a dry run and after an apply.
func TestSyncLeavesABehindDAGWithoutRollback(t *testing.T) {
	f := newSyncFixture(t)
	out, _, err := f.sync(t, "--dry-run")
	if exitCodeOf(err) != syncExitDrift {
		t.Fatalf("exit %d (err %v), want 1\n%s", exitCodeOf(err), err, out)
	}
	want := "none: registered content is newer than the workspace; run with --rollback to make " + f.labels["older"] + " current"
	if l := lineOf(out, "older "); !strings.Contains(l, "behind") || !strings.Contains(l, want) {
		t.Errorf("older: %q, want %q\n%s", l, want, out)
	}
	out, _, err = f.sync(t, "--yes")
	if exitCodeOf(err) != syncExitDrift {
		t.Errorf("a behind DAG left alone: exit %d (err %v), want 1\n%s", exitCodeOf(err), err, out)
	}
	for _, w := range f.fake.writesOf() {
		if strings.Contains(w, "currentVersion") {
			t.Errorf("rolled back without --rollback: %s", w)
		}
	}
	if !strings.Contains(lineOf(out, "edited "), "deployed") {
		t.Errorf("the deploys still go through:\n%s", out)
	}
}

// brokenProject adds to the fixture a project whose dexaflow.yaml does not
// parse, in folder dir of dags/, for a DAG registered and active as dagID.
func (f *syncFixture) brokenProject(t *testing.T, dir, dagID string, withDagPy bool) {
	t.Helper()
	p := filepath.Join(f.root, "dags", dir)
	if withDagPy {
		writeProjectFile(t, p, "dag.py", "# "+dagID+"\n")
	}
	writeProjectFile(t, p, "dexaflow.yaml", "dag_id: "+dagID+"\ndbt: {project_dir: .\n")
	f.fake.st.dags[dagID] = &statusDagFake{versions: []statusVersionFake{{"v-1", 1, "2026-08-01T00:00:00Z", true}}}
}

// TestSyncPruneRefusesWhenAConfigDoesNotParse: a dexaflow.yaml that does not
// parse hides the project's dag_id (a project with a dag.py is discovered
// under its folder name, a dbt project not at all), so its DAG would read as
// absent and be paused. With --prune nothing at all is applied.
func TestSyncPruneRefusesWhenAConfigDoesNotParse(t *testing.T) {
	for _, tc := range []struct {
		name     string
		withDag  bool
		dir, dag string
	}{
		{"a project with dag.py", true, "billing_dir", "billing"},
		{"a dbt project", false, "marts", "marts"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newSyncFixture(t)
			f.brokenProject(t, tc.dir, tc.dag, tc.withDag)
			out, _, err := f.sync(t, "--prune", "--rollback", "--yes")
			if exitCodeOf(err) != syncExitError {
				t.Fatalf("exit %d (err %v), want 2\n%s", exitCodeOf(err), err, out)
			}
			if w := f.fake.writesOf(); len(w) != 0 {
				t.Errorf("sync applied with a project it cannot read: %v", w)
			}
			if !strings.Contains(out, filepath.Join(f.root, "dags", tc.dir)) || !strings.Contains(out, "nothing was changed") {
				t.Errorf("the report must name the broken project and say nothing changed:\n%s", out)
			}
			if !strings.Contains(out, "--prune") {
				t.Errorf("the refusal must say why (--prune):\n%s", out)
			}
			out, _, err = f.sync(t, "--prune", "--dry-run")
			if exitCodeOf(err) != syncExitError {
				t.Errorf("dry run: exit %d (err %v), want 2\n%s", exitCodeOf(err), err, out)
			}
			// Without --prune the project is an error row of its own, and
			// the exit code says so.
			out, _, err = f.sync(t, "--dry-run", "--rollback")
			row := lineOf(out, tc.dir+" ") // discovered under its folder name
			if !tc.withDag {
				row = lineOf(out, "("+filepath.Join(f.root, "dags", tc.dir)+")")
			}
			if exitCodeOf(err) != syncExitError || !strings.Contains(row, "error") {
				t.Errorf("without --prune: exit %d, row %q, want exit 2 and an error row\n%s", exitCodeOf(err), row, out)
			}
		})
	}
}

// TestSyncPruneRefusesWhenADAGIsInError: a project whose DAG cannot be read
// blocks --prune too.
func TestSyncPruneRefusesWhenADAGIsInError(t *testing.T) {
	f := newSyncFixture(t)
	writeProjectFile(t, f.dirs["edited"], "dexaflow.yaml", "dag_id: edited\n") // no registry: no label
	out, _, err := f.sync(t, "--prune", "--rollback", "--yes")
	if exitCodeOf(err) != syncExitError || len(f.fake.writesOf()) != 0 {
		t.Errorf("exit %d (err %v), writes %v; want exit 2 and no writes\n%s", exitCodeOf(err), err, f.fake.writesOf(), out)
	}
	out, _, err = f.sync(t, "--rollback", "--yes")
	if exitCodeOf(err) != syncExitError || !strings.Contains(lineOf(out, "fresh "), "deployed") {
		t.Errorf("without --prune the other changes go through: exit %d\n%s", exitCodeOf(err), out)
	}
}

// TestSyncPruneNeedsYesWithoutATerminal: in CI there is no prompt, so a
// prune must be asked for with --yes.
func TestSyncPruneNeedsYesWithoutATerminal(t *testing.T) {
	f := newSyncFixture(t)
	_, _, err := f.sync(t, "--prune")
	if exitCodeOf(err) != syncExitError || !strings.Contains(err.Error(), "--yes") {
		t.Errorf("err = %v (exit %d), want exit 2 asking for --yes", err, exitCodeOf(err))
	}
	if w := f.fake.writesOf(); len(w) != 0 {
		t.Errorf("wrote %v", w)
	}
	if _, _, err := f.sync(t, "--prune", "--dry-run", "--rollback"); exitCodeOf(err) != syncExitDrift {
		t.Errorf("a dry run needs no --yes: %v", err)
	}
}

// TestSyncMaxPrune: more pauses than the workspace has projects (or than
// --max-prune) refuse the whole apply.
func TestSyncMaxPrune(t *testing.T) {
	f := newSyncFixture(t)
	for i := range 4 {
		f.fake.st.dags[fmt.Sprintf("stray%d", i)] = &statusDagFake{versions: []statusVersionFake{{"v", 1, "2026-08-01T00:00:00Z", true}}}
	}
	out, _, err := f.sync(t, "--prune", "--rollback", "--yes")
	if exitCodeOf(err) != syncExitError || !strings.Contains(out, "--max-prune") || len(f.fake.writesOf()) != 0 {
		t.Fatalf("5 pauses for 4 projects: exit %d (err %v), writes %v\n%s", exitCodeOf(err), err, f.fake.writesOf(), out)
	}
	out, _, err = f.sync(t, "--prune", "--rollback", "--yes", "--max-prune", "5")
	if err != nil {
		t.Fatalf("--max-prune 5: %v\n%s", err, out)
	}
	n := 0
	for _, w := range f.fake.writesOf() {
		if strings.HasPrefix(w, "PATCH ") {
			n++
		}
	}
	if n != 5 {
		t.Errorf("paused %d DAGs, want 5", n)
	}
}

// TestSyncListFailureWithoutPruneIsAWarning: without --prune the DAG list
// only names the absent DAGs, so a failed list is a warning and the deploys
// and rollbacks still go through.
func TestSyncListFailureWithoutPruneIsAWarning(t *testing.T) {
	f := newSyncFixture(t)
	f.fake.failList = true
	out, stderr, err := f.sync(t, "--rollback", "--yes")
	if err != nil {
		t.Fatalf("err = %v, want exit 0\n%s\n%s", err, out, stderr)
	}
	if !strings.Contains(stderr, "warning: could not list the DAGs of") {
		t.Errorf("want a warning on stderr:\n%s", stderr)
	}
	if !strings.Contains(lineOf(out, "edited "), "deployed") || !strings.Contains(lineOf(out, "older "), "rolled back") {
		t.Errorf("deploys and rollbacks must go through:\n%s", out)
	}
}

// TestSyncReadsTheTokenVariable: DEXAFLOW_TOKEN is sent as for deploy.
func TestSyncReadsTheTokenVariable(t *testing.T) {
	f := newSyncFixture(t)
	t.Setenv("DEXAFLOW_TOKEN", "env-tok")
	if _, _, err := run(t, "sync", f.root, "--dry-run", "--config", emptyConfig(t)); exitCodeOf(err) != syncExitDrift {
		t.Fatalf("err = %v", err)
	}
	for _, a := range f.fake.st.auth {
		if a != "Bearer env-tok" {
			t.Errorf("Authorization %q, want DEXAFLOW_TOKEN", a)
		}
	}
}

// runSyncInteractive runs the command tree with a sync that prompts, as for
// a person at a terminal deploying to a remote server (the fakes listen on
// loopback, which never prompts).
func runSyncInteractive(t *testing.T, stdin string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	root := NewRootCommand()
	old, _, ferr := root.Find([]string{"sync"})
	if ferr != nil {
		t.Fatal(ferr)
	}
	root.RemoveCommand(old)
	interactive := newSyncCommandWith(func(*cobra.Command, string) bool { return true })
	interactive.GroupID = old.GroupID
	root.AddCommand(interactive)
	var out, errb bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errb)
	root.SetIn(strings.NewReader(stdin))
	root.SetArgs(args)
	err = root.ExecuteContext(testCtx(nil))
	return out.String(), errb.String(), err
}

// TestSyncAsksBeforeApplying: at a terminal, sync shows the plan, asks
// naming what it will do, and applies only on yes.
func TestSyncAsksBeforeApplying(t *testing.T) {
	f := newSyncFixture(t)
	args := []string{"sync", f.root, "--config", f.cfg, "--builder", f.builder, "--prune", "--rollback"}
	out, _, err := runSyncInteractive(t, "n\n", args...)
	if exitCodeOf(err) != syncExitError || !strings.Contains(err.Error(), "aborted") {
		t.Errorf("declined: err = %v (exit %d), want exit 2, aborted\n%s", err, exitCodeOf(err), out)
	}
	if !strings.Contains(out, "Apply 2 deploys, 1 rollback and 1 pause on "+f.url+"? [y/N]") {
		t.Errorf("the prompt must name what will happen:\n%s", out)
	}
	if !strings.Contains(lineOf(out, "orphan "), "pause") {
		t.Errorf("the plan comes before the prompt:\n%s", out)
	}
	if w := f.fake.writesOf(); len(w) != 0 {
		t.Errorf("declined, yet wrote %v", w)
	}
	out, _, err = runSyncInteractive(t, "y\n", args...)
	_, after, asked := strings.Cut(out, "? [y/N]")
	if err != nil || !asked || len(f.fake.writesOf()) != 4 || !strings.Contains(lineOf(after, "orphan "), "paused") {
		t.Errorf("accepted: err = %v, writes %v\n%s", err, f.fake.writesOf(), out)
	}
	if _, _, err := runSyncInteractive(t, "y\n", append(args, "--output", "json")...); exitCodeOf(err) != syncExitError || !strings.Contains(err.Error(), "--yes") {
		t.Errorf("--output json at a prompt: err = %v, want exit 2 asking for --yes", err)
	}
}
