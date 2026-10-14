package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// devGetJSON does an authenticated GET against the running control plane and
// decodes the JSON body into out. A non-200 is an error (an error page is never
// decoded as data), which keeps the reconcile callers fail-safe.
func devGetJSON(serverURL, pathQuery, token string, out any) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(serverURL, "/")+pathQuery, http.NoBody)
	if err != nil {
		return fmt.Errorf("building request: %w", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := cliHTTPClient(10 * time.Second).Do(req)
	if err != nil {
		return fmt.Errorf("GET %s: %w", pathQuery, err)
	}
	defer func() { _ = resp.Body.Close() }() //nolint:errcheck // best-effort close of the response body
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512)) //nolint:errcheck // best-effort read of an error body for the message
		return fmt.Errorf("GET %s: %s: %s", pathQuery, resp.Status, strings.TrimSpace(string(body)))
	}
	if derr := json.NewDecoder(resp.Body).Decode(out); derr != nil {
		return fmt.Errorf("decoding %s: %w", pathQuery, derr)
	}
	return nil
}

// reconcileStartup deregisters, at Lite boot, every DAG the control plane still
// has registered that is absent from the current workspace (#404) — the stale
// ghosts left by a reused metadata DB or a previous workspace. It reuses the
// watcher's set-diff primitive (removeMissingDags), so a deregister failure
// retries on later ticks. The fetch's failure is non-fatal and fail-safe: with
// no reliable registered set it keeps the workspace IDs and deregisters nothing,
// so an unreachable or flaky control plane never wipes a DAG by mistake.
func reconcileStartup(
	registered map[string]struct{},
	fetchErr error,
	workspace map[string]struct{},
	deleteDag func(dagID string) error,
	logf func(format string, args ...any),
) map[string]struct{} {
	if fetchErr != nil {
		logf("⚠ could not list registered DAGs to reconcile (%v) — skipping stale-DAG cleanup this boot", fetchErr)
		return workspace
	}
	return removeMissingDags(registered, workspace, deleteDag, logf)
}

// registeredDagsResponse is the slice of GET /api/v2/dags reconcile reads.
type registeredDagsResponse struct {
	Dags []struct {
		DagID string `json:"dag_id"`
	} `json:"dags"`
}

// fetchRegisteredDagIDs returns the set of dag_ids the control plane currently
// has registered, via GET /api/v2/dags.
func fetchRegisteredDagIDs(serverURL, token string) (map[string]struct{}, error) {
	var parsed registeredDagsResponse
	if err := devGetJSON(serverURL, "/api/v2/dags?limit=10000", token, &parsed); err != nil {
		return nil, err
	}
	ids := make(map[string]struct{}, len(parsed.Dags))
	for _, d := range parsed.Dags {
		if d.DagID != "" {
			ids[d.DagID] = struct{}{}
		}
	}
	return ids, nil
}

// importErrorsResponse is the slice of GET /api/v2/importErrors reconcile reads.
type importErrorsResponse struct {
	ImportErrors []struct {
		Filename string `json:"filename"`
	} `json:"import_errors"`
}

// fetchImportErrorFiles returns the filenames the control plane currently has
// import errors for, via GET /api/v2/importErrors.
func fetchImportErrorFiles(serverURL, token string) ([]string, error) {
	var parsed importErrorsResponse
	if err := devGetJSON(serverURL, "/api/v2/importErrors", token, &parsed); err != nil {
		return nil, err
	}
	files := make([]string, 0, len(parsed.ImportErrors))
	for _, e := range parsed.ImportErrors {
		if e.Filename != "" {
			files = append(files, e.Filename)
		}
	}
	return files, nil
}

// importErrorStale reports whether an import error for filename is stale and
// should be cleared on boot (#404): its file is outside the current workspace (a
// previous workspace's error) OR no longer exists (a removed broken DAG). A
// current broken DAG — file present, under the workspace — is NOT stale: its
// error is real and the per-reload compile owns it.
func importErrorStale(filename, workspaceRoot string) bool {
	clean := filepath.Clean(filename)
	root := filepath.Clean(workspaceRoot)
	if clean != root && !strings.HasPrefix(clean, root+string(os.PathSeparator)) {
		return true
	}
	if _, err := os.Stat(clean); err != nil {
		return true
	}
	return false
}

// reconcileImportErrors clears every stale import error (per isStale). Fail-safe:
// a list-fetch error clears nothing. A per-entry clear failure is logged and the
// rest proceed.
func reconcileImportErrors(
	files []string,
	fetchErr error,
	isStale func(filename string) bool,
	clearErr func(filename string) error,
	logf func(format string, args ...any),
) {
	if fetchErr != nil {
		logf("⚠ could not list import errors to reconcile (%v) — skipping stale-error cleanup this boot", fetchErr)
		return
	}
	for _, f := range files {
		if !isStale(f) {
			continue
		}
		if err := clearErr(f); err != nil {
			logf("✗ failed to clear stale import error %s: %v", f, err)
			continue
		}
		logf("✓ cleared stale import error %s", f)
	}
}

// makeBootReconcile builds the one-shot boot self-heal devWatchLoop runs after
// its first reload: deregister ghost DAGs (registered but absent on disk) and
// clear stale import errors (a previous workspace's, or a removed file), so Lite
// self-heals to match the workspace instead of showing un-removable ghosts (#404).
// It returns the DAG-ID set the watcher seeds its per-tick diff from. Each step is
// independently fail-safe — a failed list fetch skips that step, never wiping.
func makeBootReconcile(
	mintToken func() string,
	uiURL, workspaceRoot string,
	workspace map[string]struct{},
	deleteDag func(dagID string) error,
	logf func(format string, args ...any),
) func() map[string]struct{} {
	return func() map[string]struct{} {
		token := mintToken()
		registered, ferr := fetchRegisteredDagIDs(uiURL, token)
		seen := reconcileStartup(registered, ferr, workspace, deleteDag, logf)
		files, ierr := fetchImportErrorFiles(uiURL, token)
		reconcileImportErrors(files, ierr,
			func(f string) bool { return importErrorStale(f, workspaceRoot) },
			func(f string) error { return clearImportError(context.Background(), uiURL, token, f) }, logf)
		return seen
	}
}
