package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// versionsServer is a fake control plane holding the versions of one DAG,
// newest first, that answers the versions listing and records set-current
// requests.
type versionsServer struct {
	versions []map[string]any
	puts     []map[string]any
	auth     string
	putCode  int
	// warnings is what the set-current answer warns about.
	warnings []string
}

func (s *versionsServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.auth = r.Header.Get("Authorization")
	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/api/v2/dags/etl/dagVersions":
		_ = json.NewEncoder(w).Encode(map[string]any{"dag_versions": s.versions, "total_entries": len(s.versions)})
	case r.Method == http.MethodPut && r.URL.Path == "/api/v2/dags/etl/currentVersion":
		body, _ := io.ReadAll(r.Body)
		var req map[string]any
		_ = json.Unmarshal(body, &req)
		s.puts = append(s.puts, req)
		if s.putCode != 0 {
			w.WriteHeader(s.putCode)
			_, _ = w.Write([]byte(`{"detail":"refused"}`))
			return
		}
		var from, to map[string]any
		for _, v := range s.versions {
			if v["is_current"] == true {
				from = v
			}
			if v["bundle_version"] == req["version"] {
				to = v
			}
		}
		answer := map[string]any{"dag_id": "etl", "changed": from["bundle_version"] != to["bundle_version"], "from": from, "to": to}
		if s.warnings != nil {
			answer["warnings"] = s.warnings
		}
		_ = json.NewEncoder(w).Encode(answer)
	default:
		http.NotFound(w, r)
	}
}

// threeVersions is etl with versions 1..3, version 3 current.
func threeVersions() *versionsServer {
	v := func(n int, label string, current bool, by any) map[string]any {
		return map[string]any{
			"id": label, "version_number": n, "dag_id": "etl", "bundle_version": label,
			"created_at": "2026-10-0" + string(rune('0'+n)) + "T10:00:00Z",
			"image":      "registry.example/etl@sha256:" + strings.Repeat(string(rune('a'+n)), 64),
			"spec_hash":  "h", "is_current": current, "created_by": by,
		}
	}
	newest := v(3, "v1.2.0-ccc", true, "7f3c2a10-5b1e-4c3d-9a8e-2f6b1c0d4e5a")
	newest["build_info"] = map[string]any{"commit": "0123456789abcdef0123456789abcdef01234567", "repo": "https://github.com/acme/etl"}
	return &versionsServer{versions: []map[string]any{
		newest,
		v(2, "v1.1.0-bbb", false, "0b9d8c7e-6f5a-4b3c-8d2e-1a0f9e8d7c6b"),
		v(1, "v1.0.0-aaa", false, nil),
	}}
}

func TestDagsVersionsPrintsTheTable(t *testing.T) {
	fake := threeVersions()
	srv := httptest.NewServer(fake)
	defer srv.Close()
	out, _, err := run(t, "dags", "versions", "etl", "--server", srv.URL, "--token", "tok")
	if err != nil {
		t.Fatalf("dags versions: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 4 {
		t.Fatalf("want a header and 3 rows, got:\n%s", out)
	}
	for _, want := range []string{"VERSION", "LABEL", "CURRENT", "CREATED", "CREATOR ID", "COMMIT", "IMAGE"} {
		if !strings.Contains(lines[0], want) {
			t.Errorf("header %q misses %s", lines[0], want)
		}
	}
	if !strings.Contains(lines[1], "v1.2.0-ccc") || !strings.Contains(lines[1], "*") || !strings.Contains(lines[1], "7f3c2a10-5b1e-4c3d-9a8e-2f6b1c0d4e5a") ||
		!strings.Contains(lines[1], "sha256:dddddddddddd") || strings.Contains(lines[1], strings.Repeat("d", 13)) {
		t.Errorf("current row = %q, want label, marker, user and a short digest", lines[1])
	}
	if !strings.Contains(lines[1], " 0123456789ab ") || strings.Contains(lines[1], "0123456789abc") {
		t.Errorf("current row = %q, want the commit shortened to 12 characters", lines[1])
	}
	if strings.Contains(lines[2], "*") || !strings.Contains(lines[3], "-") {
		t.Errorf("older rows = %q / %q, want no marker and - for an unknown user", lines[2], lines[3])
	}
	if fake.auth != "Bearer tok" {
		t.Errorf("Authorization = %q", fake.auth)
	}
}

func TestRollbackDefaultsToThePreviousVersion(t *testing.T) {
	fake := threeVersions()
	srv := httptest.NewServer(fake)
	defer srv.Close()
	out, _, err := run(t, "rollback", "etl", "--server", srv.URL, "--token", "tok")
	if err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if len(fake.puts) != 1 || fake.puts[0]["version"] != "v1.1.0-bbb" {
		t.Fatalf("set-current requests = %v, want version v1.1.0-bbb", fake.puts)
	}
	if !strings.Contains(out, "v1.2.0-ccc") || !strings.Contains(out, "v1.1.0-bbb") {
		t.Errorf("output %q does not print from and to", out)
	}
}

func TestRollbackToALabelOrANumber(t *testing.T) {
	for to, want := range map[string]string{"v1.0.0-aaa": "v1.0.0-aaa", "1": "v1.0.0-aaa", "2": "v1.1.0-bbb"} {
		fake := threeVersions()
		srv := httptest.NewServer(fake)
		_, _, err := run(t, "rollback", "etl", "--to", to, "--server", srv.URL, "--token", "tok")
		srv.Close()
		if err != nil {
			t.Fatalf("rollback --to %s: %v", to, err)
		}
		if len(fake.puts) != 1 || fake.puts[0]["version"] != want {
			t.Errorf("--to %s sent %v, want version %s", to, fake.puts, want)
		}
	}
}

func TestRollbackRefusesWhatItCannotResolve(t *testing.T) {
	fake := threeVersions()
	srv := httptest.NewServer(fake)
	defer srv.Close()
	if _, _, err := run(t, "rollback", "etl", "--to", "v9", "--server", srv.URL, "--token", "tok"); err == nil || !strings.Contains(err.Error(), "v9") {
		t.Errorf("unknown --to: err = %v, want one naming v9", err)
	}
	oldestCurrent := threeVersions()
	oldestCurrent.versions[0]["is_current"], oldestCurrent.versions[2]["is_current"] = false, true
	srv2 := httptest.NewServer(oldestCurrent)
	defer srv2.Close()
	if _, _, err := run(t, "rollback", "etl", "--server", srv2.URL, "--token", "tok"); err == nil || !strings.Contains(err.Error(), "no version before") {
		t.Errorf("oldest current: err = %v, want no version before it", err)
	}
	if len(fake.puts)+len(oldestCurrent.puts) != 0 {
		t.Errorf("sent set-current requests for unresolved targets")
	}
}

func TestRollbackReportsAServerRefusal(t *testing.T) {
	fake := threeVersions()
	fake.putCode = http.StatusBadRequest
	srv := httptest.NewServer(fake)
	defer srv.Close()
	if _, _, err := run(t, "rollback", "etl", "--server", srv.URL, "--token", "tok"); err == nil || !strings.Contains(err.Error(), "400") {
		t.Errorf("err = %v, want the 400", err)
	}
}

func TestRollbackAlreadyCurrentSaysSo(t *testing.T) {
	fake := threeVersions()
	srv := httptest.NewServer(fake)
	defer srv.Close()
	out, _, err := run(t, "rollback", "etl", "--to", "v1.2.0-ccc", "--server", srv.URL, "--token", "tok")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "already runs v1.2.0-ccc") || strings.Contains(out, "changed from") {
		t.Errorf("output %q does not say the version is already current", out)
	}
}

// The server's warnings on a rollback (an image not pinned by digest under
// deploy.require_digest=warn, #1703) are printed on stderr; the rollback
// itself succeeds.
func TestRollbackPrintsTheServerWarnings(t *testing.T) {
	fake := threeVersions()
	fake.warnings = []string{"this version's image \"etl:v1\" is not pinned by digest"}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	out, errOut, err := run(t, "rollback", "etl", "--server", srv.URL, "--token", "tok")
	if err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if !strings.Contains(errOut, "warning: the server: this version's image \"etl:v1\" is not pinned by digest\n") {
		t.Errorf("stderr = %q, want the server's warning", errOut)
	}
	if !strings.Contains(out, "current version changed") {
		t.Errorf("stdout = %q, want the change", out)
	}
}
