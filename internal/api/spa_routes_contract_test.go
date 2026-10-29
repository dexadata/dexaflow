package api

import "testing"

// TestSPAReferencedRoutesAreRegistered guards the whole class of bug behind the
// connector-config "page wouldn't open" report: the embedded Airflow 3.2 SPA polls
// a fixed set of endpoints on screen load, and its generated client handles only
// 401/403 — an unregistered route 404s and the React view crashes. This asserts
// the paramless endpoints the SPA references are all registered (any METHOD), so a
// future SPA bump or a half-wired feature can't silently reintroduce a 404 screen.
//
// The list is the paramless /api/v2 + /ui paths extracted from the embedded SPA
// bundle (internal/ui/assets). Parameterised paths (/dags/:id, /grid/...) are
// covered by their own handler tests.
func TestSPAReferencedRoutesAreRegistered(t *testing.T) {
	srv := stubsServer()
	registered := map[string]bool{}
	for _, ri := range srv.Routes() {
		registered[ri.Path] = true
	}

	// Every paramless path the SPA fetches. Method is intentionally ignored — we
	// only assert the route exists (no 404); the handler's own test covers shape.
	wantPaths := []string{
		// The reported bug + its siblings (were 404 → broken screens):
		"/api/v2/config", // Config screen
		// The connection area + its catalog:
		"/ui/connections/hook_meta",
		"/api/v2/connections",
		"/api/v2/connections/test",
		// Other screen-load polls that must not 404:
		"/api/v2/dagTags", "/api/v2/dagWarnings", "/api/v2/plugins",
		"/api/v2/plugins/importErrors", "/api/v2/pools", "/api/v2/providers",
		"/api/v2/jobs", "/api/v2/variables", "/api/v2/version",
		"/api/v2/importErrors", "/api/v2/monitor/health",
		"/ui/auth/me", "/ui/auth/menus", "/ui/config", "/ui/dependencies",
		"/ui/dashboard/dag_stats",
	}
	for _, p := range wantPaths {
		if !registered[p] {
			t.Errorf("SPA-referenced route %q is not registered — the Airflow SPA 404s on it and the React view crashes (see connections/defaults)", p)
		}
	}
}
