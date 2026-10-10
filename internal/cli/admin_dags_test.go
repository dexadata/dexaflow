package cli

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"

	apiclient "github.com/dexadata/dexaflow/pkg/client"
)

// dagsPauseServer serves a DAG list and records every PATCH so a test can
// assert which DAGs were (un)paused and with what is_paused value.
func dagsPauseServer(t *testing.T, ids []string) (*httptest.Server, *pauseRecorder) {
	t.Helper()
	rec := &pauseRecorder{paused: map[string]bool{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v2/dags", func(w http.ResponseWriter, _ *http.Request) {
		dags := make([]apiclient.DAG, 0, len(ids))
		for _, id := range ids {
			dags = append(dags, apiclient.DAG{DagId: strptr(id), IsPaused: boolptr(false)})
		}
		total := len(ids)
		writeJSON(t, w, apiclient.DAGCollection{Dags: &dags, TotalEntries: &total})
	})
	mux.HandleFunc("/api/v2/dags/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		id := strings.TrimPrefix(r.URL.Path, "/api/v2/dags/")
		var body apiclient.DAGUpdate
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		rec.record(id, body.IsPaused != nil && *body.IsPaused)
		writeJSON(t, w, apiclient.DAG{DagId: strptr(id), IsPaused: body.IsPaused})
	})
	return httptest.NewServer(mux), rec
}

type pauseRecorder struct {
	mu     sync.Mutex
	paused map[string]bool
	order  []string
}

func (p *pauseRecorder) record(id string, paused bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, seen := p.paused[id]; !seen {
		p.order = append(p.order, id)
	}
	p.paused[id] = paused
}

func (p *pauseRecorder) ids() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := append([]string(nil), p.order...)
	sort.Strings(out)
	return out
}

func TestSetPausedForDAGsPatchesEach(t *testing.T) {
	srv, rec := dagsPauseServer(t, []string{"a", "b", "c"})
	defer srv.Close()

	var sb strings.Builder
	err := setPausedForDAGs(context.Background(), &sb, newTestClient(t, srv.URL), []string{"a", "b", "c"}, true)
	if err != nil {
		t.Fatalf("setPausedForDAGs: %v", err)
	}
	got := rec.ids()
	if strings.Join(got, ",") != "a,b,c" {
		t.Errorf("patched ids = %v, want [a b c]", got)
	}
	for _, id := range got {
		if !rec.paused[id] {
			t.Errorf("dag %q was not paused (is_paused=false)", id)
		}
	}
}

// TestAdminPauseAllPatchesEveryDAG drives the full `admin dags pause --all`
// command: it must discover every DAG via the list endpoint and PATCH each.
func TestAdminPauseAllPatchesEveryDAG(t *testing.T) {
	srv, rec := dagsPauseServer(t, []string{"etl", "reports", "sync"})
	defer srv.Close()

	_, _, err := run(t, "admin", "dags", "pause", "--all", "--server", srv.URL, "--token", "x")
	if err != nil {
		t.Fatalf("admin dags pause --all: %v", err)
	}
	if got := strings.Join(rec.ids(), ","); got != "etl,reports,sync" {
		t.Errorf("patched ids = %q, want etl,reports,sync", got)
	}
	for id, paused := range rec.paused {
		if !paused {
			t.Errorf("dag %q not paused", id)
		}
	}
}

func TestAdminUnpauseSingleDAG(t *testing.T) {
	srv, rec := dagsPauseServer(t, []string{"etl"})
	defer srv.Close()

	_, _, err := run(t, "admin", "dags", "unpause", "etl", "--server", srv.URL, "--token", "x")
	if err != nil {
		t.Fatalf("admin dags unpause etl: %v", err)
	}
	if v, ok := rec.paused["etl"]; !ok || v {
		t.Errorf("etl is_paused = %v (ok=%v), want false", v, ok)
	}
}

func TestAdminPauseAllRejectsPositionalArg(t *testing.T) {
	srv, _ := dagsPauseServer(t, []string{"etl"})
	defer srv.Close()

	if _, _, err := run(t, "admin", "dags", "pause", "etl", "--all", "--server", srv.URL, "--token", "x"); err == nil {
		t.Errorf("expected error when --all is combined with a dag_id")
	}
}
