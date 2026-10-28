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
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/config"
	"github.com/dexadata/dexaflow/internal/domain"
)

// Source mode deploys (#1772, ADR 0067): a target, or the cluster, names the
// runtime image, and `dexaflow deploy` registers the DAG's dag.py on it with
// no build and no push.

// runtimeRef is a runtime image pinned by a full digest, as source mode
// requires.
const runtimeRef = "ghcr.io/dexadata/dexaflow-runtime:0.5.3@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// otherRuntimeRef is another pinned runtime image, for precedence tests.
const otherRuntimeRef = "ghcr.io/acme/runtime:1@sha256:abcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcd"

// recordingServer is a deploy fake that records every request (its
// User-Agent and X-Dexaflow-CI headers) and every registered spec, and can
// answer the register or an accepted dry run with a body of the test's own.
type recordingServer struct {
	*clusterServer

	mu         sync.Mutex
	requests   int
	agents     []string
	ciHeaders  []string
	registered []domain.DAGSpec
	// registerBody, when set, is the answer of a register, with
	// registerStatus (201 when unset).
	registerBody   string
	registerStatus int
	// dryRunBody, when set, is the answer of the dry run of a compiled spec.
	dryRunBody string
	// track makes each registered label the DAG's current version, as the
	// server does, so a redeploy of the same content is already current.
	track bool
}

// newTrackingServer is a recordingServer whose registers become the DAG's
// current version.
func newTrackingServer(clusterBody string) *recordingServer {
	s := newRecordingServer(clusterBody)
	s.deployServer.status = 0
	s.nullCurrent = true
	s.track = true
	return s
}

func newRecordingServer(clusterBody string) *recordingServer {
	return &recordingServer{clusterServer: &clusterServer{deployServer: &deployServer{status: http.StatusNotFound}, body: clusterBody}}
}

func (s *recordingServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	r.Body = io.NopCloser(bytes.NewReader(body))
	s.mu.Lock()
	s.requests++
	s.agents = append(s.agents, r.UserAgent())
	s.ciHeaders = append(s.ciHeaders, r.Header.Get("X-Dexaflow-CI"))
	isSpec := r.Method == http.MethodPost && strings.TrimSpace(string(body)) != "{}"
	register := isSpec && strings.HasSuffix(r.URL.Path, "/versions")
	if register {
		var spec domain.DAGSpec
		_ = json.Unmarshal(body, &spec)
		s.registered = append(s.registered, spec)
	}
	registerBody, dryRunBody, registerStatus := s.registerBody, s.dryRunBody, s.registerStatus
	s.mu.Unlock()
	if registerStatus == 0 {
		registerStatus = http.StatusCreated
	}
	switch {
	case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/dagRuns"):
		// The last run status reads: none yet.
		_, _ = io.WriteString(w, `{"dag_runs":[],"total_entries":0}`)
		return
	case register && registerBody != "":
		w.WriteHeader(registerStatus)
		_, _ = io.WriteString(w, registerBody)
		return
	case isSpec && strings.HasSuffix(r.URL.Path, "/versions/dry_run") && dryRunBody != "":
		w.Header().Set("Dexaflow-Dry-Run", "true")
		_, _ = io.WriteString(w, dryRunBody)
		return
	}
	s.clusterServer.ServeHTTP(w, r)
	if register && s.track {
		s.mu.Lock()
		label := s.registered[len(s.registered)-1].DagVersion
		s.mu.Unlock()
		ds := s.deployServer
		ds.mu.Lock()
		ds.current, ds.nullCurrent = label, false
		ds.registered = append(ds.registered, label)
		ds.mu.Unlock()
	}
}

// sourceModeWorkspace writes a workspace whose default target dev points at
// serverURL with source_mode_image set to image ("" for none), and a dag.py
// project etl under dags/etl with extraYAML in its dexaflow.yaml. It changes
// into the workspace root, with a recording parser, and returns the root,
// the project directory, a stub builder and the builder's log.
func sourceModeWorkspace(t *testing.T, serverURL, image, extraYAML string) (root, project, builder, log string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell stubs are POSIX-only")
	}
	isolateCLIEnv(t)
	root = t.TempDir()
	target := "targets:\n  dev:\n    server: " + serverURL + "\n"
	if image != "" {
		target += "    source_mode_image: " + image + "\n"
	}
	writeTree(t, filepath.Join(root, workspaceFileName), target+"default_target: dev\n")
	project = filepath.Join(root, "dags", "etl")
	writeProjectFile(t, project, "dexaflow.yaml", "dag_id: etl\n"+extraYAML)
	writeProjectFile(t, project, "dag.py", "from dexaflow import dag\n# the etl DAG\n")
	bin := t.TempDir()
	log = filepath.Join(bin, "builder.log")
	t.Setenv("LEOFLOW_PARSER_CMD", recordingParser(t, filepath.Join(bin, "parser.log")))
	builder = stubBuilder(t, bin, log, "")
	t.Chdir(root)
	return root, project, builder, log
}

// TestDeployInSourceModeRegistersTheRuntimeImageAndTheSource is the
// acceptance of #1772: in a workspace whose target sets source_mode_image,
// `dexaflow deploy etl` registers a version whose image is that setting and
// whose source is the DAG's dag.py, with no build, no push and no registry.
func TestDeployInSourceModeRegistersTheRuntimeImageAndTheSource(t *testing.T) {
	fake := newRecordingServer("")
	srv := httptest.NewServer(fake)
	defer srv.Close()
	root, project, builder, log := sourceModeWorkspace(t, srv.URL, runtimeRef, "")

	out, stderr, err := run(t, "deploy", "etl", "--token", "tok", "--yes", "--builder", builder, "--config", emptyConfig(t))
	if err != nil {
		t.Fatalf("deploy: %v\n%s\n%s", err, out, stderr)
	}
	if builderRan(log) {
		t.Error("a source mode deploy ran the builder")
	}
	if len(fake.registered) != 1 {
		t.Fatalf("registered %d specs, want 1\n%s", len(fake.registered), out)
	}
	spec := fake.registered[0]
	if spec.Image != runtimeRef {
		t.Errorf("registered image = %q, want the source_mode_image byte for byte %q", spec.Image, runtimeRef)
	}
	src, _ := os.ReadFile(filepath.Join(project, "dag.py"))
	if spec.Source != string(src) {
		t.Errorf("registered source = %q, want the DAG's dag.py %q", spec.Source, src)
	}
	if got := fake.dryRunImages; len(got) != 1 || got[0] != runtimeRef {
		t.Errorf("dry run images = %q, want the runtime image itself", got)
	}
	from := `(source mode, from target "dev" in ` + filepath.Join(root, workspaceFileName) + ")"
	if line := lineWith(out, "image:      "); !strings.Contains(line, runtimeRef) || !strings.Contains(line, from) {
		t.Errorf("settings line %q, want the runtime image and %s:\n%s", line, from, out)
	}
	if strings.Contains(out, "registry:   ") {
		t.Errorf("a source mode deploy printed a registry line:\n%s", out)
	}
	if strings.Contains(stderr, "recording image") {
		t.Errorf("source mode printed compile's note about an image recorded without a build:\n%s", stderr)
	}
	if strings.Contains(out+stderr, "a push") {
		t.Errorf("source mode checked a push to the runtime image's registry:\n%s\n%s", out, stderr)
	}
}

// TestDeployInSourceModeFromAConfigFileTarget: a target of the user's config
// file may set source_mode_image too.
func TestDeployInSourceModeFromAConfigFileTarget(t *testing.T) {
	fake := newRecordingServer("")
	srv := httptest.NewServer(fake)
	defer srv.Close()
	_, project, builder, log := sourceModeWorkspace(t, "https://unused.example", "", "")
	t.Chdir(project)
	cfg := filepath.Join(t.TempDir(), "config.yaml")
	writeTree(t, cfg, "targets:\n  - name: lab\n    server: "+srv.URL+"\n    source_mode_image: "+runtimeRef+"\n")

	out, stderr, err := run(t, "deploy", "--target", "lab", "--token", "tok", "--yes", "--builder", builder, "--config", cfg)
	if err != nil {
		t.Fatalf("deploy: %v\n%s\n%s", err, out, stderr)
	}
	if builderRan(log) || len(fake.registered) != 1 || fake.registered[0].Image != runtimeRef {
		t.Errorf("builder ran=%v registered=%+v, want one source mode version on %s", builderRan(log), fake.registered, runtimeRef)
	}
	if line := lineWith(out, "image:      "); !strings.Contains(line, `target "lab" in `+cfg) {
		t.Errorf("settings line %q, want it from target lab in %s", line, cfg)
	}
}

// TestDeployInSourceModeFromTheClusterDefaults: a cluster may advertise its
// runtime image, as the other deploy defaults. A target that names another
// image is refused, naming both.
func TestDeployInSourceModeFromTheClusterDefaults(t *testing.T) {
	t.Run("cluster", func(t *testing.T) {
		fake := newRecordingServer(`{"registry":"","platforms":[],"base_image":"","source_mode_image":"` + runtimeRef + `"}`)
		srv := httptest.NewServer(fake)
		defer srv.Close()
		_, _, builder, log := sourceModeWorkspace(t, srv.URL, "", "")
		out, stderr, err := run(t, "deploy", "etl", "--token", "tok", "--yes", "--builder", builder, "--config", emptyConfig(t))
		if err != nil {
			t.Fatalf("deploy: %v\n%s\n%s", err, out, stderr)
		}
		if builderRan(log) || len(fake.registered) != 1 || fake.registered[0].Image != runtimeRef {
			t.Errorf("builder ran=%v registered=%+v, want one source mode version on the cluster's image", builderRan(log), fake.registered)
		}
		if line := lineWith(out, "image:      "); !strings.Contains(line, "the deploy defaults of "+srv.URL) {
			t.Errorf("settings line %q, want it from the cluster's deploy defaults", line)
		}
	})
	t.Run("a different local image is refused", func(t *testing.T) {
		fake := newRecordingServer(`{"registry":"","platforms":[],"base_image":"","source_mode_image":"` + otherRuntimeRef + `"}`)
		srv := httptest.NewServer(fake)
		defer srv.Close()
		root, _, builder, _ := sourceModeWorkspace(t, srv.URL, runtimeRef, "")
		_, _, err := run(t, "deploy", "etl", "--token", "tok", "--yes", "--builder", builder, "--config", emptyConfig(t))
		if err == nil {
			t.Fatal("deploy succeeded, want a refusal of two different runtime images")
		}
		for _, want := range []string{runtimeRef, otherRuntimeRef, `target "dev" in ` + filepath.Join(root, workspaceFileName), "the deploy defaults of " + srv.URL} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("refusal %q does not name %q", err, want)
			}
		}
		if len(fake.registered) != 0 {
			t.Errorf("registered %d versions, want none", len(fake.registered))
		}
	})
	t.Run("the same image agrees", func(t *testing.T) {
		fake := newRecordingServer(`{"registry":"","platforms":[],"base_image":"","source_mode_image":"` + runtimeRef + `"}`)
		srv := httptest.NewServer(fake)
		defer srv.Close()
		_, _, builder, _ := sourceModeWorkspace(t, srv.URL, runtimeRef, "")
		out, stderr, err := run(t, "deploy", "etl", "--token", "tok", "--yes", "--builder", builder, "--config", emptyConfig(t))
		if err != nil || len(fake.registered) != 1 || fake.registered[0].Image != runtimeRef {
			t.Errorf("deploy: %v, registered %+v, want one version on %s\n%s\n%s", err, fake.registered, runtimeRef, out, stderr)
		}
	})
}

// TestDeployInSourceModeRefusesWhatNeedsABuild: a DAG source mode cannot run
// as it is (packages to install, build steps, files besides dag.py) is
// refused before any network call, with the reason.
func TestDeployInSourceModeRefusesWhatNeedsABuild(t *testing.T) {
	cases := []struct {
		name  string
		yaml  string
		file  string // a file written beside dag.py
		wants string // what the refusal names
	}{
		{name: "requirements file", file: "requirements.txt", wants: "requirements.txt"},
		{name: "dependencies", yaml: "dependencies: [requests==2.32.3]\n", wants: "dependencies"},
		{name: "connectors", yaml: "connectors: [postgres]\n", wants: "connectors"},
		{name: "system packages", yaml: "system_packages: [curl]\n", wants: "system_packages"},
		{name: "include paths", yaml: "include_paths: [sql]\n", file: "sql/q.sql", wants: "include_paths"},
		{name: "base image", yaml: "base_image: python:3.12-slim\n", wants: "base_image"},
		{name: "dockerfile", file: "Dockerfile", wants: "Dockerfile"},
		{name: "sibling module", file: "helpers.py", wants: "helpers.py"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := newRecordingServer("")
			srv := httptest.NewServer(fake)
			defer srv.Close()
			_, project, builder, log := sourceModeWorkspace(t, srv.URL, runtimeRef, tc.yaml)
			if tc.file != "" {
				writeProjectFile(t, project, tc.file, "x\n")
			}
			out, _, err := run(t, "deploy", "etl", "--token", "tok", "--yes", "--builder", builder, "--config", emptyConfig(t))
			if err == nil || !strings.Contains(err.Error(), tc.wants) || !strings.Contains(err.Error(), "source mode") {
				t.Fatalf("deploy: err = %v, want a source mode refusal naming %s\n%s", err, tc.wants, out)
			}
			if fake.requests != 0 {
				t.Errorf("the refusal came after %d requests to the server, want none", fake.requests)
			}
			if builderRan(log) {
				t.Error("the builder ran")
			}
		})
	}
	t.Run("advertised by the cluster", func(t *testing.T) {
		fake := newRecordingServer(`{"registry":"","platforms":[],"base_image":"","source_mode_image":"` + runtimeRef + `"}`)
		srv := httptest.NewServer(fake)
		defer srv.Close()
		_, project, builder, _ := sourceModeWorkspace(t, srv.URL, "", "")
		writeProjectFile(t, project, "requirements.txt", "requests\n")
		_, _, err := run(t, "deploy", "etl", "--token", "tok", "--yes", "--builder", builder, "--config", emptyConfig(t))
		if err == nil || !strings.Contains(err.Error(), "requirements.txt") || !strings.Contains(err.Error(), "the deploy defaults of "+srv.URL) {
			t.Fatalf("deploy: err = %v, want a refusal naming requirements.txt and where source mode came from", err)
		}
		if fake.probes+fake.dryRuns+len(fake.registered) != 0 {
			t.Errorf("probes=%d dry runs=%d registers=%d, want none", fake.probes, fake.dryRuns, len(fake.registered))
		}
	})
}

// TestDeployInSourceModeRefusesAnOversizedSource: a dag.py over the 128 KiB
// the server accepts is refused before the dry run and the register.
func TestDeployInSourceModeRefusesAnOversizedSource(t *testing.T) {
	fake := newRecordingServer("")
	srv := httptest.NewServer(fake)
	defer srv.Close()
	_, project, builder, _ := sourceModeWorkspace(t, srv.URL, runtimeRef, "")
	writeProjectFile(t, project, "dag.py", strings.Repeat("#", domain.MaxSourceModeBytes+1))
	_, _, err := run(t, "deploy", "etl", "--token", "tok", "--yes", "--builder", builder, "--config", emptyConfig(t))
	if err == nil || !strings.Contains(err.Error(), "131072") {
		t.Fatalf("deploy: err = %v, want a refusal naming the 131072 byte cap", err)
	}
	if fake.dryRuns != 0 || len(fake.registered) != 0 {
		t.Errorf("dry runs=%d registers=%d, want none", fake.dryRuns, len(fake.registered))
	}
}

// TestDeployInSourceModeRefusesAnUnpinnedImage: the server only runs a
// runtime image pinned by a full digest, so a tag can never match it.
func TestDeployInSourceModeRefusesAnUnpinnedImage(t *testing.T) {
	fake := newRecordingServer("")
	srv := httptest.NewServer(fake)
	defer srv.Close()
	_, _, builder, _ := sourceModeWorkspace(t, srv.URL, "ghcr.io/dexadata/dexaflow-runtime:0.5.3", "")
	_, _, err := run(t, "deploy", "etl", "--token", "tok", "--yes", "--builder", builder, "--config", emptyConfig(t))
	if err == nil || !strings.Contains(err.Error(), "source_mode_image") || !strings.Contains(err.Error(), "digest") || !strings.Contains(err.Error(), `target "dev"`) {
		t.Fatalf("deploy: err = %v, want a refusal of the unpinned source_mode_image naming its target", err)
	}
	if fake.requests != 0 {
		t.Errorf("the refusal came after %d requests, want none", fake.requests)
	}
}

// TestDeployPlanInSourceMode: --plan says nothing is built or pushed, on the
// runtime image, and changes nothing.
func TestDeployPlanInSourceMode(t *testing.T) {
	fake := newRecordingServer("")
	srv := httptest.NewServer(fake)
	defer srv.Close()
	_, _, builder, log := sourceModeWorkspace(t, srv.URL, runtimeRef, "")
	out, stderr, err := run(t, "deploy", "etl", "--plan", "--token", "tok", "--builder", builder, "--config", emptyConfig(t))
	if err != nil {
		t.Fatalf("deploy --plan: %v\n%s\n%s", err, out, stderr)
	}
	for _, want := range []string{
		"  image       " + runtimeRef,
		"  build       no (source mode)",
		"  push        no (source mode)",
		"  register    yes",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("plan does not say %q:\n%s", want, out)
		}
	}
	if strings.Contains(lineWith(out, "  why "), "build") {
		t.Errorf("the why line speaks of a build in source mode: %q", lineWith(out, "  why "))
	}
	if builderRan(log) || len(fake.registered) != 0 {
		t.Errorf("--plan built (%v) or registered (%d)", builderRan(log), len(fake.registered))
	}
	forced, _, err := run(t, "deploy", "etl", "--plan", "--force", "--token", "tok", "--builder", builder, "--config", emptyConfig(t))
	if err != nil {
		t.Fatalf("deploy --plan --force: %v", err)
	}
	if why := lineWith(forced, "  why "); !strings.Contains(why, "--force: registered without checking") {
		t.Errorf("--force why line = %q, want it to say the deploy only registers", why)
	}

	jsonOut, _, err := run(t, "deploy", "etl", "--plan", "--output", "json", "--token", "tok", "--builder", builder, "--config", emptyConfig(t))
	if err != nil {
		t.Fatalf("deploy --plan --output json: %v", err)
	}
	var report struct {
		DAGs []struct {
			Image string `json:"image"`
			Plan  struct {
				Build      bool `json:"build"`
				Push       bool `json:"push"`
				SourceMode bool `json:"source_mode"`
			} `json:"plan"`
		} `json:"dags"`
	}
	if jerr := json.Unmarshal([]byte(jsonOut), &report); jerr != nil || len(report.DAGs) != 1 {
		t.Fatalf("report %s: %v", jsonOut, jerr)
	}
	if p := report.DAGs[0]; p.Image != runtimeRef || p.Plan.Build || p.Plan.Push || !p.Plan.SourceMode {
		t.Errorf("plan = %+v, want the runtime image, no build, no push, source_mode true", p)
	}
}

// TestCheckSourceModeSpec: a compiled dag.json on another image than the
// runtime image, or with no source, is refused.
func TestCheckSourceModeSpec(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	write := func(image, source string) string {
		p := filepath.Join(dir, "dag.json")
		data, _ := json.Marshal(map[string]any{"dag_id": "etl", "image": image, "source": source})
		writeTree(t, p, string(data))
		return p
	}
	if err := checkSourceModeSpec(write(runtimeRef, "x = 1\n"), runtimeRef); err != nil {
		t.Errorf("a spec on the runtime image with a source: %v, want nil", err)
	}
	if err := checkSourceModeSpec(write(otherRuntimeRef, "x = 1\n"), runtimeRef); err == nil || !strings.Contains(err.Error(), "not the runtime image") {
		t.Errorf("a spec on another image: %v, want a refusal", err)
	}
	if err := checkSourceModeSpec(write(runtimeRef, ""), runtimeRef); err == nil || !strings.Contains(err.Error(), "no source") {
		t.Errorf("a spec with no source: %v, want a refusal", err)
	}
}

// TestTargetAddSourceModeImage: `target add --source-mode-image` writes the
// setting to the workspace file, or with --global to the config file, and
// refuses an image not pinned by a full digest.
func TestTargetAddSourceModeImage(t *testing.T) {
	isolateCLIEnv(t)
	root := t.TempDir()
	writeTree(t, filepath.Join(root, workspaceFileName), workspaceFileTemplate)
	t.Chdir(root)
	cfg := emptyConfig(t)
	if _, _, err := run(t, "target", "add", "dev", "https://dev.example", "--source-mode-image", runtimeRef, "--config", cfg); err != nil {
		t.Fatalf("target add: %v", err)
	}
	ws, err := findWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}
	if got := ws.Spec.Targets["dev"]; got == nil || got.SourceModeImage != runtimeRef {
		t.Errorf("dev = %+v, want source_mode_image %s", got, runtimeRef)
	}
	if _, _, gerr := run(t, "target", "add", "lab", "https://lab.example", "--global", "--source-mode-image", runtimeRef, "--config", cfg); gerr != nil {
		t.Fatalf("target add --global: %v", gerr)
	}
	store, err := config.ReadUserStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := store.Target("lab"); !ok || got.SourceModeImage != runtimeRef {
		t.Errorf("lab = %+v, want source_mode_image %s", got, runtimeRef)
	}
	// A tag alone never matches the server's pinned runtime image.
	if _, _, err := run(t, "target", "add", "tagged", "https://tagged.example", "--source-mode-image", "ghcr.io/acme/runtime:1", "--config", cfg); err == nil || !strings.Contains(err.Error(), "digest") {
		t.Errorf("target add with an unpinned image: err = %v, want a refusal naming the digest", err)
	}
}

// specs returns the registered specs, safe to read while a watch deploys.
func (s *recordingServer) specs() []domain.DAGSpec {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]domain.DAGSpec(nil), s.registered...)
}

// writeTargets replaces the workspace file of a sourceModeWorkspace with
// target dev at serverURL holding the given target lines.
func writeTargets(t *testing.T, root, serverURL, lines string) {
	t.Helper()
	writeTree(t, filepath.Join(root, workspaceFileName), "targets:\n  dev:\n    server: "+serverURL+"\n"+lines+"default_target: dev\n")
}

// TestDeployInSourceModeLabelsTheRuntimeImage: the content label of a
// source mode deploy hashes the runtime image, so a new runtime image is a
// new label and a register on it, where the same content on the same image
// is already current; `dexaflow status` computes that same label.
func TestDeployInSourceModeLabelsTheRuntimeImage(t *testing.T) {
	fake := newTrackingServer("")
	srv := httptest.NewServer(fake)
	defer srv.Close()
	root, _, builder, _ := sourceModeWorkspace(t, srv.URL, runtimeRef, "")
	deploy := func() string {
		t.Helper()
		out, stderr, err := run(t, "deploy", "etl", "--token", "tok", "--yes", "--builder", builder, "--config", emptyConfig(t))
		if err != nil {
			t.Fatalf("deploy: %v\n%s\n%s", err, out, stderr)
		}
		return out
	}
	deploy()
	if len(fake.specs()) != 1 {
		t.Fatalf("registered %d versions, want 1", len(fake.specs()))
	}
	again := deploy()
	if n := len(fake.specs()); n != 1 || !strings.Contains(again, "already") {
		t.Fatalf("a redeploy of the same content on the same image registered (%d versions), want it already current:\n%s", n, again)
	}
	writeTargets(t, root, srv.URL, "    source_mode_image: "+otherRuntimeRef+"\n")
	deploy()
	specs := fake.specs()
	if len(specs) != 2 {
		t.Fatalf("after a new runtime image, registered %d versions, want 2", len(specs))
	}
	if specs[1].Image != otherRuntimeRef || specs[1].DagVersion == specs[0].DagVersion {
		t.Errorf("second register = %s on %s, want a new label on %s (first %s)", specs[1].DagVersion, specs[1].Image, otherRuntimeRef, specs[0].DagVersion)
	}

	statusOut, _, _ := run(t, "status", "--output", "json", "--token", "tok", "--config", emptyConfig(t))
	var report statusJSON
	if err := json.Unmarshal([]byte(statusOut), &report); err != nil || len(report.Targets) != 1 || len(report.Targets[0].Dags) != 1 {
		t.Fatalf("status report %s: %v", statusOut, err)
	}
	if got := report.Targets[0].Dags[0].LocalVersion; got != specs[1].DagVersion {
		t.Errorf("status local_version = %q, want the label deploy registered %q", got, specs[1].DagVersion)
	}
}

// TestSourceModeLabelKeepsTheBuildLabel: the runtime image is hashed only in
// source mode, so no label of a build moves.
func TestSourceModeLabelKeepsTheBuildLabel(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeProjectFile(t, dir, "dexaflow.yaml", "dag_id: etl\n")
	writeProjectFile(t, dir, "dag.py", "x = 1\n")
	cfg, err := loadProjectConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	build, err := contentHash(dir, cfg, "Dockerfile", "")
	if err != nil {
		t.Fatal(err)
	}
	same, err := contentHashOn(dir, cfg, "Dockerfile", "", "")
	if err != nil || same != build {
		t.Errorf("contentHashOn with no runtime image = %s (%v), want the build hash %s", same, err, build)
	}
	a, _ := contentHashOn(dir, cfg, "Dockerfile", "", runtimeRef)
	b, _ := contentHashOn(dir, cfg, "Dockerfile", "", otherRuntimeRef)
	if a == build || a == b {
		t.Errorf("source mode hashes = %s and %s (build %s), want each runtime image its own hash", a, b, build)
	}
}

// TestClusterSourceModeYieldsToLocalChoices: the runtime image a cluster
// advertises applies only when no local source chose a registry or a runtime
// image (#1704): a local registry, --no-source-mode and a target's
// source_mode_image: none each keep the deploy a build.
func TestClusterSourceModeYieldsToLocalChoices(t *testing.T) {
	clusterBody := `{"registry":"","platforms":[],"base_image":"","source_mode_image":"` + runtimeRef + `"}`
	plan := func(t *testing.T, body, targetLines string, args ...string) (string, error) {
		t.Helper()
		fake := newRecordingServer(body)
		srv := httptest.NewServer(fake)
		t.Cleanup(srv.Close)
		root, _, builder, _ := sourceModeWorkspace(t, srv.URL, "", "")
		writeTargets(t, root, srv.URL, targetLines)
		out, stderr, err := run(t, append([]string{"deploy", "etl", "--plan", "--token", "tok", "--builder", builder, "--config", emptyConfig(t)}, args...)...)
		return out + stderr, err
	}
	buildMode := func(t *testing.T, out string, err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("deploy --plan: %v\n%s", err, out)
		}
		if strings.Contains(out, "source mode") || !strings.Contains(out, "registry:   registry.example.com/team") {
			t.Errorf("want a build to the local registry, not source mode:\n%s", out)
		}
	}
	t.Run("a target registry", func(t *testing.T) {
		out, err := plan(t, clusterBody, "    registry: registry.example.com/team\n")
		buildMode(t, out, err)
	})
	t.Run("--no-source-mode", func(t *testing.T) {
		out, err := plan(t, clusterBody, "    source_mode_image: "+runtimeRef+"\n", "--no-source-mode", "--registry", "registry.example.com/team")
		buildMode(t, out, err)
	})
	t.Run("source_mode_image none", func(t *testing.T) {
		withRegistry := `{"registry":"registry.example.com/team","platforms":[],"base_image":"","source_mode_image":"` + runtimeRef + `"}`
		out, err := plan(t, withRegistry, "    source_mode_image: none\n")
		buildMode(t, out, err)
	})
	t.Run("the cluster's registry and image", func(t *testing.T) {
		withRegistry := `{"registry":"registry.example.com/team","platforms":[],"base_image":"","source_mode_image":"` + runtimeRef + `"}`
		out, err := plan(t, withRegistry, "")
		if err != nil || !strings.Contains(out, "source mode") {
			t.Errorf("with both from the cluster, want source mode: %v\n%s", err, out)
		}
	})
	t.Run("status takes --no-source-mode", func(t *testing.T) {
		fake := newRecordingServer(clusterBody)
		srv := httptest.NewServer(fake)
		t.Cleanup(srv.Close)
		sourceModeWorkspace(t, srv.URL, runtimeRef, "")
		if _, _, err := run(t, "status", "--no-source-mode", "--token", "tok", "--config", emptyConfig(t)); err != nil && strings.Contains(err.Error(), "unknown flag") {
			t.Errorf("status --no-source-mode: %v", err)
		}
	})
}

// TestTargetAddSourceModeImageNone: none is the value that clears a runtime
// image another layer sets, so target add takes it.
func TestTargetAddSourceModeImageNone(t *testing.T) {
	isolateCLIEnv(t)
	root := t.TempDir()
	writeTree(t, filepath.Join(root, workspaceFileName), workspaceFileTemplate)
	t.Chdir(root)
	if _, _, err := run(t, "target", "add", "dev", "https://dev.example", "--source-mode-image", "none", "--config", emptyConfig(t)); err != nil {
		t.Fatalf("target add --source-mode-image none: %v", err)
	}
}

// TestDeployInSourceModeRefusesAMalformedImage: source_mode_image follows the
// image reference grammar, and the refusal prints it without the control
// characters it may carry, from a target or from the cluster.
func TestDeployInSourceModeRefusesAMalformedImage(t *testing.T) {
	const digest = "@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	for name, image := range map[string]string{
		"capitals": "ghcr.io/Acme/Runtime" + digest,
		"escape":   "ghcr.io/acme/ru\x1b[31mn" + digest,
		"space":    "ghcr.io/acme/ru n" + digest,
	} {
		t.Run(name, func(t *testing.T) {
			fake := newRecordingServer("")
			srv := httptest.NewServer(fake)
			defer srv.Close()
			root, _, builder, _ := sourceModeWorkspace(t, srv.URL, "", "")
			quoted, _ := json.Marshal(image)
			writeTargets(t, root, srv.URL, "    source_mode_image: "+string(quoted)+"\n")
			_, _, err := run(t, "deploy", "etl", "--token", "tok", "--yes", "--builder", builder, "--config", emptyConfig(t))
			if err == nil || !strings.Contains(err.Error(), "source_mode_image") {
				t.Fatalf("deploy: err = %v, want a refusal of source_mode_image", err)
			}
			if strings.ContainsRune(err.Error(), '\x1b') {
				t.Errorf("the refusal carries an escape character: %q", err)
			}
			if fake.requests != 0 {
				t.Errorf("the refusal came after %d requests, want none: a local image is checked first", fake.requests)
			}
		})
	}
	t.Run("from the cluster", func(t *testing.T) {
		fake := newRecordingServer(`{"registry":"","platforms":[],"base_image":"","source_mode_image":"ghcr.io/acme/ru\u001b[31mn` + digest + `"}`)
		srv := httptest.NewServer(fake)
		defer srv.Close()
		_, _, builder, _ := sourceModeWorkspace(t, srv.URL, "", "")
		_, _, err := run(t, "deploy", "etl", "--token", "tok", "--yes", "--builder", builder, "--config", emptyConfig(t))
		if err == nil || strings.ContainsRune(err.Error(), '\x1b') || len(fake.registered) != 0 {
			t.Errorf("deploy: err = %q, registered %d, want a refusal without the escape character", err, len(fake.registered))
		}
	})
}

// TestSourceModeScan: what source mode refuses and what it only warns about,
// walking the project as the label does (exclude_paths honored).
func TestSourceModeScan(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		yaml   string
		files  map[string]string
		refuse string // what the refusal names; "" for none
		warn   string // what the warning names; "" for none
		// noTopDag leaves out the top-level dag.py, for a dag_source elsewhere.
		noTopDag bool
	}{
		{name: "package", files: map[string]string{"lib/util.py": "x = 1\n"}, refuse: "lib/ (Python packages"},
		{name: "module", files: map[string]string{"helpers.py": "x = 1\n"}, refuse: "helpers.py beside the DAG source"},
		{name: "test files", files: map[string]string{"test_dag.py": "x\n", "dag_test.py": "x\n", "conftest.py": "x\n", "tests/test_a.py": "x\n", "__init__.py": ""}},
		{name: "a non-empty __init__.py", files: map[string]string{"__init__.py": "import os\n"}, refuse: "__init__.py"},
		{name: "excluded", yaml: "exclude_paths: [scratch]\n", files: map[string]string{"scratch/x.py": "x\n"}},
		{name: "other files", files: map[string]string{"README.md": "x\n", "data/seed.csv": "a,b\n"}, warn: "data/seed.csv"},
		{name: "build context", yaml: "build:\n  context: sub\n", refuse: "build.context"},
		{name: "include of the dag source", yaml: "dag_source: src/dag.py\ninclude_paths: [src/dag.py]\n", files: map[string]string{"src/dag.py": "x = 1\n"}, noTopDag: true},
		{name: "include of another dag.py", yaml: "include_paths: [lib/dag.py]\n", files: map[string]string{"lib/dag.py": "x = 1\n"}, refuse: "include_paths"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeProjectFile(t, dir, "dexaflow.yaml", "dag_id: etl\n"+tc.yaml)
			if !tc.noTopDag {
				writeProjectFile(t, dir, "dag.py", "x = 1\n")
			}
			for f, body := range tc.files {
				writeProjectFile(t, dir, f, body)
			}
			cfg, err := loadDeployProject(dir)
			if err != nil {
				t.Fatal(err)
			}
			scan := scanSourceMode(dir, cfg)
			needs := strings.Join(scan.needs, "\n")
			if tc.refuse == "" && needs != "" {
				t.Errorf("needs = %q, want none", needs)
			}
			if tc.refuse != "" && !strings.Contains(needs, tc.refuse) {
				t.Errorf("needs = %q, want it to name %s", needs, tc.refuse)
			}
			extras := strings.Join(scan.extras, "\n")
			if tc.warn == "" && extras != "" {
				t.Errorf("extras = %q, want none", extras)
			}
			if tc.warn != "" && !strings.Contains(extras, tc.warn) {
				t.Errorf("extras = %q, want it to name %s", extras, tc.warn)
			}
		})
	}
}

// TestDeployInSourceModeWarnsAboutOtherFiles: files a build would have
// copied, and the task will not see, are named on stderr; the deploy goes on.
func TestDeployInSourceModeWarnsAboutOtherFiles(t *testing.T) {
	fake := newRecordingServer("")
	srv := httptest.NewServer(fake)
	defer srv.Close()
	_, project, builder, _ := sourceModeWorkspace(t, srv.URL, runtimeRef, "")
	writeProjectFile(t, project, "data/seed.csv", "a,b\n")
	out, stderr, err := run(t, "deploy", "etl", "--token", "tok", "--yes", "--builder", builder, "--config", emptyConfig(t))
	if err != nil || len(fake.registered) != 1 {
		t.Fatalf("deploy: %v, %d registers\n%s\n%s", err, len(fake.registered), out, stderr)
	}
	if !strings.Contains(stderr, "warning: ") || !strings.Contains(stderr, "data/seed.csv") {
		t.Errorf("stderr does not warn about data/seed.csv:\n%s", stderr)
	}
}

// TestDeployInSourceModeBuildFlags: --dockerfile names a build source mode
// does not make, so it is refused; --registry, --platform, --rebuild and
// --builder change nothing in source mode, and a note says so.
func TestDeployInSourceModeBuildFlags(t *testing.T) {
	t.Run("--dockerfile", func(t *testing.T) {
		fake := newRecordingServer("")
		srv := httptest.NewServer(fake)
		defer srv.Close()
		_, _, builder, _ := sourceModeWorkspace(t, srv.URL, runtimeRef, "")
		_, _, err := run(t, "deploy", "etl", "--dockerfile", "Dockerfile.prod", "--token", "tok", "--yes", "--builder", builder, "--config", emptyConfig(t))
		if err == nil || !strings.Contains(err.Error(), "--dockerfile") || !strings.Contains(err.Error(), "source mode") {
			t.Fatalf("deploy --dockerfile: err = %v, want a source mode refusal naming --dockerfile", err)
		}
		if len(fake.registered) != 0 {
			t.Errorf("registered %d versions, want none", len(fake.registered))
		}
	})
	t.Run("notes", func(t *testing.T) {
		fake := newRecordingServer("")
		srv := httptest.NewServer(fake)
		defer srv.Close()
		_, _, builder, log := sourceModeWorkspace(t, srv.URL, runtimeRef, "")
		out, stderr, err := run(t, "deploy", "etl", "--registry", "registry.example.com/team", "--platform", "linux/arm64", "--rebuild",
			"--token", "tok", "--yes", "--builder", builder, "--config", emptyConfig(t))
		if err != nil || len(fake.registered) != 1 || fake.registered[0].Image != runtimeRef || builderRan(log) {
			t.Fatalf("deploy: %v, registered %+v, want one source mode version\n%s\n%s", err, fake.registered, out, stderr)
		}
		for _, flag := range []string{"--registry", "--platform", "--rebuild", "--builder"} {
			if l := lineWith(stderr, "note: "+flag); !strings.Contains(l, "source mode") {
				t.Errorf("no source mode note for %s:\n%s", flag, stderr)
			}
		}
	})
}

// TestDeployWatchInSourceMode: a watch in source mode registers each saved
// dag.py on the runtime image, with no build.
func TestDeployWatchInSourceMode(t *testing.T) {
	d := fastWatch(t, 20*time.Millisecond)
	fake := newTrackingServer("")
	srv := httptest.NewServer(fake)
	defer srv.Close()
	_, project, builder, log := sourceModeWorkspace(t, srv.URL, runtimeRef, "")
	ctx, cancel := context.WithCancel(testCtx(d))
	w := &watchRun{out: &syncBuffer{}, errb: &syncBuffer{}, cancel: cancel, done: make(chan error, 1)}
	root := NewRootCommand()
	root.SetOut(w.out)
	root.SetErr(w.errb)
	root.SetArgs([]string{"deploy", "etl", "--watch", "--token", "tok", "--yes", "--builder", builder, "--config", emptyConfig(t)})
	go func() { w.done <- root.ExecuteContext(ctx) }()
	t.Cleanup(func() {
		cancel()
		<-w.done
	})
	w.watching(t)
	changed := "from dexaflow import dag\n# the etl DAG, changed\n"
	writeProjectFile(t, project, "dag.py", changed)
	w.waitFor(t, "the redeploy", func() bool { return len(fake.specs()) == 2 })
	if err := w.stop(t); err != nil {
		t.Errorf("watch: %v", err)
	}
	specs := fake.specs()
	if specs[1].Image != runtimeRef || specs[1].Source != changed || builderRan(log) {
		t.Errorf("redeploy = image %s, source %q, builder ran %v; want the saved dag.py on %s with no build", specs[1].Image, specs[1].Source, builderRan(log), runtimeRef)
	}
}
