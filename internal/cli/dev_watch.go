package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/dexadata/dexaflow/internal/domain"
)

// projectMtimes returns the modtime of each existing path, silently skipping
// paths that do not exist so a not-yet-created file can be detected on creation.
func projectMtimes(paths []string) map[string]time.Time {
	out := make(map[string]time.Time, len(paths))
	for _, p := range paths {
		if fi, err := os.Stat(p); err == nil {
			out[p] = fi.ModTime()
		}
	}
	return out
}

// mtimesChanged reports whether any watched file appeared, vanished, or had its
// modtime move between two snapshots.
func mtimesChanged(prev, cur map[string]time.Time) bool {
	if len(prev) != len(cur) {
		return true
	}
	for p, t := range cur {
		if old, ok := prev[p]; !ok || !old.Equal(t) {
			return true
		}
	}
	return false
}

// devWatchLoop runs reload once, then again on every save of a watched file
// until the context is canceled (Ctrl-C). reload encapsulates the mode-specific
// build/register step. The set of watched files is re-derived from the
// workspace on every tick so a new project subdir added at runtime is picked
// up without restart.
func devWatchLoop(ctx context.Context, cmd *cobra.Command, ws *WorkspaceSpec, reload func() error, deleteDag func(dagID string) error, bootReconcile func() map[string]struct{}) error {
	if rerr := reload(); rerr != nil {
		devPrintf(cmd.ErrOrStderr(), "✗ %v\n", rerr)
	}
	snap := projectMtimes(workspaceWatchPaths(ws))
	// lastSeenDagIDs holds the set of DAG IDs the watcher registered in the
	// previous tick. On every tick we set-diff against the current workspace
	// scan; any DAG in lastSeen but not in current was deleted from disk and
	// must be deregistered (issue #345). Seeded from the initial reload by
	// re-resolving the workspace one more time below.
	lastSeenDagIDs := projectDagIDs(ws)
	// Boot-time self-heal (#404): deregister ghost DAGs (registered but absent on
	// disk) and clear stale import errors so a reused metadata DB or a previous
	// workspace doesn't leave un-removable entries in the UI. The closure is built
	// fail-safe by the caller; nil in tests.
	if bootReconcile != nil {
		lastSeenDagIDs = bootReconcile()
	}
	devPrintf(cmd.OutOrStdout(), "👀 watching %s; edit and save to reload (Ctrl-C to stop)\n", ws.Path)
	// Re-resolve the workspace each tick so newly-added or removed projects
	// are detected. Discovery errors (e.g. a fresh duplicate-dag_id
	// collision) surface here so the user sees them immediately.
	curWs := ws
	scan := func() (map[string]time.Time, bool) {
		resolved, derr := ResolveWorkspace(ws.Path)
		if derr != nil {
			devPrintf(cmd.ErrOrStderr(), "✗ workspace discovery: %v\n", derr)
			return nil, false
		}
		curWs = resolved
		return projectMtimes(workspaceWatchPaths(curWs)), true
	}
	onSave := func(_, _ map[string]time.Time) {
		// Set-diff lastSeen vs current: any DAG present last tick but
		// missing now was removed from disk and needs to be deregistered
		// from the control plane (issue #345). Logged to stderr so the
		// user sees what the watcher did.
		if deleteDag != nil {
			logf := func(format string, args ...any) {
				devPrintf(cmd.OutOrStdout(), format+"\n", args...)
			}
			lastSeenDagIDs = removeMissingDags(lastSeenDagIDs, projectDagIDs(curWs), deleteDag, logf)
		}
		devPrintf(cmd.OutOrStdout(), "[%s] change detected → reloading …\n", time.Now().Format("15:04:05"))
		// Time the reload: a broken DAG's reload should still finish in
		// ~1s. Anything multi-second is a red flag (parser hang, lock
		// contention with the running control plane, slow registration
		// retry). Log it so the user can grep for slow reloads when they
		// notice the UI freezing on save (#217 diagnostic).
		reloadStart := time.Now()
		rerr := reload()
		devPrintf(cmd.OutOrStdout(), "[%s] reload finished in %s\n", time.Now().Format("15:04:05"), time.Since(reloadStart).Truncate(time.Millisecond))
		if rerr != nil {
			devPrintf(cmd.ErrOrStderr(), "✗ %v\n", rerr)
		}
	}
	saveWatcher{interval: devPollInterval, scan: scan, onSave: onSave}.run(ctx, snap)
	devPrintln(cmd.OutOrStdout(), "\nstopping dev environment …")
	return nil
}

// projectDagIDs returns the set of DAG IDs the workspace currently exposes.
// It powers the watcher's "what disappeared since last tick" set-diff
// (issue #345). Returns a map so set-membership checks stay O(1) and the
// pure-function removeMissingDags can mutate it without aliasing.
func projectDagIDs(ws *WorkspaceSpec) map[string]struct{} {
	if ws == nil {
		return map[string]struct{}{}
	}
	out := make(map[string]struct{}, len(ws.Projects))
	for _, p := range ws.Projects {
		if p.Config != nil && p.Config.DagID != "" {
			out[p.Config.DagID] = struct{}{}
		}
	}
	return out
}

// workspaceWatchPaths returns the paths the mtime poller should track for a
// given workspace: every project's dexaflow.yaml (when present) and dag.py,
// PLUS the workspace root itself so a new subdir appearing nudges the mtime
// signal. Lite re-discovers projects on every reload, so a new subdir's files
// will start being watched at the next tick after it's noticed.
func workspaceWatchPaths(ws *WorkspaceSpec) []string {
	if ws == nil {
		return nil
	}
	paths := ws.WatchedPaths()
	// Workspace root mtime catches "a new subdir was just created" before any
	// of its files exist; fsnotify would be cleaner, but mtime-poll is the
	// existing primitive.
	return append(paths, ws.Path)
}

// devCompileAndRegister compiles the project (parser + overlay + guardrails),
// optionally runs afterCompile (e.g. import the built image into the cluster),
// and registers the resulting dag.json with the running control plane. Each call
// stamps a fresh dev version so a hot reload never collides with the previous
// registration (dag_versions is unique per dag_id + version).
func devCompileAndRegister(ctx context.Context, cmd *cobra.Command, dir string, opts compileOptions, token string, afterCompile func() error, uiURL string) error {
	opts.output = filepath.Join(liteDevDir(), "dag.json")
	opts.dagVersion = fmt.Sprintf("dev-%d", time.Now().UnixNano())
	opts.lite = true
	//nolint:contextcheck // runCompile derives its context from cmd; ctx here is used for registration.
	if cerr := runCompile(cmd, dir, opts); cerr != nil {
		return cerr
	}
	if afterCompile != nil {
		if aerr := afterCompile(); aerr != nil {
			return aerr
		}
	}
	data, err := os.ReadFile(opts.output) //nolint:gosec // path is leoflow-controlled under ~/.dexaflow
	if err != nil {
		return fmt.Errorf("reading compiled dag.json: %w", err)
	}
	var spec domain.DAGSpec
	if jerr := json.Unmarshal(data, &spec); jerr != nil {
		return fmt.Errorf("parsing compiled dag.json: %w", jerr)
	}
	status, body, perr := pushVersion(ctx, uiURL, token, spec.DagID, data)
	if perr != nil {
		return perr
	}
	if status >= http.StatusMultipleChoices {
		return fmt.Errorf("control plane returned %d registering %q: %s", status, spec.DagID, body)
	}
	devPrintf(cmd.OutOrStdout(), "✓ registered %q\n", spec.DagID)
	return nil
}

// devCompileAndRegisterFn is the per-project compile-and-register entry point
// used by devCompileAndRegisterAll. It is a package-level variable so tests
// can substitute a mock that records calls without spinning up the parser +
// HTTP server (the same pattern devRun / devOutput follow). Production code
// keeps the default; do NOT swap it outside _test.go.
var devCompileAndRegisterFn = devCompileAndRegister

// devCompileAndRegisterAll compiles + registers every project in the workspace.
// Each project is processed independently (one bad DAG does not stop the
// others), and per-project errors are reported through the Airflow import-
// errors UI (keyed by the project's dag.py path) so the SPA banner names the
// culprit. The pre-compile log line names the resolved config source for each
// DAG (yaml path or "auto-defaults: <subdir>") so "which config did it use?"
// is greppable.
//
// Returns nil when every project compiled OR when at least one succeeded
// (transient single-DAG failures don't block the loop). Returns the last
// error when ALL projects failed.
func devCompileAndRegisterAll(ctx context.Context, cmd *cobra.Command, ws *WorkspaceSpec, baseOpts compileOptions, token string, afterCompile func() error, uiURL string) error {
	if ws == nil || len(ws.Projects) == 0 {
		path := "<nil>"
		if ws != nil {
			path = ws.Path
		}
		devPrintf(cmd.OutOrStdout(), "▸ no DAGs in workspace %s; drop a `dag.py` into a subdirectory and save\n", path)
		return nil
	}
	var lastErr error
	successes := 0
	for _, p := range ws.Projects {
		cfgSource := p.ConfigPath
		if cfgSource == "" {
			cfgSource = "auto-defaults: " + filepath.Base(p.Path)
		}
		devPrintf(cmd.OutOrStdout(), "▸ compiling %q (config: %s)\n", p.DagID, cfgSource)
		dagPyPath := filepath.Join(p.Path, p.Config.DagSource)
		if err := devCompileAndRegisterFn(ctx, cmd, p.Path, baseOpts, token, afterCompile, uiURL); err != nil {
			devPrintf(cmd.ErrOrStderr(), "✗ %s: %v\n", p.DagID, err)
			_ = reportImportError(ctx, uiURL, token, dagPyPath, err.Error()) //nolint:errcheck // best-effort UI hint; user already sees the terminal error
			lastErr = err
			continue
		}
		_ = clearImportError(ctx, uiURL, token, dagPyPath) //nolint:errcheck // best-effort: clears the banner on a good compile
		successes++
	}
	if successes == 0 {
		return lastErr
	}
	return nil
}

// devReportingReload wraps a reload so a failed compile is published to the
// control plane as an import error (lighting the Airflow home's native "Import
// Errors" banner so the failure is visible in the UI, not only the terminal), and
// a good compile clears it. Reporting is best-effort and never masks the reload's
// own result.
//
// Multi-DAG callers should use devCompileAndRegisterAll instead, which reports
// import errors per-project. devReportingReload is the single-project path
// (cluster mode for v1).
func devReportingReload(ctx context.Context, reload func() error, serverURL, token, filename string) func() error {
	return func() error {
		if err := reload(); err != nil {
			_ = reportImportError(ctx, serverURL, token, filename, err.Error()) //nolint:errcheck // best-effort UI hint; the reload error below is authoritative
			return err
		}
		_ = clearImportError(ctx, serverURL, token, filename) //nolint:errcheck // best-effort: clears the banner on a good compile
		return nil
	}
}

// reportImportError records a failed compile as an Airflow import error so the
// UI home banner surfaces it (keyed by filename; replaces any previous error).
func reportImportError(ctx context.Context, serverURL, token, filename, stack string) error {
	body, err := json.Marshal(map[string]string{"filename": filename, "stack_trace": stack, "bundle_name": "leoflow"})
	if err != nil {
		return fmt.Errorf("encoding import error: %w", err)
	}
	return devImportErrorRequest(ctx, http.MethodPut, strings.TrimRight(serverURL, "/")+"/api/v2/importErrors", token, body)
}

// clearImportError removes any recorded import error for a file (a good re-import).
func clearImportError(ctx context.Context, serverURL, token, filename string) error {
	u := strings.TrimRight(serverURL, "/") + "/api/v2/importErrors?filename=" + url.QueryEscape(filename)
	return devImportErrorRequest(ctx, http.MethodDelete, u, token, nil)
}

// devImportErrorRequest issues an authenticated import-error write to the control plane.
func devImportErrorRequest(ctx context.Context, method, reqURL, token string, body []byte) error {
	var r io.Reader
	if body != nil {
		r = strings.NewReader(string(body))
	}
	req, err := http.NewRequestWithContext(ctx, method, reqURL, r)
	if err != nil {
		return fmt.Errorf("building request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := cliHTTPClient(10 * time.Second).Do(req)
	if err != nil {
		return fmt.Errorf("calling %s: %w", reqURL, err)
	}
	defer func() { _ = resp.Body.Close() }() //nolint:errcheck // response body discarded
	if resp.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("import error endpoint returned %d", resp.StatusCode)
	}
	return nil
}
