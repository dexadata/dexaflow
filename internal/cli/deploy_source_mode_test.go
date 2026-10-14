package cli

import (
	"bytes"
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
// runtime image, as the other deploy defaults, and a target's own setting
// wins over it.
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
	t.Run("target wins", func(t *testing.T) {
		fake := newRecordingServer(`{"registry":"","platforms":[],"base_image":"","source_mode_image":"` + otherRuntimeRef + `"}`)
		srv := httptest.NewServer(fake)
		defer srv.Close()
		_, _, builder, _ := sourceModeWorkspace(t, srv.URL, runtimeRef, "")
		out, stderr, err := run(t, "deploy", "etl", "--token", "tok", "--yes", "--builder", builder, "--config", emptyConfig(t))
		if err != nil {
			t.Fatalf("deploy: %v\n%s\n%s", err, out, stderr)
		}
		if len(fake.registered) != 1 || fake.registered[0].Image != runtimeRef {
			t.Errorf("registered %+v, want the target's image %s over the cluster's", fake.registered, runtimeRef)
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
