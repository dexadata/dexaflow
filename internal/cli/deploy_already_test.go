package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/dexadata/dexaflow/internal/domain"
)

// deployServer fakes the control plane for a deploy and records what it was
// asked for. current is the DAG's current label; registered lists every label.
type deployServer struct {
	mu          sync.Mutex
	current     string
	registered  []string
	status      int // when set, the answer to both reads
	failFirst   int // reads answered 500 before the normal answer
	nullCurrent bool
	reads       int
	registers   int
	triggers    int
	// What the summary reads back, and what the deploy asked for.
	paused       bool
	schedule     string // timetable_summary; "" answers the server's no-schedule text
	catchup      bool
	startDate    string // start_date; "" answers null
	dagCreated   bool   // dag_created of the register answer
	ignorePaused bool   // an older server: no is_paused query, no is_paused in the answer
	failTrigger  bool   // answer the trigger with 500
	patches      []string
	// regQueries is the is_paused part of each register's query, regBuilds
	// its build_* parameters (#1702).
	regQueries []string
	regBuilds  []url.Values
	// The register dry run (#1705): probes counts the empty-body probes of
	// the preflight, dryRuns the dry runs of a compiled spec. noDryRun is a
	// server that predates the dry run route and answers it 404;
	// probeStatus and dryRunStatus, when set, refuse the probe or the dry
	// run with that status and refusal; dryRunWarnings are the warnings of
	// an accepted dry run. registeredImage, when set, is the image the
	// label is registered with: a dry run of another image is a 409, of
	// that one an unchanged register. dryRunImages records the image of
	// each dry run.
	probes         int
	dryRuns        int
	noDryRun       bool
	probeStatus    int
	viaIssuer      bool // the probe's refusal says deploys go through the trusted issuer (#1773)
	dryRunStatus   int
	refusal        string
	dryRunWarnings []string
	// registerWarnings are the warnings of the register's answer (#1703).
	registerWarnings []string
	dryRunQueries    []string
	registeredImage  string
	dryRunImages     []string
	// policy, when set, is the operator's execution allowlist a dry run
	// checks the spec against, refusing with its own message as the
	// register does.
	policy *domain.ExecutionPolicy
	// taskPods, when set, is the task_pods object of an accepted dry run
	// (#1889); unset, the answer has none, as an older server's.
	taskPods string
}

// detailsJSON is the details answer: the current label and the fields the
// deploy summary reads.
func (s *deployServer) detailsJSON(current string) string {
	summary := `"Never, external triggers only"`
	if s.schedule != "" {
		summary = `"` + s.schedule + `"`
	}
	start := "null"
	if s.startDate != "" {
		start = `"` + s.startDate + `"`
	}
	return fmt.Sprintf(`{"dag_id":"etl","bundle_version":%s,"is_paused":%v,"timetable_summary":%s,"catchup":%v,"start_date":%s}`,
		current, s.paused, summary, s.catchup, start)
}

// applyPausedQuery sets paused from a register's is_paused query, as the
// server does.
func (s *deployServer) applyPausedQuery(r *http.Request) {
	paused, build := url.Values{}, url.Values{}
	for k, v := range r.URL.Query() {
		if k == "is_paused" {
			paused[k] = v
		} else if strings.HasPrefix(k, "build_") {
			build[k] = v
		}
	}
	s.regQueries = append(s.regQueries, paused.Encode())
	s.regBuilds = append(s.regBuilds, build)
	if v := r.URL.Query().Get("is_paused"); v != "" && !s.ignorePaused {
		s.paused = v == "true"
	}
}

func (s *deployServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case r.Method == http.MethodGet && (strings.HasSuffix(r.URL.Path, "/details") || strings.HasSuffix(r.URL.Path, "/dagVersions")):
		s.serveRead(w, r)
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/versions"):
		s.serveRegister(w, r)
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/versions/dry_run") && !s.noDryRun:
		body, _ := io.ReadAll(r.Body)
		s.serveDryRun(w, r, body)
	case r.Method == http.MethodPatch && r.URL.Path == "/api/v2/dags/etl":
		b, _ := io.ReadAll(r.Body)
		s.patches = append(s.patches, string(b))
		s.paused = strings.Contains(string(b), "true")
		_, _ = fmt.Fprintf(w, `{"dag_id":"etl","is_paused":%v}`, s.paused)
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/dagRuns"):
		s.triggers++
		if s.failTrigger {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

// serveRead answers the details and dagVersions reads.
func (s *deployServer) serveRead(w http.ResponseWriter, r *http.Request) {
	s.reads++
	if s.failFirst > 0 {
		s.failFirst--
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	if s.status != 0 {
		w.WriteHeader(s.status)
		return
	}
	if strings.HasSuffix(r.URL.Path, "/details") {
		current := `"` + s.current + `"`
		if s.nullCurrent {
			current = "null"
		}
		_, _ = io.WriteString(w, s.detailsJSON(current))
		return
	}
	parts := make([]string, 0, len(s.registered))
	for _, l := range s.registered {
		parts = append(parts, `{"bundle_version":"`+l+`"}`)
	}
	_, _ = io.WriteString(w, `{"dag_versions":[`+strings.Join(parts, ",")+`]}`)
}

// serveRegister answers a register with 201 and the fields the deploy reads,
// and a dry run as the server does.
func (s *deployServer) serveRegister(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	if strings.TrimSpace(string(body)) == "{}" {
		// The dag_id of the path does not match the empty spec's.
		http.Error(w, `{"detail":"dag_id in path does not match spec"}`, http.StatusBadRequest)
		return
	}
	s.registers++
	s.applyPausedQuery(r)
	w.WriteHeader(http.StatusCreated)
	if s.ignorePaused {
		_, _ = io.WriteString(w, `{"dag_id":"etl","version":"v","spec_hash":"abc123","created":true}`)
		return
	}
	warnings, _ := json.Marshal(append([]string{}, s.registerWarnings...))
	_, _ = fmt.Fprintf(w, `{"dag_id":"etl","version":"v","spec_hash":"abc123","created":true,"dag_created":%v,"is_paused":%v,"warnings":%s}`,
		s.dagCreated, s.paused, warnings)
}

// serveDryRun answers a register dry run: the probe of the preflight, or a
// dry run of a compiled spec.
func (s *deployServer) serveDryRun(w http.ResponseWriter, r *http.Request, body []byte) {
	w.Header().Set("Dexaflow-Dry-Run", "true")
	refuse := func(status int) {
		w.WriteHeader(status)
		detail, _ := json.Marshal(s.refusal)
		_, _ = fmt.Fprintf(w, `{"title":"invalid dag spec","detail":%s}`, detail)
	}
	if strings.TrimSpace(string(body)) == "{}" {
		s.probes++
		if s.probeStatus != 0 {
			w.Header().Del("Dexaflow-Dry-Run")
			if s.viaIssuer {
				w.Header().Set("Dexaflow-Deploy-Via", "trusted-issuer")
			}
			refuse(s.probeStatus)
			return
		}
		http.Error(w, `{"detail":"dag_id in path does not match spec"}`, http.StatusBadRequest)
		return
	}
	s.dryRuns++
	s.dryRunQueries = append(s.dryRunQueries, r.URL.RawQuery)
	var spec domain.DAGSpec
	_ = json.Unmarshal(body, &spec)
	s.dryRunImages = append(s.dryRunImages, spec.Image)
	if s.dryRunStatus != 0 {
		refuse(s.dryRunStatus)
		return
	}
	if err := s.policy.CheckSpec(&spec); err != nil {
		s.refusal = err.Error()
		refuse(http.StatusBadRequest)
		return
	}
	created := true
	if s.registeredImage != "" {
		if spec.Image != s.registeredImage {
			s.refusal = "version already registered with other content"
			refuse(http.StatusConflict)
			return
		}
		created = false
	}
	warnings, _ := json.Marshal(append([]string{}, s.dryRunWarnings...))
	pods := ""
	if s.taskPods != "" {
		pods = `,"task_pods":` + s.taskPods
	}
	_, _ = fmt.Fprintf(w, `{"dag_id":"etl","version":"v","spec_hash":"abc","created":%v,"dag_created":%v,"is_paused":true,"dry_run":true,"warnings":%s%s}`,
		created, s.dagCreated && created, warnings, pods)
}

func TestCheckDeployed(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		fake     *deployServer
		state    deployedState
		wantErr  string
		wantNote bool
	}{
		{"current", &deployServer{current: "L", registered: []string{"L"}}, deployCurrent, "", false},
		{"registered, not current", &deployServer{current: "B", registered: []string{"B", "L"}}, deployStale, "", false},
		{"unknown", &deployServer{current: "B", registered: []string{"B"}}, deployUnknown, "", false},
		{"never registered", &deployServer{status: http.StatusNotFound}, deployUnknown, "", false},
		{"forbidden", &deployServer{status: http.StatusForbidden}, deployUnknown, "", true},
		{"unauthorized", &deployServer{status: http.StatusUnauthorized}, deployUnknown, "server returned 401", false},
		// A 5xx is retried once, then the deploy proceeds with a note.
		{"server error", &deployServer{status: http.StatusInternalServerError}, deployUnknown, "", true},
		{"server error cured by the retry", &deployServer{failFirst: 1, current: "L", registered: []string{"L"}}, deployCurrent, "", false},
		// Nothing current to compare with: build, never "stale (none)".
		{"no current version", &deployServer{nullCurrent: true, registered: []string{"L"}}, deployUnknown, "", false},
		{"bad request", &deployServer{status: http.StatusBadRequest}, deployUnknown, "--force", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := httptest.NewServer(c.fake)
			defer srv.Close()
			var warn strings.Builder
			state, _, err := checkDeployed(context.Background(), &warn, srv.URL, "tok", "etl", "L")
			if state != c.state {
				t.Errorf("state = %v, want %v", state, c.state)
			}
			if c.wantErr == "" && err != nil || c.wantErr != "" && (err == nil || !strings.Contains(err.Error(), c.wantErr)) {
				t.Errorf("err = %v, want %q", err, c.wantErr)
			}
			if (warn.Len() > 0) != c.wantNote {
				t.Errorf("note = %q, want note=%v", warn.String(), c.wantNote)
			}
		})
	}
	// A 2xx the CLI cannot read stops the deploy.
	garbled := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "not json")
	}))
	defer garbled.Close()
	if _, _, err := checkDeployed(context.Background(), io.Discard, garbled.URL, "", "etl", "L"); err == nil || !strings.Contains(err.Error(), "--force") {
		t.Errorf("undecodable 2xx: err = %v, want a stop naming --force", err)
	}
	// A network error proceeds, with a note.
	var warn strings.Builder
	if state, _, err := checkDeployed(context.Background(), &warn, "http://127.0.0.1:0", "", "etl", "L"); state != deployUnknown || err != nil || warn.Len() == 0 {
		t.Errorf("network error: state=%v err=%v note=%q", state, err, warn.String())
	}
}

func deployFixture(t *testing.T) (dir, builder, log string) {
	t.Helper()
	// A local registry with no image, so no test reaches a real registry.
	return deployFixtureAt(t, missingRegistry(t)+"/team")
}

// deployFixtureAt is deployFixture whose registry is registryURL.
func deployFixtureAt(t *testing.T, registryURL string) (dir, builder, log string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell stubs are POSIX-only")
	}
	dir = versionProject(t, "registry:\n  url: "+registryURL+"\n  image_name: etl\n")
	bin := t.TempDir()
	log = filepath.Join(bin, "builder.log")
	t.Setenv("LEOFLOW_PARSER_CMD", recordingParser(t, filepath.Join(bin, "parser.log")))
	return dir, stubBuilder(t, bin, log, ""), log
}

func builderRan(log string) bool {
	_, err := os.Stat(log)
	return err == nil
}

// TestDeploySkipsTheCurrentContentLabel: re-running a CI job on unchanged
// sources rebuilt the image to a new digest under the same label and failed
// with 409 after a full build and push.
func TestDeploySkipsTheCurrentContentLabel(t *testing.T) {
	dir, builder, log := deployFixture(t)
	label := labelOf(t, dir)
	fake := &deployServer{current: label, registered: []string{label}}
	srv := httptest.NewServer(fake)
	defer srv.Close()

	out, _, err := run(t, "deploy", dir, "--server", srv.URL, "--token", "x", "--yes", "--builder", builder, "--trigger")
	if err != nil {
		t.Fatalf("deploy: %v", err)
	}
	for _, want := range []string{"already deployed as " + label, "--dag-version", "--force"} {
		if !strings.Contains(out, want) {
			t.Errorf("output %q does not say %q", out, want)
		}
	}
	if builderRan(log) || fake.registers != 0 {
		t.Errorf("built=%v registers=%d for the current label, want neither", builderRan(log), fake.registers)
	}
	if fake.triggers != 1 {
		t.Errorf("--trigger ran %d time(s), want once", fake.triggers)
	}
}

// TestDeployRefusesAnOlderContentLabel: deploy A, deploy B, revert to A. The
// check used to read "registered" as "current", print "already deployed" and
// exit 0 while B stayed current.
func TestDeployRefusesAnOlderContentLabel(t *testing.T) {
	dir, builder, log := deployFixture(t)
	label := labelOf(t, dir)
	fake := &deployServer{current: "LB", registered: []string{"LB", label}}
	srv := httptest.NewServer(fake)
	defer srv.Close()

	_, _, err := run(t, "deploy", dir, "--server", srv.URL, "--token", "x", "--yes", "--builder", builder, "--trigger")
	if err == nil {
		t.Fatal("deploying content of an older version must fail")
	}
	dagID := strings.Fields(strings.SplitN(err.Error(), " content matches", 2)[0])[0]
	if strings.Contains(err.Error(), "not supported") {
		t.Errorf("error %q still says rollback is not supported", err)
	}
	for _, want := range []string{label, "not the current version (LB)", "dexaflow rollback " + dagID + " --to " + label, "--dag-version"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not say %q", err, want)
		}
	}
	if builderRan(log) || fake.registers != 0 || fake.triggers != 0 {
		t.Errorf("built=%v registers=%d triggers=%d, want nothing", builderRan(log), fake.registers, fake.triggers)
	}
}

func TestDeployStopsOnAnUnauthorizedCheck(t *testing.T) {
	dir, builder, log := deployFixture(t)
	srv := httptest.NewServer(&deployServer{status: http.StatusUnauthorized})
	defer srv.Close()
	_, _, err := run(t, "deploy", dir, "--server", srv.URL, "--token", "x", "--yes", "--builder", builder)
	// A refused token is fixed by signing in, not by skipping the check.
	if err == nil || !strings.Contains(err.Error(), "401") || strings.Contains(err.Error(), "--force") {
		t.Fatalf("err = %v, want the 401 without a --force suggestion", err)
	}
	if builderRan(log) {
		t.Error("built after the control plane refused the token")
	}
}

func TestDeployBuildsWhenTheCheckDoesNotApply(t *testing.T) {
	cases := []struct {
		name  string
		args  []string
		fake  *deployServer
		reads bool
	}{
		// An explicit label is the caller's: never second-guessed.
		{"explicit --dag-version", []string{"--dag-version", "mine"}, &deployServer{}, false},
		{"--force", []string{"--force"}, &deployServer{}, false},
		{"unknown label", nil, &deployServer{current: "LB", registered: []string{"LB"}}, true},
		{"forbidden check", nil, &deployServer{status: http.StatusForbidden}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir, builder, log := deployFixture(t)
			if c.name == "--force" {
				l := labelOf(t, dir)
				c.fake = &deployServer{current: l, registered: []string{l}}
			}
			srv := httptest.NewServer(c.fake)
			defer srv.Close()
			args := append([]string{"deploy", dir, "--server", srv.URL, "--token", "x", "--yes", "--builder", builder}, c.args...)
			_, _, _ = run(t, args...) // the stub builder cannot report a digest; only the build matters here
			if (c.fake.reads > 0) != c.reads {
				t.Errorf("control plane read %d time(s), want reads=%v", c.fake.reads, c.reads)
			}
			if !builderRan(log) {
				t.Error("the builder never ran")
			}
		})
	}
}

func TestDeployTakesADockerfileOutsideTheProject(t *testing.T) {
	dir, builder, log := deployFixture(t)
	shared := filepath.Join(filepath.Dir(dir), "Dockerfile.shared")
	if err := os.WriteFile(shared, []byte("FROM scratch\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(&deployServer{status: http.StatusNotFound})
	defer srv.Close()
	_, _, err := run(t, "deploy", dir, "--server", srv.URL, "--token", "x", "--yes", "--builder", builder, "--dockerfile", "../Dockerfile.shared")
	if err != nil && strings.Contains(err.Error(), "outside the project") {
		t.Fatalf("deploy refused a --dockerfile outside the project: %v", err)
	}
	if !builderRan(log) {
		t.Errorf("the builder never ran (err %v)", err)
	}
}

func TestRegisterDeployedDAGWarnsWhenNothingNewWasRegistered(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"dag_id":"etl","version":"v1","created":false}`)
	}))
	defer srv.Close()
	out := filepath.Join(t.TempDir(), "dag.json")
	if err := os.WriteFile(out, []byte(pushSpec), 0o600); err != nil {
		t.Fatal(err)
	}
	var sb strings.Builder
	if _, err := registerDeployedDAG(context.Background(), &sb, registerRequest{serverURL: srv.URL, token: "tok", output: out, digestRef: "r@sha256:x", version: "v1", origin: labelDefault}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sb.String(), "already registered as version v1") || !strings.Contains(sb.String(), "may not be the current version") || strings.Contains(sb.String(), "Deployed") {
		t.Errorf("output %q, want the already-registered warning and no Deployed line", sb.String())
	}
}
