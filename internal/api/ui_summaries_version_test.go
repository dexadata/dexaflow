package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/domain"
)

func versionedSummariesServer(reader TaskSummaryReader, runs DagRunRepository, versions DagVersionLister) *gin.Engine {
	return newTestServer(Dependencies{
		Observability: ObservabilityDeps{Logger: discardLogger()},
		Server:        ServerDeps{CORSOrigins: []string{"*"}},
		Auth: AuthDeps{
			Authenticator: &fakeAuthn{user: &auth.User{ID: "u1", TenantID: "default", Roles: []string{"admin"}}},
			RateLimiter:   auth.NewRateLimiter(100, time.Minute),
		},
		Stores: StoreDeps{TaskSummary: reader, DagRuns: runs, DagVersions: versions},
	})
}

func summaryVersions(t *testing.T, srv *gin.Engine, query string) map[string]*int {
	t.Helper()
	rec := authGet(srv, http.MethodGet, "/ui/grid/ti_summaries/etl?"+query, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("ti_summaries = %d (%s)", rec.Code, rec.Body.String())
	}
	out := map[string]*int{}
	for _, r := range decodeNDJSON(t, rec.Body.String()) {
		for _, ti := range r.TaskInstances {
			out[r.RunID] = ti.DagVersionNumber
		}
	}
	return out
}

// TestTISummariesCarryRunDagVersionNumber pins the grid click into a task
// instance. The Airflow 3.3 grid links a cell to /dags/{d}/runs/{r}/tasks/{t}
// only when its summary carries dag_version_number; without it every cell
// opens the run-less task page, which has no Logs tab. The number is the one
// the cell's run is pinned to, and the newest one when that label is gone.
func TestTISummariesCarryRunDagVersionNumber(t *testing.T) {
	reader := &fakeTaskSummary{tis: []domain.TaskInstance{
		{RunID: "old", TaskID: "x", State: domain.TaskStateSuccess},
		{RunID: "new", TaskID: "x", State: domain.TaskStateSuccess},
		{RunID: "gone", TaskID: "x", State: domain.TaskStateSuccess},
	}}
	runs := &fakeRunRepo{runs: []domain.DagRun{
		{RunID: "old", Version: "v1"},
		{RunID: "new", Version: "v2"},
		{RunID: "gone", Version: "pruned"},
	}}
	versions := &fakeVersionLister{versions: []domain.DagVersion{ // newest first
		{ID: "b", VersionNumber: 2, Version: "v2"},
		{ID: "a", VersionNumber: 1, Version: "v1"},
	}}
	got := summaryVersions(t, versionedSummariesServer(reader, runs, versions), "run_ids=old,new,gone")
	want := map[string]int{"old": 1, "new": 2, "gone": 2}
	for run, n := range want {
		if got[run] == nil || *got[run] != n {
			t.Errorf("run %s dag_version_number = %v, want %d", run, got[run], n)
		}
	}
}

// TestTISummariesVersionNumberNullWithoutVersions keeps the null when no
// version can be resolved, rather than inventing a number.
func TestTISummariesVersionNumberNullWithoutVersions(t *testing.T) {
	reader := &fakeTaskSummary{tis: []domain.TaskInstance{{RunID: "r1", TaskID: "x", State: domain.TaskStateSuccess}}}
	runs := &fakeRunRepo{runs: []domain.DagRun{{RunID: "r1", Version: "v1"}}}
	for name, srv := range map[string]*gin.Engine{
		"no stores":   versionedSummariesServer(reader, nil, nil),
		"no versions": versionedSummariesServer(reader, runs, &fakeVersionLister{}),
		"run missing": versionedSummariesServer(reader, &fakeRunRepo{}, &fakeVersionLister{versions: []domain.DagVersion{{VersionNumber: 1, Version: "v1"}}}),
	} {
		if got := summaryVersions(t, srv, "run_ids=r1")["r1"]; got != nil {
			t.Errorf("%s: dag_version_number = %d, want null", name, *got)
		}
	}
}

// TestTISummariesETagTracksRunVersion: a clear that re-pins a run to the latest
// version changes no task row, so the ETag must still change or the grid keeps
// serving the old version number from its cache.
func TestTISummariesETagTracksRunVersion(t *testing.T) {
	reader := &fakeTaskSummary{tis: []domain.TaskInstance{{RunID: "r1", TaskID: "x", State: domain.TaskStateSuccess}}}
	versions := &fakeVersionLister{versions: []domain.DagVersion{
		{VersionNumber: 2, Version: "v2"},
		{VersionNumber: 1, Version: "v1"},
	}}
	etag := func(label string) string {
		runs := &fakeRunRepo{runs: []domain.DagRun{{RunID: "r1", Version: label}}}
		return authGet(versionedSummariesServer(reader, runs, versions), http.MethodGet, "/ui/grid/ti_summaries/etl?run_ids=r1", "").Header().Get("ETag")
	}
	if before, after := etag("v1"), etag("v2"); before == after {
		t.Errorf("ETag unchanged after the run moved from v1 to v2: %q", before)
	}
}

// TestGridRunsCarryPinnedDagVersion: the 3.3 grid draws its version-change
// markers by comparing each run's dag_versions with the next run's, so a run
// must carry the version it is pinned to. A label that no longer resolves
// leaves the list empty rather than claiming another version.
func TestGridRunsCarryPinnedDagVersion(t *testing.T) {
	at := time.Date(2026, 5, 22, 10, 0, 0, 0, time.UTC)
	runs := &fakeRunRepo{runs: []domain.DagRun{
		{DagID: "etl", RunID: "new", LogicalDate: at, Version: "v2"},
		{DagID: "etl", RunID: "old", LogicalDate: at.Add(-time.Hour), Version: "v1"},
		{DagID: "etl", RunID: "gone", LogicalDate: at.Add(-2 * time.Hour), Version: "pruned"},
	}}
	versions := &fakeVersionLister{versions: []domain.DagVersion{
		{ID: "b", VersionNumber: 2, Version: "v2"},
		{ID: "a", VersionNumber: 1, Version: "v1"},
	}}
	rec := authGet(versionedSummariesServer(nil, runs, versions), http.MethodGet, "/ui/grid/runs/etl", "")
	var cols []struct {
		RunID       string `json:"run_id"`
		DagVersions []struct {
			VersionNumber int    `json:"version_number"`
			BundleVersion string `json:"bundle_version"`
		} `json:"dag_versions"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &cols); err != nil || len(cols) != 3 {
		t.Fatalf("grid runs = %d %s", rec.Code, rec.Body.String())
	}
	want := map[string]int{"new": 2, "old": 1, "gone": 0}
	for _, col := range cols {
		n := want[col.RunID]
		if n == 0 {
			if col.DagVersions == nil || len(col.DagVersions) != 0 {
				t.Errorf("run %s dag_versions = %+v, want []", col.RunID, col.DagVersions)
			}
			continue
		}
		if len(col.DagVersions) != 1 || col.DagVersions[0].VersionNumber != n {
			t.Errorf("run %s dag_versions = %+v, want version %d", col.RunID, col.DagVersions, n)
		}
	}
}

// batchRunRepo is a run repository that reads version labels in one call, as
// the storage repository does. getCalls counts per-run reads, which the grid
// stream must not make when the batch read exists.
type batchRunRepo struct {
	fakeRunRepo
	labels     map[string]string
	err        error
	batchCalls int
	getCalls   int
}

func (b *batchRunRepo) DagRunVersionLabels(_ context.Context, _, _ string, runIDs []string) (map[string]string, error) {
	b.batchCalls++
	if b.err != nil {
		return nil, b.err
	}
	out := map[string]string{}
	for _, id := range runIDs {
		if l, ok := b.labels[id]; ok {
			out[id] = l
		}
	}
	return out, nil
}

func (b *batchRunRepo) GetDagRun(ctx context.Context, tenant, dagID, runID string) (domain.DagRun, error) {
	b.getCalls++
	return b.fakeRunRepo.GetDagRun(ctx, tenant, dagID, runID)
}

// TestTISummariesReadRunVersionsInOneQuery keeps the polled grid stream at one
// label read per request, not one run read per column, with the same numbers
// as the per-run path: pinned, newest when the label is gone or empty, null for
// a run that does not exist, and null for every run when the read fails.
func TestTISummariesReadRunVersionsInOneQuery(t *testing.T) {
	reader := &fakeTaskSummary{tis: []domain.TaskInstance{
		{RunID: "old", TaskID: "x", State: domain.TaskStateSuccess},
		{RunID: "new", TaskID: "x", State: domain.TaskStateSuccess},
		{RunID: "gone", TaskID: "x", State: domain.TaskStateSuccess},
		{RunID: "nolabel", TaskID: "x", State: domain.TaskStateSuccess},
		{RunID: "missing", TaskID: "x", State: domain.TaskStateSuccess},
	}}
	versions := &fakeVersionLister{versions: []domain.DagVersion{
		{ID: "b", VersionNumber: 2, Version: "v2"},
		{ID: "a", VersionNumber: 1, Version: "v1"},
	}}
	runs := &batchRunRepo{labels: map[string]string{"old": "v1", "new": "v2", "gone": "pruned", "nolabel": ""}}
	got := summaryVersions(t, versionedSummariesServer(reader, runs, versions), "run_ids=old,new,gone,nolabel,missing")
	for run, n := range map[string]int{"old": 1, "new": 2, "gone": 2, "nolabel": 2} {
		if got[run] == nil || *got[run] != n {
			t.Errorf("run %s dag_version_number = %v, want %d", run, got[run], n)
		}
	}
	if got["missing"] != nil {
		t.Errorf("missing run dag_version_number = %d, want null", *got["missing"])
	}
	if runs.batchCalls != 1 || runs.getCalls != 0 {
		t.Errorf("label reads: batch %d, per run %d; want 1 and 0", runs.batchCalls, runs.getCalls)
	}

	failing := &batchRunRepo{err: errors.New("db down")}
	for run, n := range summaryVersions(t, versionedSummariesServer(reader, failing, versions), "run_ids=old,new") {
		if n != nil {
			t.Errorf("run %s with a failed label read = %d, want null", run, *n)
		}
	}
}
