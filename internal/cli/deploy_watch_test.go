package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// syncBuffer is a bytes.Buffer safe to read while the watch writes to it.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// watchServer wraps deployServer to record each registered version as
// dag_id:label, to make it the DAG's current version as the server does, and
// to refuse registers while refuse is set.
type watchServer struct {
	fake     *deployServer
	mu       sync.Mutex
	versions []string
	refuse   bool
}

func (s *watchServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/versions") {
		body, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(body))
		var spec struct {
			DagID      string `json:"dag_id"`
			DagVersion string `json:"dag_version"`
		}
		if json.Unmarshal(body, &spec) == nil && spec.DagID != "" {
			s.mu.Lock()
			refuse := s.refuse
			if !refuse {
				s.versions = append(s.versions, spec.DagID+":"+spec.DagVersion)
			}
			s.mu.Unlock()
			if refuse {
				http.Error(w, `{"title":"invalid dag spec","detail":"connection warehouse is not defined"}`, http.StatusBadRequest)
				return
			}
			s.fake.mu.Lock()
			s.fake.current = spec.DagVersion
			s.fake.registered = append(s.fake.registered, spec.DagVersion)
			s.fake.mu.Unlock()
		}
	}
	s.fake.ServeHTTP(w, r)
}

func (s *watchServer) registered() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.versions...)
}

func (s *watchServer) setRefuse(v bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refuse = v
}

// watchRun is a `dexaflow deploy --watch` running in the background through
// the real cobra entry point.
type watchRun struct {
	out, errb *syncBuffer
	cancel    context.CancelFunc
	done      chan error
}

// fastWatch returns test dependencies whose watch polls every 10ms and
// settles a burst after settle.
func fastWatch(t *testing.T, settle time.Duration) *cliDeps {
	t.Helper()
	d := testDeps()
	d.watchInterval, d.watchSettle = 10*time.Millisecond, settle
	return d
}

// startWatch runs deploy --watch with args against srv under d until stop.
func startWatch(t *testing.T, d *cliDeps, srv *watchServer, builder string, args ...string) *watchRun {
	t.Helper()
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	ctx, cancel := context.WithCancel(testCtx(d))
	w := &watchRun{out: &syncBuffer{}, errb: &syncBuffer{}, cancel: cancel, done: make(chan error, 1)}
	root := NewRootCommand()
	root.SetOut(w.out)
	root.SetErr(w.errb)
	root.SetArgs(append([]string{"deploy", "--watch", "--server", ts.URL, "--token", "x", "--yes", "--builder", builder}, args...))
	go func() { w.done <- root.ExecuteContext(ctx) }()
	t.Cleanup(func() {
		cancel()
		<-w.done
	})
	return w
}

// waitFor polls until cond holds, failing the test after 10s.
func (w *watchRun) waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s\nstdout:\n%s\nstderr:\n%s", what, w.out.String(), w.errb.String())
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// watching waits for the watch to be past its first deploy.
func (w *watchRun) watching(t *testing.T) {
	t.Helper()
	w.waitFor(t, "the watch to start", func() bool { return strings.Contains(w.out.String(), "watching ") })
}

// stop cancels the watch, as Ctrl-C does, and returns its error.
func (w *watchRun) stop(t *testing.T) error {
	t.Helper()
	w.cancel()
	select {
	case err := <-w.done:
		w.done <- err // for the cleanup
		return err
	case <-time.After(10 * time.Second):
		t.Fatalf("the watch did not stop\n%s", w.out.String())
		return nil
	}
}

// save writes body to rel in dir as one move of the file: the content goes
// to a temporary file whose mtime is set a second ahead (so the poller sees
// a save even within the file system's time resolution), then a rename puts
// it in place. Writing in place and moving the mtime after is two moves, and
// a poll that falls between them on a loaded machine sees two saves.
func save(t *testing.T, dir, rel, body string) {
	t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	next := time.Now().Add(time.Second)
	if st, err := os.Stat(p); err == nil && !st.ModTime().Before(next) {
		next = st.ModTime().Add(time.Second)
	}
	tmp := filepath.Join(t.TempDir(), filepath.Base(rel))
	if err := os.WriteFile(tmp, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(tmp, next, next); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, p); err != nil {
		t.Fatal(err)
	}
}

// watchProject is a one-DAG project whose registry already holds an image
// under every tag, so a deploy takes the reuse path and never builds.
func watchProject(t *testing.T) (dir, builder string) {
	t.Helper()
	dir, _ = platformProjectWith(t, singleManifest, "build:\n  platforms: [linux/amd64]\n")
	builder, _ = argvBuilder(t)
	return dir, builder
}

func TestDeployWatchRedeploysASave(t *testing.T) {
	d := fastWatch(t, 30*time.Millisecond)
	dir, builder := watchProject(t)
	srv := &watchServer{fake: &deployServer{}}
	w := startWatch(t, d, srv, builder, dir)
	w.watching(t)
	first := labelOf(t, dir)
	if got := srv.registered(); len(got) != 1 || got[0] != "etl:"+first {
		t.Fatalf("first deploy registered %q, want [etl:%s]", got, first)
	}
	save(t, dir, "dag.py", "# dag, edited\n")
	second := labelOf(t, dir)
	w.waitFor(t, "the redeploy", func() bool { return len(srv.registered()) == 2 })
	if got := srv.registered(); got[1] != "etl:"+second || second == first {
		t.Errorf("the save registered %q, want etl:%s (a new label)", got[1], second)
	}
	w.waitFor(t, "the redeploy summary", func() bool { return strings.Count(w.out.String(), "Deployed etl -> ") == 2 })
	time.Sleep(200 * time.Millisecond) // time for a deploy to retrigger itself
	if err := w.stop(t); err != nil {
		t.Fatalf("Ctrl-C must exit 0, got %v", err)
	}
	if strings.Contains(w.out.String(), "no deployable change") {
		t.Errorf("a deploy retriggered the watch (it watches a file the deploy writes):\n%s", w.out.String())
	}
	if !strings.Contains(w.out.String(), "\n\nstopped watching\n") {
		t.Errorf("no closing line:\n%s", w.out.String())
	}
	if n := len(srv.registered()); n != 2 {
		t.Errorf("registered %d versions, want 2 (one per deployable save)", n)
	}
}

// Any file the image is built from is watched, not only dag.py and
// dexaflow.yaml: a new module in an include_paths folder is a save that
// redeploys.
func TestDeployWatchRedeploysANewHelperFile(t *testing.T) {
	d := fastWatch(t, 30*time.Millisecond)
	dir, _ := platformProjectWith(t, singleManifest, "build:\n  platforms: [linux/amd64]\ninclude_paths: [lib]\n")
	writeProjectFile(t, dir, filepath.Join("lib", "__init__.py"), "")
	builder, _ := argvBuilder(t)
	srv := &watchServer{fake: &deployServer{}}
	w := startWatch(t, d, srv, builder, dir)
	w.watching(t)
	save(t, dir, filepath.Join("lib", "helpers.py"), "def f():\n    return 1\n")
	w.waitFor(t, "the redeploy", func() bool { return len(srv.registered()) == 2 })
	if err := w.stop(t); err != nil {
		t.Fatal(err)
	}
	if got := srv.registered(); got[1] != "etl:"+labelOf(t, dir) {
		t.Errorf("the new helper registered %q, want etl:%s", got[1], labelOf(t, dir))
	}
}

// A save that leaves the content label as it is prints one line and
// registers nothing, once the server confirms the label is its current
// version.
func TestDeployWatchUnchangedSaveRegistersNothing(t *testing.T) {
	d := fastWatch(t, 30*time.Millisecond)
	dir, builder := watchProject(t)
	srv := &watchServer{fake: &deployServer{}}
	w := startWatch(t, d, srv, builder, dir)
	w.watching(t)
	before := w.out.String()
	srv.fake.mu.Lock()
	reads := srv.fake.reads
	srv.fake.mu.Unlock()
	save(t, dir, "dag.py", readProjectFile(t, dir, "dag.py"))
	w.waitFor(t, "the no-change line", func() bool { return strings.Contains(w.out.String(), "no deployable change") })
	if err := w.stop(t); err != nil {
		t.Fatal(err)
	}
	if got := srv.registered(); len(got) != 1 {
		t.Errorf("registered %q, want only the first deploy", got)
	}
	after := strings.TrimPrefix(w.out.String(), before)
	var lines []string
	for _, l := range strings.Split(after, "\n") {
		if l != "" {
			lines = append(lines, l)
		}
	}
	if len(lines) != 2 || !strings.Contains(lines[0], "etl: saved, no deployable change: version "+labelOf(t, dir)+" is the current version; nothing registered") {
		t.Errorf("an unchanged save printed %q, want one line (then the closing line)", lines)
	}
	srv.fake.mu.Lock()
	defer srv.fake.mu.Unlock()
	if srv.fake.reads == reads {
		t.Error("an unchanged save was skipped without asking the server which version is current")
	}
}

// A failed deploy (a register the server refuses, a config that does not
// load), the first one included, prints the error and the watch goes on; the
// next good save deploys.
func TestDeployWatchKeepsWatchingAfterAFailedDeploy(t *testing.T) {
	d := fastWatch(t, 30*time.Millisecond)
	dir, builder := watchProject(t)
	srv := &watchServer{fake: &deployServer{}, refuse: true}
	w := startWatch(t, d, srv, builder, dir)
	w.watching(t)
	if !strings.Contains(w.errb.String(), "deploy failed: ") || !strings.Contains(w.errb.String(), "connection warehouse is not defined") {
		t.Fatalf("the refused first deploy did not print its error:\n%s", w.errb.String())
	}
	good := readProjectFile(t, dir, "dexaflow.yaml")

	save(t, dir, "dexaflow.yaml", good+"schedule: [\n")
	w.waitFor(t, "the config error", func() bool {
		return strings.Contains(w.errb.String(), "deploy of etl failed: ") && strings.Contains(w.errb.String(), "yaml")
	})

	refusals := func(n int) func() bool {
		return func() bool { return strings.Count(w.errb.String(), "connection warehouse is not defined") == n }
	}
	save(t, dir, "dexaflow.yaml", good)
	w.waitFor(t, "the second refusal", refusals(2))
	save(t, dir, "dag.py", "# dag, v2\n")
	w.waitFor(t, "the third refusal", refusals(3))
	if got := srv.registered(); len(got) != 0 {
		t.Fatalf("registered %q while the server refused", got)
	}

	srv.setRefuse(false)
	save(t, dir, "dag.py", "# dag, v3\n")
	w.waitFor(t, "the fixed deploy", func() bool { return len(srv.registered()) == 1 })
	if got := srv.registered(); got[0] != "etl:"+labelOf(t, dir) {
		t.Errorf("the fixed save registered %q, want etl:%s", got[0], labelOf(t, dir))
	}
	if err := w.stop(t); err != nil {
		t.Fatalf("the watch must survive failed deploys and exit 0, got %v", err)
	}
}

// Saves in quick succession, as an editor's "save all" or a formatter makes,
// are one redeploy of the last content.
func TestDeployWatchDebouncesABurst(t *testing.T) {
	d := fastWatch(t, 400*time.Millisecond)
	dir, builder := watchProject(t)
	srv := &watchServer{fake: &deployServer{}}
	w := startWatch(t, d, srv, builder, dir)
	w.watching(t)
	for i, body := range []string{"# one\n", "# two\n", "# three\n"} {
		if i > 0 {
			time.Sleep(40 * time.Millisecond) // a few polls apart
		}
		save(t, dir, "dag.py", body)
	}
	w.waitFor(t, "the redeploy", func() bool { return len(srv.registered()) >= 2 })
	time.Sleep(600 * time.Millisecond) // past another settle window
	if err := w.stop(t); err != nil {
		t.Fatal(err)
	}
	if got := srv.registered(); len(got) != 2 || got[1] != "etl:"+labelOf(t, dir) {
		t.Errorf("a burst of three saves registered %q, want one redeploy of the last content (etl:%s)", got, labelOf(t, dir))
	}
}

// --paused and --active apply to the first deploy of the watch only: a
// redeploy never changes is_paused, so a DAG paused in the UI meanwhile stays
// paused.
func TestDeployWatchSetsIsPausedOnTheFirstDeployOnly(t *testing.T) {
	d := fastWatch(t, 30*time.Millisecond)
	dir, builder := watchProject(t)
	srv := &watchServer{fake: &deployServer{paused: true}}
	w := startWatch(t, d, srv, builder, dir, "--active")
	w.watching(t)
	save(t, dir, "dag.py", "# dag, edited\n")
	w.waitFor(t, "the redeploy", func() bool { return len(srv.registered()) == 2 })
	if err := w.stop(t); err != nil {
		t.Fatal(err)
	}
	srv.fake.mu.Lock()
	defer srv.fake.mu.Unlock()
	if q := srv.fake.regQueries; len(q) != 2 || q[0] != "is_paused=false" || q[1] != "" {
		t.Errorf("register queries = %q, want is_paused=false on the first deploy and nothing on the redeploy", q)
	}
}

// In a workspace, a save redeploys the project it belongs to and no other.
func TestDeployWatchRedeploysOnlyTheSavedProject(t *testing.T) {
	isolateCLIEnv(t)
	d := fastWatch(t, 30*time.Millisecond)
	reg := newFakeOCIRegistry(t, singleManifest)
	root := t.TempDir()
	writeTree(t, filepath.Join(root, workspaceFileName), "targets: {}\n")
	for _, id := range []string{"alpha", "beta"} {
		dir := filepath.Join(root, "dags", id)
		writeProjectFile(t, dir, "dag.py", "# "+id+"\n")
		writeProjectFile(t, dir, "dexaflow.yaml", "dag_id: "+id+"\nregistry:\n  url: "+reg.host+"/team\n  image_name: "+id+"\nbuild:\n  platforms: [linux/amd64]\n")
	}
	t.Setenv("LEOFLOW_PARSER_CMD", dagIDParser(t))
	builder, _ := argvBuilder(t)
	t.Chdir(root)
	srv := &watchServer{fake: &deployServer{}}
	w := startWatch(t, d, srv, builder, "--all", "--config", emptyConfig(t))
	w.watching(t)
	if got := srv.registered(); len(got) != 2 {
		t.Fatalf("the first deploy registered %q, want both DAGs", got)
	}
	beta := filepath.Join(root, "dags", "beta")
	save(t, beta, "dag.py", "# beta, edited\n")
	w.waitFor(t, "the redeploy", func() bool { return len(srv.registered()) == 3 })
	time.Sleep(100 * time.Millisecond)
	if err := w.stop(t); err != nil {
		t.Fatal(err)
	}
	if got := srv.registered(); len(got) != 3 || got[2] != "beta:"+labelOf(t, beta) {
		t.Errorf("registered %q, want the save to redeploy beta alone", got)
	}
	if strings.Contains(w.out.String(), "alpha: saved") {
		t.Errorf("alpha was redeployed though nothing of it changed:\n%s", w.out.String())
	}
}

// dagIDParser is a parser stub that compiles a spec named after the dag_id
// of the project config the CLI hands it.
func dagIDParser(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "parser.sh")
	body := "#!/usr/bin/env bash\n" +
		"out=\"\"; ver=\"\"; img=\"\"\n" +
		"while [ $# -gt 0 ]; do case \"$1\" in --output) out=\"$2\"; shift 2;; --dag-version) ver=\"$2\"; shift 2;; --image) img=\"$2\"; shift 2;; *) shift;; esac; done\n" +
		"id=$(printf '%s' \"$LEOFLOW_PROJECT_CONFIG_JSON\" | sed -n 's/.*\"dag_id\":\"\\([^\"]*\\)\".*/\\1/p')\n" +
		"cat > \"$out\" <<JSON\n" +
		"{\"schema_version\":\"1.0\",\"dag_id\":\"$id\",\"dag_version\":\"$ver\",\"image\":\"$img\"," +
		"\"tasks\":[{\"task_id\":\"t\",\"type\":\"python\",\"entrypoint\":\"dag:t\"}]}\n" +
		"JSON\n"
	writeExecutable(t, p, body)
	return p
}

// Flags that make no sense with a watch are refused before anything runs.
func TestDeployWatchRefusesFlagsThatCannotRepeat(t *testing.T) {
	dir := versionProject(t, "")
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"--trigger", "--follow"}, "--watch cannot be combined with --trigger --follow"},
		{[]string{"--trigger"}, "--watch cannot be combined with --trigger"},
		{[]string{"--output", "json"}, "--watch cannot be combined with --output json"},
		{[]string{"--plan"}, "--watch cannot be combined with --plan"},
		{[]string{"--all", "--changed-since", "main"}, "--watch cannot be combined with --changed-since"},
		{[]string{"--dag-version", "v1"}, "--watch cannot be combined with --dag-version"},
		{[]string{"--skip-build"}, "--watch cannot be combined with --skip-build"},
		{[]string{"--force"}, "--watch cannot be combined with --force"},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			args := append([]string{"deploy", "--watch", "--server", "http://127.0.0.1:1"}, tc.args...)
			if !slices.Contains(tc.args, "--all") {
				args = append(args, dir)
			}
			// A refusal that does not come would start a watch: bound it.
			ctx, cancel := context.WithTimeout(testCtx(nil), 5*time.Second)
			defer cancel()
			root := NewRootCommand()
			root.SetOut(io.Discard)
			root.SetErr(io.Discard)
			root.SetArgs(args)
			err := root.ExecuteContext(ctx)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("deploy --watch %v: err = %v, want %q", tc.args, err, tc.want)
			}
		})
	}
}

// A label the watch deployed earlier is a deploy again when the server is no
// longer on it (a rollback, a teammate's deploy): the watch asks the server,
// and the deploy says what it says for any deploy. Undoing a save back to an
// older version names the rollback, never --dag-version, which --watch
// refuses.
func TestDeployWatchAsksTheServerAboutAKnownLabel(t *testing.T) {
	d := fastWatch(t, 30*time.Millisecond)
	dir, builder := watchProject(t)
	srv := &watchServer{fake: &deployServer{}}
	w := startWatch(t, d, srv, builder, dir)
	w.watching(t)
	labelA := labelOf(t, dir)
	original := readProjectFile(t, dir, "dag.py")

	save(t, dir, "dag.py", "# dag, B\n")
	labelB := labelOf(t, dir)
	w.waitFor(t, "the deploy of B", func() bool { return len(srv.registered()) == 2 })

	save(t, dir, "dag.py", original)
	w.waitFor(t, "the undo", func() bool { return strings.Contains(w.errb.String(), "dexaflow rollback etl --to "+labelA) })
	if strings.Contains(w.errb.String(), "--dag-version") {
		t.Errorf("under --watch the hint names --dag-version, which --watch refuses:\n%s", w.errb.String())
	}

	// The user rolls back to A elsewhere, then saves B again.
	srv.fake.mu.Lock()
	srv.fake.current = labelA
	srv.fake.mu.Unlock()
	save(t, dir, "dag.py", "# dag, B\n")
	w.waitFor(t, "the deploy of B again", func() bool {
		return strings.Contains(w.errb.String(), "dexaflow rollback etl --to "+labelB)
	})
	if err := w.stop(t); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(w.out.String(), "no deployable change: version "+labelB) {
		t.Errorf("B was skipped though the server is on A:\n%s", w.out.String())
	}
}

// --timeout applies to --follow, which --watch refuses: it is refused too,
// not accepted and ignored.
func TestDeployWatchRefusesTimeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(testCtx(nil), 5*time.Second)
	defer cancel()
	root := NewRootCommand()
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	root.SetArgs([]string{"deploy", "--watch", "--timeout", "5m", "--server", "http://127.0.0.1:1", versionProject(t, "")})
	if err := root.ExecuteContext(ctx); err == nil || !strings.Contains(err.Error(), "--timeout applies to --follow") {
		t.Errorf("deploy --watch --timeout 5m: err = %v, want the --timeout refusal", err)
	}
}
