package api

import (
	"net/http"
	"strings"
	"testing"
)

const tiBase = "/api/v2/dags/etl/dagRuns/r1/taskInstances"

// TestServeSingleTaskInstance covers the single task-instance endpoint the UI's
// task panel reads, including its failure modes.
func TestServeSingleTaskInstance(t *testing.T) {
	srv := authedServer() // wires one TI: task "extract", map_index 0

	// Happy path: matching task + map_index returns the instance.
	if rec := authGet(srv, http.MethodGet, tiBase+"/extract/0", ""); rec.Code != http.StatusOK ||
		!strings.Contains(rec.Body.String(), "extract") {
		t.Errorf("extract/0 = %d (%s), want 200 with the instance", rec.Code, rec.Body.String())
	}
	// A map_index that does not exist is 404, not a wrong instance.
	if rec := authGet(srv, http.MethodGet, tiBase+"/extract/7", ""); rec.Code != http.StatusNotFound {
		t.Errorf("extract/7 (no such map_index) = %d, want 404", rec.Code)
	}
	// An unknown task is 404.
	if rec := authGet(srv, http.MethodGet, tiBase+"/ghost/0", ""); rec.Code != http.StatusNotFound {
		t.Errorf("ghost/0 = %d, want 404", rec.Code)
	}
	// A segment that is neither a map_index nor a known sub-resource is a clear
	// 404 "unsupported action" — NOT the old misleading "map_index must be an
	// integer" 400, which mislabeled UI sub-resources (e.g. "{map_index}/tries")
	// as a bad map_index.
	rec := authGet(srv, http.MethodGet, tiBase+"/extract/banana", "")
	if rec.Code != http.StatusNotFound {
		t.Errorf("extract/banana = %d, want 404", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "map_index must be an integer") {
		t.Errorf("unknown action must not be reported as a map_index error: %s", rec.Body.String())
	}
}

// TestServeTaskTries covers the try-history endpoints the task Details tab reads.
func TestServeTaskTries(t *testing.T) {
	srv := authedServer()

	// The collection returns the attempt(s) for the task.
	if rec := authGet(srv, http.MethodGet, tiBase+"/extract/tries", ""); rec.Code != http.StatusOK ||
		!strings.Contains(rec.Body.String(), "extract") {
		t.Errorf("tries = %d (%s), want 200 with the attempt", rec.Code, rec.Body.String())
	}
	// A specific attempt resolves (Dexaflow keeps one row per task).
	if rec := authGet(srv, http.MethodGet, tiBase+"/extract/tries/1", ""); rec.Code != http.StatusOK {
		t.Errorf("tries/1 = %d, want 200", rec.Code)
	}
	// A specific attempt of an unknown task is 404.
	if rec := authGet(srv, http.MethodGet, tiBase+"/ghost/tries/1", ""); rec.Code != http.StatusNotFound {
		t.Errorf("ghost/tries/1 = %d, want 404", rec.Code)
	}
}

// TestTaskInstanceExtraLinks: the Details view requires an extra_links object;
// a bare {} or a 400 would crash the UI, so the endpoint must return it.
func TestTaskInstanceExtraLinks(t *testing.T) {
	rec := authGet(authedServer(), http.MethodGet, tiBase+"/extract/links", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "extra_links") {
		t.Errorf("links = %d (%s), want 200 with extra_links", rec.Code, rec.Body.String())
	}
}
