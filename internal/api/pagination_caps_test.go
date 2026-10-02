package api

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/domain"
)

// pagedRunRepo records which run listing a handler used and the limit it asked
// for. ListDagRuns also counts every run of the DAG; ListDagRunsPage does not.
type pagedRunRepo struct {
	fakeRunRepo
	listCalls, pageCalls int
	gotLimit             int
}

func (p *pagedRunRepo) ListDagRuns(_ context.Context, _, _ string, limit, _ int) ([]domain.DagRun, int, error) {
	p.listCalls++
	p.gotLimit = limit
	return []domain.DagRun{{DagID: "d", RunID: "r1"}}, 1, nil
}

func (p *pagedRunRepo) ListDagRunsPage(_ context.Context, _, _ string, limit, _ int) ([]domain.DagRun, error) {
	p.pageCalls++
	p.gotLimit = limit
	return []domain.DagRun{{DagID: "d", RunID: "r1"}}, nil
}

func pagedServer(runs DagRunRepository, latest DagLatestRunsReader, maxPage int) *gin.Engine {
	return NewServer(Dependencies{
		Logger:        discardLogger(),
		Authenticator: &fakeAuthn{user: &auth.User{ID: "u1", TenantID: "default", Roles: []string{"admin"}}},
		RateLimiter:   auth.NewRateLimiter(100, time.Minute),
		CORSOrigins:   []string{"*"},
		DagRuns:       runs,
		Dags:          &fakeDagRepo{dags: []domain.DAG{{DagID: "d"}}},
		LatestRuns:    latest,
		MaxPageLimit:  maxPage,
	})
}

// TestGridAndLatestRunSkipTheRunCount pins that the two UI views that never
// use the total stop paying for the COUNT over every run of the DAG.
func TestGridAndLatestRunSkipTheRunCount(t *testing.T) {
	for _, path := range []string{"/ui/grid/runs/d", "/ui/dags/d/latest_run"} {
		repo := &pagedRunRepo{}
		rec := authGet(pagedServer(repo, &fakeLatestRuns{}, 0), http.MethodGet, path, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("%s = %d (%s)", path, rec.Code, rec.Body.String())
		}
		if repo.listCalls != 0 || repo.pageCalls != 1 {
			t.Errorf("%s: ListDagRuns=%d ListDagRunsPage=%d, want 0 and 1", path, repo.listCalls, repo.pageCalls)
		}
	}
}

// TestPaginationIsUncappedByDefault pins the default of server.max_page_limit:
// a large limit reaches the repository unchanged, as today.
func TestPaginationIsUncappedByDefault(t *testing.T) {
	repo := &pagedRunRepo{}
	authGet(pagedServer(repo, &fakeLatestRuns{}, 0), http.MethodGet, "/ui/grid/runs/d?limit=5000", "")
	if repo.gotLimit != 5000 {
		t.Errorf("limit reached the repository as %d, want 5000", repo.gotLimit)
	}
}

// TestPaginationIsCappedWhenConfigured pins the opt-in cap on limit.
func TestPaginationIsCappedWhenConfigured(t *testing.T) {
	cases := map[string]int{"/ui/grid/runs/d?limit=5000": 100, "/ui/grid/runs/d?limit=20": 20, "/ui/grid/runs/d": 100}
	for path, want := range cases {
		repo := &pagedRunRepo{}
		authGet(pagedServer(repo, &fakeLatestRuns{}, 100), http.MethodGet, path, "")
		if repo.gotLimit != want {
			t.Errorf("%s: limit = %d, want %d", path, repo.gotLimit, want)
		}
	}
}

// TestDagRunsLimitIsCappedWhenConfigured pins that /ui/dags' per-DAG run count
// obeys the same cap.
func TestDagRunsLimitIsCappedWhenConfigured(t *testing.T) {
	for maxPage, want := range map[int]int{0: 5000, 100: 100} {
		latest := &fakeLatestRuns{}
		authGet(pagedServer(&pagedRunRepo{}, latest, maxPage), http.MethodGet, "/ui/dags?dag_runs_limit=5000", "")
		if latest.gotN != want {
			t.Errorf("max %d: dag_runs_limit = %d, want %d", maxPage, latest.gotN, want)
		}
	}
}
