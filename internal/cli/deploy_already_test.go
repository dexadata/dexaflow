package cli

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
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
}

func (s *deployServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case r.Method == http.MethodGet && (strings.HasSuffix(r.URL.Path, "/details") || strings.HasSuffix(r.URL.Path, "/dagVersions")):
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
		if strings.HasSuffix(r.URL.Path, "/details") && s.nullCurrent {
			_, _ = io.WriteString(w, `{"dag_id":"etl","bundle_version":null}`)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/details") {
			_, _ = io.WriteString(w, `{"dag_id":"etl","bundle_version":"`+s.current+`"}`)
			return
		}
		parts := make([]string, 0, len(s.registered))
		for _, l := range s.registered {
			parts = append(parts, `{"bundle_version":"`+l+`"}`)
		}
		_, _ = io.WriteString(w, `{"dag_versions":[`+strings.Join(parts, ",")+`]}`)
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/versions"):
		s.registers++
		w.WriteHeader(http.StatusCreated)
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/dagRuns"):
		s.triggers++
		w.WriteHeader(http.StatusOK)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func TestCheckDeployed(t *testing.T) {
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
	if runtime.GOOS == "windows" {
		t.Skip("shell stubs are POSIX-only")
	}
	dir = versionProject(t, "registry:\n  url: reg.io/team\n  image_name: etl\n")
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
	for _, want := range []string{label, "not the current version (LB)", "#1702", "--dag-version"} {
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
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"dag_id":"etl","version":"v1","created":false}`)
	}))
	defer srv.Close()
	out := filepath.Join(t.TempDir(), "dag.json")
	if err := os.WriteFile(out, []byte(pushSpec), 0o600); err != nil {
		t.Fatal(err)
	}
	var sb strings.Builder
	if _, err := registerDeployedDAG(context.Background(), &sb, srv.URL, "tok", out, "r@sha256:x", "v1", labelDefault); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sb.String(), "already registered as version v1") || !strings.Contains(sb.String(), "may not be the current version") || strings.Contains(sb.String(), "Deployed") {
		t.Errorf("output %q, want the already-registered warning and no Deployed line", sb.String())
	}
}
