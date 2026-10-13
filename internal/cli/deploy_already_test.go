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

func TestVersionRegistered(t *testing.T) {
	var gotAuth, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotPath = r.Header.Get("Authorization"), r.URL.Path
		switch r.URL.Path {
		case "/api/v2/dags/etl/dagVersions":
			_, _ = io.WriteString(w, `{"dag_versions":[{"version_number":1,"bundle_version":"v1"},{"version_number":2,"bundle_version":null}],"total_entries":2}`)
		case "/api/v2/dags/denied/dagVersions":
			w.WriteHeader(http.StatusUnauthorized)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	ctx := context.Background()

	if ok, err := versionRegistered(ctx, srv.URL, "tok", "etl", "v1"); err != nil || !ok {
		t.Errorf("registered label: ok=%v err=%v, want true", ok, err)
	}
	if gotAuth != "Bearer tok" || gotPath != "/api/v2/dags/etl/dagVersions" {
		t.Errorf("request auth=%q path=%q", gotAuth, gotPath)
	}
	if ok, err := versionRegistered(ctx, srv.URL, "tok", "etl", "v2"); err != nil || ok {
		t.Errorf("unknown label: ok=%v err=%v, want false", ok, err)
	}
	if ok, err := versionRegistered(ctx, srv.URL, "tok", "nope", "v1"); err != nil || ok {
		t.Errorf("unknown DAG (404): ok=%v err=%v, want false and no error", ok, err)
	}
	if _, err := versionRegistered(ctx, srv.URL, "tok", "denied", "v1"); err == nil {
		t.Error("a 401 must be an error, so deploy proceeds as before")
	}
}

// deployServer fakes the control plane for a deploy and records what it was
// asked for.
type deployServer struct {
	mu        sync.Mutex
	label     string
	failList  bool
	listed    int
	registers int
	triggers  int
}

func (s *deployServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/dagVersions"):
		s.listed++
		if s.failList {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = io.WriteString(w, `{"dag_versions":[{"bundle_version":"`+s.label+`"}],"total_entries":1}`)
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

// TestDeploySkipsAnAlreadyDeployedContentLabel: re-running a CI job on
// unchanged sources rebuilt the image to a new digest under the same label and
// failed with 409 after a full build and push.
func TestDeploySkipsAnAlreadyDeployedContentLabel(t *testing.T) {
	dir, builder, log := deployFixture(t)
	fake := &deployServer{label: labelOf(t, dir)}
	srv := httptest.NewServer(fake)
	defer srv.Close()

	out, _, err := run(t, "deploy", dir, "--server", srv.URL, "--token", "x", "--yes", "--builder", builder, "--trigger")
	if err != nil {
		t.Fatalf("deploy: %v", err)
	}
	if !strings.Contains(out, "already deployed as "+fake.label) {
		t.Errorf("output %q, want it to say the label is already deployed", out)
	}
	if _, serr := os.Stat(log); serr == nil {
		t.Errorf("the builder ran for an already deployed label: %q", nonEmptyLines(t, log))
	}
	if fake.registers != 0 {
		t.Errorf("registered %d time(s), want none", fake.registers)
	}
	if fake.triggers != 1 {
		t.Errorf("--trigger ran %d time(s), want once", fake.triggers)
	}
}

func TestDeployBuildsWhenTheCheckDoesNotApply(t *testing.T) {
	cases := []struct {
		name     string
		args     []string
		failList bool
		listed   bool
	}{
		// An explicit label is the caller's: never second-guessed.
		{"explicit --dag-version", []string{"--dag-version", "mine"}, false, false},
		// A failed check must not block a deploy that worked before it existed.
		{"check fails", nil, true, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir, builder, log := deployFixture(t)
			fake := &deployServer{label: "mine", failList: c.failList}
			srv := httptest.NewServer(fake)
			defer srv.Close()
			args := append([]string{"deploy", dir, "--server", srv.URL, "--token", "x", "--yes", "--builder", builder}, c.args...)
			_, _, _ = run(t, args...) // the stub builder cannot report a digest; only the build matters here
			if (fake.listed > 0) != c.listed {
				t.Errorf("version list requested %d time(s), want requested=%v", fake.listed, c.listed)
			}
			if lines := nonEmptyLines(t, log); len(lines) == 0 {
				t.Error("the builder never ran")
			}
		})
	}
}
